package main

import (
	"errors"
	"testing"
	"time"
)

// countingResolver wraps stubResolver and counts LookupInfo calls so the
// cache can be shown to keep repeat requests off QRZ.
type countingResolver struct {
	stubResolver
	calls int
}

func (c *countingResolver) LookupInfo(callsign string) (CallsignInfo, error) {
	c.calls++
	return c.stubResolver.LookupInfo(callsign)
}

// --- resolveAlmanacArea ---

func TestResolveAlmanacAreaLocator(t *testing.T) {
	cases := []struct{ in, want string }{
		{"JO32", "JO32"},
		{"jo32ab", "JO32"},
		{" JO32AB ", "JO32"},
		{"JO32ab12", "JO32"},
		{"AA00", "AA00"},
		{"RR99xx", "RR99"},
	}
	for _, c := range cases {
		area, err := resolveAlmanacAreaWith(c.in, nil, nil)
		if err != nil {
			t.Fatalf("resolveAlmanacAreaWith(%q) err = %v", c.in, err)
		}
		if area.Grid4 != c.want || area.Source != qthSourceLocator {
			t.Errorf("resolveAlmanacAreaWith(%q) = %+v, want %s/locator", c.in, area, c.want)
		}
		if area.Approximate() {
			t.Errorf("%q: locator source must not be approximate", c.in)
		}
	}
}

func TestResolveAlmanacAreaValidation(t *testing.T) {
	ctyRes := testCtyResolver(t)
	for _, in := range []string{"", "   ", "XX99", "JS32", "SA00", "JO32ZZ", "JO32AY", "JO32A", "JO32AB1", "JO32ABCD", "DL1/*", "!!", "12", "ABC", "12345"} {
		_, err := resolveAlmanacAreaWith(in, nil, ctyRes)
		if err == nil {
			t.Errorf("resolveAlmanacAreaWith(%q) expected validation error", in)
			continue
		}
		if !errors.Is(err, errAlmanacInvalidQTH) {
			t.Errorf("resolveAlmanacAreaWith(%q) err = %v, want errAlmanacInvalidQTH", in, err)
		}
	}
}

func TestResolveAlmanacAreaNearLocatorIsCallsign(t *testing.T) {
	// Callsign-shaped tokens without a locator prefix go through the callsign
	// path; unknown ones are unresolved, never mis-parsed as locators.
	for _, in := range []string{"JO3A", "DL1ABC"} {
		if _, err := resolveAlmanacAreaWith(in, nil, nil); !errors.Is(err, errAlmanacUnresolved) {
			t.Errorf("resolveAlmanacAreaWith(%q) err = %v, want errAlmanacUnresolved", in, err)
		}
	}
}

func TestResolveAlmanacAreaCallsign(t *testing.T) {
	ctyRes := testCtyResolver(t)
	qrz := &stubResolver{infos: map[string]CallsignInfo{"DL1ABC": {Locator: "JO32"}}}

	area, err := resolveAlmanacAreaWith("dl1abc", qrz, ctyRes)
	if err != nil {
		t.Fatalf("qrz path err = %v", err)
	}
	if area.Grid4 != "JO32" || area.Source != qthSourceQRZ || area.Approximate() {
		t.Errorf("qrz path = %+v, want JO32/qrz/not approximate", area)
	}

	// QRZ disabled → DXCC centroid, flagged approximate (AE4).
	area, err = resolveAlmanacAreaWith("DL1ABC", nil, ctyRes)
	if err != nil {
		t.Fatalf("dxcc path err = %v", err)
	}
	want := latLngToLocator(51, 10, 4)
	if area.Grid4 != want || area.Source != qthSourceDXCC || !area.Approximate() {
		t.Errorf("dxcc path = %+v, want %s/dxcc/approximate", area, want)
	}

	// QRZ miss → DXCC.
	area, err = resolveAlmanacAreaWith("DK9ZZ", qrz, ctyRes)
	if err != nil || area.Source != qthSourceDXCC {
		t.Errorf("qrz miss => %+v, %v; want dxcc", area, err)
	}

	// Unresolvable callsign → error, not a validation error.
	_, err = resolveAlmanacAreaWith("K1ABC", qrz, ctyRes)
	if err == nil || !errors.Is(err, errAlmanacUnresolved) {
		t.Errorf("unknown callsign err = %v, want errAlmanacUnresolved", err)
	}
	// Zero-centroid entity is not a location.
	if _, err = resolveAlmanacAreaWith("ZZ1ZZ", nil, ctyRes); !errors.Is(err, errAlmanacUnresolved) {
		t.Errorf("zero centroid err = %v, want errAlmanacUnresolved", err)
	}
}

