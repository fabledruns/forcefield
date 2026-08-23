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

## What `wsl` mode enforces

These properties hold at boundaries Forcefield controls:

1. **Structured invocation.** Every command is assembled as an argv passed to `wsl.exe --exec`. Agent-authored text is never re-parsed by a host shell or by `cmd.exe`; there is no `cmd /c` path.
2. **Pinned working directory.** The requested directory is resolved to an absolute path, symlink-resolved, and required to lie inside the project workspace (the Git repository root, else the working directory). Traversal (`..`), absolute paths outside the workspace, drive-relative forms (`C:foo`), Linux-absolute paths on a Windows workspace, and symlinks that resolve outside are all rejected before any process exists.
3. **Severed host environment.** The `wsl.exe` launcher receives only `SystemRoot`, `TEMP`, `TMP`, and an explicitly **empty `WSLENV`**, which turns off all host-to-Linux variable sharing. Inside the distribution, the environment is the distribution's own defaults plus exactly the key/value pairs the tool requested. Provider API keys (e.g. `NVIDIA_API_KEY`) do not cross; Forcefield deliberately keeps them out of its own process environment too (see [Config](Config.md)).
4. **Network isolation when achievable.** With `network: disabled`, the command is launched inside a fresh user+network namespace via in-distribution `unshare --user --net --map-root-user`. Only loopback remains. File ownership maps back to your real user, so files created in this mode belong to you. Support is probed once per run; see below for what happens without support.
5. **Process lifetime.** Timeouts, context cancellation, and process-tree teardown remain fully effective inside the namespace.

## What `wsl` mode does NOT do

Stated plainly, because these are the limits:

1. **Filesystems are not confined.** A WSL distribution automounts every Windows drive under `/mnt/<letter>` and contains its own full Linux filesystem. A sandboxed command can therefore read and write any path your OS identity permits, anywhere on the machine. Only the *working directory* is validated; nothing stops a command from opening other paths. Plain WSL cannot deliver filesystem confinement, and Forcefield will not pretend otherwise.
2. **Network denial fails closed.** If the distribution cannot create network namespaces (no `unshare`, kernel restrictions, AppArmor policy), a requested `network: disabled` makes commands **refuse to run** with an explanation - it never silently runs them with host networking. Set `network: host` if you accept unisolated networking.
3. **No resource limits.** CPU, memory, and process-count limits are not enforced.
4. **Not a security boundary against the user.** This boundary constrains what agent-driven commands can reach by default posture; it is not a defense against a local user, and it is not a malware containment system.

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

## Design notes

- Policy lives with the executor; tools send requests; the UI renders `Enforcement.SummaryLines()`.
- Adding a backend means implementing `sandbox.Executor` and extending `NewExecutor`; nothing else changes.
- The package depends only on the Go standard library.
