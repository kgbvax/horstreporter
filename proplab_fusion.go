package main

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

// proplab_fusion.go implements the Propagation Lab "Fusion" variant (C).
// It layers robust conditional-quantile statistics over the same midpoint-cell
// buckets used by the Ladder engine, then fuses in three external signals:
//   1. Space-weather indices (Kp, F10.7, GOES X-ray, D-RAP absorption, OVATION
//      auroral power) to type WHY a band is closed.
//   2. An event calendar (contests, DXpeditions, POTA activations) to explain
//      demand-side spikes instead of mistaking them for propagation openings.
//   3. A propagation-prior seam (currently a no-op; dvoacap/VOACAP can be
//      plugged in later) for forecast-style MUF expectation.
//
// The core design principle: live observations always override the prior. A
// physics model may say a band is closed, but if enough distinct links with
// good SNR are observed, the verdict is open anyway.

// propagationPrior is the phase-2 seam for external prediction inputs.
// Implementations can return a probability [0,1] that a band is open from the
// target locator at a given UTC slot. A false second return means no prediction
// is available and the engine should rely purely on observations.
type propagationPrior interface {
	MUFProbability(band string, targetLat, targetLng float64, slot int) (float64, bool)
}

type noopPropagationPrior struct{}

func (noopPropagationPrior) MUFProbability(string, float64, float64, int) (float64, bool) {
	return 0, false
}

// proplabParamsC holds tunable parameters for variant C.
type proplabParamsC struct {
	LookbackDays     int     `json:"lookback_days"`
	QuantileLo       float64 `json:"quantile_lo"`
	QuantileHi       float64 `json:"quantile_hi"`
	GuardbandSigma   float64 `json:"guardband_sigma"`
	GuardbandWeight  float64 `json:"guardband_weight"`
	WitnessPerDayMin float64 `json:"witness_per_day_min"`
	OpenRatio        float64 `json:"open_ratio"`
	ClosedRatio      float64 `json:"closed_ratio"`
	KpAbsorb         float64 `json:"kp_absorb"`
	SfiLow           float64 `json:"sfi_low"`
	XrayMinClass     string  `json:"xray_min_class"`
}

func defaultProplabParamsC() proplabParamsC {
	return proplabParamsC{
		LookbackDays:     45,
		QuantileLo:       0.25,
		QuantileHi:       0.75,
		GuardbandSigma:   3.0,
		GuardbandWeight:  0.25,
		WitnessPerDayMin: 5.0,
		OpenRatio:        2.0,
		ClosedRatio:      0.5,
		KpAbsorb:         5.0,
		SfiLow:           70.0,
		XrayMinClass:     "M1",
	}
}

// fusionBandCount is the live-window observation for one band-region cell.
type fusionBandCount struct {
	Band          string
	Region        string
	SpotCount     int
	LinkCount     int
	ReporterCount int
	DistMaxKm     int
}

// fusionSWSnapshot is the latest space-weather state.
type fusionSWSnapshot struct {
	Kp          float64
	SFI         float64
	XrayClass   string // e.g. "M5.2" or "C1.0" or ""
	AuroraGW    float64 // hemispheric power in GW
	DrapHAF     map[string]float64
	DrapAgeMin  int
	Available   bool
	HasDrap     bool
	FetchedAt   int64
}

// fusionEvent is an active demand-side event.
type fusionEvent struct {
	Source   string
	Title    string
	BandMask string
	Locator4 string
	Region   string
	EndsInMin int
}

// fusionBandVerdict is one band's classification from variant C.
type fusionBandVerdict struct {
	Band           string
	Region         string
	State          string // see proplabVerdictState constants
	Label          string
	Reason         string
	Confidence     float64
	SpotsPerMinute float64
	LinksPerMinute float64
	BaselineP50    float64
	ActivityRatio  float64
	ClosureType    string // muf_limited | absorption_limited | auroral | ""
	ExplainedBy    []string
}

// proplabVerdictState values are shared across variants where applicable.
const (
	proplabStateOpenConfirmed    = "open_confirmed"
	proplabStateOpenUnconfirmed  = "open_unconfirmed"
	proplabStateClosedButActive  = "closed_but_active"
	proplabStateClosedWithCause  = "closed_with_cause"
	proplabStateClosed           = "closed"
	proplabStateInsufficientData = "insufficient_data"
)

