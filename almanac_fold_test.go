package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// In-memory model of the seasonal record. fakeAlmanacFoldStore mirrors
// foldSeasonRows in almanac_season_store.go: rows are stored sparse-encoded
// and foldDay does the same read-modify-write per key (decode the existing
// row — absent or malformed = empty — replace the day's 48-slot segment, SET
// semantics, and re-encode via almanacSparseReplaceDay),
// ORs the activity bit, raises ingest-slot totals with GREATEST, and advances
// the watermark — all on a copy that is only committed when no step fails,
// the way the single fold transaction behaves.
// ---------------------------------------------------------------------------

type fakeSeasonKey struct {
	Grid4, Band, Region string
	YearMonth           int
}

type fakeActivityKey struct {
	Grid4, Band string
	YearMonth   int
}

type fakeIngestKey struct {
	Day  int64
	Slot int
}

type fakeAlmanacState struct {
	watermark    int64
	hasWatermark bool
	counts       map[fakeSeasonKey][]byte
	masks        map[fakeActivityKey]uint32
	ingest       map[fakeIngestKey]int64
	lost         map[int64]string
}

func (s fakeAlmanacState) clone() fakeAlmanacState {
	c := fakeAlmanacState{
		watermark:    s.watermark,
		hasWatermark: s.hasWatermark,
		counts:       make(map[fakeSeasonKey][]byte, len(s.counts)),
		masks:        make(map[fakeActivityKey]uint32, len(s.masks)),
		ingest:       make(map[fakeIngestKey]int64, len(s.ingest)),
		lost:         make(map[int64]string, len(s.lost)),
	}
	for k, v := range s.counts {
		c.counts[k] = append([]byte(nil), v...)
	}
	for k, v := range s.masks {
		c.masks[k] = v
	}
	for k, v := range s.ingest {
		c.ingest[k] = v
	}
	for k, v := range s.lost {
		c.lost[k] = v
	}
	return c
}

type fakeAlmanacFoldStore struct {
	daily   map[int64][]almanacDailyRow
	state   fakeAlmanacState
	failDay int64 // foldDay(failDay) fails after writing the segments
	calls   []int64
}

func newFakeAlmanacFoldStore() *fakeAlmanacFoldStore {
	return &fakeAlmanacFoldStore{
		daily: map[int64][]almanacDailyRow{},
		state: fakeAlmanacState{}.clone(),
	}
}

func (f *fakeAlmanacFoldStore) minDailyDay() (int64, bool) {
	var min int64
	ok := false
	for d, rows := range f.daily {
		if len(rows) == 0 {
			continue
		}
		if !ok || d < min {
			min, ok = d, true
		}
	}
	return min, ok
}

func (f *fakeAlmanacFoldStore) ensureWatermark(_ context.Context, today int64) (int64, error) {
	if f.state.hasWatermark {
		return f.state.watermark, nil
	}
	min, ok := f.minDailyDay()
	w, lost, hasLost := almanacInitialWatermark(min, ok, today)
	f.state.watermark, f.state.hasWatermark = w, true
	if hasLost {
		f.state.lost[lost] = almanacLostReasonInitial
	}
	return w, nil
}

func (f *fakeAlmanacFoldStore) foldDay(_ context.Context, day int64) (int64, error) {
	f.calls = append(f.calls, day)
	if f.state.hasWatermark && f.state.watermark >= day {
		return f.state.watermark, nil
	}
	tx := f.state.clone()
	fold := newAlmanacDayFold(day)
	for _, r := range f.daily[day] {
		fold.add(r)
	}
	for k, seg := range fold.Segments {
		sk := fakeSeasonKey{k.Grid4, k.Band, k.Region, fold.YearMonth}
		row, _ := almanacSparseReplaceDay(tx.counts[sk], fold.DayOfMonth, seg)
		tx.counts[sk] = row
	}
	if day == f.failDay {
		return 0, errors.New("injected mid-transaction failure")
	}
	for gb := range foldActive(fold) {
		ak := fakeActivityKey{gb.Grid, gb.Band, fold.YearMonth}
		tx.masks[ak] |= fold.dayMaskBit()
	}
	for slot, total := range fold.SlotTotals {
		if total <= 0 {
			continue
		}
		ik := fakeIngestKey{day, slot}
		if total > tx.ingest[ik] {
			tx.ingest[ik] = total
		}
	}
	if !tx.hasWatermark || day > tx.watermark {
		tx.watermark, tx.hasWatermark = day, true
	}
	f.state = tx
	return day, nil
}

func (f *fakeAlmanacFoldStore) forceAdvance(_ context.Context, newWatermark int64, reason string) (int64, error) {
	cur := f.state.watermark
	days := almanacLostDayRange(cur, newWatermark)
	for _, d := range days {
		f.state.lost[d] = reason
	}
	if newWatermark > cur {
		f.state.watermark = newWatermark
	}
	return int64(len(days)), nil
}

func (f *fakeAlmanacFoldStore) lostDayCount(_ context.Context) (int64, error) {
	return int64(len(f.state.lost)), nil
}

type fakeAlmanacFlushState struct {
	lastOK     int64
	pendingMin int64
	hasPending bool
}

func (f *fakeAlmanacFlushState) baselineFlushLastOK() int64 { return f.lastOK }
func (f *fakeAlmanacFlushState) pendingRegionMinDay() (int64, bool) {
	return f.pendingMin, f.hasPending
}

// almanacTestNow is 2026-09-28 12:00 UTC.
var almanacTestNow = time.Date(2026, time.September, 28, 12, 0, 0, 0, time.UTC)

func almanacTestToday() int64 { return utcDayIndex(almanacTestNow.Unix()) }

func newTestAlmanacFolder(st almanacFoldStore, fl almanacFlushState) *almanacFolder {
	f := newAlmanacFolder(st, fl, "")
	f.now = func() time.Time { return almanacTestNow }
	f.diskProbe = func(string) (float64, error) { return 0.10, nil }
	return f
}

