package main

import (
	"math"
	"path/filepath"
	"testing"
)

// slotAlignedNow is 25 minutes into a 30-min UTC slot, so a 20-min window
// stays inside one slot.
const slotAlignedNow = int64(1700000700)

// newSeededEngine returns an in-memory engine whose JN68 cluster baseline for
// 10m holds perDay spots in now's slot on each of the 10 previous days
// (JO62 → JO50: one end inside JN68, one outside).
func newSeededEngine(t *testing.T, now int64, perDay int) *DxBaselineEngine {
	return newSpreadSeededEngine(t, now, perDay, 600)
}

// newSpreadSeededEngine spreads each day's perDay spots evenly over the
// spreadSec seconds before now's time of day, so windows longer than one slot
// have a cluster baseline in every slot they overlap.
func newSpreadSeededEngine(t *testing.T, now int64, perDay int, spreadSec int64) *DxBaselineEngine {
	t.Helper()
	e := newDxBaselineEngine(filepath.Join(t.TempDir(), "dx_baseline.json"))
	if err := e.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	for day := int64(1); day <= 10; day++ {
		for i := 0; i < perDay; i++ {
			offset := int64(i) * spreadSec / int64(perDay)
			e.Observe(MQTTMessage{RP: -10, T: now - day*86400 - offset, SC: "DL1ABC", SL: "JO62QM", RC: "G0ABC", RL: "JO50AA", B: "10m", MD: "FT8"})
		}
	}
	return e
}

// spotsPerMinute returns one 10m spot per minute over the last `minutes`.
func spotsPerMinuteHistory(now int64, minutes int) []MQTTMessage {
	out := make([]MQTTMessage, 0, minutes)
	for i := 0; i < minutes; i++ {
		out = append(out, MQTTMessage{RP: -10, T: now - int64(i)*60, SC: "DL1ABC", SL: "JO62QM", RC: "G0ABC", RL: "JO50AA", B: "10m", MD: "FT8"})
	}
	return out
}

func liveSpots(now int64, n int, band string) []MQTTMessage {
	out := make([]MQTTMessage, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, MQTTMessage{RP: -10, T: now - int64(i%1100), SC: "DL1ABC", SL: "JO62QM", RC: "G0ABC", RL: "JO50AA", B: band, MD: "FT8"})
	}
	return out
}

func bandByName(t *testing.T, resp dxConditionsResponse, band string) dxBandCondition {
	t.Helper()
	for _, b := range resp.Bands {
		if b.Band == band {
			return b
		}
	}
	t.Fatalf("band %s missing from response", band)
	return dxBandCondition{}
}

// A low-volume band far above its own regional normal must read "above",
// whatever the other bands carry — the old label demoted such a band for
// having fewer reports than 40m.
func TestEvaluateActivityLevelAboveNormal(t *testing.T) {
	now := slotAlignedNow
	e := newSeededEngine(t, now, 20) // 200 spots over a 9-day span → ~0.74/min
	history := liveSpots(now, 60, "10m")
	history = append(history, liveSpots(now, 900, "40m")...)

	b := bandByName(t, e.Evaluate("JO62qm", false, 20, -15, history, now), "10m")
	if b.ActivityLevel != activityLevelAbove {
		t.Fatalf("ActivityLevel = %q (ratio %.2f, regional %d vs expected %.1f), want above",
			b.ActivityLevel, b.ActivityRatio, b.RegionalSpots, b.RegionalExpected)
	}
	if b.RegionalSpots != 60 {
		t.Errorf("RegionalSpots = %d, want 60 (one cluster end per spot)", b.RegionalSpots)
	}
	if b.ActivityRatio < 3 || b.ActivityRatio > 5 {
		t.Errorf("ActivityRatio = %.2f, want ~4", b.ActivityRatio)
	}
}

