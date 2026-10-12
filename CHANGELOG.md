# Changelog

All notable changes to Forcefield. Dates are release publication dates (UTC). Each heading links to the GitHub comparison for that release. Pre-1.1.0 release candidates are superseded by v1.1.0 and listed without separate entries.

The format follows Keep a Changelog; versioning follows the project's published `vX.Y.Z` tags (no prerelease suffixes since v1.1.0).

## [Unreleased]

No unreleased code changes: `main` matches v1.5.2.

## [v1.5.2] - 2026-10-10

[ v1.5.2 ]: https://github.com/fabledruns/forcefield/compare/v1.5.1...v1.5.2

### Added

- Opt-in Linux filesystem isolation: `sandbox.mode: isolated` runs shell/job children under a Landlock ruleset (read-write workspace and private temp directory, read-only system paths, `/proc` and `$HOME` denied) plus `no_new_privs`, with always-pinned working directory and per-execution private `TMPDIR`. Refuses to run where Landlock is unavailable; refuses to construct on Windows/macOS. Optional `sandbox.isolated.read/write` extras.
- Sandbox boundary-test harness with `FF_REQUIRE_BOUNDARY` fail-closed gating, Landlock ABI probe reporting, spawn-site audit, and a three-OS sandbox-probe CI job with required-mode checks.

### Security

- Native shell/job children no longer inherit the provider credential variables Forcefield itself reads (`api_key_env` names plus legacy key); explicit per-command `env` still applies. Prompt-injection-driven `env`/`printenv` exposure of these keys is closed at the inheritance point.

### Fixed

- Required-mode CI no longer mistakes "no tests to run" for a pass: the fail-closed branch runs a gate test present on every platform.

## [v1.5.1] - 2026-10-06

[ v1.5.1 ]: https://github.com/fabledruns/forcefield/compare/v1.5.0...v1.5.1

### Changed

- HPOV benchmark suite extracted into its own repository (launch, startup, TUI-timeline, memory, calibration, methodology docs).
- TUI: Crush-style status strip, version row on the splash, interactive command palette (`/` filters, arrows move with wraparound).
- Release builds embed the version through Makefile ldflags.

### Fixed

- `WriteFile` and filesystem reads report close errors instead of dropping them.

## [v1.5.0] - 2026-10-01

[ v1.5.0 ]: https://github.com/fabledruns/forcefield/compare/v1.4.2...v1.5.0

### Added

- One hardened filesystem path shared by reads, writes, listings, search, and secret scans: workspace resolution, no-follow opens, descriptor checks (regular file, size cap, single link for writes), bounded I/O, `fchmod`. FIFOs, sockets, and devices refused.
- Git runs neutralized under the full process lifecycle: minimal environment, `-c core.fsmonitor=` on every invocation, `--no-textconv`/`--no-ext-diff` on diffs, per-repository content-filter neutralization.
- Structured sandbox reporting: `ff doctor` renders enforcement facts as info and limits as warnings, names the WSL distribution, and runs a safe interop canary.
- Pinned permission precedence (boundary denial outranks every allow; headless Ask fails closed), covered by tests.

### Fixed

- WSL `network: disabled` qualified to Linux sockets in approval and doctor output; Windows `.exe` interop keeps host networking and is refused lexically plus reported by the canary.
- Refused file writes no longer truncate before validation; denied multi-link/special-file writes leave content intact.
- Stale process-lifecycle claims corrected; lifecycle tests made hermetic.

## [v1.4.2] - 2026-10-01

[ v1.4.2 ]: https://github.com/fabledruns/forcefield/compare/v1.4.1...v1.4.2

### Security

- Provider configs reject `Authorization`, `x-api-key`, `x-goog-api-key`, and `api-key` headers in any casing, so a config value can no longer silently replace the resolved credential or store a live key in `config.yaml`.
- Sensitive-path escalation covers foreground `shell` commands and MCP tool arguments: an allowed shell or MCP call can no longer reach credential files without prompting.
- Shell `env` keys validated (`[A-Za-z_][A-Za-z0-9_]*`) before process setup on every backend; malformed keys rejected instead of reaching `/usr/bin/env`.

### Fixed

- Provider streams time out after 60 seconds without body data instead of holding the turn forever.
- Headless `ff run` prints MCP warnings to stderr; MCP shutdown drains unread stderr so fast-exiting servers keep their diagnostics.

## [v1.4.1] - 2026-09-29

[ v1.4.1 ]: https://github.com/fabledruns/forcefield/compare/v1.4.0...v1.4.1

### Fixed

