// Package engine is horstprop's scoring engine. Phases 1+2 wire Layer 1
// (empirical, from the rolling store) and Layer 2 (the KC2G MUF gate); Layer 3
// (model) plugs into the same blend (docs/horstprop.md §7.4) later without
// changing the contract.
package engine

import (
	"fmt"
	"math"
	"sort"
	"time"

	"horstreporter/cmd/horstprop/internal/geo"
	"horstreporter/cmd/horstprop/internal/model"
	"horstreporter/cmd/horstprop/internal/store"
	"horstreporter/internal/propcontract"
)

// empiricalTrustMax caps how much confident empirical evidence resists the MUF
// gate (1.0 = ignore the gate entirely). <1 keeps a little model authority even
// when observations are strong, in case the band closed since the last report.
const empiricalTrustMax = 0.85

// Resolver resolves a callsign to a DXCC entity (cty.dat). May be nil.
type Resolver interface {
	Resolve(call string) (geo.Entity, bool)
}

// MUFProvider supplies interpolated MUF(3000) in MHz at a point, plus the age of
// the freshest contributing station. Satisfied by *kc2g.Client; may be nil.
type MUFProvider interface {
	MUFAt(lat, lon float64) (mufMHz, ageMin float64, ok bool)
}

// Engine scores spots from the operator's home station.
type Engine struct {
	home       geo.LatLon
	homeOK     bool
	resolver   Resolver
	store      *store.Store   // Layer-1 rolling store; nil when no feed
	muf        MUFProvider    // Layer-2 MUF source; nil when disabled
	model      model.Provider // Layer-3 model; nil/Disabled when not built (scaffold)
	matchRings int
	now        func() time.Time
}

// New builds an Engine. resolver, st, muf and mdl may be nil.
func New(homeGrid string, resolver Resolver, st *store.Store, muf MUFProvider, mdl model.Provider, matchRings int) *Engine {
	e := &Engine{resolver: resolver, store: st, muf: muf, model: mdl, matchRings: matchRings, now: time.Now}
	if ll, ok := geo.MaidenheadCenter(homeGrid); ok {
		e.home, e.homeOK = ll, true
	}
	return e
}

// HomeResolved reports whether the home grid parsed.
func (e *Engine) HomeResolved() bool { return e.homeOK }

// StoreLen returns retained Layer-1 report count (0 if no store).
func (e *Engine) StoreLen() int {
	if e.store == nil {
		return 0
	}
	return e.store.Len()
}

