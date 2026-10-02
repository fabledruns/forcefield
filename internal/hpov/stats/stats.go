// Package stats implements HPOV's distribution-first statistics:
// nearest-rank quantiles, MAD/IQR dispersion, deterministic percentile
// bootstrap CIs, Mann-Whitney evidence, and drift/bimodality flags.
//
// Decisions use median/MAD/quantiles. Mean/stdev are reported but never
// used for latency decisions (right-skewed, heavy-tailed).
package stats

import (
	"math"
	"math/rand/v2"
	"sort"
)

// Summary is the derived distribution of one metric. It mirrors the
// schema statistics object; the runner converts it so this package
// stays dependency-free (schema validation imports stats without a
// cycle).
type Summary struct {
	N        int
	ValidN   int
	Min      float64
	P50      float64
	P90      *float64
	P95      *float64
	P99      *float64
	Max      float64
	Mean     float64
	Stdev    float64
	MAD      float64
	IQR      float64
	RobustCV float64
	CI95P50  [2]float64
	Nulls    map[string]string
}

// Minimum valid_n per percentile. Below the threshold the field is
// null with an "insufficient_n" reason: nearest-rank p99 at n=30 *is*
// the max, and reporting it as p99 would be misleading.
const (
	MinNForP90 = 10
	MinNForP95 = 20
	MinNForP99 = 100
)

// NearestRank returns the p-th percentile (0<p<=100) of already
// sorted values using rank = ceil(p/100*n), 1-based. It always returns
// an observed value. Identical to bench/startup.ps1's rule.
func NearestRank(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return math.NaN()
	}
	rank := int(math.Ceil(p / 100 * float64(n)))
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}

// Median returns the middle value (nearest-rank p50).
func Median(sorted []float64) float64 {
	return NearestRank(sorted, 50)
}

// MAD returns the median absolute deviation from the median.
func MAD(sorted []float64, median float64) float64 {
	n := len(sorted)
	if n == 0 {
		return math.NaN()
	}
	dev := make([]float64, n)
	for i, v := range sorted {
		dev[i] = math.Abs(v - median)
	}
	sort.Float64s(dev)
	return Median(dev)
}

// IQR returns the inter-quartile range (nearest-rank p75-p25).
func IQR(sorted []float64) float64 {
	if len(sorted) == 0 {
		return math.NaN()
	}
	return NearestRank(sorted, 75) - NearestRank(sorted, 25)
}

// RobustCV is 1.4826*MAD/median. Launch benchmarks flag noisy above
// 0.10, TUI/MCP above 0.15.
func RobustCV(median, mad float64) float64 {
	if median == 0 || math.IsNaN(median) || math.IsNaN(mad) {
		return math.NaN()
	}
	return 1.4826 * mad / math.Abs(median)
}

