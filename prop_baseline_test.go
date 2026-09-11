package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// propSpot builds an MQTTMessage for baseline tests. For wspr/rbn/dxcluster
// SC/SL is the receiver side; for pskr ("mqtt") RC/RL is the receiver side.
func propSpot(ts int64, source, band, sc, sl, rc, rl string) MQTTMessage {
	return MQTTMessage{Source: source, B: band, T: ts, SC: sc, SL: sl, RC: rc, RL: rl}
}

func TestPropBaselineObserveKeysBySource(t *testing.T) {
	e := newPropBaselineEngine("", "")
	now := time.Now().Unix()

	// Same band/region/time, four sources → four distinct buckets.
	e.Observe(propSpot(now, "wspr", "20m", "RX", "JO62qm", "TX", "FN31ab"))   // recv=SL JO62 (EU)
	e.Observe(propSpot(now, "mqtt", "20m", "TX", "JO62qm", "RX", "FN31ab"))   // recv=RL FN31 (NA)
	e.Observe(propSpot(now, "rbn", "20m", "SKIM", "JO62qm", "DX", "FN31ab"))  // recv=SL JO62 (EU)
	e.Observe(propSpot(now, "dxcluster", "20m", "SPOT", "JO62qm", "DX", "FN31ab"))

	if len(e.buckets) != 4 {
		t.Fatalf("expected 4 source-distinct buckets, got %d", len(e.buckets))
	}
	// pskr keys by the RC/RL (reporter) region: FN31 = NA.
	if b := e.buckets[propBaselineBucketKey("20m", "pskr", utcSlotOfDay(now), "NA")]; b == nil {
		t.Fatalf("pskr bucket must key by RC/RL region (NA); buckets: %v", keysOf(e.buckets))
	}
	// wspr keys by SC/SL (receiver) region: JO62 = EU.
	if b := e.buckets[propBaselineBucketKey("20m", "wspr", utcSlotOfDay(now), "EU")]; b == nil {
		t.Fatalf("wspr bucket must key by SC/SL region (EU); buckets: %v", keysOf(e.buckets))
	}
}

func TestPropBaselineRegionFallback(t *testing.T) {
	e := newPropBaselineEngine("", "")
	now := time.Now().Unix()

	// Receiver locator garbage → fall back to the other end's region.
	e.Observe(propSpot(now, "wspr", "20m", "RX", "XX99zz", "TX", "FN31ab"))
	if len(e.buckets) != 1 {
		t.Fatalf("expected fallback bucket, got %d", len(e.buckets))
	}
	if b := e.buckets[propBaselineBucketKey("20m", "wspr", utcSlotOfDay(now), "NA")]; b == nil {
		t.Fatalf("expected NA fallback region, buckets: %v", keysOf(e.buckets))
	}

	// Both ends garbage → no bucket.
	e.Observe(propSpot(now, "wspr", "20m", "RX", "XX99zz", "TX", "YY88ww"))
	if len(e.buckets) != 1 {
		t.Fatalf("both-invalid must not observe, got %d buckets", len(e.buckets))
	}

	// Unknown source tag → skipped entirely.
	e.Observe(propSpot(now, "pskunknown", "20m", "RX", "JO62qm", "TX", "FN31ab"))
	if len(e.buckets) != 1 {
		t.Fatalf("unknown source must be skipped, got %d buckets", len(e.buckets))
	}
}

func TestPropBaselineInMemStatsParityWithWSPR(t *testing.T) {
	unified := newPropBaselineEngine("", "")
	legacy := newWsprClimatologyEngine("")
	now := time.Now().Unix()

	// Feed identical WSPR spots to both engines across two synthetic days.
	day0 := now - 2*86400
	for i := 0; i < 4; i++ {
		m := propSpot(day0+int64(i)*300, "wspr", "20m", "RX", "JO62qm", "TX", "FN31ab")
		unified.Observe(m)
		legacy.Observe(m)
	}
	for i := 0; i < 2; i++ {
		m := propSpot(day0+86400+int64(i)*300, "wspr", "20m", "RX", "JO62qm", "TX", "FN31ab")
		unified.Observe(m)
		legacy.Observe(m)
	}

	uRows := unified.statsFromMemoryLocked(now)
	lRows := legacy.regionCalendarStatsFromMemoryLocked(now)

	slot := utcSlotOfDay(day0)
	find := func(rows []propRegionCalendarStatRow) *propRegionCalendarStatRow {
		for i := range rows {
			if rows[i].SlotOfDay == slot && rows[i].Source == "wspr" {
				return &rows[i]
			}
		}
		return nil
	}
	var u *propRegionCalendarStatRow
	if u = find(uRows); u == nil {
		t.Fatalf("unified row not found for slot %d: %+v", slot, uRows)
	}
	var l *wsprRegionCalendarStatRow
	for i := range lRows {
		if lRows[i].SlotOfDay == slot {
			l = &lRows[i]
		}
	}
	if l == nil {
		t.Fatalf("legacy row not found for slot %d: %+v", slot, lRows)
	}
	if u.Mean != l.Mean || u.StdDev != l.StdDev || u.SampleDays != l.SampleDays {
		t.Fatalf("parity mismatch: unified mean=%v std=%v days=%d vs legacy mean=%v std=%v days=%d",
			u.Mean, u.StdDev, u.SampleDays, l.Mean, l.StdDev, l.SampleDays)
	}
}

