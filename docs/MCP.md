# MCP

Package: `internal/mcp` (protocol, transport, client, adapters, Host, status)

Forcefield can expose tools from local [Model Context Protocol](https://modelcontextprotocol.io/) servers as ordinary Forcefield tools. MCP is an integration boundary: the runtime sees `mcp__server__tool` entries in the normal tool registry, while all protocol, process, and lifecycle details stay inside `internal/mcp`.

## Trust model

MCP servers are **external, untrusted execution boundaries**, even when you configured them yourself:

- They run with the **operating-system privileges of the Forcefield process** and are **not Forcefield-sandboxed**. The sandbox, workspace confinement, and `BoundaryChecker` machinery applies to native tools only.
- A server's launch directory (`cwd`) only selects where the process starts. It is not a sandbox, and workspace boundaries do not protect MCP arguments.
- Tool names, descriptions, schemas, results, errors, and stderr from a server are untrusted data. Forcefield validates, bounds, truncates, and redacts them; it never executes them.
- MCP tool schemas describe arguments. They provide no filesystem confinement, and Forcefield cannot safely inspect opaque MCP arguments for boundary decisions.

Only configure servers you trust, and prefer `Ask` (the default) over blanket `Allow` for their tools.

## Configuration

```yaml
mcp:
  servers:
    docs:
      command: /usr/local/bin/docs-server
      args: ["--root", "/srv/docs"]
      cwd: ""                    # empty = workspace root
      env:
        DOCS_MODE: readonly
      env_passthrough: []        # host variables to inherit, empty by default
      timeout_seconds: 30        # per tools/call attempt, max 300
      enabled: true
```

| Field             | Required | Description                                                                 |
| ----------------- | -------- | --------------------------------------------------------------------------- |
| key (`docs`)      | Yes      | 1–32 characters: letters, digits, `_`, `-`. Used in tool names.            |
| `command`         | Yes, when enabled | Server executable: absolute path or a name resolved at startup. Never interpreted by a shell. |
| `args`            | No       | argv elements passed directly, no expansion. Each ≤4 KiB, total ≤64 KiB.    |
| `cwd`             | No       | Launch directory. Empty means the workspace root. Must exist.               |
| `env`             | No       | Literal extra variables (≤64 entries, each ≤8 KiB). No `$VAR` expansion. Plaintext in config: not a vault. |
| `env_passthrough` | No       | Host variable names to inherit (default empty). Missing names are skipped. Explicit `env` wins on collision. |
| `timeout_seconds` | No       | Per-call timeout, 0–300. `0` (or omitted) means the 30 s default.          |
| `enabled`         | No       | `false` means the server is never launched. Default `true`.                 |

At most 8 servers may be enabled. Environment handling is deliberately minimal: the child starts from an empty baseline (plus `SystemRoot` on Windows, which process creation requires), then passthrough copies, then explicit values. Provider API keys and other host secrets are never inherited unless you explicitly pass them through — don't.

## Tool naming

Each discovered tool registers as:

```text
mcp__<server>__<tool>
```

e.g. server `docs` exposing `search` becomes `mcp__docs__search`. If two servers ever claim the same qualified name, all colliding instances are dropped rather than first-wins. A colliding native name always keeps the native tool.

## Agent selection

MCP tools are explicitly opt-in per agent through the existing mechanism:

```yaml
agents:
  coding:
    tools: [read_file, write_file, mcp__docs__search]
```

Configuring a server alone exposes nothing to any agent. A requested name with no healthy provider is omitted with a warning (see `MCPWarnings`); unknown native names still fail strictly. Plan/read-only mode never includes `mcp__*` tools.

Permissions apply per qualified name through the standard system. Unlisted `mcp__*` tools default to `Ask`; headless runs without a prompt fail closed. See [Tools](Tools.md) for the permission model.

## Lifecycle

- **Startup discovery.** Enabled servers start in sorted key order when a session starts. One server's failure never blocks the others or the native tools; failures surface as warnings.
- **Frozen tool universe.** The tool set is fixed at startup. No live re-listing, no reconnect, no respawn: a dead server stays dead until restart.
- **Shutdown.** Closing the session shuts servers down (stdin EOF, bounded grace, then process-tree termination) and records final status.
- **Timeouts.** `timeout_seconds` bounds one `tools/call` attempt and flows into adapters before registration, so the scheduler enforces it end to end.

## Doctor and status

`ff doctor` validates MCP configuration shape and reports last-known state from `.forcefield/mcp-status.json`, which sessions persist at startup and shutdown (atomic write, mode `0600`, no secrets or environment values). Doctor **never starts MCP servers**: valid configuration does not prove reachability.

Status is fingerprinted against the configuration that produced it. If the configuration changed since, doctor labels the status **stale** and shows it as history only; stale status is never deleted automatically and never presented as current. A missing file simply means no session has recorded state yet; an unreadable file is reported as a warning and rewritten on the next run.

## Slash commands

The interactive TUI manages servers through `/mcp`:

| Command | Action |
| ------- | ------ |
| `/mcp`, `/mcp list` | List configured servers with status. |
| `/mcp add` | Show the guided setup flow. |
| `/mcp add <name> <command> [args...]` | Store a new stdio server (command plus argv, no shell). |
| `/mcp get <name>` | Show one server's configuration and status. |
| `/mcp remove <name>` | Delete one server entry, leaving the rest alone. |
| `/mcp enable <name>`, `/mcp disable <name>` | Flip a server's enabled state without deleting its configuration. |
| `/mcp test <name>` | Start one server ephemerally, run the normal handshake and tool discovery, report the tools, shut it down. |

Status words: `connected` means live-connected at snapshot time; `failed` carries the bounded reason; `disabled` servers never launch; anything else is explicitly `unknown` and never claimed reachable — last-known tool names are labeled as such.

Adding, removing, or toggling a server edits `config.yaml` (persisted immediately) but does not touch the running session: the new configuration takes effect on the next session, since the tool universe is frozen at startup. `/mcp test` blocks the TUI until the probe finishes or the server's configured timeout elapses; it never alters the session's tools.

`/mcp add` covers command plus argv only. Optional fields (`cwd`, `timeout_seconds`, `enabled`, `env`, `env_passthrough`) live in `config.yaml` under `mcp.servers.<name>`. Environment values are never displayed by any subcommand; argument text is redacted before display.

## Excluded

Remote HTTP/SSE transports, reconnect/respawn, live re-listing, resources, prompts, sampling, roots, elicitation, server-initiated requests, cloud status sync, and multi-agent orchestration are out of scope.
