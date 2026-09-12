package runtime

import (
	"context"
	"fmt"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"forcefield/internal/permissions"
	"forcefield/internal/providers"
	"forcefield/internal/redact"
	"forcefield/internal/sandbox"
	"forcefield/internal/session"
	"forcefield/internal/tools"
	"forcefield/internal/tools/filesystem"
)

// SchedulerConfig controls how the scheduler runs a batch of tool calls.
type SchedulerConfig struct {
	// MaxConcurrency caps how many tool calls run at once. Values <= 0 are
	// treated as 1 (fully sequential).
	MaxConcurrency int
	// MaxRetries is how many additional attempts a retryable failure gets,
	// on top of the first attempt.
	MaxRetries int
	// BaseBackoff is the delay before the first retry; each subsequent
	// retry doubles it (exponential backoff).
	BaseBackoff time.Duration
}

// DefaultSchedulerConfig is used when the runtime isn't configured
// otherwise: up to 4 tools in flight at once, 2 retries for transient
// failures, starting at a 200ms backoff.
var DefaultSchedulerConfig = SchedulerConfig{
	MaxConcurrency: 4,
	MaxRetries:     2,
	BaseBackoff:    200 * time.Millisecond,
}

// scheduler runs independent tool calls concurrently and centralizes
// permission checks.
type scheduler struct {
	// managerMu guards manager so SetAgent can swap the filtered tool set
	// without racing an in-flight Run. Run captures the manager under
	// RLock and executes against that snapshot.
	managerMu   sync.RWMutex
	manager     *tools.Manager
	permissions *permissions.Manager
	asker       permissions.Asker
	cfg         SchedulerConfig

	// askMu serializes concurrent Ask prompts so the single TUI modal is
	// never overwritten. Without it, 4 concurrent tool calls each needing
	// approval would race on the single permissionPrompt channel.
	askMu sync.Mutex

	// sessionAllow holds session-scoped Always allow/deny decisions.
	// It is not persisted to config.yaml; it lives only for the lifetime
	// of this scheduler (one ff run / one Runtime). This prevents a single
	// benign "Always allow shell" from permanently widening the tool's
	// scope globally.
	sessionMu    sync.Mutex
	sessionAllow map[string]permissions.Decision
}

func newScheduler(manager *tools.Manager, perms *permissions.Manager, asker permissions.Asker, cfg SchedulerConfig) *scheduler {
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 1
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	if cfg.BaseBackoff <= 0 {
		cfg.BaseBackoff = 200 * time.Millisecond
	}
	return &scheduler{
		manager:      manager,
		permissions:  perms,
		asker:        asker,
		cfg:          cfg,
		sessionAllow: make(map[string]permissions.Decision),
	}
}

// getManager returns the current filtered tool manager under RLock.
func (s *scheduler) getManager() *tools.Manager {
	if s == nil {
		return nil
	}
	s.managerMu.RLock()
	defer s.managerMu.RUnlock()
	return s.manager
}

// setManager swaps the filtered tool manager under Lock.
func (s *scheduler) setManager(m *tools.Manager) {
	if s == nil {
		return
	}
	s.managerMu.Lock()
	s.manager = m
	s.managerMu.Unlock()
}

// getAsker returns the current permission asker under RLock.
func (s *scheduler) getAsker() permissions.Asker {
	if s == nil {
		return nil
	}
	s.managerMu.RLock()
	defer s.managerMu.RUnlock()
	return s.asker
}

// setAsker swaps the permission asker under Lock.
func (s *scheduler) setAsker(a permissions.Asker) {
	if s == nil {
		return
	}
	s.managerMu.Lock()
	s.asker = a
	s.managerMu.Unlock()
}

// Run executes calls concurrently and returns results in call order.
func (s *scheduler) Run(ctx context.Context, calls []providers.ToolCall, emit func(Event) bool) []ToolResult {
	return s.RunWithManager(ctx, calls, emit, s.getManager())
}

// RunWithManager executes calls against an explicit manager snapshot so a
// runtime agent switch mid-run cannot swap the tool set underneath an
// in-flight batch. Callers that already hold a consistent run snapshot
// should prefer this over Run.
func (s *scheduler) RunWithManager(ctx context.Context, calls []providers.ToolCall, emit func(Event) bool, manager *tools.Manager) []ToolResult {
	return s.runWithConcurrency(ctx, calls, emit, manager, s.cfg.MaxConcurrency)
}

