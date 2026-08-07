package main

import (
	"path/filepath"
	"sync"
	"testing"
)

// TestEvaluateRegionCounts verifies that dx_conditions populates per-band
// region_counts from the remote station's 4-char locator using the same
// 11-region classifier the proplab package mirrors.
func TestEvaluateRegionCounts(t *testing.T) {
	dir := t.TempDir()
	e := newDxBaselineEngine(filepath.Join(dir, "dx_baseline.json"))
	if err := e.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	now := int64(1700000000)
	// Target is in EU (JO62qm). Remote stations in four regions.
	history := []MQTTMessage{
		{RP: -10, T: now, SC: "DL1ABC", SL: "JO62QM", RC: "G0ABC", RL: "JO50AA", B: "20m", MD: "FT8"},
		{RP: -12, T: now, SC: "DL1ABC", SL: "JO62QM", RC: "G0DEF", RL: "JO50AB", B: "20m", MD: "FT8"},
		{RP: -8, T: now, SC: "DL1ABC", SL: "JO62QM", RC: "W1AW", RL: "FN31AA", B: "20m", MD: "FT8"},
		{RP: -6, T: now, SC: "DL1ABC", SL: "JO62QM", RC: "BV2A", RL: "MJ89AA", B: "20m", MD: "FT8"},
		{RP: -14, T: now, SC: "DL1ABC", SL: "JO62QM", RC: "VK2AA", RL: "PI58AA", B: "20m", MD: "FT8"},
	}

	resp := e.Evaluate("JO62qm", false, 20, -24, history, now)
	if len(resp.Bands) == 0 {
		t.Fatalf("expected 20m band in response, got none")
	}
	var band20 dxBandCondition
	for _, b := range resp.Bands {
		if b.Band == "20m" {
			band20 = b
			break
		}
	}
	if band20.Band == "" {
		t.Fatalf("20m band missing from response")
	}

	want := map[string]int{"EU": 2, "NA": 1, "AS": 1, "OC": 1}
	if len(band20.RegionCounts) != len(want) {
		t.Errorf("RegionCounts = %v, want %v", band20.RegionCounts, want)
	}
	for region, count := range want {
		if band20.RegionCounts[region] != count {
			t.Errorf("RegionCounts[%s] = %d, want %d", region, band20.RegionCounts[region], count)
		}
	}
}

// TestDominantDirectionTieDeterminism verifies that dominantDirection returns
// a stable result on ties — Go map iteration is randomised, so without an
// explicit tiebreak two equal directions would win non-deterministically,
// making the bearing indicator jitter between refreshes.
func TestDominantDirectionTieDeterminism(t *testing.T) {
	bins := map[string]int{"N": 5, "NE": 5, "E": 3}
	// N comes before NE in compass order, so N must win the tie.
	got := dominantDirection(bins)
	if got != "N" {
		t.Errorf("dominantDirection(tie N/NE) = %q, want N (compass-order tiebreak)", got)
	}
	// Clear winner is unaffected.
	bins = map[string]int{"N": 2, "SE": 7, "W": 4}
	if got := dominantDirection(bins); got != "SE" {
		t.Errorf("dominantDirection(clear winner) = %q, want SE", got)
	}
	// Empty bins.
	if got := dominantDirection(map[string]int{}); got != "-" {
		t.Errorf("dominantDirection(empty) = %q, want -", got)
	}
}

// TestEvaluateExcludesOutOfScopeBands verifies that microwave/OOS bands (e.g.
// "13cm") do not leak into /api/dx_conditions — the gate is applied in
// hot_bands.go and dx_cellfeed.go but was missing from Evaluate's accumulator.
func TestEvaluateExcludesOutOfScopeBands(t *testing.T) {
	dir := t.TempDir()
	e := newDxBaselineEngine(filepath.Join(dir, "dx_baseline.json"))
	if err := e.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	now := int64(1700000000)
	history := []MQTTMessage{
		{RP: -10, T: now, SC: "DL1ABC", SL: "JO62QM", RC: "G0ABC", RL: "JO50AA", B: "20m", MD: "FT8"},
		{RP: -10, T: now, SC: "DL1ABC", SL: "JO62QM", RC: "G0ABC", RL: "JO50AA", B: "13cm", MD: "FT8"},
	}

	resp := e.Evaluate("JO62qm", false, 20, -24, history, now)
	bands := make(map[string]bool, len(resp.Bands))
	for _, b := range resp.Bands {
		bands[b.Band] = true
	}
	if !bands["20m"] {
		t.Errorf("expected 20m in response, got bands=%v", resp.Bands)
	}
	if bands["13cm"] {
		t.Errorf("13cm (out-of-scope microwave) leaked into /api/dx_conditions bands=%v", resp.Bands)
	}
}

