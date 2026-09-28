package runtime

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/mcp"
	"forcefield/internal/tools"
)

// MCP runtime integration (Phase 6).
//
// Boundary, restated so future edits keep it:
//
//	MCP Host
//	  → HostSnapshot
//	  → MCP adapters (tools.Tool instances)
//	  → existing full tool manager (filtered per agent as usual)
//
// Invariants owned here:
//   - MCP tools are explicitly opt-in per agent (agents.<name>.tools).
//     Nothing is auto-exposed.
//   - MCP discovery is startup-only and frozen. The Host starts once in
//     newRuntime; per-agent managers derive from that snapshot and the
//     universe never mutates afterwards.
//   - Native tools take precedence over MCP names on collision.
//   - Duplicate MCP qualified names across servers are dropped entirely
//     (all instances), never first-wins.
//   - mcp__* tools never appear in plan/read-only mode (planModeTools is
//     an allow-list; see planmode.go).
//   - MCP processes are NOT Forcefield-sandboxed. ServerSnapshot.Dir is
//     only a launch directory, never a confinement boundary.
//   - Runtime owns Host lifecycle (Close); Host owns process lifecycle.
//     There is no per-turn teardown and no reconnect/respawn anywhere.
//
// What this file does NOT do: permissions (the Ask default already covers
// mcp__* names and adapters claim no BoundaryChecker), registry changes,
// scheduler changes, TUI/session changes, doctor/status.

// mcpHasEnabledServers reports whether cfg enables at least one MCP
// server. With none enabled the runtime skips Host construction entirely,
// keeping the zero-config startup path (and a nil mcpHost) identical.
func mcpHasEnabledServers(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	for _, s := range cfg.MCP.Servers {
		if s.IsEnabled() {
			return true
		}
	}
	return false
}

// startMCPHost builds the Host, starts enabled servers in deterministic
// sorted server-key order under the Host's own startup budget, and folds
// the frozen snapshot into full. Individual server failures never fail
// startup: healthy survivors (and all native tools) remain registered,
// with details left in the Host snapshot for MCPWarnings. Only Host
// construction failure (deterministic config/workspace breakage,
// mirroring the other newRuntime construction errors) is fatal.
func startMCPHost(cfg *config.Config, workspace string, full *tools.Manager) (*mcp.Host, error) {
	host, err := mcp.New(cfg.MCP, workspace)
	if err != nil {
		return nil, err
	}
	// Warn-and-continue: StartError names failures, but survivors are
	// determined from the snapshot below, not from the error.
	_ = host.Start(context.Background())
	registerMCPTools(full, host.Snapshot())
	return host, nil
}

// persistMCPStatus records the Host's frozen snapshot for `ff doctor`
// to read later without starting servers. Only the workspace root and
// the already-bounded snapshot feed the file: no environment values,
// secrets, arguments, results, or unscrubbed stderr ever reach it (see
// internal/mcp status.go). A nil host writes nothing and succeeds.
func persistMCPStatus(host *mcp.Host, workspaceRoot string, cfg *config.Config) error {
	if host == nil {
		return nil
	}
	mcpCfg := mcp.Config{}
	if cfg != nil {
		mcpCfg = cfg.MCP
	}
	st := mcp.NewStatusFile(host.Snapshot(), mcpCfg, time.Now().Unix())
	return mcp.WriteStatusFile(workspaceRoot, st)
}

// registerMCPTools folds one frozen Host snapshot into the full manager.
// It is deterministic and order-independent: cross-server duplicates
// (snapshot.Duplicates) drop every colliding instance, names already
// present (native precedence) are left untouched, and registration runs
// in sorted server-key order. A registration error can only mean a
// duplicate name, both causes of which are precluded above, so it is
// skipped defensively rather than failing startup.
func registerMCPTools(full *tools.Manager, snap mcp.HostSnapshot) {
	drop := make(map[string]bool, len(snap.Duplicates))
	for _, name := range snap.Duplicates {
		drop[name] = true
	}
	for _, ss := range snap.Servers {
		if !ss.Ready {
			continue
		}
		for _, t := range ss.Tools {
			name := t.Name()
			if drop[name] {
				continue
			}
			if _, exists := full.Lookup(name); exists {
				continue
			}
			_ = full.Register(t)
		}
	}
}