// fusionVerdict is the full variant-C result for a target.
type fusionVerdict struct {
	GeneratedAt   int64
	Params        proplabParamsC
	Bands         []fusionBandVerdict
	Prior         propagationPrior
	DataThin      bool
	SWAvailable   bool
	EventsActive  int
}

// fusionEngine holds optional caches; the core computation is stateless per
// request so it is easy to test and replay.
type fusionEngine struct {
	prior propagationPrior
}

func newFusionEngine() *fusionEngine {
	return &fusionEngine{prior: noopPropagationPrior{}}
}

func (e *fusionEngine) SetPrior(p propagationPrior) {
	if p == nil {
		p = noopPropagationPrior{}
	}
	_e := *e
	_e.prior = p
}

// Verdict evaluates variant C for the supplied live counts and context.
// baselineRows are the historical daily counts per (band, region, slot);
// events are currently-active demand-side events; sw is the latest space
// weather; prior may be nil.
func (e *fusionEngine) Verdict(live []fusionBandCount, baselineRows []proplabBaselineDayRow, events []fusionEvent, sw fusionSWSnapshot, prior propagationPrior, params proplabParamsC, now int64) fusionVerdict {
	if prior == nil {
		prior = e.prior
	}
	resp := fusionVerdict{
		GeneratedAt:  now,
		Params:       params,
		Prior:        prior,
		SWAvailable:  sw.Available,
		EventsActive: len(events),
		Bands:        []fusionBandVerdict{},
	}

	if len(live) == 0 {
		resp.DataThin = true
		return resp
	}

	// Group baseline rows by (band, region, slot).
	baselineMap := groupBaselineRows(baselineRows)
	slot := utcSlotOfDay(now)

	for _, cur := range live {
		v := e.classify(cur, baselineMap, events, sw, prior, params, slot)
		resp.Bands = append(resp.Bands, v)
	}
	return resp
}

func (e *fusionEngine) classify(cur fusionBandCount, baselineMap map[string][]proplabBaselineDayRow, events []fusionEvent, sw fusionSWSnapshot, prior propagationPrior, params proplabParamsC, slot int) fusionBandVerdict {
	v := fusionBandVerdict{Band: cur.Band, Region: cur.Region}
	key := fusionBaselineKey(cur.Band, cur.Region, slot)
	rows := baselineMap[key]

	windowMin := 20.0 // default fusion window is 20 min
	v.SpotsPerMinute = float64(cur.SpotCount) / windowMin
	v.LinksPerMinute = float64(cur.LinkCount) / windowMin

	if len(rows) == 0 {
		v.State = proplabStateInsufficientData
		v.Label = "insufficient data"
		v.Reason = "no historical baseline for this band/region/slot yet"
		v.Confidence = 0.0
		return v
	}

	_, p50PerDay, _, _ := weightedBaselineStats(rows, params)
	const dayMinutes = 24 * 60
	baselinePerMin := p50PerDay / dayMinutes
	v.BaselineP50 = baselinePerMin
	if baselinePerMin > 0 {
		v.ActivityRatio = v.LinksPerMinute / baselinePerMin
	}

	// Witness gate: scale current distinct reporters to a full-day equivalent.
	windowToDay := dayMinutes / windowMin
	witnessPerDay := float64(cur.ReporterCount) * windowToDay
	witnessOK := witnessPerDay >= params.WitnessPerDayMin || p50PerDay <= 0

	// Demand-side event match.
	explainedBy := eventExplanations(cur.Band, cur.Region, events)

	// Prior probability (dvoacap seam).
	priorProb, priorOK := prior.MUFProbability(cur.Band, 0, 0, slot)

	// Decide. Demand-side event explanation takes precedence over "open" so
	// contest pileups are not mistaken for propagation openings. Live data still
	// wins when no known event matches.
	switch {
	case v.LinksPerMinute >= baselinePerMin && len(explainedBy) > 0:
		v.State = proplabStateClosedButActive
		v.Label = "active (explained)"
		v.Reason = "link rate elevated but matched to known event(s)"
		v.Confidence = 0.6
		v.ExplainedBy = explainedBy
	case v.LinksPerMinute >= params.OpenRatio*baselinePerMin:
		if witnessOK {
			v.State = proplabStateOpenConfirmed
			v.Label = "open"
			v.Reason = "links well above seasonal baseline; witnesses adequate"
			v.Confidence = 0.85
		} else {
			v.State = proplabStateOpenUnconfirmed
			v.Label = "open?"
			v.Reason = "links high but reporter density too low to confirm"
			v.Confidence = 0.45
		}
	case v.LinksPerMinute <= params.ClosedRatio*baselinePerMin:
		v.State = proplabStateClosedWithCause
		v.Label = "closed"
		v.ClosureType = e.closureCause(cur, sw, priorProb, priorOK, params)
		v.Reason = causeReason(v.ClosureType, sw.Available)
		v.Confidence = 0.6
	default:
		v.State = proplabStateClosed
		v.Label = "closed"
		v.Reason = "activity within normal range"
		v.Confidence = 0.5
	}

	// If the prior disagrees strongly with the observation, note it but live
	// data wins. (A band the model says is closed but that shows real links is
	// still open — this is the DXRadar rule.)
	if priorOK && v.State == proplabStateOpenConfirmed && priorProb < 0.2 {
		v.Reason += " (observation overrides low prior)"
	}

	return v
}

