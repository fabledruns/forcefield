package builtin

import (
	"fmt"
	"sort"
	"strings"

	"forcefield/internal/command"
	"forcefield/internal/mcp"
	"forcefield/internal/redact"
)

// MCP manages local MCP servers: /mcp, /mcp list, /mcp add, /mcp get,
// /mcp remove, /mcp enable, /mcp disable, /mcp test. It renders status
// and edits configuration through command.Context; all subprocess,
// protocol, and lifecycle work stays in the runtime and mcp packages.
// Environment values are never displayed; argument text passes through
// centralized redaction before display.
type MCP struct{}

// NewMCP returns a ready-to-register /mcp command.
func NewMCP() *MCP { return &MCP{} }

func (MCP) Name() string        { return "mcp" }
func (MCP) Aliases() []string   { return nil }
func (MCP) Description() string { return "List and manage local MCP servers." }
func (MCP) Usage() string {
	return "/mcp [list|add|get|remove|enable|disable|test] ..."
}

func (MCP) Execute(ctx command.Context, args []string) error {
	if len(args) == 0 {
		return mcpStatus(ctx, true)
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return fmt.Errorf("usage: /mcp list")
		}
		return mcpStatus(ctx, false)
	case "add":
		return mcpAdd(ctx, args[1:])
	case "get":
		if len(args) != 2 {
			return fmt.Errorf("usage: /mcp get <name>")
		}
		return mcpGet(ctx, args[1])
	case "remove":
		if len(args) != 2 {
			return fmt.Errorf("usage: /mcp remove <name>")
		}
		return mcpRemove(ctx, args[1])
	case "enable", "disable":
		if len(args) != 2 {
			return fmt.Errorf("usage: /mcp %s <name>", args[0])
		}
		return mcpSetEnabled(ctx, args[1], args[0] == "enable")
	case "test":
		if len(args) != 2 {
			return fmt.Errorf("usage: /mcp test <name>")
		}
		return mcpTest(ctx, args[1])
	default:
		return fmt.Errorf("unknown /mcp subcommand %q (try list, add, get, remove, enable, disable, test)", args[0])
	}
}

// mcpStatus prints the server table. With hint, a bare /mcp also points
// at subcommands; without configured servers both forms print setup
// guidance instead of an empty table.
func mcpStatus(ctx command.Context, hint bool) error {
	servers := ctx.MCPServers()
	if len(servers) == 0 {
		ctx.Println("No MCP servers configured.")
		ctx.Println("Add one with: /mcp add <name> <command> [args...]")
		return nil
	}
	nameWidth := 4
	for _, s := range servers {
		if len(s.Name) > nameWidth {
			nameWidth = len(s.Name)
		}
	}
	ctx.Println("MCP SERVERS")
	for _, s := range servers {
		ctx.Println("%-*s  %s", nameWidth, s.Name, mcpServerDetail(s))
	}
	ctx.Println("MCP servers run unsandboxed with your OS user privileges (not confined to the workspace).")
	if hint {
		ctx.Println("Manage with: /mcp list, add, get, remove, enable, disable, test")
	}
	return nil
}

// mcpServerDetail renders one table row's state column. "connected"
// means live-connected at snapshot time; anything else makes no
// reachability claim.
func mcpServerDetail(s command.MCPServerInfo) string {
	switch s.State {
	case "ready":
		return fmt.Sprintf("connected  %d tools", len(s.Tools))
	case "failed":
		return "failed"
	case "disabled":
		return "disabled"
	default:
		if len(s.Tools) > 0 {
			return fmt.Sprintf("unknown    %d tools (last known)", len(s.Tools))
		}
		return "unknown"
	}
}

