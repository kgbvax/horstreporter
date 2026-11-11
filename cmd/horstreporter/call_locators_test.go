package main

import (
	"fmt"
	"testing"
	"unsafe"
)

func TestCallLocatorCacheFeedPrecedence(t *testing.T) {
	c := newCallLocatorCache()
	const t0 = int64(1_794_400_000)

	c.observeFeed("dl1abc", "jo62", t0)
	if loc, st := c.resolve("DL1ABC", t0); st != resolveFeed || loc != "JO62" {
		t.Fatalf("lower-case feed values: %q %v", loc, st)
	}
	c.observeFeed("DL1ABC", "JO62qm", t0+10)
	if loc, _ := c.resolve("DL1ABC", t0+10); loc != "JO62QM" {
		t.Fatalf("6-char grid for the same square should refine: %q", loc)
	}
	c.observeFeed("DL1ABC", "JO62", t0+20)
	if loc, _ := c.resolve("DL1ABC", t0+20); loc != "JO62QM" {
		t.Fatalf("4-char report of the same square must keep the 6-char grid: %q", loc)
	}
	c.observeFeed("DL1ABC", "JO31", t0+5)
	if loc, _ := c.resolve("DL1ABC", t0+20); loc != "JO62QM" {
		t.Fatalf("an older report of another square replaced the grid: %q", loc)
	}
	c.observeFeed("DL1ABC", "JN58", t0+30)
	if loc, _ := c.resolve("DL1ABC", t0+30); loc != "JN58" {
		t.Fatalf("a newer report of another square must replace: %q", loc)
	}
	c.observeFeed("DL1ABC", "XX99", t0+40)
	if loc, _ := c.resolve("DL1ABC", t0+40); loc != "JN58" {
		t.Fatalf("invalid grid accepted: %q", loc)
	}
}

func TestCallLocatorCacheQRZAndNegative(t *testing.T) {
	c := newCallLocatorCache()
	const t0 = int64(1_794_400_000)

	c.storeQRZ("w1aw", "FN31pr", t0)
	if loc, st := c.resolve("W1AW", t0); st != resolveQRZ || loc != "FN31pr" {
		t.Fatalf("qrz entry: %q %v", loc, st)
	}
	c.observeFeed("W1AW", "FN31", t0+1)
	if loc, st := c.resolve("W1AW", t0+1); st != resolveFeed || loc != "FN31" {
		t.Fatalf("feed must override qrz: %q %v", loc, st)
	}
	c.storeQRZ("W1AW", "EM00", t0+2)
	if _, st := c.resolve("W1AW", t0+2); st != resolveFeed {
		t.Fatalf("qrz replaced a fresh feed grid: %v", st)
	}

	c.storeQRZ("N0CALL", "", t0)
	if _, st := c.resolve("N0CALL", t0); st != resolveNegative {
		t.Fatalf("negative entry: %v", st)
	}
	if _, st := c.resolve("N0CALL", t0+callLocatorNegativeTTL+1); st != resolveMiss {
		t.Fatalf("negative entry past its TTL: %v", st)
	}
	if _, st := c.resolve("UNKNOWN", t0); st != resolveMiss {
		t.Fatalf("unknown call: %v", st)
	}
}

func TestCallLocatorCachePortableFallback(t *testing.T) {
	c := newCallLocatorCache()
	const t0 = int64(1_794_400_000)
	c.observeFeed("DL1ABC", "JO62QM", t0)
	for call, want := range map[string]resolveState{
		"DL1ABC/P":   resolveFeed,
		"DL1ABC/QRP": resolveFeed,
		"DL1ABC/MM":  resolveMiss,
		"EA8/DL1ABC": resolveMiss,
	} {
		if _, st := c.resolve(call, t0); st != want {
			t.Errorf("resolve(%q) = %v, want %v", call, st, want)
		}
	}
	// The portable call's own entry wins over the home grid.
	c.observeFeed("DL1ABC/P", "JN49", t0)
	if loc, _ := c.resolve("DL1ABC/P", t0); loc != "JN49" {
		t.Fatalf("portable own entry: %q", loc)
	}
}

func TestCallLocatorCacheFeedExpiresAndPrunes(t *testing.T) {
	c := newCallLocatorCache()
	const t0 = int64(1_794_400_000)
	c.observeFeed("DL1ABC", "JO62", t0)
	later := t0 + callLocatorFeedTTL + 1
	if _, st := c.resolve("DL1ABC", later); st != resolveMiss {
		t.Fatalf("stale feed entry still used: %v", st)
	}
	c.prune(later)
	if feed, _, _ := c.counts(); feed != 0 {
		t.Fatalf("prune kept %d stale entries", feed)
	}
}

func TestCallLocatorResolveDoesNotAllocate(t *testing.T) {
	c := newCallLocatorCache()
	const t0 = int64(1_794_400_000)
	c.observeFeed("DL1ABC", "JO62QM", t0)
	call := "DL1ABC/P"
	if n := testing.AllocsPerRun(1000, func() { c.resolve(call, t0) }); n != 0 {
		t.Fatalf("resolve allocates %v per call", n)
	}
	// A repeat report of a known call (the walker's steady state) neither.
	if n := testing.AllocsPerRun(1000, func() { c.observeFeed("DL1ABC", "JO62qm", t0+1) }); n != 0 {
		t.Fatalf("observeFeed of a known grid allocates %v per call", n)
	}
}

