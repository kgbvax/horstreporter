package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func propRowsOf(mean float64) []propRegionCalendarStatRow {
	return []propRegionCalendarStatRow{{Band: "20m", Source: "wspr", Region: "EU", SlotOfDay: 1, Mean: mean, SampleDays: 5}}
}

// A cold load queries Postgres without holding the engine lock: Observe runs
// under the hub write lock in production and takes that lock, so a query that
// held it stalled ingest and every history reader.
func TestPropClimatologyColdLoadDoesNotHoldTheEngineLock(t *testing.T) {
	e := newPropBaselineEngine("", "")
	e.store = &dxPostgresStore{}
	started, release := make(chan struct{}), make(chan struct{})
	e.queryStats = func(ctx context.Context, _ *dxPostgresStore, _ int, _ int64) ([]propRegionCalendarStatRow, error) {
		close(started)
		<-release
		return propRowsOf(1), nil
	}
	now := time.Now().Unix()
	done := make(chan []propRegionCalendarStatRow)
	go func() { done <- e.RegionCalendarStats(context.Background(), 30, now) }()
	<-started

	observed := make(chan struct{})
	go func() {
		e.Observe(MQTTMessage{T: now, B: "20m", MD: "FT8", SC: "DK3JF", RC: "W1AW", SL: "JO32aa", RL: "FN20bb", RP: -10})
		close(observed)
	}()
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("Observe blocked while the climatology query was running")
	}
	close(release)
	if rows := <-done; len(rows) != 1 || rows[0].Mean != 1 {
		t.Fatalf("cold load returned %v", rows)
	}
}

func TestPropClimatologyServesStaleCopyWhileRefreshing(t *testing.T) {
	e := newPropBaselineEngine("", "")
	e.store = &dxPostgresStore{}
	var calls atomic.Int64
	release := make(chan struct{})
	e.queryStats = func(ctx context.Context, _ *dxPostgresStore, _ int, _ int64) ([]propRegionCalendarStatRow, error) {
		n := calls.Add(1)
		if n == 1 {
			return propRowsOf(1), nil
		}
		<-release
		return propRowsOf(float64(n)), nil
	}
	now := time.Now().Unix()
	if rows := e.RegionCalendarStats(context.Background(), 30, now); rows[0].Mean != 1 {
		t.Fatalf("cold load: %v", rows)
	}
	// Expired: the stale copy comes back at once, a refresh starts, a second
	// expired call does not start another.
	late := now + e.statsCacheTTL + 1
	if rows := e.RegionCalendarStats(context.Background(), 30, late); rows[0].Mean != 1 {
		t.Fatalf("expected the stale copy while refreshing, got %v", rows)
	}
	waitFor(t, "refresh to start", func() bool { return calls.Load() == 2 })
	if rows := e.RegionCalendarStats(context.Background(), 30, late+1); rows[0].Mean != 1 {
		t.Fatalf("still stale while the refresh runs, got %v", rows)
	}
	if calls.Load() != 2 {
		t.Fatalf("%d queries: a second refresh started while one was in flight", calls.Load())
	}
	close(release)
	waitFor(t, "the refreshed copy", func() bool {
		return e.RegionCalendarStats(context.Background(), 30, late+2)[0].Mean == 2
	})
}

func TestPropClimatologyFailedRefreshKeepsCopyAndBacksOff(t *testing.T) {
	e := newPropBaselineEngine("", "")
	e.store = &dxPostgresStore{}
	var calls atomic.Int64
	fail := atomic.Bool{}
	e.queryStats = func(ctx context.Context, _ *dxPostgresStore, _ int, _ int64) ([]propRegionCalendarStatRow, error) {
		calls.Add(1)
		if fail.Load() {
			return nil, errors.New("pg down")
		}
		return propRowsOf(7), nil
	}
	now := time.Now().Unix()
	e.RegionCalendarStats(context.Background(), 30, now)
	fail.Store(true)
	late := now + e.statsCacheTTL + 1
	e.RegionCalendarStats(context.Background(), 30, late)
	waitFor(t, "the failed refresh", func() bool { e.mu.RLock(); defer e.mu.RUnlock(); return e.statsCacheErrAt == late })
	// Inside the back-off the stale copy is served and Postgres is left alone.
	before := calls.Load()
	if rows := e.RegionCalendarStats(context.Background(), 30, late+1); rows[0].Mean != 7 {
		t.Fatalf("lost the stale copy: %v", rows)
	}
	time.Sleep(30 * time.Millisecond)
	if calls.Load() != before {
		t.Fatalf("retried inside the back-off window (%d -> %d queries)", before, calls.Load())
	}
	// After it, a refresh is attempted again.
	fail.Store(false)
	e.RegionCalendarStats(context.Background(), 30, late+propIntelRegionBaselineNegCacheTTL+1)
	waitFor(t, "the retry", func() bool { return calls.Load() == before+1 })
}