func dailyRow(grid, band, region string, slot int, n int64) almanacDailyRow {
	return almanacDailyRow{Grid4: grid, Band: band, Region: region, Slot: slot, Count: n}
}

// ---------------------------------------------------------------------------
// Finality rule
// ---------------------------------------------------------------------------

func TestAlmanacDayFinal(t *testing.T) {
	now := almanacTestNow.Unix()
	today := almanacTestToday()
	d := today - 2
	flushAfter := (d+1)*86400 + 3600 + 1

	if almanacDayFinal(today-1, now, now, 0, false) {
		t.Fatalf("today-1 must never be final")
	}
	if almanacDayFinal(today, now, now, 0, false) {
		t.Fatalf("today must never be final")
	}
	if !almanacDayFinal(d, now, flushAfter, 0, false) {
		t.Fatalf("today-2 with a caught-up flush and no pending deltas must be final")
	}
	if almanacDayFinal(d, now, (d+1)*86400+3600, 0, false) {
		t.Fatalf("flush exactly at end(d)+1h is not after it: not final")
	}
	if almanacDayFinal(d, now, (d+1)*86400+60, 0, false) {
		t.Fatalf("flush before end(d)+1h: not final")
	}
	if almanacDayFinal(d, now, 0, 0, false) {
		t.Fatalf("no successful flush in this process: not final")
	}
	if almanacDayFinal(d, now, flushAfter, d, true) {
		t.Fatalf("a pending delta for d itself blocks finality")
	}
	if almanacDayFinal(d, now, flushAfter, d-5, true) {
		t.Fatalf("a pending delta for a day before d blocks finality")
	}
	if !almanacDayFinal(d, now, flushAfter, d+1, true) {
		t.Fatalf("pending deltas only for later days must not block d")
	}
}

// ---------------------------------------------------------------------------
// Day fold builder, offsets, cap
// ---------------------------------------------------------------------------

func TestAlmanacDayFoldBuilder(t *testing.T) {
	day := utcDayIndex(time.Date(2026, time.September, 14, 0, 0, 0, 0, time.UTC).Unix())
	f := newAlmanacDayFold(day)
	if f.YearMonth != 202609 || f.DayOfMonth != 14 {
		t.Fatalf("year_month/dom = %d/%d, want 202609/14", f.YearMonth, f.DayOfMonth)
	}
	f.add(dailyRow("JO32", "20m", "NA", 30, 7))
	f.add(dailyRow("JO32", "20m", "NA", 31, 300))
	f.add(dailyRow("JO32", "20m", "NA", 31, 1)) // saturates, never wraps
	f.add(dailyRow("JO32", "40m", "EU", 2, 3))
	f.add(dailyRow("JO33", "20m", "SA", 47, math.MaxInt64))
	f.add(dailyRow("JO33", "20m", "SA", 48, 5))  // invalid slot ignored
	f.add(dailyRow("JO33", "20m", "SA", -1, 5))  // invalid slot ignored
	f.add(dailyRow("JO33", "20m", "SA", 10, 0))  // zero ignored
	f.add(dailyRow("JO33", "20m", "SA", 11, -4)) // negative ignored

	seg := f.Segments[almanacSegKey{"JO32", "20m", "NA"}]
	if seg == nil || seg[30] != 7 || seg[31] != 255 {
		t.Fatalf("JO32/20m/NA segment = %v", seg)
	}
	if got := f.Segments[almanacSegKey{"JO33", "20m", "SA"}]; got == nil || got[47] != 255 || got[10] != 0 || got[11] != 0 {
		t.Fatalf("JO33 segment = %v", got)
	}
	if len(f.Segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(f.Segments))
	}
	active := foldActive(f)
	for _, gb := range []almanacGridBand{{"JO32", "20m"}, {"JO32", "40m"}, {"JO33", "20m"}} {
		if _, ok := active[gb]; !ok {
			t.Fatalf("activity missing %+v", gb)
		}
	}
	if len(active) != 3 {
		t.Fatalf("activity entries = %d, want 3", len(active))
	}
	// Ingest totals are uncapped sums of spot_count (the region-key unit).
	if f.SlotTotals[31] != 301 || f.SlotTotals[30] != 7 || f.SlotTotals[2] != 3 {
		t.Fatalf("slot totals = %v", f.SlotTotals)
	}
	if f.dayMaskBit() != 1<<13 {
		t.Fatalf("day mask bit = %b, want bit 13", f.dayMaskBit())
	}
	if almanacSegmentOffset(1) != 0 || almanacSegmentOffset(14) != 13*48 || almanacSegmentOffset(31) != 30*48 {
		t.Fatalf("segment offsets drifted")
	}
	if almanacSeasonCountsLen != 31*48 {
		t.Fatalf("counts length = %d, want 1488", almanacSeasonCountsLen)
	}
}

func TestAlmanacCountCap(t *testing.T) {
	f := newAlmanacDayFold(almanacTestToday() - 2)
	f.add(dailyRow("JO32", "20m", "NA", 5, 300))
	if got := f.Segments[almanacSegKey{"JO32", "20m", "NA"}][5]; got != 255 {
		t.Fatalf("a count of 300 is stored as %d, want 255", got)
	}
}

// ---------------------------------------------------------------------------
// Fold driver (idempotence, watermark, failure)
// ---------------------------------------------------------------------------

func finalFlush(today int64) *fakeAlmanacFlushState {
	return &fakeAlmanacFlushState{lastOK: almanacTestNow.Unix()}
}