// Score produces a Score for a spot (docs/horstprop.md §5.5 / §7).
func (e *Engine) Score(spot propcontract.Spot) propcontract.Score {
	loc, locSource := e.resolveLocation(spot)
	band := geo.FreqToBand(spot.FreqHz)

	sc := propcontract.Score{
		DXCall:    spot.DXCall,
		FreqHz:    spot.FreqHz,
		Band:      band,
		Timestamp: spot.Timestamp,
		Layers: propcontract.Layers{
			Empirical: propcontract.LayerResult{Available: false},
			MUFGate:   propcontract.MUFGate{Available: false},
			Model:     propcontract.LayerResult{Available: false},
		},
	}

	if locSource == "unresolved" {
		neutral := 50
		sc.Score, sc.Confidence = &neutral, 0.15
		sc.Grade = propcontract.Grade(&neutral, 0.15)
		sc.Reason = "DX location unresolved (no grid; callsign not in cty.dat) — neutral, cannot assess path"
		return sc
	}

	if e.homeOK {
		sc.DistanceKm = geo.HaversineKm(e.home, loc)
		sc.BearingDeg = geo.BearingDeg(e.home, loc)
	}

	// Layer 1 — empirical (from the rolling store).
	l1, l1ok := e.empirical(loc, band)
	if l1ok {
		sc.Layers.Empirical = l1
	}
	// Layer 2 — MUF gate (multiplicative).
	l2, l2ok := e.mufGate(loc, spot.FreqHz)
	if l2ok {
		sc.Layers.MUFGate = l2
	}
	// Layer 3 — model (scaffold; Disabled abstains until Phase 3 is built).
	l3, l3ok := e.modelPredict(loc, band, float64(spot.FreqHz)/1e6)
	if l3ok {
		sc.Layers.Model = l3
	}

	// Blend (§7.4): empirical-first base; model is the fallback base; MUF gate
	// applied multiplicatively (softened by empirical strength).
	base, baseConf := 50, 0.15 // unknown / neutral
	switch {
	case l1ok:
		base, baseConf = *l1.Score, l1.Confidence
	case l3ok:
		base, baseConf = *l3.Score, l3.Confidence
	}
	// When both L1 and L3 are present, agreement raises confidence, disagreement
	// lowers it (§7.4).
	if l1ok && l3ok {
		if absI(*l1.Score-*l3.Score) <= 15 {
			baseConf = minF(0.95, baseConf+0.1)
		} else {
			baseConf *= 0.8
		}
	}
	gate := 1.0
	if l2ok {
		gate = *l2.Gate
	}
	// Empirical-first (§1): real observations outrank the model. Strong, confident
	// empirical evidence raises a gate floor so a 30-min-old interpolated MUF can't
	// crush a path that is demonstrably being heard. Weak/absent empirical lets the
	// gate fully apply.
	effGate := gate
	if l1ok {
		trust := clampF((l1.Confidence-0.5)/0.42, 0, 1) * empiricalTrustMax
		effGate = gate + (1-gate)*trust
	}
	score := clampI(int(math.Round(float64(base)*effGate)), 0, 100)
	conf := baseConf
	if l2ok {
		switch {
		case gate <= 0.4:
			conf = maxF(baseConf, l2.Confidence) // confident in the result given the MUF state
		case !l1ok && !l3ok:
			conf = l2.Confidence // MUF is the only evidence
		}
	}

	sc.Score = &score
	sc.Confidence = conf
	sc.Grade = propcontract.Grade(&score, conf)
	sc.Reason = buildReason(l1, l1ok, l2, l2ok)
	if l1ok && l2ok && gate < 0.85 && effGate > gate+0.05 {
		sc.Reason += " (empirical observations override the model gate)"
	}
	return sc
}

func (e *Engine) resolveLocation(spot propcontract.Spot) (geo.LatLon, string) {
	if ll, ok := geo.MaidenheadCenter(spot.Grid); ok {
		return ll, "grid"
	}
	if e.resolver != nil {
		if ent, ok := e.resolver.Resolve(spot.DXCall); ok {
			return ent.Loc, "centroid"
		}
	}
	return geo.LatLon{}, "unresolved"
}

// empirical computes Layer 1. Returns ok=false (abstain) on no reports — silence
// is not a low score (§7.1).
func (e *Engine) empirical(loc geo.LatLon, band string) (propcontract.LayerResult, bool) {
	if e.store == nil || band == "" {
		return propcontract.LayerResult{Available: false}, false
	}
	dxX, dxY := geo.SquareXYFromLatLon(loc)
	reps := e.store.Lookup(band, dxX, dxY, e.matchRings)
	if len(reps) == 0 {
		return propcontract.LayerResult{Available: false}, false
	}
	snrs := make([]int, len(reps))
	newest := reps[0].At
	for i, r := range reps {
		snrs[i] = r.SNRDb
		if r.At.After(newest) {
			newest = r.At
		}
	}
	sort.Ints(snrs)
	best, med := snrs[len(snrs)-1], snrs[len(snrs)/2]
	snrScore := 0.6*ft8SnrToScore(best) + 0.4*ft8SnrToScore(med)

	ageFrac := e.now().Sub(newest).Seconds() / e.store.Window().Seconds()
	if ageFrac < 0 {
		ageFrac = 0
	}
	score := clampI(int(math.Round(snrScore*clampF(1-0.4*ageFrac, 0.6, 1))), 0, 100)
	conf := empiricalConfidence(len(reps), ageFrac)
	return propcontract.LayerResult{
		Available:  true,
		Score:      &score,
		Confidence: conf,
		Detail: map[string]any{
			"n_reports":   len(reps),
			"best_snr_db": best,
			"window_min":  int(e.store.Window().Minutes()),
			"match":       "region",
		},
	}, true
}