func TestEvaluateActivityLevelBelowAndLowSample(t *testing.T) {
	now := slotAlignedNow
	e := newSeededEngine(t, now, 60) // ~2.2/min → ~44 expected in 20 min

	below := bandByName(t, e.Evaluate("JO62qm", false, 20, -15, liveSpots(now, 10, "10m"), now), "10m")
	if below.ActivityLevel != activityLevelBelow {
		t.Errorf("10 vs ~44 expected: ActivityLevel = %q, want below", below.ActivityLevel)
	}

	quiet := newSeededEngine(t, now, 15) // 150 raw counts, ~0.56/min → ~11 expected
	low := bandByName(t, quiet.Evaluate("JO62qm", false, 20, -15, liveSpots(now, 3, "10m"), now), "10m")
	if low.ActivityLevel != activityLevelLowSample {
		t.Errorf("3 vs ~11 expected: ActivityLevel = %q, want low_sample", low.ActivityLevel)
	}

	// 50 raw counts behind the slot is below activityLevelMinSupport: the
	// baseline itself is too thin to call anything.
	thin := newSeededEngine(t, now, 5)
	thinBand := bandByName(t, thin.Evaluate("JO62qm", false, 20, -15, liveSpots(now, 60, "10m"), now), "10m")
	if thinBand.ActivityLevel != activityLevelNoBaseline {
		t.Errorf("thin baseline: ActivityLevel = %q, want no_baseline", thinBand.ActivityLevel)
	}
}

func TestEvaluateActivityLevelNoBaseline(t *testing.T) {
	now := slotAlignedNow
	e := newDxBaselineEngine(filepath.Join(t.TempDir(), "dx_baseline.json"))
	if err := e.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	b := bandByName(t, e.Evaluate("JO62qm", false, 20, -15, liveSpots(now, 30, "10m"), now), "10m")
	if b.ActivityLevel != activityLevelNoBaseline || b.ActivityRatio != 0 {
		t.Errorf("no baseline: level=%q ratio=%.2f, want no_baseline/0", b.ActivityLevel, b.ActivityRatio)
	}
}

// Live rates divide by the span the in-memory history can cover: a 120-min
// request over ~65 min of history (pruning runs every 5 min) must count and
// divide exactly like a 60-min one — not halve spots_per_minute, and not
// count the extra 5 minutes against a 60-minute expectation.
func TestEvaluateRateWindowCappedAtLiveRetention(t *testing.T) {
	now := slotAlignedNow
	e := newSpreadSeededEngine(t, now, 240, 3600) // baseline in all three overlapped slots
	history := spotsPerMinuteHistory(now, 65)
	r60 := bandByName(t, e.Evaluate("JO62qm", false, 60, -15, history, now), "10m")
	r120 := bandByName(t, e.Evaluate("JO62qm", false, 120, -15, history, now), "10m")
	if r60.ActivityLevel == activityLevelNoBaseline {
		t.Fatalf("60-min window should have a cluster baseline in every slot, got no_baseline")
	}
	if r60.RegionalSpots != 61 || r120.RegionalSpots != 61 {
		t.Errorf("regional spots 60-min = %d, 120-min = %d; want 61 for both (live window capped at %d min)",
			r60.RegionalSpots, r120.RegionalSpots, liveHistoryRetentionMinutes)
	}
	if r120.SpotsPerMinute != r60.SpotsPerMinute || math.Abs(r60.SpotsPerMinute-61.0/60.0) > 0.01 {
		t.Errorf("spots_per_minute 120-min = %.2f, 60-min = %.2f; want both ≈ %.2f", r120.SpotsPerMinute, r60.SpotsPerMinute, 61.0/60.0)
	}
	if r120.ActivityRatio != r60.ActivityRatio || r120.ActivityLevel != r60.ActivityLevel {
		t.Errorf("120-min ratio %.2f/%s != 60-min %.2f/%s", r120.ActivityRatio, r120.ActivityLevel, r60.ActivityRatio, r60.ActivityLevel)
	}
}