// resolveAgentTools partitions an agent's configured tool list against
// the frozen full universe. Known names are kept verbatim. Requested
// mcp__* names absent from the universe are omitted for MCPWarnings to
// report. Unknown native names are kept so Filtered raises the existing
// strict "unknown tool" error; MCP must never weaken that path.
func resolveAgentTools(full *tools.Manager, want []string) (keep []string, missing []string) {
	keep = make([]string, 0, len(want))
	for _, name := range want {
		if _, ok := full.Lookup(name); ok {
			keep = append(keep, name)
			continue
		}
		if mcp.IsQualifiedToolName(name) {
			missing = append(missing, name)
			continue
		}
		keep = append(keep, name)
	}
	return keep, missing
}

// resolveAgentDefinitionTools is resolveAgentTools over a Definition,
// kept as a separate helper so SetAgent and newRuntime share one path.
func resolveAgentDefinitionTools(full *tools.Manager, def agent.Definition) ([]string, []string) {
	return resolveAgentTools(full, def.Tools)
}

// MCPWarnings reports MCP integration warnings, pull-based like
// SkillWarnings: failed-server diagnostics from the Host snapshot, plus
// every agent's requested-but-unavailable mcp__* tool. Empty when MCP is
// unconfigured, healthy, or fully unreferenced. All inputs are already
// bounded (snapshot strings, config tool names).
func (r *Runtime) MCPWarnings() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	host := r.mcpHost
	registry := r.agents
	full := r.fullManager
	note := r.mcpStatusNote
	r.mu.RUnlock()
	var out []string
	if host != nil {
		for _, ss := range host.Snapshot().Servers {
			if ss.Enabled && !ss.Ready && ss.LastError != "" {
				out = append(out, fmt.Sprintf("mcp server %q failed to start: %s", ss.Key, ss.LastError))
			}
		}
	}
	if registry == nil || full == nil {
		return out
	}
	for _, def := range registry.List() {
		var missing []string
		for _, name := range def.Tools {
			if !mcp.IsQualifiedToolName(name) {
				continue
			}
			if _, ok := full.Lookup(name); !ok {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			out = append(out, fmt.Sprintf("agent %q requests unavailable MCP tool(s): %s", def.Name, joinQuoted(missing)))
		}
	}
	if note != "" {
		out = append(out, note)
	}
	return out
}

func joinQuoted(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, n := range names {
		quoted = append(quoted, fmt.Sprintf("%q", n))
	}
	return strings.Join(quoted, ", ")
}

// Close shuts the runtime's MCP Host down if one exists, owning Host
// teardown exactly once. It never touches per-turn state: the frozen
// tool universe stays registered (later calls fail inside the tools,
// not here). The host pointer is cleared under lock so repeated calls
// are trivially safe even if Host.Close regressed.
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	host := r.mcpHost
	r.mcpHost = nil
	cfg := r.cfg
	workspace := r.workspaceRoot
	r.mu.Unlock()
	if host == nil {
		return nil
	}
	// Capture the final snapshot (including crashed servers) for doctor
	// before teardown. Best-effort: shutdown stays bounded and a write
	// failure must not fail Close.
	_ = persistMCPStatus(host, workspace, cfg)
	return host.Close()
}

// MCP management (slash commands).
//
// These methods let interactive commands inspect and edit MCP servers
// without touching subprocesses, pipes, or protocol code: all lifecycle
// work stays inside mcp.Host, all persistence inside config.Save, and
// the frozen startup tool universe is never mutated here. Adding,
// removing, or toggling a server changes configuration only; the running
// session keeps its existing Host until restart.