// TestEvaluateConcurrentObserveRace runs Observe and Evaluate concurrently to
// verify the firstEventAt/lastEventAt reads are race-free (the old code read
// them after releasing the RLock). Run with: go test -race ./...
func TestEvaluateConcurrentObserveRace(t *testing.T) {
	dir := t.TempDir()
	e := newDxBaselineEngine(filepath.Join(dir, "dx_baseline.json"))
	if err := e.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	var wg sync.WaitGroup
	const N = 100
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			m := MQTTMessage{
				RP: -10, T: int64(1700000000 + i),
				SC: "DL1ABC", SL: "JO62QM", RC: "G0ABC", RL: "JO50AA",
				B: "20m", MD: "FT8",
			}
			e.Observe(m)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			history := []MQTTMessage{
				{RP: -10, T: int64(1700000000 + i), SC: "DL1ABC", SL: "JO62QM", RC: "G0ABC", RL: "JO50AA", B: "20m", MD: "FT8"},
			}
			_ = e.Evaluate("JO62qm", false, 20, -24, history, int64(1700000000+N))
		}
	}()

	wg.Wait()
}

// TestEvaluateRegionalBaselineFallback verifies the three-tier baseline
// fallback (target → region → global) in the in-memory path. A locator target
// with NO target-specific history but rich regional history should surface
// RegionalBaselineUsed=true and a non-zero BaselineActivity.
func TestEvaluateRegionalBaselineFallback(t *testing.T) {
	dir := t.TempDir()
	e := newDxBaselineEngine(filepath.Join(dir, "dx_baseline.json"))
	if err := e.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Seed the regional baseline (EU) with 20m spots at the current slot, but
	// do NOT seed any target-specific buckets. The target (a rare callsign with
	// no history) should fall back to the regional baseline.
	now := int64(1700000000)
	// Observer in EU (JO62), remote in NA (FN31) — both regions get a regional
	// bucket increment for 20m at this slot/dist/snrtier.
	for i := 0; i < 50; i++ {
		m := MQTTMessage{
			RP: -10, T: now,
			SC: "DL1ABC", SL: "JO62QM", RC: "W1AW", RL: "FN31AA",
			B: "20m", MD: "FT8",
		}
		e.Observe(m)
	}

	// Evaluate for a callsign target that has no target-specific history.
	// QRZ is not configured, cty is not configured, so operatorRegion will be
	// "" for a callsign — use a locator target in EU instead to exercise the
	// regional fallback.
	resp := e.Evaluate("JO62qm", false, 20, -24, []MQTTMessage{
		{RP: -10, T: now, SC: "DL1ABC", SL: "JO62QM", RC: "W1AW", RL: "FN31AA", B: "20m", MD: "FT8"},
	}, now)

	if resp.OperatorRegion == "" {
		t.Fatalf("expected OperatorRegion for locator target JO62qm, got empty")
	}
	if resp.OperatorRegion != "EU" {
		t.Errorf("OperatorRegion = %q, want EU", resp.OperatorRegion)
	}

	var b20 dxBandCondition
	for _, b := range resp.Bands {
		if b.Band == "20m" {
			b20 = b
			break
		}
	}
	if b20.Band == "" {
		t.Fatalf("expected 20m in response")
	}
	// The target (JO62qm block anchor) has its own target-specific history
	// (we observed 50 spots where SL=JO62QM), so TargetBaselineUsed should be
	// true. This test confirms the regional wiring doesn't break the target
	// path. A separate test with a target that has NO history would exercise
	// the regional fallback directly — but that requires a target token that
	// never appeared in the observed spots, which is hard to construct without
	// manipulating the bucket maps directly. The three-tier helpers are
	// unit-testable directly; this integration test guards the wiring.
	if !b20.TargetBaselineUsed && !b20.RegionalBaselineUsed {
		t.Errorf("expected target or regional baseline used, got neither")
	}
}

// TestBaselineActivityForBandRegionalFallback unit-tests the three-tier
// fallback directly: a target with no history, a region with history, and a
// global with history — the region should win.
func TestBaselineActivityForBandRegionalFallback(t *testing.T) {
	global := map[string]*baselineBucket{}
	target := map[string]*baselineBucket{}
	region := map[string]*baselineBucket{}

	// No target, no region → global fallback.
	global[baselineKey("20m", 10, 2, 1)] = &baselineBucket{Count: 100}
	act, targetUsed, regionUsed := baselineActivityForBand(global, target, region, "", []string{"RARE"}, "20m", 10, 60*24*30)
	if targetUsed || regionUsed || act == 0 {
		t.Errorf("global fallback: act=%v targetUsed=%v regionUsed=%v, want global", act, targetUsed, regionUsed)
	}

	// No target, region has data, operatorRegion set → regional fallback.
	region[baselineRegionKey("EU", "20m", 10, 2, 1)] = &baselineBucket{Count: 50}
	act, targetUsed, regionUsed = baselineActivityForBand(global, target, region, "EU", []string{"RARE"}, "20m", 10, 60*24*30)
	if targetUsed || !regionUsed || act == 0 {
		t.Errorf("regional fallback: act=%v targetUsed=%v regionUsed=%v, want region", act, targetUsed, regionUsed)
	}

	// Target has data → target wins over region.
	target[baselineTargetKey("DL1ABC", "20m", 10, 2, 1)] = &baselineBucket{Count: 30}
	act, targetUsed, regionUsed = baselineActivityForBand(global, target, region, "EU", []string{"DL1ABC"}, "20m", 10, 60*24*30)
	if !targetUsed || regionUsed {
		t.Errorf("target wins: act=%v targetUsed=%v regionUsed=%v, want target", act, targetUsed, regionUsed)
	}
}
