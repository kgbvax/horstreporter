package proplab

import (
	"math"
	"sort"
)

// stats.go contains small statistical helpers used by the Ladder and Fusion
// engines.

// IntMedian returns the median of an int slice. The input is sorted in place.
func IntMedian(in []int) int {
	sort.Ints(in)
	n := len(in)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return in[n/2]
	}
	return (in[n/2-1] + in[n/2]) / 2
}

// PercentileInt returns the p-th percentile of an int slice using nearest-rank.
// The input is sorted in place. p is expected in the range [0,1].
func PercentileInt(in []int, p float64) float64 {
	sort.Ints(in)
	n := len(in)
	if n == 0 {
		return 0
	}
	if p <= 0 {
		return float64(in[0])
	}
	if p >= 1 {
		return float64(in[n-1])
	}
	idx := int(math.Ceil(float64(n)*p)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return float64(in[idx])
}

// PercentileFloat is PercentileInt for float64 slices (sorted in place).
func PercentileFloat(in []float64, p float64) float64 {
	sort.Float64s(in)
	n := len(in)
	if n == 0 {
		return 0
	}
	if p <= 0 {
		return in[0]
	}
	if p >= 1 {
		return in[n-1]
	}
	idx := int(math.Ceil(float64(n)*p)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return in[idx]
}
