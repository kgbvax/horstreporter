package main

import (
	"context"
	"testing"
	"time"
)

func TestWsprClimatologyObserve(t *testing.T) {
	// Covers F1: a WSPR spot with remote locator in EU on 20m at UTC 10:00
	// increments the (20m, EU, slot=20) bucket.
	e := newWsprClimatologyEngine("")
	now := int64(1786224360) // 2026-08-08 21:26:00 UTC → slot 42

	// Receiver in EU (JO31), sender in NA (FN31).
	m := MQTTMessage{
		RP:      5,
		T:       now,
		SC:      "DL1ABC",
		SL:      "JO31",
		RC:      "KF5XYZ",
		RL:      "FN31",
		B:       "20m",
		MD:      "WSPR",
		Source:  "wspr",
		TXPower: 20000,
	}
	e.Observe(m)

	slot := utcSlotOfDay(now)
	key := wsprClimatologyKey("20m", slot, "EU")
	b, ok := e.buckets[key]
	if !ok {
		t.Fatalf("expected bucket for 20m/EU/slot=%d, got none", slot)
	}
	if b.Count != 1 {
		t.Errorf("bucket Count = %d, want 1", b.Count)
	}

	// Verify no NA bucket (we key by receiver region only).
	naKey := wsprClimatologyKey("20m", slot, "NA")
	if _, ok := e.buckets[naKey]; ok {
		t.Error("did not expect a NA bucket (receiver region only)")
	}
}

func TestWsprClimatologyRegionResolution(t *testing.T) {
	// A spot with remote locator FN31 (NA) and another with JO62 (EU) on
	// the same band/slot increment different buckets.
	e := newWsprClimatologyEngine("")
	now := int64(1786224360)
	slot := utcSlotOfDay(now)

	// NA spot: receiver in FN31, sender in JO31.
	e.Observe(MQTTMessage{
		RP: 3, T: now, SC: "DL1ABC", SL: "JO31", RC: "KF5XYZ", RL: "FN31",
		B: "20m", MD: "WSPR", Source: "wspr", TXPower: 10000,
	})
	// EU spot: receiver in JO62, sender in FN31.
	e.Observe(MQTTMessage{
		RP: 5, T: now, SC: "DL1ABC", SL: "FN31", RC: "SM0ABC", RL: "JO62",
		B: "20m", MD: "WSPR", Source: "wspr", TXPower: 5000,
	})

	naBucket := e.buckets[wsprClimatologyKey("20m", slot, "NA")]
	if naBucket == nil || naBucket.Count != 1 {
		t.Errorf("NA bucket count = %v, want 1", naBucket)
	}
	euBucket := e.buckets[wsprClimatologyKey("20m", slot, "EU")]
	if euBucket == nil || euBucket.Count != 1 {
		t.Errorf("EU bucket count = %v, want 1", euBucket)
	}
}

func TestWsprClimatologyUnknownRegion(t *testing.T) {
	// A spot with an invalid remote locator does not increment any bucket.
	e := newWsprClimatologyEngine("")
	now := int64(1786224360)

	e.Observe(MQTTMessage{
		RP: 3, T: now, SC: "DL1ABC", SL: "INVALID", RC: "KF5XYZ", RL: "ALSOBAD",
		B: "20m", MD: "WSPR", Source: "wspr", TXPower: 10000,
	})
	if len(e.buckets) != 0 {
		t.Errorf("expected 0 buckets for invalid locators, got %d", len(e.buckets))
	}
}

func TestWsprClimatologyInMemoryStats(t *testing.T) {
	// The in-memory fallback computes mean from aggregated count / span.
	e := newWsprClimatologyEngine("")

	// Seed 10 spots over 2 hours, all in the same (20m, EU, slot=42) bucket.
	now := int64(1786224360)
	for i := 0; i < 10; i++ {
		e.Observe(MQTTMessage{
			RP: 5, T: now + int64(i*60), SC: "DL1ABC", SL: "JO31",
			RC: "KF5XYZ", RL: "JO62", B: "20m", MD: "WSPR",
			Source: "wspr", TXPower: 20000,
		})
	}

	rows := e.regionCalendarStatsFromMemory(now)
	if len(rows) == 0 {
		t.Fatal("expected at least 1 stats row, got 0")
	}
	var found bool
	for _, r := range rows {
		if r.Band == "20m" && r.Region == "EU" && r.SlotOfDay == utcSlotOfDay(now) {
			if r.Mean <= 0 {
				t.Errorf("mean = %f, want > 0", r.Mean)
			}
			// StdDev is 0 when all spots fall on a single day (1 sample,
			// no variance). With multiple days of data, StdDev > 0.
			if r.StdDev != 0 {
				t.Errorf("in-memory stddev = %f, want 0 (single-day test data)", r.StdDev)
			}
			found = true
		}
	}
	if !found {
		t.Error("did not find expected 20m/EU/slot row in stats")
	}
}