func TestAlmanacFoldWritesSegmentAndAdvancesWatermark(t *testing.T) {
	today := almanacTestToday()
	d := today - 2
	st := newFakeAlmanacFoldStore()
	st.daily[d-1] = []almanacDailyRow{dailyRow("JO32", "20m", "EU", 0, 1)} // initial (lost) day
	st.daily[d] = []almanacDailyRow{
		dailyRow("JO32", "20m", "NA", 36, 9),
		dailyRow("JO32", "20m", "NA", 37, 4),
	}
	st.daily[today-1] = []almanacDailyRow{dailyRow("JO32", "20m", "NA", 36, 2)}

	f := newTestAlmanacFolder(st, finalFlush(today))
	if err := f.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if st.state.watermark != d {
		t.Fatalf("watermark = %d, want today-2 = %d", st.state.watermark, d)
	}
	for _, c := range st.calls {
		if c == today-1 {
			t.Fatalf("today-1 must not be folded")
		}
	}
	ym, dom := almanacYearMonthDOM(d)
	row := decodeFoldRow(t, st.state.counts[fakeSeasonKey{"JO32", "20m", "NA", ym}])
	lo, hi := (dom-1)*48, dom*48
	for i, b := range row {
		inSeg := i >= lo && i < hi
		switch {
		case i == lo+36 && b != 9, i == lo+37 && b != 4:
			t.Fatalf("byte %d = %d", i, b)
		case !inSeg && b != 0:
			t.Fatalf("byte %d outside [%d,%d) was written: %d", i, lo, hi, b)
		}
	}
	if st.state.masks[fakeActivityKey{"JO32", "20m", ym}]&(1<<(dom-1)) == 0 {
		t.Fatalf("activity bit for dom %d not set", dom)
	}
	if h := f.health(); h.WatermarkDay != d || h.FailStreak != 0 || h.LastOKUnix == 0 {
		t.Fatalf("health = %+v", h)
	}
}

func TestAlmanacFoldTwiceIsByteIdentical(t *testing.T) {
	today := almanacTestToday()
	d := today - 2
	st := newFakeAlmanacFoldStore()
	st.state.watermark, st.state.hasWatermark = d-1, true
	st.daily[d] = []almanacDailyRow{
		dailyRow("JO32", "20m", "NA", 36, 9),
		dailyRow("JO32", "40m", "SA", 2, 400),
	}
	if _, err := st.foldDay(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	first := st.state.clone()

	// Re-apply the same day as if the watermark had not advanced (crash after
	// commit but before the in-memory state caught up, operator re-fold...).
	st.state.watermark = d - 1
	if _, err := st.foldDay(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if len(st.state.counts) != len(first.counts) || len(st.state.masks) != len(first.masks) {
		t.Fatalf("re-fold changed the key set")
	}
	for k, v := range first.counts {
		if !bytes.Equal(st.state.counts[k], v) {
			t.Fatalf("re-fold changed counts for %+v", k)
		}
	}
	for k, v := range first.masks {
		if st.state.masks[k] != v {
			t.Fatalf("re-fold changed mask for %+v: %b vs %b", k, st.state.masks[k], v)
		}
	}
	for k, v := range first.ingest {
		if st.state.ingest[k] != v {
			t.Fatalf("re-fold changed ingest totals for %+v", k)
		}
	}
	// And a second runOnce over an already-folded range is a no-op.
	f := newTestAlmanacFolder(st, finalFlush(today))
	st.calls = nil
	if err := f.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(st.calls) != 0 {
		t.Fatalf("runOnce re-folded already-folded days: %v", st.calls)
	}
}

// decodeFoldRow decodes a stored sparse row into the dense month grid.
func decodeFoldRow(t *testing.T, enc []byte) *[almanacSeasonCountsLen]byte {
	t.Helper()
	var dense [almanacSeasonCountsLen]byte
	if err := almanacSparseDecode(enc, &dense); err != nil {
		t.Fatalf("stored row does not decode: %v", err)
	}
	return &dense
}

func TestAlmanacFoldOverwritesDaySegmentKeepsOtherDays(t *testing.T) {
	today := almanacTestToday()
	d2 := today - 2
	d1 := today - 3
	ym1, dom1 := almanacYearMonthDOM(d1)
	ym2, dom2 := almanacYearMonthDOM(d2)
	if ym1 != ym2 {
		t.Skip("fixture days straddle a month")
	}
	key := fakeSeasonKey{"JO32", "20m", "NA", ym2}
	st := newFakeAlmanacFoldStore()
	st.state.watermark, st.state.hasWatermark = d1-1, true
	// An existing row with data on another day of the month (first and last
	// positions too) and stale data on d2's own segment.
	var pre [almanacSeasonCountsLen]byte
	pre[0], pre[almanacSeasonCountsLen-1] = 3, 4
	pre[almanacSegmentOffset(dom2)+5] = 99 // stale: must be replaced, not added to
	pre[almanacSegmentOffset(dom2)+47] = 7 // stale: absent from the new segment → zero
	st.state.counts[key] = almanacSparseEncode(&pre)

	st.daily[d1] = []almanacDailyRow{dailyRow("JO32", "20m", "NA", 10, 2)}
	st.daily[d2] = []almanacDailyRow{dailyRow("JO32", "20m", "NA", 5, 6), dailyRow("JO32", "20m", "NA", 6, 1)}
	f := newTestAlmanacFolder(st, finalFlush(today))
	if err := f.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := decodeFoldRow(t, st.state.counts[key])
	want := pre
	want[almanacSegmentOffset(dom1)+10] = 2
	want[almanacSegmentOffset(dom2)+5] = 6
	want[almanacSegmentOffset(dom2)+6] = 1
	want[almanacSegmentOffset(dom2)+47] = 0
	if *got != want {
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("pos %d = %d, want %d", i, got[i], want[i])
			}
		}
		t.FailNow()
	}
	// The stored bytes are exactly the canonical encoding.
	if !bytes.Equal(st.state.counts[key], almanacSparseEncode(&want)) {
		t.Fatalf("stored row is not the canonical encoding")
	}
}

