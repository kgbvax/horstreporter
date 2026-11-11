package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func withRetention(t *testing.T, minutes int) {
	t.Helper()
	prev := liveHistoryRetentionMinutes
	liveHistoryRetentionMinutes = minutes
	t.Cleanup(func() { liveHistoryRetentionMinutes = prev })
}

func withCleanFeedLag(t *testing.T) {
	t.Helper()
	pskrFeedLag.reset()
	t.Cleanup(pskrFeedLag.reset)
}

// feedArrivals records n arrivals at second arrival with ages spread evenly
// over [minAge, maxAge] seconds.
func feedArrivals(f *feedLagTracker, arrival int64, n int, minAge, maxAge int64) {
	for i := 0; i < n; i++ {
		age := minAge
		if n > 1 {
			age = minAge + (maxAge-minAge)*int64(i)/int64(n-1)
		}
		f.observe(arrival-age, arrival)
	}
}

func TestFeedLagOnTimeFeedIsNotShifted(t *testing.T) {
	withRetention(t, 60)
	var f feedLagTracker
	now := int64(1_794_410_000)
	feedArrivals(&f, now-30, 500, 5, 240)
	feedArrivals(&f, now-5, 500, 5, 240)
	lag, ok := f.estimate(now)
	if !ok || lag > 60 {
		t.Fatalf("on-time feed: lag %d ok %v, want under a minute", lag, ok)
	}
	if got := clampFeedLag(lag, 15); got != 0 {
		t.Fatalf("on-time feed shifted by %d", got)
	}
}

// The 2026-11-11 shape: everything arriving 13 to 21 minutes old.
func TestFeedLagLateFeed(t *testing.T) {
	withRetention(t, 60)
	var f feedLagTracker
	now := int64(1_794_410_000)
	feedArrivals(&f, now-50, 1000, 13*60, 21*60)
	feedArrivals(&f, now-2, 1000, 13*60, 21*60)
	lag, ok := f.estimate(now)
	if !ok {
		t.Fatal("late feed: no estimate")
	}
	// The 10th percentile of ages 13..21 min, measured at now (up to 50 s
	// after the arrivals), floored to the 15 s bucket.
	if lag < 13*60 || lag > 14*60+30 {
		t.Fatalf("late feed: lag %d s, want about 13.8 min", lag)
	}
	if got := clampFeedLag(lag, 15); got != lag {
		t.Fatalf("15-minute window shifted %d, want %d", got, lag)
	}
}

// A few reporters with fast clocks (age 0) must not hide the lag.
func TestFeedLagIgnoresFastClocks(t *testing.T) {
	var f feedLagTracker
	now := int64(1_794_410_000)
	feedArrivals(&f, now-1, 1000, 13*60, 21*60)
	for i := 0; i < 40; i++ {
		f.observe(now+600, now-1) // reporter clock ten minutes fast
	}
	if lag, _ := f.estimate(now); lag < 13*60 {
		t.Fatalf("fast clocks pulled the lag to %d s", lag)
	}
}

func TestFeedLagWithoutArrivalsIsZero(t *testing.T) {
	var f feedLagTracker
	if lag, ok := f.estimate(1_794_410_000); lag != 0 || ok {
		t.Fatalf("empty tracker: lag %d ok %v", lag, ok)
	}
}

// A feed that stops keeps the window at its last data: the lag grows from the
// head the last full minute of arrivals put the feed at, however long no
// request asked (the head comes from the arrivals, not from the last call).
func TestFeedLagStallFallsBackToLastHead(t *testing.T) {
	withRetention(t, 60)
	var f feedLagTracker
	now := int64(1_794_410_000)
	feedArrivals(&f, now-1, 1000, 5*60, 6*60)
	head := now - 1 - 5*60
	for _, later := range []int64{now + 3*60, now + 10*60} {
		lag, ok := f.estimate(later)
		if ok || lag != later-head {
			t.Fatalf("stalled at %d: lag %d ok %v, want %d from the last head", later-now, lag, ok, later-head)
		}
	}
	if got := clampFeedLag(f.mustEstimate(now+40*60), 15); got != 0 {
		t.Fatalf("stall past the cap still shifted by %d", got)
	}
}

