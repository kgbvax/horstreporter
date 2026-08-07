package main

import (
	"math"
	"sort"
	"strings"
)

// Tunable thresholds for the hot-bands recommender. All evaluation is
// per-band relative to the target's baseline; falsy targets and bands the
// operator is already on are suppressed.
const (
	hotBandsMinHistoryMinutes  = 24 * 60 // need at least 1 day of baseline before "surprise"/"dx_surge" are trustworthy
	hotBandsMinSustainedBins   = 2       // ≥ N consecutive trailing bins above hotBandsSparklineFloor to count as sustained
	hotBandsSparklineFloor     = 30.0    // bin value (0..100 normalised) considered "elevated"
	hotBandsMinLiveRate        = 0.5     // spots/min — drops noisy 1-spot bursts
	hotBandsSurpriseBaselineMx = 0.1     // expected spots/min ≤ this means band is usually quiet for this target
	hotBandsSurpriseRatio      = 5.0     // current rate must be ≥ N× baseline to count as "rare opening"
	hotBandsDxSurgeRatio       = 1.5     // live p90 distance / baseline p90 distance
	hotBandsDxSurgeMinDistKm   = 5000.0  // suppress low-band 1k→1.5k "surges"
	hotBandsMinTrendDelta      = 0.2     // matches Evaluate's "rising" gate
	hotBandsRisingRatioMin     = 1.2     // current rate above its own baseline
	hotBandsMaxResults         = 3
)

type hotBandRecommendation struct {
	Band                string  `json:"band"`
	Kind                string  `json:"kind"`     // "surprise" | "dx_surge" | "rising"
	Priority            string  `json:"priority"` // "high" | "normal"
	Reason              string  `json:"reason"`
	RankScore           float64 `json:"rank_score"`
	SpotsPerMinute      float64 `json:"spots_per_minute"`
	BaselineActivity    float64 `json:"baseline_activity"`
	ActivityRatio       float64 `json:"activity_ratio"`
	SustainedBins       int     `json:"sustained_bins"`
	P90DistanceKm       float64 `json:"p90_distance_km"`
	BaselineP90Distance float64 `json:"baseline_p90_distance_km,omitempty"`
	DistanceRatio       float64 `json:"distance_ratio,omitempty"`
	Trend               string  `json:"trend"`
	TrendDelta          float64 `json:"trend_delta"`
	Status              string  `json:"status"`
}

type hotBandsResponse struct {
	Target           string                  `json:"target"`
	Surroundings     bool                    `json:"surroundings"`
	CurrentBand      string                  `json:"current_band"`
	CurrentSlotOfDay int                     `json:"current_slot_of_day"`
	GeneratedAt      int64                   `json:"generated_at"`
	BaselineHistoryM int                     `json:"baseline_history_minutes"`
	Recommendations  []hotBandRecommendation `json:"recommendations"`
}

