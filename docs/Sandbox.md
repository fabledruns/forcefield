# Sandbox

Package: `internal/sandbox`

The `sandbox` package defines Forcefield's execution boundary for shell commands: an explicit policy describing what a command may do, and executors that enforce - or honestly decline to enforce - that policy.

The architectural rule:

```text
The agent requests capabilities.
The executor enforces them.
The TUI only displays them.
```

Tools never construct processes themselves. The shell tool hands its request to the active executor, and no other command-building path exists, so the agent cannot bypass the executor.

---

## Execution modes

### `native` (default)

Historical Forcefield behavior with **no isolation**:

- On Unix, commands run under the system Bash with the full host environment.
- On Windows, commands are relayed through `wsl.exe` purely so GNU Bash exists; this relay is an availability mechanism, **not** a security boundary. The host environment flows to `wsl.exe`, and the distribution can access everything your Windows user can.
- Commands run with your user's permissions on your whole machine.

Native mode is never described as sandboxed anywhere in the UI.

### `wsl`

Commands execute inside a WSL distribution under an explicitly restricted invocation. Requires Windows. If WSL is unavailable or misconfigured, Forcefield **fails with a clear error and never falls back to native execution**.

## Configuration

```yaml
sandbox:
  mode: native          # native | wsl
  wsl:
    distribution: ""    # "" = system default distribution
    network: disabled   # disabled | host
```

| Field                       | Meaning                                                                                                        |
| --------------------------- | -------------------------------------------------------------------------------------------------------------- |
| `mode`                      | Execution backend. Empty/`native` preserves current behavior.                                                   |
| `sandbox.wsl.distribution`  | Named distribution to use. Validated against `[A-Za-z0-9._-]` and may not start with `-`, so a value can never become a command-line flag of its own. |
| `sandbox.wsl.network`       | `disabled` (default): deny network access via an in-distribution network namespace when possible. `host`: inherit WSL/host networking, never isolated. |

Unknown values are rejected when config loads, naming the exact field and value.

---

## Workspace boundary

```yaml
workspace:
  root: ""          # empty = Git top-level when available, else cwd
  mode: permissive  # permissive | strict
```

The workspace root resolves once at startup: an explicit `root`
(absolute, or relative to the startup directory, and it must exist),
else the Git top-level, else the working directory. `ff doctor`
reports the resolved root and mode.

- **Permissive** (default): historical behavior. Nothing is confined;
  old configs without this block are unaffected.
- **Strict**: every filesystem tool (`read_file`, `write_file`,
  `list_files`, `search_files`, `find_files`, `secret_scan`) and the
  shell working directory resolve through one shared pipeline —
  resolve + canonicalize (symlinks/junctions included) + boundary
  check — then permission check, then execution. Relative paths anchor
  at the root; absolute paths, drive letters, UNC paths, `..`
  traversal, and symlinks resolving outside are rejected with
  `ErrWorkspaceEscape`/`ErrInvalidDir` before anything runs. The
  enforcement lives in the execution layer (`internal/sandbox` shared
  with the `wsl` path), never in prompts or tool descriptions.

Strict native enforces the same path invariant as the `wsl` path; only
the backend differs (host Bash, full environment, host network).
Command *text* is not filtered — a command may still name outside
paths; that action is gated by permissions (`ask`), exactly as in
`wsl` mode.

---

## What `wsl` mode enforces

These properties hold at boundaries Forcefield controls:

1. **Structured invocation.** Every command is assembled as an argv passed to `wsl.exe --exec`. Agent-authored text is never re-parsed by a host shell or by `cmd.exe`; there is no `cmd /c` path.
2. **Pinned working directory.** The requested directory is resolved to an absolute path, symlink-resolved, and required to lie inside the project workspace (the Git repository root, else the working directory). Traversal (`..`), absolute paths outside the workspace, drive-relative forms (`C:foo`), Linux-absolute paths on a Windows workspace, and symlinks that resolve outside are all rejected before any process exists.
3. **Severed host environment.** The `wsl.exe` launcher receives only `SystemRoot`, `TEMP`, `TMP`, and an explicitly **empty `WSLENV`**, which turns off all host-to-Linux variable sharing. Inside the distribution, the environment is the distribution's own defaults plus exactly the key/value pairs the tool requested. Provider API keys (e.g. `NVIDIA_API_KEY`) do not cross; Forcefield deliberately keeps them out of its own process environment too (see [Config](Config.md)).
4. **Network isolation when achievable.** With `network: disabled`, the command is launched inside a fresh user+network namespace via in-distribution `unshare --user --net --map-root-user`. Only loopback remains. File ownership maps back to your real user, so files created in this mode belong to you. Support is probed once per run; see below for what happens without support.
5. **Process lifetime.** Timeouts, context cancellation, and Windows-side process-tree teardown (the `wsl.exe` relay) remain fully effective. Linux-side processes inside the distribution may outlive the relay: no Windows host primitive used by Forcefield reaches inside the distribution, so their cleanup is not guaranteed. A full distribution-level sweep requires `wsl --shutdown`.

