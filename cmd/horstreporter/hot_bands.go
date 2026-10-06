package main

import (
	"math"
	"net/url"
	"sort"
	"strconv"
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
	hotBandsSurpriseMinP90Km   = 800.0   // a "rare opening" must reach past the local skip zone
	hotBandsSurprisePoissonP   = 0.01    // fallback (no regional ratio): live count must be this unlikely under the baseline rate
	hotBandsDxSurgeRatio       = 1.5     // dx_conditions reach_ratio (live p90 / baseline p90)
	hotBandsDxSurgeMinDistKm   = 5000.0  // suppress low-band 1k→1.5k "surges"
	hotBandsMinTrendDelta      = 1.5     // well above computeTrend's own 0.6 "rising" cut: slope noise on a flat band stays out
	hotBandsRisingRatioMin     = 1.5     // current rate above its own baseline
	hotBandsRisingMinBins      = 3       // rising needs a longer elevated run than the shared 2-bin floor
	hotBandsRisingMinLiveRate  = 1.0     // spots/min — rising on a trickle is noise
	hotBandsMaxResults         = 3
	hotBandsMaxResultsLimit    = 12 // upper clamp for the max= override
)

type hotBandRecommendation struct {
	Band             string  `json:"band"`
	Kind             string  `json:"kind"`     // "surprise" | "dx_surge" | "rising"
	Priority         string  `json:"priority"` // "high" | "normal"
	Reason           string  `json:"reason"`
	RankScore        float64 `json:"rank_score"`
	SpotsPerMinute   float64 `json:"spots_per_minute"`
	BaselineActivity float64 `json:"baseline_activity"`
	ActivityRatio    float64 `json:"activity_ratio"`
	// ActivityLevel is set only when ActivityRatio is Evaluate's like-for-like
	// regional ratio ("above"|"normal"|"below"); empty when the ratio is the
	// fallback your-squares-vs-baseline estimate, which isn't "× normal".
	ActivityLevel       string  `json:"activity_level,omitempty"`
	SustainedBins       int     `json:"sustained_bins"`
	P90DistanceKm       float64 `json:"p90_distance_km"`
	BaselineP90Distance float64 `json:"baseline_p90_distance_km,omitempty"`
	DistanceRatio       float64 `json:"distance_ratio,omitempty"`
	Trend               string  `json:"trend"`
	TrendDelta          float64 `json:"trend_delta"`
	Status              string  `json:"status"`
}

// hotBandHolding is the current state of a band the client asked to track
// via include=, whether or not it still qualifies as a recommendation. It
// lets a client tell "no longer rising" (still open) from "closed".
type hotBandHolding struct {
	Band           string  `json:"band"`
	SpotsPerMinute float64 `json:"spots_per_minute"`
	Status         string  `json:"status"`
	ActivityLevel  string  `json:"activity_level"`
}

type hotBandsResponse struct {
	QTH              string `json:"qth"`
	Surroundings     bool   `json:"surroundings"`
	CurrentBand      string `json:"current_band"`
	CurrentSlotOfDay int    `json:"current_slot_of_day"`
	GeneratedAt      int64  `json:"generated_at"`
	BaselineHistoryM int    `json:"baseline_history_minutes"`
	// Area is the live area the recommendations are scoped to; present only
	// when the request asked for one (rings=auto or rings=N).
	Area            *liveArea               `json:"area,omitempty"`
	Recommendations []hotBandRecommendation `json:"recommendations"`
	// Holding is present only when the request named bands in include=.
	Holding []hotBandHolding `json:"holding,omitempty"`
}

// hotBandsOptions are the opt-in request knobs. The zero value reproduces the
// default response: every in-scope band, hotBandsMaxResults results, no
// holding list.
type hotBandsOptions struct {
	// Bands restricts recommendations to these bands (applied before the
	// cap). Empty: no filter.
	Bands map[string]bool
	// Max overrides hotBandsMaxResults when > 0.
	Max int
	// Include lists bands to report in Holding, in request order.
	Include []string
}

