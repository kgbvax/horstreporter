package main

import (
	"fmt"
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

// rawSpotTestRow builds a rawSpotRow with the given timestamp and callsigns so
// the SQL arms can assert per-row parameter placement. The builder reads the
// precomputed columns (band/sc/rc/sl/rl/md), not the message fields, so both
// are populated.
func rawSpotTestRow(t int64, sc, rc string) rawSpotRow {
	return rawSpotRow{
		m:       MQTTMessage{RP: -12, T: t, SC: sc, SL: "JO62", RC: rc, RL: "FN31", B: "20m", MD: "FT8"},
		band:    "20m",
		source4: "JO62",
		sc:      sc,
		rc:      rc,
		sl:      "JO62",
		rl:      "FN31",
		md:      "FT8",
	}
}

func TestBuildRawSpotInsertSQLChunkArms(t *testing.T) {
	// Chunk-boundary arms of the multi-VALUES raw-spot INSERT builder that the
	// existing TestBuildRawSpotInsertSQL (main_test.go) does not cover: the
	// single-row ceiling (no $14+ on a 1-row chunk) and the empty-chunk shape.
	t.Run("single row stops at $13", func(t *testing.T) {
		q, args := buildRawSpotInsertSQL([]rawSpotRow{rawSpotTestRow(1700000000, "DL1ABC", "K1ABC")})
		if len(args) != 13 {
			t.Fatalf("args = %d, want 13", len(args))
		}
		if !strings.Contains(q, "$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13") {
			t.Fatalf("VALUES group missing $1..$13: %q", q)
		}
		if strings.Contains(q, "$14") {
			t.Fatalf("single-row SQL must not reference $14+: %q", q)
		}
		// Arg order: T, band, sc, rc, sl, rl, md, RP, source4, source_type
		// "mqtt" (literal), empty spotter, nil frequency, empty comment.
		want := []any{
			int64(1700000000), "20m", "DL1ABC", "K1ABC", "JO62", "FN31", "FT8", -12,
			"JO62", "mqtt", "", (*float64)(nil), "",
		}
		for i, w := range want {
			if args[i] != w {
				t.Fatalf("args[%d] = %v, want %v (full: %v)", i, args[i], w, args)
			}
		}
	})

	t.Run("empty chunk yields header only with no args", func(t *testing.T) {
		q, args := buildRawSpotInsertSQL(nil)
		if len(args) != 0 {
			t.Fatalf("args = %v, want none", args)
		}
		if !strings.Contains(q, "INSERT INTO dx_raw_spots") {
			t.Fatalf("SQL missing table: %q", q)
		}
		if strings.Contains(q, "($") {
			t.Fatalf("empty chunk must not emit a VALUES group: %q", q)
		}
	})
}

func TestRawSpotInsertArgs(t *testing.T) {
	// Normalization arms of the single-row INSERT (insertRawSpot's column
	// values), now extracted into rawSpotInsertArgs.
	t.Run("empty band falls back to the message band, then unknown", func(t *testing.T) {
		m := MQTTMessage{T: 42, B: "40m"}
		args := rawSpotInsertArgs(m, "", "mqtt", "", nil, "")
		if args[1] != "40m" {
			t.Fatalf("band fallback = %v, want 40m", args[1])
		}
		args = rawSpotInsertArgs(MQTTMessage{T: 42}, "", "mqtt", "", nil, "")
		if args[1] != "unknown" {
			t.Fatalf("unresolvable band = %v, want unknown", args[1])
		}
		if got, ok := args[0].(int64); !ok || got != 42 {
			t.Fatalf("spot_time arg = %v, want 42", args[0])
		}
	})

	t.Run("empty sourceType defaults to mqtt and is lowercased", func(t *testing.T) {
		if got := rawSpotInsertArgs(MQTTMessage{}, "20m", "", "", nil, "")[9]; got != "mqtt" {
			t.Fatalf("blank sourceType = %v, want mqtt", got)
		}
		if got := rawSpotInsertArgs(MQTTMessage{}, "20m", "DXCLUSTER", "", nil, "")[9]; got != "dxcluster" {
			t.Fatalf("dxcluster sourceType = %v, want lowercased dxcluster", got)
		}
	})

	t.Run("callsigns, locators and mode are upper-trimmed", func(t *testing.T) {
		m := MQTTMessage{T: 1, SC: " dl1abc ", RC: " k1abc ", SL: " jo62 ", RL: " fn31 ", MD: " ft8 "}
		args := rawSpotInsertArgs(m, "20m", "mqtt", " spotter1 ", nil, " hello ")
		if args[2] != "DL1ABC" || args[3] != "K1ABC" {
			t.Fatalf("callsign args = %v, %v", args[2], args[3])
		}
		if args[4] != "JO62" || args[5] != "FN31" {
			t.Fatalf("locator args = %v, %v", args[4], args[5])
		}
		if args[6] != "FT8" {
			t.Fatalf("mode arg = %v, want FT8", args[6])
		}
		if args[10] != "SPOTTER1" {
			t.Fatalf("spotter arg = %v, want SPOTTER1", args[10])
		}
		if args[12] != "hello" {
			t.Fatalf("comment arg = %v, want trimmed hello", args[12])
		}
	})

	t.Run("frequency pointer passes through as-is", func(t *testing.T) {
		f := 14180.5
		if got, ok := rawSpotInsertArgs(MQTTMessage{}, "20m", "mqtt", "", &f, "")[11].(*float64); !ok || *got != 14180.5 {
			t.Fatalf("frequency arg = %v, want 14180.5", got)
		}
		freq := 14095.0
		if got, ok := rawSpotInsertArgs(MQTTMessage{}, "20m", "mqtt", "", &freq, "")[11].(*float64); !ok || *got != 14095.0 {
			t.Fatalf("frequency arg = %v, want 14095", got)
		}
	})
}

func TestRawSpotSingleInsertSQLMatchesBuilder(t *testing.T) {
	// The single-row INSERT and the multi-VALUES builder must stay in lockstep
	// on the column list — they write the same table with the same 13 columns.
	qSingle, argsSingle := rawSpotSingleInsertSQL, rawSpotInsertArgs(MQTTMessage{}, "", "", "", nil, "")
	if len(argsSingle) != 13 {
		t.Fatalf("single-row arg count = %d, want 13", len(argsSingle))
	}
	qMulti, argsMulti := buildRawSpotInsertSQL([]rawSpotRow{rawSpotTestRow(1, "A", "B")})
	if len(argsMulti) != 13 {
		t.Fatalf("builder arg count = %d, want 13", len(argsMulti))
	}
	cols := func(q string) string {
		start := strings.Index(q, "dx_raw_spots (")
		if start < 0 {
			return ""
		}
		start += len("dx_raw_spots (")
		end := strings.Index(q[start:], ")")
		if end < 0 {
			return ""
		}
		return strings.Join(strings.Fields(q[start:start+end]), " ")
	}
	if cols(qSingle) == "" || cols(qSingle) != cols(qMulti) {
		t.Fatalf("column lists diverge:\nsingle: %q\nmulti:  %q", cols(qSingle), cols(qMulti))
	}
}

func TestTrimRawSpotRows(t *testing.T) {
	// Regression for the UNLOGGED-spots remediation: the retry buffer must
	// converge instead of growing without bound, dropping the OLDEST rows and
	// preserving the order of everything it keeps (Sep 4 OOM).
	mk := func(n, t0 int) []rawSpotRow {
		rows := make([]rawSpotRow, 0, n)
		for i := 0; i < n; i++ {
			rows = append(rows, rawSpotTestRow(int64(t0+i), fmt.Sprintf("DL%d", i), "K1ABC"))
		}
		return rows
	}

	t.Run("within capacity keeps everything", func(t *testing.T) {
		rows := mk(5, 100)
		got, dropped := trimRawSpotRows(rows, 10)
		if dropped != 0 {
			t.Fatalf("dropped = %d, want 0", dropped)
		}
		if len(got) != 5 || got[0].m.T != 100 || got[4].m.T != 104 {
			t.Fatalf("rows changed unexpectedly: len=%d", len(got))
		}
	})

	t.Run("over capacity keeps the newest in order and reports the drop count", func(t *testing.T) {
		rows := mk(10, 0) // t = 0..9
		got, dropped := trimRawSpotRows(rows, 4)
		if dropped != 6 {
			t.Fatalf("dropped = %d, want 6", dropped)
		}
		if len(got) != 4 {
			t.Fatalf("len = %d, want 4", len(got))
		}
		for i, r := range got {
			if r.m.T != int64(6+i) {
				t.Fatalf("got[%d].m.T = %d, want %d (newest kept, order preserved)", i, r.m.T, 6+i)
			}
		}
	})

	t.Run("trimmed slice owns its capacity so appends cannot clobber it", func(t *testing.T) {
		// The original trim re-sliced with a full cap expression precisely so
		// appending to the trimmed buffer cannot overwrite the dropped region.
		rows := mk(10, 0)
		got, _ := trimRawSpotRows(rows, 4)
		if cap(got) != len(got) {
			t.Fatalf("cap = %d, want %d", cap(got), len(got))
		}
	})

	t.Run("exact fit is not an overflow", func(t *testing.T) {
		rows := mk(4, 0)
		_, dropped := trimRawSpotRows(rows, 4)
		if dropped != 0 {
			t.Fatalf("dropped = %d, want 0", dropped)
		}
	})
}

func TestMergeRawSpotRowsOrder(t *testing.T) {
	// The failed batch is requeued AHEAD of rows that arrived during the swap
	// so the retry order stays oldest-first (arrival order is roughly stable).
	requeued := []rawSpotRow{rawSpotTestRow(1, "A", "B"), rawSpotTestRow(2, "C", "D")}
	pending := []rawSpotRow{rawSpotTestRow(3, "E", "F")}
	got := mergeRawSpotRows(requeued, pending)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i, want := range []int64{1, 2, 3} {
		if got[i].m.T != want {
			t.Fatalf("got[%d].m.T = %d, want %d", i, got[i].m.T, want)
		}
	}

	// No rows to requeue → empty result, nothing to prepend.
	if got := mergeRawSpotRows(nil, pending); len(got) != 1 {
		t.Fatalf("empty requeue merged %d rows, want 1 passthrough", len(got))
	}
}

func TestMergeRawSpotsBack(t *testing.T) {
	// Direct-struct instantiation (no pool), following TestMergePendingBack.
	t.Run("prepends failed rows ahead of rows added since the swap", func(t *testing.T) {
		s := &dxPostgresStore{pendingRawSpots: []rawSpotRow{rawSpotTestRow(3, "E", "F")}}
		s.mergeRawSpotsBack([]rawSpotRow{rawSpotTestRow(1, "A", "B"), rawSpotTestRow(2, "C", "D")})
		if len(s.pendingRawSpots) != 3 {
			t.Fatalf("len = %d, want 3", len(s.pendingRawSpots))
		}
		for i, want := range []int64{1, 2, 3} {
			if s.pendingRawSpots[i].m.T != want {
				t.Fatalf("pendingRawSpots[%d].m.T = %d, want %d (requeued first)", i, s.pendingRawSpots[i].m.T, want)
			}
		}
	})

	t.Run("overlapping rows are requeued as-is, not deduplicated", func(t *testing.T) {
		// Characterization: mergeRawSpotsBack re-enqueues exactly what failed;
		// dedup against rows that arrived during the swap happens at flush
		// time, not here. Both copies must survive so nothing is dropped by
		// the retry path (the data-loss direction is dropping, not duplicating).
		dup := rawSpotTestRow(1, "A", "B")
		s := &dxPostgresStore{pendingRawSpots: []rawSpotRow{dup}}
		s.mergeRawSpotsBack([]rawSpotRow{dup, dup})
		if len(s.pendingRawSpots) != 3 {
			t.Fatalf("len = %d, want 3 (overlap must not drop rows)", len(s.pendingRawSpots))
		}
	})

	t.Run("empty requeue is a no-op", func(t *testing.T) {
		s := &dxPostgresStore{pendingRawSpots: []rawSpotRow{rawSpotTestRow(1, "A", "B")}}
		s.mergeRawSpotsBack(nil)
		if len(s.pendingRawSpots) != 1 {
			t.Fatalf("len = %d, want 1", len(s.pendingRawSpots))
		}
	})

	t.Run("requeue above capacity trims the oldest", func(t *testing.T) {
		rows := make([]rawSpotRow, 0, rawSpotMaxPendingRows+2)
		for i := 0; i < rawSpotMaxPendingRows+2; i++ {
			rows = append(rows, rawSpotTestRow(int64(i), "A", "B"))
		}
		s := &dxPostgresStore{}
		s.mergeRawSpotsBack(rows)
		if len(s.pendingRawSpots) != rawSpotMaxPendingRows {
			t.Fatalf("len = %d, want %d (trimmed)", len(s.pendingRawSpots), rawSpotMaxPendingRows)
		}
		if s.pendingRawSpots[0].m.T != 2 || s.pendingRawSpots[len(s.pendingRawSpots)-1].m.T != int64(rawSpotMaxPendingRows+1) {
			t.Fatalf("trim dropped the wrong end: first=%d last=%d",
				s.pendingRawSpots[0].m.T, s.pendingRawSpots[len(s.pendingRawSpots)-1].m.T)
		}
	})
}

func TestFlushHealthAccounting(t *testing.T) {
	t.Run("zeroed store reports no health at all", func(t *testing.T) {
		s := &dxPostgresStore{}
		rawLastOK, rawStreak, baseLastOK, baseStreak := s.FlushHealth()
		if rawLastOK != 0 || rawStreak != 0 || baseLastOK != 0 || baseStreak != 0 {
			t.Fatalf("FlushHealth = %d,%d,%d,%d, want zeros", rawLastOK, rawStreak, baseLastOK, baseStreak)
		}
	})

	t.Run("synthetic failures count into the streak and FlushHealth", func(t *testing.T) {
		s := &dxPostgresStore{}
		err := fmt.Errorf("connection refused")
		s.reportFlushFailure("raw spot", err, &s.rawFlushFailStreak, &s.rawFlushLastOKUnix)
		s.reportFlushFailure("raw spot", err, &s.rawFlushFailStreak, &s.rawFlushLastOKUnix)
		s.reportFlushFailure("baseline", err, &s.baselineFlushFailStreak, &s.baselineFlushLastOKUnix)
		if s.rawFlushFailStreak.Load() != 2 {
			t.Fatalf("raw streak = %d, want 2", s.rawFlushFailStreak.Load())
		}
		rawLastOK, rawStreak, baseLastOK, baseStreak := s.FlushHealth()
		if rawStreak != 2 || baseStreak != 1 {
			t.Fatalf("FlushHealth streaks = %d/%d, want 2/1", rawStreak, baseStreak)
		}
		if rawLastOK != 0 || baseLastOK != 0 {
			t.Fatalf("failing flushes must not set lastOK: %d/%d", rawLastOK, baseLastOK)
		}
	})

	t.Run("success clears the streak and stamps lastOK", func(t *testing.T) {
		s := &dxPostgresStore{}
		before := time.Now().Unix()
		recordFlushSuccess(&s.rawFlushFailStreak, &s.rawFlushLastOKUnix)
		if s.rawFlushFailStreak.Load() != 0 {
			t.Fatalf("streak = %d, want 0 after success", s.rawFlushFailStreak.Load())
		}
		if last := s.rawFlushLastOKUnix.Load(); last < before || last > time.Now().Unix() {
			t.Fatalf("lastOK = %d, want within [%d, now]", last, before)
		}

		// Degraded → recovered: a streak built up by failures must reset to 0.
		s.reportFlushFailure("raw spot", fmt.Errorf("boom"), &s.rawFlushFailStreak, &s.rawFlushLastOKUnix)
		if s.rawFlushFailStreak.Load() != 1 {
			t.Fatalf("pre-recovery streak = %d, want 1", s.rawFlushFailStreak.Load())
		}
		recordFlushSuccess(&s.rawFlushFailStreak, &s.rawFlushLastOKUnix)
		if _, rawStreak, _, _ := s.FlushHealth(); rawStreak != 0 {
			t.Fatalf("streak after recovery = %d, want 0", rawStreak)
		}
	})

	t.Run("streaks escalate only at the documented thresholds", func(t *testing.T) {
		// reportFlushFailure logs the escalation at 15/450/...; the streak
		// itself must accumulate monotonically regardless of the log level.
		s := &dxPostgresStore{}
		for i := 0; i < 15; i++ {
			s.reportFlushFailure("raw spot", fmt.Errorf("down"), &s.rawFlushFailStreak, &s.rawFlushLastOKUnix)
		}
		if got := s.rawFlushFailStreak.Load(); got != 15 {
			t.Fatalf("streak = %d, want 15", got)
		}
	})
}

func TestBandPairsQueryArm(t *testing.T) {
	// Only dx_baseline_global is queried (the target table was removed in v8);
	// band/slot are the two positional parameters.
	q, args := bandPairsQuery("20m", 39)
	if !strings.Contains(q, "FROM dx_baseline_global") {
		t.Fatalf("query targets the wrong table: %q", q)
	}
	if !strings.Contains(q, "band = $1") || !strings.Contains(q, "slot_of_day = $2") {
		t.Fatalf("arm placeholders wrong: %q", q)
	}
	if strings.Contains(q, "dx_baseline_target") {
		t.Fatalf("removed target table must not be referenced: %q", q)
	}
	if len(args) != 2 || args[0] != "20m" || args[1] != 39 {
		t.Fatalf("args = %v, want [20m 39]", args)
	}
}

// fakeBandSlotRows is a hand-rolled bandSlotRowSource feeding
// scanBandSlotPairs in tests — no pgx pool required.
type fakeBandSlotRows struct {
	rows   [][5]any // band, slot, distance_tier, snr_tier, count
	i      int
	err    error
	errAt  int // rows.Scan fails when i == errAt (1-indexed scan); 0 = never
	closed bool
}

func (f *fakeBandSlotRows) Next() bool { return f.i < len(f.rows) }

func (f *fakeBandSlotRows) Scan(dest ...any) error {
	if f.errAt != 0 && f.i == f.errAt-1 {
		return fmt.Errorf("scan failed at row %d", f.i)
	}
	r := f.rows[f.i]
	*dest[0].(*string) = r[0].(string)
	*dest[1].(*int) = r[1].(int)
	*dest[2].(*int) = r[2].(int)
	*dest[3].(*int) = r[3].(int)
	*dest[4].(*int64) = r[4].(int64)
	f.i++
	return nil
}

func (f *fakeBandSlotRows) Err() error { return f.err }

func (f *fakeBandSlotRows) Close() { f.closed = true }

func TestScanBandSlotPairs(t *testing.T) {
	t.Run("groups rows by (band, slot) preserving scan order", func(t *testing.T) {
		rows := &fakeBandSlotRows{rows: [][5]any{
			{"20m", 38, 1, 2, int64(7)},
			{"20m", 38, 3, 0, int64(9)},
			{"40m", 39, 2, 1, int64(4)},
			{"20m", 40, 1, 1, int64(1)},
		}}
		idx, err := scanBandSlotPairs(rows)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if !rows.closed {
			t.Fatalf("rows must be closed after the scan")
		}
		if len(idx) != 3 {
			t.Fatalf("keys = %d, want 3", len(idx))
		}
		got := idx[bandSlotKey{Band: "20m", Slot: 38}]
		if len(got) != 2 || got[0] != (baselinePair{1, 2, 7}) || got[1] != (baselinePair{3, 0, 9}) {
			t.Fatalf("grouped pairs = %+v", got)
		}
		if got := idx[bandSlotKey{Band: "40m", Slot: 39}]; len(got) != 1 || got[0].Count != 4 {
			t.Fatalf("40m pairs = %+v", got)
		}
	})

	t.Run("scan error aborts with the error", func(t *testing.T) {
		rows := &fakeBandSlotRows{rows: [][5]any{
			{"20m", 38, 1, 2, int64(7)},
			{"20m", 39, 1, 2, int64(9)},
		}, errAt: 2}
		idx, err := scanBandSlotPairs(rows)
		if err == nil {
			t.Fatalf("expected a scan error")
		}
		if idx != nil {
			t.Fatalf("expected nil index on error, got %+v", idx)
		}
	})

	t.Run("rows.Err is surfaced", func(t *testing.T) {
		rows := &fakeBandSlotRows{rows: [][5]any{{"20m", 38, 1, 1, int64(1)}}, err: fmt.Errorf("conn lost")}
		idx, err := scanBandSlotPairs(rows)
		if err == nil || err.Error() != "conn lost" {
			t.Fatalf("err = %v, want conn lost", err)
		}
		if idx == nil {
			t.Fatalf("rows.Err path still returns the accumulated index")
		}
		if idx[bandSlotKey{Band: "20m", Slot: 38}][0].Count != 1 {
			t.Fatalf("unexpected index contents: %+v", idx)
		}
	})
}

func TestInitSchemaStmtsArms(t *testing.T) {
	// Regression guard for the Sep'26 raw-spots data-loss incident: the
	// dx_raw_spots DDL must stay LOGGED (never UNLOGGED) so a crash can't wipe
	// history, and spot_geom must stay dropped.
	stmts := initSchemaStmts()
	if len(stmts) == 0 {
		t.Fatalf("no schema statements")
	}
	for _, q := range stmts {
		upper := strings.ToUpper(q)
		if strings.Contains(upper, "UNLOGGED") {
			t.Fatalf("dx_raw_spots durability regression: schema statement is UNLOGGED: %q", q)
		}
	}
	var hasRawTable, hasGeomDrop, hasSpotTimeIndex, hasCellTable, hasSWTable bool
	for _, q := range stmts {
		if strings.Contains(q, "CREATE TABLE IF NOT EXISTS dx_raw_spots") {
			hasRawTable = true
			if !strings.Contains(q, "spot_time BIGINT NOT NULL") || !strings.Contains(q, "source_type TEXT NOT NULL DEFAULT 'mqtt'") {
				t.Fatalf("dx_raw_spots DDL drifted: %q", q)
			}
		}
		if strings.Contains(q, "DROP COLUMN IF EXISTS spot_geom") {
			hasGeomDrop = true
		}
		if strings.Contains(q, "idx_dx_raw_spots_spot_time") && strings.Contains(q, "CREATE INDEX") {
			hasSpotTimeIndex = true
		}
		if strings.Contains(q, "CREATE TABLE IF NOT EXISTS proplab_cell_buckets") {
			hasCellTable = true
		}
		if strings.Contains(q, "CREATE TABLE IF NOT EXISTS proplab_sw_series") {
			hasSWTable = true
		}
	}
	if !hasRawTable {
		t.Fatalf("dx_raw_spots CREATE TABLE missing from schema statements")
	}
	if !hasGeomDrop {
		t.Fatalf("spot_geom DROP missing from schema statements (it must stay dropped)")
	}
	if !hasSpotTimeIndex {
		t.Fatalf("spot_time index missing (the reader's only sargable path)")
	}
	if !hasCellTable || !hasSWTable {
		t.Fatalf("cellfeed schema statements not appended (proplab_cell_buckets=%v proplab_sw_series=%v)", hasCellTable, hasSWTable)
	}
}

func TestPlausibleBaselinePairGuard(t *testing.T) {
	// Corrupt baseline counts (int64 wrap from the historic double-merge bug)
	// must be excluded from sums.
	if !plausibleBaselinePair(baselinePair{Count: 1}) {
		t.Fatalf("a positive count is plausible")
	}
	if !plausibleBaselinePair(baselinePair{Count: maxPlausibleBaselineCount}) {
		t.Fatalf("the max plausible count must pass its own bound")
	}
	if plausibleBaselinePair(baselinePair{Count: 0}) || plausibleBaselinePair(baselinePair{Count: -1}) {
		t.Fatalf("non-positive counts must be excluded")
	}
	if plausibleBaselinePair(baselinePair{Count: maxPlausibleBaselineCount + 1}) {
		t.Fatalf("counts above the bound must be excluded")
	}
	if sumPairs([]baselinePair{{Count: 5}, {Count: maxPlausibleBaselineCount * 2}, {Count: 3}}) != 8 {
		t.Fatalf("sumPairs must skip implausible pairs")
	}
}