func TestAlmanacFoldMalformedRowTreatedAsAbsent(t *testing.T) {
	today := almanacTestToday()
	d := today - 2
	ym, dom := almanacYearMonthDOM(d)
	key := fakeSeasonKey{"JO32", "20m", "NA", ym}
	st := newFakeAlmanacFoldStore()
	st.state.watermark, st.state.hasWatermark = d-1, true
	st.state.counts[key] = []byte{0x7f, 0x00, 0x05} // bad version
	st.daily[d] = []almanacDailyRow{dailyRow("JO32", "20m", "NA", 9, 3)}
	if _, err := st.foldDay(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	var want [almanacSeasonCountsLen]byte
	want[almanacSegmentOffset(dom)+9] = 3
	if *decodeFoldRow(t, st.state.counts[key]) != want {
		t.Fatalf("malformed row not replaced by the fresh segment")
	}
}

func TestAlmanacFoldRespectsFinality(t *testing.T) {
	today := almanacTestToday()
	d := today - 2
	mk := func() *fakeAlmanacFoldStore {
		st := newFakeAlmanacFoldStore()
		st.state.watermark, st.state.hasWatermark = d-1, true
		st.daily[d] = []almanacDailyRow{dailyRow("JO32", "20m", "NA", 36, 9)}
		return st
	}

	t.Run("pending delta for d blocks the fold", func(t *testing.T) {
		st := mk()
		f := newTestAlmanacFolder(st, &fakeAlmanacFlushState{lastOK: almanacTestNow.Unix(), pendingMin: d, hasPending: true})
		if err := f.runOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if st.state.watermark != d-1 || len(st.calls) != 0 {
			t.Fatalf("folded despite a pending delta: w=%d calls=%v", st.state.watermark, st.calls)
		}
	})
	t.Run("stale flush blocks the fold", func(t *testing.T) {
		st := mk()
		f := newTestAlmanacFolder(st, &fakeAlmanacFlushState{lastOK: (d+1)*86400 + 1800})
		if err := f.runOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if st.state.watermark != d-1 || len(st.calls) != 0 {
			t.Fatalf("folded before flush caught up: w=%d calls=%v", st.state.watermark, st.calls)
		}
	})
}

func TestAlmanacFoldMidTransactionFailure(t *testing.T) {
	today := almanacTestToday()
	st := newFakeAlmanacFoldStore()
	st.state.watermark, st.state.hasWatermark = today-5, true
	for d := today - 4; d <= today-2; d++ {
		st.daily[d] = []almanacDailyRow{dailyRow("JO32", "20m", "NA", 36, d%7+1)}
	}
	st.failDay = today - 3
	f := newTestAlmanacFolder(st, finalFlush(today))
	if err := f.runOnce(context.Background()); err == nil {
		t.Fatalf("expected the injected failure to surface")
	}
	if st.state.watermark != today-4 {
		t.Fatalf("watermark = %d, want %d (unchanged by the failed day)", st.state.watermark, today-4)
	}
	ym, dom := almanacYearMonthDOM(today - 3)
	enc := st.state.counts[fakeSeasonKey{"JO32", "20m", "NA", ym}]
	off := almanacSegmentOffset(dom)
	if enc != nil && decodeFoldRow(t, enc)[off+36] != 0 {
		t.Fatalf("failed day left bytes behind")
	}
	if h := f.health(); h.FailStreak != 1 {
		t.Fatalf("fail streak = %d, want 1", h.FailStreak)
	}
	st.failDay = 0
	if err := f.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st.state.watermark != today-2 || f.health().FailStreak != 0 {
		t.Fatalf("recovery: w=%d streak=%d", st.state.watermark, f.health().FailStreak)
	}
}

func TestAlmanacInitialWatermark(t *testing.T) {
	today := almanacTestToday()
	st := newFakeAlmanacFoldStore()
	min := today - 35
	for d := min; d <= today; d++ {
		st.daily[d] = []almanacDailyRow{dailyRow("JO32", "20m", "NA", 36, 1)}
	}
	f := newTestAlmanacFolder(st, finalFlush(today))
	if err := f.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(st.calls) == 0 || st.calls[0] != min+1 {
		t.Fatalf("folding must begin at min+1, calls=%v", st.calls)
	}
	if st.state.lost[min] != almanacLostReasonInitial || len(st.state.lost) != 1 {
		t.Fatalf("lost days = %v, want only the initial partial day %d", st.state.lost, min)
	}
	if f.health().LostDays != 1 {
		t.Fatalf("health lost days = %d", f.health().LostDays)
	}

	w, lost, hasLost := almanacInitialWatermark(0, false, today)
	if w != today-1 || hasLost || lost != 0 {
		t.Fatalf("empty daily table: w=%d lost=%d/%v, want today-1 and no lost day", w, lost, hasLost)
	}
}

func TestAlmanacFoldSeedsIngestSlots(t *testing.T) {
	today := almanacTestToday()
	d := today - 2
	st := newFakeAlmanacFoldStore()
	st.state.watermark, st.state.hasWatermark = d-2, true
	// d-1: pre-deploy backlog, no ingest-slot rows at all.
	st.daily[d-1] = []almanacDailyRow{
		dailyRow("JO32", "20m", "NA", 36, 9),
		dailyRow("JO33", "20m", "EU", 36, 1),
		dailyRow("JO32", "40m", "EU", 2, 3),
	}
	// d: deploy day, flush totals cover only part of the day.
	st.daily[d] = []almanacDailyRow{dailyRow("JO32", "20m", "NA", 40, 12)}
	st.state.ingest[fakeIngestKey{d, 40}] = 5
	f := newTestAlmanacFolder(st, finalFlush(today))
	if err := f.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st.state.ingest[fakeIngestKey{d - 1, 36}] != 10 || st.state.ingest[fakeIngestKey{d - 1, 2}] != 3 {
		t.Fatalf("pre-deploy day not seeded: %v", st.state.ingest)
	}
	if _, ok := st.state.ingest[fakeIngestKey{d - 1, 0}]; ok {
		t.Fatalf("zero slots must not be seeded")
	}
	if st.state.ingest[fakeIngestKey{d, 40}] != 12 {
		t.Fatalf("partial flush totals not raised to the day's sum: %d", st.state.ingest[fakeIngestKey{d, 40}])
	}
}

// ---------------------------------------------------------------------------
// Prune gating
// ---------------------------------------------------------------------------

func TestAlmanacPruneDecide(t *testing.T) {
	cases := []struct {
		name              string
		cutoff, watermark int64
		diskOver          bool
		wantBelow         int64
		wantForce         bool
		wantNewW          int64
	}{
		{"fold ahead of cutoff: plain cutoff", 100, 150, false, 100, false, 0},
		{"watermark at cutoff-1: plain cutoff", 100, 99, false, 100, false, 0},
		{"fold behind within grace: hold at watermark+1", 100, 95, false, 96, false, 0},
		{"fold behind exactly at grace edge", 100, 92, false, 93, false, 0},
		{"fold behind past grace: forced", 100, 80, false, 93, true, 92},
		{"disk over: grace skipped, forced to cutoff", 100, 95, true, 100, true, 99},
		{"disk over but fold ahead: plain", 100, 120, true, 100, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := almanacPruneDecide(c.cutoff, c.watermark, almanacFoldGraceDays, c.diskOver)
			if got.DeleteBelow != c.wantBelow || got.Force != c.wantForce || (c.wantForce && got.NewWatermark != c.wantNewW) {
				t.Fatalf("decide(%d,%d,disk=%v) = %+v", c.cutoff, c.watermark, c.diskOver, got)
			}
			if !got.Force && got.DeleteBelow > c.watermark+1 {
				t.Fatalf("unforced prune would delete unfolded days: %+v", got)
			}
			if got.Force && got.DeleteBelow != got.NewWatermark+1 {
				t.Fatalf("forced prune must only delete days the new watermark covers: %+v", got)
			}
		})
	}
	if almanacFoldGraceDays != 7 {
		t.Fatalf("grace = %d, want 7", almanacFoldGraceDays)
	}
	if days := almanacLostDayRange(80, 92); len(days) != 12 || days[0] != 81 || days[11] != 92 {
		t.Fatalf("lost range = %v", days)
	}
	if days := almanacLostDayRange(92, 92); len(days) != 0 {
		t.Fatalf("no-op lost range = %v", days)
	}
}

