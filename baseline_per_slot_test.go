package main

import (
	"math"
	"testing"
)

// TestNormalizeBaselineToSpotsPerMinute pins the fix for the inflated-baseline
// bug: an unknown span (historyMinutes <= 0) must yield 0 ("no baseline"),
// never a value divided by a 1-day floor — which, with months of accumulated
// counts, inflated the baseline by ~that many days.
func TestNormalizeBaselineToSpotsPerMinute(t *testing.T) {
	cases := []struct {
		name           string
		totalCount     float64
		historyMinutes int
		want           float64
	}{
		// The old bug: span reported as 0 → would have floored to 1 day and
		// returned 270000/30 = 9000/min. Must now be 0 (unavailable).
		{"unknown span returns zero", 270000, 0, 0},
		{"negative span returns zero", 100, -5, 0},
		// 90 days of history: 270000 / (90 * 30) = 100/min.
		{"ninety days", 270000, 90 * 24 * 60, 100},
		// Sub-day span is floored to 1 day (slot observed ~once).
		{"half day floors to one day", 300, 12 * 60, 10},
		{"exactly one day", 300, 24 * 60, 10},
		{"zero count", 0, 30 * 24 * 60, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeBaselineToSpotsPerMinute(tc.totalCount, tc.historyMinutes)
			if math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("normalizeBaselineToSpotsPerMinute(%v, %d) = %v, want %v",
					tc.totalCount, tc.historyMinutes, got, tc.want)
			}
		})
	}
}

// TestBaselineActivityForBandAllSlotsTargetWins exercises the per-slot helper
// when each slot has target rows: every slot should be marked target-used and
// the rate should match the per-slot count normalised over the history.
func TestBaselineActivityForBandAllSlotsTargetWins(t *testing.T) {
	const band = "20m"
	const historyMinutes = 24 * 60 // one day → denominator = 30 spots/slot

	global := map[string]*baselineBucket{}
	target := map[string]*baselineBucket{}
	targets := []string{"JO22"}

	// Two slots populated under target token: slot 24 (count 30), slot 25 (count 60).
	target[baselineQthKey("JO22", band, 24, 2, 2)] = &baselineBucket{
		Band: band, SlotOfDay: 24, DistanceTier: 2, SnrTier: 2, Count: 30,
	}
	target[baselineQthKey("JO22", band, 25, 2, 2)] = &baselineBucket{
		Band: band, SlotOfDay: 25, DistanceTier: 2, SnrTier: 2, Count: 60,
	}

	rates, used, _ := baselineActivityForBandAllSlots(global, target, nil, "", targets, band, historyMinutes)
	if got := len(rates); got != SlotsOfDay {
		t.Fatalf("rates len = %d, want %d", got, SlotsOfDay)
	}
	if got := len(used); got != SlotsOfDay {
		t.Fatalf("used len = %d, want %d", got, SlotsOfDay)
	}

	cases := []struct {
		slot     int
		wantRate float64
		wantUsed bool
	}{
		{24, 1.0, true}, // 30 / (1 * 30) = 1.0
		{25, 2.0, true}, // 60 / (1 * 30) = 2.0
		{0, 0.0, false},
		{47, 0.0, false},
	}
	for _, c := range cases {
		if math.Abs(rates[c.slot]-c.wantRate) > 1e-6 {
			t.Errorf("slot %d rate = %v, want %v", c.slot, rates[c.slot], c.wantRate)
		}
		if used[c.slot] != c.wantUsed {
			t.Errorf("slot %d used = %v, want %v", c.slot, used[c.slot], c.wantUsed)
		}
	}
}

// TestBaselineActivityForBandAllSlotsFallsBackPerSlot pins the slot-by-slot
// fallback: target rows win for the slots they cover, but slots without target
// data fall back to the global baseline independently. Used=false marks each
// such fallback slot.
func TestBaselineActivityForBandAllSlotsFallsBackPerSlot(t *testing.T) {
	const band = "20m"
	const historyMinutes = 24 * 60

	global := map[string]*baselineBucket{}
	target := map[string]*baselineBucket{}
	targets := []string{"JO22"}

	// Slot 24: target has 30, global has 999 (ignored — target wins).
	target[baselineQthKey("JO22", band, 24, 2, 2)] = &baselineBucket{
		Band: band, SlotOfDay: 24, Count: 30,
	}
	global[baselineKey(band, 24, 2, 2)] = &baselineBucket{
		Band: band, SlotOfDay: 24, Count: 999,
	}
	// Slot 30: only global has 90 → fallback.
	global[baselineKey(band, 30, 2, 2)] = &baselineBucket{
		Band: band, SlotOfDay: 30, Count: 90,
	}

	rates, used, _ := baselineActivityForBandAllSlots(global, target, nil, "", targets, band, historyMinutes)

	if math.Abs(rates[24]-1.0) > 1e-6 || !used[24] {
		t.Errorf("slot 24: rate=%v used=%v, want 1.0 true", rates[24], used[24])
	}
	if math.Abs(rates[30]-3.0) > 1e-6 || used[30] {
		t.Errorf("slot 30: rate=%v used=%v, want 3.0 false (global fallback)", rates[30], used[30])
	}
	// Empty slot: zero rate, not used.
	if rates[5] != 0 || used[5] {
		t.Errorf("slot 5: rate=%v used=%v, want 0 false", rates[5], used[5])
	}
}

// TestBaselineActivityForBandAllSlotsHistoryScaling verifies the
// per-slot normalisation matches normalizeBaselineToSpotsPerMinute. With
// 7 days of history, a slot count of 210 should normalise to 1.0/min
// (210 / (7 * 30)).
func TestBaselineActivityForBandAllSlotsHistoryScaling(t *testing.T) {
	const band = "20m"
	target := map[string]*baselineBucket{}
	target[baselineQthKey("JO22", band, 12, 2, 2)] = &baselineBucket{
		Band: band, SlotOfDay: 12, Count: 210,
	}
	rates, used, _ := baselineActivityForBandAllSlots(nil, target, nil, "", []string{"JO22"}, band, 7*24*60)
	if !used[12] {
		t.Fatalf("slot 12 should be target-used")
	}
	if math.Abs(rates[12]-1.0) > 1e-6 {
		t.Errorf("slot 12 rate = %v, want 1.0", rates[12])
	}
}
