package stats

import (
	"slices"
	"testing"
)

// Reference values were produced by CPython 3.14 random.Random and V2's
// benchmark_statistics functions.
func TestPyRandomMatchesCPython(t *testing.T) {
	cases := []struct {
		seed int64
		n    int
		want []int
	}{
		{20260911, 10, []int{9, 5, 0, 7, 1, 2, 8, 6, 5, 7, 4, 2}},
		{7, 1000, []int{331, 970, 154, 404, 666, 49}},
		{1<<40 + 5, 97, []int{64, 66, 34, 84, 3, 67}},
	}
	for _, c := range cases {
		r := NewPyRandom(c.seed)
		got := make([]int, len(c.want))
		for i := range got {
			got[i] = r.Randrange(c.n)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("seed %d: got %v want %v", c.seed, got, c.want)
		}
	}
	r := NewPyRandom(20260911)
	for _, want := range []float64{0.8089241653945822, 0.36215397726950616, 0.44857735108070806} {
		if got := r.Random(); got != want {
			t.Fatalf("random() = %v want %v", got, want)
		}
	}
}

func TestBootstrapMatchesV2(t *testing.T) {
	v := []float64{0.1, 0.5, -0.2, 0.9, 0.3, 0.0, 0.7}
	ci := BootstrapMeanCI(v, .95, 2000, 20260911)
	if ci["mean"] != 0.328571 || ci["ci_low"] != 0.071429 || ci["ci_high"] != 0.6 || ci["n"] != 7 {
		t.Fatalf("%v", ci)
	}
	e := PairedEffect(v, .95, 500, 11)
	if e["ci_low"] != 0.057143 || e["ci_high"] != 0.614286 || e["wins"] != 5 || e["ties"] != 1 || e["cohen_dz"] != 0.832656 || e["win_rate"] != 0.714286 {
		t.Fatalf("%v", e)
	}
	if one := BootstrapMeanCI([]float64{2}, .95, 10, 1); one["ci_low"] != 2.0 || one["resamples"] != 0 {
		t.Fatalf("%v", one)
	}
}
