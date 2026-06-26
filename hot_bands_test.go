package main

import (
	"math"
	"testing"
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
				Target:           "JO22",
				CurrentSlotOfDay: 24,
				BaselineHistoryM: 30 * 24 * 60, // trusted
				Bands: []dxBandCondition{{
					Band:               tc.band,
					Status:             tc.status,
					Score:              60,
					SpotsPerMinute:     tc.live,
					BaselineActivity:   tc.baseAct,
					TargetBaselineUsed: true,
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
		case b.TargetBaselineUsed && trustedBaseline &&
			b.BaselineActivity > 0 && b.BaselineActivity <= hotBandsSurpriseBaselineMx &&
			ratio >= hotBandsSurpriseRatio:
			out = append(out, hotBandRecommendation{Band: b.Band, Kind: "surprise", Priority: "high"})
		case b.TargetBaselineUsed && trustedBaseline && bpUsed && bp > 0 &&
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
