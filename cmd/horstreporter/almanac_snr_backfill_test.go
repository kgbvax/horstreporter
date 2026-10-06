package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Almanac SNR backfill (almanac_snr_backfill.go).

// ---------------------------------------------------------------------------
// Row replay: parity with live ingest
// ---------------------------------------------------------------------------

// liveRegionDeltas runs m through the live ingest path (the PSKReporter mode
// gate of ingestPSKRMessage, DxBaselineEngine.Observe, observe) and returns
// the pending region deltas.
func liveRegionDeltas(m MQTTMessage) map[dxPulseRegionBaselineDailyKey]regionDelta {
	s := newObserveTestStore()
	e := newDxBaselineEngine("")
	e.store = s
	if pskrModeFeedsBaseline(strings.ToUpper(strings.TrimSpace(m.MD))) || m.MD == "DXCLUSTER" {
		e.Observe(m)
	}
	return s.pendingRegion
}

func TestAlmanacSNRBackfillObserveParity(t *testing.T) {
	ts := time.Now().Unix() - 60 // inside the live late-spot clamp
	row := func(band, sl, rl, mode string, snr int) almanacSNRRawRow {
		return almanacSNRRawRow{SpotTime: ts, Band: band, SenderLoc: sl, ReceiverLoc: rl, Mode: mode, SNR: snr}
	}
	cases := []struct {
		name   string
		rows   []almanacSNRRawRow
		wantOK bool
	}{
		{"FT8 tier boundaries (inclusive)", []almanacSNRRawRow{
			row("20m", "FN31", "JO32", "FT8", -21), row("20m", "FN31", "JO32", "FT8", -20),
			row("20m", "FN31", "JO32", "FT8", -16), row("20m", "FN31", "JO32", "FT8", -15),
			row("20m", "FN31", "JO32", "FT8", -10), row("20m", "FN31", "JO32", "FT8", -6),
			row("20m", "FN31", "JO32", "FT8", -5), row("20m", "FN31", "JO32", "FT8", -1),
			row("20m", "FN31", "JO32", "FT8", 0), row("20m", "FN31", "JO32", "FT8", 12),
		}, true},
		{"FT4, 6-char locators", []almanacSNRRawRow{row("40m", "FN31PR", "JO32AB", "FT4", -12)}, true},
		{"band without m suffix", []almanacSNRRawRow{row("20", "FN31", "JO32", "FT8", -3)}, true},
		{"same region both ends", []almanacSNRRawRow{row("20m", "JO31", "JO32", "FT8", -3)}, true},
		{"out-of-scope band (live keeps it)", []almanacSNRRawRow{row("23cm", "JO31", "JO32", "FT8", -3)}, true},
		{"non-FT8 mode rejected", []almanacSNRRawRow{row("20m", "FN31", "JO32", "CW", 10)}, false},
		{"empty receiver locator", []almanacSNRRawRow{row("20m", "FN31", "", "FT8", -3)}, false},
		{"empty band", []almanacSNRRawRow{row("", "FN31", "JO32", "FT8", -3)}, false},
		{"short locator: one orientation only", []almanacSNRRawRow{row("20m", "FN", "JO32", "FT8", -3)}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			agg := map[dxPulseRegionBaselineDailyKey]regionDelta{}
			live := map[dxPulseRegionBaselineDailyKey]regionDelta{}
			anyOK := false
			for _, r := range c.rows {
				if almanacSNRBackfillObserve(agg, r) {
					anyOK = true
				}
				m := MQTTMessage{T: r.SpotTime, B: r.Band, SL: r.SenderLoc, RL: r.ReceiverLoc, MD: r.Mode, RP: r.SNR}
				for k, d := range liveRegionDeltas(m) {
					e := live[k]
					e.add(d)
					live[k] = e
				}
			}
			if !reflect.DeepEqual(agg, live) {
				t.Fatalf("backfill deltas differ from live observe():\nbackfill %+v\nlive     %+v", agg, live)
			}
			if c.wantOK && (!anyOK || len(agg) == 0) {
				t.Fatalf("expected keys, got none")
			}
		})
	}

	t.Run("tier counters", func(t *testing.T) {
		agg := map[dxPulseRegionBaselineDailyKey]regionDelta{}
		for _, snr := range []int{-21, -20, -15, -10, -5, 0} {
			almanacSNRBackfillObserve(agg, row("20m", "FN31", "JO32", "FT8", snr))
		}
		want := regionDelta{Count: 6, SNR: 6, GE: [almanacSNRTiers]int64{5, 4, 3, 2, 1}}
		for k, d := range agg {
			if d != want {
				t.Fatalf("%+v: %+v, want %+v", k, d, want)
			}
		}
	})

	t.Run("DX cluster excluded", func(t *testing.T) {
		agg := map[dxPulseRegionBaselineDailyKey]regionDelta{}
		r := row("20m", "FN31", "JO32", "DXCLUSTER", 0)
		if almanacSNRBackfillObserve(agg, r) || len(agg) != 0 {
			t.Fatalf("a DX-cluster row must add no SNR counters: %+v", agg)
		}
		live := liveRegionDeltas(MQTTMessage{T: ts, B: "20m", SL: "FN31", RL: "JO32", MD: "DXCLUSTER"})
		for k, d := range live {
			if d.SNR != 0 || d.GE != ([almanacSNRTiers]int64{}) || d.Count != 1 {
				t.Fatalf("live DX-cluster spot must count toward spot_count only: %+v %+v", k, d)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Range
// ---------------------------------------------------------------------------

func TestPlanAlmanacSNRBackfill(t *testing.T) {
	const since = 20725
	const cutoff = 1790609737 // 2026-09-28 15:35:37 UTC, inside day 20724
	partialStart := int64(20724 * 86400)
	rawMin := int64(20720*86400 + 15*3600 + 37*60) // 2026-09-24 15:37

	p, err := planAlmanacSNRBackfill(rawMin, true, since, cutoff, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.FullDays, []int64{20721, 20722, 20723}) || !p.HasPartial || p.PartialDay != 20724 ||
		p.Cutoff != cutoff || p.NewSince != 20721 {
		t.Fatalf("plan = %+v", p)
	}
	if !reflect.DeepEqual(p.Days(), []int64{20721, 20722, 20723, 20724}) {
		t.Fatalf("days = %v", p.Days())
	}

	// min(spot_time) exactly at a day start covers that day.
	p, _ = planAlmanacSNRBackfill(20722*86400, true, since, cutoff, false)
	if !reflect.DeepEqual(p.FullDays, []int64{20722, 20723}) || p.NewSince != 20722 {
		t.Fatalf("day-start coverage: %+v", p)
	}

	// Only the deploy day covered: partial only.
	p, _ = planAlmanacSNRBackfill(partialStart-10, true, since, cutoff, true)
	if len(p.FullDays) != 0 || !p.HasPartial || !p.PartialDone || p.NewSince != 20724 {
		t.Fatalf("partial-only: %+v", p)
	}

	// The deploy day not fully covered: nothing contiguous, since unchanged.
	for _, c := range []struct {
		min    int64
		hasRaw bool
	}{{partialStart + 1, true}, {0, false}} {
		p, err = planAlmanacSNRBackfill(c.min, c.hasRaw, since, 0, false)
		if err != nil || len(p.Days()) != 0 || p.NewSince != since {
			t.Fatalf("uncovered deploy day must be a no-op: %+v %v", p, err)
		}
	}

	// Cutoff unknown / outside the deploy day.
	if _, err := planAlmanacSNRBackfill(rawMin, true, since, 0, false); !errors.Is(err, errAlmanacSNRCutoffUnknown) {
		t.Fatalf("unknown cutoff must fail: %v", err)
	}
	for _, bad := range []int64{partialStart, partialStart - 1, partialStart + 86400 + 1} {
		if _, err := planAlmanacSNRBackfill(rawMin, true, since, bad, false); err == nil {
			t.Fatalf("cutoff %d outside the deploy day must fail", bad)
		}
	}
	if _, err := planAlmanacSNRBackfill(rawMin, true, since, partialStart+86400, false); err != nil {
		t.Fatalf("cutoff at the end of the deploy day is valid: %v", err)
	}
}

// ---------------------------------------------------------------------------
// SET vs ADD, SQL shape
// ---------------------------------------------------------------------------

func TestAlmanacSNRBackfillMerge(t *testing.T) {
	existing := regionDelta{Count: 10, SNR: 3, GE: [almanacSNRTiers]int64{3, 2, 2, 1, 0}}
	fresh := regionDelta{Count: 99, SNR: 4, GE: [almanacSNRTiers]int64{4, 4, 3, 1, 1}}

	set := almanacSNRBackfillMerge(existing, fresh, false)
	if want := (regionDelta{Count: 10, SNR: 4, GE: [almanacSNRTiers]int64{4, 4, 3, 1, 1}}); set != want {
		t.Fatalf("SET = %+v, want %+v", set, want)
	}
	if again := almanacSNRBackfillMerge(set, fresh, false); again != set {
		t.Fatalf("SET must be idempotent: %+v", again)
	}
	add := almanacSNRBackfillMerge(existing, fresh, true)
	if want := (regionDelta{Count: 10, SNR: 7, GE: [almanacSNRTiers]int64{7, 6, 5, 2, 1}}); add != want {
		t.Fatalf("ADD = %+v, want %+v", add, want)
	}
	capped := almanacSNRBackfillMerge(regionDelta{Count: 5}, fresh, false)
	if want := (regionDelta{Count: 5, SNR: 4, GE: [almanacSNRTiers]int64{4, 4, 3, 1, 1}}); capped != want {
		t.Fatalf("cap = %+v", capped)
	}
	capped = almanacSNRBackfillMerge(regionDelta{Count: 2}, fresh, false)
	if capped.SNR != 2 || capped.GE[0] != 2 || capped.GE[4] != 1 {
		t.Fatalf("counters must be capped at spot_count: %+v", capped)
	}
}

func TestAlmanacSNRBackfillSQLShape(t *testing.T) {
	for _, c := range regionSNRColumnNames {
		if !strings.Contains(almanacSNRBackfillSetSQL, c+" = LEAST(t."+c+", d.spot_count)") {
			t.Fatalf("SET must assign %s from the temp table: %s", c, almanacSNRBackfillSetSQL)
		}
		if !strings.Contains(almanacSNRBackfillAddSQL, c+" = LEAST(d."+c+" + t."+c+", d.spot_count)") {
			t.Fatalf("ADD must add %s: %s", c, almanacSNRBackfillAddSQL)
		}
	}
	for _, sql := range []string{almanacSNRBackfillSetSQL, almanacSNRBackfillAddSQL} {
		if strings.Contains(sql, "spot_count =") {
			t.Fatalf("the backfill must never touch spot_count: %s", sql)
		}
		if !strings.Contains(sql, "d.day_index = $1::bigint") {
			t.Fatalf("the UPDATE must be bounded to the day (day_index index): %s", sql)
		}
	}
	if !strings.Contains(almanacSNRBackfillStreamSQL, "source_type = $1") {
		t.Fatalf("only PSKReporter rows: %s", almanacSNRBackfillStreamSQL)
	}
	if almanacSNRBackfillSourceType != "mqtt" {
		t.Fatalf("PSKReporter raw rows are source_type 'mqtt'")
	}
	if !reflect.DeepEqual(almanacSNRBackfillPlanStmts, []string{`ANALYZE almanac_snr_backfill_keys`,
		`SET LOCAL enable_hashjoin = off`, `SET LOCAL enable_mergejoin = off`}) {
		t.Fatalf("the batch UPDATE must be pinned to PK probes: %v", almanacSNRBackfillPlanStmts)
	}
	if !reflect.DeepEqual(almanacSNRBatchTimeoutStmts(false), []string{`SET LOCAL statement_timeout = '30s'`, `SET LOCAL lock_timeout = '5s'`}) ||
		!reflect.DeepEqual(almanacSNRBatchTimeoutStmts(true), []string{`SET LOCAL statement_timeout = '10s'`, `SET LOCAL lock_timeout = '2s'`}) {
		t.Fatalf("batch timeouts: %v / %v", almanacSNRBatchTimeoutStmts(false), almanacSNRBatchTimeoutStmts(true))
	}
	if almanacSNRBackfillSetBatch != 20000 || almanacSNRBackfillAddBatch != 2000 {
		t.Fatalf("batch sizes drifted")
	}
	if !strings.Contains(almanacSNRBackfillProgressSQL, "WHERE dx_meta.v::bigint = $3::bigint") {
		t.Fatalf("ADD progress must only advance from the previous batch: %s", almanacSNRBackfillProgressSQL)
	}
	if !strings.Contains(almanacSNRBackfillSinceSQL, "LEAST(") {
		t.Fatalf("the since-day may only move down: %s", almanacSNRBackfillSinceSQL)
	}
	if !strings.Contains(almanacSNRBackfillPartialGuardSQL, "DO NOTHING") {
		t.Fatalf("the partial-day guard must be insert-once: %s", almanacSNRBackfillPartialGuardSQL)
	}
}

func TestAlmanacSNRBackfillSQLParamsCast(t *testing.T) {
	for name, sql := range map[string]string{
		"meta":      almanacSNRBackfillMetaSQL,
		"rawmin":    almanacSNRBackfillRawMinSQL,
		"stream":    almanacSNRBackfillStreamSQL,
		"guard":     almanacSNRBackfillPartialGuardSQL,
		"progress":  almanacSNRBackfillProgressSQL,
		"advisory":  almanacSNRBackfillWatermarkAdvisorySQL,
		"since":     almanacSNRBackfillSinceSQL,
		"done":      almanacSNRBackfillDoneSQL,
		"updateSet": almanacSNRBackfillSetSQL,
		"updateAdd": almanacSNRBackfillAddSQL,
	} {
		for i := 1; i <= 4; i++ {
			p := fmt.Sprintf("$%d", i)
			rest := sql
			for {
				idx := strings.Index(rest, p)
				if idx < 0 {
					break
				}
				if !strings.HasPrefix(rest[idx+len(p):], "::") {
					t.Errorf("%s: %s must carry an explicit ::type cast: %q", name, p, sql)
				}
				rest = rest[idx+len(p):]
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Re-fold (fake fold store)
// ---------------------------------------------------------------------------

// refoldDay mirrors pgAlmanacFoldStore.refoldDay: foldDay's transaction
// without the watermark check and without moving the watermark.
func (f *fakeAlmanacFoldStore) refoldDay(_ context.Context, day int64, withSNR bool) error {
	if !f.state.hasWatermark {
		return errors.New("almanac watermark missing")
	}
	tx := f.state.clone()
	fold := newAlmanacDayFold(day)
	for _, r := range f.daily[day] {
		if !withSNR {
			r.SNR, r.GE = 0, [almanacSNRTiers]int64{}
		}
		fold.add(r)
	}
	for k, seg := range fold.Segments {
		sk := fakeSeasonKey{k.Grid4, k.Band, k.Region, fold.YearMonth}
		row, _ := almanacSparseReplaceDay(tx.counts[sk], fold.DayOfMonth, seg, fold.Hist[k])
		tx.counts[sk] = row
	}
	if day == f.failDay {
		return errors.New("injected mid-transaction failure")
	}
	for gb := range foldActive(fold) {
		tx.masks[fakeActivityKey{gb.Grid, gb.Band, fold.YearMonth}] |= fold.dayMaskBit()
	}
	for slot, total := range fold.SlotTotals {
		ik := fakeIngestKey{day, slot}
		if total > 0 && total > tx.ingest[ik] {
			tx.ingest[ik] = total
		}
	}
	f.state = tx
	return nil
}

func TestAlmanacRefoldDayIdempotent(t *testing.T) {
	ctx := context.Background()
	day := almanacTestToday() - 3
	snrRow := func(slot int) almanacDailyRow {
		r := dailyRow("JO32", "20m", "NA", slot, 9)
		r.SNR, r.GE = 6, [almanacSNRTiers]int64{6, 5, 3, 1, 0}
		return r
	}
	rows := []almanacDailyRow{snrRow(30), snrRow(31), dailyRow("JO32", "40m", "EU", 2, 3)}

	// Folded before the backfill: the day predated the SNR start.
	st := newFakeAlmanacFoldStore()
	st.daily[day] = rows
	st.state.watermark, st.state.hasWatermark = day-1, true
	st.snrSince, st.hasSNRSince = day+1, true
	if _, err := st.foldDay(ctx, day); err != nil {
		t.Fatal(err)
	}
	before := st.state.clone()
	key := fakeSeasonKey{"JO32", "20m", "NA", 202609}
	if hist, err := snrHistCells(before.counts[key]); err != nil || len(hist) != 0 {
		t.Fatalf("pre-backfill fold must carry no SNR histogram: %v %v", hist, err)
	}

	// Re-fold with SNR.
	if err := st.refoldDay(ctx, day, true); err != nil {
		t.Fatal(err)
	}
	after := st.state.clone()
	if after.watermark != day {
		t.Fatalf("refold must not move the watermark: %d", after.watermark)
	}
	if hist, err := snrHistCells(after.counts[key]); err != nil || len(hist) != 2 {
		t.Fatalf("refold must add the SNR histograms (slots 30, 31): %v %v", hist, err)
	}
	if !reflect.DeepEqual(after.masks, before.masks) || !reflect.DeepEqual(after.ingest, before.ingest) {
		t.Fatalf("activity (OR) and ingest seed (GREATEST) must be unchanged by a refold")
	}

	// Byte-identical to folding with SNR from the start.
	ref := newFakeAlmanacFoldStore()
	ref.daily[day] = rows
	ref.state.watermark, ref.state.hasWatermark = day-1, true
	ref.snrSince, ref.hasSNRSince = day, true
	if _, err := ref.foldDay(ctx, day); err != nil {
		t.Fatal(err)
	}
	for k, v := range ref.state.counts {
		if !bytes.Equal(after.counts[k], v) {
			t.Fatalf("%+v: refold differs from a fold with SNR", k)
		}
	}

	// Idempotent.
	if err := st.refoldDay(ctx, day, true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st.state, after) {
		t.Fatalf("a second refold must change nothing")
	}

	// A failed refold leaves the state untouched.
	st.failDay = day
	st.daily[day] = []almanacDailyRow{dailyRow("JO32", "20m", "NA", 30, 200)}
	if err := st.refoldDay(ctx, day, true); err == nil || !reflect.DeepEqual(st.state, after) {
		t.Fatalf("failed refold must roll back: %v", err)
	}
}

// snrHistCells decodes a sparse row and returns its non-empty SNR
// histograms by cell position.
func snrHistCells(row []byte) (map[int]almanacSNRHist, error) {
	hist := map[int]almanacSNRHist{}
	err := almanacSparseEachCell(row, func(pos, _ int, h *almanacSNRHist) {
		if h != nil && *h != (almanacSNRHist{}) {
			hist[pos] = *h
		}
	})
	return hist, err
}

// ---------------------------------------------------------------------------
// Runner (fake backfill store)
// ---------------------------------------------------------------------------

type fakeSNRRaw struct {
	source string
	row    almanacSNRRawRow
}

type fakeSNRBatchCall struct {
	day   int64
	index int
	add   bool
	n     int
}

type fakeSNRBackfillStore struct {
	meta      map[string]string
	raw       []fakeSNRRaw
	daily     map[dxPulseRegionBaselineDailyKey]regionDelta
	watermark int64
	hasW      bool

	failWriteDay   int64
	failBatch      func(day int64, spec almanacSNRBatchSpec) error
	batches        []fakeSNRBatchCall
	failRefold     bool
	failFinalize   bool
	refolds        []int64
	writes         []int64
	beforeFinalize func(f *fakeSNRBackfillStore) // e.g. the fold ticker advancing
}

func (f *fakeSNRBackfillStore) loadMeta(context.Context) (almanacSNRBackfillMeta, error) {
	var m almanacSNRBackfillMeta
	_, m.Done = f.meta[almanacSNRBackfillDoneKey]
	_, m.PartialDone = f.meta[almanacSNRBackfillPartialDoneKey]
	m.PartialProgress = -1
	if v, ok := f.meta[almanacSNRBackfillPartialProgressKey]; ok {
		m.PartialProgress, _ = strconv.Atoi(v)
	}
	if v, ok := f.meta[almanacSNRSinceKey]; ok {
		m.Since, _ = strconv.ParseInt(v, 10, 64)
		m.HasSince = true
	}
	if v, ok := f.meta[almanacSNRColumnsAddedKey]; ok {
		m.AddedUnix, _ = strconv.ParseInt(v, 10, 64)
	}
	return m, nil
}

func (f *fakeSNRBackfillStore) rawCoverageStart(context.Context) (int64, bool, error) {
	var min int64
	ok := false
	for _, r := range f.raw {
		if r.source == almanacSNRBackfillSourceType && (!ok || r.row.SpotTime < min) {
			min, ok = r.row.SpotTime, true
		}
	}
	return min, ok, nil
}

func (f *fakeSNRBackfillStore) aggregate(_ context.Context, from, to int64) (map[dxPulseRegionBaselineDailyKey]regionDelta, almanacSNRDayScan, error) {
	agg := map[dxPulseRegionBaselineDailyKey]regionDelta{}
	var scan almanacSNRDayScan
	for _, r := range f.raw {
		if r.source != almanacSNRBackfillSourceType || r.row.SpotTime < from || r.row.SpotTime >= to {
			continue
		}
		scan.Rows++
		if almanacSNRBackfillObserve(agg, r.row) {
			scan.Accepted++
		}
	}
	return agg, scan, nil
}

func (f *fakeSNRBackfillStore) writeBatch(_ context.Context, day int64, batch []almanacSNRKeyDelta, spec almanacSNRBatchSpec) (int64, bool, error) {
	f.batches = append(f.batches, fakeSNRBatchCall{day, spec.Index, spec.Add, len(batch)})
	if f.failBatch != nil {
		if err := f.failBatch(day, spec); err != nil {
			return 0, false, err // rolled back: nothing written
		}
	}
	if day == f.failWriteDay {
		return 0, false, errors.New("injected write failure")
	}
	if spec.Add {
		// almanacSNRBackfillProgressSQL: insert when absent, else advance
		// only from Index−1.
		if v, ok := f.meta[almanacSNRBackfillPartialProgressKey]; ok {
			if cur, _ := strconv.Atoi(v); cur != spec.Index-1 {
				return 0, true, nil
			}
		}
		f.meta[almanacSNRBackfillPartialProgressKey] = strconv.Itoa(spec.Index)
	}
	if n := len(f.writes); n == 0 || f.writes[n-1] != day {
		f.writes = append(f.writes, day)
	}
	var updated int64
	for _, e := range batch {
		d, ok := f.daily[e.Key]
		if !ok || e.Key.DayIndex != day {
			continue
		}
		f.daily[e.Key] = almanacSNRBackfillMerge(d, e.Delta, spec.Add)
		updated++
	}
	if spec.Add && spec.Last {
		if _, ok := f.meta[almanacSNRBackfillPartialDoneKey]; !ok {
			f.meta[almanacSNRBackfillPartialDoneKey] = strconv.FormatInt(spec.Cutoff, 10)
		}
	}
	return updated, false, nil
}

func (f *fakeSNRBackfillStore) foldWatermark(context.Context) (int64, bool, error) {
	return f.watermark, f.hasW, nil
}

func (f *fakeSNRBackfillStore) refoldDay(_ context.Context, day int64, withSNR bool) error {
	if f.failRefold {
		return errors.New("injected refold failure")
	}
	if !withSNR {
		return errors.New("the backfill must re-fold with SNR")
	}
	f.refolds = append(f.refolds, day)
	return nil
}

func (f *fakeSNRBackfillStore) finalize(_ context.Context, newSince int64, days []int64, refolded map[int64]bool) ([]int64, error) {
	if f.beforeFinalize != nil {
		f.beforeFinalize(f)
	}
	if f.failFinalize {
		return nil, errors.New("injected finalize failure")
	}
	if pending := almanacSNRBackfillPending(days, refolded, f.watermark, f.hasW); len(pending) > 0 {
		return pending, nil
	}
	cur, _ := strconv.ParseInt(f.meta[almanacSNRSinceKey], 10, 64)
	f.meta[almanacSNRSinceKey] = strconv.FormatInt(min(cur, newSince), 10)
	f.meta[almanacSNRBackfillDoneKey] = "1"
	return nil, nil
}

const (
	snrTestSince  = 20725
	snrTestCutoff = 1790609737 // 2026-09-28 15:35:37 UTC
)

// newSNRBackfillFixture: raw PSKReporter rows from 2026-09-24 15:37 (days
// 20721–20723 full, 20724 the deploy day), one daily key per day, fold
// watermark 20722.
func newSNRBackfillFixture() *fakeSNRBackfillStore {
	f := &fakeSNRBackfillStore{
		meta:      map[string]string{almanacSNRSinceKey: strconv.Itoa(snrTestSince)},
		daily:     map[dxPulseRegionBaselineDailyKey]regionDelta{},
		watermark: 20722,
		hasW:      true,
	}
	add := func(source string, ts int64, snr int) {
		f.raw = append(f.raw, fakeSNRRaw{source, almanacSNRRawRow{SpotTime: ts, Band: "20m", SenderLoc: "FN31", ReceiverLoc: "JO32", Mode: "FT8", SNR: snr}})
	}
	add("mqtt", 20720*86400+15*3600+37*60, -3)
	for day := int64(20721); day <= 20724; day++ {
		t0 := day*86400 + 12*3600
		add("mqtt", t0, -3)
		add("mqtt", t0+60, -18)
		add("dxcluster", t0+120, 0) // excluded (source)
		add("rbn", t0+180, 20)      // excluded (source)
		for _, k := range dxPulseRegionBaselineKeysForSpot(t0, "20m", "FN31", "JO32") {
			f.daily[k] = regionDelta{Count: 10}
		}
	}
	// Deploy day: live already counted one SNR spot; a raw row after the
	// cutoff is live-counted and must not be added again.
	for _, k := range dxPulseRegionBaselineKeysForSpot(20724*86400+12*3600, "20m", "FN31", "JO32") {
		f.daily[k] = regionDelta{Count: 10, SNR: 1, GE: [almanacSNRTiers]int64{1, 1, 1, 1, 0}}
	}
	add("mqtt", snrTestCutoff+10, -1)
	return f
}

func newTestSNRRunner(st almanacSNRBackfillStore) *almanacSNRBackfillRunner {
	return &almanacSNRBackfillRunner{
		store:      st,
		cutoffFlag: snrTestCutoff,
		status:     &almanacSNRBackfillStatus{enabled: true},
		pause:      func(context.Context, time.Duration) error { return nil },
		setBatch:   almanacSNRBackfillSetBatch,
		addBatch:   almanacSNRBackfillAddBatch,
	}
}

func snrDailyForDay(f *fakeSNRBackfillStore, day int64) []regionDelta {
	var out []regionDelta
	for k, d := range f.daily {
		if k.DayIndex == day {
			out = append(out, d)
		}
	}
	return out
}

func TestAlmanacSNRBackfillRun(t *testing.T) {
	ctx := context.Background()
	f := newSNRBackfillFixture()
	r := newTestSNRRunner(f)
	if err := r.run(ctx); err != nil {
		t.Fatal(err)
	}
	if f.meta[almanacSNRSinceKey] != "20721" || f.meta[almanacSNRBackfillDoneKey] == "" {
		t.Fatalf("since-day/done after success: %v", f.meta)
	}
	if !reflect.DeepEqual(f.refolds, []int64{20721, 20722}) {
		t.Fatalf("only backfilled days ≤ watermark are re-folded: %v", f.refolds)
	}
	if !reflect.DeepEqual(f.writes, []int64{20721, 20722, 20723, 20724}) {
		t.Fatalf("days written oldest first: %v", f.writes)
	}
	full := regionDelta{Count: 10, SNR: 2, GE: [almanacSNRTiers]int64{2, 1, 1, 1, 0}}
	for day := int64(20721); day <= 20723; day++ {
		for _, d := range snrDailyForDay(f, day) {
			if d != full {
				t.Fatalf("day %d: %+v, want %+v (DX-cluster/RBN rows excluded)", day, d, full)
			}
		}
	}
	partial := regionDelta{Count: 10, SNR: 3, GE: [almanacSNRTiers]int64{3, 2, 2, 2, 0}}
	for _, d := range snrDailyForDay(f, 20724) {
		if d != partial {
			t.Fatalf("deploy day: %+v, want live + pre-cutoff raw %+v", d, partial)
		}
	}
	st := r.status.stats()
	if !st.Done || st.DaysBackfilled != 4 || st.SinceDay != 20721 || st.LastError != "" {
		t.Fatalf("status = %+v", st)
	}

	// Done: a second run changes nothing.
	f.writes, f.refolds = nil, nil
	if err := newTestSNRRunner(f).run(ctx); err != nil || len(f.writes) != 0 || len(f.refolds) != 0 {
		t.Fatalf("done key must stop a re-run: %v %v %v", err, f.writes, f.refolds)
	}
}

func TestAlmanacSNRBackfillFailureLeavesStateUntouched(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name   string
		inject func(f *fakeSNRBackfillStore)
	}{
		{"write fails", func(f *fakeSNRBackfillStore) { f.failWriteDay = 20722 }},
		{"refold fails", func(f *fakeSNRBackfillStore) { f.failRefold = true }},
		{"finalize fails", func(f *fakeSNRBackfillStore) { f.failFinalize = true }},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newSNRBackfillFixture()
			c.inject(f)
			r := newTestSNRRunner(f)
			if err := r.run(ctx); err == nil {
				t.Fatalf("expected failure")
			}
			if f.meta[almanacSNRSinceKey] != strconv.Itoa(snrTestSince) {
				t.Fatalf("since-day must not move on failure: %v", f.meta)
			}
			if _, ok := f.meta[almanacSNRBackfillDoneKey]; ok {
				t.Fatalf("done key must stay unset on failure")
			}
			if st := r.status.stats(); st.Done || st.LastError == "" || st.Running {
				t.Fatalf("status after failure: %+v", st)
			}

			// Retry succeeds; the deploy day is added exactly once.
			f.failWriteDay, f.failRefold, f.failFinalize = 0, false, false
			if err := newTestSNRRunner(f).run(ctx); err != nil {
				t.Fatal(err)
			}
			if f.meta[almanacSNRSinceKey] != "20721" {
				t.Fatalf("retry must finish: %v", f.meta)
			}
			for _, d := range snrDailyForDay(f, 20724) {
				if d.SNR != 3 {
					t.Fatalf("deploy day added twice across runs: %+v", d)
				}
			}
			for _, d := range snrDailyForDay(f, 20721) {
				if d.SNR != 2 {
					t.Fatalf("full days must be SET (idempotent) across runs: %+v", d)
				}
			}
		})
	}
}

func TestAlmanacSNRBackfillFoldAdvancesMeanwhile(t *testing.T) {
	f := newSNRBackfillFixture()
	advanced := false
	f.beforeFinalize = func(f *fakeSNRBackfillStore) {
		if !advanced { // the ticker folds 20723 (without SNR) mid-run
			f.watermark, advanced = 20723, true
		}
	}
	if err := newTestSNRRunner(f).run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.refolds, []int64{20721, 20722, 20723}) || f.meta[almanacSNRSinceKey] != "20721" {
		t.Fatalf("a day folded meanwhile must be re-folded before the since-day moves: %v %v", f.refolds, f.meta)
	}
}

func TestAlmanacSNRBackfillCutoffSources(t *testing.T) {
	ctx := context.Background()

	// No flag, no dx_meta record: error, nothing written.
	f := newSNRBackfillFixture()
	r := newTestSNRRunner(f)
	r.cutoffFlag = 0
	if err := r.run(ctx); !errors.Is(err, errAlmanacSNRCutoffUnknown) || len(f.writes) != 0 {
		t.Fatalf("unknown cutoff: %v %v", err, f.writes)
	}

	// dx_meta record used when the flag is 0.
	f = newSNRBackfillFixture()
	f.meta[almanacSNRColumnsAddedKey] = strconv.Itoa(snrTestCutoff)
	r = newTestSNRRunner(f)
	r.cutoffFlag = 0
	if err := r.run(ctx); err != nil || f.meta[almanacSNRBackfillPartialDoneKey] != strconv.Itoa(snrTestCutoff) {
		t.Fatalf("dx_meta cutoff: %v %v", err, f.meta)
	}

	// Nothing covered (raw starts after the deploy day's start): done, since
	// unchanged.
	f = newSNRBackfillFixture()
	f.raw = f.raw[len(f.raw)-1:] // only the post-cutoff row
	if err := newTestSNRRunner(f).run(ctx); err != nil {
		t.Fatal(err)
	}
	if f.meta[almanacSNRSinceKey] != strconv.Itoa(snrTestSince) || f.meta[almanacSNRBackfillDoneKey] == "" || len(f.writes) != 0 {
		t.Fatalf("uncovered: %v %v", f.meta, f.writes)
	}

	// Since-day not recorded: error.
	f = newSNRBackfillFixture()
	delete(f.meta, almanacSNRSinceKey)
	if err := newTestSNRRunner(f).run(ctx); err == nil {
		t.Fatalf("missing since-day must fail")
	}
}

// ---------------------------------------------------------------------------
// Batching
// ---------------------------------------------------------------------------

func TestAlmanacSNRBackfillBatches(t *testing.T) {
	for _, c := range []struct {
		n, size int
		want    [][2]int
	}{
		{0, 3, nil},
		{1, 3, [][2]int{{0, 1}}},
		{3, 3, [][2]int{{0, 3}}},
		{4, 3, [][2]int{{0, 3}, {3, 4}}},
		{7, 2, [][2]int{{0, 2}, {2, 4}, {4, 6}, {6, 7}}},
	} {
		if got := almanacSNRBackfillBatches(c.n, c.size); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("batches(%d, %d) = %v, want %v", c.n, c.size, got, c.want)
		}
	}
}

func TestAlmanacSNRBackfillEntries(t *testing.T) {
	k := func(grid string, slot int, day int64) dxPulseRegionBaselineDailyKey {
		return dxPulseRegionBaselineDailyKey{TargetGrid4: grid, Band: "20m", SlotOfDay: slot, Region: "NA", DayIndex: day}
	}
	agg := map[dxPulseRegionBaselineDailyKey]regionDelta{
		k("JO32", 5, 100): {Count: 3, SNR: 2},
		k("FN31", 7, 100): {Count: 1, SNR: 1},
		k("FN31", 2, 100): {Count: 4, SNR: 4},
		k("JO33", 1, 100): {Count: 2}, // no SNR: skipped
		k("AA00", 1, 99):  {Count: 1, SNR: 1},
	}
	got := almanacSNRBackfillEntries(agg, 100)
	var keys []dxPulseRegionBaselineDailyKey
	for _, e := range got {
		keys = append(keys, e.Key)
	}
	want := []dxPulseRegionBaselineDailyKey{k("FN31", 2, 100), k("FN31", 7, 100), k("JO32", 5, 100)}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("entries = %v, want %v (sorted, SNR > 0, this day only)", keys, want)
	}
}

// addDeployDayKeys adds n more deploy-day keys (pre-cutoff raw rows with
// distinct sender grids, each also present in the daily table).
func addDeployDayKeys(f *fakeSNRBackfillStore, n int) {
	t0 := int64(20724*86400 + 12*3600)
	for i := 0; i < n; i++ {
		sl := fmt.Sprintf("EM%02d", 10+i)
		f.raw = append(f.raw, fakeSNRRaw{"mqtt", almanacSNRRawRow{SpotTime: t0 + int64(i), Band: "20m", SenderLoc: sl, ReceiverLoc: "JO32", Mode: "FT8", SNR: -7}})
		for _, key := range dxPulseRegionBaselineKeysForSpot(t0+int64(i), "20m", sl, "JO32") {
			f.daily[key] = regionDelta{Count: 10, SNR: 1, GE: [almanacSNRTiers]int64{1, 1, 1, 1, 0}}
		}
	}
}

func deployDaySNR(f *fakeSNRBackfillStore) map[dxPulseRegionBaselineDailyKey]regionDelta {
	out := map[dxPulseRegionBaselineDailyKey]regionDelta{}
	for k, d := range f.daily {
		if k.DayIndex == 20724 {
			out[k] = d
		}
	}
	return out
}

func TestAlmanacSNRBackfillSetBatched(t *testing.T) {
	f := newSNRBackfillFixture()
	r := newTestSNRRunner(f)
	r.setBatch = 1 // 2 keys per full day → 2 batches
	if err := r.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range f.batches {
		if !c.add {
			n++
			if c.n != 1 {
				t.Fatalf("SET batch size %d", c.n)
			}
		}
	}
	if n != 6 {
		t.Fatalf("SET batches = %d, want 2 per full day × 3", n)
	}
}

func TestAlmanacSNRBackfillAddResumeNeverDoubleAdds(t *testing.T) {
	ctx := context.Background()

	// Reference: one uninterrupted run.
	ref := newSNRBackfillFixture()
	addDeployDayKeys(ref, 9)
	rr := newTestSNRRunner(ref)
	rr.addBatch = 2
	if err := rr.run(ctx); err != nil {
		t.Fatal(err)
	}
	want := deployDaySNR(ref)

	f := newSNRBackfillFixture()
	addDeployDayKeys(f, 9) // FN31/EU, JO32/NA + 9 EMxx/EU = 11 keys → 6 ADD batches of 2
	failed := false
	f.failBatch = func(day int64, spec almanacSNRBatchSpec) error {
		if spec.Add && spec.Index == 3 && !failed {
			failed = true
			return errors.New("injected batch failure")
		}
		return nil
	}
	r := newTestSNRRunner(f)
	r.addBatch = 2
	if err := r.run(ctx); err == nil {
		t.Fatalf("expected failure")
	}
	if f.meta[almanacSNRBackfillPartialProgressKey] != "2" {
		t.Fatalf("progress after batches 0–2 committed: %v", f.meta)
	}
	if _, ok := f.meta[almanacSNRBackfillPartialDoneKey]; ok {
		t.Fatalf("partial-done must wait for the last batch")
	}
	if f.meta[almanacSNRSinceKey] != strconv.Itoa(snrTestSince) {
		t.Fatalf("since-day moved on failure")
	}

	// A replay of an already committed batch is refused by the guard.
	if _, skipped, err := f.writeBatch(ctx, 20724, nil, almanacSNRBatchSpec{Add: true, Index: 1}); err != nil || !skipped {
		t.Fatalf("committed batch must be skipped: %v %v", skipped, err)
	}

	f.batches = nil
	r = newTestSNRRunner(f)
	r.addBatch = 2
	if err := r.run(ctx); err != nil {
		t.Fatal(err)
	}
	var addIdx []int
	for _, c := range f.batches {
		if c.add {
			addIdx = append(addIdx, c.index)
		}
	}
	if !reflect.DeepEqual(addIdx, []int{3, 4, 5}) {
		t.Fatalf("retry must resume after the last committed batch: %v", addIdx)
	}
	if got := deployDaySNR(f); !reflect.DeepEqual(got, want) {
		t.Fatalf("resumed ADD differs from an uninterrupted run (double add?):\ngot  %v\nwant %v", got, want)
	}
	if f.meta[almanacSNRBackfillPartialDoneKey] == "" || f.meta[almanacSNRSinceKey] != "20721" {
		t.Fatalf("meta after resume: %v", f.meta)
	}
}

func TestAlmanacSNRBackfillLockTimeoutRetry(t *testing.T) {
	f := newSNRBackfillFixture()
	fails := 0
	f.failBatch = func(day int64, spec almanacSNRBatchSpec) error {
		if spec.Add && fails < 2 {
			fails++
			return &pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout"}
		}
		return nil
	}
	r := newTestSNRRunner(f)
	var pauses []time.Duration
	r.pause = func(_ context.Context, d time.Duration) error {
		pauses = append(pauses, d)
		return nil
	}
	if err := r.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slicesContainsSeq(pauses, []time.Duration{time.Second, 2 * time.Second}) {
		t.Fatalf("lock timeouts must back off 1 s, 2 s: %v", pauses)
	}
	for _, d := range deployDaySNR(f) {
		if d.SNR != 3 {
			t.Fatalf("retried batch applied once: %+v", d)
		}
	}

	// Non-lock errors are not retried.
	if almanacSNRRetryable(errors.New("boom")) || !almanacSNRRetryable(fmt.Errorf("w: %w", &pgconn.PgError{Code: "55P03"})) {
		t.Fatalf("retryable classification")
	}
}

func slicesContainsSeq(s, seq []time.Duration) bool {
	for i := 0; i+len(seq) <= len(s); i++ {
		if reflect.DeepEqual(s[i:i+len(seq)], seq) {
			return true
		}
	}
	return false
}
