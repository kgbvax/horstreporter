package main

import (
	"reflect"
	"sort"
	"testing"
)

func TestQthSquares(t *testing.T) {
	sorted := func(s []string) []string {
		out := append([]string(nil), s...)
		sort.Strings(out)
		return out
	}
	t.Run("6-char locator matches its whole square", func(t *testing.T) {
		if got := qthSquares("FN76OJ", false); !reflect.DeepEqual(got, []string{"FN76"}) {
			t.Fatalf("got %v, want [FN76]", got)
		}
	})
	t.Run("surroundings gives the 3x3 block", func(t *testing.T) {
		got := sorted(qthSquares("FN76OJ", true))
		want := sorted([]string{"FN65", "FN66", "FN67", "FN75", "FN76", "FN77", "FN85", "FN86", "FN87"})
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
	t.Run("callsign is passed through", func(t *testing.T) {
		for _, surroundings := range []bool{false, true} {
			if got := qthSquares("VE9CF", surroundings); !reflect.DeepEqual(got, []string{"VE9CF"}) {
				t.Fatalf("surroundings=%v: got %v, want [VE9CF]", surroundings, got)
			}
		}
	})
}

// A 6-char QTH used to match only spots in that exact subsquare, so a sparse
// operator saw nothing. Through qthSquares it matches the whole 4-char square.
func TestSixCharQthMatchesWholeSquare(t *testing.T) {
	m := MQTTMessage{B: "20m", SC: "K1ABC", SL: "FN76AB", RC: "DL1XYZ", RL: "JO32AB", RP: -10}
	client := &Client{qthSet: qthSquares("FN76OJ", false)}
	if _, ok := matchAndCreateSpot(client, m, m.T); !ok {
		t.Fatal("spot from FN76AB must match qth FN76OJ")
	}
	ev, ok := extractMatchedBandEvent(m, qthSquares("FN76OJ", false))
	if !ok {
		t.Fatal("extractMatchedBandEvent must match qth FN76OJ at grid4")
	}
	if ev.band != "20m" {
		t.Fatalf("band = %q", ev.band)
	}
	if _, ok := extractMatchedBandEvent(m, qthSquares("FN31PR", false)); ok {
		t.Fatal("spot from FN76 must not match qth FN31PR")
	}
}

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
