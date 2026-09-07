// Package hardening is Forcefield's failure-injection and regression
// laboratory for the RC6 hardening cycle (P1.15-P1.21).
//
// It contains no production code. Every file here is either a helper for
// deterministic failure injection or a regression test that reproduces an
// RC5 audit finding and proves it stays fixed.
//
// Layout mirrors the mandatory lab areas:
//
//   soak_test.go          long-horizon / accelerated growth
//   provider_chaos_test.go 429/5xx/Retry-After/timeout/mid-stream/malformed/cancel
//   tool_correctness_test.go FinishLength, truncation, fence injection
//   injection_test.go     prompt-injection / trust-boundary adversarial
//   filesystem_test.go    traversal/symlink/dotfile/O_NOFOLLOW/native behavior
//   shell_test.go         size/timeout/cancel/env/cwd/grandchildren per mode
//   resources_test.go     limits<=0, enormous inputs, session/memory/task growth
//   concurrency_test.go   concurrent sessions/writes, racing cancel/complete
//   crash_test.go         write/rename/compaction/resume interruption
//   context_test.go       100-msg window, CJK, system-prompt budgeting
//   scrub_test.go         nested secrets across args/results/errors/session
//   MANUAL_SOAK.md        how to run a real multi-hour soak manually
//
// Rules: no new dependencies, stdlib only, deterministic (no sleeps >100ms
// except where testing timeouts), race-safe, and every hardening fix in
// P1.16-P1.21 must cite one of these tests as its reproduction.
package hardening
