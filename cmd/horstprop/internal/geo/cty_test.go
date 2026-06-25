package geo

import (
	"math"
	"strings"
	"testing"
)

// A tiny AD1C-format cty.dat fixture: two entities, one with an exact-call
// override. Longitudes are WEST-positive in the file (negated on parse).
const ctyFixture = `Christmas Island:  29:  54:  OC:   -10.48:  -105.63:   -7.0:  VK9X:
    VK9X,=VK9XX;
Fed. Rep. of Germany:  14:  28:  EU:    51.17:   -10.33:   -1.0:  DL:
    DA,DB,DC,DD,DF,DG,DH,DJ,DK,DL,DM,DO,DP;
`

func TestParseAndResolve(t *testing.T) {
	res, err := ParseCty(strings.NewReader(ctyFixture))
	if err != nil {
		t.Fatalf("ParseCty: %v", err)
	}

	e, ok := res.Resolve("VK9XX") // exact override
	if !ok || e.Name != "Christmas Island" {
		t.Fatalf("VK9XX -> %+v ok=%v", e, ok)
	}
	if math.Abs(e.Loc.Lon-105.63) > 0.01 { // east-positive
		t.Errorf("Christmas Is lon=%.2f want +105.63 (east)", e.Loc.Lon)
	}

	e, ok = res.Resolve("VK9CXYZ") // VK9C not present, falls to VK9? -> no; should be VK9X? no.
	if ok {
		t.Errorf("VK9CXYZ unexpectedly resolved to %s", e.Name)
	}

	e, ok = res.Resolve("DL9ET") // longest-prefix match on DL
	if !ok || e.Name != "Fed. Rep. of Germany" {
		t.Fatalf("DL9ET -> %+v ok=%v", e, ok)
	}
	if e.Continent != "EU" || e.CQ != 14 {
		t.Errorf("DE entity fields: cont=%s cq=%d", e.Continent, e.CQ)
	}

	if _, ok := res.Resolve("ZZ1ZZ"); ok {
		t.Errorf("ZZ1ZZ should not resolve")
	}
}

func TestStripOverrides(t *testing.T) {
	cases := map[string]string{
		"VK9X":           "VK9X",
		"VK9X(38)":       "VK9X",
		"K[7]":           "K",
		"4U1V<46.2/6.1>": "4U1V",
		"VK9X;":          "VK9X",
	}
	for in, want := range cases {
		if got := stripOverrides(in); got != want {
			t.Errorf("stripOverrides(%q)=%q want %q", in, got, want)
		}
	}
}
