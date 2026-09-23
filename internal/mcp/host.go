package mcp

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"forcefield/internal/process"
	"forcefield/internal/redact"
	"forcefield/internal/tools"
)

// MCP server Host: the lifecycle owner for local stdio MCP servers.
//
// Ownership hierarchy (strict):
//
//	Host
//	 ├── process (exec.Cmd, owned end to end: spawn, wait, reap)
//	 ├── streams (stdin/stdout/stderr pipes created before Start)
//	 ├── Transport (wraps stdout/stdin; never closes them)
//	 ├── Client (protocol state; closes the transport, never the streams)
//	 └── Tool adapters (share the client; own no lifecycle)
//
// Security boundary (read before extending this file):
//
// MCP subprocesses are external and UNTRUSTED. They execute with the OS
// privileges of the Forcefield process. They are NOT Forcefield
// sandboxed, they receive NO BoundaryChecker enforcement, and they may
// act outside the workspace. The workspace directory only selects the
// child's launch directory; it confines nothing. Do not claim otherwise.
//
// This phase stops at the Host: no runtime wiring, no registry
// integration, no persisted status, no reconnect or respawn. A dead
// server stays dead; Phase 6 decides warn-and-continue from Snapshot.

// Timing budgets. Vars (not consts) so tests can shrink them with
// save/restore; production defaults keep shutdown bounded without
// hurrying graceful servers.
var (
	// hostStartupBudget caps a whole Host.Start across all servers.
	hostStartupBudget = 60 * time.Second
	// hostShutdownGrace is the graceful-exit wait after stdin close
	// before escalation.
	hostShutdownGrace = 5 * time.Second
	// hostTerminateWait bounds each escalation wait (after Terminate,
	// after Kill) and reader/pump join waits.
	hostTerminateWait = 2 * time.Second
)

// maxStderrBytes caps the per-server stderr ring. Only the most recent
// bytes are kept: enough for crash diagnostics, too small for log
// flooding or exfiltration staging. The ring never blocks the stdout
// transport because a dedicated pump drains stderr concurrently.
const maxStderrBytes = 32 << 10

// serverState is one managed server's lifecycle position. Transitions run
// one way toward closed; failed and closed are terminal and never leave.
type serverState int

const (
	stateNew serverState = iota + 1
	stateStarting
	stateReady
	stateFailed
	stateClosing
	stateClosed
)

func (s serverState) String() string {
	switch s {
	case stateNew:
		return "new"
	case stateStarting:
		return "starting"
	case stateReady:
		return "ready"
	case stateFailed:
		return "failed"
	case stateClosing:
		return "closing"
	case stateClosed:
		return "closed"
	default:
		return "unknown"
	}
}

// ServerSnapshot is one server's read-only, bounded status for future
// Runtime integration. It carries copies and capped strings only: no
// mutable maps, no process handles, no raw environment, and stderr only
// as a scrubbed tail.
type ServerSnapshot struct {
	Key       string
	Enabled   bool
	Skipped   bool // disabled in config: never spawned
	Started   bool // process spawned at least once (stays true after crash)
	Ready     bool // adapters usable right now
	Healthy   bool // ready, process unreaped, transport healthy
	Dir       string
	Tools     []*Tool
	LastError string // bounded, scrubbed; "" when healthy
	Exited    bool
	ExitCode  int
	Stderr    string // bounded, scrubbed tail
}

// HostSnapshot aggregates every known server plus the cross-server
// duplicate qualified names Phase 6 must resolve. Duplicates are reported,
// never silently merged: the authoritative drop-both policy belongs to the
// future Runtime/registry integration, which sees the full identities here.
type HostSnapshot struct {
	Servers    []ServerSnapshot // sorted by key
	Duplicates []string         // qualified names claimed by >1 ready server, sorted
}