// parseHotBandsList splits a comma list of band labels into normalized,
// in-scope, de-duplicated bands in first-seen order. Unknown and
// out-of-scope labels are dropped.
func parseHotBandsList(raw string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		band := normalizeBand(part)
		if band == "" || !bandInScope(band) || seen[band] {
			continue
		}
		seen[band] = true
		out = append(out, band)
	}
	return out
}

// parseHotBandsOptions reads the opt-in bands=/max=/include= query params.
// Absent, empty or unparseable values leave the matching knob at its zero
// value (the default response).
func parseHotBandsOptions(q url.Values) hotBandsOptions {
	var opts hotBandsOptions
	if bands := parseHotBandsList(q.Get("bands")); len(bands) > 0 {
		opts.Bands = make(map[string]bool, len(bands))
		for _, b := range bands {
			opts.Bands[b] = true
		}
	}
	if raw := strings.TrimSpace(q.Get("max")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			opts.Max = clampHotBandsMax(n)
		}
	}
	if include := parseHotBandsList(q.Get("include")); len(include) > 0 {
		opts.Include = include
	}
	return opts
}

// clampHotBandsMax clamps a requested result cap to 1..hotBandsMaxResultsLimit.
func clampHotBandsMax(n int) int {
	if n < 1 {
		return 1
	}
	if n > hotBandsMaxResultsLimit {
		return hotBandsMaxResultsLimit
	}
	return n
}

// HotBands evaluates the per-band conditions and returns up to
// hotBandsMaxResults recommendations sorted by priority then by class-specific
// rank. An empty recommendations slice is a valid response — the frontend
// hides the indicator in that case.
func (e *DxBaselineEngine) HotBands(qth string, surroundings bool, minutes int, cwMinDb int, currentBand string, history []MQTTMessage, now int64) hotBandsResponse {
	return e.HotBandsArea(qth, surroundings, minutes, cwMinDb, currentBand, history, now, nil)
}

// HotBandsArea is HotBands scoped to a live area (nil: as HotBands).
func (e *DxBaselineEngine) HotBandsArea(qth string, surroundings bool, minutes int, cwMinDb int, currentBand string, history []MQTTMessage, now int64, area *liveArea) hotBandsResponse {
	return e.HotBandsAreaWith(qth, surroundings, minutes, cwMinDb, currentBand, history, now, area, hotBandsOptions{})
}

// HotBandsAreaWith is HotBandsArea with the opt-in bands/max/include knobs.
func (e *DxBaselineEngine) HotBandsAreaWith(qth string, surroundings bool, minutes int, cwMinDb int, currentBand string, history []MQTTMessage, now int64, area *liveArea, opts hotBandsOptions) hotBandsResponse {
	return e.HotBandsAreaWindow(qth, surroundings, minutes, cwMinDb, currentBand, historyWindow{msgs: history}, now, area, opts)
}

// HotBandsAreaWindow is HotBandsAreaWith over a sequence-numbered window (see
// EvaluateAreaWindow).
func (e *DxBaselineEngine) HotBandsAreaWindow(qth string, surroundings bool, minutes int, cwMinDb int, currentBand string, win historyWindow, now int64, area *liveArea, opts hotBandsOptions) hotBandsResponse {
	// Lite: hot_bands reads the scores, rates, distances and series but none of
	// the per-band diagnostics the full evaluation also builds.
	cond := e.evaluateAreaWindow(qth, surroundings, minutes, cwMinDb, win, now, area, true)
	return hotBandsFromConditions(cond, currentBand, opts)
}