func (f *feedLagTracker) mustEstimate(now int64) int64 {
	lag, _ := f.estimate(now)
	return lag
}

// After a restart the window must not end before ingest resumed.
func TestEvalNowForStaysPastRestartGap(t *testing.T) {
	withRetention(t, 60)
	withCleanFeedLag(t)
	now := int64(1_794_410_000)
	prevEnd, prevSince := liveHistoryGapEnd.Load(), liveHistoryCompleteSince.Load()
	t.Cleanup(func() { liveHistoryGapEnd.Store(prevEnd); liveHistoryCompleteSince.Store(prevSince) })
	feedArrivals(&pskrFeedLag, now-1, 1000, 18*60, 22*60)

	liveHistoryGapEnd.Store(now - 60) // restarted a minute ago
	if ev, lag, raw := evalNowFor(now, 15); ev != now || lag != 0 || raw < 18*60 {
		t.Fatalf("1 min after restart: evalNow %d lag %d raw %d", ev, lag, raw)
	}
	liveHistoryGapEnd.Store(now - 10*60)
	if ev, lag, _ := evalNowFor(now, 15); lag != 10*60 || ev != now-10*60 {
		t.Fatalf("10 min after restart: evalNow %d lag %d, want the gap end", ev, lag)
	}
	liveHistoryGapEnd.Store(0)
	liveHistoryCompleteSince.Store(now - 60)
	if _, lag, _ := evalNowFor(now, 15); lag != 0 {
		t.Fatalf("history complete only since a minute ago: lag %d", lag)
	}
}

func TestEvalNowForIgnoresBrokenFeed(t *testing.T) {
	withRetention(t, 60)
	withCleanFeedLag(t)
	now := int64(1_794_410_000)
	feedArrivals(&pskrFeedLag, now-1, 1000, 40*60, 45*60)
	if ev, lag, raw := evalNowFor(now, 15); ev != now || lag != 0 || raw != 0 {
		t.Fatalf("40 min behind: evalNow %d lag %d raw %d, want no shift and no note", ev, lag, raw)
	}
}

func TestClampFeedLagKeepsWindowInsideRetention(t *testing.T) {
	withRetention(t, 60)
	cases := []struct {
		lag     int64
		minutes int
		want    int64
	}{
		{60, 15, 0}, // under the apply threshold
		{13 * 60, 15, 13 * 60},
		{13 * 60, 30, 13 * 60},
		{13 * 60, 50, 9 * 60}, // only 10 min of room minus the margin
		{13 * 60, 60, 0},      // no room at all
		{31 * 60, 15, 0},      // past the cap: broken, not late
	}
	for _, c := range cases {
		if got := clampFeedLag(c.lag, c.minutes); got != c.want {
			t.Errorf("clampFeedLag(%d, %d) = %d, want %d", c.lag, c.minutes, got, c.want)
		}
	}
}

func TestEvalNowForUsesGlobalTracker(t *testing.T) {
	withRetention(t, 60)
	withCleanFeedLag(t)
	now := int64(1_794_410_000)
	if ev, lag, raw := evalNowFor(now, 15); ev != now || lag != 0 || raw != 0 {
		t.Fatalf("empty tracker: %d %d", ev, lag)
	}
	feedArrivals(&pskrFeedLag, now-1, 1000, 13*60, 21*60)
	ev, lag, raw := evalNowFor(now, 15)
	if lag < 13*60 || ev != now-lag || raw != lag {
		t.Fatalf("late feed: evalNow %d lag %d raw %d", ev, lag, raw)
	}
	// A 60-minute window cannot move; the raw lag still reports the delay.
	if ev, lag, raw := evalNowFor(now, 60); ev != now || lag != 0 || raw < 13*60 {
		t.Fatalf("60-minute window: evalNow %d lag %d raw %d", ev, lag, raw)
	}
}

