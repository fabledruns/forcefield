# Security Policy

This document describes what Forcefield does and does not defend against, and how to report a vulnerability. It tracks the implementation, not aspirations: where a control is partial, that is stated.

## Supported versions

Security fixes ship as new releases. Run the latest release and upgrade before reporting:

```bash
ff --version
```

```bash
curl -fsSL https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/install.sh | sh
```

Only the latest release receives fixes. Older releases get no backports.

## Reporting a vulnerability

Use GitHub private vulnerability reporting so details stay non-public until a fix ships:

`https://github.com/fabledruns/forcefield/security/advisories/new`

There is no separate security email. Do not open a public issue for an unpatched vulnerability, and do not post exploit details, live credentials, or private user data anywhere public.

A useful report includes:

- The output of `ff --version` and your OS and architecture.
- What you did, what you expected, and what happened instead.
- The impact as you see it: what an attacker (or a compromised model output) can read, change, or execute.
- A minimal reproduction with synthetic data. Redact real keys, tokens, file contents, and paths that identify you or your systems.
- Your config with secrets removed (`~/.forcefield/config.yaml` structure, never values).

Reports are reviewed against the running code, fixed in a release, and documented in `CHANGELOG.md`. If a report turns out to describe documented behavior rather than a defect, the response says so with a pointer to the relevant docs.

## What Forcefield defends

Forcefield is the controlled runtime between a model and the developer's machine. Its controls constrain what agent-driven commands reach by default. They are not malware containment and not a defense against a local attacker who already runs code as you.

**Permissions.** Every tool call passes `allow`, `ask`, or `deny` before it executes. Unknown tools default to `ask`. Headless runs fail closed on `ask`. Workspace-boundary denials outrank every allow, including session `Always allow`. These rules are pinned by tests in `internal/permissions` and `internal/runtime`.

**Filesystem tools.** `read_file`, `write_file`, `list_files`, search tools, and `secret_scan` resolve every path inside the workspace root (symlinks and Windows junctions included) and refuse escapes before any permission prompt. No approval overrides a boundary denial.

**Shell backends.** `native` mode runs with your user privileges and is never described as isolated. `wsl` mode (Windows only) pins the working directory, severs host variable sharing at the launcher, and optionally isolates Linux sockets; it never falls back to `native` silently, and it refuses to run when the requested isolation cannot be established. `isolated` mode (Linux only, opt-in via `sandbox.mode: isolated`) adds kernel-enforced Landlock filesystem confinement plus `no_new_privs`, and likewise refuses to run when the kernel cannot provide it. All three backends fail closed instead of substituting a weaker one: a policy that cannot be honored is an error (`ErrBackendUnavailable`), never a silent downgrade. Exact guarantees per mode live in `docs/Sandbox.md`.

**Credential handling.** Keys read from `.env` files stay in memory and never enter `os.Environ`, and native shell/job children no longer inherit the credential variables Forcefield itself reads (`internal/config`, `internal/sandbox`). The `internal/redact` package scrubs registered secrets from tool results, arguments, errors, session files, memory, traces, and diagnostics, and `ff doctor` never prints secret values.

**Session durability.** Session state writes use temp file plus fsync plus rename, with Windows rename retries; interrupted runs record incomplete work instead of reporting success. Covered by durability tests in `internal/session`.

**Supply chain.** Release binaries ship with `checksums.txt`, verified by the installers. CI runs CodeQL, gitleaks, `go vet`, the full test suite with `-race` on Linux, Windows, and macOS, gofmt enforcement, and CUE schema validation.

## What Forcefield does not defend

- **Prompt injection.** Tool output, MCP output, memory entries, and repository files are untrusted data that can carry instructions. Forcefield fences and scrubs them as data; it does not and cannot distinguish a malicious instruction from a legitimate one. Treat everything the model reads as attacker-influenced.
- **Secret redaction is best-effort.** Redaction covers recognized secrets in the places listed above. It does not guarantee every secret-shaped value is caught. Never paste live credentials into prompts, tool arguments, memory, or issues.
- **WSL lexical filtering is not a boundary.** The `/mnt/`, drive-letter, and interop pattern refusals are conservative mitigations. Quoting, variables, and indirection bypass them by design; only the working-directory pin and the restricted launcher are structural.
- **`isolated` mode is filesystem-only.** Host networking, process groups, CPU/memory/PID limits, macOS, Windows, MCP servers, and `.git/hooks` inside a writable workspace are outside it. `docs/Sandbox.md` lists each residual.
- **Kernel and local-user attacks.** Landlock and namespaces reduce reach but are not a hypervisor, and nothing here resists a malicious local user or a compromised kernel.

## After a fix ships

Upgrade with the installer commands above (add `--force` / `-Force` to reinstall the same version). Downgrades are refused unless forced. Release artifacts keep the same names per platform (`ff-linux-amd64`, `ff-darwin-arm64`, `ff-windows-amd64.exe`, and the rest); verify `checksums.txt` as the installer does.
