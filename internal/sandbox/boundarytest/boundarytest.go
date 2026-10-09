// Package boundarytest provides test-only helpers for sandbox boundary
// tests: capability-gated skips that are explicit, diagnosable, and
// enforceable from CI.
//
// A boundary test demonstrates an OS-enforced property (Landlock denial,
// namespace egress refusal, PID-namespace cleanup). An application-logic
// test proves code behaviour and must NOT use this package: logic tests
// run hermetically on every platform.
//
// Contract:
//
//   - RequireBoundary skips with the exact message
//     "SKIP(boundary-unavailable:<capability>): <reason>" when the
//     capability is unavailable.
//   - When the FF_REQUIRE_BOUNDARY environment variable lists the
//     capability (comma-separated, e.g. "landlock,netns"), the same
//     situation fails the test instead of skipping, so CI turns a
//     silently-skipped boundary into a failure.
//
// No function in this package claims any isolation exists. Probing what
// the machine supports lives in probe.go; this file only gates tests.
package boundarytest

import (
	"os"
	"strings"
	"testing"
)

// KnownCapabilities lists the machine-stable capability IDs a boundary
// test may gate on. RequireBoundary refuses any other name so a typo
// fails closed instead of silently skipping required mode.
var KnownCapabilities = map[string]bool{
	"landlock": true,
	"netns":    true,
	"pidns":    true,
	"seatbelt": true,
	"wsl":      true,
}

// SkipMarker builds the diagnosable skip marker. It is a pure function
// so tests pin the exact format CI logs are grepped for, independent of
// the skip/fail control flow in RequireBoundary.
func SkipMarker(capability, reason string) string {
	return "SKIP(boundary-unavailable:" + capability + "): " + reason
}

// RequireBoundary gates a boundary test on capability.
//
// capability must name a KnownCapabilities entry (e.g. "landlock").
// reason explains why this machine cannot demonstrate it (missing
// kernel feature, missing distribution, wrong OS).
//
// On success it returns and the test proceeds. Otherwise it either
// skips (default) or fails (when FF_REQUIRE_BOUNDARY requires the
// capability). Both paths log the same SKIP(...) marker so CI logs stay
// greppable.
func RequireBoundary(t *testing.T, capability, reason string) {
	t.Helper()
	capability = strings.TrimSpace(capability)
	reason = strings.TrimSpace(reason)
	if capability == "" {
		t.Fatalf("boundarytest: empty capability name (reason: %s)", reason)
	}
	if !KnownCapabilities[capability] {
		t.Fatalf("boundarytest: unknown capability %q (known: landlock, netns, pidns, seatbelt, wsl)", capability)
	}
	if reason == "" {
		reason = "no reason given"
	}
	marker := SkipMarker(capability, reason)
	if IsRequired(capability) {
		t.Fatalf("boundary required but unavailable: %s (FF_REQUIRE_BOUNDARY=%q)", marker, os.Getenv("FF_REQUIRE_BOUNDARY"))
	}
	t.Skipf("%s", marker)
}

// IsRequired reports whether FF_REQUIRE_BOUNDARY lists capability. The
// variable is comma-separated; entries are trimmed and matched exactly.
// An empty variable requires nothing.
func IsRequired(capability string) bool {
	return RequiredCapabilities()[capability]
}

// RequiredCapabilities parses FF_REQUIRE_BOUNDARY into a set. Empty
// entries are ignored. Matching is exact and case-sensitive: capability
// names are machine-stable IDs, not prose.
func RequiredCapabilities() map[string]bool {
	return parseRequired(os.Getenv("FF_REQUIRE_BOUNDARY"))
}

// parseRequired is the pure core of RequiredCapabilities so tests can
// exercise the grammar without touching the process environment.
func parseRequired(v string) map[string]bool {
	out := make(map[string]bool)
	for _, part := range strings.Split(v, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		out[name] = true
	}
	return out
}
