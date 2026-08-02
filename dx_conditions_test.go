package main

import (
	"path/filepath"
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