// MeanStdev returns the sample mean and sample (n-1) stdev.
func MeanStdev(values []float64) (mean, stdev float64) {
	n := len(values)
	if n == 0 {
		return math.NaN(), math.NaN()
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	mean = sum / float64(n)
	if n == 1 {
		return mean, 0
	}
	ss := 0.0
	for _, v := range values {
		d := v - mean
		ss += d * d
	}
	return mean, math.Sqrt(ss / float64(n-1))
}

// Summarize derives a Summary from raw values. Only valid samples
// enter: pass values and the parallel valid mask (nil mask = all
// valid). Warm-up samples must already be excluded by the caller.
func Summarize(values []float64, valid []bool, bootstrapSeed int64) *Summary {
	n := len(values)
	if n == 0 {
		return nil
	}
	kept := make([]float64, 0, n)
	for i, v := range values {
		if valid == nil || valid[i] {
			kept = append(kept, v)
		}
	}
	st := &Summary{N: n, ValidN: len(kept)}
	if len(kept) == 0 {
		return st
	}
	sorted := append([]float64(nil), kept...)
	sort.Float64s(sorted)
	st.Min = sorted[0]
	st.Max = sorted[len(sorted)-1]
	st.P50 = Median(sorted)
	st.Mean, st.Stdev = MeanStdev(kept)
	st.MAD = MAD(sorted, st.P50)
	st.IQR = IQR(sorted)
	st.RobustCV = RobustCV(st.P50, st.MAD)
	lo, hi := BootstrapCI50(kept, bootstrapSeed)
	st.CI95P50 = [2]float64{lo, hi}
	st.Nulls = map[string]string{}
	if len(kept) >= MinNForP90 {
		v := NearestRank(sorted, 90)
		st.P90 = &v
	} else {
		st.Nulls["p90"] = "insufficient_n(<10)"
	}
	if len(kept) >= MinNForP95 {
		v := NearestRank(sorted, 95)
		st.P95 = &v
	} else {
		st.Nulls["p95"] = "insufficient_n(<20)"
	}
	if len(kept) >= MinNForP99 {
		v := NearestRank(sorted, 99)
		st.P99 = &v
	} else {
		st.Nulls["p99"] = "insufficient_n(<100)"
	}
	if len(st.Nulls) == 0 {
		st.Nulls = nil
	}
	return st
}

// BootstrapCI50 returns the percentile-method 95% CI of the median
// over 5000 resamples with a deterministic seed. If drift is flagged
// the caller should prefer MovingBlockCI50.
func BootstrapCI50(values []float64, seed int64) (lo, hi float64) {
	return bootstrapCI(values, seed, 5000, len(values), false)
}

// MovingBlockCI50 is the drift-robust variant: blocks of
// ceil(n^(1/3)) preserve autocorrelation so CIs are not overconfident.
func MovingBlockCI50(values []float64, seed int64) (lo, hi float64) {
	n := len(values)
	block := int(math.Ceil(math.Pow(float64(n), 1.0/3.0)))
	if block < 1 {
		block = 1
	}
	return bootstrapCI(values, seed, 5000, block, true)
}

func bootstrapCI(values []float64, seed int64, resamples, block int, blocked bool) (lo, hi float64) {
	n := len(values)
	if n == 0 {
		return math.NaN(), math.NaN()
	}
	rng := rand.New(rand.NewPCG(uint64(seed), uint64(seed>>32|1)))
	meds := make([]float64, resamples)
	buf := make([]float64, n)
	for r := 0; r < resamples; r++ {
		if blocked {
			for i := 0; i < n; {
				start := rng.IntN(n)
				for b := 0; b < block && i < n; b++ {
					buf[i] = values[(start+b)%n]
					i++
				}
			}
		} else {
			for i := range buf {
				buf[i] = values[rng.IntN(n)]
			}
		}
		tmp := append([]float64(nil), buf...)
		sort.Float64s(tmp)
		meds[r] = Median(tmp)
	}
	sort.Float64s(meds)
	return NearestRank(meds, 2.5), NearestRank(meds, 97.5)
}

// DifferenceCI50 returns the 95% bootstrap CI of the difference of
// medians (cur - base). Paired uses pair differences (interleaved
// A/B); unpaired resamples both sides independently.
func DifferenceCI50(base, cur []float64, paired bool, seed int64) (lo, hi float64) {
	return differencePercentileCI(base, cur, 50, paired, seed)
}

// DifferencePercentileCI is DifferenceCI50 for an arbitrary percentile:
// the 95% bootstrap CI of cur(pct) - base(pct). It exists because a tail
// verdict needs the same evidence rule as the median — a threshold alone
// on p95 fires on noise at every n.
func DifferencePercentileCI(base, cur []float64, pct float64, paired bool, seed int64) (lo, hi float64) {
	return differencePercentileCI(base, cur, pct, paired, seed)
}

func differencePercentileCI(base, cur []float64, pct float64, paired bool, seed int64) (lo, hi float64) {
	const resamples = 5000
	rng := rand.New(rand.NewPCG(uint64(seed), uint64(seed>>32|1)))
	meds := make([]float64, resamples)
	if paired {
		n := len(base)
		if len(cur) < n {
			n = len(cur)
		}
		if n == 0 {
			return math.NaN(), math.NaN()
		}
		d := make([]float64, n)
		for i := range d {
			d[i] = cur[i] - base[i]
		}
		buf := make([]float64, n)
		for r := 0; r < resamples; r++ {
			for i := range buf {
				buf[i] = d[rng.IntN(n)]
			}
			tmp := append([]float64(nil), buf...)
			sort.Float64s(tmp)
			meds[r] = NearestRank(tmp, pct)
		}
		sort.Float64s(meds)
		return NearestRank(meds, 2.5), NearestRank(meds, 97.5)
	}
	nb, nc := len(base), len(cur)
	if nb == 0 || nc == 0 {
		return math.NaN(), math.NaN()
	}
	bb := make([]float64, nb)
	cc := make([]float64, nc)
	for r := 0; r < resamples; r++ {
		for i := range bb {
			bb[i] = base[rng.IntN(nb)]
		}
		for i := range cc {
			cc[i] = cur[rng.IntN(nc)]
		}
		tb := append([]float64(nil), bb...)
		tc := append([]float64(nil), cc...)
		sort.Float64s(tb)
		sort.Float64s(tc)
		meds[r] = NearestRank(tc, pct) - NearestRank(tb, pct)
	}
	sort.Float64s(meds)
	return NearestRank(meds, 2.5), NearestRank(meds, 97.5)
}

// MannWhitney runs a two-sided Mann-Whitney U test with tie
// correction (normal approximation) and returns U and p. It needs
// n >= 8 per side for the approximation; smaller samples return
// p = NaN so callers fall back to inconclusive.
func MannWhitney(a, b []float64) (u, p float64) {
	na, nb := len(a), len(b)
	if na < 8 || nb < 8 {
		return math.NaN(), math.NaN()
	}
	type ranked struct {
		v float64
		g int // 0 = a, 1 = b
	}
	all := make([]ranked, 0, na+nb)
	for _, v := range a {
		all = append(all, ranked{v, 0})
	}
	for _, v := range b {
		all = append(all, ranked{v, 1})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].v < all[j].v })
	// Average ranks over ties; accumulate tie correction.
	ra := 0.0
	tieCorr := 0.0
	for i := 0; i < len(all); {
		j := i + 1
		for j < len(all) && all[j].v == all[i].v {
			j++
		}
		avg := float64(i+1+j) / 2 // 1-based average rank
		for k := i; k < j; k++ {
			if all[k].g == 0 {
				ra += avg
			}
		}
		t := float64(j - i)
		tieCorr += t*t*t - t
		i = j
	}
	n := float64(na + nb)
	mu := float64(na*nb) / 2
	u = ra - float64(na*(na+1))/2
	sig := float64(na*nb) / 12 * ((n + 1) - tieCorr/(n*(n-1)))
	if sig <= 0 {
		// Fully tied samples: variance is zero; p=1 iff U is
		// exactly at its null mean.
		if u == mu {
			return u, 1
		}
		return u, 0
	}
	z := (u - mu) / math.Sqrt(sig)
	// Continuity correction is omitted; two-sided p from normal tail.
	p = math.Erfc(math.Abs(z) / math.Sqrt2)
	return u, p
}