// server is one managed MCP server process and its stack.
type server struct {
	key string
	cfg ServerConfig

	mu      sync.Mutex
	state   serverState
	lastErr string // bounded, scrubbed

	cmd     *exec.Cmd
	release process.ReleaseFunc
	stdin   io.WriteCloser
	stdoutR io.ReadCloser
	stderrR io.ReadCloser

	transport *Transport
	client    *Client
	adapters  []*Tool

	stderrMu sync.Mutex
	stderr   []byte // ring tail, raw; scrubbed on read

	runCancel context.CancelFunc // aborts in-flight startup on Close

	exited   bool
	exitCode int
	exitedCh chan struct{} // closed once, when Wait returns
	pumpDone chan struct{} // closed once, when the stderr pump exits
	dir      string
	started  bool
}

// Host manages one Config's MCP servers. It is safe for concurrent
// snapshots and Close; Start runs once.
type Host struct {
	mu        sync.Mutex
	servers   map[string]*server
	order     []string // sorted keys, deterministic
	workspace string
	closed    bool
}

// New validates cfg (pure shape checks, no I/O beyond the workspace) and
// builds dormant server records. It spawns nothing: Start does that.
// workspace anchors default working directories and must be an existing
// absolute directory.
func New(cfg Config, workspace string) (*Host, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(workspace) {
		return nil, fmt.Errorf("mcp: workspace %q must be absolute: %w", quoteBounded(workspace), ErrInvalidConfig)
	}
	info, err := os.Stat(workspace)
	if err != nil {
		return nil, fmt.Errorf("mcp: workspace %q: %v: %w", quoteBounded(workspace), err, ErrInvalidConfig)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("mcp: workspace %q is not a directory: %w", quoteBounded(workspace), ErrInvalidConfig)
	}
	h := &Host{servers: make(map[string]*server), workspace: workspace}
	for key, scfg := range cfg.Servers {
		h.servers[key] = &server{key: key, cfg: scfg, state: stateNew}
		h.order = append(h.order, key)
	}
	sort.Strings(h.order)
	return h, nil
}

// StartError names every server that failed to reach ready. The Host keeps
// successfully started servers running: the future Runtime layer applies
// warn-and-continue from Snapshot, and doctor-style callers treat this
// error as the failure signal.
type StartError struct {
	Failed []string // sorted server keys
}

func (e *StartError) Error() string {
	return fmt.Sprintf("mcp: %d server(s) failed to start: %s", len(e.Failed), strings.Join(e.Failed, ", "))
}

// Start spawns and initializes every enabled server sequentially in
// sorted key order under an overall budget, then returns nil only when
// all of them are ready. Disabled servers are recorded skipped and never
// spawn. A failed server is fully cleaned up (no orphan process) while
// unrelated servers keep running.
func (h *Host) Start(ctx context.Context) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return fmt.Errorf("mcp: start on closed host: %w", ErrClosed)
	}
	h.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, hostStartupBudget)
	defer cancel()
	var failed []string
	for _, key := range h.order {
		s := h.servers[key]
		if !s.cfg.IsEnabled() {
			continue
		}
		if err := s.start(ctx, h.workspace); err != nil {
			failed = append(failed, key)
		}
	}
	if len(failed) > 0 {
		sort.Strings(failed)
		return &StartError{Failed: failed}
	}
	return nil
}

// Close shuts every server down idempotently: graceful stdin EOF, bounded
// grace, tree termination on escalation, reap, stream release, transport
// drain. It never fails: teardown errors are swallowed after bounded
// attempts because there is nothing useful left to report them to.
func (h *Host) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	servers := make([]*server, 0, len(h.order))
	for _, key := range h.order {
		servers = append(servers, h.servers[key])
	}
	h.mu.Unlock()
	for _, s := range servers {
		s.shutdown(false)
	}
	return nil
}

