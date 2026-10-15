// Package stats holds V2's deterministic statistics: a CPython-compatible
// random generator (so bootstrap intervals match V2 bit for bit), percentile,
// bootstrap mean intervals and paired effect summaries.
package stats

import (
	"math"
	"math/big"
	"sort"
	"strconv"
)

// PyRandom reproduces CPython's random.Random (MT19937 + init_by_array).
type PyRandom struct {
	mt  [624]uint32
	idx int
}

// NewPyRandom seeds like random.Random(seed) for an integer seed.
func NewPyRandom(seed int64) *PyRandom {
	n := new(big.Int).Abs(big.NewInt(seed))
	key := []uint32{}
	mask := big.NewInt(0xffffffff)
	for n.Sign() > 0 {
		key = append(key, uint32(new(big.Int).And(n, mask).Uint64()))
		n.Rsh(n, 32)
	}
	if len(key) == 0 {
		key = []uint32{0}
	}
	r := &PyRandom{}
	r.initGenrand(19650218)
	i, j := 1, 0
	for k := max(624, len(key)); k > 0; k-- {
		r.mt[i] = (r.mt[i] ^ ((r.mt[i-1] ^ (r.mt[i-1] >> 30)) * 1664525)) + key[j] + uint32(j)
		i++
		j++
		if i >= 624 {
			r.mt[0] = r.mt[623]
			i = 1
		}
		if j >= len(key) {
			j = 0
		}
	}
	for k := 623; k > 0; k-- {
		r.mt[i] = (r.mt[i] ^ ((r.mt[i-1] ^ (r.mt[i-1] >> 30)) * 1566083941)) - uint32(i)
		i++
		if i >= 624 {
			r.mt[0] = r.mt[623]
			i = 1
		}
	}
	r.mt[0] = 0x80000000
	r.idx = 624
	return r
}

func (r *PyRandom) initGenrand(s uint32) {
	r.mt[0] = s
	for i := 1; i < 624; i++ {
		r.mt[i] = 1812433253*(r.mt[i-1]^(r.mt[i-1]>>30)) + uint32(i)
	}
	r.idx = 624
}

func (r *PyRandom) uint32() uint32 {
	if r.idx >= 624 {
		for k := 0; k < 624; k++ {
			y := (r.mt[k] & 0x80000000) | (r.mt[(k+1)%624] & 0x7fffffff)
			v := r.mt[(k+397)%624] ^ (y >> 1)
			if y&1 != 0 {
				v ^= 0x9908b0df
			}
			r.mt[k] = v
		}
		r.idx = 0
	}
	y := r.mt[r.idx]
	r.idx++
	y ^= y >> 11
	y ^= (y << 7) & 0x9d2c5680
	y ^= (y << 15) & 0xefc60000
	return y ^ (y >> 18)
}

// getrandbits mirrors CPython for k <= 32.
func (r *PyRandom) getrandbits(k int) uint32 { return r.uint32() >> (32 - k) }

// Randrange returns a uniform int in [0, n) exactly like random.randrange(n).
func (r *PyRandom) Randrange(n int) int {
	if n <= 1 {
		return 0
	}
	k := 0
	for v := n; v > 0; v >>= 1 {
		k++
	}
	for {
		if v := int(r.getrandbits(k)); v < n {
			return v
		}
	}
}

// Random returns a float in [0, 1) exactly like random.random().
func (r *PyRandom) Random() float64 {
	a, b := r.uint32()>>5, r.uint32()>>6
	return (float64(a)*67108864 + float64(b)) * (1.0 / 9007199254740992.0)
}

// Percentile is linear interpolation between closest ranks (V2).
func Percentile(values []float64, p float64) float64 {
	p = math.Min(1, math.Max(0, p))
	o := append([]float64{}, values...)
	sort.Float64s(o)
	if len(o) == 1 {
		return o[0]
	}
	pos := float64(len(o)-1) * p
	lo, hi := int(math.Floor(pos)), int(math.Ceil(pos))
	if lo == hi {
		return o[lo]
	}
	return o[lo] + (o[hi]-o[lo])*(pos-float64(lo))
}

