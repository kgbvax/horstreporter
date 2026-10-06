package main

import (
	"math"
	"sort"
)

// rankIndex is the index percentileFloat / percentileInt pick for p in (0, 1)
// out of n values: ceil(n*p)-1, clamped.
func rankIndex(n int, p float64) int {
	idx := int(math.Ceil(float64(n)*p)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return idx
}

// medianAndP90Float is percentileFloat(in, 0.5) and percentileFloat(in, 0.9)
// from one copy and two selections instead of two copies and two sorts (the
// distance lists of a busy area hold hundreds of thousands of values).
func medianAndP90Float(in []float64) (median, p90 float64) {
	n := len(in)
	if n == 0 {
		return 0, 0
	}
	for _, v := range in { // NaN has no rank in a selection; sort as before
		if v != v {
			return percentileFloat(in, 0.5), percentileFloat(in, 0.9)
		}
	}
	cp := append([]float64(nil), in...)
	k90 := rankIndex(n, 0.9)
	k50 := rankIndex(n, 0.5)
	p90 = selectFloat(cp, k90)
	// After the selection everything before k90 is <= p90: the median is the
	// k50-th smallest of that prefix (k50 <= k90 for any n).
	median = selectFloat(cp[:k90+1], k50)
	return median, p90
}

// selectFloat returns the k-th smallest (0-based) value of a, partially
// reordering it so a[:k] <= a[k] <= a[k+1:].
func selectFloat(a []float64, k int) float64 {
	lo, hi := 0, len(a)-1
	for lo < hi {
		// Median of three as the pivot keeps sorted and reversed input linear.
		mid := lo + (hi-lo)/2
		if a[mid] < a[lo] {
			a[mid], a[lo] = a[lo], a[mid]
		}
		if a[hi] < a[lo] {
			a[hi], a[lo] = a[lo], a[hi]
		}
		if a[hi] < a[mid] {
			a[hi], a[mid] = a[mid], a[hi]
		}
		pivot := a[mid]
		i, j := lo, hi
		for i <= j {
			for a[i] < pivot {
				i++
			}
			for a[j] > pivot {
				j--
			}
			if i <= j {
				a[i], a[j] = a[j], a[i]
				i++
				j--
			}
		}
		switch {
		case k <= j:
			hi = j
		case k >= i:
			lo = i
		default:
			return a[k]
		}
	}
	return a[k]
}

// medianAndP90Int is percentileInt(in, 0.5) and percentileInt(in, 0.9). SNR
// values sit in a few dozen distinct dB steps, so a counting pass replaces the
// copy and the sort when the spread is small.
func medianAndP90Int(in []int) (median, p90 float64) {
	n := len(in)
	if n == 0 {
		return 0, 0
	}
	lo, hi := in[0], in[0]
	for _, v := range in {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	if hi-lo >= 4096 {
		cp := append([]int(nil), in...)
		sort.Ints(cp)
		return float64(cp[rankIndex(n, 0.5)]), float64(cp[rankIndex(n, 0.9)])
	}
	counts := make([]int, hi-lo+1)
	for _, v := range in {
		counts[v-lo]++
	}
	at := func(rank int) float64 { // the rank-th smallest value
		seen := 0
		for i, c := range counts {
			seen += c
			if seen > rank {
				return float64(lo + i)
			}
		}
		return float64(hi)
	}
	return at(rankIndex(n, 0.5)), at(rankIndex(n, 0.9))
}
