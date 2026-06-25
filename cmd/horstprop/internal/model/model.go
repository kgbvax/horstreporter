// Package model is the Layer-3 (propagation-model) seam for horstprop.
//
// SCAFFOLD ONLY. Phase 3 backs Provider with a self-hosted ITURHFProp/voacapl
// precomputed grid + solar scaling (docs/horstprop.md §7.3 and §8 Phase 3 — the
// Linux-host continuation guide). Until then Disabled is the default and always
// abstains, so the engine blend runs on Layers 1+2 unchanged.
//
// Implementations MUST precompute an O(1) lookup grid; never run the propagation
// engine per call (§7.3 / §9).
package model

import "horstreporter/cmd/horstprop/internal/geo"

// Request is one path/time query.
type Request struct {
	Home    geo.LatLon
	DX      geo.LatLon
	Band    string
	FreqMHz float64
	UTCHour int // 0–23 UTC
	Month   int // 1–12
}

// Prediction is the model's answer. Score is 0–100; Detail is surfaced verbatim
// in the LayerResult breakdown.
type Prediction struct {
	Score      int
	Confidence float64
	Detail     map[string]any
}

// Provider is a Layer-3 propagation model. Predict returns ok=false when the
// model is unavailable (not built, grid not loaded, out of coverage).
type Provider interface {
	Predict(Request) (Prediction, bool)
}

// Disabled is the default no-op provider; it always abstains.
type Disabled struct{}

// Predict always abstains.
func (Disabled) Predict(Request) (Prediction, bool) { return Prediction{}, false }
