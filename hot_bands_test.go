package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSustainedRecentBins(t *testing.T) {
	cases := []struct {
		name      string
		sparkline []float64
		floor     float64
		want      int
	}{
		{"empty", nil, 30, 0},
		{"all zero", []float64{0, 0, 0}, 30, 0},
		{"flash spike single trailing", []float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 80}, 30, 1},
		{"two trailing sustained", []float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 60, 80}, 30, 2},
		{"three trailing sustained", []float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 40, 60, 80}, 30, 3},
		{"gap breaks streak", []float64{0, 0, 0, 0, 0, 0, 0, 0, 80, 10, 60, 80}, 30, 2},
		{"below floor doesn't count", []float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 20, 25}, 30, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sustainedRecentBins(tc.sparkline, tc.floor)
			if got != tc.want {
				t.Errorf("sustainedRecentBins(%v) = %d, want %d", tc.sparkline, got, tc.want)
			}
		})
	}
}

func TestActivityRatio(t *testing.T) {
	cases := []struct {
		name           string
		live, baseline float64
		min, max       float64
	}{
		{"both zero", 0, 0, 0, 0},
		{"baseline zero, live positive", 0.5, 0, 1.0, 2.0},
		{"normal ratio", 1.0, 0.5, 2.0, 2.0},
		{"big ratio", 5.0, 0.05, 99.0, 101.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := activityRatio(tc.live, tc.baseline)
			if got < tc.min || got > tc.max {
				t.Errorf("activityRatio(%v,%v) = %v, want in [%v,%v]", tc.live, tc.baseline, got, tc.min, tc.max)
			}
		})
	}
}

func TestP90FromTierCounts(t *testing.T) {
	cases := []struct {
		name   string
		counts [5]int64
		want   float64
		eps    float64
	}{
		{"empty", [5]int64{}, 0, 0.01},
		{"only tier 0", [5]int64{100, 0, 0, 0, 0}, 450, 1},
		{"only tier 4", [5]int64{0, 0, 0, 0, 100}, 11500, 1},
		{"uniform across tiers", [5]int64{10, 10, 10, 10, 10}, 9500, 1},
		{"mass at low tiers", [5]int64{1000, 100, 10, 1, 0}, 600, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := p90FromTierCounts(tc.counts)
			if math.Abs(got-tc.want) > tc.eps {
				t.Errorf("p90FromTierCounts(%v) = %v, want %v ±%v", tc.counts, got, tc.want, tc.eps)
			}
		})
	}
}

func TestSortHotBandRecommendations(t *testing.T) {
	recs := []hotBandRecommendation{
		{Band: "20m", Kind: "rising", Priority: "normal", RankScore: 70},
		{Band: "10m", Kind: "surprise", Priority: "high", RankScore: 8},
		{Band: "15m", Kind: "dx_surge", Priority: "normal", RankScore: 2.5},
		{Band: "17m", Kind: "surprise", Priority: "high", RankScore: 12},
	}
	sortHotBandRecommendations(recs)
	wantOrder := []string{"17m", "10m", "15m", "20m"}
	for i, b := range wantOrder {
		if recs[i].Band != b {
			t.Errorf("position %d: got %q want %q (full order: %v)", i, recs[i].Band, b, bandsOf(recs))
		}
	}
}

func bandsOf(recs []hotBandRecommendation) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.Band
	}
	return out
}

