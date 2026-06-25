// Package propcontract holds the wire types shared between HorstReporter-side
// code and the standalone horstprop scoring service. It is the single Go-level
// source of truth for the score contract in docs/horstprop.md §5.
//
// The root binary (package main) cannot be imported, so contract types that more
// than one binary needs live here under internal/ where anything in the module
// can import them. The runtime boundary stays HTTP/JSON; this just keeps the
// shapes (and the grade banding) in one place.
package propcontract

import "time"

// Grade letters (§5.5).
const (
	GradeA       = "A"
	GradeB       = "B"
	GradeC       = "C"
	GradeD       = "D"
	GradeUnknown = "?"
)

// Spot is the scoring input supplied by the operator's DX-cluster client (§5.1).
type Spot struct {
	DXCall    string `json:"dx_call"`
	FreqHz    int64  `json:"freq_hz"`
	Timestamp string `json:"timestamp,omitempty"`
	Mode      string `json:"mode,omitempty"`
	Grid      string `json:"grid,omitempty"`
}

// PropReport is one Layer-1 reception observation, adapted from HorstReporter's
// public stream shape (toStreamSpot) by the PropFeedSource adapter (§5.2).
//
// IMPORTANT: the public feed is LOCATOR-LEVEL. It does not expose callsigns,
// frequency, or mode — only the two grids, SNR, band and source. Layer 1 is
// therefore locator/path based, not callsign based. See docs/horstprop.md §5.2
// and §3 A2. ("dxcluster" source is dropped by the adapter.)
type PropReport struct {
	TxLocator  string    `json:"tx_locator"` // heard (DX-side) grid — streamSpot.locator
	RxLocator  string    `json:"rx_locator"` // receiver/monitor grid — streamSpot.reporterLocator
	SNRDb      int       `json:"snr_db"`
	Band       string    `json:"band"`
	Source     string    `json:"source"` // "mqtt" (PSKReporter); RBN later if added upstream
	ObservedAt time.Time `json:"observed_at"`
}

// Geometry is derived once per spot and shared by all layers (§5.3).
type Geometry struct {
	DXLat      float64 `json:"dx_lat"`
	DXLon      float64 `json:"dx_lon"`
	LocSource  string  `json:"loc_source"` // "grid" | "centroid" | "unresolved"
	BearingDeg float64 `json:"bearing_deg"`
	DistanceKm float64 `json:"distance_km"`
	Band       string  `json:"band"`
}

// LayerResult is what a scoring layer returns, or {Available:false} when it has
// nothing (§5.4). Score is a pointer so "no score" is distinct from zero.
type LayerResult struct {
	Available  bool           `json:"available"`
	Score      *int           `json:"score,omitempty"`
	Confidence float64        `json:"confidence,omitempty"`
	Detail     map[string]any `json:"detail,omitempty"`
}

// MUFGate is the special Layer-2 result: a multiplier in [0,1] rather than a
// score (§5.4).
type MUFGate struct {
	Available  bool           `json:"available"`
	Gate       *float64       `json:"gate,omitempty"`
	Confidence float64        `json:"confidence,omitempty"`
	Detail     map[string]any `json:"detail,omitempty"`
}

// Layers is the per-layer breakdown carried in the Score (§5.5).
type Layers struct {
	Empirical LayerResult `json:"empirical"`
	MUFGate   MUFGate     `json:"muf_gate"`
	Model     LayerResult `json:"model"`
}

// Score is the scoring-API output (§5.5). Score is a pointer: null until the
// engine produces a value (grade is then "?").
type Score struct {
	DXCall     string  `json:"dx_call"`
	FreqHz     int64   `json:"freq_hz"`
	Band       string  `json:"band"`
	Timestamp  string  `json:"timestamp,omitempty"`
	BearingDeg float64 `json:"bearing_deg"`
	DistanceKm float64 `json:"distance_km"`
	Score      *int    `json:"score"`
	Grade      string  `json:"grade"`
	Confidence float64 `json:"confidence"`
	Layers     Layers  `json:"layers"`
	Reason     string  `json:"reason,omitempty"`
}

// Grade maps a (score, confidence) pair to a letter using the §5.5 bands:
// A >= 75, B 55-74, C 35-54, D < 35, and "?" when score is null or confidence
// is below 0.2. Lives here so the banding is defined in exactly one place.
func Grade(score *int, confidence float64) string {
	if score == nil || confidence < 0.2 {
		return GradeUnknown
	}
	switch s := *score; {
	case s >= 75:
		return GradeA
	case s >= 55:
		return GradeB
	case s >= 35:
		return GradeC
	default:
		return GradeD
	}
}
