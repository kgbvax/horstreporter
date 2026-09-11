package main

import (
	"testing"
)

// TestLatLngToLocatorFieldPrecision pins the 2-char (field) output, which is
// correct across the whole grid including clamping at the edges.
func TestLatLngToLocatorFieldPrecision(t *testing.T) {
	tests := []struct {
		name     string
		lat, lng float64
		want     string
	}{
		{"Berlin square centre", 52.5, 13.0, "JO"},
		{"New England centre", 41.5, -73.0, "FN"},
		{"just east of a field boundary", 52.5, 14.0, "JO"},
		{"out-of-range NW clamps to RR", 95.0, 200.0, "RR"},
		{"out-of-range SE clamps to AA", -95.0, -185.0, "AA"},
		{"north pole clamps to RR", 90.0, 180.0, "RR"},
		{"south pole clamps to AA", -90.0, -180.0, "AA"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := latLngToLocator(tc.lat, tc.lng, 2); got != tc.want {
				t.Errorf("latLngToLocator(%v, %v, 2) = %q, want %q", tc.lat, tc.lng, got, tc.want)
			}
		})
	}
}

// TestLatLngToLocatorLowLatitudesRoundTrip verifies the 4-char output in the
// region where it is currently correct (lat+90 < 100, i.e. lat < 10°N, and
// lng < 20°E): there the function is the inverse of locatorToLatLng for the
// square centre.
func TestLatLngToLocatorLowLatitudesRoundTrip(t *testing.T) {
	squares := []string{"AA00", "II00", "JJ00", "JI00", "JA00"}
	for _, sq := range squares {
		lat, lng := locatorToLatLng(sq)
		if got := latLngToLocator(lat, lng, 4); got != sq {
			t.Errorf("round-trip %s: locatorToLatLng -> (%v, %v), latLngToLocator = %q, want %q", sq, lat, lng, got, sq)
		}
	}
}

// TestLatLngToLocatorLowLatitudeEdges pins the 4-char square behaviour at the
// boundaries of the region where it is correct.
func TestLatLngToLocatorLowLatitudeEdges(t *testing.T) {
	tests := []struct {
		name     string
		lat, lng float64
		want     string
	}{
		{"top row of field J", 9.5, 13.0, "JJ69"},
		{"inside field J west edge", 0.5, 1.0, "JJ00"},
		{"negative latitude row", -9.5, 1.0, "JI00"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := latLngToLocator(tc.lat, tc.lng, 4); got != tc.want {
				t.Errorf("latLngToLocator(%v, %v, 4) = %q, want %q", tc.lat, tc.lng, got, tc.want)
			}
		})
	}
}

// TestLatLngToLocatorHighLatitudeClampCharacterization is a characterization
// test for a KNOWN DEFECT, pinned so any fix is a deliberate, visible change.
//
// In the 4-char branch the sub-square coordinates are x = (lng+180)/2 and
// y = lat+90 (the fieldLat terms cancel algebraically), both spanning
// 0..179 — but the code clamps both at >=100 to 99 as if the domain were
// 0..99. Every point with lat >= 10°N or lng >= 20°E therefore collapses to
// the "99" row/column: Berlin (52.5, 13.0) yields "JJ69" instead of "JO62".
// Introduced with the grid-cluster baseline (b6dd55d) and affects the DXCC
// entity-centroid → cluster-anchor fallback (deriveOperatorCluster,
// dx_conditions.go) for callsign QTHs north of 10°N. If you fix
// latLngToLocator, update these expectations to the corrected values.
func TestLatLngToLocatorHighLatitudeClampCharacterization(t *testing.T) {
	tests := []struct {
		name     string
		lat, lng float64
		want     string // current (defective) behaviour
	}{
		{"Berlin square centre collapses to JJ69", 52.5, 13.0, "JJ69"},
		{"square centre one square west is identical (y column lost)", 52.5, 12.0, "JJ69"},
		{"east square boundary only moves the x column", 52.5, 14.0, "JJ79"},
		{"north square boundary invisible (y column lost)", 53.0, 13.0, "JJ69"},
		{"JO62qm sub-square centre collapses too", 52.5208, 13.3958, "JJ69"},
		{"New England centre collapses to EJ99", 41.5, -81.0, "EJ99"},
		{"northeast clamp corner collapses to JJ99", 95.0, 200.0, "JJ99"},
		{"north pole collapses to JJ99", 90.0, 180.0, "JJ99"},
		{"southwest clamp is correct", -95.0, -185.0, "AA00"},
		{"lng 20 field boundary collapses (should be JK00)", 0.5, 20.0, "JJ90"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := latLngToLocator(tc.lat, tc.lng, 4); got != tc.want {
				t.Errorf("latLngToLocator(%v, %v, 4) = %q, want %q (characterized)", tc.lat, tc.lng, got, tc.want)
			}
		})
	}
}