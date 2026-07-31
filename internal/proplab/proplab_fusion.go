package proplab

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

// proplab_fusion.go implements the Propagation Lab "Fusion" variant (C).
// It layers robust conditional-quantile statistics over the same midpoint-cell
// buckets used by the Ladder engine, then fuses in three external signals:
//   1. Space-weather indices (Kp, F10.7, GOES X-ray flux, D-RAP absorption,
//      OVATION auroral power) to type WHY a band is closed.
//   2. An event calendar (contests, DXpeditions, POTA activations) to explain
//      demand-side spikes instead of mistaking them for propagation openings.
//   3. A propagation-prior seam (currently a no-op; dvoacap/VOACAP can be
//      plugged in later) for forecast-style MUF expectation.
//
// The core design principle: live observations always override the prior. A
// physics model may say a band is closed, but if enough distinct links with
// good SNR are observed, the verdict is open anyway.

// PropagationPrior is the phase-2 seam for external prediction inputs.
// Implementations can return a probability [0,1] that a band is open from the
// target locator at a given UTC slot. A false second return means no prediction
// is available and the engine should rely purely on observations.
type PropagationPrior interface {
	MUFProbability(band string, targetLat, targetLng float64, slot int) (float64, bool)
}

type noopPropagationPrior struct{}

func (noopPropagationPrior) MUFProbability(string, float64, float64, int) (float64, bool) {
	return 0, false
}