// Round6 is Python round(x, 6): the exact binary value correctly rounded to
// six decimals (strconv rounds the exact value, unlike x*1e6).
func Round6(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return x
	}
	f, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', 6, 64), 64)
	return f
}

// Mean is Python statistics.mean: the exact rational sum divided by n,
// rounded once to the nearest float64.
func Mean(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	sum := new(big.Rat)
	r := new(big.Rat)
	for _, x := range v {
		sum.Add(sum, r.SetFloat64(x))
	}
	f, _ := sum.Quo(sum, new(big.Rat).SetInt64(int64(len(v)))).Float64()
	return f
}

func finite(values []float64) []float64 {
	out := []float64{}
	for _, v := range values {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			out = append(out, v)
		}
	}
	return out
}

// BootstrapMeanCI is V2 bootstrap_mean_ci: percentile bootstrap of the mean
// using a seeded CPython-compatible generator.
func BootstrapMeanCI(values []float64, confidence float64, resamples int, seed int64) map[string]any {
	c := finite(values)
	if len(c) == 0 {
		return map[string]any{"n": 0, "mean": nil, "confidence": confidence, "ci_low": nil, "ci_high": nil, "resamples": 0}
	}
	draws := max(1, resamples)
	m := Mean(c)
	if len(c) == 1 {
		return map[string]any{"n": 1, "mean": Round6(m), "confidence": confidence, "ci_low": Round6(m), "ci_high": Round6(m), "resamples": 0}
	}
	rng := NewPyRandom(seed)
	means := make([]float64, draws)
	for d := range means {
		s := 0.0
		for range c {
			s += c[rng.Randrange(len(c))]
		}
		means[d] = s / float64(len(c))
	}
	alpha := (1 - confidence) / 2
	return map[string]any{"n": len(c), "mean": Round6(m), "confidence": confidence,
		"ci_low": Round6(Percentile(means, alpha)), "ci_high": Round6(Percentile(means, 1-alpha)), "resamples": draws}
}

// Stdev is the sample standard deviation.
func Stdev(v []float64) float64 {
	if len(v) < 2 {
		return 0
	}
	m, s := Mean(v), 0.0
	for _, x := range v {
		s += (x - m) * (x - m)
	}
	return math.Sqrt(s / float64(len(v)-1))
}

// PairedEffect is V2 paired_effect_summary: bootstrap CI plus win/tie/loss and Cohen's dz.
func PairedEffect(deltas []float64, confidence float64, resamples int, seed int64) map[string]any {
	c := finite(deltas)
	out := BootstrapMeanCI(c, confidence, resamples, seed)
	if len(c) == 0 {
		out["wins"], out["ties"], out["losses"], out["win_rate"], out["cohen_dz"] = 0, 0, 0, nil, nil
		return out
	}
	w, t, l := 0, 0, 0
	for _, x := range c {
		switch {
		case x > 0:
			w++
		case x == 0:
			t++
		default:
			l++
		}
	}
	out["wins"], out["ties"], out["losses"], out["win_rate"] = w, t, l, Round6(float64(w)/float64(len(c)))
	out["cohen_dz"] = nil
	if sd := Stdev(c); sd > 0 {
		out["cohen_dz"] = Round6(Mean(c) / sd)
	}
	return out
}

// PySum is CPython >= 3.12 sum() over floats: Neumaier compensated summation
// (needed for bit-identical results with V2).
func PySum(xs []float64) float64 {
	f, c := 0.0, 0.0
	for _, x := range xs {
		t := f + x
		if math.Abs(f) >= math.Abs(x) {
			c += (f - t) + x
		} else {
			c += (x - t) + f
		}
		f = t
	}
	if c != 0 && !math.IsInf(c, 0) && !math.IsNaN(c) {
		f += c
	}
	return f
}
