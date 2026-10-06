package main

import (
	"strings"
	"testing"

	"horstreporter/internal/cty"
)

// testCtyDat is a two-entity cty.dat fixture: Germany (centroid 51N 10E,
// lon West-positive in the file) and a zero-centroid entity that must never
// resolve (deriveOperatorCluster skips entities with a 0 lat/lon).
const testCtyDat = `Fed. Rep. of Germany:     14:  28:  EU:   51.00:   -10.00:    -1.0:  DL:
    DA,DB,DC,DD,DE,DF,DG,DH,DI,DJ,DK,DL,DM,DN,DO,DP,DQ,DR;
Null Island:              01:  01:  AF:    0.00:     0.00:     0.0:  ZZ:
    ZZ;
`

func testCtyResolver(t *testing.T) *cty.Resolver {
	t.Helper()
	r, err := cty.Parse(strings.NewReader(testCtyDat))
	if err != nil {
		t.Fatalf("cty.Parse: %v", err)
	}
	return r
}

// --- deriveOperatorCluster characterization (pre-refactor behaviour) ---

func TestDeriveOperatorClusterCharacterization(t *testing.T) {
	ctyRes := testCtyResolver(t)
	qrz := &stubResolver{infos: map[string]CallsignInfo{
		"DL1ABC": {Locator: "JO62"},
		"DL2BAD": {Locator: "notaloc"}, // invalid QRZ locator → cty fallback
		"ZZ1ZZ":  {Locator: ""},        // empty → cty (zero centroid) → ""
	}}
	e := newDxBaselineEngine("")
	e.SetResolvers(qrz, ctyRes)

	ctyAnchor, _ := locatorClusterAnchor(latLngToLocator(51, 10, 4))
	jo62Anchor, _ := locatorClusterAnchor("JO62")
	cases := []struct {
		qth, want string
	}{
		{"", ""},
		{"JO62", jo62Anchor},
		{"JO62QM", jo62Anchor},
		{"DL1ABC", jo62Anchor},
		{"DL2BAD", ctyAnchor},
		{"DL9XYZ", ctyAnchor}, // QRZ miss → cty
		{"ZZ1ZZ", ""},         // zero centroid skipped
		{"K1ABC", ""},         // unknown everywhere
		{"XX99", ""},          // not a locator, unknown callsign
	}
	for _, c := range cases {
		if got := e.deriveOperatorCluster(c.qth); got != c.want {
			t.Errorf("deriveOperatorCluster(%q) = %q, want %q", c.qth, got, c.want)
		}
	}

	// No resolvers at all: callsigns yield "", locators still work.
	bare := newDxBaselineEngine("")
	if got := bare.deriveOperatorCluster("DL1ABC"); got != "" {
		t.Errorf("no resolvers: callsign => %q, want \"\"", got)
	}
	if got := bare.deriveOperatorCluster("JO62"); got != jo62Anchor {
		t.Errorf("no resolvers: locator => %q, want %q", got, jo62Anchor)
	}
}