// mufGate computes Layer 2: the minimum MUF along the path vs the frequency.
func (e *Engine) mufGate(dx geo.LatLon, freqHz int64) (propcontract.MUFGate, bool) {
	if e.muf == nil || !e.homeOK {
		return propcontract.MUFGate{Available: false}, false
	}
	// VHF (6m/4m/2m, >=50 MHz) propagates via Es / tropo / meteor-scatter, not the
	// F-layer MUF the KC2G nowcast models — so the MUF gate doesn't apply. Scoring
	// there leans on empirical evidence (L1) instead.
	if float64(freqHz)/1e6 >= 50 {
		return propcontract.MUFGate{Available: false}, false
	}
	mufMin, ageMin, have := 0.0, 0.0, false
	for _, p := range controlPoints(e.home, dx) {
		muf, age, ok := e.muf.MUFAt(p.Lat, p.Lon)
		if !ok || muf <= 0 {
			continue
		}
		if !have || muf < mufMin {
			mufMin = muf
		}
		if age > ageMin {
			ageMin = age // worst (oldest) contributing age
		}
		have = true
	}
	if !have {
		return propcontract.MUFGate{Available: false}, false
	}
	freqMHz := float64(freqHz) / 1e6
	gate, state := gateForRatio(freqMHz / mufMin)
	conf := clampF(0.6-ageMin/240*0.25, 0.35, 0.6)
	return propcontract.MUFGate{
		Available:  true,
		Gate:       &gate,
		Confidence: conf,
		Detail: map[string]any{
			"muf_mhz":  math.Round(mufMin*10) / 10,
			"freq_mhz": math.Round(freqMHz*100) / 100,
			"state":    state,
			"age_min":  int(math.Round(ageMin)),
		},
	}, true
}

// modelPredict computes Layer 3 (scaffold: Disabled abstains).
func (e *Engine) modelPredict(dx geo.LatLon, band string, freqMHz float64) (propcontract.LayerResult, bool) {
	if e.model == nil || !e.homeOK || band == "" {
		return propcontract.LayerResult{Available: false}, false
	}
	now := e.now().UTC()
	pred, ok := e.model.Predict(model.Request{
		Home: e.home, DX: dx, Band: band, FreqMHz: freqMHz,
		UTCHour: now.Hour(), Month: int(now.Month()),
	})
	if !ok {
		return propcontract.LayerResult{Available: false}, false
	}
	s := pred.Score
	return propcontract.LayerResult{Available: true, Score: &s, Confidence: pred.Confidence, Detail: pred.Detail}, true
}

// MUFStations reports how many fresh KC2G stations are cached (0 if no/!capable
// MUF provider) — for health/diagnostics.
func (e *Engine) MUFStations() int {
	if fs, ok := e.muf.(interface{ FreshStations() int }); ok {
		return fs.FreshStations()
	}
	return 0
}

// ControlPointDiag is one sampled path control point (for the debug endpoint).
type ControlPointDiag struct {
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	HasData bool    `json:"has_data"`
	MUFMHz  float64 `json:"muf_mhz,omitempty"`
	AgeMin  float64 `json:"age_min,omitempty"`
}

// Diagnostics is the per-request internals surfaced by the debug endpoint.
type Diagnostics struct {
	HomeGrid          string             `json:"home_grid"`
	Home              geo.LatLon         `json:"home"`
	DX                geo.LatLon         `json:"dx"`
	LocSource         string             `json:"loc_source"`
	Band              string             `json:"band"`
	DistanceKm        float64            `json:"distance_km"`
	BearingDeg        float64            `json:"bearing_deg"`
	ControlPoints     []ControlPointDiag `json:"control_points"`
	StoreReportsTotal int                `json:"store_reports_total"`
	StoreMatchCount   int                `json:"store_match_count"`
	MUFFreshStations  int                `json:"muf_fresh_stations"`
}

