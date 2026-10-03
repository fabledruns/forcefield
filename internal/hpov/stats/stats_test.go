package stats

import (
	"math"
	"sort"
	"testing"
)

func TestNearestRank(t *testing.T) {
	s := []float64{10, 20, 30, 40, 50}
	if got := NearestRank(s, 50); got != 30 {
		t.Fatalf("p50 = %v, want 30", got)
	}
	if got := NearestRank(s, 90); got != 50 {
		t.Fatalf("p90 = %v, want 50", got)
	}
	// Single sample: every percentile is the sample.
	if got := NearestRank([]float64{7}, 99); got != 7 {
		t.Fatalf("single = %v, want 7", got)
	}
}

func TestMAD_IQR(t *testing.T) {
	s := []float64{1, 2, 3, 4, 100}
	if got := Median(s); got != 3 {
		t.Fatalf("median = %v, want 3", got)
	}
	if got := MAD(s, 3); got != 1 {
		t.Fatalf("MAD = %v, want 1", got)
	}
	if got := IQR(s); got != 2 {
		t.Fatalf("IQR = %v, want 2", got)
	}
}

func TestMeanStdev(t *testing.T) {
	mean, stdev := MeanStdev([]float64{2, 4, 4, 4, 5, 5, 7, 9})
	if mean != 5 {
		t.Fatalf("mean = %v, want 5", mean)
	}
	if math.Abs(stdev-2.138089935) > 1e-6 {
		t.Fatalf("stdev = %v, want ~2.13809", stdev)
	}
}

func TestSummarizeNulls(t *testing.T) {
	vals := []float64{10, 11, 12, 13, 14, 15, 16, 17, 18, 19}
	st := Summarize(vals, nil, 424242)
	if st.N != 10 || st.ValidN != 10 {
		t.Fatalf("n/valid_n = %d/%d", st.N, st.ValidN)
	}
	if st.P90 == nil {
		t.Fatal("p90 should be present at n=10")
	}
	if st.P95 != nil || st.P99 != nil {
		t.Fatal("p95/p99 must be null below n=20/n=100")
	}
	if st.Nulls["p95"] == "" || st.Nulls["p99"] == "" {
		t.Fatalf("null reasons missing: %v", st.Nulls)
	}
	full := make([]float64, 100)
	for i := range full {
		full[i] = float64(i)
	}
	stf := Summarize(full, nil, 424242)
	if stf.P99 == nil {
		t.Fatal("p99 should be present at n=100")
	}
	// p99 of 0..99 nearest-rank: rank ceil(99) = 99 -> value 98.
	if *stf.P99 != 98 {
		t.Fatalf("p99 = %v, want 98", *stf.P99)
	}
}

func TestSummarizeInvalidExcluded(t *testing.T) {
	vals := []float64{10, 11, 12, 5000}
	valid := []bool{true, true, true, false}
	st := Summarize(vals, valid, 1)
	if st.ValidN != 3 {
		t.Fatalf("valid_n = %d, want 3", st.ValidN)
	}
	if st.Max != 12 {
		t.Fatalf("max over valid = %v, want 12", st.Max)
	}
	if Summarize(vals, []bool{false, false, false, false}, 1).ValidN != 0 {
		t.Fatal("all-invalid must yield valid_n=0")
	}
}

func TestBootstrapDeterministic(t *testing.T) {
	vals := []float64{120, 131, 128, 140, 119, 135, 127, 133, 129, 136,
		124, 130, 132, 126, 134, 121, 137, 125, 138, 122}
	lo1, hi1 := BootstrapCI50(vals, 424242)
	lo2, hi2 := BootstrapCI50(vals, 424242)
	if lo1 != lo2 || hi1 != hi2 {
		t.Fatal("bootstrap CI must be deterministic for a fixed seed")
	}
	if lo1 > hi1 {
		t.Fatal("CI lo > hi")
	}
	// Median of sorted data: nearest-rank rank 10 -> check containment.
	s := append([]float64(nil), vals...)
	sortFloats(s)
	p50 := Median(s)
	if p50 < lo1 || p50 > hi1 {
		t.Fatalf("p50 %v outside CI [%v,%v]", p50, lo1, hi1)
	}
	// Constant data: every resample median is the constant.
	constant := []float64{5, 5, 5, 5, 5, 5, 5, 5}
	clo, chi := BootstrapCI50(constant, 424242)
	if clo != 5 || chi != 5 {
		t.Fatalf("constant CI = [%v,%v], want [5,5]", clo, chi)
	}
}

