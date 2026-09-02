package main

import (
	"strings"
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

func TestSplitTargetTokens(t *testing.T) {
	locators, calls := splitTargetTokens([]string{
		"JO62", "JO62QM", "JO62QM31", "DL1ABC", "K1ABC", "JO_2", "%", "", "DL",
	})
	wantLoc := map[string]bool{"JO62": true, "JO62QM": true, "JO62QM31": true}
	if len(locators) != len(wantLoc) {
		t.Fatalf("locators = %v, want %v", locators, wantLoc)
	}
	for _, l := range locators {
		if !wantLoc[l] {
			t.Fatalf("unexpected locator token %q", l)
		}
	}
	wantCalls := map[string]bool{"DL1ABC": true, "K1ABC": true, "JO_2": true, "%": true, "": true, "DL": true}
	if len(calls) != len(wantCalls) {
		t.Fatalf("calls = %v, want %v", calls, wantCalls)
	}
	for _, c := range calls {
		if !wantCalls[c] {
			t.Fatalf("unexpected callsign token %q", c)
		}
	}
}

func TestLocatorPrefixRange(t *testing.T) {
	// The range [lo, hi) must hold exactly the strings extending the prefix
	// (HasPrefix semantics) under byte-wise pattern-op ordering.
	cases := []struct {
		prefix, lo, hi string
		in, out        []string
	}{
		{prefix: "JO62", lo: "JO62", hi: "JO63",
			in: []string{"JO62", "JO620", "JO62AB", "JO62QM31"}, out: []string{"JO6", "JO63", "JO63AB"}},
		{prefix: "JO62QM", lo: "JO62QM", hi: "JO62QN",
			in: []string{"JO62QM", "JO62QM31", "JO62QMAA"}, out: []string{"JO62", "JO62QL", "JO62QN"}},
	}
	for _, c := range cases {
		lo, hi, ok := locatorPrefixRange(c.prefix)
		if !ok || lo != c.lo || hi != c.hi {
			t.Fatalf("locatorPrefixRange(%q) = (%q, %q, %v), want (%q, %q, true)", c.prefix, lo, hi, ok, c.lo, c.hi)
		}
		for _, s := range c.in {
			if !(s >= lo && s < hi) {
				t.Fatalf("locatorPrefixRange(%q): %q should be in [%q, %q)", c.prefix, s, lo, hi)
			}
		}
		for _, s := range c.out {
			if s >= lo && s < hi {
				t.Fatalf("locatorPrefixRange(%q): %q should NOT be in [%q, %q)", c.prefix, s, lo, hi)
			}
		}
	}
}

func TestAppendTargetArms(t *testing.T) {
	// Pure-locator targets → only indexable range arms, no callsign arm (a
	// non-indexable OR arm would force the planner back to a filter scan).
	locators, calls := splitTargetTokens([]string{"JO62QM"})
	if len(calls) != 0 {
		t.Fatalf("JO62QM should classify as locator, got calls=%v", calls)
	}
	arms := make([]string, 0, 2)
	args := []any{int64(1), int64(2)}
	arms, args, _ = appendTargetArms(arms, args, len(args)+1, locators, calls)
	if len(arms) != 1 {
		t.Fatalf("expected 1 range arm, got %d: %v", len(arms), arms)
	}
	if strings.Contains(arms[0], "ANY") {
		t.Fatalf("locator-only targets must not produce a callsign arm: %v", arms)
	}
	if !strings.Contains(arms[0], "~>=~ $3") || !strings.Contains(arms[0], "~<~ $4") {
		t.Fatalf("range arm should use params $3/$4 (after 2 leading args): %v", arms)
	}
	if len(args) != 4 || args[2] != "JO62QM" || args[3] != "JO62QN" {
		t.Fatalf("args = %v, want [1 2 JO62QM JO62QN]", args)
	}

	// Mixed targets → range arm + callsign arm with the callsign list last.
	arms = nil
	args = []any{int64(1)}
	arms, args, _ = appendTargetArms(arms, args, len(args)+1, []string{"JO62"}, []string{"DL1ABC"})
	if len(arms) != 2 {
		t.Fatalf("expected 2 arms, got %v", arms)
	}
	if !strings.Contains(arms[1], "= ANY($4)") {
		t.Fatalf("callsign arm should use param $4 (after 1 leading arg + 2 range args): %v", arms)
	}
	if got, ok := args[3].([]string); !ok || len(got) != 1 || got[0] != "DL1ABC" {
		t.Fatalf("callsign arg = %v, want [DL1ABC]", args[3])
	}
}