func (e *fusionEngine) closureCause(cur fusionBandCount, sw fusionSWSnapshot, priorProb float64, priorOK bool, params proplabParamsC) string {
	if !sw.Available {
		return ""
	}
	// Absorption-limited: D-RAP says the band is affected, or strong X-ray on
	// a sunlit path, or Kp is elevated.
	bandLower := bandLowerEdgeMHz(cur.Band)
	if sw.HasDrap && sw.DrapHAF != nil {
		if haf, ok := sw.DrapHAF[cur.Region]; ok && haf > 0 && bandLower <= haf {
			return "absorption_limited"
		}
	}
	if sw.XrayClass != "" {
		if xrayMagnitude(sw.XrayClass) >= xrayMagnitude(params.XrayMinClass) {
			return "absorption_limited"
		}
	}
	if sw.Kp >= params.KpAbsorb {
		return "absorption_limited"
	}
	// Auroral degradation on high-latitude regions.
	if sw.AuroraGW >= 50 {
		if lat, _, ok := latLngFromRegionName(cur.Region); ok && math.Abs(lat) > 55 {
			return "auroral"
		}
	}
	// MUF-limited: low solar flux and the band is relatively high.
	if sw.SFI <= params.SfiLow {
		if cur.Band == "15m" || cur.Band == "12m" || cur.Band == "10m" || cur.Band == "6m" {
			return "muf_limited"
		}
	}
	if priorOK && priorProb < 0.1 {
		return "muf_limited"
	}
	return ""
}

func causeReason(cause string, swAvailable bool) string {
	switch cause {
	case "absorption_limited":
		return "absorption-limited (D-layer/Kp/X-ray)"
	case "muf_limited":
		return "MUF-limited (solar flux / prior)"
	case "auroral":
		return "auroral degradation"
	default:
		if swAvailable {
			return "closed (no typed cause)"
		}
		return "closed (SW feed off; cause unknown)"
	}
}

// weightedBaselineStats computes quantiles from daily counts, downweighting
// days that were themselves anomalous or overlapped a known event. This is the
// Farrington-style guardband: contest weekends don't inflate the baseline.
func weightedBaselineStats(rows []proplabBaselineDayRow, params proplabParamsC) (p25, p50, p75, mad float64) {
	if len(rows) == 0 {
		return 0, 0, 0, 0
	}
	counts := make([]float64, 0, len(rows))
	weights := make([]float64, 0, len(rows))
	for _, r := range rows {
		c := float64(r.LinkCount)
		w := 1.0
		// Guardband: downweight anomalous historical days. We don't have the
		// event overlap per row here, so we approximate by statistical outlier
		// detection after computing a first-pass median/MAD.
		counts = append(counts, c)
		weights = append(weights, w)
	}
	p50 = weightedQuantile(counts, weights, 0.5)
	// Second pass: downweight values > median + sigma*MAD.
	devs := make([]float64, len(counts))
	for i, c := range counts {
		d := math.Abs(c - p50)
		devs[i] = d
	}
	mad = weightedQuantile(devs, weights, 0.5)
	if mad == 0 {
		mad = 1.0
	}
	for i := range weights {
		if counts[i] > p50+params.GuardbandSigma*mad {
			weights[i] = params.GuardbandWeight
		}
	}
	p25 = weightedQuantile(counts, weights, params.QuantileLo)
	p50 = weightedQuantile(counts, weights, 0.5)
	p75 = weightedQuantile(counts, weights, params.QuantileHi)
	return p25, p50, p75, mad
}

