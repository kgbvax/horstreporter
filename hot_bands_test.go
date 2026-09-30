package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
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
		{"baseline zero, live positive", 0.5, 0, 0, 0},
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

// TestHotBandsHeuristicGating exercises the production classifier
// (classifyHotBand) through hotBandsFromConditions on a synthetic Evaluate
// result. It pins the three classes and the sustained-vs-flash filter without
// needing live MQTT, Postgres or a fully populated baseline.
func TestHotBandsHeuristicGating(t *testing.T) {
	cases := []struct {
		name       string
		band       string
		spark      []float64
		live       float64
		links      int
		baseAct    float64
		liveP90    float64
		reachLevel string
		reachRatio float64
		trend      string
		trendDel   float64
		status     string
		wantKind   string
		wantEmpty  bool
	}{
		{
			name:     "surprise: rare baseline, big ratio, sustained, improbable count",
			band:     "10m",
			spark:    []float64{0, 0, 0, 0, 0, 0, 0, 0, 40, 70, 80, 95},
			live:     0.8,
			links:    12, // 0.8/min over 15 min vs λ = 0.05×15 = 0.75
			baseAct:  0.05,
			liveP90:  3000,
			trend:    "rising",
			trendDel: 0.3,
			status:   "yellow",
			wantKind: "surprise",
		},
		{
			name:       "dx_surge: reach longer, sustained, normal baseline",
			band:       "20m",
			spark:      []float64{0, 0, 0, 0, 0, 30, 40, 60, 70, 80, 90, 95},
			live:       2.0,
			links:      30,
			baseAct:    1.5,
			liveP90:    11000,
			reachLevel: "longer",
			reachRatio: 2.75,
			trend:      "stable",
			trendDel:   0.0,
			status:     "green",
			wantKind:   "dx_surge",
		},
		{
			name:     "rising: trend up with elevated ratio",
			band:     "15m",
			spark:    []float64{0, 0, 0, 0, 0, 0, 0, 0, 40, 50, 70, 90},
			live:     1.5,
			links:    22,
			baseAct:  1.0,
			liveP90:  3000,
			trend:    "rising",
			trendDel: 2.1,
			status:   "green",
			wantKind: "rising",
		},
		{
			name:      "flash spike suppressed by sustained guard",
			band:      "12m",
			spark:     []float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 95},
			live:      0.8,
			links:     12,
			baseAct:   0.05,
			liveP90:   3000,
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
			links:     22,
			baseAct:   1.0,
			liveP90:   3000,
			trend:     "rising",
			trendDel:  2.1,
			status:    "green",
			wantEmpty: true,
		},
		{
			name:      "too low rate suppressed",
			band:      "17m",
			spark:     []float64{0, 0, 0, 0, 0, 0, 0, 0, 30, 50, 60, 70},
			live:      0.3, // below hotBandsMinLiveRate
			links:     4,
			baseAct:   0.05,
			liveP90:   3000,
			trend:     "rising",
			trendDel:  0.3,
			status:    "yellow",
			wantEmpty: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cond := dxConditionsResponse{
				QTH:              "JO22",
				CurrentSlotOfDay: 24,
				BaselineHistoryM: 30 * 24 * 60, // trusted
				liveSpanMin:      15,
				Bands: []dxBandCondition{{
					Band:                tc.band,
					Status:              tc.status,
					Score:               60,
					CurrentLinks:        tc.links,
					SpotsPerMinute:      tc.live,
					BaselineActivity:    tc.baseAct,
					ClusterBaselineUsed: true,
					P90DistanceKm:       tc.liveP90,
					BaselineP90DistanceKm: func() float64 {
						if tc.reachRatio > 0 {
							return tc.liveP90 / tc.reachRatio
						}
						return 0
					}(),
					ReachLevel: tc.reachLevel,
					ReachRatio: tc.reachRatio,
					Trend:      tc.trend,
					TrendDelta: tc.trendDel,
					Sparkline:  tc.spark,
				}},
			}

			recs := hotBandsFromConditions(cond, "all", hotBandsOptions{}).Recommendations

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

// risingBand is a band condition that just clears every "rising" gate; the
// boundary tests below nudge one field at a time across its threshold.
func risingBand() dxBandCondition {
	return dxBandCondition{
		Band:           "20m",
		Status:         "green",
		Score:          55,
		CurrentLinks:   30,
		SpotsPerMinute: hotBandsRisingMinLiveRate,
		// A regional ratio, so the fallback estimate doesn't mask the boundary.
		ActivityLevel:    activityLevelNormal,
		ActivityRatio:    hotBandsRisingRatioMin,
		BaselineActivity: 1.0,
		P90DistanceKm:    2500,
		Trend:            "rising",
		TrendDelta:       hotBandsMinTrendDelta,
		// Exactly hotBandsRisingMinBins trailing bins above the floor.
		Sparkline: []float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 40, 60, 90},
	}
}

func TestClassifyHotBandRisingBoundaries(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(b *dxBandCondition)
		want   bool
	}{
		{"all gates at threshold", func(b *dxBandCondition) {}, true},
		{"trend delta just below 1.5", func(b *dxBandCondition) { b.TrendDelta = 1.49 }, false},
		{"old loose trend delta 0.6 rejected", func(b *dxBandCondition) { b.TrendDelta = 0.6 }, false},
		{"ratio just below 1.5", func(b *dxBandCondition) { b.ActivityRatio = 1.49 }, false},
		{"old loose ratio 1.2 rejected", func(b *dxBandCondition) { b.ActivityRatio = 1.2 }, false},
		{"only 2 sustained bins", func(b *dxBandCondition) {
			b.Sparkline = []float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 60, 90}
		}, false},
		{"spm just below 1.0", func(b *dxBandCondition) { b.SpotsPerMinute = 0.99 }, false},
		{"status red", func(b *dxBandCondition) { b.Status = "red" }, false},
		{"no baseline and no regional ratio", func(b *dxBandCondition) {
			b.ActivityLevel, b.ActivityRatio, b.BaselineActivity = "", 0, 0
			b.SpotsPerMinute, b.TrendDelta = 5, 4 // strong slope, busy band
		}, false},
		{"fallback ratio from a real baseline still qualifies", func(b *dxBandCondition) {
			b.ActivityLevel, b.ActivityRatio = "", 0
			b.BaselineActivity, b.SpotsPerMinute = 1.0, 1.5
		}, true},
		{"unrounded sub-0.005 baseline counts as a baseline", func(b *dxBandCondition) {
			b.ActivityLevel, b.ActivityRatio, b.BaselineActivity = "", 0, 0
			b.baselineRate = 0.004
		}, true},
		{"trend label stable", func(b *dxBandCondition) { b.Trend = "stable" }, false},
		{"status yellow ok", func(b *dxBandCondition) { b.Status = "yellow" }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := risingBand()
			tc.mutate(&b)
			rec, ok := classifyHotBand(b, true, 15)
			if ok != tc.want {
				t.Fatalf("classifyHotBand ok = %v (rec %+v), want %v", ok, rec, tc.want)
			}
			if ok && rec.Kind != "rising" {
				t.Errorf("kind = %q, want rising", rec.Kind)
			}
		})
	}
}