func TestMannWhitney(t *testing.T) {
	a := []float64{5, 5, 5, 5, 5, 5, 5, 5, 5, 5}
	_, p := MannWhitney(a, append([]float64(nil), a...))
	if math.Abs(p-1) > 1e-9 {
		t.Fatalf("identical samples p = %v, want 1", p)
	}
	b := []float64{101, 102, 103, 104, 105, 106, 107, 108, 109, 110}
	c := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	_, p = MannWhitney(c, b)
	if !(p < 0.01) {
		t.Fatalf("separated samples p = %v, want < 0.01", p)
	}
	_, p = MannWhitney([]float64{1, 2}, []float64{3, 4})
	if !math.IsNaN(p) {
		t.Fatalf("small-n p = %v, want NaN", p)
	}
}

func TestHodgesLehmann(t *testing.T) {
	// Diffs: 5,-5,15,5 -> sorted -5,5,5,15 -> p50 rank 2 -> 5.
	if got := HodgesLehmann([]float64{10, 20}, []float64{15, 25}); got != 5 {
		t.Fatalf("HL = %v, want 5", got)
	}
}

func TestSpearmanDrift(t *testing.T) {
	up := make([]float64, 30)
	for i := range up {
		up[i] = float64(i)
	}
	rho, p := Spearman(up)
	if rho < 0.99 || p > 1e-6 {
		t.Fatalf("monotonic rho=%v p=%v", rho, p)
	}
	if !DriftFlagged(up) {
		t.Fatal("monotonic ramp must flag drift")
	}
	flat := []float64{5, 5, 5, 5, 5, 5, 5, 5, 5, 5}
	if DriftFlagged(flat) {
		t.Fatal("constant series must not flag drift")
	}
}

func TestBimodal(t *testing.T) {
	bi := []float64{10, 10, 10, 10, 10, 100, 100, 100, 100, 100}
	s := append([]float64(nil), bi...)
	sortFloats(s)
	m := Median(s)
	if !Bimodal(s, m, MAD(s, m)) {
		t.Fatal("two-cluster data must flag bimodal")
	}
	uni := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	su := append([]float64(nil), uni...)
	mu := Median(su)
	if Bimodal(su, mu, MAD(su, mu)) {
		t.Fatal("uniform data must not flag bimodal")
	}
}

func TestDifferenceCI(t *testing.T) {
	base := []float64{100, 102, 101, 103, 99, 100, 102, 101, 100, 99}
	cur := []float64{110, 112, 111, 113, 109, 110, 112, 111, 110, 109}
	lo, hi := DifferenceCI50(base, cur, false, 424242)
	if lo <= 0 || hi <= 0 {
		t.Fatalf("shifted CI [%v,%v] should exclude 0", lo, hi)
	}
	lo2, hi2 := DifferenceCI50(base, append([]float64(nil), base...), false, 424242)
	if !(lo2 <= 0 && 0 <= hi2) {
		t.Fatalf("A/A CI [%v,%v] should include 0", lo2, hi2)
	}
}