func weightedQuantile(values, weights []float64, p float64) float64 {
	n := len(values)
	if n == 0 {
		return 0
	}
	if n != len(weights) {
		weights = make([]float64, n)
		for i := range weights {
			weights[i] = 1.0
		}
	}
	type pair struct{ v, w float64 }
	pairs := make([]pair, n)
	total := 0.0
	for i := range values {
		pairs[i] = pair{values[i], weights[i]}
		total += weights[i]
	}
	if total == 0 {
		return 0
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].v < pairs[j].v })
	target := p * total
	cum := 0.0
	for _, pair := range pairs {
		cum += pair.w
		if cum >= target {
			return pair.v
		}
	}
	return pairs[n-1].v
}

func groupBaselineRows(rows []proplabBaselineDayRow) map[string][]proplabBaselineDayRow {
	out := make(map[string][]proplabBaselineDayRow)
	for _, r := range rows {
		key := fusionBaselineKey(r.Band, r.Region, r.Slot)
		out[key] = append(out[key], r)
	}
	return out
}

func fusionBaselineKey(band, region string, slot int) string {
	return band + "|" + region + "|" + strconv.Itoa(slot)
}

func eventExplanations(band, region string, events []fusionEvent) []string {
	var out []string
	for _, e := range events {
		if e.BandMask != "" && !eventBandMatches(e.BandMask, band) {
			continue
		}
		if e.Region != "" && e.Region != region {
			continue
		}
		out = append(out, e.Title)
	}
	return out
}

func eventBandMatches(mask, band string) bool {
	if mask == "" {
		return true
	}
	band = strings.ToLower(strings.TrimSpace(band))
	mask = strings.ToLower(mask)
	if strings.Contains(mask, band) {
		return true
	}
	// Look for a "80m-10m" style range token, ignoring surrounding text.
	for _, tok := range strings.FieldsFunc(mask, func(r rune) bool { return r == ' ' || r == '/' || r == ',' || r == ';' }) {
		parts := strings.Split(tok, "-")
		if len(parts) != 2 {
			continue
		}
		lo := bandLowerEdgeMHz(strings.TrimSpace(parts[0]))
		hi := bandLowerEdgeMHz(strings.TrimSpace(parts[1]))
		b := bandLowerEdgeMHz(band)
		if lo > 0 && hi > 0 && b > 0 && b >= lo && b <= hi {
			return true
		}
	}
	return false
}

func bandLowerEdgeMHz(band string) float64 {
	m := map[string]float64{
		"160m": 1.8, "80m": 3.5, "60m": 5.3, "40m": 7.0, "30m": 10.1,
		"20m": 14.0, "17m": 18.1, "15m": 21.0, "12m": 24.9, "10m": 28.0,
		"6m": 50.0, "4m": 70.0, "2m": 144.0,
	}
	return m[strings.ToLower(strings.TrimSpace(band))]
}

func xrayMagnitude(class string) float64 {
	class = strings.ToUpper(strings.TrimSpace(class))
	letter := ""
	var mag float64
	if len(class) >= 1 {
		letter = string(class[0])
		if v, err := strconv.ParseFloat(strings.TrimSpace(class[1:]), 64); err == nil {
			mag = v
		}
	}
	switch letter {
	case "A":
		return mag
	case "B":
		return mag * 10
	case "C":
		return mag * 100
	case "M":
		return mag * 1000
	case "X":
		return mag * 10000
	}
	return 0
}

func latLngFromRegionName(region string) (lat, lon float64, ok bool) {
	// Approximate region centroids for auroral-latitude gating.
	centroids := map[string][2]float64{
		"EU": {50, 15}, "NA": {45, -100}, "SA": {-15, -60}, "AF": {0, 20},
		"AS": {35, 100}, "JA": {36, 138}, "OC": {-25, 135}, "VK": {-30, 145},
		"KH6": {20, -157}, "CAR": {18, -75}, "AN": {-80, 0},
	}
	if c, ok := centroids[strings.ToUpper(region)]; ok {
		return c[0], c[1], true
	}
	return 0, 0, false
}