// surpriseBand clears the pre-existing surprise gates (cluster baseline,
// quiet usual rate, ≥5× ratio, sustained, ≥0.5 spm) in the fallback path:
// no regional ratio, and a live count that is NOT Poisson-significant
// (2 spots over 4 min vs λ = 0.1×4 = 0.4 → P(X≥2) ≈ 0.062).
func surpriseBand() dxBandCondition {
	return dxBandCondition{
		Band:                "10m",
		Status:              "yellow",
		Score:               40,
		CurrentLinks:        2,
		SpotsPerMinute:      0.5,
		BaselineActivity:    0.1,
		ClusterBaselineUsed: true,
		ActivityLevel:       activityLevelLowSample,
		P90DistanceKm:       2500,
		Trend:               "stable",
		Sparkline:           []float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 60, 90},
	}
}

func TestClassifyHotBandSurpriseGates(t *testing.T) {
	const liveSpanMin = 4.0
	// significant: 8 spots over the 4-min span (2.0/min, 20× the baseline)
	// against λ = 0.1×4 = 0.4, P(X≥8) ≈ 1e-8.
	significant := func(b *dxBandCondition) {
		b.CurrentLinks = 8
		b.SpotsPerMinute = 2.0
	}
	cases := []struct {
		name     string
		mutate   func(b *dxBandCondition)
		trusted  bool
		wantKind string // "" = no recommendation
	}{
		{"fallback path, count not significant", func(b *dxBandCondition) {}, true, ""},
		{"fallback path, Poisson-significant count", significant, true, "surprise"},
		{"regional ratio present", func(b *dxBandCondition) {
			b.ActivityLevel = activityLevelAbove
			b.ActivityRatio = 6
		}, true, "surprise"},
		{"regional ratio below 5x", func(b *dxBandCondition) {
			b.ActivityLevel = activityLevelAbove
			b.ActivityRatio = 4
		}, true, ""},
		{"regional ratio but P90 under 800 km", func(b *dxBandCondition) {
			b.ActivityLevel = activityLevelAbove
			b.ActivityRatio = 6
			b.P90DistanceKm = 799
		}, true, ""},
		{"significant count but P90 under 800 km", func(b *dxBandCondition) {
			significant(b)
			b.P90DistanceKm = 700
		}, true, ""},
		{"P90 exactly 800 km", func(b *dxBandCondition) {
			significant(b)
			b.P90DistanceKm = 800
		}, true, "surprise"},
		{"untrusted baseline", significant, false, ""},
		{"global (non-cluster) baseline", func(b *dxBandCondition) {
			significant(b)
			b.ClusterBaselineUsed = false
		}, true, ""},
		// 2 spots over 4 min against the unrounded 0.035/min (λ = 0.14,
		// P(X≥2) ≈ 0.0089) is significant; against its round2 of 0.04
		// (λ = 0.16, P ≈ 0.0115) it would not be.
		{"Poisson uses the unrounded baseline rate", func(b *dxBandCondition) {
			b.BaselineActivity = 0.04
			b.baselineRate = 0.035
		}, true, "surprise"},
		{"rounded baseline rate alone is not significant", func(b *dxBandCondition) {
			b.BaselineActivity = 0.04
		}, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := surpriseBand()
			tc.mutate(&b)
			rec, ok := classifyHotBand(b, tc.trusted, liveSpanMin)
			got := ""
			if ok {
				got = rec.Kind
			}
			if got != tc.wantKind {
				t.Errorf("kind = %q, want %q (rec %+v)", got, tc.wantKind, rec)
			}
			if ok && rec.Priority != "high" {
				t.Errorf("priority = %q, want high", rec.Priority)
			}
		})
	}
}

