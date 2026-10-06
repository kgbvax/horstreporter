package main

import (
	"context"
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

// levels builds chooseAlmanacRadius input where each radius r has the first
// nBands[r] in-scope bands at `slots` known slots and the rest at none.
func levels(slots int, nBands [almanacLevels]int) [almanacLevels][]int {
	var k [almanacLevels][]int
	for r := range k {
		k[r] = make([]int, len(almanacInScopeBands))
		for b := 0; b < nBands[r] && b < len(k[r]); b++ {
			k[r][b] = slots
		}
	}
	return k
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
	all := len(almanacInScopeBands)
	need := almanacBandsNeeded(all)
	min := almanacWidenMinKnownSlots

	t.Run("r0 sufficient", func(t *testing.T) {
		r, sq := chooseAlmanacRadius("JO32", levels(min, [almanacLevels]int{all, all, all}), min)
		if r != 0 || len(sq) != 1 || sq[0] != "JO32" {
			t.Errorf("r=%d squares=%v, want 0 [JO32]", r, sq)
		}
	})

	t.Run("r1 when centre thin", func(t *testing.T) {
		r, sq := chooseAlmanacRadius("JO32", levels(min, [almanacLevels]int{0, all, all}), min)
		if r != 1 || len(sq) != 9 {
			t.Errorf("r=%d len=%d, want 1/9", r, len(sq))
		}
	})

	t.Run("r2 via the outer ring", func(t *testing.T) {
		r, sq := chooseAlmanacRadius("JO32", levels(min, [almanacLevels]int{0, 0, all}), min)
		if r != 2 || len(sq) != 25 {
			t.Errorf("r=%d len=%d, want 2/25", r, len(sq))
		}
	})

	t.Run("nothing sufficient caps at 2", func(t *testing.T) {
		r, sq := chooseAlmanacRadius("JO32", [almanacLevels][]int{}, min)
		if r != almanacMaxWidenRadius || len(sq) != 25 {
			t.Errorf("empty: r=%d len=%d, want 2/25", r, len(sq))
		}
		r, _ = chooseAlmanacRadius("JO32", levels(min-1, [almanacLevels]int{all, all, all}), min)
		if r != almanacMaxWidenRadius {
			t.Errorf("one slot short everywhere: r=%d, want cap", r)
		}
	})

	t.Run("boundary: exactly half widens, one more keeps", func(t *testing.T) {
		if need != all/2+1 {
			t.Fatalf("bands needed = %d, want strictly more than half (%d)", need, all/2+1)
		}
		half := all / 2
		if r, _ := chooseAlmanacRadius("JO32", levels(min, [almanacLevels]int{half, all, all}), min); r != 1 {
			t.Errorf("exactly half (%d) known: r=%d, want widen to 1", half, r)
		}
		if r, _ := chooseAlmanacRadius("JO32", levels(min, [almanacLevels]int{half + 1, all, all}), min); r != 0 {
			t.Errorf("half+1 (%d) known: r=%d, want 0", half+1, r)
		}
	})

	t.Run("known-slot threshold boundary", func(t *testing.T) {
		if r, _ := chooseAlmanacRadius("JO32", levels(min, [almanacLevels]int{all, all, all}), min); r != 0 {
			t.Errorf("exactly %d known slots: r=%d, want 0", min, r)
		}
		if r, _ := chooseAlmanacRadius("JO32", levels(min-1, [almanacLevels]int{all, all, all}), min); r == 0 {
			t.Errorf("%d known slots must not suffice", min-1)
		}
	})

	t.Run("edge grid AA00 clipped", func(t *testing.T) {
		r, sq := chooseAlmanacRadius("AA00", [almanacLevels][]int{}, min)
		if r != 2 || len(sq) != 9 { // 3×3 quadrant survives clipping
			t.Errorf("AA00: r=%d len=%d, want 2/9", r, len(sq))
		}
		r, sq = chooseAlmanacRadius("RR99", [almanacLevels][]int{}, min)
		if r != 2 || len(sq) != 9 {
			t.Errorf("RR99: r=%d len=%d, want 2/9", r, len(sq))
		}
		if r, _ := chooseAlmanacRadius("AA00", levels(min, [almanacLevels]int{0, all, all}), min); r != 1 {
			t.Errorf("AA00 known from ring 1: r=%d, want 1", r)
		}
	})

	t.Run("lowercase centre normalized", func(t *testing.T) {
		if r, sq := chooseAlmanacRadius("jo32ab", levels(min, [almanacLevels]int{all, all, all}), min); r != 0 || sq[0] != "JO32" {
			t.Errorf("lowercase: r=%d sq=%v", r, sq)
		}
	})
}

func TestAlmanacBandsKnown(t *testing.T) {
	if got := almanacBandsKnown([]int{9, 8, 7, 0, 48}, 8); got != 3 {
		t.Errorf("bands known = %d, want 3", got)
	}
	if got := almanacBandsNeeded(0); got != 1 {
		t.Errorf("bands needed for 0 = %d, want 1", got)
	}
}

// A rural square: spots on a dozen days on most bands, but always in the same
// two half-hours, so no lane can show more than a couple of slots. The old
// test (any spot on ≥ M_min days) called that "enough" and stayed at radius 0
// while the panel read "not enough data" nearly everywhere; per-slot
// knownness widens to the neighbours that do have coverage.
func TestAlmanacRuralSquareWidens(t *testing.T) {
	f := newAggWorld()
	win := almanacWindowFor(aggTestNow.Unix())
	for d := win.Start; d < win.Start+12; d++ {
		for _, b := range almanacInScopeBands[:8] {
			f.add("JO32", b, "EU", d, 20, 1)
		}
	}
	f.activeAllBands("JO33", win.Start, win.End, allSlots())

	acc, err := readAlmanacAccum(context.Background(), f, "JO32", win, false, -1)
	if err != nil {
		t.Fatalf("readAlmanacAccum: %v", err)
	}
	known := acc.knownSlotCounts(almanacMinActiveDays30)
	for bi, b := range almanacInScopeBands[:8] {
		if k := known[0][bi]; k == 0 || k >= almanacWidenMinKnownSlots {
			t.Fatalf("%s at r=0: %d known slots, want a few but fewer than %d", b, k, almanacWidenMinKnownSlots)
		}
		if known[1][bi] < almanacSeasonSlotsPerDay-1 {
			t.Errorf("%s at r=1: %d known slots, want nearly all (JO33 covers the day)", b, known[1][bi])
		}
	}
	typ := computeAlmanacTypical(acc)
	if typ.Radius != 1 || len(typ.Squares) != 9 {
		t.Fatalf("radius = %d squares = %d, want 1 / 9", typ.Radius, len(typ.Squares))
	}
	// The lanes are read at the widened radius: JO33's coverage is in them.
	if l := findLane(t, typ, "20m", "EU"); l.unknown(10) {
		t.Errorf("20m slot 10 must be known at radius 1 (m=%d)", l.M[10])
	}
}

// Known-slot counts only grow with the radius (rings add coverage).
func TestAlmanacKnownSlotCountsMonotonic(t *testing.T) {
	f := newAggWorld()
	win := almanacWindowFor(aggTestNow.Unix())
	f.activeAllBands("JO32", win.Start, win.End, slotRange(0, 5))
	f.activeAllBands("JO33", win.Start, win.End, slotRange(10, 20))
	f.activeAllBands("JO34", win.Start, win.End, slotRange(30, 40))
	acc, err := readAlmanacAccum(context.Background(), f, "JO32", win, false, -1)
	if err != nil {
		t.Fatalf("readAlmanacAccum: %v", err)
	}
	known := acc.knownSlotCounts(almanacMinActiveDays30)
	for bi := range almanacInScopeBands {
		if !(known[0][bi] < known[1][bi] && known[1][bi] < known[2][bi]) {
			t.Fatalf("band %d: known slots by radius = %d, %d, %d, want strictly increasing",
				bi, known[0][bi], known[1][bi], known[2][bi])
		}
	}
}
