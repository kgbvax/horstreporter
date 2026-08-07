package cty

import (
	"strings"
	"testing"
)

// Minimal AD1C-format fixture (real primary prefixes so prefixISO lookups hit).
const fixture = `Fed. Rep. of Germany:    14:  28:  EU:  51.00:  -10.00:  -1.0:  DL:
    DA,DB,DL,=DL1ABC;
Japan:                   25:  45:  AS:  36.00: -139.00:   9.0:  JA:
    JA,JE,JR;
`

func TestParseResolveAndISO(t *testing.T) {
	res, err := Parse(strings.NewReader(fixture))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	ent, iso, ok := res.Resolve("DL7VEE")
	if !ok || ent.Name != "Fed. Rep. of Germany" || iso != "DE" {
		t.Errorf("DL7VEE -> name=%q iso=%q ok=%v (want Germany/DE)", ent.Name, iso, ok)
	}
	// exact-call override still resolves.
	if ent, iso, ok := res.Resolve("DL1ABC"); !ok || iso != "DE" || ent.PrimaryPrefix != "DL" {
		t.Errorf("DL1ABC -> %+v iso=%q ok=%v", ent, iso, ok)
	}
	if _, iso, ok := res.Resolve("JA1XYZ"); !ok || iso != "JP" {
		t.Errorf("JA1XYZ -> iso=%q ok=%v (want JP)", iso, ok)
	}
	if _, _, ok := res.Resolve("ZZ9ZZ"); ok {
		t.Error("ZZ9ZZ should not resolve")
	}
	// Entity centroid (East-positive lon) is populated for the regional
	// baseline's DXCC-center fallback when a callsign has no QRZ locator.
	ent, _, _ = res.Resolve("DL7VEE")
	if ent.Lat != 51.0 || ent.Lon != 10.0 {
		t.Errorf("DL7VEE centroid = (%v, %v), want (51, 10) East-positive", ent.Lat, ent.Lon)
	}
	ent, _, _ = res.Resolve("JA1XYZ")
	if ent.Lat != 36.0 || ent.Lon != 139.0 {
		t.Errorf("JA1XYZ centroid = (%v, %v), want (36, 139) East-positive", ent.Lat, ent.Lon)
	}
}

// Guard the generated table is present and sane.
func TestPrefixISOTable(t *testing.T) {
	for pfx, want := range map[string]string{"DL": "DE", "JA": "JP", "K": "US", "VK": "AU", "VU": "IN"} {
		if prefixISO[pfx] != want {
			t.Errorf("prefixISO[%q]=%q want %q", pfx, prefixISO[pfx], want)
		}
	}
	if len(prefixISO) < 200 {
		t.Errorf("prefixISO has only %d entries, expected ~278", len(prefixISO))
	}
}