// dxSurgeBand is a band whose dx_conditions reach reads "longer" at ×2 with a
// long live p90: a dx_surge from ReachLevel alone, no per-band baseline query.
func dxSurgeBand() dxBandCondition {
	return dxBandCondition{
		Band:                  "17m",
		Status:                "green",
		Score:                 60,
		CurrentLinks:          40,
		SpotsPerMinute:        2.0,
		BaselineActivity:      1.8,
		ClusterBaselineUsed:   true,
		ActivityLevel:         activityLevelNormal,
		ActivityRatio:         1.1,
		P90DistanceKm:         9000,
		BaselineP90DistanceKm: 4500,
		ReachRatio:            2.0,
		ReachLevel:            "longer",
		Trend:                 "stable",
		Sparkline:             []float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 40, 60, 90},
	}
}

func TestClassifyHotBandDxSurgeFromReach(t *testing.T) {
	b := dxSurgeBand()
	rec, ok := classifyHotBand(b, true, 15)
	if !ok || rec.Kind != "dx_surge" {
		t.Fatalf("got (%+v, %v), want a dx_surge", rec, ok)
	}
	if rec.BaselineP90Distance != 4500 || rec.DistanceRatio != 2.0 || rec.RankScore != 2.0 {
		t.Errorf("baseline_p90/distance_ratio/rank = %v/%v/%v, want 4500/2/2 (from dx_conditions reach)",
			rec.BaselineP90Distance, rec.DistanceRatio, rec.RankScore)
	}

	cases := []struct {
		name   string
		mutate func(b *dxBandCondition)
	}{
		{"reach typical", func(b *dxBandCondition) { b.ReachLevel = "typical" }},
		{"reach unsupported", func(b *dxBandCondition) { b.ReachLevel = ""; b.ReachRatio = 0 }},
		{"reach ratio below 1.5", func(b *dxBandCondition) { b.ReachRatio = 1.4 }},
		{"live p90 under 5000 km", func(b *dxBandCondition) { b.P90DistanceKm = 4900 }},
		{"no cluster baseline", func(b *dxBandCondition) { b.ClusterBaselineUsed = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := dxSurgeBand()
			tc.mutate(&b)
			if rec, ok := classifyHotBand(b, true, 15); ok {
				t.Errorf("unexpected recommendation %+v", rec)
			}
		})
	}
	if rec, ok := classifyHotBand(dxSurgeBand(), false, 15); ok {
		t.Errorf("untrusted baseline: unexpected recommendation %+v", rec)
	}
}

