package main

import (
	"math/rand"
	"path/filepath"
	"testing"
	"time"
)

// bruteRegional is the walk's own regional count (EvaluateAreaWindow before the
// index kept it): per in-scope band, the cluster-block ends of conditions-mode
// messages timed in [from, to].
func bruteRegional(msgs []MQTTMessage, ax, ay int, from, to int64) (out [clusterBandSlots]int) {
	for i := range msgs {
		m := msgs[i]
		if m.T < from || m.T > to {
			continue
		}
		if isNonConditionsMode(m.MD) || !feedsClusterBaseline(m) {
			continue
		}
		n := clusterEndCount(m, ax, ay)
		if n == 0 {
			continue
		}
		if slot := inScopeBandIndex(normalizeBand(m.B)); slot >= 0 {
			out[slot] += n
		}
	}
	return out
}

func TestSecondCountsMatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	now := time.Now().Unix()
	var s secondCounts
	s.setSpan()
	type ev struct {
		t    int64
		slot int
		n    int32
	}
	var evs []ev
	for i := 0; i < 20000; i++ {
		tt := now - 3000 + int64(rng.Intn(3300))
		switch rng.Intn(50) {
		case 0:
			tt = now + 86400*int64(1+rng.Intn(3)) // a sender with a clock a day ahead
		case 1:
			tt = now - 86400*int64(1+rng.Intn(3)) // and one behind
		}
		e := ev{tt, rng.Intn(clusterBandSlots), int32(1 + rng.Intn(2))}
		evs = append(evs, e)
		if !s.add(e.t, e.slot, e.n) {
			t.Fatalf("add %d overflowed", i)
		}
	}
	check := func(from, to int64) {
		t.Helper()
		var want [clusterBandSlots]int
		for _, e := range evs {
			if e.t >= from && e.t <= to {
				want[e.slot] += int(e.n)
			}
		}
		if got := s.sum(from, to); got != want {
			t.Fatalf("sum [%d,%d]: got %v want %v", from-now, to-now, got, want)
		}
	}
	check(now-3600, now)
	check(now-900, now+10)
	check(now-100000, now+400000) // everything, including the outliers
	check(now+3, now+2)           // empty
	// Trimming drops only what no window ending at now or later can reach.
	s.trim(now + 1200)
	keep := now + 1200 - s.span
	check(keep, now+1200)
	check(now-300, now+1200)
	if s.base > keep {
		t.Fatalf("trim moved base past the kept range: %d > %d", s.base, keep)
	}
}

func TestSecondCountsOverflowIsReported(t *testing.T) {
	var s secondCounts
	s.setSpan()
	now := time.Now().Unix()
	s.add(now, 0, 1)
	ok := true
	for i := 0; i < secondCountsMaxFar+10 && ok; i++ {
		ok = s.add(now+86400+int64(i)*2, 0, 1) // distinct far seconds
	}
	if ok {
		t.Fatal("overflow of the out-of-range map was not reported")
	}
}

// The regional counts the index keeps over its incremental scan equal the
// walk's, through arrivals, a shorter window, a dropped front and a stale
// snapshot, with unordered timestamps and some far-off ones.
func TestAreaIndexRegionalCountsMatchWalk(t *testing.T) {
	rng := rand.New(rand.NewSource(17))
	now := time.Now().Unix()
	withLiveHistoryState(t, 0, 0, 0, 60)
	msgs := randomWindowMsgs(rng, 30000, now-3600, now)
	for i := range msgs { // the feed's timestamps are not strictly ordered
		msgs[i].T += int64(rng.Intn(5)) - 2
		if i%997 == 0 {
			msgs[i].T = now + 7200 // bad clock
		}
	}
	cx, cy, _ := locatorSquareXY("JO30")
	cx, cy = clusterAnchorXY(cx, cy)
	k, ok := areaIndexKeyFor("JO32", false, explicitLiveArea("JO32", 0), squareXYToLocator(cx, cy))
	if !ok || !k.clusterOK {
		t.Fatal("key without a cluster")
	}
	c := &areaIndexCache{m: map[areaIndexKey]*areaIndexEntry{}}

	check := func(label string, hist []MQTTMessage, firstSeq uint64, liveStart, to int64) {
		t.Helper()
		r := c.lookup(k, historyWindow{msgs: hist, firstSeq: firstSeq, hubFirst: 1}, liveStart, to)
		if !r.regionalOK {
			t.Fatalf("%s: regional counts unavailable", label)
		}
		if want := bruteRegional(hist, cx, cy, liveStart, to); r.regional != want {
			t.Fatalf("%s: regional %v, walk %v", label, r.regional, want)
		}
	}

	check("first build", msgs, 1, now-3600, now)
	check("same window again", msgs, 1, now-3600, now)
	check("a later liveStart", msgs, 1, now-900, now)

	more := append(append([]MQTTMessage{}, msgs...), randomWindowMsgs(rng, 3000, now, now+60)...)
	check("new arrivals", more, 1, now-3600, now+60)

	// A request whose snapshot predates the newest scan reuses the entry; its
	// counts are by message time, so they include the newer arrivals timed
	// inside the range.
	e := c.m[k]
	base, scanned := e.base, e.scanned
	check("stale snapshot", more, 1, now-3600, now)
	r := c.lookup(k, historyWindow{msgs: msgs, firstSeq: 1, hubFirst: 1}, now-3600, now)
	if want := bruteRegional(more, cx, cy, now-3600, now); r.regional != want {
		t.Fatalf("stale snapshot: regional %v, want %v", r.regional, want)
	}
	if e.base != base || e.scanned != scanned {
		t.Fatalf("a stale snapshot rebuilt the entry (base %d->%d, scanned %d->%d)", base, e.base, scanned, e.scanned)
	}

	// The hub dropped its front: a window starting later keeps the same counts.
	drop := 6000
	check("front dropped", more[drop:], uint64(drop)+1, now-1800, now+60)
}

