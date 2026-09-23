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
