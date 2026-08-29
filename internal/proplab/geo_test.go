package proplab

import (
	"math"
	"testing"
)

// TestHaversineKmNeverNaN pins the a-clamp: out-of-range inputs (malformed
// locator coords) and antipodal float overflow must stay finite because
// callers int()-convert the result (int(NaN) is garbage and crashed the dest
// backfill with an int4 encode error).
func TestHaversineKmNeverNaN(t *testing.T) {
	cases := [][4]float64{
		{200, 400, -300, 999}, // far out of range
		{90, 0, -90, 180},     // exact antipodes
		{50, 10, 50, 10},      // identical
		{0, 0, 0, 0},          // origin
		{52.4, 13.3, 35.6, 139.7},
	}
	for _, c := range cases {
		d := HaversineKm(c[0], c[1], c[2], c[3])
		if math.IsNaN(d) || math.IsInf(d, 0) {
			t.Errorf("HaversineKm%v = %v, want finite", c, d)
		}
		if d < 0 || d > 21000 {
			t.Errorf("HaversineKm%v = %v, want 0..21000 km", c, d)
		}
	}
	if d := HaversineKm(90, 0, -90, 180); d < 19000 {
		t.Errorf("antipodal distance = %v, want ~20015 km", d)
	}
}

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