func TestAlmanacPruneGate(t *testing.T) {
	today := almanacTestToday()

	t.Run("fold disabled: prune ungated", func(t *testing.T) {
		s := &dxPostgresStore{}
		got, err := s.dxRegionPruneCutoff(context.Background(), today-35)
		if err != nil || got != today-35 {
			t.Fatalf("ungated cutoff = %d, %v", got, err)
		}
	})

	t.Run("gated at watermark+1", func(t *testing.T) {
		st := newFakeAlmanacFoldStore()
		st.state.watermark, st.state.hasWatermark = today-38, true
		s := &dxPostgresStore{}
		s.setAlmanacFolder(newTestAlmanacFolder(st, finalFlush(today)))
		got, err := s.dxRegionPruneCutoff(context.Background(), today-35)
		if err != nil || got != today-37 {
			t.Fatalf("gated cutoff = %d, %v; want min(cutoff, W+1) = %d", got, err, today-37)
		}
		if len(st.state.lost) != 0 {
			t.Fatalf("within grace nothing is lost: %v", st.state.lost)
		}
	})

	t.Run("past grace: forced prune records lost days and fold skips them", func(t *testing.T) {
		st := newFakeAlmanacFoldStore()
		w0 := today - 50
		st.state.watermark, st.state.hasWatermark = w0, true
		for d := w0 + 1; d <= today; d++ {
			st.daily[d] = []almanacDailyRow{dailyRow("JO32", "20m", "NA", 36, 1)}
		}
		f := newTestAlmanacFolder(st, finalFlush(today))
		s := &dxPostgresStore{}
		s.setAlmanacFolder(f)
		cutoff := today - 35
		got, err := s.dxRegionPruneCutoff(context.Background(), cutoff)
		forced := cutoff - almanacFoldGraceDays
		if err != nil || got != forced {
			t.Fatalf("forced cutoff = %d, %v; want %d", got, err, forced)
		}
		if st.state.watermark != forced-1 {
			t.Fatalf("watermark = %d, want %d", st.state.watermark, forced-1)
		}
		for d := w0 + 1; d < forced; d++ {
			if st.state.lost[d] != almanacLostReasonForcedPrune {
				t.Fatalf("day %d not recorded lost: %v", d, st.state.lost)
			}
		}
		if f.health().LostDays != forced-1-w0 {
			t.Fatalf("health lost days = %d", f.health().LostDays)
		}
		if err := f.runOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		for _, c := range st.calls {
			if c < forced {
				t.Fatalf("fold touched lost day %d", c)
			}
		}
	})

	t.Run("disk over threshold: grace skipped", func(t *testing.T) {
		st := newFakeAlmanacFoldStore()
		st.state.watermark, st.state.hasWatermark = today-38, true
		f := newTestAlmanacFolder(st, finalFlush(today))
		f.diskProbe = func(string) (float64, error) { return 0.85, nil }
		s := &dxPostgresStore{}
		s.setAlmanacFolder(f)
		got, err := s.dxRegionPruneCutoff(context.Background(), today-35)
		if err != nil || got != today-35 {
			t.Fatalf("cutoff = %d, %v; want the plain cutoff", got, err)
		}
		if st.state.watermark != today-36 || len(st.state.lost) != 2 {
			t.Fatalf("w=%d lost=%v", st.state.watermark, st.state.lost)
		}
	})

	t.Run("disk probe error counts as over threshold", func(t *testing.T) {
		st := newFakeAlmanacFoldStore()
		st.state.watermark, st.state.hasWatermark = today-38, true
		f := newTestAlmanacFolder(st, finalFlush(today))
		f.diskProbe = func(string) (float64, error) { return 0, errors.New("statfs: no such file") }
		s := &dxPostgresStore{}
		s.setAlmanacFolder(f)
		got, _ := s.dxRegionPruneCutoff(context.Background(), today-35)
		if got != today-35 {
			t.Fatalf("probe error must skip the grace period, got cutoff %d", got)
		}
	})
}

