// Package main marker contract tests: main-entry emission, gating,
// and headless marker order. Gated by FF_TEST_BINARY=<ff binary> so
// the default `go test ./...` stays fast and offline; HPOV runs set it.
package main

import (
	"bytes"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func testBinary(t *testing.T) string {
	t.Helper()
	p := os.Getenv("FF_TEST_BINARY")
	if p == "" {
		t.Skip("FF_TEST_BINARY not set")
	}
	return p
}

func runFF(t *testing.T, home string, markers bool, args ...string) (string, string, int) {
	t.Helper()
	bin := testBinary(t)
	cmd := exec.Command(bin, args...)
	env := []string{"PATH=" + os.Getenv("PATH")}
	if os.Getenv("SystemRoot") != "" {
		env = append(env,
			"SystemRoot="+os.Getenv("SystemRoot"),
			"TEMP="+os.Getenv("TEMP"),
			"TMP="+os.Getenv("TMP"),
			"USERPROFILE="+home,
			"HOME="+home,
		)
	} else {
		env = append(env,
			"HOME="+home,
			"TMPDIR="+os.Getenv("TMPDIR"),
		)
	}
	if markers {
		env = append(env, "FF_PERF_MARKERS=1")
	}
	cmd.Env = env
	cmd.Dir = t.TempDir()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run: %v", err)
		}
	}
	return stdout.String(), stderr.String(), code
}

func markerEvents(stderr string) []string {
	var out []string
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ff-perf ") {
			continue
		}
		if f := strings.Fields(strings.TrimPrefix(line, "ff-perf ")); len(f) > 0 {
			out = append(out, f[0])
		}
	}
	return out
}

func TestMainEntryFirstWithMarkers(t *testing.T) {
	_, stderr, code := runFF(t, t.TempDir(), true, "--version")
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, stderr)
	}
	events := markerEvents(stderr)
	if len(events) == 0 || events[0] != "main-entry" {
		t.Fatalf("first marker = %v, want main-entry first", events)
	}
}

func TestNoMarkersWhenDisabled(t *testing.T) {
	_, stderr, code := runFF(t, t.TempDir(), false, "--version")
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, stderr)
	}
	if strings.Contains(stderr, "ff-perf") {
		t.Fatalf("markers emitted while disabled:\n%s", stderr)
	}
}

func TestHeadlessMarkerOrder(t *testing.T) {
	_, stderr, code := runFF(t, t.TempDir(), true, "run", "--agent", "__bench_bogus__", "x")
	if code != 1 {
		t.Fatalf("exit = %d, want the unknown-agent 1\n%s", code, stderr)
	}
	events := markerEvents(stderr)
	want := []string{"main-entry", "config-loaded", "runtime-init-start",
		"stage-skills", "stage-memory", "stage-provider", "stage-tools", "stage-agents"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("headless markers = %v, want %v", events, want)
	}
}
