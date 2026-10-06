package main

import (
	"net/http/httptest"
	"testing"
)

const laNow = int64(1_800_000_000)

// laMsgs returns n FT8 reports on band whose sender sits d squares east of
// FN76 (the receiver is far away, in JO32).
func laMsgs(band string, d, n int) []MQTTMessage {
	cx, cy, _ := locatorSquareXY("FN76")
	loc := squareXYToLocator(cx+d, cy)
	out := make([]MQTTMessage, n)
	for i := range out {
		out[i] = MQTTMessage{T: laNow - 60, B: band, MD: "FT8", SC: "K1ABC", SL: loc + "AA", RC: "DL1XYZ", RL: "JO32AB", RP: -10}
	}
	return out
}

func laConcat(parts ...[]MQTTMessage) []MQTTMessage {
	var out []MQTTMessage
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func laResolve(t *testing.T, surroundings bool, h []MQTTMessage) (*liveArea, bool) {
	t.Helper()
	liveHistoryCompleteSince.Store(0)
	a, decided := resolveLiveAreaFrom("FN76OJ", surroundings, laNow, h)
	if a == nil {
		t.Fatal("nil area for a locator qth")
	}
	return a, decided
}

func TestResolveLiveAreaDenseStaysAtBase(t *testing.T) {
	h := laConcat(laMsgs("20m", 0, 30), laMsgs("40m", 0, 30), laMsgs("15m", 0, 30))
	a, decided := laResolve(t, false, h)
	if a.Radius != 0 || a.Widened || !decided {
		t.Fatalf("radius=%d widened=%v decided=%v, want 0/false/true", a.Radius, a.Widened, decided)
	}
	if a.Centre != "FN76" || a.BaseRadius != 0 {
		t.Fatalf("centre=%q base=%d", a.Centre, a.BaseRadius)
	}
	if a.widenedBand("20m") {
		t.Fatal("no band is widened in an unwidened area")
	}
}

func TestResolveLiveAreaWidensToSmallestSufficientRadius(t *testing.T) {
	// One band is full at the centre; two more only fill up at ring 1 and 2.
	h := laConcat(laMsgs("20m", 0, 30), laMsgs("40m", 1, 30), laMsgs("15m", 2, 30), laMsgs("30m", 3, 30))
	a, decided := laResolve(t, false, h)
	if a.Radius != 2 || !a.Widened || !decided {
		t.Fatalf("radius=%d widened=%v decided=%v, want 2/true/true", a.Radius, a.Widened, decided)
	}
	if a.widenedBand("20m") {
		t.Fatal("20m is full at the base radius, not widened")
	}
	if !a.widenedBand("40m") || !a.widenedBand("15m") {
		t.Fatal("40m and 15m only have a sample because of widening")
	}
}

func TestResolveLiveAreaCapsAtMaxRadius(t *testing.T) {
	// Links exist but no radius ever gets 3 full bands: use the widest block.
	h := laConcat(laMsgs("20m", 3, 30), laMsgs("40m", 3, 30))
	a, decided := laResolve(t, false, h)
	if a.Radius != liveAreaMaxRadius || !a.Widened || !decided {
		t.Fatalf("radius=%d widened=%v decided=%v, want %d/true/true", a.Radius, a.Widened, decided, liveAreaMaxRadius)
	}
}

func TestResolveLiveAreaIgnoresLinksBeyondMaxRadius(t *testing.T) {
	h := laMsgs("20m", liveAreaMaxRadius+1, 100)
	a, decided := laResolve(t, false, h)
	if a.Radius != 0 || a.Widened || decided {
		t.Fatalf("radius=%d widened=%v decided=%v, want 0/false/false", a.Radius, a.Widened, decided)
	}
}

func TestResolveLiveAreaSurroundingsBaseIsOne(t *testing.T) {
	dense := laConcat(laMsgs("20m", 0, 30), laMsgs("40m", 1, 30), laMsgs("15m", 1, 30))
	a, _ := laResolve(t, true, dense)
	if a.Radius != 1 || a.BaseRadius != 1 || a.Widened {
		t.Fatalf("radius=%d base=%d widened=%v, want 1/1/false", a.Radius, a.BaseRadius, a.Widened)
	}
	thin := laConcat(laMsgs("20m", 2, 30), laMsgs("40m", 2, 30), laMsgs("15m", 2, 30))
	a, _ = laResolve(t, true, thin)
	if a.Radius != 2 || !a.Widened {
		t.Fatalf("radius=%d widened=%v, want 2/true", a.Radius, a.Widened)
	}
}

func TestResolveLiveAreaTooFewLinksStaysAtBase(t *testing.T) {
	a, decided := laResolve(t, false, laMsgs("20m", 1, liveAreaFullSampleLinks-1))
	if a.Radius != 0 || a.Widened || decided {
		t.Fatalf("radius=%d widened=%v decided=%v, want 0/false/false", a.Radius, a.Widened, decided)
	}
}

func TestResolveLiveAreaUndecidedWhileHistoryIncomplete(t *testing.T) {
	t.Cleanup(func() { liveHistoryCompleteSince.Store(0) })
	h := laConcat(laMsgs("20m", 1, 30), laMsgs("40m", 1, 30), laMsgs("15m", 1, 30))
	liveHistoryCompleteSince.Store(laNow - 5*60) // restart 5 min ago: window not covered
	a, decided := resolveLiveAreaFrom("FN76OJ", false, laNow, h)
	if a.Radius != 0 || a.Widened || decided {
		t.Fatalf("radius=%d widened=%v decided=%v, want 0/false/false", a.Radius, a.Widened, decided)
	}
}

func TestResolveLiveAreaCountsOnlyConditionsSpots(t *testing.T) {
	h := laMsgs("20m", 0, 30)
	for i := range h {
		h[i].Source = "rbn"
		h[i].MD = "CW"
	}
	h = append(h, laMsgs("2m", 0, 0)...)
	a, decided := laResolve(t, false, h)
	if decided || len(a.baseLinks) != 0 {
		t.Fatalf("RBN spots must not count: decided=%v baseLinks=%v", decided, a.baseLinks)
	}
	// Out-of-scope bands and weak reports do not count either.
	weak := laMsgs("20m", 0, 40)
	for i := range weak {
		weak[i].RP = defaultDxCwViableMinDb - 1
	}
	if _, decided := laResolve(t, false, weak); decided {
		t.Fatal("reports below the SNR floor must not count")
	}
	if _, decided := laResolve(t, false, laMsgs("13cm", 0, 40)); decided {
		t.Fatal("out-of-scope bands must not count")
	}
}

func TestResolveLiveAreaCallsignQthHasNoArea(t *testing.T) {
	if a, _ := resolveLiveAreaFrom("VE9CF", false, laNow, nil); a != nil {
		t.Fatalf("callsign qth must not resolve an area, got %+v", a)
	}
}

func TestLiveAreaContains(t *testing.T) {
	a := explicitLiveArea("FN76OJ", 2)
	for loc, want := range map[string]bool{
		"FN76": true, "FN76AB": true, "FN78": true, "FN74": true, "FN56": true, "FN79": false,
		"FN46": false, "FO70": false, "FN73": false, "GN76": false, "fn76aa": true, "": false, "??": false,
	} {
		if got := a.contains(loc); got != want {
			t.Errorf("contains(%q) = %v, want %v", loc, got, want)
		}
	}
	// The block is clipped at the edge of the grid instead of wrapping.
	edge := explicitLiveArea("AA00", 3)
	for loc, want := range map[string]bool{"AA00": true, "AA33": true, "AA04": false, "AA40": false, "RR99": false} {
		if got := edge.contains(loc); got != want {
			t.Errorf("edge contains(%q) = %v, want %v", loc, got, want)
		}
	}
	if explicitLiveArea("VE9CF", 2) != nil || explicitLiveArea("FN76", 0) != nil {
		t.Fatal("explicit area needs a locator and rings > 0")
	}
	if got := explicitLiveArea("FN76", 500).Radius; got != maxAreaRings {
		t.Fatalf("explicit rings clamp: got %d, want %d", got, maxAreaRings)
	}
}

func TestParseRingsParam(t *testing.T) {
	for _, tc := range []struct {
		query     string
		rings     int
		auto      bool
		wantLabel string
	}{
		{"", 0, false, "absent"},
		{"rings=auto", 0, true, "auto"},
		{"rings=AUTO", 0, true, "auto is case-insensitive"},
		{"rings=2", 2, false, "int"},
		{"rings=99", maxAreaRings, false, "clamped"},
		{"rings=-3", 0, false, "negative"},
		{"rings=abc", 0, false, "garbage"},
	} {
		r := httptest.NewRequest("GET", "/api/x?"+tc.query, nil)
		rings, auto := parseRingsParam(r)
		if rings != tc.rings || auto != tc.auto {
			t.Errorf("%s: got (%d, %v), want (%d, %v)", tc.wantLabel, rings, auto, tc.rings, tc.auto)
		}
	}
}

func TestLiveAreaForCachesTheDecision(t *testing.T) {
	setHistory := func(h []MQTTMessage) {
		hub.Lock()
		hub.history = h
		hub.Unlock()
	}
	t.Cleanup(func() {
		setHistory(nil)
		liveHistoryCompleteSince.Store(0)
		liveAreaCache.Lock()
		liveAreaCache.m = make(map[string]liveAreaCacheEntry)
		liveAreaCache.Unlock()
	})
	liveHistoryCompleteSince.Store(0)
	liveAreaCache.Lock()
	liveAreaCache.m = make(map[string]liveAreaCacheEntry)
	liveAreaCache.Unlock()

	thin := laConcat(laMsgs("20m", 1, 30), laMsgs("40m", 1, 30), laMsgs("15m", 1, 30))
	setHistory(thin)
	first := liveAreaFor("FN76OJ", false, laNow)
	if first == nil || first.Radius != 1 || !first.Widened {
		t.Fatalf("first decision: %+v", first)
	}

	// The feed changes, but the decision holds until the TTL has passed.
	dense := laConcat(laMsgs("20m", 0, 30), laMsgs("40m", 0, 30), laMsgs("15m", 0, 30))
	setHistory(dense)
	if again := liveAreaFor("FN76OJ", false, laNow+liveAreaCacheTTLSeconds-1); again != first {
		t.Fatalf("decision must be cached inside the TTL, got %+v", again)
	}
	later := laNow + liveAreaCacheTTLSeconds + 1
	for i := range dense {
		dense[i].T = later - 60
	}
	after := liveAreaFor("FN76OJ", false, later)
	if after == first || after.Radius != 0 || after.Widened {
		t.Fatalf("decision must be recomputed after the TTL, got %+v", after)
	}

	// surroundings is part of the key.
	if s := liveAreaFor("FN76OJ", true, later); s == after || s.BaseRadius != 1 {
		t.Fatalf("surroundings must not share the base-0 entry: %+v", s)
	}
	if liveAreaFor("VE9CF", false, later) != nil {
		t.Fatal("callsign qth must resolve no area")
	}
}