// MCPServerState is one configured server as the management UI sees it:
// live Host truth when available, else last-known persisted state,
// explicitly marked. State is one of "ready", "failed", "disabled", or
// "unknown". Tools lists live adapters when ready, else last-known names
// when a current persisted entry has them. Error carries the bounded
// failure reason when failed.
type MCPServerState struct {
	Name           string
	Enabled        bool
	Command        string
	Args           []string
	Cwd            string
	TimeoutSeconds float64
	State          string
	Tools          []string
	Error          string
}

// MCPServerStates merges configuration, the live Host snapshot, and the
// persisted status file into one deterministic (server-key sorted) view.
// Live data wins when the Host exists; otherwise current persisted state
// fills in, labeled by the caller as last-known. A server with neither is
// "unknown" — never claimed reachable.
func (r *Runtime) MCPServerStates() []MCPServerState {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	cfg := r.cfg
	host := r.mcpHost
	workspace := r.workspaceRoot
	r.mu.RUnlock()
	if cfg == nil {
		return nil
	}
	names := make([]string, 0, len(cfg.MCP.Servers))
	for name := range cfg.MCP.Servers {
		names = append(names, name)
	}
	sort.Strings(names)

	var live map[string]mcp.ServerSnapshot
	if host != nil {
		live = make(map[string]mcp.ServerSnapshot, len(names))
		for _, ss := range host.Snapshot().Servers {
			live[ss.Key] = ss
		}
	}
	var persisted map[string]mcp.ServerStatus
	if st, err := mcp.ReadStatusFile(workspace); err == nil && st.CurrentFor(cfg.MCP) {
		persisted = make(map[string]mcp.ServerStatus, len(st.Servers))
		for _, ss := range st.Servers {
			persisted[ss.Key] = ss
		}
	}

	out := make([]MCPServerState, 0, len(names))
	for _, name := range names {
		sc := cfg.MCP.Servers[name]
		st := MCPServerState{
			Name:           name,
			Enabled:        sc.IsEnabled(),
			Command:        sc.Command,
			Args:           append([]string(nil), sc.Args...),
			Cwd:            sc.Cwd,
			TimeoutSeconds: sc.TimeoutSeconds,
		}
		if !st.Enabled {
			st.State = "disabled"
			out = append(out, st)
			continue
		}
		if ss, ok := live[name]; ok {
			switch {
			case ss.Ready:
				st.State = "ready"
				for _, t := range ss.Tools {
					if t != nil {
						st.Tools = append(st.Tools, t.Name())
					}
				}
			case ss.Started || ss.LastError != "":
				st.State = "failed"
				st.Error = ss.LastError
			default:
				st.State = "unknown"
			}
			out = append(out, st)
			continue
		}
		if ps, ok := persisted[name]; ok {
			if !ps.Ready {
				st.State = "failed"
				st.Error = ps.LastError
			} else {
				st.State = "unknown"
				st.Tools = append([]string(nil), ps.Tools...)
			}
			out = append(out, st)
			continue
		}
		st.State = "unknown"
		out = append(out, st)
	}
	return out
}

// MCPAddServer validates and persists a new stdio server entry. Like
// SetModel, the in-memory change applies first and a save failure is
// returned rather than silently ignored. It never starts the server:
// the running Host keeps its frozen universe until restart.
func (r *Runtime) MCPAddServer(name, command string, args []string) error {
	if r == nil {
		return fmt.Errorf("runtime not available")
	}
	r.mu.RLock()
	if r.cfg == nil {
		r.mu.RUnlock()
		return fmt.Errorf("no config to modify")
	}
	cfgCopy := *r.cfg
	r.mu.RUnlock()
	if cfgCopy.MCP.Servers == nil {
		cfgCopy.MCP.Servers = make(map[string]mcp.ServerConfig)
	}
	if err := cfgCopy.AddMCPServer(name, mcp.ServerConfig{Command: command, Args: append([]string(nil), args...)}); err != nil {
		return err
	}
	r.mu.Lock()
	r.cfg = &cfgCopy
	r.mu.Unlock()
	if err := r.SaveConfig(); err != nil {
		return fmt.Errorf("save MCP server: %w", err)
	}
	return nil
}