// mcpAdd stores a new server or, with no arguments, prints the guided
// setup flow. There is no prompt primitive for commands, so the
// interactive form is explicit guidance rather than a wizard; environment
// variables stay in config.yaml, where secrets belong.
func mcpAdd(ctx command.Context, args []string) error {
	if len(args) == 0 {
		ctx.Println("Add an MCP server: /mcp add <name> <command> [args...]")
		ctx.Println("Example: /mcp add github npx -y @modelcontextprotocol/server-github")
		ctx.Println("Optional fields live in config.yaml under mcp.servers.<name>:")
		ctx.Println("  cwd, timeout_seconds (0-300), enabled, env, env_passthrough")
		ctx.Println("The server starts on the next session; verify it now with /mcp test <name>.")
		return nil
	}
	if len(args) < 2 {
		return fmt.Errorf("usage: /mcp add <name> <command> [args...]")
	}
	name, command, cmdArgs := args[0], args[1], args[2:]
	if err := ctx.MCPAddServer(name, command, cmdArgs); err != nil {
		return fmt.Errorf("add MCP server: %w", err)
	}
	ctx.Println("Added MCP server '%s'.", name)
	return nil
}

// mcpGet prints one server's configured details. Environment values are
// never shown; argument text is redacted before display.
func mcpGet(ctx command.Context, name string) error {
	s, err := ctx.MCPServer(name)
	if err != nil {
		return fmt.Errorf("get MCP server: %w", err)
	}
	enabled := "no"
	if s.Enabled {
		enabled = "yes"
	}
	timeout := s.TimeoutSeconds
	if timeout <= 0 {
		timeout = mcp.DefaultServerTimeoutSeconds
	}
	cwd := s.Cwd
	if cwd == "" {
		cwd = "workspace default"
	}
	ctx.Println("MCP server '%s':", s.Name)
	ctx.Println("  Enabled: %s", enabled)
	ctx.Println("  Command: %s", s.Command)
	if len(s.Args) > 0 {
		ctx.Println("  Args: %s", redact.Scrub(strings.Join(s.Args, " ")))
	}
	ctx.Println("  Working directory: %s", cwd)
	ctx.Println("  Timeout: %gs", timeout)
	ctx.Println("  Status: %s", mcpGetStatus(s))
	if len(s.Tools) > 0 {
		tools := append([]string(nil), s.Tools...)
		sort.Strings(tools)
		ctx.Println("  Tools (%d): %s", len(tools), strings.Join(tools, ", "))
	}
	if s.Error != "" {
		ctx.Println("  Error: %s", s.Error)
	}
	return nil
}

// mcpGetStatus words the status line. Last-known tools never read as
// current reachability.
func mcpGetStatus(s command.MCPServerInfo) string {
	switch s.State {
	case "ready":
		return "connected"
	case "failed":
		return "failed"
	case "disabled":
		return "disabled"
	default:
		if len(s.Tools) > 0 {
			return "unknown (last-known tools below)"
		}
		return "unknown"
	}
}

func mcpRemove(ctx command.Context, name string) error {
	if err := ctx.MCPRemoveServer(name); err != nil {
		return fmt.Errorf("remove MCP server: %w", err)
	}
	ctx.Println("Removed MCP server '%s'.", name)
	return nil
}

func mcpSetEnabled(ctx command.Context, name string, enabled bool) error {
	if err := ctx.MCPSetServerEnabled(name, enabled); err != nil {
		return fmt.Errorf("mcp server: %w", err)
	}
	state := "disabled"
	if enabled {
		state = "enabled"
	}
	ctx.Println("MCP server '%s' %s.", name, state)
	return nil
}

// mcpTest starts the server ephemerally through the runtime, reports the
// discovered tools, and shuts it down. All lifecycle work stays outside
// this command.
func mcpTest(ctx command.Context, name string) error {
	res, err := ctx.MCPTestServer(name)
	if err != nil {
		return fmt.Errorf("test MCP server: %w", err)
	}
	tools := append([]string(nil), res.Tools...)
	sort.Strings(tools)
	ctx.Println("MCP server '%s' discovered %d tool(s).", name, len(tools))
	for _, t := range tools {
		ctx.Println("  %s", t)
	}
	return nil
}