// runWithConcurrency is RunWithManager with an explicit worker cap. The
// runtime lowers it to 1 for providers that report no parallel tool
// support; values <= 0 also mean sequential.
func (s *scheduler) runWithConcurrency(ctx context.Context, calls []providers.ToolCall, emit func(Event) bool, manager *tools.Manager, maxConcurrency int) []ToolResult {
	results := make([]ToolResult, len(calls))
	if len(calls) == 0 {
		return results
	}
	if maxConcurrency <= 0 {
		maxConcurrency = 1
	}

	var emitMu sync.Mutex
	safeEmit := func(e Event) bool {
		emitMu.Lock()
		defer emitMu.Unlock()
		return emit(e)
	}

	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup
	var stopped atomic.Bool

	for i, call := range calls {
		i, call := i, call

		wg.Add(1)
		go func() {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] = s.cancelledResult(call)
				return
			}
			defer func() { <-sem }()

			if stopped.Load() || ctx.Err() != nil {
				results[i] = s.cancelledResult(call)
				return
			}

			result := s.runOneWithManager(ctx, call, safeEmit, manager)
			results[i] = result
			if ctx.Err() != nil {
				stopped.Store(true)
			}
		}()
	}

	wg.Wait()
	return results
}

// runOneWithManager executes one tool call against an explicit manager
// snapshot. See RunWithManager for why the snapshot matters.
func (s *scheduler) runOneWithManager(ctx context.Context, call providers.ToolCall, emit func(Event) bool, manager *tools.Manager) ToolResult {
	started := time.Now()
	emit(Event{Type: EventToolStart, ToolCall: &call})

	if manager == nil {
		result := ToolResult{
			ToolCallID: call.ID,
			Name:       call.Name,
			Arguments:  call.Arguments,
			Success:    false,
			IsError:    true,
			Content:    "tool not found: " + call.Name,
			Duration:   time.Since(started),
			Attempt:    1,
			Err:        tools.ErrNotFound,
		}
		emit(Event{Type: EventToolFailed, ToolResult: &result})
		return result
	}
	tool, ok := manager.Lookup(call.Name)
	if !ok {
		result := ToolResult{
			ToolCallID: call.ID,
			Name:       call.Name,
			Arguments:  call.Arguments,
			Success:    false,
			IsError:    true,
			Content:    "tool not found: " + call.Name,
			Duration:   time.Since(started),
			Attempt:    1,
			Err:        tools.ErrNotFound,
		}
		emit(Event{Type: EventToolFailed, ToolResult: &result})
		return result
	}

	// Strict validation before permission so the prompt cannot hide intent
	// behind a lenient default (e.g. {"path":12345} silently becoming ".").
	if err := tools.ValidateArgs(tools.Definition{Name: tool.Name(), InputSchema: tool.InputSchema()}, call.Arguments); err != nil {
		result := ToolResult{
			ToolCallID: call.ID,
			Name:       call.Name,
			Arguments:  call.Arguments,
			Success:    false,
			IsError:    true,
			Content:    err.Error(),
			Duration:   time.Since(started),
			Attempt:    1,
			Err:        err,
		}
		emit(Event{Type: EventToolFailed, ToolResult: &result})
		return result
	}

	if denied, result := s.checkPermissionWithManager(ctx, call, emit, manager); denied {
		return *result
	}

	meta := tools.MetadataOf(tool)

	onChunk := func(chunk tools.StreamChunk) {
		// Live output reaches the transcript as it streams, bypassing
		// the result scrub below: redact here so shell progress can
		// never display a secret the final result would have hidden.
		chunk.Data = redact.Scrub(chunk.Data)
		emit(Event{Type: EventToolProgress, ToolProgress: &ToolProgress{
			ToolCallID: call.ID,
			Name:       call.Name,
			Stream:     chunk.Stream,
			Data:       chunk.Data,
		}})
	}

	maxAttempts := 1
	if meta.Retryable {
		maxAttempts += s.cfg.MaxRetries
	}

	var last tools.Result
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Every attempt runs under the resolved tool timeout, always
		// clamped to the hard MaxTimeout ceiling.
		callCtx, cancel := context.WithTimeout(ctx, effectiveTimeout(tool, meta))

		res, err := execute(callCtx, tool, call.Arguments, onChunk)
		cancel()
		last, lastErr = res, err

		if ctx.Err() != nil {
			// Parent cancellation (Ctrl+C, whole-run timeout): stop
			// retrying and report cancellation, not failure.
			result := ToolResult{
				ToolCallID: call.ID,
				Name:       call.Name,
				Arguments:  call.Arguments,
				Success:    false,
				IsError:    true,
				Content:    "tool execution cancelled",
				Duration:   time.Since(started),
				Attempt:    attempt,
				Err:        ctx.Err(),
			}
			emit(Event{Type: EventToolCancelled, ToolResult: &result})
			return result
		}

		if err == nil {
			// Tool ran to completion; res.IsError (a tool-reported,
			// deterministic failure, e.g. "file not found") is never
			// retried even if the tool is marked retryable. Scrub secrets
			// before emitting so the TUI and session never display raw keys.
			content := session.ScrubContent(res.Content)
			stdout := session.ScrubContent(res.Stdout)
			stderr := session.ScrubContent(res.Stderr)
			result := ToolResult{
				ToolCallID:  call.ID,
				Name:        call.Name,
				Arguments:   call.Arguments,
				Content:     content,
				IsError:     res.IsError,
				Success:     !res.IsError,
				Duration:    time.Since(started),
				Attempt:     attempt,
				Stdout:      stdout,
				Stderr:      stderr,
				ExitCode:    res.ExitCode,
				HasExitCode: res.ExitCode != 0 || res.Tool == "shell",
			}
			if res.IsError {
				emit(Event{Type: EventToolFailed, ToolResult: &result})
			} else {
				emit(Event{Type: EventToolFinish, ToolResult: &result})
			}
			return result
		}

		// err != nil: an execution-level failure (process couldn't start,
		// transient I/O error, etc). Retry only if the tool opted in and
		// attempts remain.
		if !meta.Retryable || attempt == maxAttempts {
			break
		}

		backoff := s.cfg.BaseBackoff * time.Duration(1<<uint(attempt-1))
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			result := ToolResult{
				ToolCallID: call.ID,
				Name:       call.Name,
				Arguments:  call.Arguments,
				Success:    false,
				IsError:    true,
				Content:    "tool execution cancelled",
				Duration:   time.Since(started),
				Attempt:    attempt,
				Err:        ctx.Err(),
			}
			emit(Event{Type: EventToolCancelled, ToolResult: &result})
			return result
		}
	}

	content := last.Content
	if content == "" && lastErr != nil {
		content = lastErr.Error()
	}
	content = session.ScrubContent(content)
	result := ToolResult{
		ToolCallID: call.ID,
		Name:       call.Name,
		Arguments:  call.Arguments,
		Content:    content,
		IsError:    true,
		Success:    false,
		Duration:   time.Since(started),
		Attempt:    maxAttempts,
		Err:        lastErr,
	}
	emit(Event{Type: EventToolFailed, ToolResult: &result})
	return result
}