func sortFloats(s []float64) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// A paired percentile interval must estimate the quantity a tail verdict
// tests — p95(cur) - p95(base) — and not "the p95 of the per-round
// differences", which is a different statistic.
//
// The two disagree in sign on data of exactly the shape an interleaved A/A
// produces: a candidate whose tail sits lower while its per-round
// differences are mostly positive. A 20-trial A/A campaign on identical
// builds produced 23 tail claims this way, every one of them on an interval
// that did not cover the p95 difference it was claimed for.
func TestDifferencePercentileCIPairedEstimatesThePercentileDifference(t *testing.T) {
	// Candidate: same body, a lower tail. The per-round differences are
	// positive in the body, so their p95 is positive while
	// p95(cur)-p95(base) is negative.
	base := []float64{
		40, 41, 42, 43, 44, 45, 46, 46, 47, 48,
		49, 50, 51, 52, 53, 54, 55, 56, 57, 58,
		59, 60, 61, 62, 63, 64, 65, 90, 95, 100,
	}
	cur := []float64{
		40.5, 41.5, 42.5, 43.5, 44.5, 45.5, 46.5, 46.5, 47.5, 48.5,
		49.5, 50.5, 51.5, 52.5, 53.5, 54.5, 55.5, 56.5, 57.5, 58.5,
		59.5, 60.5, 61.5, 62.5, 63.5, 64.5, 65.5, 50, 52, 54,
	}
	bs, cs := sortFloats2(base), sortFloats2(cur)
	p95diff := NearestRank(cs, 95) - NearestRank(bs, 95)
	if p95diff >= 0 {
		t.Fatalf("fixture precondition: p95 difference = %v, want negative", p95diff)
	}
	// The per-round differences have the opposite sign at their tail.
	d := make([]float64, len(base))
	for i := range d {
		d[i] = cur[i] - base[i]
	}
	if NearestRank(sortFloats2(d), 95) <= 0 {
		t.Fatalf("fixture precondition: p95 of differences = %v, want positive",
			NearestRank(sortFloats2(d), 95))
	}

	lo, hi := DifferencePercentileCI(base, cur, 95, true, 424242)
	if !(lo <= p95diff && p95diff <= hi) {
		t.Errorf("paired p95 CI [%v,%v] must bracket the p95 difference %v", lo, hi, p95diff)
	}
	if !(lo <= 0 && 0 <= hi) {
		t.Errorf("paired p95 CI [%v,%v] must include 0 when the tail difference is negative", lo, hi)
	}
	// The unpaired estimator agrees about the sign: both bracket the same
	// quantity, they only differ in width.
	ulo, uhi := DifferencePercentileCI(base, cur, 95, false, 424242)
	if !(ulo <= 0 && 0 <= uhi) {
		t.Errorf("unpaired p95 CI [%v,%v] must also include 0", ulo, uhi)
	}
	// Pairing must still cancel a shared shift, so it cannot be wider than
	// the unpaired interval on data with no round-level noise.
	sameBase := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	sameCur := append([]float64(nil), sameBase...)
	for i := range sameCur {
		sameCur[i] += 5
	}
	plo, phi := DifferencePercentileCI(sameBase, sameCur, 95, true, 99)
	if !(plo > 0 && phi > 0) {
		t.Errorf("a pure paired shift must give an interval above zero: [%v,%v]", plo, phi)
	}
	if !(plo <= 5 && 5 <= phi) {
		t.Errorf("a pure paired shift of 5 must bracket 5: [%v,%v]", plo, phi)
	}
}

// The median path keeps its own paired estimator: the median of the pair
// differences is the right location estimate and is unchanged.
func TestDifferenceCI50PairedStillUsesPairDifferences(t *testing.T) {
	base := []float64{10, 12, 11, 13, 9, 10, 12, 11, 10, 9}
	cur := []float64{11, 13, 12, 14, 10, 11, 13, 12, 11, 10}
	lo, hi := DifferenceCI50(base, cur, true, 424242)
	if lo <= 0 || hi <= 0 {
		t.Fatalf("paired median CI [%v,%v] should exclude 0 for a +1 shift", lo, hi)
	}
	if lo > 1 || hi < 1 {
		t.Errorf("paired median CI [%v,%v] should bracket the +1 shift", lo, hi)
	}
}

// Unequal sides must still pair index-wise over the shorter one. A joint
// resample that left the longer buffer's tail at zero would rank those zeros
// as data, dragging the interval toward zero.
func TestDifferencePercentileCIPairedUnequalLengths(t *testing.T) {
	base := make([]float64, 20)
	longer := make([]float64, 40)
	for i := range base {
		base[i] = float64(100 + i)
	}
	for i := range longer {
		longer[i] = float64(200 + i)
	}
	// Paired over the shorter side: p50 of base 100..119 is 109, p50 of the
	// first 20 candidates 200..219 is 209, so every paired difference is +100.
	// Twenty trailing zeros would make the candidate centre collapse instead.
	lo, hi := DifferencePercentileCI(base, longer, 50, true, 7)
	if !(lo <= 100 && 100 <= hi) {
		t.Errorf("paired median CI [%v,%v] must bracket the +100 paired shift", lo, hi)
	}
	lo2, hi2 := DifferencePercentileCI(longer, base, 50, true, 7)
	if !(hi2 <= -100 && -100 <= lo2) {
		t.Errorf("paired median CI [%v,%v] must bracket the -100 shift when reversed", lo2, hi2)
	}
	// The tail behaves the same way: p95 of base is 118, p95 of the paired
	// candidate range is 218.
	lo3, hi3 := DifferencePercentileCI(base, longer, 95, true, 7)
	if !(lo3 <= 100 && 100 <= hi3) {
		t.Errorf("paired p95 CI [%v,%v] must bracket the +100 paired shift", lo3, hi3)
	}
}

func sortFloats2(s []float64) []float64 {
	out := append([]float64(nil), s...)
	sort.Float64s(out)
	return out
}