func TestWsprClimatologyJSONLRoundTrip(t *testing.T) {
	// Save() then Load() preserves bucket counts.
	dir := t.TempDir()
	path := dir + "/wspr_climatology.json"
	e := newWsprClimatologyEngine(path)
	now := int64(1786224360)

	e.Observe(MQTTMessage{
		RP: 5, T: now, SC: "DL1ABC", SL: "JO31", RC: "KF5XYZ", RL: "JO62",
		B: "20m", MD: "WSPR", Source: "wspr", TXPower: 20000,
	})
	e.Observe(MQTTMessage{
		RP: 3, T: now, SC: "K1ABC", SL: "FN31", RC: "DL1DEF", RL: "JO62",
		B: "20m", MD: "WSPR", Source: "wspr", TXPower: 10000,
	})

	if err := e.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	e2 := newWsprClimatologyEngine(path)
	if err := e2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	slot := utcSlotOfDay(now)
	euBucket := e2.buckets[wsprClimatologyKey("20m", slot, "EU")]
	if euBucket == nil || euBucket.Count != 1 {
		t.Errorf("EU bucket after round-trip = %v, want Count=1", euBucket)
	}
	naBucket := e2.buckets[wsprClimatologyKey("20m", slot, "NA")]
	if naBucket == nil || naBucket.Count != 1 {
		t.Errorf("NA bucket after round-trip = %v, want Count=1", naBucket)
	}
}

func TestWsprClimatologyColdStart(t *testing.T) {
	// Covers AE8: a freshly loaded climatology with 5 days of depth returns
	// SampleDays reflecting the span. (The in-memory fallback uses the span,
	// not actual per-day counts.)
	e := newWsprClimatologyEngine("")
	// Simulate 5 days of events by setting firstEventAt 5 days ago.
	now := int64(1786224360)
	e.firstEventAt = now - 5*86400
	e.lastEventAt = now
	// Seed one bucket with per-day counts spanning 5 days.
	slot := utcSlotOfDay(now)
	today := utcDayIndex(now)
	startDay := today - 5
	dayCounts := make(map[int64]int64)
	for d := startDay; d <= today; d++ {
		dayCounts[d] = int64(2 + (d % 3)) // varying counts: 2, 3, 4, 2, 3, 4
	}
	e.buckets[wsprClimatologyKey("20m", slot, "JA")] = &wsprClimatologyBucket{
		Band: "20m", SlotOfDay: slot, Region: "JA", Count: 12, DayCounts: dayCounts,
	}

	rows := e.regionCalendarStatsFromMemory(now)
	var found bool
	for _, r := range rows {
		if r.Band == "20m" && r.Region == "JA" && r.SlotOfDay == slot {
			if r.SampleDays < 5 {
				t.Errorf("SampleDays = %d, want >= 5", r.SampleDays)
			}
			if r.StdDev <= 0 {
				t.Errorf("StdDev = %f, want > 0 (per-day counts available)", r.StdDev)
			}
			found = true
		}
	}
	if !found {
		t.Error("did not find 20m/JA/slot row in cold-start stats")
	}
}
// TestWsprRegionCapPendingOverflow: capPendingWsprRegion drops the oldest day
// index group when the pending queue overflows (data loss preferred over OOM).
func TestWsprRegionCapPendingOverflow(t *testing.T) {
	pending := make(map[wsprRegionBaselineKey]int64)
	// Two day groups, 60 keys each (over a tiny cap of 60).
	for i := 0; i < 60; i++ {
		pending[wsprRegionBaselineKey{Band: "20m", SlotOfDay: i, Region: "EU", DayIndex: 100}] = 1
	}
	for i := 0; i < 60; i++ {
		pending[wsprRegionBaselineKey{Band: "20m", SlotOfDay: i, Region: "NA", DayIndex: 200}] = 1
	}

	// Under the cap: identity, no trimming.
	same, n := capPendingWsprRegion(pending, 120)
	if n != 120 || len(same) != 120 {
		t.Fatalf("under-cap must be a no-op, got n=%d len=%d", n, len(same))
	}

	out, n := capPendingWsprRegion(pending, 60)
	if n > 60 || len(out) > 60 {
		t.Fatalf("cap not enforced: n=%d len=%d", n, len(out))
	}
	// The oldest day (100) must be dropped entirely, the newer day kept.
	for k := range out {
		if k.DayIndex == 100 {
			t.Fatalf("oldest day index not dropped: %+v", k)
		}
		if k.DayIndex != 200 {
			t.Fatalf("unexpected surviving day index %d", k.DayIndex)
		}
	}
}