// After a restart without backfill, hub.history only holds spots since the
// process started; the live window (count and expectation) must start there.
func TestEvaluateLiveWindowStartsAtHistoryCompleteness(t *testing.T) {
	prev := liveHistoryCompleteSince.Load()
	t.Cleanup(func() { liveHistoryCompleteSince.Store(prev) })

	now := slotAlignedNow
	e := newSpreadSeededEngine(t, now, 240, 3600)
	history := spotsPerMinuteHistory(now, 60)

	full := bandByName(t, e.Evaluate("JO62qm", false, 60, -15, history, now), "10m")
	liveHistoryCompleteSince.Store(now - 10*60)
	partial := bandByName(t, e.Evaluate("JO62qm", false, 60, -15, history, now), "10m")

	if partial.RegionalSpots != 11 {
		t.Errorf("regional spots with history complete for 10 min = %d, want 11", partial.RegionalSpots)
	}
	if partial.RegionalExpected >= full.RegionalExpected/3 {
		t.Errorf("expected count must shrink with the span: 10 min = %.1f, 60 min = %.1f", partial.RegionalExpected, full.RegionalExpected)
	}
	if math.Abs(partial.SpotsPerMinute-full.SpotsPerMinute) > 0.15 {
		t.Errorf("spots_per_minute should stay ~1/min for a steady stream: partial %.2f vs full %.2f", partial.SpotsPerMinute, full.SpotsPerMinute)
	}
}

// A restart leaves a hole between the newest backfilled spot and ingest
// resuming; both the live count and the expectation skip it.
func TestEvaluateSkipsLiveHistoryGap(t *testing.T) {
	prevS, prevE := liveHistoryGapStart.Load(), liveHistoryGapEnd.Load()
	t.Cleanup(func() { liveHistoryGapStart.Store(prevS); liveHistoryGapEnd.Store(prevE) })

	now := slotAlignedNow
	e := newSpreadSeededEngine(t, now, 240, 3600)
	// A steady 1/min stream with nothing in [now-40min, now-20min).
	var history []MQTTMessage
	for _, m := range spotsPerMinuteHistory(now, 60) {
		if m.T >= now-40*60 && m.T < now-20*60 {
			continue
		}
		history = append(history, m)
	}
	noGap := bandByName(t, e.Evaluate("JO62qm", false, 60, -15, history, now), "10m")
	liveHistoryGapStart.Store(now - 40*60)
	liveHistoryGapEnd.Store(now - 20*60)
	withGap := bandByName(t, e.Evaluate("JO62qm", false, 60, -15, history, now), "10m")

	if withGap.RegionalExpected >= noGap.RegionalExpected*0.75 {
		t.Errorf("expected count with a 20-min gap = %.1f, without = %.1f; want about two thirds", withGap.RegionalExpected, noGap.RegionalExpected)
	}
	if withGap.ActivityRatio <= noGap.ActivityRatio*1.3 {
		t.Errorf("the gap must not read as quiet air: ratio %.2f with gap vs %.2f treating it as live", withGap.ActivityRatio, noGap.ActivityRatio)
	}
	if math.Abs(withGap.SpotsPerMinute-1) > 0.05 {
		t.Errorf("spots_per_minute over the complete 40 min = %.2f, want ~1", withGap.SpotsPerMinute)
	}
}

func TestLiveSegments(t *testing.T) {
	prevS, prevE := liveHistoryGapStart.Load(), liveHistoryGapEnd.Load()
	t.Cleanup(func() { liveHistoryGapStart.Store(prevS); liveHistoryGapEnd.Store(prevE) })

	liveHistoryGapStart.Store(0)
	liveHistoryGapEnd.Store(0)
	if got := liveSegments(100, 200); len(got) != 1 || got[0] != [2]int64{100, 200} {
		t.Errorf("no gap: %v", got)
	}
	liveHistoryGapStart.Store(120)
	liveHistoryGapEnd.Store(150)
	if got := liveSegments(100, 200); len(got) != 2 || got[0] != [2]int64{100, 120} || got[1] != [2]int64{150, 200} {
		t.Errorf("gap inside: %v", got)
	}
	if got := liveSegments(130, 200); len(got) != 1 || got[0] != [2]int64{150, 200} {
		t.Errorf("gap at start: %v", got)
	}
	if got := liveSegments(160, 200); len(got) != 1 || got[0] != [2]int64{160, 200} {
		t.Errorf("gap before window: %v", got)
	}
}