// isSensitiveCall reports whether a tool call targets a sensitive file
// that should require explicit permission even if the tool is otherwise
// allowed. This is a defense-in-depth check that does not rely on the
// model to recognize secrets.
func isSensitiveCall(call providers.ToolCall) bool {
	switch call.Name {
	case "read_file", "write_file", "list_files", "search_files", "find_files", "git", "secret_scan":
		if v, ok := call.Arguments["path"]; ok {
			if s, ok := v.(string); ok {
				return filesystem.IsSensitivePath(s)
			}
		}
	case "shell_job":
		// Jobs scope through cwd rather than path.
		if v, ok := call.Arguments["cwd"]; ok {
			if s, ok := v.(string); ok && s != "" {
				return filesystem.IsSensitivePath(s)
			}
		}
	}
	return false
}

func (s *scheduler) getSessionDecision(tool string) (permissions.Decision, bool) {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	d, ok := s.sessionAllow[tool]
	return d, ok
}

func (s *scheduler) setSessionDecision(tool string, d permissions.Decision) {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	if s.sessionAllow == nil {
		s.sessionAllow = make(map[string]permissions.Decision)
	}
	s.sessionAllow[tool] = d
}

// sessionAllowKey returns the session-scoped Always-allow lookup key for a
// call. Command-oriented tools (shell, shell_job) scope by normalized
// command text, working directory, and environment; all other tools have
// no meaningful operation identifier and preserve the historical
// per-tool-name behavior.
func sessionAllowKey(call providers.ToolCall) string {
	switch call.Name {
	case "shell", "shell_job":
		if cmd, ok := normalizedCommand(call.Arguments); ok {
			return call.Name + "\x00" + cmd + "\x00" + normalizedCwd(call.Arguments) + "\x00" + canonicalEnvHash(call.Arguments)
		}
	}
	return call.Name
}

