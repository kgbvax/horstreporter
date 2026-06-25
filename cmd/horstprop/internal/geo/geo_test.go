package geo

import (
	"math"
	"testing"
)

func TestMaidenheadCenter(t *testing.T) {
	tests := []struct {
		grid     string
		lat, lon float64
		ok       bool
	}{
		{"JO32we", 52.1875, 7.875, true}, // NL/DE border-ish
		{"OH29", -10.5, 105.0, true},     // Christmas Island area
		{"AA00aa", -89.97916, -179.95833, true},
		{"", 0, 0, false},
		{"ZZ99", 0, 0, false},
		{"JO3", 0, 0, false},
	}
	for _, tc := range tests {
		got, ok := MaidenheadCenter(tc.grid)
		if ok != tc.ok {
			t.Errorf("%q: ok=%v want %v", tc.grid, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if math.Abs(got.Lat-tc.lat) > 0.1 || math.Abs(got.Lon-tc.lon) > 0.1 {
			t.Errorf("%q: got (%.3f,%.3f) want (%.3f,%.3f)", tc.grid, got.Lat, got.Lon, tc.lat, tc.lon)
		}
	}
}

func TestHaversineAndBearing(t *testing.T) {
	home, _ := MaidenheadCenter("JO32we") // ~52.5N, 7E
	xmas, _ := MaidenheadCenter("OH29")   // ~10.5S, 105E
	d := HaversineKm(home, xmas)
	if d < 11000 || d > 12000 {
		t.Errorf("JO32we->OH29 distance %.0f km, expected ~11400", d)
	}
	b := BearingDeg(home, xmas)
	if b < 80 || b > 110 {
		t.Errorf("JO32we->OH29 bearing %.1f°, expected ~ESE (90-100)", b)
	}
}

func TestFreqToBand(t *testing.T) {
	cases := map[int64]string{
		14074000: "20m",
		18145000: "17m",
		7074000:  "40m",
		50313000: "6m",
		12345000: "",
	}
	for hz, want := range cases {
		if got := FreqToBand(hz); got != want {
			t.Errorf("FreqToBand(%d)=%q want %q", hz, got, want)
		}
	}
}
