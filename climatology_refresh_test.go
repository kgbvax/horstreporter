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