func TestClusterCoverageFromSpans(t *testing.T) {
	now := int64(1_800_000_000)
	day := int64(86400)
	if got := clusterCoverageFromSpans(now-100*day, now-25*day, now); math.Abs(got-0.25) > 1e-9 {
		t.Errorf("25 of 100 days = %v, want 0.25", got)
	}
	if got := clusterCoverageFromSpans(now-100*day, now-200*day, now); got != 1 {
		t.Errorf("cluster older than global = %v, want clamped 1", got)
	}
	if got := clusterCoverageFromSpans(now+day, now-day, now); got != 1 {
		t.Errorf("future global start = %v, want 1", got)
	}
}

func TestFeedsClusterBaseline(t *testing.T) {
	for src, want := range map[string]bool{"": true, "mqtt": true, "dxcluster": true, "rbn": false, "wspr": false} {
		if got := feedsClusterBaseline(MQTTMessage{Source: src, MD: "FT8"}); got != want {
			t.Errorf("feedsClusterBaseline(source=%q) = %v, want %v", src, got, want)
		}
	}
}

// A cluster table holding half the global span (coverage 0.5) doubles every
// cluster rate, so the same live count reads half as far above normal.
func TestEvaluateClusterCoverageScalesBaseline(t *testing.T) {
	now := slotAlignedNow
	e := newSeededEngine(t, now, 20)
	history := liveSpots(now, 60, "10m")
	before := bandByName(t, e.Evaluate("JO62qm", false, 20, -15, history, now), "10m")

	// Double the global total without touching the cluster buckets: the
	// cluster table now looks like it covers half the global span.
	e.mu.Lock()
	var global int64
	for _, b := range e.buckets {
		global += b.Count
	}
	e.buckets[baselineKey("80m", 0, 0, 0)] = &baselineBucket{Band: "80m", Count: global}
	e.mu.Unlock()

	resp := e.Evaluate("JO62qm", false, 20, -15, history, now)
	after := bandByName(t, resp, "10m")
	if d := resp.ClusterBaselineHistoryM*2 - resp.BaselineHistoryM; d < -1 || d > 1 {
		t.Errorf("cluster span = %d min, global span = %d min; want half", resp.ClusterBaselineHistoryM, resp.BaselineHistoryM)
	}
	if math.Abs(after.ActivityRatio*2-before.ActivityRatio) > 0.05 {
		t.Errorf("ratio with coverage 0.5 = %.2f, with coverage 1 = %.2f; want half", after.ActivityRatio, before.ActivityRatio)
	}
}

// The chart scale maps regional baseline units to the bars' population, which
// includes non-conditions sources (WSPR/RBN) the regional count excludes.
func TestEvaluateBaselineLocalScale(t *testing.T) {
	now := slotAlignedNow
	e := newSeededEngine(t, now, 20)
	history := liveSpots(now, 40, "10m")
	for i := 0; i < 10; i++ {
		history = append(history, MQTTMessage{RP: -5, T: now - int64(i), SC: "DL1ABC", SL: "JO62QM", RC: "G0ABC", RL: "JO50AA", B: "10m", MD: "WSPR"})
	}
	b := bandByName(t, e.Evaluate("JO62qm", false, 20, -15, history, now), "10m")
	if b.RegionalSpots != 40 {
		t.Errorf("RegionalSpots = %d, want 40 (WSPR excluded like the baseline)", b.RegionalSpots)
	}
	if math.Abs(b.BaselineLocalScale-50.0/40.0) > 1e-3 {
		t.Errorf("BaselineLocalScale = %.4f, want %.4f", b.BaselineLocalScale, 50.0/40.0)
	}
}