func TestAlmanacAreaCacheTTL(t *testing.T) {
	ctyRes := testCtyResolver(t)
	qrz := &countingResolver{stubResolver: stubResolver{infos: map[string]CallsignInfo{"DL1ABC": {Locator: "JO32"}}}}
	now := time.Unix(1_800_000_000, 0)
	c := newAlmanacAreaCache()
	c.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		area, err := c.resolve("DL1ABC", qrz, ctyRes)
		if err != nil || area.Grid4 != "JO32" {
			t.Fatalf("resolve #%d = %+v, %v", i, area, err)
		}
	}
	if qrz.calls != 1 {
		t.Errorf("QRZ calls = %d after 3 cached resolves, want 1", qrz.calls)
	}
	// Normalized key: lowercase hits the same entry.
	if _, err := c.resolve(" dl1abc", qrz, ctyRes); err != nil || qrz.calls != 1 {
		t.Errorf("normalized key missed cache: calls=%d err=%v", qrz.calls, err)
	}
	now = now.Add(almanacAreaCacheTTL + time.Second)
	if _, err := c.resolve("DL1ABC", qrz, ctyRes); err != nil {
		t.Fatal(err)
	}
	if qrz.calls != 2 {
		t.Errorf("QRZ calls = %d after TTL expiry, want 2", qrz.calls)
	}

	// Unresolvable callsigns are negatively cached (no QRZ hammering).
	before := qrz.calls
	for i := 0; i < 3; i++ {
		if _, err := c.resolve("K1ABC", qrz, ctyRes); !errors.Is(err, errAlmanacUnresolved) {
			t.Fatalf("K1ABC err = %v", err)
		}
	}
	if qrz.calls != before+1 {
		t.Errorf("negative cache: QRZ calls grew by %d, want 1", qrz.calls-before)
	}

	// Engine wrapper uses the engine's resolvers.
	e := newDxBaselineEngine("")
	e.SetResolvers(nil, ctyRes)
	area, err := e.resolveAlmanacArea("DL5XX")
	if err != nil || area.Source != qthSourceDXCC {
		t.Errorf("engine resolveAlmanacArea = %+v, %v; want dxcc", area, err)
	}
	var nilEngine *DxBaselineEngine
	if area, err := nilEngine.resolveAlmanacArea("jo32"); err != nil || area.Grid4 != "JO32" {
		t.Errorf("nil engine locator = %+v, %v", area, err)
	}
}

// --- widening ---

func fullMask(days int) uint64 { return (uint64(1) << uint(days)) - 1 }

// setBands marks the first n in-scope bands of grid as active on `days` days.
func setBands(m map[almanacGridBand]uint64, grid string, n int, mask uint64) {
	for i := 0; i < n && i < len(almanacInScopeBands); i++ {
		m[almanacGridBand{Grid: grid, Band: almanacInScopeBands[i]}] |= mask
	}
}

func TestAlmanacInScopeBandsExclude6m(t *testing.T) {
	for _, b := range almanacInScopeBands {
		if b == "6m" || b == "4m" || b == "2m" {
			t.Errorf("band %s must not be in scope", b)
		}
	}
	if len(almanacInScopeBands) != 10 || almanacInScopeBands[0] != "160m" || almanacInScopeBands[9] != "10m" {
		t.Errorf("in-scope bands = %v, want 160m..10m (10 bands)", almanacInScopeBands)
	}
}