// hotBandsFromConditions turns an Evaluate result into the hot-bands
// response: per-band classification, the current_band exclusion, the
// optional bands= filter, the result cap, and the optional holding list.
// The live span comes from the same Evaluate run, so the surprise gate's
// Poisson expectation and the observed count cover the same minutes.
func hotBandsFromConditions(cond dxConditionsResponse, currentBand string, opts hotBandsOptions) hotBandsResponse {
	currentBand = normalizeBand(currentBand)

	resp := hotBandsResponse{
		QTH:              cond.QTH,
		Surroundings:     cond.Surroundings,
		Area:             cond.Area,
		CurrentBand:      currentBand,
		CurrentSlotOfDay: cond.CurrentSlotOfDay,
		GeneratedAt:      cond.GeneratedAt,
		BaselineHistoryM: cond.BaselineHistoryM,
		Recommendations:  []hotBandRecommendation{},
	}

	if cond.QTH == "" || len(cond.Bands) == 0 {
		return resp
	}

	trustedBaseline := cond.BaselineHistoryM >= hotBandsMinHistoryMinutes
	liveSpanMin := math.Max(1, cond.liveSpanMin)

	recs := make([]hotBandRecommendation, 0, len(cond.Bands))
	for _, b := range cond.Bands {
		if currentBand != "" && currentBand != "all" && b.Band == currentBand {
			continue
		}
		if len(opts.Bands) > 0 && !opts.Bands[b.Band] {
			continue
		}
		if rec, ok := classifyHotBand(b, trustedBaseline, liveSpanMin); ok {
			recs = append(recs, rec)
		}
	}

	sortHotBandRecommendations(recs)
	limit := hotBandsMaxResults
	if opts.Max > 0 {
		limit = clampHotBandsMax(opts.Max)
	}
	if len(recs) > limit {
		recs = recs[:limit]
	}
	resp.Recommendations = recs

	if len(opts.Include) > 0 {
		byBand := make(map[string]*dxBandCondition, len(cond.Bands))
		for i := range cond.Bands {
			byBand[cond.Bands[i].Band] = &cond.Bands[i]
		}
		for _, band := range opts.Include {
			b, ok := byBand[band]
			if !ok || !bandInScope(band) {
				continue
			}
			resp.Holding = append(resp.Holding, hotBandHolding{
				Band:           b.Band,
				SpotsPerMinute: b.SpotsPerMinute,
				Status:         b.Status,
				ActivityLevel:  b.ActivityLevel,
			})
		}
	}
	return resp
}