func TestClusterCoverageFromTotals(t *testing.T) {
	cases := []struct {
		cluster, global, want float64
	}{
		{200, 100, 1},   // same span: Σcluster = 2·Σglobal
		{50, 100, 0.25}, // cluster holds a quarter of the span
		{500, 100, 1},   // clamped: never older than global
		{0, 100, 1},     // empty cluster table: nothing to correct
		{100, 0, 1},     // no global data
	}
	for _, c := range cases {
		if got := clusterCoverageFromTotals(c.cluster, c.global); got != c.want {
			t.Errorf("clusterCoverageFromTotals(%v, %v) = %v, want %v", c.cluster, c.global, got, c.want)
		}
	}
}

// Cluster rates normalise by the cluster's own span, so a rebuilt cluster
// table no longer reads ~4× low against the global span.
func TestBaselineActivityForBandUsesClusterSpan(t *testing.T) {
	cluster := map[string]*baselineBucket{
		baselineClusterKey("JN68", "20m", 10, 2, 1): {Count: 300},
	}
	full, _ := baselineActivityForBand(nil, cluster, "JN68", "20m", 10, 100*1440, 100*1440)
	short, used := baselineActivityForBand(nil, cluster, "JN68", "20m", 10, 100*1440, 25*1440)
	if !used {
		t.Fatalf("expected cluster baseline")
	}
	if math.Abs(short/full-4) > 1e-9 {
		t.Errorf("cluster rate with 25-day span = %v, with 100-day span = %v; want 4×", short, full)
	}
}

func TestExpectedClusterCountStraddlesSlots(t *testing.T) {
	rates := make([]float64, SlotsOfDay)
	used := make([]bool, SlotsOfDay)
	slotStart := int64(1699999200) // a slot boundary
	cur := utcSlotOfDay(slotStart)
	prev := (cur + SlotsOfDay - 1) % SlotsOfDay
	rates[prev], used[prev] = 1, true
	rates[cur], used[cur] = 3, true

	// 10 min in the previous slot at 1/min + 20 min in the current at 3/min.
	got, ok := expectedClusterCount(rates, used, slotStart-600, slotStart+1200)
	if !ok || math.Abs(got-70) > 1e-9 {
		t.Errorf("expectedClusterCount = %v, %v; want 70, true", got, ok)
	}

	used[prev] = false
	if _, ok := expectedClusterCount(rates, used, slotStart-600, slotStart+1200); ok {
		t.Errorf("a global-fallback slot inside the span must make the count unavailable")
	}
	if _, ok := expectedClusterCount(nil, nil, slotStart, slotStart+60); ok {
		t.Errorf("nil series must be unavailable")
	}
}

func TestClassifyActivityLevel(t *testing.T) {
	cases := []struct {
		observed, expected float64
		want               string
	}{
		{60, 20, activityLevelAbove},
		{30, 20, activityLevelAbove}, // exactly 1.5×
		{25, 20, activityLevelNormal},
		{14, 20, activityLevelNormal},
		{13, 20, activityLevelBelow},
		{5, 100, activityLevelBelow}, // small count, but significant against 100
		{3, 2, activityLevelLowSample},
		{40, 0, activityLevelNoBaseline},
		// Classified on the published 2-decimal ratio.
		{67, 100, activityLevelBelow},
		{68, 100, activityLevelNormal},
		{149, 100, activityLevelNormal},
		{150, 100, activityLevelAbove},
	}
	for _, c := range cases {
		if got := classifyActivityLevel(c.observed, c.expected); got != c.want {
			t.Errorf("classifyActivityLevel(%v, %v) = %q, want %q", c.observed, c.expected, got, c.want)
		}
	}
}