func TestChooseAlmanacRadius(t *testing.T) {
	full := fullMask(almanacMinActiveDays30)
	need := almanacBandsNeeded(len(almanacInScopeBands))

	t.Run("r0 sufficient", func(t *testing.T) {
		m := map[almanacGridBand]uint64{}
		setBands(m, "JO32", len(almanacInScopeBands), full)
		r, sq := chooseAlmanacRadius("JO32", m, almanacMinActiveDays30)
		if r != 0 || len(sq) != 1 || sq[0] != "JO32" {
			t.Errorf("r=%d squares=%v, want 0 [JO32]", r, sq)
		}
	})

	t.Run("r1 when centre thin", func(t *testing.T) {
		m := map[almanacGridBand]uint64{}
		setBands(m, "JO32", len(almanacInScopeBands), 1) // 1 day only
		setBands(m, "JO33", len(almanacInScopeBands), full)
		r, sq := chooseAlmanacRadius("JO32", m, almanacMinActiveDays30)
		if r != 1 || len(sq) != 9 {
			t.Errorf("r=%d len=%d, want 1/9", r, len(sq))
		}
	})

	t.Run("days union across squares, not summed", func(t *testing.T) {
		// Centre and neighbour both active on the SAME 5 days: union = 5 < 10
		// at every radius, so it caps at 2. Summing would falsely give 10.
		m := map[almanacGridBand]uint64{}
		setBands(m, "JO32", len(almanacInScopeBands), fullMask(5))
		setBands(m, "JO33", len(almanacInScopeBands), fullMask(5))
		if r, _ := chooseAlmanacRadius("JO32", m, almanacMinActiveDays30); r != almanacMaxWidenRadius {
			t.Errorf("r=%d, want cap %d", r, almanacMaxWidenRadius)
		}
		// Disjoint days in two squares union to 10 → r=1.
		m2 := map[almanacGridBand]uint64{}
		setBands(m2, "JO32", len(almanacInScopeBands), fullMask(5))
		setBands(m2, "JO33", len(almanacInScopeBands), fullMask(10)&^fullMask(5))
		if r, _ := chooseAlmanacRadius("JO32", m2, almanacMinActiveDays30); r != 1 {
			t.Errorf("disjoint union r=%d, want 1", r)
		}
	})

	t.Run("r2 via ring-2 square", func(t *testing.T) {
		m := map[almanacGridBand]uint64{}
		setBands(m, "JO34", len(almanacInScopeBands), full) // 2 squares north
		r, sq := chooseAlmanacRadius("JO32", m, almanacMinActiveDays30)
		if r != 2 || len(sq) != 25 {
			t.Errorf("r=%d len=%d, want 2/25", r, len(sq))
		}
	})

	t.Run("nothing sufficient caps at 2", func(t *testing.T) {
		r, sq := chooseAlmanacRadius("JO32", map[almanacGridBand]uint64{}, almanacMinActiveDays30)
		if r != almanacMaxWidenRadius || len(sq) != 25 {
			t.Errorf("empty: r=%d len=%d, want 2/25", r, len(sq))
		}
		r, _ = chooseAlmanacRadius("JO32", nil, almanacMinActiveDays30)
		if r != almanacMaxWidenRadius {
			t.Errorf("nil masks: r=%d, want 2", r)
		}
	})

	t.Run("boundary: exactly half widens, one more keeps", func(t *testing.T) {
		if need != len(almanacInScopeBands)/2+1 {
			t.Fatalf("bands needed = %d, want strictly more than half (%d)", need, len(almanacInScopeBands)/2+1)
		}
		half := len(almanacInScopeBands) / 2
		m := map[almanacGridBand]uint64{}
		setBands(m, "JO32", half, full)
		setBands(m, "JO33", len(almanacInScopeBands), full)
		if r, _ := chooseAlmanacRadius("JO32", m, almanacMinActiveDays30); r != 1 {
			t.Errorf("exactly half (%d) meeting M_min: r=%d, want widen to 1", half, r)
		}
		setBands(m, "JO32", half+1, full)
		if r, _ := chooseAlmanacRadius("JO32", m, almanacMinActiveDays30); r != 0 {
			t.Errorf("half+1 (%d) meeting M_min: r=%d, want 0", half+1, r)
		}
	})

	t.Run("M_min boundary and out-of-scope bands", func(t *testing.T) {
		m := map[almanacGridBand]uint64{}
		setBands(m, "JO32", len(almanacInScopeBands), fullMask(almanacMinActiveDays30-1))
		m[almanacGridBand{Grid: "JO32", Band: "6m"}] = full
		m[almanacGridBand{Grid: "JO32", Band: "2m"}] = full
		if r, _ := chooseAlmanacRadius("JO32", m, almanacMinActiveDays30); r != almanacMaxWidenRadius {
			t.Errorf("M_min-1 days: r=%d, want cap", r)
		}
		// Seasonal threshold (8) is met by 9 days.
		if r, _ := chooseAlmanacRadius("JO32", m, almanacMinActiveDaysSeasonal); r != 0 {
			t.Errorf("seasonal M_min: r=%d, want 0", r)
		}
	})

	t.Run("edge grid AA00 clipped", func(t *testing.T) {
		m := map[almanacGridBand]uint64{}
		r, sq := chooseAlmanacRadius("AA00", m, almanacMinActiveDays30)
		if r != 2 || len(sq) != 9 { // 3×3 quadrant survives clipping
			t.Errorf("AA00: r=%d len=%d, want 2/9", r, len(sq))
		}
		r, sq = chooseAlmanacRadius("RR99", m, almanacMinActiveDays30)
		if r != 2 || len(sq) != 9 {
			t.Errorf("RR99: r=%d len=%d, want 2/9", r, len(sq))
		}
		setBands(m, "AA01", len(almanacInScopeBands), full)
		if r, _ := chooseAlmanacRadius("AA00", m, almanacMinActiveDays30); r != 1 {
			t.Errorf("AA00 with active neighbour AA01: r=%d, want 1", r)
		}
	})

	t.Run("lowercase centre normalized", func(t *testing.T) {
		m := map[almanacGridBand]uint64{}
		setBands(m, "JO32", len(almanacInScopeBands), full)
		if r, sq := chooseAlmanacRadius("jo32ab", m, almanacMinActiveDays30); r != 0 || sq[0] != "JO32" {
			t.Errorf("lowercase: r=%d sq=%v", r, sq)
		}
	})
}

func TestAlmanacBandsMeeting(t *testing.T) {
	m := map[almanacGridBand]uint64{}
	setBands(m, "JO32", 3, fullMask(almanacMinActiveDays30))
	if got := almanacBandsMeeting([]string{"JO32"}, m, almanacMinActiveDays30); got != 3 {
		t.Errorf("bands meeting = %d, want 3", got)
	}
	if got := almanacBandsNeeded(0); got != 1 {
		t.Errorf("bands needed for 0 = %d, want 1", got)
	}
}
