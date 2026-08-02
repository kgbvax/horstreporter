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
		{200, 400, -300, 999},   // far out of range
		{90, 0, -90, 180},       // exact antipodes
		{50, 10, 50, 10},        // identical
		{0, 0, 0, 0},            // origin
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