func TestClusterEndCount(t *testing.T) {
	ax, ay, _ := locatorSquareXY("JN68")
	cases := []struct {
		sl, rl string
		want   int
	}{
		{"JO62QM", "JO50AA", 1}, // JO50 is in the JN58 cluster
		{"JO62QM", "JO73AA", 2}, // both ends in JN68: counted twice, as the baseline does
		{"jo62qm", "FN31AA", 1}, // case-insensitive, like Observe
		{"FN31AA", "PM95AA", 0},
		{"JO62QM", "", 0}, // Observe skips spots without both locators
	}
	for _, c := range cases {
		if got := clusterEndCount(MQTTMessage{SL: c.sl, RL: c.rl}, ax, ay); got != c.want {
			t.Errorf("clusterEndCount(%s, %s) = %d, want %d", c.sl, c.rl, got, c.want)
		}
	}
}

// Weak reports skew long-haul; the live side drops them below cw_min_db, so
// the baseline p90 must drop the same tiers or every band reads "shorter".
func TestReachBaselineP90AppliesSnrFloor(t *testing.T) {
	slotStart := int64(1699999200)
	cur := utcSlotOfDay(slotStart)
	rates := make([]float64, SlotsOfDay)
	rates[cur] = 1
	pairs := func(slot int) []baselinePair {
		if slot != cur {
			return nil
		}
		return []baselinePair{
			{DistanceTier: 1, SnrTier: 2, Count: 900}, // 500–1500 km, loud
			{DistanceTier: 4, SnrTier: 0, Count: 900}, // 7000+ km, ≤ -16 dB
		}
	}
	all, supportAll, ok1 := reachBaselineP90(pairs, rates, slotStart, slotStart+1200, -40)
	floored, supportFloored, ok2 := reachBaselineP90(pairs, rates, slotStart, slotStart+1200, -15)
	if !ok1 || !ok2 || supportAll != 1800 || supportFloored != 900 {
		t.Fatalf("ok=%v/%v support=%d/%d, want true/true 1800/900", ok1, ok2, supportAll, supportFloored)
	}
	if floored >= 1500 || all <= 7000 {
		t.Errorf("p90 with floor = %.0f (want within tier 1), without = %.0f (want within tier 4)", floored, all)
	}
	if got := classifyReachLevel(tieredP90([]float64{800, 900, 1000, 1200}) / floored); got != "typical" {
		t.Errorf("same-tier live distances: reach level %q, want typical", got)
	}
}

// A live hour straddling slots is compared against every overlapped slot,
// weighted by the spots each is expected to contribute.
func TestReachBaselineP90MixesOverlappedSlots(t *testing.T) {
	slotStart := int64(1699999200)
	cur := utcSlotOfDay(slotStart)
	prev := (cur + SlotsOfDay - 1) % SlotsOfDay
	rates := make([]float64, SlotsOfDay)
	rates[prev], rates[cur] = 9, 1 // the previous slot dominates the span
	pairs := func(slot int) []baselinePair {
		switch slot {
		case prev:
			return []baselinePair{{DistanceTier: 4, SnrTier: 3, Count: 500}} // long haul
		case cur:
			return []baselinePair{{DistanceTier: 0, SnrTier: 3, Count: 500}} // local
		}
		return nil
	}
	mixed, _, ok := reachBaselineP90(pairs, rates, slotStart-1800, slotStart+1800, -15)
	current, _, _ := reachBaselineP90(pairs, rates, slotStart, slotStart+1800, -15)
	if !ok || mixed <= 7000 || current >= 500 {
		t.Errorf("mixed p90 = %.0f (want tier 4), current-slot p90 = %.0f (want tier 0)", mixed, current)
	}
	if _, _, ok := reachBaselineP90(pairs, rates, slotStart-3600, slotStart+1800, -15); ok {
		t.Errorf("a slot without cluster pairs inside the span must make reach unavailable")
	}
}
