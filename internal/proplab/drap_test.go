package proplab

import (
	"math"
	"testing"
)

func TestParseDRAPText(t *testing.T) {
	g, err := ParseDRAPText([]byte(drapSample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if g.ValidAt == 0 {
		t.Errorf("expected valid time parsed")
	}
	if len(g.Lons) != 11 {
		t.Fatalf("expected 11 longitudes, got %d", len(g.Lons))
	}
	if len(g.Lats) != 6 {
		t.Fatalf("expected 6 latitudes, got %d", len(g.Lats))
	}
	wantLons := []float64{-10, -6, -2, 2, 6, 10, 14, 18, 22, 26, 30}
	for i, v := range wantLons {
		if math.Abs(g.Lons[i]-v) > 1e-9 {
			t.Errorf("lon[%d] = %v, want %v", i, g.Lons[i], v)
		}
	}
	wantLats := []float64{55, 53, 51, 49, 47, 45}
	for i, v := range wantLats {
		if math.Abs(g.Lats[i]-v) > 1e-9 {
			t.Errorf("lat[%d] = %v, want %v", i, g.Lats[i], v)
		}
	}
	// EU centroid sits inside the synthetic grid; HAF should be the constant 30 MHz.
	if haf, ok := g.HAF(50, 15); !ok {
		t.Errorf("HAF(50,15) not ok")
	} else if math.Abs(haf-30.0) > 1e-6 {
		t.Errorf("HAF(50,15) = %v, want 30.0", haf)
	}
	// Region map should be non-empty for known regions.
	m := g.RegionHAFMap()
	if len(m) == 0 {
		t.Errorf("RegionHAFMap empty")
	}
}

const drapSample = `# DRAP Tabular Values
# Product: D-Region Absorption         /images/drap2_tab.txt
# Product Valid At : 2026-07-31 07:03 UTC
#
# Estimated Recovery Time : No Estimate
#
#  X-RAY Message : Normal X-ray Background
#
Frequency (MHz) as a function of Latitude and Longitude
#
       -10   -6   -2    2    6   10   14   18   22   26   30
------------------------------------------------------------
 55 | 30.0 30.0 30.0 30.0 30.0 30.0 30.0 30.0 30.0 30.0 30.0
 53 | 30.0 30.0 30.0 30.0 30.0 30.0 30.0 30.0 30.0 30.0 30.0
 51 | 30.0 30.0 30.0 30.0 30.0 30.0 30.0 30.0 30.0 30.0 30.0
 49 | 30.0 30.0 30.0 30.0 30.0 30.0 30.0 30.0 30.0 30.0 30.0
 47 | 30.0 30.0 30.0 30.0 30.0 30.0 30.0 30.0 30.0 30.0 30.0
 45 | 30.0 30.0 30.0 30.0 30.0 30.0 30.0 30.0 30.0 30.0 30.0
`