// The walker reads each history message once, follows the front being
// dropped, and starts over after the backfill swaps the history.
func TestCallLocatorWalkerReadsNewMessagesOnly(t *testing.T) {
	h := &Hub{clients: map[*Client]bool{}}
	c := newCallLocatorCache()
	var w callLocatorWalker
	const t0 = int64(1_794_400_000)

	h.Lock()
	h.history = []MQTTMessage{
		{Source: "mqtt", T: t0, SC: "DL1ABC", SL: "JO62qm", RC: "W1AW", RL: "FN31"},
		{Source: "rbn", T: t0, SC: "W3LPL", SL: "FM19", RC: "K1ABC", RL: "FN42"},
		{Source: "wspr", T: t0, SC: "G4ABC", SL: "IO91", RC: "VK2XYZ", RL: "QF56"},
	}
	h.Unlock()
	if n := w.pass(h, c); n != 3 {
		t.Fatalf("first pass read %d", n)
	}
	if _, st := c.resolve("K1ABC", t0); st != resolveMiss {
		t.Fatal("RBN spots must not feed the cache (their locators come from it)")
	}
	for _, call := range []string{"DL1ABC", "W1AW", "G4ABC", "VK2XYZ"} {
		if _, st := c.resolve(call, t0); st != resolveFeed {
			t.Fatalf("%s not recorded", call)
		}
	}
	if n := w.pass(h, c); n != 0 {
		t.Fatalf("second pass re-read %d messages", n)
	}

	h.Lock()
	h.history = append(h.history, MQTTMessage{Source: "mqtt", T: t0 + 1, SC: "SP9X", SL: "KN09", RC: "OK1A", RL: "JO70"})
	h.dropFrontLocked(2)
	h.Unlock()
	if n := w.pass(h, c); n != 1 {
		t.Fatalf("after append + front drop read %d, want 1", n)
	}

	h.Lock()
	h.replaceHistoryLocked([]MQTTMessage{{Source: "mqtt", T: t0, SC: "EA1X", SL: "IN53", RC: "CT1Y", RL: "IM58"}})
	h.Unlock()
	if n := w.pass(h, c); n != 1 {
		t.Fatalf("after a history swap read %d, want the whole new history", n)
	}
	if _, st := c.resolve("CT1Y", t0); st != resolveFeed {
		t.Fatal("backfilled history not recorded")
	}
}

// A full cache still stores QRZ answers (else the worker asks QRZ again for
// the same call forever), and the walker's shrink frees room from the feed.
func TestCallLocatorCacheFullKeepsQRZAndShrinksFeed(t *testing.T) {
	c := newCallLocatorCache()
	const now = int64(1_794_400_000)
	for i := 0; i < callLocatorMaxEntries; i++ {
		seen := now - 2*24*3600 // two days old: shrinkable
		if i%2 == 0 {
			seen = now // fresh
		}
		c.m[fmt.Sprintf("K%dX", i)] = &callLocatorEntry{loc: "FN31", src: callLocFeed, seen: seen}
	}
	c.observeFeed("DL9NEW", "JO62", now)
	if _, st := c.resolve("DL9NEW", now); st != resolveMiss || !c.full {
		t.Fatalf("feed insert into a full cache: %v full=%v", st, c.full)
	}
	c.storeQRZ("W9QRZ", "EN52", now)
	c.storeQRZ("N9NONE", "", now)
	if _, st := c.resolve("W9QRZ", now); st != resolveQRZ {
		t.Fatalf("QRZ answer dropped by a full cache: %v", st)
	}
	if _, st := c.resolve("N9NONE", now); st != resolveNegative {
		t.Fatalf("negative answer dropped by a full cache: %v", st)
	}
	if n := c.shrinkFeed(now); n != callLocatorMaxEntries/2 {
		t.Fatalf("shrink dropped %d, want the %d day-old feed entries", n, callLocatorMaxEntries/2)
	}
	if _, st := c.resolve("W9QRZ", now); st != resolveQRZ {
		t.Fatal("shrink dropped a QRZ entry")
	}
	if c.shrinkFeed(now) != 0 {
		t.Fatal("shrink ran without the cache filling up again")
	}
}

func TestCallLocatorPortablePrefersBaseFeedGrid(t *testing.T) {
	c := newCallLocatorCache()
	const now = int64(1_794_400_000)
	c.storeQRZ("DL1ABC/P", "", now)
	if _, st := c.resolve("DL1ABC/P", now); st != resolveNegative {
		t.Fatalf("negative portable entry: %v", st)
	}
	c.observeFeed("DL1ABC", "JO62QM", now+1)
	if loc, st := c.resolve("DL1ABC/P", now+1); st != resolveFeed || loc != "JO62QM" {
		t.Fatalf("base feed grid hidden by the portable negative: %q %v", loc, st)
	}
	c.storeQRZ("DK2XY/P", "JN49", now)
	c.observeFeed("DK2XY", "JO40", now)
	if loc, _ := c.resolve("DK2XY/P", now); loc != "JO40" {
		t.Fatalf("a QRZ grid for the portable call beat the base call's feed grid: %q", loc)
	}
}

// Stored keys and locators must not alias the caller's strings (feed calls
// and grids are substrings of MQTT topics).
func TestCallLocatorCacheDoesNotKeepCallerStrings(t *testing.T) {
	c := newCallLocatorCache()
	topic := []byte("DL1ABC JO62QM")
	call, grid := string(topic[:6]), string(topic[7:])
	c.observeFeed(call, grid, 1)
	c.observeFeed(call, grid, 2) // refresh: must not replace the key
	for k, e := range c.m {
		if unsafe.StringData(k) == unsafe.StringData(call) {
			t.Fatal("key aliases the caller's call")
		}
		if unsafe.StringData(e.loc) == unsafe.StringData(grid) {
			t.Fatal("locator aliases the caller's grid")
		}
	}
}