func TestAreaIndexRegionalCountsUnusableAfterOverflow(t *testing.T) {
	now := time.Now().Unix()
	withLiveHistoryState(t, 0, 0, 0, 60)
	n := 2 * secondCountsMaxFar
	msgs := make([]MQTTMessage, n)
	for i := range msgs {
		// Distinct seconds a day ahead, spread wider than the dense range.
		msgs[i] = MQTTMessage{T: now + 86400 + int64(i)*10, B: "20m", MD: "FT8", Source: "mqtt", SC: "A", RC: "B", SL: "JO32aa", RL: "JO32bb"}
	}
	cx, cy, _ := locatorSquareXY("JO32")
	cx, cy = clusterAnchorXY(cx, cy)
	k, _ := areaIndexKeyFor("JO32", false, explicitLiveArea("JO32", 0), squareXYToLocator(cx, cy))
	c := &areaIndexCache{m: map[areaIndexKey]*areaIndexEntry{}}
	win := historyWindow{msgs: msgs, firstSeq: 1, hubFirst: 1}
	r := c.lookup(k, win, now-3600, now)
	if r.regionalOK {
		t.Fatal("counts reported usable after the out-of-range map overflowed")
	}
	// Once the offending messages have left the hub the entry starts over.
	win2 := historyWindow{msgs: msgs[n-5:], firstSeq: uint64(n-5) + 1, hubFirst: uint64(n-5) + 1}
	if r := c.lookup(k, win2, now-3600, now); !r.regionalOK {
		t.Fatal("entry stayed unusable after the overflow cause left the hub")
	}
}

// With the counters unusable (overflow) the evaluation walks the whole window
// and still equals the unindexed result.
func TestEvaluateAreaWindowFallsBackWhenCountsOverflow(t *testing.T) {
	resetAreaIndex(t)
	rng := rand.New(rand.NewSource(23))
	eng := newDxBaselineEngine(filepath.Join(t.TempDir(), "b.json"))
	if err := eng.Load(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	withLiveHistoryState(t, 0, 0, 0, 60)
	hist := randomWindowMsgs(rng, 20000, now-3600, now)
	for _, m := range hist[:2000] {
		eng.Observe(m)
	}
	// Far-future seconds in the JO32 cluster: more distinct ones than the
	// counters hold outside their dense range.
	for i := 0; i < 2*secondCountsMaxFar; i++ {
		hist = append(hist, MQTTMessage{T: now + 86400 + int64(i)*10, B: "20m", MD: "FT8", Source: "mqtt", SC: "DK3JF", RC: "W1AW", SL: "JO32aa", RL: "FN20bb", RP: -5})
	}
	area := explicitLiveArea("JO32", 2)
	win := historyWindow{msgs: hist, firstSeq: 1, hubFirst: 1}

	areaIndexEnabled = false
	want := mustJSON(t, eng.EvaluateAreaWindow("JO32", false, 60, -15, win, now, area))
	areaIndexEnabled = true
	t.Cleanup(func() { areaIndexEnabled = true })
	got := mustJSON(t, eng.EvaluateAreaWindow("JO32", false, 60, -15, win, now, area))
	if got != want {
		t.Fatalf("indexed result differs after counter overflow\n got  %.300s\n want %.300s", got, want)
	}
	key, _ := areaIndexKeyFor("JO32", false, area, eng.deriveOperatorCluster("JO32"))
	if e := areaIdx.m[key]; e == nil || !e.ccBad {
		t.Fatal("the counters did not overflow: the fallback was not exercised")
	}
}
