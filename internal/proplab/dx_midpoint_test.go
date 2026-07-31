package proplab

import (
	"math"
	"testing"
)

func TestGreatCircleMidpoint(t *testing.T) {
	const eps = 0.05
	tests := []struct {
		name                   string
		lat1, lon1, lat2, lon2 float64
		wantLat, wantLon       float64
	}{
		{"equator eastward", 0, 0, 0, 10, 0, 5},
		{"meridian northward", 0, 0, 10, 0, 5, 0},
		{"antimeridian pacific", 10, -179, 10, 175, 10, 178},
		{"across date line negative side", 10, 179, 10, -175, 10, -178},
		{"diagonal", 0, 0, 10, 10, 5.05, 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lat, lon := GreatCircleMidpoint(tc.lat1, tc.lon1, tc.lat2, tc.lon2)
			if math.Abs(lat-tc.wantLat) > eps || math.Abs(lon-tc.wantLon) > eps {
				t.Fatalf("got %.3f,%.3f want %.3f,%.3f", lat, lon, tc.wantLat, tc.wantLon)
			}
		})
	}
}

func TestGreatCircleMidpointAntimeridianMinimisesDistance(t *testing.T) {
	lat, lon := GreatCircleMidpoint(10, 179, 10, -175)
	// Result should be close to 10,-178 (the short way across the date line),
	// not 10,2 (the long way).
	if math.Abs(lat-10) > 0.1 || math.Abs(lon-(-178)) > 0.5 {
		t.Fatalf("antimeridian midpoint should be short-way, got %.3f,%.3f", lat, lon)
	}
}

func TestGreatCircleMidpointPole(t *testing.T) {
	lat, _ := GreatCircleMidpoint(80, 0, 80, 180)
	// The great-circle midpoint of two points near the pole on opposite sides
	// should be very close to the pole. Longitude is degenerate at the pole.
	if lat < 89 {
		t.Fatalf("expected midpoint near the pole, got lat=%.3f", lat)
	}
}

func TestLatLngToLocator4RoundTrip(t *testing.T) {
	for _, in := range []string{"JO62QM", "FN31", "RR73", "AA00", "RR79"} {
		lat, lon := LocatorToLatLng(in)
		out, ok := LatLngToLocator4(lat, lon)
		if !ok {
			t.Fatalf("%s -> %.3f,%.3f -> not ok", in, lat, lon)
		}
		want := in[:4]
		if out != want {
			t.Fatalf("%s -> %.3f,%.3f -> %s want %s", in, lat, lon, out, want)
		}
	}
}

func TestLatLngToLocator4Edges(t *testing.T) {
	cases := []struct {
		lat, lon float64
		want     string
	}{
		{-90, -180, "AA00"},
		{89.999, 179.999, "RR99"}, // max valid x=179 (RR), max y=179 (99)
	}
	for _, c := range cases {
		got, ok := LatLngToLocator4(c.lat, c.lon)
		if !ok || got != c.want {
			t.Fatalf("%.3f,%.3f -> %s ok=%v want %s", c.lat, c.lon, got, ok, c.want)
		}
	}
}

func TestLatLngToLocator4CentreRoundTrip(t *testing.T) {
	// Pick a few square centres and confirm round-tripping through
	// LocatorToLatLng gives the same lower-left square.
	for _, loc := range []string{"JO62", "FN31", "RR73", "AA00"} {
		lat, lon := LocatorToLatLng(loc)
		got, ok := LatLngToLocator4(lat, lon)
		if !ok || got != loc {
			t.Fatalf("%s centre %.4f,%.4f -> %s ok=%v want %s", loc, lat, lon, got, ok, loc)
		}
	}
}