// TestHotBandsHeuristicGating exercises HotBands through a synthetic baseline
// engine. It pins the three classes and the sustained-vs-flash filter without
// needing live MQTT, Postgres or a fully populated baseline.
func TestHotBandsHeuristicGating(t *testing.T) {
	cases := []struct {
		name      string
		band      string
		spark     []float64
		live      float64
		baseAct   float64
		liveP90   float64
		baseP90   float64
		trend     string
		trendDel  float64
		status    string
		wantKind  string
		wantEmpty bool
	}{
		{
			name:     "surprise: rare baseline, big ratio, sustained",
			band:     "10m",
			spark:    []float64{0, 0, 0, 0, 0, 0, 0, 0, 40, 70, 80, 95},
			live:     0.8,
			baseAct:  0.05,
			liveP90:  3000,
			baseP90:  2500,
			trend:    "rising",
			trendDel: 0.3,
			status:   "yellow",
			wantKind: "surprise",
		},
		{
			name:     "dx_surge: long path, sustained, normal baseline",
			band:     "20m",
			spark:    []float64{0, 0, 0, 0, 0, 30, 40, 60, 70, 80, 90, 95},
			live:     2.0,
			baseAct:  1.5,
			liveP90:  11000,
			baseP90:  4000,
			trend:    "stable",
			trendDel: 0.0,
			status:   "green",
			wantKind: "dx_surge",
		},
		{
			name:     "rising: trend up with elevated ratio",
			band:     "15m",
			spark:    []float64{0, 0, 0, 0, 0, 0, 0, 0, 40, 50, 70, 90},
			live:     1.5,
			baseAct:  1.0,
			liveP90:  3000,
			baseP90:  2800,
			trend:    "rising",
			trendDel: 0.4,
			status:   "green",
			wantKind: "rising",
		},
		{
			name:      "flash spike suppressed by sustained guard",
			band:      "12m",
			spark:     []float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 95},
			live:      0.8,
			baseAct:   0.05,
			liveP90:   3000,
			baseP90:   2500,
			trend:     "rising",
			trendDel:  0.4,
			status:    "yellow",
			wantEmpty: true,
		},
		{
			name:      "out-of-scope band (13cm) suppressed",
			band:      "13cm", // microwave: same rising setup as 15m, must not surface
			spark:     []float64{0, 0, 0, 0, 0, 0, 0, 0, 40, 50, 70, 90},
			live:      1.5,
			baseAct:   1.0,
			liveP90:   3000,
			baseP90:   2800,
			trend:     "rising",
			trendDel:  0.4,
			status:    "green",
			wantEmpty: true,
		},
		{
			name:      "too low rate suppressed",
			band:      "17m",
			spark:     []float64{0, 0, 0, 0, 0, 0, 0, 0, 30, 50, 60, 70},
			live:      0.3, // below hotBandsMinLiveRate
			baseAct:   0.05,
			liveP90:   3000,
			baseP90:   2500,
			trend:     "rising",
			trendDel:  0.3,
			status:    "yellow",
			wantEmpty: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cond := dxConditionsResponse{
				QTH:           "JO22",
				CurrentSlotOfDay: 24,
				BaselineHistoryM: 30 * 24 * 60, // trusted
				Bands: []dxBandCondition{{
					Band:               tc.band,
					Status:             tc.status,
					Score:              60,
					SpotsPerMinute:     tc.live,
					BaselineActivity:   tc.baseAct,
					ClusterBaselineUsed: true,
					P90DistanceKm:      tc.liveP90,
					Trend:              tc.trend,
					TrendDelta:         tc.trendDel,
					Sparkline:          tc.spark,
				}},
			}

			recs := classifyHotBandsForTest(cond, "all", func(_ string) (float64, bool) {
				return tc.baseP90, true
			})

			if tc.wantEmpty {
				if len(recs) != 0 {
					t.Fatalf("expected empty, got %d recs: %v", len(recs), recs)
				}
				return
			}
			if len(recs) != 1 {
				t.Fatalf("expected 1 rec, got %d: %v", len(recs), recs)
			}
			if recs[0].Kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", recs[0].Kind, tc.wantKind)
			}
		})
	}
}