// Snapshot copies every server's bounded status plus cross-server
// duplicate qualified names. Adapters are shared pointers to immutable
// tools; the slices are fresh per call.
func (h *Host) Snapshot() HostSnapshot {
	h.mu.Lock()
	servers := make([]*server, 0, len(h.order))
	for _, key := range h.order {
		servers = append(servers, h.servers[key])
	}
	h.mu.Unlock()
	snap := HostSnapshot{}
	claims := make(map[string][]string)
	for _, s := range servers {
		ss := s.snapshot()
		snap.Servers = append(snap.Servers, ss)
		if ss.Ready {
			names := make([]string, 0, len(ss.Tools))
			for _, t := range ss.Tools {
				names = append(names, t.Name())
			}
			for _, n := range names {
				claims[n] = append(claims[n], ss.Key)
			}
		}
	}
	snap.Duplicates = findDuplicateQualified(claims)
	return snap
}

// findDuplicateQualified reports qualified names claimed by more than one
// server, sorted. Within one Host this is structurally unexpected because
// qualified names embed the unique server key and discovery dedupes
// intra-server collisions; the check stays as defense-in-depth so an
// ambiguous set can never present as clean to the future registry layer.
func findDuplicateQualified(claims map[string][]string) []string {
	var out []string
	for name, keys := range claims {
		seen := make(map[string]struct{}, len(keys))
		distinct := 0
		for _, k := range keys {
			if _, ok := seen[k]; !ok {
				seen[k] = struct{}{}
				distinct++
			}
		}
		if distinct > 1 {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Tools flattens every ready server's adapters in server-key order for
// future registry consumption. Duplicates are included as-is: collision
// policy belongs to Runtime (see Snapshot.Duplicates), never to silent
// filtering here.
func (h *Host) Tools() []*Tool {
	snap := h.Snapshot()
	var out []*Tool
	for _, ss := range snap.Servers {
		out = append(out, ss.Tools...)
	}
	return out
}

// start runs one server's full startup sequence under a per-server
// timeout derived from its configuration. Any failure cleans up fully
// (process reaped, streams and transport closed, partial tools dropped)
// and records a bounded, scrubbed diagnostic.
func (s *server) start(parent context.Context, workspace string) error {
	s.mu.Lock()
	if s.state != stateNew {
		s.mu.Unlock()
		return fmt.Errorf("mcp: server %q start in state %s: %w", s.key, s.state, ErrProtocol)
	}
	s.state = stateStarting
	s.mu.Unlock()

	fail := func(err error) error {
		s.recordError(err)
		s.shutdown(true)
		s.mu.Lock()
		// A concurrent Close wins over failure bookkeeping: shutdown
		// stays the reported state, mirroring failLocked.
		if s.state != stateClosing && s.state != stateClosed {
			s.state = stateFailed
		}
		s.mu.Unlock()
		return err
	}

	timeout := serverTimeout(s.cfg)
	ctx, cancel := context.WithTimeout(parent, timeout)
	s.mu.Lock()
	s.runCancel = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.runCancel = nil
		s.mu.Unlock()
		cancel()
	}()

	if err := s.checkAborted(); err != nil {
		return fail(err)
	}
	dir, err := s.resolveDir(workspace)
	if err != nil {
		return fail(err)
	}
	exe, err := resolveExecutable(s.key, s.cfg.Command, dir, s.cfg)
	if err != nil {
		return fail(err)
	}
	env := buildEnv(s.cfg)
	registerSecrets(s.cfg)
	if err := s.spawn(exe, dir, env); err != nil {
		return fail(err)
	}
	s.mu.Lock()
	s.started = true
	s.mu.Unlock()
	if err := s.checkAborted(); err != nil {
		return fail(err)
	}
	tr, err := NewTransport(s.stdoutR, s.stdin)
	if err != nil {
		return fail(err)
	}
	s.mu.Lock()
	s.transport = tr
	s.mu.Unlock()
	cl, err := NewClient(tr, s.key, DefaultClientInfo())
	if err != nil {
		return fail(err)
	}
	s.mu.Lock()
	s.client = cl
	s.mu.Unlock()
	if err := cl.Initialize(ctx); err != nil {
		return fail(fmt.Errorf("mcp: server %q initialize: %w", s.key, err))
	}
	if err := s.checkAborted(); err != nil {
		return fail(err)
	}
	if err := cl.Discover(ctx); err != nil {
		return fail(fmt.Errorf("mcp: server %q discover: %w", s.key, err))
	}
	defs, err := cl.Tools()
	if err != nil {
		return fail(fmt.Errorf("mcp: server %q tools: %w", s.key, err))
	}
	adapters := make([]*Tool, 0, len(defs))
	for _, def := range defs {
		t, err := NewTool(cl, def)
		if err != nil {
			return fail(fmt.Errorf("mcp: server %q adapter: %w", s.key, err))
		}
		t.SetLimits(tools.Limits{Timeout: timeout})
		adapters = append(adapters, t)
	}
	s.mu.Lock()
	s.adapters = adapters
	s.dir = dir
	s.state = stateReady
	s.lastErr = ""
	s.mu.Unlock()
	return nil
}

// checkAborted reports Close/interrupt during startup so a closing Host
// never promotes a half-started server to ready.
func (s *server) checkAborted() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == stateClosing || s.state == stateClosed {
		return fmt.Errorf("mcp: server %q startup aborted: closing: %w", s.key, ErrClosed)
	}
	return nil
}

// recordError stores a bounded, scrubbed diagnostic without secret or
// payload content.
func (s *server) recordError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastErr = BoundedDetail(redact.Scrub(err.Error()))
}

