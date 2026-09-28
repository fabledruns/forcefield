package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"forcefield/internal/config"
	"forcefield/internal/mcp"
	"forcefield/internal/runtime"
)

// doctorMCP validates MCP server configuration and reports last-known
// status without ever starting a server. Configuration shape was already
// checked by config.Load; this layer restates the effective per-server
// values (never environment values, secrets, or full command lines) and
// layers the persisted status file on top, clearly labeled current or
// stale. A valid configuration here does NOT mean the server is
// reachable: reachability requires spawning, which doctor refuses to do.
func doctorMCP(cfg *config.Config, report func(verdict, string, ...any)) {
	if cfg == nil {
		return
	}
	servers := cfg.MCP.Servers
	if len(servers) == 0 {
		report(vOK, "mcp: no servers configured")
		return
	}
	if err := cfg.MCP.Validate(); err != nil {
		report(vFail, "mcp: invalid configuration: %v", err)
		return
	}

	root, err := runtime.ResolveWorkspace(cfg)
	if err != nil {
		report(vWarn, "mcp: cannot resolve workspace root (%v); skipping cwd and status checks", err)
		root = ""
	}

	keys := make([]string, 0, len(servers))
	for k := range servers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		doctorMCPServer(key, servers[key], root, report)
	}

	doctorMCPStatus(cfg, root, report)

	// The guarantee, stated plainly every run with MCP configured: shape
	// and last-known state are all doctor can speak to.
	report(vOK, "mcp: doctor never starts servers; valid configuration does not prove reachability")
}

// doctorMCPServer reports one server's effective configuration. Only
// shape-level facts are shown: the executable path, the resolved timeout,
// the working directory, and environment entry counts. Environment values,
// full argument vectors, and secrets are never printed.
func doctorMCPServer(key string, sc mcp.ServerConfig, root string, report func(verdict, string, ...any)) {
	if !sc.IsEnabled() {
		report(vOK, "mcp: server %q: disabled (never started)", key)
		return
	}
	timeout := sc.TimeoutSeconds
	if timeout == 0 {
		timeout = mcp.DefaultServerTimeoutSeconds
	}
	cwd := "workspace default"
	if sc.Cwd != "" {
		cwd = fmt.Sprintf("%q", sc.Cwd)
		if root != "" {
			dir := sc.Cwd
			if !filepath.IsAbs(dir) {
				dir = filepath.Join(root, dir)
			}
			if info, err := os.Stat(dir); err != nil || !info.IsDir() {
				report(vWarn, "mcp: server %q: cwd %q does not exist or is not a directory", key, sc.Cwd)
			}
		}
	}
	report(vOK, "mcp: server %q: command %q, timeout %gs, cwd %s, env %d explicit + %d passthrough (values hidden)",
		key, sc.Command, timeout, cwd, len(sc.Env), len(sc.EnvPassthrough))
}

// doctorMCPStatus reports the persisted last-known state, labeled current
// or stale by configuration fingerprint. It never spawns, probes, or
// modifies anything; a missing file simply means no session has recorded
// state here yet.
func doctorMCPStatus(cfg *config.Config, root string, report func(verdict, string, ...any)) {
	if root == "" {
		return
	}
	st, err := mcp.ReadStatusFile(root)
	if err != nil {
		if errors.Is(err, mcp.ErrStatusNotFound) {
			report(vOK, "mcp: no status recorded yet (written when a session starts with MCP enabled)")
			return
		}
		report(vWarn, "mcp: status file unreadable (%v); it will be rewritten on the next run", err)
		return
	}
	current := st.CurrentFor(cfg.MCP)
	freshness := "current"
	v := vOK
	if !current {
		freshness = "stale (configuration changed since)"
		v = vWarn
		report(vWarn, "mcp: status is stale (configuration changed since it was written); showing last-known state")
	}
	known := make(map[string]bool, len(st.Servers))
	for _, ss := range st.Servers {
		known[ss.Key] = true
		if _, ok := cfg.MCP.Servers[ss.Key]; !ok {
			report(vWarn, "mcp: server %q: removed from configuration (%s)", ss.Key, freshness)
			continue
		}
		doctorMCPServerStatus(ss, freshness, v, report)
	}
	for key, sc := range cfg.MCP.Servers {
		if !sc.IsEnabled() || known[key] {
			continue
		}
		report(v, "mcp: server %q: no status entry (%s)", key, freshness)
	}
}

// doctorMCPServerStatus renders one persisted server entry. Failure
// reasons reuse the snapshot's bounded, scrubbed diagnostic; tool counts
// stand in for tool identities.
func doctorMCPServerStatus(ss mcp.ServerStatus, freshness string, staleVerdict verdict, report func(verdict, string, ...any)) {
	if !ss.Enabled {
		report(staleVerdict, "mcp: server %q last run: disabled (%s)", ss.Key, freshness)
		return
	}
	if ss.Ready && ss.Healthy {
		report(staleVerdict, "mcp: server %q last run: healthy, %d tool(s) (%s)", ss.Key, len(ss.Tools), freshness)
		return
	}
	reason := ss.LastError
	if reason == "" {
		reason = "no diagnostic recorded"
	}
	report(vWarn, "mcp: server %q last run: failed: %s (%s)", ss.Key, reason, freshness)
}
