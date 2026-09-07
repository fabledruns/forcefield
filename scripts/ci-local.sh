#!/usr/bin/env bash
# Mirror .github/workflows/test.yml as closely as a local Windows/Git-Bash
# run can. Fail on the first red step.
set -euo pipefail
cd "$(dirname "$0")/.."

export PATH="$(go env GOPATH | tr '\\' '/')/bin:${PATH}"

echo "=== Check formatting ==="
unformatted=$(gofmt -l . | grep -v 'main\.go$' || true)
if [ -n "$unformatted" ]; then
  echo "These files are not gofmt-clean:"
  echo "$unformatted"
  exit 1
fi
echo "fmt ok"

echo "=== Download / verify dependencies ==="
go mod download
go mod verify

echo "=== Run vet ==="
go vet ./...

echo "=== Run tests ==="
go test ./...

echo "=== Run tests with race detector ==="
go test -race ./...

echo "=== Build Forcefield ==="
go build -v ./...

echo "=== Coverage gate (cmd >= 30%) ==="
go test ./cmd -coverprofile=coverage_cmd.out -covermode=count
pct=$(go tool cover -func=coverage_cmd.out | awk '/total:/ {print $3}' | tr -d '%')
echo "cmd coverage: ${pct}%"
awk -v pct="$pct" 'BEGIN { if (pct+0 < 30) { print "FAIL: cmd coverage " pct "% is below 30% gate"; exit 1 } else { print "coverage gate passed" } }'
rm -f coverage_cmd.out

echo "=== CUE config schemas ==="
if ! command -v cue >/dev/null 2>&1; then
  echo "cue not on PATH; installing via go install"
  go install cuelang.org/go/cmd/cue@latest
fi
cue version
(
  cd cue
  cue vet .
  for ok in testdata/valid-*.yaml; do
    echo "expecting pass: $ok"
    cue vet . "$ok" -d '#Config' -c
  done
  status=0
  for bad in testdata/invalid-*.yaml; do
    echo "expecting rejection: $bad"
    if cue vet . "$bad" -d '#Config' -c >/dev/null 2>&1; then
      echo "invalid config unexpectedly passed: $bad"
      status=1
    else
      echo "rejected as expected"
    fi
  done
  exit "$status"
)

echo "=== ALL CI CHECKS PASSED ==="