// serverTimeout resolves one server's whole-startup budget: the
// configured per-server timeout, or the default when unset. Validation
// guarantees the range 0..300s finite, so no further checks are needed.
// The single deadline covers spawn, initialize, discovery, and adapter
// construction: no phase gets a separate unlimited timeout, and writes
// can never block past it because every transport wait honors ctx.
func serverTimeout(cfg ServerConfig) time.Duration {
	if cfg.TimeoutSeconds > 0 {
		return time.Duration(cfg.TimeoutSeconds * float64(time.Second))
	}
	return time.Duration(DefaultServerTimeoutSeconds * float64(time.Second))
}

// resolveDir validates the launch directory: empty selects the workspace
// root; anything else must exist, be a directory, and canonicalizes via
// symlinks. There is no fallback directory: invalid means startup fails
// before anything spawns.
func (s *server) resolveDir(workspace string) (string, error) {
	raw := s.cfg.Cwd
	if strings.TrimSpace(raw) == "" {
		return workspace, nil
	}
	abs := raw
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(workspace, abs)
	}
	clean := filepath.Clean(abs)
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", fmt.Errorf("mcp: server %q cwd %q: %v: %w", s.key, quoteBounded(raw), err, ErrInvalidConfig)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("mcp: server %q cwd %q: %v: %w", s.key, quoteBounded(raw), err, ErrInvalidConfig)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("mcp: server %q cwd %q is not a directory: %w", s.key, quoteBounded(raw), ErrInvalidConfig)
	}
	return resolved, nil
}

// isPathSpell reports whether cmd names a path (absolute or containing a
// separator) rather than a bare executable name for PATH lookup.
func isPathSpell(cmd string) bool {
	if filepath.IsAbs(cmd) {
		return true
	}
	if strings.ContainsRune(cmd, '/') {
		return true
	}
	if runtime.GOOS == "windows" && strings.ContainsRune(cmd, '\\') {
		return true
	}
	return false
}