func TestPoissonUpperTail(t *testing.T) {
	cases := []struct {
		k      int
		lambda float64
		want   float64
	}{
		{0, 3, 1},
		{-1, 3, 1},
		{1, 0, 0},
		{1, 1, 1 - math.Exp(-1)},
		{2, 0.4, 1 - math.Exp(-0.4)*1.4},
		{3, 5, 1 - math.Exp(-5)*(1+5+12.5)},
		// Deep upper tail: P(X≥12 | 0.75) ≈ 0.75^12 e^-0.75 / 12! × (1 + …).
		{12, 0.75, math.Exp(-0.75) * math.Pow(0.75, 12) / 479001600 * (1 + 0.75/13 + 0.75*0.75/(13*14) + 0.75*0.75*0.75/(13*14*15))},
	}
	for _, tc := range cases {
		got := poissonUpperTail(tc.k, tc.lambda)
		if math.Abs(got-tc.want) > 1e-9*math.Max(1, tc.want) && math.Abs(got-tc.want)/math.Max(tc.want, 1e-300) > 1e-6 {
			t.Errorf("poissonUpperTail(%d, %v) = %g, want %g", tc.k, tc.lambda, got, tc.want)
		}
	}
	// Monotone in k.
	prev := 1.0
	for k := 0; k <= 30; k++ {
		p := poissonUpperTail(k, 6)
		if p > prev+1e-12 {
			t.Fatalf("not monotone at k=%d: %g > %g", k, p, prev)
		}
		prev = p
	}
}

// hotBandsTestCond is an Evaluate result with five rising bands (two outside
// the HF dare set) whose scores rank the non-HF ones first.
func hotBandsTestCond() dxConditionsResponse {
	bands := []dxBandCondition{}
	for i, band := range []string{"2m", "160m", "20m", "15m", "10m", "40m"} {
		b := risingBand()
		b.Band = band
		b.Score = float64(90 - i*5)
		bands = append(bands, b)
	}
	// A quiet band: present in the evaluation, not a recommendation.
	quiet := risingBand()
	quiet.Band = "17m"
	quiet.Trend = "stable"
	quiet.Status = "yellow"
	quiet.SpotsPerMinute = 0.7
	quiet.ActivityLevel = activityLevelBelow
	bands = append(bands, quiet)
	return dxConditionsResponse{
		QTH:              "JO32",
		CurrentSlotOfDay: 24,
		BaselineHistoryM: 30 * 24 * 60,
		Bands:            bands,
		liveSpanMin:      15,
	}
}