// MCPRemoveServer deletes a server entry and persists. Only that entry
// is touched. Like MCPAddServer, the live session is unchanged.
func (r *Runtime) MCPRemoveServer(name string) error {
	if r == nil {
		return fmt.Errorf("runtime not available")
	}
	r.mu.RLock()
	if r.cfg == nil {
		r.mu.RUnlock()
		return fmt.Errorf("no config to modify")
	}
	cfgCopy := *r.cfg
	r.mu.RUnlock()
	servers := make(map[string]mcp.ServerConfig, len(cfgCopy.MCP.Servers))
	for k, v := range cfgCopy.MCP.Servers {
		servers[k] = v
	}
	cfgCopy.MCP.Servers = servers
	if err := cfgCopy.RemoveMCPServer(name); err != nil {
		return err
	}
	r.mu.Lock()
	r.cfg = &cfgCopy
	r.mu.Unlock()
	if err := r.SaveConfig(); err != nil {
		return fmt.Errorf("save MCP server: %w", err)
	}
	return nil
}

// MCPSetServerEnabled flips a server's enabled state and persists.
// Disabling keeps the configuration; it never deletes anything.
func (r *Runtime) MCPSetServerEnabled(name string, enabled bool) error {
	if r == nil {
		return fmt.Errorf("runtime not available")
	}
	r.mu.RLock()
	if r.cfg == nil {
		r.mu.RUnlock()
		return fmt.Errorf("no config to modify")
	}
	cfgCopy := *r.cfg
	r.mu.RUnlock()
	servers := make(map[string]mcp.ServerConfig, len(cfgCopy.MCP.Servers))
	for k, v := range cfgCopy.MCP.Servers {
		servers[k] = v
	}
	cfgCopy.MCP.Servers = servers
	if err := cfgCopy.SetMCPServerEnabled(name, enabled); err != nil {
		return err
	}
	r.mu.Lock()
	r.cfg = &cfgCopy
	r.mu.Unlock()
	if err := r.SaveConfig(); err != nil {
		return fmt.Errorf("save MCP server: %w", err)
	}
	return nil
}

// MCPTestResult is the outcome of probing one server: the discovered
// qualified tool names on success.
type MCPTestResult struct {
	Tools []string
}

// MCPTestServer validates one configured server, starts it through an
// ephemeral Host (never the session Host, whose frozen universe must not
// change), runs the normal initialization handshake and tool discovery,
// reports the discovered tools, and shuts the server down. It respects
// the server's configured timeout plus the Host startup budget. Disabled
// servers are refused: enable one before testing it.
func (r *Runtime) MCPTestServer(name string) (MCPTestResult, error) {
	if r == nil {
		return MCPTestResult{}, fmt.Errorf("runtime not available")
	}
	r.mu.RLock()
	if r.cfg == nil {
		r.mu.RUnlock()
		return MCPTestResult{}, fmt.Errorf("no config to read")
	}
	sc, ok := r.cfg.MCP.Servers[name]
	workspace := r.workspaceRoot
	r.mu.RUnlock()
	if !ok {
		return MCPTestResult{}, fmt.Errorf("mcp server %q not found", name)
	}
	if !sc.IsEnabled() {
		return MCPTestResult{}, fmt.Errorf("mcp server %q is disabled (enable it before testing)", name)
	}
	timeout := sc.TimeoutSeconds
	if timeout <= 0 {
		timeout = mcp.DefaultServerTimeoutSeconds
	}
	single := mcp.Config{Servers: map[string]mcp.ServerConfig{name: sc}}
	host, err := mcp.New(single, workspace)
	if err != nil {
		return MCPTestResult{}, err
	}
	defer func() { _ = host.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout*float64(time.Second)))
	defer cancel()
	if err := host.Start(ctx); err != nil {
		return MCPTestResult{}, err
	}
	var tools []string
	for _, ss := range host.Snapshot().Servers {
		if ss.Key != name || !ss.Ready {
			continue
		}
		for _, t := range ss.Tools {
			if t != nil {
				tools = append(tools, t.Name())
			}
		}
	}
	sort.Strings(tools)
	return MCPTestResult{Tools: tools}, nil
}