// ---------------------------------------------------------------------------
// Disk probe
// ---------------------------------------------------------------------------

func TestAlmanacDiskProbe(t *testing.T) {
	if !almanacDiskOverThreshold(0.5, errors.New("boom")) {
		t.Fatalf("probe error must count as over threshold")
	}
	if !almanacDiskOverThreshold(0.81, nil) {
		t.Fatalf("81%% is over the 80%% threshold")
	}
	if almanacDiskOverThreshold(0.80, nil) || almanacDiskOverThreshold(0.2, nil) {
		t.Fatalf("<=80%% is not over")
	}
	if _, err := almanacDiskUsedFraction(""); err == nil {
		t.Fatalf("an unset -almanac-disk-path must be a probe error (fail-safe)")
	}
	if _, err := almanacDiskUsedFraction(t.TempDir() + "/does-not-exist"); err == nil {
		t.Fatalf("missing path must error")
	}
	frac, err := almanacDiskUsedFraction(t.TempDir())
	if err != nil || frac < 0 || frac > 1 {
		t.Fatalf("probe on a real dir = %v, %v", frac, err)
	}
	if got := almanacUsedFraction(100, 60, 50); math.Abs(got-40.0/90.0) > 1e-9 {
		t.Fatalf("used fraction (df semantics) = %v", got)
	}
	if got := almanacUsedFraction(0, 0, 0); got != 1 {
		t.Fatalf("zero-block filesystem must read as full, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Live observe() late-spot clamp
// ---------------------------------------------------------------------------

func newObserveTestStore() *dxPostgresStore {
	return &dxPostgresStore{
		pendingGlobal:  make(map[baselineGlobalKey]baselineDelta),
		pendingRegion:  make(map[dxPulseRegionBaselineDailyKey]int64),
		pendingCluster: make(map[clusterBaselineKey]baselineDelta),
		flushCh:        make(chan struct{}, 1),
	}
}

func TestObserveLateSpotClamp(t *testing.T) {
	now := time.Now().Unix()
	spot := func(ts int64) MQTTMessage {
		return MQTTMessage{T: ts, SC: "K1ABC", SL: "FN31", RC: "DL1ABC", RL: "JO32", B: "20m", MD: "FT8"}
	}

	s := newObserveTestStore()
	_ = s.observe(spot(now-60), "20m", 0, 0, 0)
	if len(s.pendingRegion) != 2 || s.regionLateDrops.Load() != 0 {
		t.Fatalf("fresh spot: region keys=%d drops=%d", len(s.pendingRegion), s.regionLateDrops.Load())
	}

	s = newObserveTestStore()
	folded := now - 3*86400 // a day that may already be folded
	_ = s.observe(spot(folded), "20m", 0, 0, 0)
	if len(s.pendingRegion) != 0 {
		t.Fatalf("a spot older than 24h must not emit region keys: %v", s.pendingRegion)
	}
	if s.regionLateDrops.Load() != 1 {
		t.Fatalf("late drop not counted: %d", s.regionLateDrops.Load())
	}
	if len(s.pendingGlobal) != 1 || len(s.pendingCluster) == 0 {
		t.Fatalf("other baselines must be unaffected by the clamp")
	}

	s = newObserveTestStore()
	_ = s.observe(spot(now+11*60), "20m", 0, 0, 0)
	if len(s.pendingRegion) != 0 || s.regionLateDrops.Load() != 1 {
		t.Fatalf("a spot >10 min in the future must be dropped and counted")
	}

	if !almanacRegionTimestampAccepted(now-86400, now) || almanacRegionTimestampAccepted(now-86401, now) {
		t.Fatalf("24h boundary drifted")
	}
	if !almanacRegionTimestampAccepted(now+600, now) || almanacRegionTimestampAccepted(now+601, now) {
		t.Fatalf("10 min future boundary drifted")
	}
}

func TestRebuildEmitterKeepsOldSpots(t *testing.T) {
	// The clamp lives only in live observe(): the shared key emitter used by
	// ensureDxPulseRegionBaseline must still emit keys for old spots.
	old := time.Now().Unix() - 20*86400
	if keys := dxPulseRegionBaselineKeysForSpot(old, "20m", "FN31", "JO32"); len(keys) != 2 {
		t.Fatalf("rebuild emitter dropped an old spot: %v", keys)
	}
}

// ---------------------------------------------------------------------------
// Pending/in-flight min day, flush totals
// ---------------------------------------------------------------------------

func TestPendingRegionMinDayIncludesInflight(t *testing.T) {
	s := newObserveTestStore()
	if _, ok := s.pendingRegionMinDay(); ok {
		t.Fatalf("empty store reports a pending day")
	}
	s.pendingRegion[dxPulseRegionBaselineDailyKey{TargetGrid4: "JO32", DayIndex: 20000}] = 1
	s.pendingRegion[dxPulseRegionBaselineDailyKey{TargetGrid4: "JO33", DayIndex: 19999}] = 1
	if d, ok := s.pendingRegionMinDay(); !ok || d != 19999 {
		t.Fatalf("min pending = %d, %v", d, ok)
	}
	s.setInflightRegion(map[dxPulseRegionBaselineDailyKey]int64{{DayIndex: 19990}: 3})
	if d, _ := s.pendingRegionMinDay(); d != 19990 {
		t.Fatalf("in-flight flush batch must count as pending: %d", d)
	}
	s.setInflightRegion(nil)
	if d, _ := s.pendingRegionMinDay(); d != 19999 {
		t.Fatalf("after the flush completes: %d", d)
	}
}

func TestAlmanacIngestSlotTotalsFromRegion(t *testing.T) {
	region := map[dxPulseRegionBaselineDailyKey]int64{
		{TargetGrid4: "JO32", Band: "20m", SlotOfDay: 36, Region: "NA", DayIndex: 100}: 3,
		{TargetGrid4: "FN31", Band: "20m", SlotOfDay: 36, Region: "EU", DayIndex: 100}: 3,
		{TargetGrid4: "JO32", Band: "40m", SlotOfDay: 2, Region: "EU", DayIndex: 101}:  4,
	}
	got := almanacIngestSlotTotalsFromRegion(region)
	if len(got) != 2 || got[almanacIngestSlotKey{100, 36}] != 6 || got[almanacIngestSlotKey{101, 2}] != 4 {
		t.Fatalf("totals = %v", got)
	}
}

func TestBaselineFlushStmtsIncludeIngestTotals(t *testing.T) {
	region := map[dxPulseRegionBaselineDailyKey]int64{
		{TargetGrid4: "JO32", Band: "20m", SlotOfDay: 36, Region: "NA", DayIndex: 100}: 3,
		{TargetGrid4: "FN31", Band: "20m", SlotOfDay: 36, Region: "EU", DayIndex: 100}: 2,
	}
	type flushStmt struct {
		sql  string
		args []any
	}
	var stmts []flushStmt
	record := func(sql string, args ...any) { stmts = append(stmts, flushStmt{sql, args}) }
	queued := baselineFlushStmts(nil, region, nil, record)
	if queued != len(stmts) {
		t.Fatalf("returned count %d != queued statements %d", queued, len(stmts))
	}
	var regionN, totals int
	for _, st := range stmts {
		switch {
		case strings.Contains(st.sql, "INSERT INTO dx_region_baseline_daily"):
			regionN++
		case strings.Contains(st.sql, "INSERT INTO almanac_ingest_slots"):
			totals++
			if st.args[0] != int64(100) || st.args[1] != 36 || st.args[2] != almanacSeasonLayerPSKR || st.args[3] != int64(5) {
				t.Fatalf("totals args = %v", st.args)
			}
			if !strings.Contains(st.sql, "almanac_ingest_slots.spot_total + EXCLUDED.spot_total") {
				t.Fatalf("flush totals must add (incremental), got %q", st.sql)
			}
		}
	}
	if regionN != 2 || totals != 1 {
		t.Fatalf("stmts: region=%d totals=%d (%d total)", regionN, totals, len(stmts))
	}
	stmts = nil
	if n := baselineFlushStmts(nil, nil, nil, record); n != 0 || len(stmts) != 0 {
		t.Fatalf("empty flush must queue nothing")
	}
}

// ---------------------------------------------------------------------------
// SQL shape guards (the SQL mirrors fakeAlmanacFoldStore)
// ---------------------------------------------------------------------------

func TestAlmanacFoldSQLShape(t *testing.T) {
	if !strings.Contains(almanacFoldUpsertSQL, "SET counts = EXCLUDED.counts") {
		t.Fatalf("season upsert must store the re-encoded row (SET semantics): %q", almanacFoldUpsertSQL)
	}
	if strings.Contains(almanacFoldUpsertSQL, "counts +") || strings.Contains(almanacFoldUpsertSQL, "+ EXCLUDED") ||
		strings.Contains(almanacFoldUpsertSQL, "overlay(") {
		t.Fatalf("season upsert must never add or patch counts in SQL (sparse rows are merged in Go)")
	}
	if !strings.Contains(almanacFoldDeclareSQL, "JOIN almanac_fold_seg") || !strings.Contains(almanacFoldDeclareSQL, "CURSOR") {
		t.Fatalf("existing rows must be streamed through a cursor joined on the day's keys: %q", almanacFoldDeclareSQL)
	}
	if want := fmt.Sprintf("FETCH %d ", almanacFoldFetchBatch); !strings.HasPrefix(almanacFoldFetchSQL, want) {
		t.Fatalf("fetch batch drifted from almanacFoldFetchBatch: %q", almanacFoldFetchSQL)
	}
	if !strings.Contains(almanacFoldActivitySQL, "day_mask | EXCLUDED.day_mask") {
		t.Fatalf("activity upsert must OR the mask: %q", almanacFoldActivitySQL)
	}
	if !strings.Contains(almanacFoldIngestSeedSQL, "GREATEST(") {
		t.Fatalf("ingest seed must be idempotent (GREATEST): %q", almanacFoldIngestSeedSQL)
	}
	if !strings.Contains(almanacFoldStreamSQL, "WHERE day_index = $1") {
		t.Fatalf("fold must stream one day via the day_index index: %q", almanacFoldStreamSQL)
	}
	if strings.Contains(strings.ToLower(almanacFoldStreamSQL), "extract(") {
		t.Fatalf("no extract() in fold SQL")
	}
}

func TestAlmanacFoldStatsBlock(t *testing.T) {
	if almanacFoldStats(nil) != nil {
		t.Fatalf("no store: no block")
	}
	s := newObserveTestStore()
	s.regionLateDrops.Add(3)
	b := almanacFoldStats(s)
	if b.Enabled || b.WatermarkDay != -1 || b.LateRegionDrops != 3 {
		t.Fatalf("fold disabled block = %+v", b)
	}
	st := newFakeAlmanacFoldStore()
	st.state.watermark, st.state.hasWatermark = almanacTestToday()-3, true
	f := newTestAlmanacFolder(st, finalFlush(almanacTestToday()))
	s.setAlmanacFolder(f)
	if err := f.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	b = almanacFoldStats(s)
	want := time.Unix((almanacTestToday()-2)*86400, 0).UTC().Format("2006-01-02")
	if !b.Enabled || b.WatermarkDay != almanacTestToday()-2 || b.WatermarkDate != want || b.LastOKUnix == 0 {
		t.Fatalf("enabled block = %+v", b)
	}
}

// ---------------------------------------------------------------------------
// U6: pre-warm the configured areas after each successful fold
// ---------------------------------------------------------------------------

type failingAlmanacFoldStore struct{ *fakeAlmanacFoldStore }

func (failingAlmanacFoldStore) ensureWatermark(context.Context, int64) (int64, error) {
	return 0, errors.New("injected watermark failure")
}

func TestAlmanacFoldPrewarmsConfiguredAreas(t *testing.T) {
	svc, _, _ := newTestAlmanacService(populatedAggWorld())
	prevSvc := almanacSvc
	t.Cleanup(func() { almanacSvc = prevSvc })
	almanacSvc = svc
	areas, err := parseAlmanacWSPRBackfillAreas("jo32, JO33")
	if err != nil {
		t.Fatalf("parse areas: %v", err)
	}
	hook := almanacPrewarmHook(areas)

	// A failed fold run does not pre-warm.
	bad := newTestAlmanacFolder(failingAlmanacFoldStore{newFakeAlmanacFoldStore()}, &fakeAlmanacFlushState{})
	bad.afterFold = hook
	bad.tick(context.Background())
	if _, ok := svc.warm(almanacArea{Grid4: "JO32", Source: qthSourceLocator}); ok {
		t.Fatalf("JO32 warm after a failed fold run")
	}

	f := newTestAlmanacFolder(newFakeAlmanacFoldStore(), &fakeAlmanacFlushState{})
	f.afterFold = hook
	f.tick(context.Background())
	for _, g := range []string{"JO32", "JO33"} {
		if _, ok := svc.warm(almanacArea{Grid4: g, Source: qthSourceLocator}); !ok {
			t.Errorf("%s not warm after a fold run", g)
		}
	}
}

// TestAlmanacPrewarmTolerant: no service, no areas, or a failing read are
// logged and never fatal.
func TestAlmanacPrewarmTolerant(t *testing.T) {
	almanacPrewarmAreas(nil, []string{"JO32"})
	f := populatedAggWorld()
	f.err = errors.New("boom")
	svc, _, _ := newTestAlmanacService(f)
	almanacPrewarmAreas(svc, []string{"JO32"})
	if _, ok := svc.warm(almanacArea{Grid4: "JO32"}); ok {
		t.Fatalf("warm after failed read")
	}
	prevSvc := almanacSvc
	t.Cleanup(func() { almanacSvc = prevSvc })
	almanacSvc = svc
	almanacPrewarmHook(nil)()
	almanacSvc = nil
	almanacPrewarmHook([]string{"JO32"})()
}

// foldActive is the day's active (grid, band) set: the distinct (Grid4, Band)
// of the segment keys, as almanacFoldActivitySQL derives it.
func foldActive(f *almanacDayFold) map[almanacGridBand]struct{} {
	out := make(map[almanacGridBand]struct{}, len(f.Segments))
	for k := range f.Segments {
		out[almanacGridBand{Grid: k.Grid4, Band: k.Band}] = struct{}{}
	}
	return out
}

// TestPruneRegionBaselinesGateFailure: a gate error skips only the dx
// table (never pruned ungated), still prunes the other two, and bumps the
// streak; a gate success resets it. ERROR escalation fires at 3, then every
// 24 further failures.
func TestPruneRegionBaselinesGateFailure(t *testing.T) {
	var streak atomic.Int64
	var pruned map[string]int64
	prune := func(table string, cutoff int64) (int64, error) {
		pruned[table] = cutoff
		return 1, nil
	}
	gateErr := func() (int64, error) { return 0, errors.New("injected gate failure") }
	for i := 1; i <= 3; i++ {
		pruned = map[string]int64{}
		n, err := pruneRegionBaselinesGated(100, gateErr, prune, &streak)
		if err != nil || n != 2 {
			t.Fatalf("run %d: n=%d err=%v, want 2 nil", i, n, err)
		}
		if _, ok := pruned["dx_region_baseline_daily"]; ok {
			t.Fatalf("run %d: dx table pruned despite gate failure", i)
		}
		if pruned["wspr_region_baseline_daily"] != 100 || pruned["prop_region_baseline_daily"] != 100 {
			t.Fatalf("run %d: other tables = %v", i, pruned)
		}
		if got := streak.Load(); got != int64(i) {
			t.Fatalf("run %d: streak = %d", i, got)
		}
	}

	pruned = map[string]int64{}
	n, err := pruneRegionBaselinesGated(100, func() (int64, error) { return 93, nil }, prune, &streak)
	if err != nil || n != 3 {
		t.Fatalf("gate ok: n=%d err=%v", n, err)
	}
	if pruned["dx_region_baseline_daily"] != 93 {
		t.Fatalf("dx cutoff = %d, want gated 93", pruned["dx_region_baseline_daily"])
	}
	if streak.Load() != 0 {
		t.Fatalf("streak = %d after success, want 0", streak.Load())
	}

	for n, want := range map[int64]bool{1: false, 2: false, 3: true, 4: false, 26: false, 27: true, 51: true, 50: false} {
		if got := pruneGateEscalate(n); got != want {
			t.Errorf("pruneGateEscalate(%d) = %v, want %v", n, got, want)
		}
	}
}

// TestAlmanacFoldStatsPruneGateStreak: the streak is exposed in
// /api/stats postgres.almanac_fold.
func TestAlmanacFoldStatsPruneGateStreak(t *testing.T) {
	st := &dxPostgresStore{}
	st.pruneGateFailStreak.Store(5)
	b := almanacFoldStats(st)
	if b == nil || b.PruneGateFailStreak != 5 {
		t.Fatalf("stats = %+v, want prune_gate_fail_streak 5", b)
	}
	raw, _ := json.Marshal(b)
	if !strings.Contains(string(raw), `"prune_gate_fail_streak":5`) {
		t.Fatalf("json = %s", raw)
	}
}