func TestPropBaselinePendingCap(t *testing.T) {
	pending := make(map[propBaselineKey]int64)
	// Two day groups, 60 keys each (over a tiny cap of 60).
	for i := 0; i < 60; i++ {
		pending[propBaselineKey{Band: "20m", Source: "wspr", SlotOfDay: i, Region: "EU", DayIndex: 100}] = 1
	}
	for i := 0; i < 60; i++ {
		pending[propBaselineKey{Band: "20m", Source: "wspr", SlotOfDay: i, Region: "NA", DayIndex: 200}] = 1
	}
	out, n := capPendingPropBaseline(pending, 60)
	if n > 60 || len(out) > 60 {
		t.Fatalf("cap not enforced: n=%d len=%d", n, len(out))
	}
	// Oldest day (100) must be dropped entirely.
	for k := range out {
		if k.DayIndex == 100 {
			t.Fatalf("oldest day index not dropped: %+v", k)
		}
	}
}

func TestPropBaselineJSONLRoundTripAndV1Import(t *testing.T) {
	dir := t.TempDir()

	// V1 import: write a legacy wsprClimatologySnapshot, import it.
	legacyPath := filepath.Join(dir, "wspr_climatology.json")
	legacy := wsprClimatologySnapshot{
		Version:      wsprClimatologyVersion,
		SavedAt:      123,
		FirstEventAt: 100,
		LastEventAt:  200,
		Buckets: map[string]*wsprClimatologyBucket{
			"20m|20|EU": {Band: "20m", SlotOfDay: 20, Region: "EU", Count: 7, DayCounts: map[int64]int64{50: 7}},
		},
	}
	if err := writeJSONAtomic(legacyPath, legacy); err != nil {
		t.Fatalf("write legacy snapshot: %v", err)
	}
	e := newPropBaselineEngine("", legacyPath)
	if err := e.Load(); err != nil {
		t.Fatalf("v1 import load: %v", err)
	}
	b := e.buckets[propBaselineBucketKey("20m", "wspr", 20, "EU")]
	if b == nil || b.Source != "wspr" || b.Count != 7 {
		t.Fatalf("v1 import mismatch: %+v", b)
	}

	// V2 round-trip.
	v2Path := filepath.Join(dir, "prop_baseline.json")
	e2 := newPropBaselineEngine(v2Path, legacyPath)
	e2.Observe(propSpot(time.Now().Unix(), "rbn", "40m", "SKIM", "JO62qm", "DX", "FN31ab"))
	if err := e2.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	e3 := newPropBaselineEngine(v2Path, legacyPath)
	if err := e3.Load(); err != nil {
		t.Fatalf("round-trip load: %v", err)
	}
	var rbnBucket *propBaselineBucket
	for _, b := range e3.buckets {
		if b.Source == "rbn" {
			rbnBucket = b
		}
	}
	if rbnBucket == nil || rbnBucket.Count != 1 {
		t.Fatalf("round-trip lost rbn bucket: %+v", e3.buckets)
	}
	// Loading v2 must NOT re-import the legacy file on top.
	if e3.firstEventAt == 100 {
		t.Fatalf("legacy import must not run when v2 file exists")
	}

	// Missing files are fine.
	e4 := newPropBaselineEngine(filepath.Join(dir, "nope.json"), filepath.Join(dir, "nope2.json"))
	if err := e4.Load(); err != nil {
		t.Fatalf("missing files must be a no-op: %v", err)
	}
	os.Remove(legacyPath) // silence unused warning if assertions reordered
}

