package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestLocatorBlockToken pins the 2×2-block collapse used to dedup target
// tokens. Storage is keyed on the even-anchored block so the disk footprint
// shrinks by ~4× on the locator portion; the read path always queries with
// a token that's already block-normalised.
func TestLocatorBlockToken(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"JO22", "JO22"},
		{"JO23", "JO22"},
		{"JO32", "JO22"},
		{"JO33", "JO22"},
		{"JO42", "JO42"},
		{"JO43", "JO42"},
		{"JO89", "JO88"},
		{"AA00", "AA00"},
		{"RR99", "RR88"},
	}
	for _, c := range cases {
		if got := locatorBlockToken(c.in); got != c.want {
			t.Errorf("locatorBlockToken(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeTargetsForBaselineCollapsesAndDedupes(t *testing.T) {
	// User typed JO32 with surroundings → 9 squares spanning 4 blocks.
	in := []string{"JO21", "JO22", "JO23", "JO31", "JO32", "JO33", "JO41", "JO42", "JO43"}
	got := normalizeTargetsForBaseline(in)
	want := map[string]bool{"JO20": true, "JO22": true, "JO40": true, "JO42": true}
	if len(got) != len(want) {
		t.Fatalf("got %d blocks (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for _, b := range got {
		if !want[b] {
			t.Errorf("unexpected block %q in %v", b, got)
		}
	}
}

func TestNormalizeTargetsForBaselinePassesCallsigns(t *testing.T) {
	in := []string{"DK3JF", "W1AW", "DK3JF"} // duplicate callsign
	got := normalizeTargetsForBaseline(in)
	if len(got) != 2 {
		t.Fatalf("got %v, want exactly 2 deduped callsigns", got)
	}
	// Order is insertion order — DK3JF comes first.
	if got[0] != "DK3JF" || got[1] != "W1AW" {
		t.Errorf("got %v, want [DK3JF W1AW]", got)
	}
}

func TestNormalizeTargetTokenUpperLocatorAndCallsign(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// 4-char locators collapse to 2×2 blocks. The helper expects
		// already-upper-cased input; case-folding happens in the caller
		// (normalizeTargetToken).
		{"JO32", "JO22"},
		{"FN31", "FN20"},
		// 6-char locators still match isLocator and collapse to a 4-char block.
		{"JO32WI", "JO22"},
		// Callsigns pass through unchanged.
		{"W1AW", "W1AW"},
		{"DK3JF", "DK3JF"},
		// Empty/short inputs pass through.
		{"", ""},
		{"X", "X"},
	}
	for _, c := range cases {
		got := normalizeTargetTokenUpper(c.in)
		if got != c.want {
			t.Errorf("normalizeTargetTokenUpper(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestLegacyV5SnapshotLoadCollapsesSource4AndBlocks writes a synthetic v5
// snapshot to disk with two source4 variants and two locator tokens that
// share a 2×2 block, then loads it through DxBaselineEngine.Load and checks
// that the in-memory maps reflect the merged v6 shape.
func TestLegacyV5SnapshotLoadCollapsesSource4AndBlocks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dx_baseline.json")

	// Old shape: source4 in the key. Two distinct source4 values collide on
	// the same (band, slot, dist, snr) tuple after v6 collapse — counts sum.
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
		// Two locator target tokens that share the same 2×2 block (JO22)
		// must merge after the v6 block-collapse, AND collapse their source4.
		"target_buckets": map[string]any{
			"JO32|20m|10|0|0|JN58": map[string]any{
				"band": "20m", "slot_of_day": 10,
				"distance_tier": 0, "snr_tier": 0,
				"source4": "JN58", "count": 3,
			},
			"JO33|20m|10|0|0|JN58": map[string]any{
				"band": "20m", "slot_of_day": 10,
				"distance_tier": 0, "snr_tier": 0,
				"source4": "JN58", "count": 2,
			},
			// Callsign target token: passes through unchanged.
			"W1AW|20m|10|0|0|JN58": map[string]any{
				"band": "20m", "slot_of_day": 10,
				"distance_tier": 0, "snr_tier": 0,
				"source4": "JN58", "count": 11,
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

	if got := len(e.targetBuckets); got != 2 {
		t.Fatalf("target buckets after load = %d, want 2 (JO22 merged + W1AW)", got)
	}
	jo22Key := baselineTargetKeyFromBase("JO22", baselineKey("20m", 10, 0, 0))
	if e.targetBuckets[jo22Key] == nil || e.targetBuckets[jo22Key].Count != 5 {
		t.Errorf("JO22 block target = %v, want count 5 (3+2)", e.targetBuckets[jo22Key])
	}
	w1awKey := baselineTargetKeyFromBase("W1AW", baselineKey("20m", 10, 0, 0))
	if e.targetBuckets[w1awKey] == nil || e.targetBuckets[w1awKey].Count != 11 {
		t.Errorf("W1AW target = %v, want count 11", e.targetBuckets[w1awKey])
	}
}

// TestSaveProducesV6Snapshot asserts the engine writes the current snapshot
// version on save, and that the persisted JSON has no source4 field on its
// buckets. (Version bumped to 7 when regional_buckets were added; the
// source4 invariant is still the v6 contract this test guards.)
func TestSaveProducesV6Snapshot(t *testing.T) {
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
	if v, ok := snap["version"].(float64); !ok || int(v) != 7 {
		t.Errorf("snapshot version = %v, want 7", snap["version"])
	}
	// regional_buckets may be omitted (omitempty) when empty — that's fine;
	// Load handles nil. Only assert presence when non-empty.
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