func TestFeedLagObserveDoesNotAllocate(t *testing.T) {
	var f feedLagTracker
	now := int64(1_794_410_000)
	if n := testing.AllocsPerRun(1000, func() { f.observe(now-800, now) }); n != 0 {
		t.Fatalf("observe allocates %v per call", n)
	}
}

// TestPropIntelV2HandlerFollowsLateFeed: with the feed 20 minutes late the
// 15-minute window ends at the feed head, so reports from 20 minutes ago are
// "now"; the trend stays on the wall clock.
func TestPropIntelV2HandlerFollowsLateFeed(t *testing.T) {
	withRetention(t, 60)
	withCleanFeedLag(t)
	now := time.Now().Unix()
	today := utcDayIndex(now)
	withFromHereSource(t, &fakeFromHereSource{rows: steadyRows("20m", "NA", 30, today-40, today), cov: fullCoverage(today-40, today)})
	withFromHereDaySource(t, nil)

	const late = 20 * 60
	hub.Lock()
	orig := hub.history
	// PSKReporter (and one WSPR) reports from 20 minutes ago, plus a WSPR
	// report from now: WSPR is not late and keeps the wall-clock window.
	hub.history = append(fromHereFixture(now-late),
		MQTTMessage{Source: "wspr", T: now - 30, B: "20m", MD: "WSPR", SC: "RX", SL: "JO32CD", RC: "TX", RL: "FN31AB", RP: -10, TXPower: 37})
	hub.Unlock()
	defer func() {
		hub.Lock()
		hub.history = orig
		hub.Unlock()
	}()

	server := httptest.NewServer(http.HandlerFunc(propIntelV2Handler))
	defer server.Close()
	get := func() propIntelV2Response {
		t.Helper()
		resp, err := http.Get(server.URL + "/api/prop_intel/v2?qth=JO32&surroundings=true&from_here=true&sources=wspr,pskr&trend=1")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var decoded propIntelV2Response
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			t.Fatal(err)
		}
		return decoded
	}

	onTime := get()
	if onTime.FeedLagSeconds != 0 || sourceCount(findV2Cell(t, onTime, "20m", "NA"), "pskr") != 0 {
		t.Fatalf("no lag measured: lag %d, cells %+v", onTime.FeedLagSeconds, onTime.Cells)
	}

	feedArrivals(&pskrFeedLag, time.Now().Unix(), 500, late, late+5*60)
	shifted := get()
	if shifted.FeedLagSeconds < late-30 || shifted.FeedLagSeconds > late+60 {
		t.Fatalf("feed_lag_seconds = %d, want about %d", shifted.FeedLagSeconds, late)
	}
	if shifted.Now > now-late+60 {
		t.Fatalf("now = %d, want the feed head near %d", shifted.Now, now-late)
	}
	na := findV2Cell(t, shifted, "20m", "NA")
	if sourceCount(na, "pskr") == 0 {
		t.Fatalf("late PSKReporter reports not counted: %+v", na)
	}
	if got := sourceCount(na, "wspr"); got != 1 {
		t.Fatalf("WSPR keeps the wall-clock window: %d WSPR spots, want only the current one", got)
	}
	if shifted.TrendNow < now {
		t.Fatalf("trend_now = %d, want the wall clock", shifted.TrendNow)
	}
	if len(na.Trend) != 12 || na.Trend[11] != 0 || na.Trend[7] == 0 {
		t.Fatalf("trend should stay on the wall clock: %v", na.Trend)
	}
}

func sourceCount(c propIntelV2Cell, source string) int {
	for _, s := range c.PerSource {
		if s.Source == source {
			return s.SpotCount
		}
	}
	return 0
}
