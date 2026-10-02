package stats

import (
	"math"
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