// HodgesLehmann returns the median of pairwise differences
// (cur - base): the reported effect estimate.
func HodgesLehmann(base, cur []float64) float64 {
	if len(base) == 0 || len(cur) == 0 {
		return math.NaN()
	}
	d := make([]float64, 0, len(base)*len(cur))
	for _, c := range cur {
		for _, b := range base {
			d = append(d, c-b)
		}
	}
	sort.Float64s(d)
	return Median(d)
}

// Spearman returns Spearman's rho between iteration index and value
// (drift/thermal detector) with a two-sided p-value via the Fisher z
// approximation.
func Spearman(values []float64) (rho, p float64) {
	n := len(values)
	if n < 3 {
		return 0, 1
	}
	rx := ranks(seq(n))
	ry := ranks(append([]float64(nil), values...))
	mx, my := mean(rx), mean(ry)
	num, dx, dy := 0.0, 0.0, 0.0
	for i := range rx {
		a, b := rx[i]-mx, ry[i]-my
		num += a * b
		dx += a * a
		dy += b * b
	}
	if dx == 0 || dy == 0 {
		return 0, 1
	}
	rho = num / math.Sqrt(dx*dy)
	if math.Abs(rho) >= 1 {
		return rho, 0
	}
	z := math.Atanh(rho) * math.Sqrt(float64(n-3))
	p = math.Erfc(math.Abs(z) / math.Sqrt2)
	return rho, p
}

// DriftFlagged reports |rho| > 0.5 with p < 0.01.
func DriftFlagged(values []float64) bool {
	rho, p := Spearman(values)
	return math.Abs(rho) > 0.5 && p < 0.01
}

// Bimodal applies the gap test: a sorted gap wider than
// max(5*MAD, 0.2*median) with >=3 samples on both sides.
func Bimodal(sorted []float64, median, mad float64) bool {
	n := len(sorted)
	if n < 6 || math.IsNaN(median) || math.IsNaN(mad) {
		return false
	}
	thresh := 5 * mad
	if m := 0.2 * math.Abs(median); m > thresh {
		thresh = m
	}
	for i := 1; i < n; i++ {
		if sorted[i]-sorted[i-1] > thresh && i >= 3 && n-i >= 3 {
			return true
		}
	}
	return false
}

func seq(n int) []float64 {
	s := make([]float64, n)
	for i := range s {
		s[i] = float64(i)
	}
	return s
}

func mean(v []float64) float64 {
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

// ranks assigns average ranks (1-based) over ties.
func ranks(v []float64) []float64 {
	n := len(v)
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(i, j int) bool { return v[idx[i]] < v[idx[j]] })
	r := make([]float64, n)
	for i := 0; i < n; {
		j := i + 1
		for j < n && v[idx[j]] == v[idx[i]] {
			j++
		}
		avg := float64(i+1+j) / 2
		for k := i; k < j; k++ {
			r[idx[k]] = avg
		}
		i = j
	}
	return r
}