## What `wsl` mode does NOT do

Stated plainly, because these are the limits:

1. **Linux-side processes are not reaped.** Killing the Windows relay stops the Windows side promptly, but descendants running inside the distribution outlive it by platform design. This is an OS boundary, not a Forcefield bug; see item 5 above.

2. **Filesystems are not confined.** A WSL distribution automounts every Windows drive under `/mnt/<letter>` and contains its own full Linux filesystem. A sandboxed command can therefore read and write any path your OS identity permits, anywhere on the machine. Only the *working directory* is validated; nothing stops a command from opening other paths. Plain WSL cannot deliver filesystem confinement, and Forcefield will not pretend otherwise.
3. **Network denial fails closed.** If the distribution cannot create network namespaces (no `unshare`, kernel restrictions, AppArmor policy), a requested `network: disabled` makes commands **refuse to run** with an explanation - it never silently runs them with host networking. Set `network: host` if you accept unisolated networking.
4. **No resource limits.** CPU, memory, and process-count limits are not enforced.
5. **Not a security boundary against the user.** This boundary constrains what agent-driven commands can reach by default posture; it is not a defense against a local user, and it is not a malware containment system.

## Approval UX

Permission prompts render their execution block directly from the executor's `Enforcement` report, so wording always matches reality. Examples:

For native:

```text
Execution:    native
Filesystem:   host user permissions
Network:      host network
Environment:  full host environment
Isolation:    none
Note:         native execution has no isolation: commands run with your user's permissions
```

For WSL with enforced network isolation:

```text
Execution:    WSL (Ubuntu)
Filesystem:   working directory pinned to the project workspace (other paths are NOT blocked)
Network:      disabled - enforced (isolated network namespace)
Environment:  restricted (host variables are not forwarded)
Isolation:    WSL execution boundary
```

If you ever see stronger wording than this table allows, that is a bug.

## Doctor

`ff doctor` reports the configured mode, whether the backend is actually usable, the selected distribution, and every enforcement fact above. Configured-but-unavailable WSL mode fails doctor with exit code 1 and explains why; limitations print as warnings, not as all-clear lines.

---

## Security model (RC6 explicit guarantees)

- **Default posture is permissive history, not isolation.** `native` +
  `permissive` runs shell with your user's privileges and full host
  environment, and filesystem tools resolve paths as given. Do not treat
  defaults as a sandbox.
- **Reads are allowed by default but sensitive paths escalate.**
  `read_file`/`list_files`/`search_files`/`find_files`/`git`/`secret_scan`
  default to `allow` for usability; any call whose `path` (or `cwd` for
  `shell_job`) matches `IsSensitivePath` (`.env*`, keys, `.ssh/`,
  cloud credentials, etc.) forces `Ask` even under `Allow` or
  session-scoped `Always allow`. `search`/`find` additionally skip
  sensitive files during traversal. Lexical only: a renamed/symlinked
  sensitive file outside those patterns is not caught — treat as
  defense-in-depth, not a boundary.
- **Session `Always allow` is session-scoped and never persisted to
  `config.yaml`.** For `shell`/`shell_job`, `Always allow` is scoped to
  the normalized command text: approving one command does not authorize
  a different command (still gated by the interactive and WSL lexical
  refusals). Other tools without a meaningful operation identifier keep
  per-tool-name scope; `Always deny` stays per-tool-name (fail-closed).
  Sensitive-file calls still prompt even under `Always allow`.
  Cross-agent switches retain session decisions for shared tools —
  re-prompt on agent switch for high-risk tools.
- **Shell command text is never confined, in any mode.** Strict/WSL pin
  the working directory and cage filesystem *tools*; `cat /etc/passwd`
  or `/mnt/c/...` in command text is gated only by permissions (`ask`)
  plus conservative lexical refusals (interactive-TTY list, WSL
  `/mnt/`/drive/`..`/interop patterns applied to both `shell` and
  `shell_job`). Those patterns are bypassable via shell indirection
  (`$var`, quoting, `proc/self/root`) by design — documented mitigation,
  not a boundary. `Enforcement.FilesystemConfined` is always false for
  shell backends.
- **Tool output and memory are untrusted data.** Results are fenced
  (`<tool_result>`, `</tool_result>` in content escaped) and scrubbed;
  memory facts are scrubbed but injected as plain prompt text (no fence
  yet) — a malicious memory entry or repository file can attempt
  instruction override. Treat both as data, never as control plane.
- **Secrets are scrubbed deep, not shallow.** `redact.ScrubMap` recurses
  into nested maps/slices (e.g. `shell.env`), and every boundary
  (tool results, pending args, errors, session files, memory, traces,
  diagnostics) scrubs via the central `redact` package plus registered
  env values. Coverage is conservative and explicit, never perfect —
  add new shapes in `redact` with tests.

## Design notes

- Policy lives with the executor; tools send requests; the UI renders `Enforcement.SummaryLines()`.
- Adding a backend means implementing `sandbox.Executor` and extending `NewExecutor`; nothing else changes.
- The package depends only on the Go standard library.