// Debug returns the Score plus the internals that produced it (tuning endpoint).
func (e *Engine) Debug(spot propcontract.Spot) (propcontract.Score, Diagnostics) {
	sc := e.Score(spot)
	loc, locSource := e.resolveLocation(spot)
	d := Diagnostics{
		HomeGrid:          "", // filled by caller (api knows the grid string)
		Home:              e.home,
		DX:                loc,
		LocSource:         locSource,
		Band:              sc.Band,
		DistanceKm:        sc.DistanceKm,
		BearingDeg:        sc.BearingDeg,
		StoreReportsTotal: e.StoreLen(),
		MUFFreshStations:  e.MUFStations(),
	}
	if locSource != "unresolved" {
		if e.store != nil && sc.Band != "" {
			dxX, dxY := geo.SquareXYFromLatLon(loc)
			d.StoreMatchCount = len(e.store.Lookup(sc.Band, dxX, dxY, e.matchRings))
		}
		if e.muf != nil && e.homeOK {
			for _, p := range controlPoints(e.home, loc) {
				cp := ControlPointDiag{Lat: round1(p.Lat), Lon: round1(p.Lon)}
				if muf, age, ok := e.muf.MUFAt(p.Lat, p.Lon); ok {
					cp.HasData, cp.MUFMHz, cp.AgeMin = true, round1(muf), round1(age)
				}
				d.ControlPoints = append(d.ControlPoints, cp)
			}
		}
	}
	return sc, d
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// controlPoints samples the path midpoint and points ~1500 km in from each end.
func controlPoints(home, dx geo.LatLon) []geo.LatLon {
	D := geo.HaversineKm(home, dx)
	if D < 1 {
		return []geo.LatLon{home}
	}
	fracs := []float64{0.5}
	for _, d := range []float64{1500, D - 1500} {
		if f := d / D; f > 0.05 && f < 0.95 {
			fracs = append(fracs, f)
		}
	}
	pts := make([]geo.LatLon, 0, len(fracs))
	for _, f := range fracs {
		pts = append(pts, geo.IntermediatePoint(home, dx, f))
	}
	return pts
}

// gateForRatio maps r = freq/MUF to a [0,1] gate and a state label (§7.2).
func gateForRatio(r float64) (float64, string) {
	switch {
	case r <= 0.85:
		return 1.0, "open"
	case r <= 1.0:
		return 1.0 - (r-0.85)/0.15*0.6, "marginal"
	default:
		g := 0.4 - (r-1.0)/0.3*0.4
		if g < 0 {
			g = 0
		}
		return g, "above_muf"
	}
}

// ft8SnrToScore maps FT8 SNR (dB) to 0-100 via piecewise-linear anchors.
func ft8SnrToScore(snr int) float64 {
	type pt struct{ snr, score float64 }
	pts := []pt{{-25, 8}, {-18, 25}, {-5, 55}, {5, 85}, {15, 96}}
	s := float64(snr)
	if s <= pts[0].snr {
		return pts[0].score
	}
	if s >= pts[len(pts)-1].snr {
		return pts[len(pts)-1].score
	}
	for i := 1; i < len(pts); i++ {
		if s <= pts[i].snr {
			a, b := pts[i-1], pts[i]
			return a.score + (b.score-a.score)*(s-a.snr)/(b.snr-a.snr)
		}
	}
	return pts[len(pts)-1].score
}

func empiricalConfidence(n int, ageFrac float64) float64 {
	conf := 0.5 + 0.05*float64(n-1)
	conf *= clampF(1-0.3*ageFrac, 0.7, 1)
	return clampF(conf, 0.5, 0.92)
}

func buildReason(l1 propcontract.LayerResult, l1ok bool, l2 propcontract.MUFGate, l2ok bool) string {
	var parts []string
	if l1ok {
		n, _ := l1.Detail["n_reports"].(int)
		best, _ := l1.Detail["best_snr_db"].(int)
		win, _ := l1.Detail["window_min"].(int)
		parts = append(parts, fmt.Sprintf("Heard by %d receiver(s) near home in last %d min (best %+d dB FT8)", n, win, best))
	}
	if l2ok {
		muf, _ := l2.Detail["muf_mhz"].(float64)
		freq, _ := l2.Detail["freq_mhz"].(float64)
		state, _ := l2.Detail["state"].(string)
		parts = append(parts, fmt.Sprintf("path MUF ~%.1f MHz vs %.3f MHz — %s", muf, freq, state))
	}
	if len(parts) == 0 {
		return "no empirical or MUF data for this path — neutral pending more layers"
	}
	return joinReason(parts)
}

func joinReason(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "; "
		}
		out += p
	}
	return out
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampI(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func absI(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
