package main

import "testing"

func TestParseDXClusterSpot(t *testing.T) {
	line := "DX de DL1ABC: 14074.0 K1XYZ FT8 cq test"
	spot, ok := parseDXClusterSpot(line, 123456789)
	if !ok {
		t.Fatalf("expected line to parse")
	}
	if spot.Spotter != "DL1ABC" {
		t.Fatalf("unexpected spotter: %q", spot.Spotter)
	}
	if spot.DXCall != "K1XYZ" {
		t.Fatalf("unexpected dx call: %q", spot.DXCall)
	}
	if spot.FrequencyKHz != 14074.0 {
		t.Fatalf("unexpected frequency: %v", spot.FrequencyKHz)
	}
}

func TestBandFromFrequencyKHz(t *testing.T) {
	cases := []struct {
		freq float64
		band string
	}{
		{14074.0, "20m"},
		{7074.0, "40m"},
		{50100.0, "6m"},
		{999.0, ""},
	}
	for _, tc := range cases {
		if got := bandFromFrequencyKHz(tc.freq); got != tc.band {
			t.Fatalf("freq %v: expected %q, got %q", tc.freq, tc.band, got)
		}
	}
}
