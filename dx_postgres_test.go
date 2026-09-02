package main

import (
	"testing"
	"time"

	"horstreporter/internal/region"
)

func TestDxPulseRegionBaselineKeysForSpot(t *testing.T) {
	ts := time.Date(2026, time.May, 29, 12, 10, 0, 0, time.UTC).Unix()

	t.Run("builds keys for both propagation directions", func(t *testing.T) {
		keys := dxPulseRegionBaselineKeysForSpot(ts, "20m", "FN31", "JO32")
		if len(keys) != 2 {
			t.Fatalf("expected 2 keys, got %d: %+v", len(keys), keys)
		}

		foundFn31EU := false
		foundJo32NA := false
		for _, key := range keys {
			if key.Band != "20m" {
				t.Fatalf("expected normalized band 20m, got %q", key.Band)
			}
			if key.SlotOfDay != utcSlotOfDay(ts) {
				t.Fatalf("expected slot %d, got %d", utcSlotOfDay(ts), key.SlotOfDay)
			}
			if key.DayIndex != utcDayIndex(ts) {
				t.Fatalf("expected day index %d, got %d", utcDayIndex(ts), key.DayIndex)
			}
			if key.TargetGrid4 == "FN31" && key.Region == string(region.EU) {
				foundFn31EU = true
			}
			if key.TargetGrid4 == "JO32" && key.Region == string(region.NA) {
				foundJo32NA = true
			}
		}
		if !foundFn31EU || !foundJo32NA {
			t.Fatalf("expected FN31/EU and JO32/NA keys, got %+v", keys)
		}
	})

	t.Run("deduplicates mirrored self-region keys", func(t *testing.T) {
		keys := dxPulseRegionBaselineKeysForSpot(ts, "20m", "JO32", "JO32")
		if len(keys) != 1 {
			t.Fatalf("expected 1 deduplicated key, got %d: %+v", len(keys), keys)
		}
		if keys[0].TargetGrid4 != "JO32" || keys[0].Region != string(region.EU) {
			t.Fatalf("unexpected deduplicated key: %+v", keys[0])
		}
	})

	t.Run("rejects invalid input", func(t *testing.T) {
		if keys := dxPulseRegionBaselineKeysForSpot(ts, "", "FN31", "JO32"); len(keys) != 0 {
			t.Fatalf("expected no keys for empty band, got %+v", keys)
		}
		if keys := dxPulseRegionBaselineKeysForSpot(ts, "20m", "", ""); len(keys) != 0 {
			t.Fatalf("expected no keys for missing locators, got %+v", keys)
		}
	})
}

func TestMergePendingBackMergesOnce(t *testing.T) {
	// Regression: mergePendingBack used to iterate the cluster map twice,
	// doubling pending cluster deltas on every failed flush. Compounding
	// across a PG outage (2s flush ticker) this grew counts to ~2^62 before
	// int64 wrap — the corrupted dx_baseline_cluster rows seen on prod.
	s := &dxPostgresStore{
		pendingGlobal:  make(map[baselineGlobalKey]baselineDelta),
		pendingRegion:  make(map[dxPulseRegionBaselineDailyKey]int64),
		pendingCluster: make(map[clusterBaselineKey]baselineDelta),
	}
	gk := baselineGlobalKey{Band: "40m", SlotOfDay: 39}
	ck := clusterBaselineKey{ClusterAnchor: "JN68", Band: "40m", SlotOfDay: 39}
	rk := dxPulseRegionBaselineDailyKey{TargetGrid4: "JO62", Band: "40m", Region: "EU"}

	s.mergePendingBack(
		map[baselineGlobalKey]baselineDelta{gk: {Count: 3}},
		map[dxPulseRegionBaselineDailyKey]int64{rk: 5},
		map[clusterBaselineKey]baselineDelta{ck: {Count: 7}},
	)
	if got := s.pendingCluster[ck].Count; got != 7 {
		t.Fatalf("cluster delta merged more than once: count=%d, want 7", got)
	}
	if got := s.pendingGlobal[gk].Count; got != 3 {
		t.Fatalf("global delta = %d, want 3", got)
	}
	if got := s.pendingRegion[rk]; got != 5 {
		t.Fatalf("region delta = %d, want 5", got)
	}
	if s.pendingCount != 3 {
		t.Fatalf("pendingCount = %d, want 3 (one per requeued entry)", s.pendingCount)
	}

	// A second failed flush requeues the same deltas again (once).
	s.mergePendingBack(
		map[baselineGlobalKey]baselineDelta{gk: {Count: 3}},
		map[dxPulseRegionBaselineDailyKey]int64{rk: 5},
		map[clusterBaselineKey]baselineDelta{ck: {Count: 7}},
	)
	if got := s.pendingCluster[ck].Count; got != 14 {
		t.Fatalf("cluster delta after two requeues = %d, want 14", got)
	}
}