// HotBands evaluates the per-band conditions and returns up to
// hotBandsMaxResults recommendations sorted by priority then by class-specific
// rank. An empty recommendations slice is a valid response — the frontend
// hides the indicator in that case.
func (e *DxBaselineEngine) HotBands(target string, surroundings bool, minutes int, cwMinDb int, currentBand string, history []MQTTMessage, now int64) hotBandsResponse {
	cond := e.Evaluate(target, surroundings, minutes, cwMinDb, history, now)
	currentBand = normalizeBand(currentBand)

	resp := hotBandsResponse{
		Target:           cond.Target,
		Surroundings:     cond.Surroundings,
		CurrentBand:      currentBand,
		CurrentSlotOfDay: cond.CurrentSlotOfDay,
		GeneratedAt:      cond.GeneratedAt,
		BaselineHistoryM: cond.BaselineHistoryM,
		Recommendations:  []hotBandRecommendation{},
	}

	if cond.Target == "" || len(cond.Bands) == 0 {
		return resp
	}

	// Baseline p90 distance lookup is target-scoped, so we need the same
	// block-anchored target list that Evaluate uses internally.
	targets := []string{cond.Target}
	if cond.Surroundings && isLocator(cond.Target) {
		targets = getSurroundingSquares(cond.Target)
	}
	baselineTargets := normalizeTargetsForBaseline(targets)

	trustedBaseline := cond.BaselineHistoryM >= hotBandsMinHistoryMinutes

	recs := make([]hotBandRecommendation, 0, len(cond.Bands))
	for _, b := range cond.Bands {
		if b.Band == "" {
			continue
		}
		// Only surface trends for bands in scope (160m–2m). Microwave spots
		// (e.g. 13cm) can leak through normalizeBand and must not appear as
		// "13cm rising".
		if !bandInScope(b.Band) {
			continue
		}
		if currentBand != "" && currentBand != "all" && b.Band == currentBand {
			continue
		}

		ratio := activityRatio(b.SpotsPerMinute, b.BaselineActivity)
		sustained := sustainedRecentBins(b.Sparkline, hotBandsSparklineFloor)
		if sustained < hotBandsMinSustainedBins {
			continue
		}
		if b.SpotsPerMinute < hotBandsMinLiveRate {
			continue
		}

		baseP90, baseUsed := e.lookupBaselineP90(baselineTargets, cond.OperatorRegion, b.Band, cond.CurrentSlotOfDay)
		distRatio := 0.0
		if baseP90 > 0 {
			distRatio = b.P90DistanceKm / baseP90
		}

		// classify, in priority order — first match wins.
		// The surprise/dx_surge gates accept either a target-specific OR a
		// regional baseline — a regional baseline gives operators with thin
		// target history the same "unusual opening" detection as those with
		// rich target history.
		baselineScoped := b.TargetBaselineUsed || b.RegionalBaselineUsed
		switch {
		case baselineScoped && trustedBaseline &&
			b.BaselineActivity > 0 && b.BaselineActivity <= hotBandsSurpriseBaselineMx &&
			ratio >= hotBandsSurpriseRatio:
			recs = append(recs, hotBandRecommendation{
				Band:                b.Band,
				Kind:                "surprise",
				Priority:            "high",
				Reason:              "rare opening",
				RankScore:           ratio,
				SpotsPerMinute:      b.SpotsPerMinute,
				BaselineActivity:    b.BaselineActivity,
				ActivityRatio:       round2(ratio),
				SustainedBins:       sustained,
				P90DistanceKm:       b.P90DistanceKm,
				BaselineP90Distance: round1(baseP90),
				DistanceRatio:       round2(distRatio),
				Trend:               b.Trend,
				TrendDelta:          b.TrendDelta,
				Status:              b.Status,
			})

		case baselineScoped && trustedBaseline && baseUsed &&
			baseP90 > 0 && distRatio >= hotBandsDxSurgeRatio &&
			b.P90DistanceKm >= hotBandsDxSurgeMinDistKm:
			recs = append(recs, hotBandRecommendation{
				Band:                b.Band,
				Kind:                "dx_surge",
				Priority:            "normal",
				Reason:              "DX surge",
				RankScore:           distRatio,
				SpotsPerMinute:      b.SpotsPerMinute,
				BaselineActivity:    b.BaselineActivity,
				ActivityRatio:       round2(ratio),
				SustainedBins:       sustained,
				P90DistanceKm:       b.P90DistanceKm,
				BaselineP90Distance: round1(baseP90),
				DistanceRatio:       round2(distRatio),
				Trend:               b.Trend,
				TrendDelta:          b.TrendDelta,
				Status:              b.Status,
			})

		case b.Trend == "rising" && b.TrendDelta >= hotBandsMinTrendDelta &&
			(b.Status == "yellow" || b.Status == "green") &&
			ratio >= hotBandsRisingRatioMin:
			recs = append(recs, hotBandRecommendation{
				Band:                b.Band,
				Kind:                "rising",
				Priority:            "normal",
				Reason:              "rising",
				RankScore:           b.Score,
				SpotsPerMinute:      b.SpotsPerMinute,
				BaselineActivity:    b.BaselineActivity,
				ActivityRatio:       round2(ratio),
				SustainedBins:       sustained,
				P90DistanceKm:       b.P90DistanceKm,
				BaselineP90Distance: round1(baseP90),
				DistanceRatio:       round2(distRatio),
				Trend:               b.Trend,
				TrendDelta:          b.TrendDelta,
				Status:              b.Status,
			})
		}
	}

	sortHotBandRecommendations(recs)
	if len(recs) > hotBandsMaxResults {
		recs = recs[:hotBandsMaxResults]
	}
	resp.Recommendations = recs
	return resp
}

func (e *DxBaselineEngine) lookupBaselineP90(targets []string, operatorRegion, band string, slot int) (float64, bool) {
	if e == nil {
		return 0, false
	}
	e.mu.RLock()
	st := e.store
	var globalCopy, targetCopy, regionCopy map[string]*baselineBucket
	if st == nil {
		globalCopy = cloneBuckets(e.buckets)
		targetCopy = cloneBuckets(e.targetBuckets)
		regionCopy = cloneBuckets(e.regionalBuckets)
	}
	e.mu.RUnlock()

	if st != nil {
		km, used, _, err := st.baselineP90DistanceForBand(targets, operatorRegion, band, slot)
		if err != nil {
			return 0, false
		}
		return km, used
	}
	km, used, _ := baselineP90DistanceForBand(globalCopy, targetCopy, regionCopy, operatorRegion, targets, band, slot)
	return km, used
}

// sustainedRecentBins counts the trailing consecutive sparkline bins whose
// value is at or above floor, scanning right-to-left and stopping at the
// first dip. A flash spike (one high bin, neighbours low) returns 1; an
// opening that has been live for 30 min returns 3 etc.
func sustainedRecentBins(sparkline []float64, floor float64) int {
	count := 0
	for i := len(sparkline) - 1; i >= 0; i-- {
		if sparkline[i] >= floor {
			count++
			continue
		}
		break
	}
	return count
}

func activityRatio(live, baseline float64) float64 {
	if baseline <= 0 {
		if live <= 0 {
			return 0
		}
		// No baseline → treat as moderately elevated so the rising class
		// can still fire for never-before-seen bands with target_baseline_used=false.
		return math.Max(1.0, live*2.0)
	}
	return live / baseline
}

func sortHotBandRecommendations(recs []hotBandRecommendation) {
	priorityRank := func(p string) int {
		if p == "high" {
			return 0
		}
		return 1
	}
	kindRank := func(k string) int {
		switch k {
		case "surprise":
			return 0
		case "dx_surge":
			return 1
		case "rising":
			return 2
		}
		return 3
	}
	sort.SliceStable(recs, func(i, j int) bool {
		pi, pj := priorityRank(recs[i].Priority), priorityRank(recs[j].Priority)
		if pi != pj {
			return pi < pj
		}
		ki, kj := kindRank(recs[i].Kind), kindRank(recs[j].Kind)
		if ki != kj {
			return ki < kj
		}
		if recs[i].RankScore != recs[j].RankScore {
			return recs[i].RankScore > recs[j].RankScore
		}
		return strings.Compare(recs[i].Band, recs[j].Band) < 0
	})
}