// TestWsprClimatologyStatsAndSetStore covers Stats and SetStore (nil and
// real receivers) surfaced via /api/stats.
func TestWsprClimatologyStatsAndSetStore(t *testing.T) {
	// Nil receiver never panics.
	var nilEngine *WsprClimatologyEngine
	if c, m := nilEngine.Stats(time.Now().Unix()); c != 0 || m != 0 {
		t.Fatalf("nil engine stats = (%d, %d), want (0, 0)", c, m)
	}
	nilEngine.SetStore(nil) // must not panic

	e := newWsprClimatologyEngine("")
	if c, m := e.Stats(time.Now().Unix()); c != 0 || m != 0 {
		t.Fatalf("empty engine stats = (%d, %d), want (0, 0)", c, m)
	}
	now := int64(1786224360)
	e.Observe(MQTTMessage{
		RP: 5, T: now, SC: "DL1ABC", SL: "JO31", RC: "KF5XYZ", RL: "JO62",
		B: "20m", MD: "WSPR", Source: "wspr", TXPower: 20000,
	})
	e.Observe(MQTTMessage{
		RP: 5, T: now + 180, SC: "DL1ABC", SL: "JO31", RC: "KF5XYZ", RL: "JO62",
		B: "20m", MD: "WSPR", Source: "wspr", TXPower: 20000,
	})
	c, m := e.Stats(now + 180)
	if c != 1 {
		t.Fatalf("bucket count = %d, want 1 (both spots share the slot)", c)
	}
	// Span is (last-first)/60 = 3 minutes.
	if m < 2 || m > 4 {
		t.Fatalf("history minutes = %d, want ~3", m)
	}

	// SetStore on a real engine wires the store in.
	st := &dxPostgresStore{}
	e.SetStore(st)
	if e.store != st {
		t.Fatalf("SetStore did not wire the store")
	}
}

// TestWsprClimatologyFlushPendingNilStoreDrains: with no Postgres store wired,
// FlushPending drains the synthetic pending queue and reports zero flushed
// keys; the store-method nil guards are exercised too.
func TestWsprClimatologyFlushPendingNilStoreDrains(t *testing.T) {
	var s *dxPostgresStore
	if err := s.FlushPendingWsprRegion(context.Background(), nil); err != nil {
		t.Fatalf("nil store flush must be a no-op, got %v", err)
	}
	if err := s.ensureWsprRegionBaseline(context.Background()); err != nil {
		t.Fatalf("nil store ensure must be a no-op, got %v", err)
	}
	if rows, err := s.WsprRegionCalendarStats(context.Background(), 30, time.Now().Unix()); rows != nil || err != nil {
		t.Fatalf("nil store stats = (%v, %v), want (nil, nil)", rows, err)
	}

	e := newWsprClimatologyEngine("")
	n, err := e.FlushPending(context.Background())
	if n != 0 || err != nil {
		t.Fatalf("empty pending flush = (%d, %v), want (0, nil)", n, err)
	}

	// Synthetic pending queue (Observe only enqueues when a store is wired).
	day := utcDayIndex(time.Now().Unix())
	slot := utcSlotOfDay(time.Now().Unix())
	e.mu.Lock()
	e.pendingWsprRegion[wsprRegionBaselineKey{Band: "20m", SlotOfDay: slot, Region: "EU", DayIndex: day}] = 3
	e.pendingWsprCount = 3
	e.mu.Unlock()

	n, err = e.FlushPending(context.Background())
	if n != 0 || err != nil {
		t.Fatalf("nil-store flush = (%d, %v), want (0, nil)", n, err)
	}
	e.mu.RLock()
	remaining := len(e.pendingWsprRegion)
	count := e.pendingWsprCount
	e.mu.RUnlock()
	if remaining != 0 || count != 0 {
		t.Fatalf("nil-store flush must drain the queue, got len=%d count=%d", remaining, count)
	}
}