// resolveExecutable finds cmd as a file without consulting the parent
// process environment. serverKey attributes diagnostics; dir is the
// already-validated launch directory anchoring relative path spellings.
// Bare names search only the constructed minimal environment's PATH (never
// the parent process PATH): without an explicit passthrough or absolute
// path, lookup fails with an actionable message instead of silently
// resolving against unexpected host state.
func resolveExecutable(serverKey, cmd, dir string, cfg ServerConfig) (string, error) {
	if isPathSpell(cmd) {
		p := cmd
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		if err := checkExecutable(p); err != nil {
			return "", fmt.Errorf("mcp: server %q command %q: %v: %w", serverKey, quoteBounded(cmd), err, ErrInvalidConfig)
		}
		return p, nil
	}
	pathVal := ""
	for _, e := range buildEnv(cfg) {
		if name, val, ok := strings.Cut(e, "="); ok && envNameEq(name, "PATH") {
			pathVal = val
			break
		}
	}
	if strings.TrimSpace(pathVal) == "" {
		return "", fmt.Errorf("mcp: command %q not found: no PATH in the constructed minimal environment (use an absolute command path or add \"PATH\" to env_passthrough): %w", quoteBounded(cmd), ErrInvalidConfig)
	}
	for _, d := range filepath.SplitList(pathVal) {
		if strings.TrimSpace(d) == "" {
			continue
		}
		for _, cand := range candidateNames(d, cmd) {
			if checkExecutable(cand) == nil {
				return cand, nil
			}
		}
	}
	return "", fmt.Errorf("mcp: command %q not found on the constructed PATH: %w", quoteBounded(cmd), ErrInvalidConfig)
}

// candidateNames yields the executable spellings for dir/name: the bare
// join on Unix, plus PATHEXT expansions on Windows (mirroring LookPath
// semantics without inheriting the parent environment).
func candidateNames(dir, name string) []string {
	joined := filepath.Join(dir, name)
	if runtime.GOOS != "windows" {
		return []string{joined}
	}
	if strings.ContainsRune(name, '.') {
		return []string{joined}
	}
	exts := []string{".com", ".exe", ".bat", ".cmd"}
	if pathext, ok := os.LookupEnv("PATHEXT"); ok && strings.TrimSpace(pathext) != "" {
		// Parent PATHEXT only supplies extensions, never directories:
		// no environment content reaches the filesystem search.
		var parsed []string
		for _, e := range strings.Split(pathext, ";") {
			e = strings.TrimSpace(e)
			if e != "" && strings.HasPrefix(e, ".") {
				parsed = append(parsed, e)
			}
		}
		if len(parsed) > 0 {
			exts = parsed
		}
	}
	out := []string{joined}
	for _, e := range exts {
		out = append(out, joined+e)
	}
	return out
}

// checkExecutable verifies p names an existing non-directory file,
// executable by permission on Unix (Windows has no exec bit; existence
// governs, and spawn errors surface at Start with diagnostics).
func checkExecutable(p string) error {
	info, err := os.Stat(p)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("is a directory")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("not executable")
	}
	return nil
}

// spawn creates the process with argv directly (never a shell), attaches
// pipes, starts it, and launches the exit watcher plus the stderr pump.
// argv entries stay distinct: Command takes the resolved path and Args
// verbatim, so metacharacters are data to the child, never interpreted.
func (s *server) spawn(exe, dir string, env []string) error {
	cmd := exec.Command(exe, s.cfg.Args...)
	// Plain Command, not CommandContext: automatic cancellation would race
	// the explicit tree-termination lifecycle below (which kills the whole
	// group/job, not just the direct child).
	process.Configure(cmd)
	cmd.Dir = dir
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("mcp: server %q stdin pipe: %v: %w", s.key, err, ErrTransport)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("mcp: server %q stdout pipe: %v: %w", s.key, err, ErrTransport)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("mcp: server %q stderr pipe: %v: %w", s.key, err, ErrTransport)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("mcp: server %q start: %v: %w", s.key, scrubbedStartError(err), ErrTransport)
	}
	s.mu.Lock()
	s.cmd = cmd
	s.release = process.Track(cmd)
	s.stdin = stdin
	s.stdoutR = stdout
	s.stderrR = stderr
	s.exitedCh = make(chan struct{})
	s.pumpDone = make(chan struct{})
	s.mu.Unlock()
	go s.watch()
	go s.pumpStderr()
	return nil
}

// scrubbedStartError bounds spawn failure text. Paths and system errors
// are operator configuration, not remote input, but the bound keeps
// diagnostics predictable.
func scrubbedStartError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s", quoteBounded(err.Error()))
}