func keysOf(m map[string]*propBaselineBucket) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestPropBaselineStatsAndSetStore covers Stats and SetStore (nil and real
// receivers) surfaced via /api/stats.
func TestPropBaselineStatsAndSetStore(t *testing.T) {
	var nilEngine *propBaselineEngine
	if c, m := nilEngine.Stats(time.Now().Unix()); c != 0 || m != 0 {
		t.Fatalf("nil engine stats = (%d, %d), want (0, 0)", c, m)
	}
	nilEngine.SetStore(nil) // must not panic

	e := newPropBaselineEngine("", "")
	if c, m := e.Stats(time.Now().Unix()); c != 0 || m != 0 {
		t.Fatalf("empty engine stats = (%d, %d), want (0, 0)", c, m)
	}
	// Anchor inside a single 30-minute UTC slot so now/now+180 can't straddle
	// a slot boundary and flip the bucket count (time-of-run flake).
	blockStart := time.Now().Unix() - time.Now().Unix()%(30*60)
	now := blockStart + 60
	e.Observe(propSpot(now, "wspr", "20m", "RX", "JO62qm", "TX", "FN31ab"))
	e.Observe(propSpot(now+180, "wspr", "20m", "RX", "JO62qm", "TX", "FN31ab"))
	c, m := e.Stats(now + 180)
	if c != 1 {
		t.Fatalf("bucket count = %d, want 1 (both spots share the slot)", c)
	}
	if m < 2 || m > 4 {
		t.Fatalf("history minutes = %d, want ~3", m)
	}

	st := &dxPostgresStore{}
	e.SetStore(st)
	if e.store != st {
		t.Fatalf("SetStore did not wire the store")
	}
}

// TestPropBaselineFlushPendingNilStoreDrains: with no Postgres store wired,
// FlushPending drains the synthetic pending queue and reports zero flushed
// keys; the store-method nil guards are exercised too.
func TestPropBaselineFlushPendingNilStoreDrains(t *testing.T) {
	var s *dxPostgresStore
	if err := s.FlushPendingPropBaseline(context.Background(), nil); err != nil {
		t.Fatalf("nil store flush must be a no-op, got %v", err)
	}
	if err := s.ensurePropRegionBaseline(context.Background()); err != nil {
		t.Fatalf("nil store ensure must be a no-op, got %v", err)
	}
	if rows, err := s.PropRegionCalendarStats(context.Background(), []string{"wspr"}, 30, time.Now().Unix()); rows != nil || err != nil {
		t.Fatalf("nil store stats = (%v, %v), want (nil, nil)", rows, err)
	}

	e := newPropBaselineEngine("", "")
	n, err := e.FlushPending(context.Background())
	if n != 0 || err != nil {
		t.Fatalf("empty pending flush = (%d, %v), want (0, nil)", n, err)
	}

	// Synthetic pending queue (Observe only enqueues when a store is wired).
	day := utcDayIndex(time.Now().Unix())
	slot := utcSlotOfDay(time.Now().Unix())
	e.mu.Lock()
	e.pending[propBaselineKey{Band: "20m", Source: "wspr", SlotOfDay: slot, Region: "EU", DayIndex: day}] = 3
	e.pendingCount = 3
	e.mu.Unlock()

	n, err = e.FlushPending(context.Background())
	if n != 0 || err != nil {
		t.Fatalf("nil-store flush = (%d, %v), want (0, nil)", n, err)
	}
	e.mu.RLock()
	remaining := len(e.pending)
	count := e.pendingCount
	e.mu.RUnlock()
	if remaining != 0 || count != 0 {
		t.Fatalf("nil-store flush must drain the queue, got len=%d count=%d", remaining, count)
	}
}

// TestPropBaselineRegionCalendarStatsFromMemorySyntheticSeries:
// engine.RegionCalendarStats computes calendar stats from a synthetic
// multi-day series with no Postgres, and the stats cache serves the TTL window
// before recomputing.
func TestPropBaselineRegionCalendarStatsFromMemorySyntheticSeries(t *testing.T) {
	e := newPropBaselineEngine("", "")
	now := int64(1786224360) // fixed 21:26 UTC, slot 42
	slot := utcSlotOfDay(now)

	// wspr keys the region by the receiver end (SL): day -2: 5 spots,
	// day -1: 9 spots, today: 2 spots.
	for i := 0; i < 5; i++ {
		e.Observe(propSpot(now-2*86400, "wspr", "20m", "RX", "JO31ab", "TX", "FN31ab"))
	}
	for i := 0; i < 9; i++ {
		e.Observe(propSpot(now-86400, "wspr", "20m", "RX", "JO31ab", "TX", "FN31ab"))
	}
	for i := 0; i < 2; i++ {
		e.Observe(propSpot(now, "wspr", "20m", "RX", "JO31ab", "TX", "FN31ab"))
	}

	rows := e.RegionCalendarStats(context.Background(), 30, now)
	var row *propRegionCalendarStatRow
	for i := range rows {
		if rows[i].Band == "20m" && rows[i].Source == "wspr" && rows[i].Region == "EU" && rows[i].SlotOfDay == slot {
			row = &rows[i]
		}
	}
	if row == nil {
		t.Fatalf("no 20m/wspr/EU/slot=%d row in %+v", slot, rows)
	}
	if row.SampleDays != 3 {
		t.Fatalf("sample days = %d, want 3", row.SampleDays)
	}
	if row.Today != 2 {
		t.Fatalf("today = %d, want 2", row.Today)
	}
	wantMean := 16.0 / 3.0
	if row.Mean < wantMean-1e-6 || row.Mean > wantMean+1e-6 {
		t.Fatalf("mean = %v, want %v", row.Mean, wantMean)
	}
	if row.StdDev <= 0 {
		t.Fatalf("stddev = %v, want > 0 for the multi-day series", row.StdDev)
	}

	// Cache hit within the TTL: a newly seeded bucket must NOT show up.
	e.mu.Lock()
	e.buckets[propBaselineBucketKey("30m", "wspr", slot, "EU")] = &propBaselineBucket{
		Band: "30m", Source: "wspr", SlotOfDay: slot, Region: "EU",
		DayCounts: map[int64]int64{utcDayIndex(now): 1},
	}
	e.mu.Unlock()
	cached := e.RegionCalendarStats(context.Background(), 30, now+60)
	for _, r := range cached {
		if r.Band == "30m" {
			t.Fatalf("cache must serve within the TTL window: %+v", cached)
		}
	}

	// Past the TTL the cache is recomputed and the new bucket appears.
	fresh := e.RegionCalendarStats(context.Background(), 30, now+200)
	var has30m bool
	for _, r := range fresh {
		if r.Band == "30m" {
			has30m = true
		}
	}
	if !has30m {
		t.Fatalf("expired cache must recompute from memory: %+v", fresh)
	}
}

// TestPropBaselineRegionCalendarStatsNegativeCache: after a stats failure
// timestamp is recorded, the negative cache short-circuits (returns nil)
// instead of recomputing, until the negative-cache window expires.
func TestPropBaselineRegionCalendarStatsNegativeCache(t *testing.T) {
	e := newPropBaselineEngine("", "")
	now := int64(1786224360)
	slot := utcSlotOfDay(now)
	e.buckets[propBaselineBucketKey("20m", "wspr", slot, "EU")] = &propBaselineBucket{
		Band: "20m", Source: "wspr", SlotOfDay: slot, Region: "EU",
		DayCounts: map[int64]int64{utcDayIndex(now): 4},
	}
	// Simulate a prior Postgres failure without a prior success cache.
	e.statsCacheErrAt = now

	if rows := e.RegionCalendarStats(context.Background(), 30, now+1); rows != nil {
		t.Fatalf("negative cache must return nil within the window, got %+v", rows)
	}
	// Past the window the engine falls back to the in-memory buckets.
	rows := e.RegionCalendarStats(context.Background(), 30, now+propIntelRegionBaselineNegCacheTTL+1)
	if len(rows) == 0 {
		t.Fatalf("expired negative cache must recompute from memory, got nil")
	}
}

// TestPropBaselineFlushLoopGuards covers the guard arms of the timer-coupled
// loops without any real timer: nil engine, non-positive interval, and an
// already-closed stop channel (which makes the loops return before the first
// tick fires).
func TestPropBaselineFlushLoopGuards(t *testing.T) {
	var nilEngine *propBaselineEngine
	nilEngine.FlushPendingAsync(time.Minute, nil) // must not panic
	nilEngine.SaveLoop(time.Minute, nil)          // must not panic

	dir := t.TempDir()
	e := newPropBaselineEngine(dir+"/prop_baseline.json", "")
	e.Observe(propSpot(time.Now().Unix(), "wspr", "20m", "RX", "JO62qm", "TX", "FN31ab"))

	// Non-positive intervals return immediately.
	e.FlushPendingAsync(0, make(chan struct{}))
	e.SaveLoop(0, make(chan struct{}))

	// A pre-closed stop channel makes both loops exit before the first tick,
	// so the pending queue and the JSONL file are untouched.
	stop := make(chan struct{})
	close(stop)
	e.FlushPendingAsync(time.Hour, stop)
	e.SaveLoop(time.Hour, stop)

	e.mu.RLock()
	pending := len(e.pending)
	e.mu.RUnlock()
	if pending != 0 {
		t.Fatalf("no tick may run against a closed stop channel, pending=%d", pending)
	}
	if err := e.Save(); err != nil {
		t.Fatalf("Save after loop exit: %v", err)
	}
}