func TestMidpointCell(t *testing.T) {
	cell, ok := MidpointCell("JO62QM", "FN31AB")
	if !ok || cell == "" {
		t.Fatalf("expected a midpoint cell, got %s ok=%v", cell, ok)
	}
	// JO62 ~ 50.5N 14.0E, FN31 ~ 41.5N 73.0W. Midpoint is roughly 50N 30W ->
	// square around IO41/IO51. Just assert non-empty and valid.
	if len(cell) != 4 {
		t.Fatalf("expected 4-char cell, got %q", cell)
	}

	if _, ok := MidpointCell("JO62", "invalid"); ok {
		t.Fatal("expected invalid receiver to fail")
	}
	if _, ok := MidpointCell("JO", "FN31"); ok {
		t.Fatal("expected short locator to fail")
	}
}

func TestEsSkipClassify(t *testing.T) {
	cases := []struct {
		band   string
		distKm float64
		isEs   bool
		hops   int
	}{
		{"6m", 1500, true, 1},
		{"6m", 900, true, 1},
		{"6m", 2300, true, 2},
		{"6m", 700, false, 0},
		{"6m", 5000, false, 0},
		{"20m", 1500, false, 0},
		{"10m", 1500, false, 0},
		{"2m", 1200, true, 1},
	}
	for _, c := range cases {
		isEs, hops := esSkipClassify(c.band, c.distKm)
		if isEs != c.isEs || hops != c.hops {
			t.Fatalf("%s %.0f km -> isEs=%v hops=%d, want isEs=%v hops=%d",
				c.band, c.distKm, isEs, hops, c.isEs, c.hops)
		}
	}
}

func TestLadderOpenBands(t *testing.T) {
	tests := []struct {
		name       string
		open       map[string]bool
		wantRuns   [][]string
		wantMaxRun int
	}{
		{
			name:       "empty",
			open:       map[string]bool{},
			wantRuns:   nil,
			wantMaxRun: 0,
		},
		{
			name:       "single",
			open:       map[string]bool{"20m": true},
			wantRuns:   [][]string{{"20m"}},
			wantMaxRun: 1,
		},
		{
			name:       "coherent run",
			open:       map[string]bool{"20m": true, "17m": true, "15m": true},
			wantRuns:   [][]string{{"20m", "17m", "15m"}},
			wantMaxRun: 3,
		},
		{
			name:       "lone high band",
			open:       map[string]bool{"20m": true, "17m": true, "10m": true},
			wantRuns:   [][]string{{"20m", "17m"}, {"10m"}},
			wantMaxRun: 2,
		},
		{
			name:       "non-ladder ignored",
			open:       map[string]bool{"6m": true, "20m": true, "17m": true, "80m": true},
			wantRuns:   [][]string{{"20m", "17m"}},
			wantMaxRun: 2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runs, maxRun := ladderOpenBands(tc.open)
			if len(runs) != len(tc.wantRuns) || maxRun != tc.wantMaxRun {
				t.Fatalf("runs=%v maxRun=%d want runs=%v maxRun=%d", runs, maxRun, tc.wantRuns, tc.wantMaxRun)
			}
			for i := range runs {
				if len(runs[i]) != len(tc.wantRuns[i]) {
					t.Fatalf("run %d = %v want %v", i, runs[i], tc.wantRuns[i])
				}
				for j := range runs[i] {
					if runs[i][j] != tc.wantRuns[i][j] {
						t.Fatalf("run %d[%d] = %s want %s", i, j, runs[i][j], tc.wantRuns[i][j])
					}
				}
			}
		})
	}
}

func TestBandInOpenRun(t *testing.T) {
	open := map[string]bool{"20m": true, "17m": true, "15m": true, "10m": true}
	if !bandInOpenRun("17m", open, 2) {
		t.Fatal("17m should be in a run of length 3")
	}
	if bandInOpenRun("10m", open, 2) {
		t.Fatal("10m is a lone band and should not be in a run of length >=2")
	}
	if !bandInOpenRun("10m", open, 1) {
		t.Fatal("any open band qualifies when minRun=1")
	}
}