// watch owns process reaping: exactly one Wait per started process. On
// unexpected exit it records bounded diagnostics (exit code plus scrubbed
// stderr tail) and marks the server failed. No respawn, no retry, no
// re-initialization, ever.
func (s *server) watch() {
	err := s.cmd.Wait()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			code = -1
		}
	}
	s.mu.Lock()
	s.exited = true
	s.exitCode = code
	closing := s.state == stateClosing || s.state == stateClosed
	if !closing && s.state != stateFailed {
		s.state = stateFailed
		if s.lastErr == "" {
			s.lastErr = BoundedDetail(fmt.Sprintf("process exited with code %d", code))
			if tail := strings.TrimSpace(redact.Scrub(s.stderrTailLocked())); tail != "" {
				s.lastErr = BoundedDetail(fmt.Sprintf("process exited with code %d: %s", code, tail))
			}
		}
	}
	close(s.exitedCh)
	s.mu.Unlock()
}

// StderrTail returns the scrubbed tail of captured stderr.
func (s *server) StderrTail() string {
	return redact.Scrub(s.stderrTailLocked())
}

// stderrTailLocked copies the raw ring. Callers needing the scrubbed form
// use StderrTail.
func (s *server) stderrTailLocked() string {
	s.stderrMu.Lock()
	defer s.stderrMu.Unlock()
	return string(s.stderr)
}