// classifyHotBand decides whether one evaluated band is a hot-band
// recommendation, and of which kind. Pure: everything it needs is on the
// band condition (trustedBaseline: the baseline spans at least
// hotBandsMinHistoryMinutes; liveSpanMin: the minutes spots_per_minute
// covers).
func classifyHotBand(b dxBandCondition, trustedBaseline bool, liveSpanMin float64) (hotBandRecommendation, bool) {
	// Only surface trends for bands in scope (160m–2m). Microwave spots
	// (e.g. 13cm) can leak through normalizeBand and must not appear as
	// "13cm rising".
	if b.Band == "" || !bandInScope(b.Band) {
		return hotBandRecommendation{}, false
	}

	// Unrounded baseline rate; conditions built outside Evaluate (tests,
	// decoded JSON) only carry the rounded one.
	baselineRate := b.baselineRate
	if baselineRate <= 0 {
		baselineRate = b.BaselineActivity
	}

	// Prefer Evaluate's like-for-like regional ratio; your-squares rate
	// over the whole-cluster baseline is only the no-cluster fallback. With
	// neither there is nothing to compare against and the ratio is 0, so no
	// ratio-gated class can fire on the trend alone.
	ratio := activityRatio(b.SpotsPerMinute, baselineRate)
	ratioLevel := ""
	hasRegionalRatio := activityLevelHasRatio(b.ActivityLevel)
	if hasRegionalRatio {
		ratio = b.ActivityRatio
		ratioLevel = b.ActivityLevel
	}
	sustained := sustainedRecentBins(b.Sparkline, hotBandsSparklineFloor)
	if sustained < hotBandsMinSustainedBins {
		return hotBandRecommendation{}, false
	}
	if b.SpotsPerMinute < hotBandsMinLiveRate {
		return hotBandRecommendation{}, false
	}

	// p90_distance_km is dx_conditions' raw percentile; distance_ratio is its
	// reach_ratio, which compares a tier-interpolated live p90 with the
	// baseline's, so the two need not divide to the same figure (and the
	// dx_surge 5000 km floor applies to the raw one).
	rec := hotBandRecommendation{
		Band:                b.Band,
		SpotsPerMinute:      b.SpotsPerMinute,
		BaselineActivity:    b.BaselineActivity,
		ActivityRatio:       round2(ratio),
		ActivityLevel:       ratioLevel,
		SustainedBins:       sustained,
		P90DistanceKm:       b.P90DistanceKm,
		BaselineP90Distance: round1(b.BaselineP90DistanceKm),
		DistanceRatio:       round2(b.ReachRatio),
		Trend:               b.Trend,
		TrendDelta:          b.TrendDelta,
		Status:              b.Status,
	}

	// classify, in priority order — first match wins.
	// The surprise/dx_surge gates accept the cluster baseline — a cluster
	// baseline gives operators with thin qth history the same "unusual
	// opening" detection as those with rich qth history.
	baselineScoped := b.ClusterBaselineUsed
	switch {
	case baselineScoped && trustedBaseline &&
		b.BaselineActivity > 0 && b.BaselineActivity <= hotBandsSurpriseBaselineMx &&
		ratio >= hotBandsSurpriseRatio &&
		b.P90DistanceKm >= hotBandsSurpriseMinP90Km &&
		(hasRegionalRatio || poissonUpperTail(b.CurrentLinks, baselineRate*liveSpanMin) < hotBandsSurprisePoissonP):
		// Without a regional ratio the fallback ratio can be large on a
		// handful of spots; the Poisson tail demands the count itself be
		// improbable at the baseline rate.
		rec.Kind, rec.Priority, rec.Reason, rec.RankScore = "surprise", "high", "rare opening", ratio
		return rec, true

	case baselineScoped && trustedBaseline &&
		b.ReachLevel == "longer" && b.ReachRatio >= hotBandsDxSurgeRatio &&
		b.P90DistanceKm >= hotBandsDxSurgeMinDistKm:
		rec.Kind, rec.Priority, rec.Reason, rec.RankScore = "dx_surge", "normal", "DX surge", b.ReachRatio
		return rec, true

	// A band with no baseline and no regional ratio has ratio 0 and never
	// qualifies: a slope alone can't tell an opening from a busy hour.
	case b.Trend == "rising" && b.TrendDelta >= hotBandsMinTrendDelta &&
		(b.Status == "yellow" || b.Status == "green") &&
		ratio >= hotBandsRisingRatioMin &&
		sustained >= hotBandsRisingMinBins &&
		b.SpotsPerMinute >= hotBandsRisingMinLiveRate:
		rec.Kind, rec.Priority, rec.Reason, rec.RankScore = "rising", "normal", "rising", b.Score
		return rec, true
	}
	return hotBandRecommendation{}, false
}

// poissonUpperTail is P(X ≥ k) for X ~ Poisson(lambda). Sums whichever side
// of the distribution is shorter, in log space for the first term, so it stays
// accurate for the small tails the surprise gate compares against.
func poissonUpperTail(k int, lambda float64) float64 {
	if k <= 0 {
		return 1
	}
	if lambda <= 0 {
		return 0
	}
	logTerm := func(i int) float64 {
		lg, _ := math.Lgamma(float64(i) + 1)
		return -lambda + float64(i)*math.Log(lambda) - lg
	}
	if float64(k) <= lambda {
		// Lower side: 1 − P(X ≤ k−1), k terms.
		term := math.Exp(-lambda)
		sum := term
		for i := 1; i < k; i++ {
			term *= lambda / float64(i)
			sum += term
		}
		return clamp01(1 - sum)
	}
	// Upper side: terms fall off geometrically once i > lambda.
	term := math.Exp(logTerm(k))
	sum := term
	for i := k + 1; term > sum*1e-15 && i < k+10000; i++ {
		term *= lambda / float64(i)
		sum += term
	}
	return clamp01(sum)
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
	// No baseline → no evidence the band is above its normal.
	if baseline <= 0 {
		return 0
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
