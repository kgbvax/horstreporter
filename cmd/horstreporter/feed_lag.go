package main

import "sync"

// PSKReporter feed lag.
//
// The PSKReporter MQTT feed is normally a minute or two behind the reports it
// carries, but at times it runs much later (13 to 21 minutes on 2026-11-11).
// Every "now" figure compares the reports in [now - window, now] with the
// normal for a full window, so a late feed leaves the newest minutes empty and
// every band and region reads far below normal while the earlier minutes show
// it busy (the 15-minute region cells all under ×1, the 30- and 60-minute band
// ratio at ×3 for the same band).
//
// The tracker estimates how far behind the feed is from what arrives: per
// arrival minute, a histogram of the arrivals' age (arrival time minus the
// report's own timestamp). The lag is the age at the 10th percentile, the young
// edge of what is arriving now. A low percentile instead of the newest report
// keeps a reporter whose clock runs fast from hiding the lag, and it is the
// edge where the data behind it is mostly in.
//
// The PSKReporter "now" evaluations (dx_conditions, hot_bands, prop_intel v2)
// then end their window at now - lag instead of the wall clock (evalNowFor).

const (
	feedLagBucketSec = 15
	// feedLagBuckets covers ages up to one hour; older arrivals share the last
	// bucket.
	feedLagBuckets = 3600 / feedLagBucketSec
	// feedLagQuantile is the share of arrivals younger than the lag.
	feedLagQuantile = 0.10
	// feedLagMinSamples is how many arrivals in the last two minutes an
	// estimate needs. The feed carries hundreds per second; fewer means it is
	// stalled or the process is just starting.
	feedLagMinSamples = 50
	// feedLagApplyMinSec is the smallest lag the evaluations shift for. Below
	// it the feed is on time and the window stays at the wall clock, as before.
	feedLagApplyMinSec = 120
	// feedLagMaxSec caps the shift. A feed further behind than this is treated
	// as broken, not late, and the window stays at the wall clock.
	feedLagMaxSec = 30 * 60
	// feedLagRetentionMarginSec keeps a shifted window clear of the front of
	// hub.history, which drops spots by the wall clock.
	feedLagRetentionMarginSec = 60
)

type feedLagSlot struct {
	minute int64
	last   int64 // newest arrival second in the slot
	n      uint32
	hist   [feedLagBuckets]uint32
}

// quantileAge is the lower bound of the bucket at which feedLagQuantile of
// the n arrivals in hist are reached, so the estimate errs towards the wall
// clock.
func quantileAge(hist *[feedLagBuckets]uint32, n uint32) int64 {
	want := uint32(float64(n)*feedLagQuantile + 0.5)
	if want < 1 {
		want = 1
	}
	var cum uint32
	for b, c := range hist {
		cum += c
		if cum >= want {
			return int64(b) * feedLagBucketSec
		}
	}
	return 0
}

// feedLagTracker holds the age histograms of the current and the previous
// arrival minute. observe runs on the ingest path: one short critical section,
// no allocation.
type feedLagTracker struct {
	mu    sync.Mutex
	slots [2]feedLagSlot
}

var pskrFeedLag feedLagTracker

// observe records one arrival at wall-clock second arrival of a report
// timestamped t.
func (f *feedLagTracker) observe(t, arrival int64) {
	if t <= 0 || arrival <= 0 {
		return
	}
	age := arrival - t
	if age < 0 {
		age = 0
	}
	b := age / feedLagBucketSec
	if b >= feedLagBuckets {
		b = feedLagBuckets - 1
	}
	minute := arrival / 60
	f.mu.Lock()
	s := &f.slots[minute&1]
	if s.minute != minute {
		*s = feedLagSlot{minute: minute}
	}
	s.n++
	s.hist[b]++
	if arrival > s.last {
		s.last = arrival
	}
	f.mu.Unlock()
}

// estimate is the feed lag in seconds at wall-clock second now and whether
// arrivals of the last two minutes backed it. When the feed has stalled it
// falls back to the newest minute that had enough arrivals: the feed head was
// that minute's last arrival minus its lag, and the lag now is the time since
// that head.
func (f *feedLagTracker) estimate(now int64) (int64, bool) {
	minute := now / 60
	var hist [feedLagBuckets]uint32
	var n uint32
	var stale *feedLagSlot
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.slots {
		s := &f.slots[i]
		if s.n == 0 {
			continue
		}
		if s.minute != minute && s.minute != minute-1 {
			if s.minute < minute && s.n >= feedLagMinSamples && (stale == nil || s.minute > stale.minute) {
				stale = s
			}
			continue
		}
		n += s.n
		for b, c := range s.hist {
			hist[b] += c
		}
	}
	if n >= feedLagMinSamples {
		return quantileAge(&hist, n), true
	}
	if stale != nil {
		if head := stale.last - quantileAge(&stale.hist, stale.n); head > 0 && head <= now {
			return now - head, false
		}
	}
	return 0, false
}

// reset clears the tracker (tests).
func (f *feedLagTracker) reset() {
	f.mu.Lock()
	f.slots = [2]feedLagSlot{}
	f.mu.Unlock()
}

// evalNowFor is where a PSKReporter "now" window of the given minutes ends at
// wall-clock second now: now itself while the feed is on time, now - lag while
// it runs late. The shift is capped so the whole window stays inside
// hub.history (which drops spots by the wall clock): a 60-minute window with
// a 60-minute retention is never shifted. lag is the shift applied, raw the
// measured feed lag when it is at least feedLagApplyMinSec (0 otherwise), so a
// client can tell a window that could not move back far enough.
//
// The window also never ends before the process had a complete live history
// (liveHistoryCompleteSince, liveHistoryGapEnd after a restart): the live-rate
// bookkeeping of dx_conditions / hot_bands assumes the window end is past the
// restart gap, so for the first lag minutes after a restart the window stays
// at the wall clock.
func evalNowFor(now int64, minutes int) (evalNow, lag, raw int64) {
	raw, _ = pskrFeedLag.estimate(now)
	if raw < feedLagApplyMinSec || raw > feedLagMaxSec {
		raw = 0
	}
	lag = clampFeedLag(raw, minutes)
	bound := liveHistoryCompleteSince.Load()
	if ge := liveHistoryGapEnd.Load(); ge > bound {
		bound = ge
	}
	if lag > 0 && bound > 0 && now-lag < bound {
		lag = now - bound
		if lag < feedLagApplyMinSec {
			lag = 0
		}
	}
	return now - lag, lag, raw
}

// clampFeedLag is the shift a window of the given minutes can take for a feed
// lag of lag seconds.
func clampFeedLag(lag int64, minutes int) int64 {
	if lag < feedLagApplyMinSec || lag > feedLagMaxSec {
		return 0
	}
	if liveHistoryRetentionMinutes > 0 {
		room := int64(liveHistoryRetentionMinutes-minutes)*60 - feedLagRetentionMarginSec
		if room < feedLagApplyMinSec {
			return 0
		}
		if lag > room {
			lag = room
		}
	}
	return lag
}