// classifyHotBandsForTest exercises the same logic as HotBands but pulls the
// baseline p90 from a test stub so we don't need Postgres or an in-memory
// engine. Kept here (not in hot_bands.go) so production code stays single-
// purpose.
func classifyHotBandsForTest(cond dxConditionsResponse, currentBand string, baseP90 func(band string) (float64, bool)) []hotBandRecommendation {
	trustedBaseline := cond.BaselineHistoryM >= hotBandsMinHistoryMinutes
	out := make([]hotBandRecommendation, 0, len(cond.Bands))
	for _, b := range cond.Bands {
		if !bandInScope(b.Band) {
			continue
		}
		if currentBand != "" && currentBand != "all" && b.Band == currentBand {
			continue
		}
		ratio := activityRatio(b.SpotsPerMinute, b.BaselineActivity)
		sustained := sustainedRecentBins(b.Sparkline, hotBandsSparklineFloor)
		if sustained < hotBandsMinSustainedBins || b.SpotsPerMinute < hotBandsMinLiveRate {
			continue
		}
		bp, bpUsed := baseP90(b.Band)
		distRatio := 0.0
		if bp > 0 {
			distRatio = b.P90DistanceKm / bp
		}
		switch {
		case b.ClusterBaselineUsed && trustedBaseline &&
			b.BaselineActivity > 0 && b.BaselineActivity <= hotBandsSurpriseBaselineMx &&
			ratio >= hotBandsSurpriseRatio:
			out = append(out, hotBandRecommendation{Band: b.Band, Kind: "surprise", Priority: "high"})
		case b.ClusterBaselineUsed && trustedBaseline && bpUsed && bp > 0 &&
			distRatio >= hotBandsDxSurgeRatio && b.P90DistanceKm >= hotBandsDxSurgeMinDistKm:
			out = append(out, hotBandRecommendation{Band: b.Band, Kind: "dx_surge", Priority: "normal"})
		case b.Trend == "rising" && b.TrendDelta >= hotBandsMinTrendDelta &&
			(b.Status == "yellow" || b.Status == "green") &&
			ratio >= hotBandsRisingRatioMin:
			out = append(out, hotBandRecommendation{Band: b.Band, Kind: "rising", Priority: "normal"})
		}
	}
	return out
}

// TestLookupBaselineP90ClusterFallback exercises lookupBaselineP90 through the
// global-swap pattern (main_test.go style): a fresh in-memory engine seeded
// with one global and one cluster bucket, assigned to the dxBaseline global
// and restored afterwards.
func TestLookupBaselineP90ClusterFallback(t *testing.T) {
	orig := dxBaseline
	defer func() { dxBaseline = orig }()

	const slot = 10
	e := newDxBaselineEngine("")
	e.buckets[baselineKey("20m", slot, 0, 1)] = &baselineBucket{Band: "20m", SlotOfDay: slot, DistanceTier: 0, SnrTier: 1, Count: 100}
	e.clusterBuckets[baselineClusterKey("JN68", "20m", slot, 4, 0)] = &baselineBucket{Band: "20m", SlotOfDay: slot, DistanceTier: 4, SnrTier: 0, Count: 100}
	dxBaseline = e

	// Cluster has data for JN68 → cluster tier wins (all mass in tier 4).
	km, used := dxBaseline.lookupBaselineP90("JN68", "20m", slot)
	if !used || math.Abs(km-11500) > 1 {
		t.Errorf("cluster lookup = (%v, %v), want (11500, true)", km, used)
	}

	// Unknown cluster → global fallback (tier 0).
	km, used = dxBaseline.lookupBaselineP90("ZZ99", "20m", slot)
	if used || math.Abs(km-450) > 1 {
		t.Errorf("global fallback = (%v, %v), want (450, false)", km, used)
	}

	// Empty operator cluster → global fallback.
	km, used = dxBaseline.lookupBaselineP90("", "20m", slot)
	if used || math.Abs(km-450) > 1 {
		t.Errorf("empty cluster = (%v, %v), want (450, false)", km, used)
	}

	// No data at all for the band.
	km, used = dxBaseline.lookupBaselineP90("JN68", "17m", slot)
	if used || km != 0 {
		t.Errorf("no data = (%v, %v), want (0, false)", km, used)
	}

	// A nil engine never panics.
	var nilEngine *DxBaselineEngine
	km, used = nilEngine.lookupBaselineP90("JN68", "20m", slot)
	if used || km != 0 {
		t.Errorf("nil engine = (%v, %v), want (0, false)", km, used)
	}
}