func TestWsprClimatologyServesStaleCopyAndDoesNotHoldTheEngineLock(t *testing.T) {
	e := newWsprClimatologyEngine("")
	e.store = &dxPostgresStore{}
	var calls atomic.Int64
	started, release := make(chan struct{}), make(chan struct{})
	rows := func(mean float64) []wsprRegionCalendarStatRow {
		return []wsprRegionCalendarStatRow{{Band: "20m", Region: "EU", SlotOfDay: 1, Mean: mean, SampleDays: 5}}
	}
	e.queryStats = func(ctx context.Context, _ *dxPostgresStore, _ int, _ int64) ([]wsprRegionCalendarStatRow, error) {
		n := calls.Add(1)
		if n == 1 {
			return rows(1), nil
		}
		if n == 2 {
			close(started)
		}
		<-release
		return rows(float64(n)), nil
	}
	now := time.Now().Unix()
	if got := e.RegionCalendarStats(context.Background(), 30, now); got[0].Mean != 1 {
		t.Fatalf("cold load: %v", got)
	}
	late := now + e.statsCacheTTL + 1
	if got := e.RegionCalendarStats(context.Background(), 30, late); got[0].Mean != 1 {
		t.Fatalf("expected the stale copy, got %v", got)
	}
	<-started
	observed := make(chan struct{})
	go func() {
		e.Observe(MQTTMessage{T: now, B: "20m", MD: "WSPR", Source: "wspr", SC: "RX", RC: "TX", SL: "JO32aa", RL: "FN20bb", RP: -10})
		close(observed)
	}()
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("Observe blocked while the WSPR climatology refresh was running")
	}
	close(release)
	waitFor(t, "the refreshed copy", func() bool {
		return e.RegionCalendarStats(context.Background(), 30, late+1)[0].Mean == 2
	})
}

func ft8Engine(t *testing.T) *propIntelEngine {
	t.Helper()
	base := newDxBaselineEngine(t.TempDir() + "/b.json")
	base.store = &dxPostgresStore{}
	return &propIntelEngine{baseline: base}
}

func ft8Rows(mean float64) []regionCalendarStatRow {
	return []regionCalendarStatRow{{Band: "20m", Region: "EU", SlotOfDay: 3, Mean: mean, SampleDays: 10}}
}

// The FT8 climatology takes seconds on prod (8-19 s): a request must never
// wait on it, and before the first load lands the answer is "no data".
func TestFT8BaselinesNeverBlockRequestsAndSwapInWhenLoaded(t *testing.T) {
	e := ft8Engine(t)
	release := make(chan struct{})
	var calls atomic.Int64
	e.queryFT8 = func(ctx context.Context, _ *dxPostgresStore, _ int, _ int64) ([]regionCalendarStatRow, error) {
		calls.Add(1)
		<-release
		return ft8Rows(4), nil
	}
	now := time.Now().Unix()

	done := make(chan map[regionBaselineKey]regionCalendarStatRow, 1)
	go func() { done <- e.loadFT8Baselines(now) }()
	select {
	case got := <-done:
		if got != nil {
			t.Fatalf("cold call returned %v, want nil", got)
		}
	case <-time.After(time.Second):
		t.Fatal("loadFT8Baselines waited for the Postgres query")
	}
	// Further requests while it runs neither block nor start a second query.
	e.loadFT8Baselines(now + 1)
	waitFor(t, "the refresh to start", func() bool { return calls.Load() == 1 })
	if calls.Load() != 1 {
		t.Fatalf("%d queries in flight", calls.Load())
	}
	close(release)
	waitFor(t, "the first load", func() bool { return e.loadFT8Baselines(now+2) != nil })
	got := e.loadFT8Baselines(now + 3)
	if row, ok := got[regionBaselineKey{"20m", "EU", 3}]; !ok || row.Mean != 4 {
		t.Fatalf("loaded climatology = %v", got)
	}
}

func TestFT8BaselinesStaleCopyServedWhileRefreshingAndFailureBacksOff(t *testing.T) {
	e := ft8Engine(t)
	var calls atomic.Int64
	fail := atomic.Bool{}
	release := make(chan struct{})
	e.queryFT8 = func(ctx context.Context, _ *dxPostgresStore, _ int, _ int64) ([]regionCalendarStatRow, error) {
		n := calls.Add(1)
		if n == 1 {
			return ft8Rows(1), nil
		}
		<-release
		if fail.Load() {
			return nil, errors.New("pg down")
		}
		return ft8Rows(float64(n)), nil
	}
	now := time.Now().Unix()
	e.loadFT8Baselines(now)
	waitFor(t, "the first load", func() bool { return e.loadFT8Baselines(now+1) != nil })

	// Inside the TTL: no query. After it: the stale copy comes back at once and
	// one refresh starts.
	if calls.Load() != 1 {
		t.Fatalf("%d queries inside the TTL", calls.Load())
	}
	late := now + propIntelFT8BaselineTTL + 1
	fail.Store(true)
	if got := e.loadFT8Baselines(late); got[regionBaselineKey{"20m", "EU", 3}].Mean != 1 {
		t.Fatalf("expected the stale copy, got %v", got)
	}
	waitFor(t, "the refresh to start", func() bool { return calls.Load() == 2 })
	close(release)
	waitFor(t, "the failed refresh", func() bool {
		e.ft8CacheMu.RLock()
		defer e.ft8CacheMu.RUnlock()
		return e.ft8CacheErrAt == late && !e.ft8Refreshing
	})
	// The previous copy survives the failure, and Postgres is left alone until
	// the retry window has passed.
	if got := e.loadFT8Baselines(late + 1); got[regionBaselineKey{"20m", "EU", 3}].Mean != 1 {
		t.Fatalf("lost the copy after a failed refresh: %v", got)
	}
	time.Sleep(30 * time.Millisecond)
	if calls.Load() != 2 {
		t.Fatalf("retried inside the back-off (%d queries)", calls.Load())
	}
	fail.Store(false)
	e.loadFT8Baselines(late + propIntelFT8RetryAfter + 1)
	waitFor(t, "the retry", func() bool { return calls.Load() == 3 })
}

func TestFT8BaselinesWithoutStoreStayNil(t *testing.T) {
	e := &propIntelEngine{baseline: newDxBaselineEngine(t.TempDir() + "/b.json")}
	if got := e.loadFT8Baselines(time.Now().Unix()); got != nil {
		t.Fatalf("no store: %v", got)
	}
}
