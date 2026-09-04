# Agents

Package: `internal/agent` + `internal/runtime` (registry integration)

Forcefield supports multiple specialised agents. Each agent has its own identity, system prompt, tool set, and optional model/provider hints. The runtime remains shared.

## Built-in agents

| Name | Description | Tools |
|---|---|---|
| `coding` | Software engineering — inspect, edit, test, verify | `read_file, write_file, list_files, pwd, shell, load_skill, update_task_state, add_project_memory` |
| `cyber` | Defensive security analysis — review, threat modelling, secure config | `read_file, list_files, pwd, shell, load_skill, update_task_state, add_project_memory` |
| `legal` | Legal text analysis — obligations, ambiguity (not a lawyer) | `read_file, list_files, pwd, load_skill, update_task_state, add_project_memory` |
| `docs` | Documentation — discover, write, restructure | `read_file, write_file, list_files, pwd, load_skill, update_task_state, add_project_memory` |
| `research` | Research — gather evidence, compare sources, synthesise | `read_file, list_files, pwd, load_skill, update_task_state, add_project_memory` |
| `devops` | DevOps — builds, tests, configs, operations | `read_file, write_file, list_files, pwd, shell, load_skill, update_task_state, add_project_memory` |
| `general` | General assistant — default Forcefield experience | `read_file, write_file, list_files, pwd, shell, load_skill, update_task_state, add_project_memory` |

`general` and `coding` currently expose the same tool set to preserve backwards compatibility.

## Selection

CLI:
```bash
ff --agent coding
ff chat --agent cyber
ff run --agent legal "summarise this clause"
```

TUI:
```text
/agent           # list agents
/agent coding    # switch
```

Precedence: `CLI --agent` > `resumed Session.Agent` > `config:agent.name` > `general`. When CLI overrides a resumed session's agent, the session is updated and the TUI shows the switch.

Switching happens only at a turn boundary. An active stream is cancelled before the switch.

## Tool isolation

Isolation is code-enforced, not prompt-only:

1. `Runtime.manager.Definitions()` filters the tool list sent to the model.
2. `scheduler.Lookup` fails closed (`tool not found`) if the model hallucinates a filtered tool.

`ToolSummaries()` and `/tools` reflect the active agent's filtered set.

## Permissions

Permissions remain global (`permissions.default` + `permissions.tools`). An agent's definition does not bypass the permission system. `Always allow` decisions are tool-scoped, not `agent+tool` scoped — a prior `Always allow shell` under `coding` still applies after switching to `cyber` for the shared `shell` tool. Documented as future work.

## Configuration

```yaml
model:
  provider: ollama
  name: ornith:9b

agent:
  name: general

agents:
  coding:
    # Optional overrides; only non-empty values replace the built-in.
    # Unknown agent names are rejected.
    description: "Custom coding agent"
    system_prompt: "You are a custom coding agent..."
    tools:
      - read_file
      - write_file
      - list_files
      - pwd
      - shell
    provider: openai
    model: gpt-4o-mini

  legal:
    system_prompt: "You are a careful legal assistant..."
```

- `agents.<name>.tools` must list known tool names; unknown tools are rejected at load.
- `agents.<name>` may only override built-in names (`coding, cyber, legal, docs, research, devops, general`); unknown names are rejected.
- Model/provider hints are in-memory only and are applied via the existing `SetProvider`/`SetModel` path. Failures leave the previous agent, provider, and model unchanged.

### Legacy `agent:` compatibility

Pre-feature configs used a free-form `agent.name` / `agent.system_prompt`:

- `agent.name: default` (or empty) → `general`.
- `agent.name` matching a built-in (e.g. `legal`) → that agent.
- Any other `agent.name` (e.g. `Jarvis`) → `general` behaviour with the custom label preserved in the header.
- `agent.system_prompt`, when set, still applies — unless `agents.<name>.system_prompt` overrides it for the active agent.

Prompt precedence: `agents.<name>.system_prompt` > legacy `agent.system_prompt` > built-in prompt.

## Sessions

`Session.Agent` (`json:"agent,omitempty"`) persists the active agent. Old sessions without the field load as `general`. Switching sessions restores the target session's agent (fallback to `general` with a notice if the stored agent no longer exists).

## Registry

```go
r := agent.DefaultRegistry() // 7 built-ins, independent copy
def, err := r.Get("coding")
list := r.List()      // registration order
def := r.Default()   // general
```

The registry is instance-scoped (owned by `Runtime`) and effectively immutable after construction. `Register` is only called during startup.

## Adding a new agent

1. Add a `Definition` to `internal/agent/builtins.go`.
2. Register it in `builtInDefinitions()`.
3. Add tests for its tool set and prompt.
4. Update `docs/Agents.md` and `cue/config.cue`.

No runtime loop changes are required. Do not add conditional branches like `if agent == "new"` inside `runtime.run`.

## Security

Tool isolation is the security boundary. The cyber agent is defensive-only (system prompt + no `write_file`). Do not add offensive capabilities to make it appear specialised.