// normalizedCommand extracts the trimmed command text so semantically
// identical approvals (e.g. extra surrounding whitespace) share one key.
func normalizedCommand(args map[string]any) (string, bool) {
	if args == nil {
		return "", false
	}
	raw, ok := args["command"]
	if !ok {
		return "", false
	}
	s, ok := raw.(string)
	if !ok {
		return "", false
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	return s, true
}

// normalizedCwd extracts the trimmed working directory so the same
// command approved in one directory does not silently authorize the
// same string elsewhere. Missing or blank reads as empty (stable).
func normalizedCwd(args map[string]any) string {
	if args == nil {
		return ""
	}
	raw, ok := args["cwd"]
	if !ok {
		return ""
	}
	s, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

// canonicalEnvHash folds the call's environment block into a stable
// digest so an approval granted with one environment is not reused
// under another. Missing or non-object blocks hash as empty; entries
// are sorted so key order cannot change the key.
func canonicalEnvHash(args map[string]any) string {
	if args == nil {
		return emptyEnvHash()
	}
	raw, ok := args["env"]
	if !ok || raw == nil {
		return emptyEnvHash()
	}
	env, ok := raw.(map[string]any)
	if !ok {
		h := fnv.New64a()
		fmt.Fprintf(h, "%v", raw)
		return strconv.FormatUint(h.Sum64(), 16)
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := fnv.New64a()
	for _, k := range keys {
		fmt.Fprintf(h, "%s\x00%v\x01", k, env[k])
	}
	return strconv.FormatUint(h.Sum64(), 16)
}

func emptyEnvHash() string {
	h := fnv.New64a()
	return strconv.FormatUint(h.Sum64(), 16)
}

// checkPermissionWithManager resolves a tool call's permission before
// execution against an explicit manager snapshot. See RunWithManager for
// why the snapshot matters.
func (s *scheduler) checkPermissionWithManager(ctx context.Context, call providers.ToolCall, emit func(Event) bool, manager *tools.Manager) (denied bool, result *ToolResult) {
	if s.permissions == nil {
		return false, nil // no permission manager configured: fail open
	}

	// Session-scoped Always allow/deny takes precedence over persisted rules.
	// Deny stays per-tool-name (fail-closed); Allow is operation-scoped
	// via sessionAllowKey so one "Always allow shell" authorizes only the
	// approved command in its directory and environment, not every
	// future command in the session.
	if d, ok := s.getSessionDecision(call.Name); ok && d == permissions.Deny {
		result = s.deniedResult(call, fmt.Sprintf("permission denied for tool %q", call.Name))
		emit(Event{Type: EventToolDenied, ToolResult: result})
		return true, result
	}
	if d, ok := s.getSessionDecision(sessionAllowKey(call)); ok && d == permissions.Allow {
		// Even session-allowed operations still require explicit approval
		// for sensitive files; otherwise a single Always allow would bypass
		// the sensitive-file guard.
		if isSensitiveCall(call) {
			// Fall through to Ask path
		} else {
			return false, nil
		}
	}

	decision := s.permissions.Check(call.Name)
	// Sensitive files always require Ask, even if globally allowed.
	if decision == permissions.Allow && isSensitiveCall(call) {
		decision = permissions.Ask
	}

	if decision == permissions.Allow {
		return false, nil
	}

	if decision == permissions.Ask {
		resolved, err := s.resolveAskWithManager(ctx, call, manager)
		if err != nil {
			// Cancellation while the prompt was open is a cancel, not a
			// denial: report ToolCancelled so the transcript, session,
			// and failure counters all see the truth.
			if ctx.Err() != nil {
				result := ToolResult{
					ToolCallID: call.ID,
					Name:       call.Name,
					Arguments:  call.Arguments,
					Success:    false,
					IsError:    true,
					Content:    "tool execution cancelled",
					Err:        ctx.Err(),
				}
				emit(Event{Type: EventToolCancelled, ToolResult: &result})
				return true, &result
			}
			result := s.deniedResult(call, fmt.Sprintf("permission prompt failed: %v", err))
			emit(Event{Type: EventToolDenied, ToolResult: result})
			return true, result
		}
		if resolved == permissions.Allow {
			return false, nil
		}
	}

	// decision == permissions.Deny, or an "ask" that resolved to deny.
	result = s.deniedResult(call, fmt.Sprintf("permission denied for tool %q", call.Name))
	emit(Event{Type: EventToolDenied, ToolResult: result})
	return true, result
}

// executionEnforcementSource is implemented by tools whose process
// execution has a policy story (the shell tool, via its sandbox
// executor). The scheduler consults it so permission requests can state
// exactly what will run and under what boundary.
type executionEnforcementSource interface {
	ExecutionEnforcement(ctx context.Context) (sandbox.Enforcement, bool)
}

// resolveAskWithManager prompts for a decision against an explicit manager
// snapshot. It handles "always" as session-scoped and serializes concurrent
// asks so the single TUI modal is never overwritten.
func (s *scheduler) resolveAskWithManager(ctx context.Context, call providers.ToolCall, manager *tools.Manager) (permissions.Decision, error) {
	asker := s.getAsker()
	if asker == nil {
		// No interactive surface available (e.g. non-interactive
		// automation): fail closed rather than silently executing.
		return permissions.Deny, fmt.Errorf("tool %q requires approval but no permission prompt is configured", call.Name)
	}

	s.askMu.Lock()
	defer s.askMu.Unlock()

	req := permissions.Request{Tool: call.Name, Arguments: call.Arguments}
	if src, ok := lookupEnforcementSource(manager, call.Name); ok {
		if e, ok := src.ExecutionEnforcement(ctx); ok {
			req.Execution = &e
		}
	}

	prompt, err := asker.Ask(ctx, req)
	if err != nil {
		return permissions.Deny, err
	}

	if prompt.Persist() {
		// Session-scoped: do not persist globally to config.yaml. This
		// prevents a single "Always allow shell" from permanently widening
		// the tool's scope for all future sessions. Allow is stored under
		// the operation-scoped key; Deny stays per-tool-name (fail-closed).
		if prompt.Decision() == permissions.Allow {
			s.setSessionDecision(sessionAllowKey(call), permissions.Allow)
		} else {
			s.setSessionDecision(call.Name, permissions.Deny)
		}
	}

	return prompt.Decision(), nil
}

func (s *scheduler) deniedResult(call providers.ToolCall, reason string) *ToolResult {
	result := &ToolResult{
		ToolCallID: call.ID,
		Name:       call.Name,
		Arguments:  call.Arguments,
		Success:    false,
		IsError:    true,
		Content:    reason,
	}
	return result
}

// lookupEnforcementSource resolves the registered tool implementing the
// enforcement-source interface, if any.
func lookupEnforcementSource(manager *tools.Manager, name string) (executionEnforcementSource, bool) {
	if manager == nil {
		return nil, false
	}
	tool, ok := manager.Lookup(name)
	if !ok {
		return nil, false
	}
	src, ok := tool.(executionEnforcementSource)
	return src, ok
}

func execute(ctx context.Context, tool tools.Tool, args map[string]any, onChunk func(tools.StreamChunk)) (tools.Result, error) {
	if st, ok := tool.(tools.StreamingTool); ok {
		return st.ExecuteStream(ctx, args, onChunk)
	}
	return tool.Execute(ctx, args)
}

// effectiveTimeout resolves one tool call's execution bound: a
// configured per-tool override wins, then the advertised metadata, then
// the shared default — always clamped to the hard MaxTimeout ceiling so
// no tool (and no misconfigured override) can run unbounded.
func effectiveTimeout(tool tools.Tool, meta tools.Metadata) time.Duration {
	timeout := meta.Timeout
	if lp, ok := tool.(tools.LimitsProvider); ok {
		if l := lp.ToolLimits(); l.Timeout > 0 {
			timeout = l.Timeout
		}
	}
	return tools.ClampTimeout(timeout, tools.DefaultToolTimeout)
}

func (s *scheduler) cancelledResult(call providers.ToolCall) ToolResult {
	return ToolResult{
		ToolCallID: call.ID,
		Name:       call.Name,
		Arguments:  call.Arguments,
		Success:    false,
		IsError:    true,
		Content:    "tool execution cancelled before it started",
		Attempt:    0,
		Err:        context.Canceled,
	}
}