// TestHotBandsEmptyBaselineStableEmpty verifies that HotBands on an engine
// without any baseline data returns a stable, empty recommendation list (the
// frontend hides the indicator for exactly this case) and propagates the qth.
func TestHotBandsEmptyBaselineStableEmpty(t *testing.T) {
	e := newDxBaselineEngine("")
	now := int64(1700000000)

	resp := e.HotBands("JO62qm", false, 15, -24, "all", nil, now)
	if resp.QTH != "JO62QM" {
		t.Errorf("QTH = %q, want JO62QM (normalized)", resp.QTH)
	}
	if len(resp.Recommendations) != 0 {
		t.Errorf("empty baseline produced recommendations: %+v", resp.Recommendations)
	}
	if resp.Recommendations == nil {
		t.Errorf("Recommendations must be an empty slice, not nil (frontend hides the indicator)")
	}

	// Empty qth short-circuits before any band evaluation.
	resp = e.HotBands("", false, 15, -24, "all", nil, now)
	if len(resp.Recommendations) != 0 {
		t.Errorf("empty qth produced recommendations: %+v", resp.Recommendations)
	}
}

// TestHotBandsRisingRecFromSeededBaseline is an end-to-end run of HotBands
// against a seeded in-memory baseline: a 15m band with a rising live sparkline
// and modest baseline support must surface exactly one "rising" recommendation.
// Baseline buckets, the observed-event ring, and the event span are seeded
// directly (package-internal access) so no Postgres or Observe funnel is
// involved.
//
// Seeded shape, at slot = utcSlotOfDay(now):
//   - global 15m baseline: 1600 events in SNR tier 0 + 400 in tier 1
//     (distance tier 3) → baselineActivity ≈ 1.09 spots/min over a 61-day span
//   - event ring: 8/14/18 spots in the last three 75s bins of a 15-min window
//     → normalized sparkline 44.44/77.78/100 (sustained ≥ 2, trend "rising")
//   - live history: 40 spots over the 15-min window → 2.67 spots/min live rate
func TestHotBandsRisingRecFromSeededBaseline(t *testing.T) {
	const now = int64(1700000000)
	slot := utcSlotOfDay(now)

	e := newDxBaselineEngine("")
	e.mu.Lock()
	e.buckets[baselineKey("15m", slot, 3, 0)] = &baselineBucket{Band: "15m", SlotOfDay: slot, DistanceTier: 3, SnrTier: 0, Count: 1600}
	e.buckets[baselineKey("15m", slot, 3, 1)] = &baselineBucket{Band: "15m", SlotOfDay: slot, DistanceTier: 3, SnrTier: 1, Count: 400}
	events := make([]dxObservedEvent, 0, 40)
	for i := 0; i < 8; i++ {
		events = append(events, dxObservedEvent{T: now - 200, B: "15m", SC: fmt.Sprintf("DK%dAB", i), RC: fmt.Sprintf("W%dXY", i), SL: "JO62QM", RL: "FN31AA", RP: -10})
	}
	for i := 0; i < 14; i++ {
		events = append(events, dxObservedEvent{T: now - 120, B: "15m", SC: fmt.Sprintf("OK%dAB", i), RC: "W9XY", SL: "JO62QM", RL: "FN31AA", RP: -10})
	}
	for i := 0; i < 18; i++ {
		events = append(events, dxObservedEvent{T: now - 30, B: "15m", SC: fmt.Sprintf("SM%dAB", i), RC: "K8XY", SL: "JO62QM", RL: "FN31AA", RP: -10})
	}
	e.events = events
	e.firstEventAt = now - 61*86400
	e.lastEventAt = now
	e.mu.Unlock()

	history := make([]MQTTMessage, 0, 40)
	for i := 0; i < 40; i++ {
		history = append(history, MQTTMessage{
			T: now - 90, SC: fmt.Sprintf("DK%dABC", i), RC: fmt.Sprintf("W%dXYZ", i),
			SL: "JO62QM", RL: "FN31AA", RP: -10, B: "15m", MD: "FT8",
		})
	}

	resp := e.HotBands("JO62qm", false, 15, -24, "all", history, now)

	if len(resp.Recommendations) != 1 {
		t.Fatalf("expected exactly 1 recommendation, got %d: %+v", len(resp.Recommendations), resp.Recommendations)
	}
	rec := resp.Recommendations[0]
	if rec.Band != "15m" {
		t.Errorf("band = %q, want 15m", rec.Band)
	}
	if rec.Kind != "rising" {
		t.Errorf("kind = %q, want rising", rec.Kind)
	}
	if rec.Priority != "normal" {
		t.Errorf("priority = %q, want normal", rec.Priority)
	}
	if rec.SustainedBins != 3 {
		t.Errorf("sustained bins = %d, want 3", rec.SustainedBins)
	}
	if rec.Trend != "rising" {
		t.Errorf("trend = %q, want rising", rec.Trend)
	}
	if rec.SpotsPerMinute != 2.67 {
		t.Errorf("spots_per_minute = %v, want 2.67 (40 spots / 15 min)", rec.SpotsPerMinute)
	}
}
// TestHotBandsHandlerSeededBaseline runs hotBandsHandler end-to-end over
// HTTP against the seeded-baseline fixture from
// TestHotBandsRisingRecFromSeededBaseline, this time reading the live window
// from the hub.history global like the real request path does. Skips if a
// 30-minute UTC slot boundary is crossed mid-test (the engine's `now` comes
// from time.Now in the handler, which tests cannot pin).
func TestHotBandsHandlerSeededBaseline(t *testing.T) {
	origBaseline := dxBaseline
	defer func() { dxBaseline = origBaseline }()

	now := time.Now().Unix()
	slot := utcSlotOfDay(now)

	e := newDxBaselineEngine("")
	e.mu.Lock()
	e.buckets[baselineKey("15m", slot, 3, 0)] = &baselineBucket{Band: "15m", SlotOfDay: slot, DistanceTier: 3, SnrTier: 0, Count: 1600}
	e.buckets[baselineKey("15m", slot, 3, 1)] = &baselineBucket{Band: "15m", SlotOfDay: slot, DistanceTier: 3, SnrTier: 1, Count: 400}
	events := make([]dxObservedEvent, 0, 40)
	for i := 0; i < 8; i++ {
		events = append(events, dxObservedEvent{T: now - 200, B: "15m", SC: fmt.Sprintf("DK%dAB", i), RC: fmt.Sprintf("W%dXY", i), SL: "JO62QM", RL: "FN31AA", RP: -10})
	}
	for i := 0; i < 14; i++ {
		events = append(events, dxObservedEvent{T: now - 120, B: "15m", SC: fmt.Sprintf("OK%dAB", i), RC: "W9XY", SL: "JO62QM", RL: "FN31AA", RP: -10})
	}
	for i := 0; i < 18; i++ {
		events = append(events, dxObservedEvent{T: now - 30, B: "15m", SC: fmt.Sprintf("SM%dAB", i), RC: "K8XY", SL: "JO62QM", RL: "FN31AA", RP: -10})
	}
	e.events = events
	e.firstEventAt = now - 61*86400
	e.lastEventAt = now
	e.mu.Unlock()
	dxBaseline = e

	hub.Lock()
	origHistory := hub.history
	hub.history = make([]MQTTMessage, 0, 40)
	for i := 0; i < 40; i++ {
		hub.history = append(hub.history, MQTTMessage{
			T: now - 90, SC: fmt.Sprintf("DK%dABC", i), RC: fmt.Sprintf("W%dXYZ", i),
			SL: "JO62QM", RL: "FN31AA", RP: -10, B: "15m", MD: "FT8", Source: "mqtt",
		})
	}
	hub.Unlock()
	defer func() {
		hub.Lock()
		hub.history = origHistory
		hub.Unlock()
	}()

	server := httptest.NewServer(http.HandlerFunc(hotBandsHandler))
	defer server.Close()

	// minutes=15 pins the window to the fixture the seeded sparkline was built
	// for (the handler's default is 20 minutes, which re-bins the events).
	httpResp, err := http.Get(server.URL + "/api/hot_bands?qth=JO62qm&current_band=all&minutes=15")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", httpResp.StatusCode)
	}
	if ctype := httpResp.Header.Get("Content-Type"); ctype != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ctype)
	}

	// If the UTC slot rolled over between seeding and evaluation the seeded
	// sparkline no longer lines up with the handler's slot — skip, don't flake.
	if utcSlotOfDay(time.Now().Unix()) != slot {
		t.Skipf("UTC slot boundary crossed during test")
	}

	var decoded map[string]any
	if err := json.NewDecoder(httpResp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	for _, key := range []string{"qth", "surroundings", "current_band", "current_slot_of_day", "generated_at", "baseline_history_minutes", "recommendations"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("response missing key %q: %v", key, decoded)
		}
	}

	recsRaw, ok := decoded["recommendations"].([]any)
	if !ok {
		t.Fatalf("recommendations must be a list: %v", decoded["recommendations"])
	}
	if len(recsRaw) != 1 {
		t.Fatalf("expected exactly 1 recommendation, got %d: %v", len(recsRaw), recsRaw)
	}
	rec, ok := recsRaw[0].(map[string]any)
	if !ok {
		t.Fatalf("recommendation must be an object: %v", recsRaw[0])
	}
	if rec["band"] != "15m" {
		t.Errorf("band = %v, want 15m", rec["band"])
	}
	if rec["kind"] != "rising" {
		t.Errorf("kind = %v, want rising", rec["kind"])
	}
	if rec["priority"] != "normal" {
		t.Errorf("priority = %v, want normal", rec["priority"])
	}
	if int(rec["sustained_bins"].(float64)) != 3 {
		t.Errorf("sustained_bins = %v, want 3", rec["sustained_bins"])
	}
	if rec["trend"] != "rising" {
		t.Errorf("trend = %v, want rising", rec["trend"])
	}
	if rec["spots_per_minute"] != 2.67 {
		t.Errorf("spots_per_minute = %v, want 2.67", rec["spots_per_minute"])
	}
}

