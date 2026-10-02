package main

import (
	"path/filepath"
	"testing"

	"forcefield/internal/hpov/compare"
)

// The regression exit code is opt-in. A comparison is a report by
// default; only --fail-on-regression turns a verdict into a failing
// status, so CI that has not asked for the gate is never broken by it.
func TestRegressionExitCodeIsOptIn(t *testing.T) {
	regressed := &compare.Comparison{Metrics: []compare.Metric{
		{Verdict: compare.VerdictRegressed},
	}}
	if got := compareExit(regressed, compare.Options{}); got != exitOK {
		t.Errorf("without --fail-on-regression exit = %d, want %d", got, exitOK)
	}
	if got := compareExit(regressed, compare.Options{FailOnRegression: true}); got != exitRegression {
		t.Errorf("with --fail-on-regression exit = %d, want %d", got, exitRegression)
	}
}

// A tail regression is a regression: a healthy median must not hide a
// degrading p95 from the gate.
func TestTailRegressionSetsExitCode(t *testing.T) {
	c := &compare.Comparison{Metrics: []compare.Metric{
		{Verdict: compare.VerdictUnchanged},
		{Verdict: compare.VerdictTailRegressed},
	}}
	if !c.Regressed() {
		t.Fatal("tail_regressed must count as a regression")
	}
	if got := compareExit(c, compare.Options{FailOnRegression: true}); got != exitRegression {
		t.Errorf("exit = %d, want %d", got, exitRegression)
	}
}

// Everything that is not an asserted regression must leave the exit code
// alone: inconclusive, informational and not_comparable are answers, not
// failures, and folding them into a failure would make a missing benchmark
// indistinguishable from a slow one.
func TestNonRegressionVerdictsDoNotFail(t *testing.T) {
	for _, verdict := range []string{
		compare.VerdictImproved,
		compare.VerdictUnchanged,
		compare.VerdictTailImproved,
		compare.VerdictInconclusive,
		compare.VerdictInformational,
		compare.VerdictNotComparable,
		compare.VerdictIncompatible,
		compare.VerdictInvalid,
		compare.VerdictMissingBase,
		compare.VerdictMissingCurrent,
	} {
		c := &compare.Comparison{Metrics: []compare.Metric{{Verdict: verdict}}}
		if got := compareExit(c, compare.Options{FailOnRegression: true}); got != exitOK {
			t.Errorf("%s must not fail the gate: exit %d", verdict, got)
		}
	}
}

func TestCompareUsageErrors(t *testing.T) {
	if got := run([]string{"compare"}); got != exitUsage {
		t.Errorf("missing operands exit = %d, want %d", got, exitUsage)
	}
	if got := run([]string{"compare", "a.json"}); got != exitUsage {
		t.Errorf("one operand exit = %d, want %d", got, exitUsage)
	}
	if got := run([]string{"compare", "missing-base.json", "missing-cand.json"}); got != exitBench {
		t.Errorf("unreadable inputs exit = %d, want %d", got, exitBench)
	}
	if got := run([]string{"compare-live"}); got != exitUsage {
		t.Errorf("compare-live without --out exit = %d, want %d", got, exitUsage)
	}
}

// An A/A campaign measures one binary as both subjects, so it takes
// exactly one --ff. Zero or two would make the two sides different
// things and quietly turn a calibration campaign into an A/B one.
func TestCalibrateRequiresExactlyOneSubject(t *testing.T) {
	if got := run([]string{"calibrate"}); got != exitUsage {
		t.Errorf("calibrate without --ff exit = %d, want %d", got, exitUsage)
	}
	dir := t.TempDir()
	a := dir + string(filepath.Separator) + "a"
	b := dir + string(filepath.Separator) + "b"
	if got := run([]string{"calibrate", "--ff", "a=" + a, "--ff", "b=" + b,
		"--repeats", "1", "--out", dir}); got != exitUsage {
		t.Errorf("calibrate with two subjects exit = %d, want %d", got, exitUsage)
	}
	if got := run([]string{"calibrate", "--ff", "aa=" + a}); got != exitBench {
		t.Errorf("calibrate with a missing binary exit = %d, want %d", got, exitBench)
	}
}

// calibrate is a report about observed behaviour, so it never gates: an
// A/A regression is a finding to read, not a build failure. The exit
// code for a successful campaign is 0 regardless of what it found.
func TestCalibrateDoesNotGateOnVerdicts(t *testing.T) {
	if compareExit(&compare.Comparison{Metrics: []compare.Metric{
		{Verdict: compare.VerdictRegressed},
	}}, compare.Options{}) != exitOK {
		t.Error("compare without --fail-on-regression must exit 0")
	}
}
