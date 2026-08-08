package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestLocatorClusterAnchor pins the 6×6 grid-cluster anchor computation.
// Each locator maps to the bottom-left square of its 6×6 cluster.
func TestLocatorClusterAnchor(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"JO62", "JN68", true},  // Berlin
		{"JO62QM", "JN68", true}, // 6-char subsquare → same cluster
		{"FN31", "EM86", true},  // New York
		{"CM87", "CM46", true},  // San Francisco
		{"AA00", "AA00", true},  // origin
		{"W1AW", "", false},     // callsign → not a locator
		{"", "", false},         // empty
	}
	for _, c := range cases {
		got, ok := locatorClusterAnchor(c.in)
		if ok != c.ok {
			t.Errorf("locatorClusterAnchor(%q) ok = %v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && got != c.want {
			t.Errorf("locatorClusterAnchor(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestGetSquaresInCluster returns 36 squares for a valid locator.
func TestGetSquaresInCluster(t *testing.T) {
	squares := getSquaresInCluster("JO62")
	if len(squares) != 36 {
		t.Fatalf("getSquaresInCluster(JO62) = %d squares, want 36", len(squares))
	}
	// Non-locator passes through.
	if got := getSquaresInCluster("W1AW"); len(got) != 1 || got[0] != "W1AW" {
		t.Errorf("getSquaresInCluster(W1AW) = %v, want [W1AW]", got)
	}
}

// TestLegacyV5SnapshotLoadCollapsesSource4 writes a synthetic v5 snapshot
// with source4 in the key, then loads it and checks that the global buckets
// merge correctly. (The per-call and 11-region tiers were removed in v8;
// legacy target_buckets/regional_buckets are discarded — the cluster tier
// is rebuilt from dx_raw_spots on startup.)
func TestLegacyV5SnapshotLoadCollapsesSource4(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dx_baseline.json")

	legacy := map[string]any{
		"version":  5,
		"saved_at": int64(1700000000),
		"buckets": map[string]any{
			"20m|10|0|0|JO31": map[string]any{
				"band": "20m", "slot_of_day": 10,
				"distance_tier": 0, "snr_tier": 0,
				"source4": "JO31", "count": 7,
			},
			"20m|10|0|0|JO32": map[string]any{
				"band": "20m", "slot_of_day": 10,
				"distance_tier": 0, "snr_tier": 0,
				"source4": "JO32", "count": 5,
			},
		},
		// Legacy target_buckets are discarded in v8 (per-call tier removed).
		"target_buckets": map[string]any{
			"JO32|20m|10|0|0|JN58": map[string]any{
				"band": "20m", "slot_of_day": 10,
				"distance_tier": 0, "snr_tier": 0,
				"source4": "JN58", "count": 3,
			},
		},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy snapshot: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write legacy snapshot: %v", err)
	}

	e := newDxBaselineEngine(path)
	if err := e.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := len(e.buckets); got != 1 {
		t.Fatalf("buckets after load = %d, want 1 (two source4 variants must merge)", got)
	}
	gk := baselineKey("20m", 10, 0, 0)
	if e.buckets[gk] == nil || e.buckets[gk].Count != 12 {
		t.Errorf("global bucket count = %v, want 12 (7+5)", e.buckets[gk])
	}
	// Legacy target_buckets are discarded in v8.
	if len(e.clusterBuckets) != 0 {
		t.Errorf("qthBuckets should be empty (per-call tier removed), got %d", len(e.clusterBuckets))
	}
}

// TestSaveProducesV8Snapshot asserts the engine writes version 8 on save,
// and that the persisted JSON has no source4 field on its buckets.
func TestSaveProducesV8Snapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dx_baseline.json")

	e := newDxBaselineEngine(path)
	e.buckets[baselineKey("20m", 10, 0, 0)] = &baselineBucket{
		Band: "20m", SlotOfDay: 10, DistanceTier: 0, SnrTier: 0, Count: 42,
	}
	if err := e.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	var snap map[string]any
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("parse saved file: %v", err)
	}
	if v, ok := snap["version"].(float64); !ok || int(v) != 8 {
		t.Errorf("snapshot version = %v, want 8", snap["version"])
	}
	// The first bucket should not have a source4 field.
	buckets, _ := snap["buckets"].(map[string]any)
	for _, b := range buckets {
		entry, _ := b.(map[string]any)
		if _, has := entry["source4"]; has {
			t.Errorf("saved bucket still has source4: %+v", entry)
		}
		break
	}
}