- Failed session saves latch a sticky degraded state and cancel the run: no further side-effecting tools execute against a stale file, and the run exits non-zero.
- New `ExitUnverified` (5): `EventDone` exits 0 only with verified status; `ff supervise` parks on unverified completions.
- Live shell stream output shares the tool byte budget with a single truncation marker.
- Headless permission prompts no longer print raw arguments; the TUI approval modal scrubs non-string argument values.

## [v1.4.0] - 2026-09-29

[ v1.4.0 ]: https://github.com/fabledruns/forcefield/compare/v1.3.1...v1.4.0

### Added

- Local MCP servers over stdio (`mcp__<server>__<tool>`), with `/mcp` listing, `ff doctor` last-known state, warn-and-continue on individual server failure, and crash-safe status persistence (mode 0600).
- Pure-Go upward `.git` search for project-root detection, falling back to `git rev-parse` only for ambiguous layouts (worktrees, submodules, `GIT_*` overrides).

### Changed

- Startup roughly halved on the measured machine (`ff --version` ~66 ms to ~39 ms median) by removing per-invocation process-list walks, duplicate config parsing, and redundant probes. Reference numbers from the release notes, not guarantees.

### Fixed

- MCP status failure test corrected for Linux `ENOTDIR`-vs-`ENOENT` behavior.
- Git parity coverage across plain, nested, worktree, submodule, symlinked, and malformed layouts.

## [v1.3.1] - 2026-09-21

[ v1.3.1 ]: https://github.com/fabledruns/forcefield/compare/v1.3.0...v1.3.1

### Changed

- TUI-first startup: the interface renders before the runtime finishes initializing; input stays gated until ready, quit stays available during init.

## [v1.3.0] - 2026-09-18

[ v1.3.0 ]: https://github.com/fabledruns/forcefield/compare/v1.2.1...v1.3.0

### Added

- Slash commands: `/plan` (draft without touching files) and `/build`, `/usage`, `/context`, `/diff`, `/git`, `/jobs`, `/cancel`, `/compact`; `/status` shows the active plan.
- Redesigned installers: two-tone plan output, skip when the version is installed, silent downgrades refused without `--force`/`-Force`, Windows `-RemovePath` opt-in.

## [v1.2.1] - 2026-09-18

[ v1.2.1 ]: https://github.com/fabledruns/forcefield/compare/v1.2.0...v1.2.1

### Added

- llama.cpp provider for local models through a llama-server endpoint.

### Security

- Filesystem workspace boundary hardened against traversal, symlink/junction escapes, and Windows-specific escape shapes, with expanded regression coverage.

## [v1.2.0] - 2026-09-15

[ v1.2.0 ]: https://github.com/fabledruns/forcefield/compare/v1.1.1...v1.2.0

### Added

- Ripgrep-powered `search_code` tool.

### Fixed

- Premature repeated-tool execution stops in the runtime loop.

## [v1.1.1] - 2026-09-12

[ v1.1.1 ]: https://github.com/fabledruns/forcefield/compare/v1.1.0...v1.1.1

### Fixed

- Patch release; no user-facing changes documented beyond the comparison linked above.

## [v1.1.0] - 2026-09-12

[ v1.1.0 ]: https://github.com/fabledruns/forcefield/compare/v1.0.0...v1.1.0

### Added

- First full release: local-first agent runtime (no account, cloud, or telemetry), Ollama/LM Studio/NVIDIA NIM/OpenAI/Anthropic/Gemini/xAI/OpenRouter/Groq/Mistral/Together/OpenAI-compatible providers, persistent sessions and memory, skills, permission system with persisted fail-closed rules, native and WSL sandboxed execution, cancellation with process-tree cleanup, secret redaction, read-only Git tooling, long-running tasks with crash-safe state, Bubble Tea/Lipgloss/Glamour TUI.
- `ff doctor` diagnostics (config, providers, sessions, skills, memory, shell backend, sandbox readiness).
- CUE schemas for `config.yaml`, validated in CI against valid/invalid fixtures.

### Fixed

- CodeQL dead-store alerts in shell execution and doctor probing addressed; writable close errors handled; Go version matrix CI restored with gofmt enforcement.

## [v1.0.0] - 2026-08-23

[ v1.0.0 ]: https://github.com/fabledruns/forcefield/releases/tag/v1.0.0

### Added

- Beta milestone: WSL sandboxed shell execution (pinned cwd, severed host env, network namespace with fail-closed deny, no silent fallback), atomic session and config persistence, session-ID traversal rejection, `ff doctor`, mouse-enabled TUI with `/status` and `/tools`, CUE-validated config, race-enabled CI on Linux/Windows/macOS.
