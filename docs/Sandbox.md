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
- Host variables Forcefield itself reads as provider credentials (the configured `api_key_env` names plus the legacy `NVIDIA_API_KEY`) are removed from shell/job children; explicit per-command `env` still applies. This is hygiene against accidental leakage, **not** a boundary: a determined same-user child can still read the parent's environment through the OS.
- On Windows, commands are relayed through `wsl.exe` purely so GNU Bash exists; this relay is an availability mechanism, **not** a security boundary. The host environment (minus Forcefield's own credential variables, as above) flows to `wsl.exe`, and the distribution can access everything your Windows user can.
- Commands run with your user's permissions on your whole machine.

Native mode is never described as sandboxed anywhere in the UI.

### `wsl`

Commands execute inside a WSL distribution under an explicitly restricted invocation. Requires Windows. If WSL is unavailable or misconfigured, Forcefield **fails with a clear error and never falls back to native execution**.

### `isolated` (opt-in, Linux only)

Commands execute under a kernel-enforced Landlock filesystem ruleset plus `no_new_privs`, applied by a re-executed helper (`ff __sandbox-exec`) that then becomes the shell. Requires Linux with a Landlock-capable kernel (5.13+ with Landlock enabled; ABI 2+ recommended so cross-directory renames keep working). If Landlock is unavailable, Forcefield **fails with a clear error and never falls back to native execution**. On Windows or macOS the mode exists in configuration but refuses to construct.

---

## Capability matrix (v1.5.0)

One row per supported configuration. Every cell states what is
actually enforced; the [Known limitations](#known-limitations-v150)
below qualify each `partial` and `no`.

| Configuration | Shell cwd | Shell text | FS tools | Network | Environment | Process tree |
|---|---|---|---|---|---|---|
| Windows `native`, permissive | unpinned | open | caged | host | full host | Job + taskkill, suspended start |
| Windows `native`, strict | pinned | open | caged | host | full host | Job + taskkill, suspended start |
| Windows `wsl`, `network: disabled` | pinned | open (lexical mitigation) | caged | loopback-only for Linux sockets; `.exe` interop keeps host net | restricted launcher | Windows side reaped; Linux side may outlive |
| Windows `wsl`, `network: host` | pinned | open (lexical mitigation) | caged | shared WSL/host | restricted launcher | Windows side reaped; Linux side may outlive |
| Linux/macOS (`native` ± strict) | pinned iff strict | open | caged | host | full host | process group, no Start→Track gap |
| Linux `isolated` | pinned | confined (workspace/tmp writes, system read-only) | caged | host | full host minus credential vars | process group, no Start→Track gap |
| MCP servers (any mode) | n/a (launch dir only) | opaque args | **not confined** | host | minimal allowlist | bounded shutdown, Phase 4 lifecycle |

`caged` = confined to the workspace root via the shared
resolve+canonicalize+boundary pipeline with descriptor-checked,
no-follow opens. `open` = never confined; gated by permissions
(`ask`) plus conservative lexical refusals where noted.
`ff doctor` renders this same matrix from the executor's own
`Enforcement` report: facts as info lines, limits as warnings.

## Configuration

```yaml
sandbox:
  mode: native          # native | wsl | isolated (isolated = Linux only)
  wsl:
    distribution: ""    # "" = system default distribution
    network: disabled   # disabled | host
  isolated:
    read: []            # extra read-only paths beyond the built-in system set
    write: []           # extra writable paths beyond workspace + private tmp
```

| Field                       | Meaning                                                                                                        |
| --------------------------- | -------------------------------------------------------------------------------------------------------------- |
| `mode`                      | Execution backend. Empty/`native` preserves current behavior.                                                   |
| `sandbox.wsl.distribution`  | Named distribution to use. Validated against `[A-Za-z0-9._-]` and may not start with `-`, so a value can never become a command-line flag of its own. |
| `sandbox.wsl.network`       | `disabled` (default): deny network access via an in-distribution network namespace when possible (Linux sockets only; Windows `.exe` interop keeps host networking). `host`: inherit WSL/host networking, never isolated. |
| `sandbox.isolated.read`     | Extra read-only paths granted in `isolated` mode (canonicalized, must exist; absolute paths recommended — relative entries resolve against the process working directory). Ignored by other backends. |
| `sandbox.isolated.write`    | Extra writable paths granted in `isolated` mode (same resolution rules as `read`). Ignored by other backends. |

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

- **Permissive** (default): filesystem tools stay confined to the
  root, exactly as in strict mode; only shell working-directory
  pinning is off, preserving historical shell behavior.
  Old configs without this block are unaffected.
- **Strict**: every filesystem tool (`read_file`, `write_file`,
  `list_files`, `search_files`, `search_code`, `find_files`, `secret_scan`) and the
  shell working directory resolve through one shared pipeline —
  resolve + canonicalize (symlinks/junctions included) + boundary
  check — then permission check, then execution. Relative paths anchor
  at the root; absolute paths, drive letters, UNC paths, `..`
  traversal, and symlinks resolving outside are rejected with
  `ErrWorkspaceEscape`/`ErrInvalidDir` before anything runs. The
  enforcement lives in the execution layer (`internal/sandbox` shared
  with the `wsl` path), never in prompts or tool descriptions.

Strict native enforces the same shell path invariant as the `wsl`
path; only the backend differs (host Bash, full environment, host
network). Filesystem tools enforce the workspace boundary in every
mode, not just strict: outside paths are denied before any permission
prompt, and approval — one-shot or Always allow — can never override
the denial. Permission prompts show the canonical resolved path so a
model spelling can never hide the real target.
Command *text* is not filtered — a command may still name outside
paths; that action is gated by permissions (`ask`), exactly as in
`wsl` mode.

Search execution: `search_code` spawns the host `rg` binary directly
(never through a shell or the WSL relay) in every mode, against the
same caged root. In `wsl` mode shell commands run in-distribution
while search runs on the host; the workspace pinning is identical
because both resolve through `policy.Workspace`.

---

## What `wsl` mode enforces

These properties hold at boundaries Forcefield controls:

1. **Structured invocation.** Every command is assembled as an argv passed to `wsl.exe --exec`. Agent-authored text is never re-parsed by a host shell or by `cmd.exe`; there is no `cmd /c` path.
2. **Pinned working directory.** The requested directory is resolved to an absolute path, symlink-resolved, and required to lie inside the project workspace (the Git repository root, else the working directory). Traversal (`..`), absolute paths outside the workspace, drive-relative forms (`C:foo`), Linux-absolute paths on a Windows workspace, and symlinks that resolve outside are all rejected before any process exists.
3. **Severed host environment.** The `wsl.exe` launcher receives only `SystemRoot`, `TEMP`, `TMP`, and an explicitly **empty `WSLENV`**, which turns off all host-to-Linux variable sharing. Inside the distribution, the environment is the distribution's own defaults plus exactly the key/value pairs the tool requested. Provider API keys (e.g. `NVIDIA_API_KEY`) do not cross; Forcefield deliberately keeps them out of its own process environment too (see [Config](Config.md)).
4. **Network isolation for Linux sockets when achievable.** With `network: disabled`, the command is launched inside a fresh user+network namespace via in-distribution `unshare --user --net --map-root-user`. Only loopback remains **for Linux sockets**. Windows `.exe` interop is explicitly not covered: interop executables (`cmd.exe`, `powershell.exe`, `curl.exe`, …) still run inside that namespace on the Windows host network stack, even with an empty environment. File ownership maps back to your real user, so files created in this mode belong to you. Support is probed once per run; see below for what happens without support. `ff doctor` runs a safe resolution canary (`command -v` only, no traffic) reporting which helpers resolve inside the namespace.
5. **Process lifetime.** Timeouts, context cancellation, and Windows-side process-tree teardown (the `wsl.exe` relay) remain fully effective. Linux-side processes inside the distribution may outlive the relay: no Windows host primitive used by Forcefield reaches inside the distribution, so their cleanup is not guaranteed. A full distribution-level sweep requires `wsl --shutdown`.

## What `wsl` mode does NOT do

Stated plainly, because these are the limits:

1. **Linux-side processes are not reaped.** Killing the Windows relay stops the Windows side promptly, but descendants running inside the distribution outlive it by platform design. This is an OS boundary, not a Forcefield bug; see item 5 above.

2. **Filesystems are not confined.** A WSL distribution automounts every Windows drive under `/mnt/<letter>` and contains its own full Linux filesystem. A sandboxed command can therefore read and write any path your OS identity permits, anywhere on the machine. Only the *working directory* is validated; nothing stops a command from opening other paths. Plain WSL cannot deliver filesystem confinement, and Forcefield will not pretend otherwise.
3. **Network denial fails closed (for what it covers).** If the distribution cannot create network namespaces (no `unshare`, kernel restrictions, AppArmor policy), a requested `network: disabled` makes commands **refuse to run** with an explanation - it never silently runs them with host networking. Set `network: host` if you accept unisolated networking. Either way, Windows `.exe` interop keeps host networking: fail-closed governs Linux networking only.
4. **No resource limits.** CPU, memory, and process-count limits are not enforced.
5. **Not a security boundary against the user.** This boundary constrains what agent-driven commands can reach by default posture; it is not a defense against a local user, and it is not a malware containment system.

## What `isolated` mode enforces

These properties hold on Linux under a Landlock-capable kernel:

1. **Kernel-enforced filesystem confinement.** The shell (and everything it spawns, including grandchildren) can write only inside the workspace and a per-execution private temp directory (`TMPDIR` points at it — always, even over an explicit `TMPDIR` — and it is removed afterwards; a SIGKILL of Forcefield itself can leak it, like any temp file). Reads add the system paths Bash and common tools need (`/usr`, `/bin`, `/lib`, `/lib64`, `/sbin`, `/etc`, `/dev`, skipping entries that do not exist) plus a narrow read+write grant on `/dev/null`. Denied opens fail with `EACCES` regardless of how the path is spelled — no lexical check is involved.
2. **Pinned working directory, always.** Unlike `native`, `isolated` pins the shell working directory to the workspace even in permissive mode.
3. **No new privileges.** `PR_SET_NO_NEW_PRIVS` is set before the shell starts, so setuid binaries and file capabilities cannot escalate the confined command.
4. **Fail-closed setup.** A kernel without Landlock, a failed ruleset, or any helper error makes commands **refuse to run** with an explanation — it never silently runs them unconfined. `ff doctor` reports the detected Landlock ABI and whether confinement is in effect.
5. **Credential hygiene.** The provider credential variables Forcefield itself reads are removed from the child environment (explicit per-command `env` still applies, except `TMPDIR`, which always points at the private temp directory). As in every mode, this is hygiene, not a boundary: a determined same-user child can read the parent's environment through the OS.

## What `isolated` mode does NOT do

Stated plainly, because these are the limits:

1. **No network, PID, or resource isolation.** Commands keep host networking, run as your user outside any PID namespace (a `setsid` child still escapes process-group cleanup), and have no CPU/memory limits. These are separate milestones.
2. **No macOS or Windows coverage.** The mode refuses to construct there; use `wsl` on Windows.
3. **Workspace `.git/hooks` (and similar) are not protected by Landlock.** Landlock cannot deny a subdirectory of an allowed hierarchy, so a shell command can write `workspace/.git/hooks/*`, `.vscode/tasks.json`, or other later-executing files. Treat hook-adjacent writes as untrusted: review diffs before committing or running task definitions the agent touched.
4. **Approved network use is unconfined.** A command allowed to reach the network can exfiltrate through it; Landlock governs filesystem access only.
5. **Newer-kernel access rights stay permitted.** The ruleset requests only the filesystem rights understood through Landlock ABI 3 (plus nothing newer); per Landlock semantics, rights introduced by later ABIs (device ioctls, scopes, network controls) are allowed, not denied.
6. **Not a security boundary against the user.** Same posture statement as `wsl` mode: it constrains agent-driven commands by default, it is not malware containment, and kernel bugs are out of scope.

## Migrating tools to `isolated` mode

Tools that assume a full home directory need explicit accommodation, because `$HOME` (and `/proc`) are denied:

- **Git identity:** pass `-c user.name=... -c user.email=...` (or `GIT_AUTHOR_*`/`GIT_COMMITTER_*`) instead of relying on `~/.gitconfig`. Repository operations themselves need no home access.
- **Caches (npm/pip/cargo):** point them at the workspace or add `sandbox.isolated.write` entries for cache directories you accept sharing.
- **Localhost services:** there is no network namespace, so loopback works exactly as on the host — nothing to change.
- **`/proc`-dependent tools** (`ps`, `top`, some runtimes): they see `EACCES`. Prefer tool-native status (job polling, exit codes) or run those commands in `native` mode.
- **Providers and doctor:** `ff doctor` tells you whether your kernel enforces the boundary; if it reports unavailable, commands refuse to run until you switch back to `native`.

## Known limitations (v1.5.0)

The complete residual list. Each item is enforced in code or tests
only as far as stated here — nothing beyond this list is claimed.

- **Windows has no `O_NOFOLLOW` or link-count equivalence.** Opens
  refuse pre-existing symlinks via `Lstat` plus workspace
  pre-resolution, but a link swapped between check and open is not
  stopped (Unix closes this with `O_NOFOLLOW`). Hard-link writes are
  refused on Unix only; Windows cannot count links with the standard
  library. Hard-link *reads* are allowed everywhere by policy.
- **Residual `MkdirAll` TOCTOU.** Parent creation itself cannot be
  atomic; post-creation re-validation turns a swap into an error
  before anything opens, but the window exists.
- **Process limits: `setsid`, SIGHUP.** A child calling `setsid`
  leaves the Unix process group by kernel design. Dying by OS signal
  (`SIGKILL`, `SIGHUP` on terminal close) bypasses all teardown —
  long sessions belong under `tmux`/`nohup`. See
  [Recovery](Recovery.md).
- **WSL Windows-side networking is not isolated,** and large
  commands stage to a `%TEMP%` script visible via `/mnt` (leaked if
  the process is SIGKILLed before cleanup).
- **MCP is UNSANDBOXED** in every mode (warn, never refuse — see
  [MCP](MCP.md)). Server stderr echoing a passthrough variable is
  not redacted; never pass secrets through.
- **No CPU/memory/PID limits**, and **no macOS isolation backend**:
  macOS runs on the host with the shared path cage plus honest
  reporting. Linux gains opt-in filesystem confinement via
  [`isolated` mode](#isolated-opt-in-linux-only), which still has no
  network, PID, or resource isolation.

## What v1.5.0 hardened (and what it did not change)

- **Filesystem (one hardened path):** resolve → no-follow open →
  descriptor checks (regular file, size cap) → bounded
  context-aware reads/writes → `fchmod`. Non-regular files
  (FIFO/socket/device) are refused; multi-link writes are refused
  where countable; `secret_scan` and `search_files` share the path.
- **Helpers:** `git` runs under the full process lifecycle with a
  minimal environment, `-c core.fsmonitor=` on every invocation,
  and `--no-textconv`/`--no-ext-diff` on all diffs;
  `search_code` tracks its tree post-start like the shell;
  `taskkill.exe` resolves via System32, not `PATH`.
- **Lifecycle:** Windows children start suspended and resume after
  job assignment (no Start→Track gap); output lines cap at 256 KiB;
  `Runtime.Close` reaps jobs then MCP; TUI quit waits bounded (5s);
  headless runs close on exit.
- **Reporting:** `Enforcement.Limitations` is the structured source;
  doctor renders facts as info and limits as warnings (no substring
  matching); the WSL interop canary observes `command -v` only.
- **Permissions:** boundary denial outranks every allow
  (boundary > session Deny > session Allow > persisted Check >
  sensitive escalation > Ask); headless Ask fails closed; MCP and
  shell-text refusals hold regardless of grants.
- **Unchanged by design:** no container backend, no Linux/macOS
  isolation, no MCP sandboxing, no Windows egress control, no
  resource limits. The milestone name "Full Sandbox" means the
  boundary is complete and honest — not airtight.

## Approval UX

Permission prompts render their execution block directly from the executor's `Enforcement` report, so wording always matches reality. Examples:

For native:

```text
Execution:    native
Filesystem:   host user permissions
Network:      host network
Environment:  full host environment minus Forcefield credential variables
Isolation:    none
Note:         native execution has no isolation: commands run with your user's permissions
```

For WSL with enforced network isolation:

```text
Execution:    WSL (Ubuntu)
Filesystem:   working directory pinned to the project workspace (other paths are NOT blocked)
Network:      disabled - enforced for Linux sockets (isolated network namespace)
Environment:  restricted (host variables are not forwarded)
Isolation:    WSL execution boundary
```

If you ever see stronger wording than this table allows, that is a bug.

## Doctor

`ff doctor` reports the configured mode, whether the backend is actually usable, the selected distribution, and every enforcement fact above. Configured-but-unavailable WSL mode fails doctor with exit code 1 and explains why; limitations print as warnings, not as all-clear lines. In `network: disabled` mode doctor additionally runs the interop canary and reports which Windows helpers resolve inside the namespace.

---

## Security model (RC6 explicit guarantees)

- **Precedence is fixed: boundary > session Deny > session Allow >
  persisted Check > sensitive escalation > Ask.** The workspace
  boundary pre-flight denies escapes before any prompt, and no
  one-shot or Always allow — session or persisted — can authorize
  them; denials surface as tool failures, not permission denials.
  Headless runs with no asker fail closed.
- **Default posture confines file tools, not the shell.** `native` +
  `permissive` runs shell with your user's privileges and full host
  environment. Filesystem tools are always confined to the workspace
  root: paths are canonicalized and resolved inside it before any
  permission prompt, and re-checked at execution (symlinks, junctions,
  TOCTOU). Outside paths are denied fail-closed. Do not treat the
  shell default as a sandbox.
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
   Per-tool-name Always allow stays safe for filesystem tools because
   the workspace boundary denies outside paths regardless of approval.
   Sensitive-file calls still prompt even under `Always allow`.
  Cross-agent switches retain session decisions for shared tools —
  re-prompt on agent switch for high-risk tools.
- **Shell command text is never confined in `native` or `wsl` mode.**
  `isolated` mode confines it with Landlock (see above). Strict/WSL pin
  the working directory; filesystem *tools* are caged in every mode.
  `cat /etc/passwd`
  or `/mnt/c/...` in command text is gated only by permissions (`ask`)
  plus conservative lexical refusals (interactive-TTY list, WSL
  `/mnt/`/drive/`..`/interop patterns applied to both `shell` and
  `shell_job`). Those patterns are bypassable via shell indirection
  (`$var`, quoting, `proc/self/root`) by design — documented mitigation,
  not a boundary. `Enforcement.FilesystemConfined` is false for shell
  backends except `isolated` mode when Landlock support probes OK.
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
- The package depends only on the Go standard library plus `golang.org/x/sys` (already a module dependency) for the Linux Landlock syscalls; no cgo, no new modules.

## Boundary algorithm notes

All filesystem and shell-cwd checks share one pipeline: resolve +
canonicalize (symlinks and Windows junctions included) + boundary
check, then permission check, then execution.

- Relative paths anchor at the workspace, never at Forcefield's cwd.
  Absolute paths, drive letters, UNC paths, `..` traversal, and
  symlinks resolving outside are rejected with
  `ErrWorkspaceEscape`/`ErrInvalidDir` before anything runs.
- The workspace has two valid spellings after resolution (for example
  `/var/x` vs `/private/var/x` on macOS, long vs 8.3 names on
  Windows). Containment accepts either known spelling and nothing
  else; resolve first, classify second.
- `EnsureWithinWorkspace` covers not-yet-existing creation targets by
  walking existing ancestors for symlink escapes.
- `EvalLinks` follows symlinks **and** NTFS junctions/mount points
  (`filepath.EvalSymlinks` skips junctions; `os.Readlink` catches
  what is left, leftmost-first with a depth guard).
- On Windows, Linux-absolute (`/home/...`), drive-relative (`C:foo`),
  and bare-drive (`C:`) forms are treated as absolute and refused as
  escapes rather than guessed.
- Comparisons are boundary-aware and case-insensitive on Windows
  volumes, exact elsewhere.