// pumpStderr drains the stderr pipe into the bounded ring until EOF or
// Close. It is the reason a chatty server can never wedge the stdout
// transport by filling the stderr pipe buffer.
func (s *server) pumpStderr() {
	defer close(s.pumpDone)
	buf := make([]byte, 4096)
	for {
		s.mu.Lock()
		r := s.stderrR
		s.mu.Unlock()
		if r == nil {
			return
		}
		n, err := r.Read(buf)
		if n > 0 {
			s.stderrMu.Lock()
			s.stderr = append(s.stderr, buf[:n]...)
			if len(s.stderr) > maxStderrBytes {
				s.stderr = append([]byte(nil), s.stderr[len(s.stderr)-maxStderrBytes:]...)
			}
			s.stderrMu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// shutdown runs the deterministic teardown sequence shared by normal
// Close and startup-failure cleanup: fail pending work, invite graceful
// exit via stdin EOF, escalate through tree termination on a bounded
// schedule, reap, release tracking, drain streams, and join machinery.
// failed selects the terminal state and preserves the recorded cause.
func (s *server) shutdown(failed bool) {
	s.mu.Lock()
	if s.state == stateClosed || s.state == stateClosing {
		s.mu.Unlock()
		return
	}
	if s.state == stateNew {
		// Never spawned: nothing to tear down.
		if failed {
			s.state = stateFailed
		} else {
			s.state = stateClosed
		}
		s.mu.Unlock()
		return
	}
	s.state = stateClosing
	if s.runCancel != nil {
		s.runCancel()
	}
	cl := s.client
	s.mu.Unlock()

	// Fail in-flight tool calls first so nothing hangs on dying pipes.
	if cl != nil {
		_ = cl.Close()
	}

	s.mu.Lock()
	stdin := s.stdin
	s.mu.Unlock()
	if stdin != nil {
		// Graceful invitation: stdio servers conventionally exit on EOF.
		_ = stdin.Close()
	}

	if !s.waitExited(hostShutdownGrace) {
		s.mu.Lock()
		cmd := s.cmd
		s.mu.Unlock()
		_ = process.Terminate(cmd)
		if !s.waitExited(hostTerminateWait) {
			_ = process.Kill(cmd)
			s.waitExited(hostTerminateWait)
		}
	}

	s.mu.Lock()
	release := s.release
	s.release = nil
	stdoutR := s.stdoutR
	stderrR := s.stderrR
	s.stdoutR = nil
	s.stderrR = nil
	tr := s.transport
	s.mu.Unlock()
	if release != nil {
		release()
	}
	// Unblock the transport reader and stderr pump; both exit promptly on
	// closed pipes, and both joins are bounded below.
	if stdoutR != nil {
		_ = stdoutR.Close()
	}
	if stderrR != nil {
		_ = stderrR.Close()
	}
	if tr != nil {
		waitChan(tr.Done(), hostTerminateWait)
	}
	s.mu.Lock()
	pumpDone := s.pumpDone
	s.mu.Unlock()
	if pumpDone != nil {
		waitChan(pumpDone, hostTerminateWait)
	}

	s.mu.Lock()
	if failed {
		s.state = stateFailed
	} else {
		s.state = stateClosed
	}
	s.mu.Unlock()
}

// waitExited reports whether the exit watcher reaped the process within d.
func (s *server) waitExited(d time.Duration) bool {
	s.mu.Lock()
	ch := s.exitedCh
	s.mu.Unlock()
	if ch == nil {
		return true
	}
	select {
	case <-ch:
		return true
	case <-time.After(d):
		return false
	}
}

// waitChan waits for ch up to d, then gives up without failing: teardown
// is best-effort past this point, never hanging Close forever.
func waitChan(ch <-chan struct{}, d time.Duration) {
	if ch == nil {
		return
	}
	select {
	case <-ch:
	case <-time.After(d):
	}
}

// snapshot copies one server's bounded status. Adapters are shared
// immutable pointers; the slice is fresh. Stderr and errors are scrubbed.
func (s *server) snapshot() ServerSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := ServerSnapshot{
		Key:       s.key,
		Enabled:   s.cfg.IsEnabled(),
		Skipped:   !s.cfg.IsEnabled(),
		Started:   s.started,
		Ready:     s.state == stateReady,
		Dir:       s.dir,
		LastError: s.lastErr,
		Exited:    s.exited,
		ExitCode:  s.exitCode,
	}
	if s.state == stateReady {
		ss.Tools = append([]*Tool(nil), s.adapters...)
		ss.Healthy = !s.exited && s.transport != nil && s.transport.Healthy() && s.client.Ready()
		if !ss.Healthy && ss.LastError == "" {
			ss.LastError = "server unhealthy"
		}
	}
	s.stderrMu.Lock()
	tail := string(s.stderr)
	s.stderrMu.Unlock()
	ss.Stderr = redact.Scrub(tail)
	return ss
}

// buildEnv constructs the child environment explicitly. Precedence:
//
//	minimal platform baseline
//	  → allowed passthrough copies (config order, absent skipped)
//	  → explicit server env (wins on key collision; map order sorted)
//
// Nothing is inherited wholesale: os.Environ is never consulted except
// for the individual passthrough names and, on Windows, the SystemRoot
// baseline. Values are literal: no expansion of $VAR, %VAR%, or command
// substitutions happens anywhere on this path.
func buildEnv(cfg ServerConfig) []string {
	var env []string
	if runtime.GOOS == "windows" {
		// Minimum required Windows execution environment: process
		// creation and DLL resolution need the system root. The value is
		// never logged; only the key name appears in diagnostics.
		if v, ok := os.LookupEnv("SystemRoot"); ok {
			env = append(env, "SystemRoot="+v)
		}
	}
	seen := make(map[string]struct{})
	for _, name := range cfg.EnvPassthrough {
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	keys := make([]string, 0, len(cfg.Env))
	for k := range cfg.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := cfg.Env[k]
		replaced := false
		for i, e := range env {
			if name, _, ok := strings.Cut(e, "="); ok && envNameEq(name, k) {
				env[i] = k + "=" + v
				replaced = true
				break
			}
		}
		if !replaced {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// envNameEq compares variable names: exact on Unix, case-insensitive on
// Windows where the OS itself folds names.
func envNameEq(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// registerSecrets registers explicitly configured secret-looking values
// with the process redaction registry so accidental echoes in errors or
// diagnostics scrub. Passthrough values are not registered: their
// sensitivity is unknown to us. Values below the registry's length floor
// are ignored by AddSecret itself. Never logs values.
func registerSecrets(cfg ServerConfig) {
	keys := make([]string, 0, len(cfg.Env))
	for k := range cfg.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if SecretLookingKey(k) {
			redact.AddSecret(cfg.Env[k])
		}
	}
}
