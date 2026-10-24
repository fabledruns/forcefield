# Agents

Package: `internal/agent` + `internal/runtime` (registry integration)

Forcefield supports multiple specialised agents. Each agent is a capability
profile — identity, system prompt, skill assignment, tool set, behavioral
constraints, and optional model/provider hints. The runtime remains shared:
what the agent *is* lives in the definition; *how it runs* lives in `Runtime`.

## Capability model

```text
Agent Definition
├── identity / description
├── system prompt
├── skills (allow-list of skill IDs, or all for general)
├── tools (allow-list of tool names)
├── constraints (behavioral guidance, prompt-only)
└── optional model/provider hints
```

Separation of concerns (enforced, not aspirational):

| Concept | Role | Enforced by |
|---|---|---|
| Skills | Knowledge/workflows/instructions | Per-agent catalog + scoped `load_skill` |
| Tools | Executable capabilities | Filtered manager + fail-closed scheduler lookup |
| Constraints | Behavioral guidance | Prompt text only — never a security boundary |
| Permissions | Authorization | Unchanged global permission system |
| Runtime/scheduler | Enforcement | Owns execution; agents declare, runtime applies |

A skill NEVER grants a tool. A constraint NEVER gates execution. An
available tool still passes permissions.

## Built-in agents

| Name | Tools | Skills | Constraints |
|---|---|---|---|
| `coding` | read/write/list/pwd/shell/search/scan + runtime | intelligence, code-review, debugging, architecture, clean-code | scope discipline |
| `cyber` | read/list/pwd/shell/search/scan + runtime | intelligence, code-review | defensive-only (3 rules) |
| `legal` | read/list/pwd/search + runtime | none assigned | not-a-lawyer (2 rules) |
| `docs` | read/write/list/pwd/search + runtime | none assigned | don't change code behavior |
| `research` | read/list/pwd/search + runtime | intelligence | facts-vs-interpretation |
| `devops` | read/write/list/pwd/shell/search + runtime | intelligence, debugging | no destructive commands |
| `general` | all 14 tools | **all** installed skills | none |

"+ runtime" = `load_skill, update_task_state, add_project_memory`.
Skill IDs reference the global store; IDs not installed degrade gracefully
(warning + omission). No fake skill files are shipped: install from
`examples/skills/` into `~/.forcefield/skills/` to light up assignments.

## Selection

CLI:
```bash
ff --agent coding
ff chat --agent cyber
ff run --agent legal "summarise this clause"
```

TUI:
```text
/agent           # list agents with tools + skills
/agent coding    # switch (reports tool/skill counts)
/status          # shows active agent, tools, skills
```

Precedence: `CLI --agent` > `resumed Session.Agent` > `config:agent.name` > `general`. When CLI overrides a resumed session's agent, the session is updated and the TUI shows the switch.

Switching happens only at a turn boundary. An active stream is cancelled before the switch.

## Tool isolation

Isolation is code-enforced, not prompt-only:

1. `Runtime.manager.Definitions()` filters the tool list sent to the model.
2. `scheduler.Lookup` fails closed (`tool not found`) if the model hallucinates a filtered tool.

`ToolSummaries()` and `/tools` reflect the active agent's filtered set.

## Skill isolation

Each agent gets its own catalog section (assigned IDs in store order).
`load_skill` is scoped to the active agent's set:

- assigned + installed → body loads
- installed but unassigned → soft error naming the agent
- absent entirely → soft "not found in the catalog" error

Matching is exact-ID only (no normalization fallthrough), so a skill can
never be granted by naming similarity, and a missing skill never resolves
to a different skill. Bodies are never fabricated.

`ff doctor` reports assigned-but-missing skills as warnings.

## Permissions

Permissions remain global (`permissions.default` + `permissions.tools`). An agent's definition does not bypass the permission system. `Always allow` decisions are tool-scoped, not `agent+tool` scoped — a prior `Always allow shell` under `coding` still applies after switching to `cyber` for the shared `shell` tool. Documented as future work.

New tools ship with defaults: `search_files: allow`, `search_code: allow`, `find_files: allow`, `secret_scan: allow`
(read-only, consistent with `read_file`/`list_files`). All four join the
sensitive-path escalation set (`secret_scan` on a sensitive path still
requires approval).

## Delegation (orchestration)

A parent agent can hand bounded subtasks to child agent runs with the
`delegate` tool. The whole feature is off by default
(`orchestration.enabled: false`): with it off there is no orchestrator and
no delegate tool in any universe; a config naming `delegate` under
`agents.*.tools` is rejected at load (fail-closed, truthfully reported: the
capability requires the orchestrator).

When enabled, `delegate` joins the full tool universe once, and every
agent that opted into tools (non-empty `tools` list) gains it. An agent
with an explicitly empty `tools` list stays a pure-text agent. Delegation
bounds are configured under `orchestration:` and enforced at the call
site, never in prompts:

| Setting | Default | Ceiling | Meaning |
|---|---|---|---|
| `max_depth` | 1 | 3 | nesting levels (1 = children only) |
| `max_concurrent_children` | 4 | 8 | child runs running at once, process-wide |
| `max_children_per_run` | 16 | 64 | spawns per top-level run, cumulative across levels |
| `max_task_chars` | 8000 | 65536 | payload per delegation (floor 256) |

Child runs are isolated by construction:

- The child scheduler shares the global rule set but starts with an empty
  session-approval map: nothing the operator approved in the parent
  session carries in, and nothing the child records leaks back.
- `child_permissions: deny` (default) fails closed: any call that would
  prompt is denied, no approval surface is opened, and nothing is
  persisted.
- `child_permissions: interactive` routes child asks to the operator's
  assessment surface, stamped with the delegation chain (`root > child`),
  and downgrades `always allow`/`always deny` answers to one-time
  decisions: a child can never persist a global rule.
- Each child gets a definition-scoped `load_skill` (its own assignment
  only) and a tool set filtered exactly like a parent's (missing MCP
  names omitted; unknown natives fail).
- The task enters the child as a user message only: parent conversation
  never reaches the child context, and the task text never enters a
  system prompt.
- Children always run in chat mode: plan overlays never leak into child
  policy.

Depth is structural: a child at the deepest permitted level has
`delegate` stripped from its universe entirely, so a grandchild attempt
fails at tool resolution even if the model hallucinates the call; the
per-run spawn budget additionally covers every level combined (completed
children never free budget).

The parent receives a bounded report: honest lifecycle counts (turns,
tool calls, failures, denials), the child's final answer (size-capped and
secret-scrubbed), and denial notes. Child statuses are informational —
the parent's own verification state is computed independently and is
never upgraded by a child's claim. Parent cancellation joins the child
first: nothing runs detached, nothing reports after the tool result.

## Configuration

```yaml
model:
  provider: ollama
  name: ornith:9b

agent:
  name: general

agents:
  coding:
    # Scalars: non-empty replaces. Lists: omitted keeps, explicit replaces
    # (`skills: []` means "no skills" — verified against yaml.v3).
    description: "Custom coding agent"
    system_prompt: "You are a custom coding agent..."
    tools:
      - read_file
      - write_file
      - list_files
      - pwd
      - shell
    skills:
      - code-review
      - debugging
    constraints:
      - Stay in scope.
    provider: openai
    model: gpt-4o-mini

  legal:
    system_prompt: "You are a careful legal assistant..."
    skills: []   # explicitly no skills
```

- `agents.<name>.tools` may list known tool names (incl. `delegate`, which additionally requires orchestration); unknown tools fail at startup when the tool universe is built, rejecting the run (fail-closed).
- `agents.<name>.skills` accepts any IDs; unknown IDs warn at runtime (skills are user-local), never fail load. Empty/missing entries are rejected; duplicates are rejected.
- `agents.<name>` may only override built-in names (`coding, cyber, legal, docs, research, devops, general`); unknown names are rejected. Config cannot create genuinely new agents in v1.
- Model/provider hints are in-memory only and are applied via the existing `SetProvider`/`SetModel` path. Failures leave the previous agent, provider, model, tools, and skills unchanged.

### Legacy `agent:` compatibility

Pre-feature configs used a free-form `agent.name` / `agent.system_prompt`:

- `agent.name: default` (or empty) → `general`.
- `agent.name` matching a built-in (e.g. `legal`) → that agent.
- Any other `agent.name` (e.g. `Jarvis`) → `general` behaviour with the custom label preserved in the header.
- `agent.system_prompt`, when set, still applies — unless `agents.<name>.system_prompt` overrides it for the active agent.

Prompt precedence: `agents.<name>.system_prompt` > legacy `agent.system_prompt` > built-in prompt.

## Sessions

`Session.Agent` (`json:"agent,omitempty"`) persists the active agent key. Old sessions without the field load as `general`. Switching sessions restores the target session's agent (fallback to `general` with a notice if the stored agent no longer exists). Capabilities resolve from current definitions at load — sessions store identity, not capability snapshots.

## Registry

```go
r := agent.DefaultRegistry() // 7 built-ins, independent copy
def, err := r.Get("coding")
list := r.List()      // registration order
def := r.Default()   // general
```

The registry is instance-scoped (owned by `Runtime`) and effectively immutable after construction. `Register` is only called during startup.

## Adding a new agent

1. Add a `Definition` to `internal/agent/builtins.go` (tools + skills + constraints).
2. Register it in `builtInDefinitions()`.
3. Add tests for its tool set, skill set, and prompt.
4. Update `docs/Agents.md` and `cue/config.cue`.

No runtime loop changes are required. Do not add conditional branches like `if agent == "new"` inside `runtime.run`. Tools are globally registered (`internal/tools/...`) and assigned per agent — never owned per agent.

## Security

Tool isolation is the security boundary. The cyber agent is defensive-only (system prompt + constraints + no `write_file`); `secret_scan` reports with redacted snippets and never transmits, validates, or uses findings. Do not add offensive capabilities to make it appear specialised.