func TestHotBandsFromConditionsDefaultShape(t *testing.T) {
	resp := hotBandsFromConditions(hotBandsTestCond(), "", hotBandsOptions{})
	if got := bandsOf(resp.Recommendations); fmt.Sprint(got) != "[2m 160m 20m]" {
		t.Errorf("default recommendations = %v, want [2m 160m 20m] (top 3 by score)", got)
	}
	if resp.Holding != nil {
		t.Errorf("default response carries holding: %+v", resp.Holding)
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["holding"]; ok {
		t.Errorf("default JSON has a holding key: %s", raw)
	}
}

func TestHotBandsFromConditionsBandsFilterBeforeCap(t *testing.T) {
	hf := map[string]bool{}
	for _, b := range parseHotBandsList("80m,40m,30m,20m,17m,15m,12m,10m,6m") {
		hf[b] = true
	}

	resp := hotBandsFromConditions(hotBandsTestCond(), "", hotBandsOptions{Bands: hf})
	if got := bandsOf(resp.Recommendations); fmt.Sprint(got) != "[20m 15m 10m]" {
		t.Errorf("bands=hf recommendations = %v, want [20m 15m 10m] (filter before the 3-cap)", got)
	}

	resp = hotBandsFromConditions(hotBandsTestCond(), "", hotBandsOptions{Bands: hf, Max: 10})
	if got := bandsOf(resp.Recommendations); fmt.Sprint(got) != "[20m 15m 10m 40m]" {
		t.Errorf("bands=hf&max=10 recommendations = %v, want [20m 15m 10m 40m]", got)
	}

	resp = hotBandsFromConditions(hotBandsTestCond(), "", hotBandsOptions{Max: 1})
	if got := bandsOf(resp.Recommendations); fmt.Sprint(got) != "[2m]" {
		t.Errorf("max=1 recommendations = %v, want [2m]", got)
	}

	// current_band still excludes from recommendations.
	resp = hotBandsFromConditions(hotBandsTestCond(), "20m", hotBandsOptions{Bands: hf, Max: 10})
	if got := bandsOf(resp.Recommendations); fmt.Sprint(got) != "[15m 10m 40m]" {
		t.Errorf("current_band=20m recommendations = %v, want [15m 10m 40m]", got)
	}
}

func TestHotBandsFromConditionsHolding(t *testing.T) {
	opts := hotBandsOptions{Include: parseHotBandsList("17m,20m,12m,13cm")}
	resp := hotBandsFromConditions(hotBandsTestCond(), "20m", opts)

	// 17m (quiet, not a recommendation) and 20m (current_band, excluded from
	// recommendations) are held; 12m is absent from the evaluation, 13cm is
	// out of scope.
	if len(resp.Holding) != 2 {
		t.Fatalf("holding = %+v, want 17m and 20m", resp.Holding)
	}
	h := resp.Holding[0]
	if h.Band != "17m" || h.Status != "yellow" || h.SpotsPerMinute != 0.7 || h.ActivityLevel != activityLevelBelow {
		t.Errorf("holding[0] = %+v, want 17m yellow 0.7 below", h)
	}
	if resp.Holding[1].Band != "20m" || resp.Holding[1].Status != "green" {
		t.Errorf("holding[1] = %+v, want 20m green", resp.Holding[1])
	}
	for _, r := range resp.Recommendations {
		if r.Band == "20m" {
			t.Errorf("current_band 20m leaked into recommendations: %v", bandsOf(resp.Recommendations))
		}
	}

	// Holding does not depend on bands=.
	opts.Bands = map[string]bool{"40m": true}
	resp = hotBandsFromConditions(hotBandsTestCond(), "", opts)
	if len(resp.Holding) != 2 {
		t.Errorf("holding with bands=40m = %+v, want 17m and 20m", resp.Holding)
	}
}

func TestParseHotBandsList(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"", "[]"},
		{"20m,15m", "[20m 15m]"},
		{" 20M , 20m,15 ,,", "[20m 15m]"},
		{"13cm,foo,6m", "[6m]"},
	}
	for _, tc := range cases {
		if got := fmt.Sprint(parseHotBandsList(tc.raw)); got != tc.want {
			t.Errorf("parseHotBandsList(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestParseHotBandsOptions(t *testing.T) {
	parse := func(raw string) hotBandsOptions {
		t.Helper()
		q, err := url.ParseQuery(raw)
		if err != nil {
			t.Fatal(err)
		}
		return parseHotBandsOptions(q)
	}

	if opts := parse("qth=JO32&current_band=20m"); opts.Bands != nil || opts.Max != 0 || opts.Include != nil {
		t.Errorf("no opt-in params: %+v, want the zero value", opts)
	}
	opts := parse("bands=20m,15m,13cm&max=10&include=17m,bogus")
	if len(opts.Bands) != 2 || !opts.Bands["20m"] || !opts.Bands["15m"] {
		t.Errorf("bands = %v, want {20m 15m}", opts.Bands)
	}
	if opts.Max != 10 {
		t.Errorf("max = %d, want 10", opts.Max)
	}
	if fmt.Sprint(opts.Include) != "[17m]" {
		t.Errorf("include = %v, want [17m]", opts.Include)
	}
	for raw, want := range map[string]int{"max=0": 1, "max=-3": 1, "max=99": 12, "max=1": 1, "max=abc": 0, "max=": 0} {
		if got := parse(raw).Max; got != want {
			t.Errorf("%s: Max = %d, want %d", raw, got, want)
		}
	}
	// Empty or all-unknown lists leave the knob off.
	if opts := parse("bands=13cm,foo&include="); opts.Bands != nil || opts.Include != nil {
		t.Errorf("unknown-only lists: %+v, want no filter and no include", opts)
	}
}

func TestClampHotBandsMax(t *testing.T) {
	for in, want := range map[int]int{-5: 1, 0: 1, 1: 1, 10: 10, 12: 12, 99: 12} {
		if got := clampHotBandsMax(in); got != want {
			t.Errorf("clampHotBandsMax(%d) = %d, want %d", in, got, want)
		}
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

// seedHotBandsRisingEngine is an in-memory baseline engine seeded with the
// 15m fixture described on TestHotBandsRisingRecFromSeededBaseline.
func seedHotBandsRisingEngine(now int64) *DxBaselineEngine {
	slot := utcSlotOfDay(now)
	e := newDxBaselineEngine("")
	e.mu.Lock()
	// JO62's grid-cluster baseline, in the current slot and the one before
	// it (a 15-min window can straddle the boundary), so the regional
	// activity ratio is available the way it is in production.
	for _, sl := range []int{slot, (slot + SlotsOfDay - 1) % SlotsOfDay} {
		e.clusterBuckets[baselineClusterKey("JN68", "15m", sl, 3, 0)] = &baselineBucket{Band: "15m", SlotOfDay: sl, DistanceTier: 3, SnrTier: 0, Count: 1600}
		e.clusterBuckets[baselineClusterKey("JN68", "15m", sl, 3, 1)] = &baselineBucket{Band: "15m", SlotOfDay: sl, DistanceTier: 3, SnrTier: 1, Count: 400}
	}
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
	return e
}

// seededHotBandsHistory is the fixture's live window: 40 15m spots from
// JO62QM, 90 s old.
func seededHotBandsHistory(now int64) []MQTTMessage {
	history := make([]MQTTMessage, 0, 40)
	for i := 0; i < 40; i++ {
		history = append(history, MQTTMessage{
			T: now - 90, SC: fmt.Sprintf("DK%dABC", i), RC: fmt.Sprintf("W%dXYZ", i),
			SL: "JO62QM", RL: "FN31AA", RP: -10, B: "15m", MD: "FT8", Source: "mqtt",
		})
	}
	return history
}

// TestHotBandsRisingRecFromSeededBaseline is an end-to-end run of HotBands
// against a seeded in-memory baseline: a 15m band with a rising live sparkline
// and modest baseline support must surface exactly one "rising" recommendation.
// Baseline buckets, the observed-event ring, and the event span are seeded
// directly (package-internal access) so no Postgres or Observe funnel is
// involved.
//
// Seeded shape, at slot = utcSlotOfDay(now):
//   - JN68 cluster 15m baseline: 1600 events in SNR tier 0 + 400 in tier 1
//     (distance tier 3) → baselineActivity ≈ 1.09 spots/min over a 61-day span
//   - event ring: 8/14/18 spots in the last three 75s bins of a 15-min window
//     → normalized sparkline 44.44/77.78/100 (3 sustained bins, trend
//     "rising" with trend_delta ≈ 3.7, clearing the tightened rising gates)
//   - live history: 40 spots over the 15-min window → 2.67 spots/min live rate
func TestHotBandsRisingRecFromSeededBaseline(t *testing.T) {
	const now = int64(1700000000)
	e := seedHotBandsRisingEngine(now)

	history := seededHotBandsHistory(now)

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

	// The production path is Evaluate + classifyHotBand: the same band
	// condition fed straight to the classifier yields the same rec.
	cond := e.Evaluate("JO62qm", false, 15, -24, history, now)
	var band15 dxBandCondition
	for _, b := range cond.Bands {
		if b.Band == "15m" {
			band15 = b
		}
	}
	direct, ok := classifyHotBand(band15, cond.BaselineHistoryM >= hotBandsMinHistoryMinutes, cond.liveSpanMin)
	if !ok || direct != rec {
		t.Errorf("classifyHotBand = (%+v, %v), want the HotBands rec %+v", direct, ok, rec)
	}
	// The span hot_bands reads off Evaluate is the one spots_per_minute
	// was divided by, so the Poisson expectation and the count agree.
	if got := float64(band15.CurrentLinks) / cond.liveSpanMin; math.Abs(got-band15.SpotsPerMinute) > 0.005 {
		t.Errorf("current_links / liveSpanMin = %v, want spots_per_minute %v", got, band15.SpotsPerMinute)
	}
}

// TestHotBandsSurpriseFromSeededBaseline runs the surprise class through a
// real Evaluate: a quiet 10m cluster baseline (~0.05 spots/min, too thin for
// a regional ratio) and a burst of long-haul live spots. The recommendation
// can only come from the fallback path, i.e. the Poisson gate fed by
// Evaluate's own CurrentLinks, unrounded baseline rate and live span.
func TestHotBandsSurpriseFromSeededBaseline(t *testing.T) {
	const now = int64(1700000000)
	slot := utcSlotOfDay(now)
	e := newDxBaselineEngine("")
	e.mu.Lock()
	for _, sl := range []int{slot, (slot + SlotsOfDay - 1) % SlotsOfDay} {
		e.clusterBuckets[baselineClusterKey("JN68", "10m", sl, 3, 0)] = &baselineBucket{Band: "10m", SlotOfDay: sl, DistanceTier: 3, SnrTier: 0, Count: 92}
	}
	events := make([]dxObservedEvent, 0, 30)
	for bin, age := range []int64{200, 120, 30} {
		for i := 0; i < 10; i++ {
			events = append(events, dxObservedEvent{T: now - age, B: "10m", SC: fmt.Sprintf("D%d%dAB", bin, i), RC: fmt.Sprintf("W%d%dXY", bin, i), SL: "JO62QM", RL: "FN31AA", RP: -10})
		}
	}
	e.events = events
	e.firstEventAt = now - 61*86400
	e.lastEventAt = now
	e.mu.Unlock()

	history := make([]MQTTMessage, 0, 40)
	for i := 0; i < 40; i++ {
		history = append(history, MQTTMessage{
			T: now - 90, SC: fmt.Sprintf("DK%dABC", i), RC: fmt.Sprintf("W%dXYZ", i),
			SL: "JO62QM", RL: "FN31AA", RP: -10, B: "10m", MD: "FT8", Source: "mqtt",
		})
	}

	cond := e.Evaluate("JO62qm", false, 15, -24, history, now)
	var band10 dxBandCondition
	for _, b := range cond.Bands {
		if b.Band == "10m" {
			band10 = b
		}
	}
	if band10.Band == "" {
		t.Fatalf("10m missing from Evaluate: %+v", cond.Bands)
	}
	if band10.baselineRate <= 0 || round2(band10.baselineRate) != band10.BaselineActivity {
		t.Errorf("baselineRate = %v, want the unrounded BaselineActivity %v", band10.baselineRate, band10.BaselineActivity)
	}
	if got := float64(band10.CurrentLinks) / cond.liveSpanMin; math.Abs(got-band10.SpotsPerMinute) > 0.005 {
		t.Errorf("current_links / liveSpanMin = %v, want spots_per_minute %v", got, band10.SpotsPerMinute)
	}

	resp := e.HotBands("JO62qm", false, 15, -24, "all", history, now)
	if len(resp.Recommendations) != 1 {
		t.Fatalf("expected exactly 1 recommendation, got %+v (10m condition %+v)", resp.Recommendations, band10)
	}
	rec := resp.Recommendations[0]
	if rec.Band != "10m" || rec.Kind != "surprise" || rec.Priority != "high" {
		t.Errorf("rec = %+v, want a high-priority 10m surprise", rec)
	}
	if rec.ActivityLevel != "" {
		t.Errorf("activity_level = %q, want empty (fallback path, so the Poisson gate decided)", rec.ActivityLevel)
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

	e := seedHotBandsRisingEngine(now)
	dxBaseline = e

	hub.Lock()
	origHistory := hub.history
	hub.history = seededHotBandsHistory(now)
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

	if _, ok := decoded["holding"]; ok {
		t.Errorf("default response must not carry holding: %v", decoded["holding"])
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

// TestHotBandsHandlerOptInParams drives bands=/max=/include= through the
// handler against the seeded 15m fixture. The fixture has a single
// recommendation, so max= clamping is pinned by TestParseHotBandsOptions.
func TestHotBandsHandlerOptInParams(t *testing.T) {
	origBaseline := dxBaseline
	defer func() { dxBaseline = origBaseline }()

	now := time.Now().Unix()
	slot := utcSlotOfDay(now)
	dxBaseline = seedHotBandsRisingEngine(now)

	hub.Lock()
	origHistory := hub.history
	hub.history = seededHotBandsHistory(now)
	hub.Unlock()
	defer func() {
		hub.Lock()
		hub.history = origHistory
		hub.Unlock()
	}()

	server := httptest.NewServer(http.HandlerFunc(hotBandsHandler))
	defer server.Close()

	get := func(query string) hotBandsResponse {
		t.Helper()
		httpResp, err := http.Get(server.URL + "/api/hot_bands?qth=JO62qm&minutes=15&" + query)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer httpResp.Body.Close()
		if httpResp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", query, httpResp.StatusCode)
		}
		var decoded hotBandsResponse
		if err := json.NewDecoder(httpResp.Body).Decode(&decoded); err != nil {
			t.Fatalf("decode JSON: %v", err)
		}
		return decoded
	}

	hf := get("bands=80m,40m,30m,20m,17m,15m,12m,10m,6m&max=10")
	outside := get("bands=20m,17m")
	unknownOnly := get("bands=13cm,foo&max=0")
	// current_band=15m drops it from recommendations but not from holding.
	held := get("current_band=15m&include=15m,20m,bogus")

	if utcSlotOfDay(time.Now().Unix()) != slot {
		t.Skipf("UTC slot boundary crossed during test")
	}

	if got := bandsOf(hf.Recommendations); fmt.Sprint(got) != "[15m]" {
		t.Errorf("bands=hf&max=10: recommendations = %v, want [15m]", got)
	}
	if hf.Holding != nil {
		t.Errorf("no include: holding = %+v, want absent", hf.Holding)
	}
	if len(outside.Recommendations) != 0 {
		t.Errorf("bands=20m,17m: recommendations = %v, want none", bandsOf(outside.Recommendations))
	}
	// Unknown bands are ignored, leaving no filter.
	if got := bandsOf(unknownOnly.Recommendations); fmt.Sprint(got) != "[15m]" {
		t.Errorf("bands=13cm,foo&max=0: recommendations = %v, want [15m]", got)
	}
	if len(held.Recommendations) != 0 {
		t.Errorf("current_band=15m: recommendations = %v, want none", bandsOf(held.Recommendations))
	}
	if len(held.Holding) != 1 || held.Holding[0].Band != "15m" || held.Holding[0].SpotsPerMinute != 2.67 {
		t.Errorf("include=15m,20m,bogus: holding = %+v, want just 15m at 2.67 spm", held.Holding)
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
