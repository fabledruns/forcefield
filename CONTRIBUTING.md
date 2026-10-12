# Contributing to Forcefield

Forcefield welcomes bug reports, documentation fixes, test coverage, and small focused code changes. Large features land best as a short discussion first: open an issue describing the problem and the proposed shape before writing code.

## Prerequisites

- Go 1.26.4 or newer (`go.mod` pins the toolchain; `go version` to check).
- Git with a checkout of the repository.
- Bash for `scripts/ci-local.sh` (Git Bash on Windows).
- `cue` for schema checks. CI installs it; locally the same script installs it with `go install` when missing.
- `golangci-lint` only if you run `make lint`; CI does not run it.

## Setup and build

```bash
git clone https://github.com/fabledruns/forcefield.git
cd forcefield
go mod download
go build -o ff .
```

Windows:

```powershell
go build -o ff.exe .
```

Run diagnostics against your setup with `./ff doctor`.

## Required checks

CI (`.github/workflows/test.yml`) runs these on Linux, Windows, and macOS. Run them locally before opening a pull request:

```bash
gofmt -l .            # must print nothing except the pre-existing main.go exception
go vet ./...
go test ./...
go test -race ./...
go build -v ./...
```

`scripts/ci-local.sh` runs the same sequence from a Bash shell:

```bash
bash scripts/ci-local.sh
```

Two more gates run in CI:

- Command-package coverage stays at or above 30%:
  ```bash
  make coverage-gate
  ```
- CUE schemas validate, and valid fixtures pass while invalid fixtures are rejected:
  ```bash
  cd cue && cue vet .
  ```
  (`make cue-check` covers the main schemas.)

## Running relevant tests

`go test ./...` is the baseline. When you touch an area, also run its focused suites, including race where the code is concurrency-sensitive:

| Area changed | Run |
|---|---|
| Permissions, scheduling, agent loop | `go test ./internal/permissions/ ./internal/runtime/ -count=1` |
| Shell execution, background jobs | `go test ./internal/tools/shell/ -count=1` |
| Sandbox backends, workspace boundary | `go test ./internal/sandbox/... -count=1` |
| Redaction, secrets handling | `go test ./internal/redact/ ./internal/hardening/ -count=1` |
| Providers, models, discovery | `go test ./internal/providers/ ./internal/config/ -count=1` |
| Sessions, persistence, recovery | `go test ./internal/session/ ./internal/runtime/ -count=1` |
| Installer scripts | `scripts/tests/` harnesses for the script you touched |

The `internal/hardening` package holds failure-injection and regression tests. Every behavior fix there cites the test that reproduces the original finding; follow that pattern.

## Expectations for changes

- Read `docs/` before changing architecture; `AGENTS.md` and `docs/Index.md` are the entry points. If implementation and docs disagree, ask rather than silently changing architecture.
- Keep changes small and incremental. One concern per pull request; no unrelated refactors.
- Add or extend a regression test for every behavior change, and prove it fails without the fix where practical.
- Never `panic` on expected failures. Wrap errors with context (`fmt.Errorf("loading provider: %w", err)`) so messages tell the user how to fix the issue.
- Security-sensitive changes (permissions, sandboxing, redaction, process lifecycle) must fail closed, keep existing guarantees, and state residual risks in code comments and `docs/`.
- Preserve cross-platform behavior: Linux, Windows, and macOS all build with `CGO_ENABLED=0`. Use build tags for platform code and keep the other platforms compiling; gate platform-only tests with skips that name the reason.
- Do not silently rewrite user configuration files, and do not break session compatibility: new persisted fields stay optional and old files keep loading.
- Match the surrounding code style: small packages and functions, explicit control flow, early returns, standard library first. `gofmt` must be clean.

## Pull requests and commits

- Open pull requests against `main`. There are no PR or issue templates; describe the problem, the change, and how you verified it.
- Keep commit messages short with the area first, matching recent history: `sandbox: ...`, `fix(filesystem): ...`, `ci: ...`, `docs: ...`, `tui: ...`.
- CI must be green before merge. Do not push binaries, build output, or local-only files.

## Bug reports

Open an issue at `https://github.com/fabledruns/forcefield/issues` with the `ff --version` output, OS and architecture, the command or config that triggers the bug, and what you expected instead. Include logs and redacted config; see below first.

## Never submit secrets

Do not put credentials, API keys, tokens, private file contents, or other user data in tests, logs, issues, commits, or pull requests. Use obviously fake markers (canary names like `FF_SHELL_CRED_CANARY` with junk values, never real keys) the way the existing tests do. CI runs gitleaks over every change; a finding blocks the merge.

## License

Forcefield is Apache License 2.0 (see `LICENSE`). Contributions are accepted under the same license.