// TestWsprRegionCalendarStatsFromMemorySyntheticSeries: engine.RegionCalendarStats
// computes calendar stats from a synthetic multi-day series with no Postgres,
// and the stats cache serves the TTL window before recomputing.
func TestWsprRegionCalendarStatsFromMemorySyntheticSeries(t *testing.T) {
	e := newWsprClimatologyEngine("")
	now := int64(1786224360)
	slot := utcSlotOfDay(now)
	today := utcDayIndex(now)

	// Day -2: 5 spots, day -1: 9 spots, today: 2 spots.
	// Sample stddev of (5, 9, 2): mean 16/3 ≈ 5.333, stddev ≈ 3.512.
	for i := 0; i < 5; i++ {
		e.Observe(MQTTMessage{RP: 5, T: now - 2*86400, SC: "DL1ABC", SL: "JO31",
			RC: "KF5XYZ", RL: "JO62", B: "20m", MD: "WSPR", Source: "wspr"})
	}
	for i := 0; i < 9; i++ {
		e.Observe(MQTTMessage{RP: 5, T: now - 86400, SC: "DL1ABC", SL: "JO31",
			RC: "KF5XYZ", RL: "JO62", B: "20m", MD: "WSPR", Source: "wspr"})
	}
	for i := 0; i < 2; i++ {
		e.Observe(MQTTMessage{RP: 5, T: now, SC: "DL1ABC", SL: "JO31",
			RC: "KF5XYZ", RL: "JO62", B: "20m", MD: "WSPR", Source: "wspr"})
	}
	// Force slot drift safety: all spots share one slot by construction above
	// (now-2d and now-1d land in the same slot for the fixed timestamp).

	rows := e.RegionCalendarStats(context.Background(), 30, now)
	var row *wsprRegionCalendarStatRow
	for i := range rows {
		if rows[i].Band == "20m" && rows[i].Region == "EU" && rows[i].SlotOfDay == slot {
			row = &rows[i]
		}
	}
	if row == nil {
		t.Fatalf("no 20m/EU/slot=%d row in %+v", slot, rows)
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

	// Cache hit within the TTL: a newly observed bucket must NOT show up.
	e.mu.Lock()
	e.buckets[wsprClimatologyKey("30m", slot, "EU")] = &wsprClimatologyBucket{
		Band: "30m", SlotOfDay: slot, Region: "EU", DayCounts: map[int64]int64{today: 1},
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

// TestWsprRegionCalendarStatsNegativeCache: after a stats failure timestamp is
// recorded, the negative cache short-circuits (returns nil) instead of
// recomputing, until the negative-cache window expires.
func TestWsprRegionCalendarStatsNegativeCache(t *testing.T) {
	e := newWsprClimatologyEngine("")
	now := int64(1786224360)
	slot := utcSlotOfDay(now)
	e.buckets[wsprClimatologyKey("20m", slot, "EU")] = &wsprClimatologyBucket{
		Band: "20m", SlotOfDay: slot, Region: "EU", DayCounts: map[int64]int64{utcDayIndex(now): 4},
	}
	// Simulate a prior Postgres failure without a prior success cache.
	e.statsCacheErrAt = now

	if rows := e.RegionCalendarStats(context.Background(), 30, now + 1); rows != nil {
		t.Fatalf("negative cache must return nil within the window, got %+v", rows)
	}
	// Past the window the engine falls back to the in-memory buckets.
	rows := e.RegionCalendarStats(context.Background(), 30, now + propIntelRegionBaselineNegCacheTTL + 1)
	if len(rows) == 0 {
		t.Fatalf("expired negative cache must recompute from memory, got nil")
	}
}

// TestWsprClimatologyFlushLoopGuards covers the guard arms of the timer-coupled
// loops without any real timer: nil engine, non-positive interval, and an
// already-closed stop channel (which makes the loops return before the first
// tick fires).
func TestWsprClimatologyFlushLoopGuards(t *testing.T) {
	var nilEngine *WsprClimatologyEngine
	nilEngine.FlushPendingAsync(time.Minute, nil) // must not panic
	nilEngine.SaveLoop(time.Minute, nil)          // must not panic

	dir := t.TempDir()
	e := newWsprClimatologyEngine(dir + "/wspr_climatology.json")
	e.Observe(MQTTMessage{RP: 5, T: time.Now().Unix(), SC: "DL1ABC", SL: "JO31",
		RC: "KF5XYZ", RL: "JO62", B: "20m", MD: "WSPR", Source: "wspr"})

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
	pending := len(e.pendingWsprRegion)
	e.mu.RUnlock()
	if pending != 0 {
		t.Fatalf("no tick may run against a closed stop channel, pending=%d", pending)
	}
	if err := e.Save(); err != nil {
		t.Fatalf("Save after loop exit: %v", err)
	}
}