// TestHotBandsHandlerDegradesWithoutBaseline: with no baseline engine wired the
// handler answers 200 with an empty (non-nil) recommendation list and echoes
// the normalized qth; a missing qth is a 400 before anything is evaluated.
// Non-positive and oversized minutes values are tolerated (clamped, no error).
func TestHotBandsHandlerDegradesWithoutBaseline(t *testing.T) {
	origBaseline := dxBaseline
	defer func() { dxBaseline = origBaseline }()
	dxBaseline = nil

	server := httptest.NewServer(http.HandlerFunc(hotBandsHandler))
	defer server.Close()

	// Missing qth → 400.
	resp, err := http.Get(server.URL + "/api/hot_bands")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing qth: status = %d, want 400", resp.StatusCode)
	}

	// No baseline + present qth → stable empty recommendations.
	// minutes=99999 clamps to maxDxWindowMinutes without erroring.
	resp, err = http.Get(server.URL + "/api/hot_bands?qth=JO62qm&minutes=99999")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("no-baseline status = %d, want 200", resp.StatusCode)
	}
	var decoded hotBandsResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if decoded.QTH != "JO62QM" {
		t.Errorf("QTH = %q, want JO62QM (normalized uppercase)", decoded.QTH)
	}
	if decoded.Recommendations == nil || len(decoded.Recommendations) != 0 {
		t.Errorf("recommendations = %v, want an empty non-nil slice", decoded.Recommendations)
	}
}