// FusionParams holds tunable parameters for variant C.
type FusionParams struct {
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

// FusionBandCount is the live-window observation for one band-region cell.

// DefaultFusionParams returns the factory defaults for variant C.
func DefaultFusionParams() FusionParams {
	return FusionParams{
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

// FusionBandCount is the live-window observation for one band-region cell.
type FusionBandCount struct {
	Band          string
	Region        string
	SpotCount     int
	LinkCount     int
	ReporterCount int
	DistMaxKm     int
}

// FusionSWSnapshot is the latest space-weather state.
type FusionSWSnapshot struct {
	Kp         float64            `json:"kp"`
	SFI        float64            `json:"sfi"`
	XrayClass  string             `json:"xray_class"` // e.g. "M5.2" or "C1.0" or ""
	AuroraGW   float64            `json:"aurora_gw"`  // hemispheric power in GW
	DrapHAF    map[string]float64 `json:"drap_haf"`
	DrapAgeMin int                `json:"drap_age_min"`
	Available  bool               `json:"available"`
	HasDrap    bool               `json:"has_drap"`
	FetchedAt  int64              `json:"fetched_at"`
}

// FusionEvent is an active demand-side event.
type FusionEvent struct {
	Source    string `json:"source"`
	Title     string `json:"title"`
	BandMask  string `json:"band_mask"`
	Locator4  string `json:"locator4"`
	Region    string `json:"region"`
	EndsInMin int    `json:"ends_in_min"`
}

// FusionBandVerdict is one band's classification from variant C.
type FusionBandVerdict struct {
	Band           string   `json:"band"`
	Region         string   `json:"region"`
	State          string   `json:"state"` // see VerdictState constants
	Label          string   `json:"label"`
	Reason         string   `json:"reason"`
	Confidence     float64  `json:"confidence"`
	SpotsPerMinute float64  `json:"spots_per_minute"`
	LinksPerMinute float64  `json:"links_per_minute"`
	BaselineP50    float64  `json:"baseline_p50"`
	ActivityRatio  float64  `json:"activity_ratio"`
	ClosureType    string   `json:"closure_type"` // muf_limited | absorption_limited | auroral | ""
	ExplainedBy    []string `json:"explained_by"`
}

// VerdictState values are shared across variants where applicable.
const (
	StateOpenConfirmed    = "open_confirmed"
	StateOpenUnconfirmed  = "open_unconfirmed"
	StateClosedButActive  = "closed_but_active"
	StateClosedWithCause  = "closed_with_cause"
	StateClosed           = "closed"
	StateInsufficientData = "insufficient_data"
)

// FusionVerdict is the full variant-C result for a target.
type FusionVerdict struct {
	GeneratedAt  int64               `json:"generated_at"`
	Params       FusionParams        `json:"params"`
	Bands        []FusionBandVerdict `json:"bands"`
	Prior        PropagationPrior    `json:"-"`
	DataThin     bool                `json:"data_thin"`
	SWAvailable  bool                `json:"sw_available"`
	EventsActive int                 `json:"events_active"`
}

// FusionEngine holds optional caches; the core computation is stateless per
// request so it is easy to test and replay.
type FusionEngine struct {
	prior PropagationPrior
}

// NewFusionEngine creates a fresh Fusion engine.
func NewFusionEngine() *FusionEngine {
	return &FusionEngine{prior: noopPropagationPrior{}}
}

// SetPrior replaces the engine's propagation prior.
func (e *FusionEngine) SetPrior(p PropagationPrior) {
	if p == nil {
		p = noopPropagationPrior{}
	}
	e.prior = p
}

// Verdict evaluates variant C for the supplied live counts and context.
// baselineRows are the historical daily counts per (band, region, slot);
// events are currently-active demand-side events; sw is the latest space
// weather; prior may be nil.
func (e *FusionEngine) Verdict(live []FusionBandCount, baselineRows []BaselineDayRow, events []FusionEvent, sw FusionSWSnapshot, prior PropagationPrior, params FusionParams, now int64) FusionVerdict {
	if prior == nil {
		prior = e.prior
	}
	resp := FusionVerdict{
		GeneratedAt:  now,
		Params:       params,
		Prior:        prior,
		SWAvailable:  sw.Available,
		EventsActive: len(events),
		Bands:        []FusionBandVerdict{},
	}

	if len(live) == 0 {
		resp.DataThin = true
		return resp
	}

	// Group baseline rows by (band, region, slot).
	baselineMap := groupBaselineRows(baselineRows)
	slot := UTCSlotOfDay(now)

	for _, cur := range live {
		v := e.classify(cur, baselineMap, events, sw, prior, params, slot)
		resp.Bands = append(resp.Bands, v)
	}
	return resp
}

func (e *FusionEngine) classify(cur FusionBandCount, baselineMap map[string][]BaselineDayRow, events []FusionEvent, sw FusionSWSnapshot, prior PropagationPrior, params FusionParams, slot int) FusionBandVerdict {
	v := FusionBandVerdict{Band: cur.Band, Region: cur.Region}
	key := fusionBaselineKey(cur.Band, cur.Region, slot)
	rows := baselineMap[key]

	windowMin := 20.0 // default fusion window is 20 min
	v.SpotsPerMinute = float64(cur.SpotCount) / windowMin
	v.LinksPerMinute = float64(cur.LinkCount) / windowMin

	if len(rows) == 0 {
		v.State = StateInsufficientData
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
		v.State = StateClosedButActive
		v.Label = "active (explained)"
		v.Reason = "link rate elevated but matched to known event(s)"
		v.Confidence = 0.6
		v.ExplainedBy = explainedBy
	case v.LinksPerMinute >= params.OpenRatio*baselinePerMin:
		if witnessOK {
			v.State = StateOpenConfirmed
			v.Label = "open"
			v.Reason = "links well above seasonal baseline; witnesses adequate"
			v.Confidence = 0.85
		} else {
			v.State = StateOpenUnconfirmed
			v.Label = "open?"
			v.Reason = "links high but reporter density too low to confirm"
			v.Confidence = 0.45
		}
	case v.LinksPerMinute <= params.ClosedRatio*baselinePerMin:
		v.State = StateClosedWithCause
		v.Label = "closed"
		v.ClosureType = e.closureCause(cur, sw, priorProb, priorOK, params)
		v.Reason = causeReason(v.ClosureType, sw.Available)
		v.Confidence = 0.6
	default:
		v.State = StateClosed
		v.Label = "closed"
		v.Reason = "activity within normal range"
		v.Confidence = 0.5
	}

	// If the prior disagrees strongly with the observation, note it but live
	// data wins. (A band the model says is closed but that shows real links is
	// still open — this is the DXRadar rule.)
	if priorOK && v.State == StateOpenConfirmed && priorProb < 0.2 {
		v.Reason += " (observation overrides low prior)"
	}

	return v
}

func (e *FusionEngine) closureCause(cur FusionBandCount, sw FusionSWSnapshot, priorProb float64, priorOK bool, params FusionParams) string {
	if !sw.Available {
		return ""
	}
	// Absorption-limited: D-RAP says the band is affected, or strong X-ray on
	// a sunlit path, or Kp is elevated.
	bandLower := BandLowerEdgeMHz(cur.Band)
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
func weightedBaselineStats(rows []BaselineDayRow, params FusionParams) (p25, p50, p75, mad float64) {
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

func groupBaselineRows(rows []BaselineDayRow) map[string][]BaselineDayRow {
	out := make(map[string][]BaselineDayRow)
	for _, r := range rows {
		key := fusionBaselineKey(r.Band, r.Region, r.Slot)
		out[key] = append(out[key], r)
	}
	return out
}

func fusionBaselineKey(band, region string, slot int) string {
	return band + "|" + region + "|" + strconv.Itoa(slot)
}

func eventExplanations(band, region string, events []FusionEvent) []string {
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
		lo := BandLowerEdgeMHz(strings.TrimSpace(parts[0]))
		hi := BandLowerEdgeMHz(strings.TrimSpace(parts[1]))
		b := BandLowerEdgeMHz(band)
		if lo > 0 && hi > 0 && b > 0 && b >= lo && b <= hi {
			return true
		}
	}
	return false
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
