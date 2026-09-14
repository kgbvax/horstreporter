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

// TestLatLngToLocatorLowLatitudesRoundTrip verifies the 4-char output is the
// inverse of locatorToLatLng for the square centre, grid-wide (including both
// clamp corners, which fold into AA00 / RR99).
func TestLatLngToLocatorLowLatitudesRoundTrip(t *testing.T) {
	squares := []string{"AA00", "II00", "JJ00", "JI00", "JA00", "JO62", "FN31", "JJ69", "EN91", "QK00", "RR99"}
	for _, sq := range squares {
		lat, lng := locatorToLatLng(sq)
		if got := latLngToLocator(lat, lng, 4); got != sq {
			t.Errorf("round-trip %s: locatorToLatLng -> (%v, %v), latLngToLocator = %q, want %q", sq, lat, lng, got, sq)
		}
	}
}

// TestLatLngToLocatorLowLatitudeEdges pins the 4-char square behaviour at
// field and square boundaries.
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

// TestLatLngToLocatorHighLatitudes covers the 4-char branch at lat >= 10°N and
// lng >= 20°E — the region the former x/y clamp (0..99 over a 0..179 domain)
// collapsed into the "99" row/column (Berlin encoded as JJ69 instead of JO62;
// fixed 2026-09 by computing field-relative col/row directly).
func TestLatLngToLocatorHighLatitudes(t *testing.T) {
	tests := []struct {
		name     string
		lat, lng float64
		want     string
	}{
		{"Berlin square centre", 52.5, 13.0, "JO62"},
		{"square centre one square west", 52.5, 12.0, "JO62"},
		{"east square boundary moves the x column", 52.5, 14.0, "JO72"},
		{"north square boundary moves the y row", 53.0, 13.0, "JO63"},
		{"JO62qm sub-square centre", 52.5208, 13.3958, "JO62"},
		{"Toledo OH (41.5, -81)", 41.5, -81.0, "EN91"},
		{"northeast clamp corner folds into RR99", 95.0, 200.0, "RR99"},
		{"north pole folds into RR99", 90.0, 180.0, "RR99"},
		{"southwest clamp is correct", -95.0, -185.0, "AA00"},
		{"lng 20 starts field K (lat 0.5 is field J)", 0.5, 20.0, "KJ00"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := latLngToLocator(tc.lat, tc.lng, 4); got != tc.want {
				t.Errorf("latLngToLocator(%v, %v, 4) = %q, want %q", tc.lat, tc.lng, got, tc.want)
			}
		})
	}
}