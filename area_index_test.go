package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// randomWindowMsgs: ~35% of messages touch squares around JO32 (so areas of
// radius 0-3 match something), the rest are world-wide; mixed-case locators,
// some padded with spaces, some missing, a few non-FT8 modes and sources.
func randomWindowMsgs(rng *rand.Rand, n int, t0, t1 int64) []MQTTMessage {
	near := []string{"JO32", "JO31", "JO33", "JO22", "JO42", "JN32", "JN33", "JO21", "JP32", "JO52", "KO32", "IO32", "JO12", "JN22", "JM32"}
	far := []string{"FN20", "EM73", "PM95", "QF22", "OJ12", "IO91", "KP20", "RE78", "GG87", "DM79"}
	subs := []string{"", "lk", "LK", "gu", "xx", "AA"}
	band := []string{"20m", "40m", "20M", "15m", "10m", "13cm", "6m", " 17m "}
	mode := []string{"FT8", "FT8", "FT8", "FT4", "CW", "WSPR", "DXCLUSTER", ""}
	src := []string{"", "", "mqtt", "wspr", "rbn", "dxcluster"}
	calls := []string{"DK3JF", "dl1abc", "W1AW", "F5XYZ/P", "JA1ZZZ", "VK2ABC", "OH2XX", "N0CALL", "EA8ZZ"}
	loc := func() string {
		var base string
		if rng.Intn(100) < 35 {
			base = near[rng.Intn(len(near))]
		} else {
			base = far[rng.Intn(len(far))]
		}
		l := base + subs[rng.Intn(len(subs))]
		switch rng.Intn(40) {
		case 0:
			return ""
		case 1:
			return " " + l + " "
		case 2:
			return "J"
		}
		if rng.Intn(3) == 0 {
			return l
		}
		return l
	}
	out := make([]MQTTMessage, n)
	for i := range out {
		out[i] = MQTTMessage{
			T: t0 + int64(i)*(t1-t0)/int64(n+1), B: band[rng.Intn(len(band))], MD: mode[rng.Intn(len(mode))], Source: src[rng.Intn(len(src))],
			SC: calls[rng.Intn(len(calls))], RC: calls[rng.Intn(len(calls))], SL: loc(), RL: loc(), RP: rng.Intn(40) - 25,
		}
	}
	return out
}

// mustJSON marshals v in a canonical form. The engine breaks score ties by map
// iteration order (best_bands / worst_bands, the bands array), so two
// evaluations of the same input can order tied bands differently; the
// comparison sorts those lists.
func mustJSON(t *testing.T, v interface{}) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var generic interface{}
	if err := json.Unmarshal(b, &generic); err != nil {
		t.Fatal(err)
	}
	// Which of several equal-score bands make the top/bottom three is the same
	// tie-break by map order; the per-band entries in "bands" carry the data.
	if m, ok := generic.(map[string]interface{}); ok {
		for _, k := range []string{"best_bands", "worst_bands", "recommended_bands", "avoid_bands"} {
			delete(m, k)
		}
	}
	out, err := json.Marshal(canonicalize(generic))
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func canonicalize(v interface{}) interface{} {
	switch x := v.(type) {
	case map[string]interface{}:
		for k, e := range x {
			x[k] = canonicalize(e)
		}
		return x
	case []interface{}:
		for i := range x {
			x[i] = canonicalize(x[i])
		}
		sort.SliceStable(x, func(i, j int) bool {
			return fmt.Sprint(sortKey(x[i])) < fmt.Sprint(sortKey(x[j]))
		})
		return x
	}
	return v
}

func sortKey(v interface{}) interface{} {
	if m, ok := v.(map[string]interface{}); ok {
		if b, ok := m["band"]; ok {
			return b
		}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// The indexed evaluation must equal the full scan, across areas, qth kinds
// and a history that grows and loses its front between requests.
func TestEvaluateAreaWindowIndexMatchesFullScan(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	eng := newDxBaselineEngine(filepath.Join(t.TempDir(), "b.json"))
	if err := eng.Load(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	all := randomWindowMsgs(rng, 30000, now-3600, now)
	for _, m := range all[:3000] {
		eng.Observe(m)
	}

	type scenario struct {
		qth          string
		surroundings bool
		area         *liveArea
	}
	scenarios := []scenario{
		{"JO32", false, nil},
		{"JO32", true, nil},
		{"JO32", false, explicitLiveArea("JO32", 0)},
		{"JO32", false, explicitLiveArea("JO32", 1)},
		{"JO32", false, explicitLiveArea("JO32", 2)},
		{"JO32", false, explicitLiveArea("JO32", 3)},
		{"JO31", false, explicitLiveArea("JO32", 2)},
		{"FN20", false, explicitLiveArea("FN20", 2)},
		{"FN31", false, nil},
		{"DK3JF", false, nil},                        // callsign target: not indexable
		{"JO32", false, explicitLiveArea("JO32", 8)}, // too wide: not indexed
	}

	// baseSeq/oldest: simulate hub history with a dropped front.
	baseSeq := uint64(1000)
	hist := all
	for round := 0; round < 6; round++ {
		minutes := []int{15, 15, 60, 15, 30, 15}[round]
		cutoff := now - int64(minutes)*60
		idx := 0
		for idx < len(hist) && hist[idx].T < cutoff {
			idx++
		}
		win := historyWindow{msgs: hist[idx:len(hist):len(hist)], firstSeq: baseSeq + uint64(idx) + 1, hubFirst: baseSeq + 1}
		for _, sc := range scenarios {
			areaIndexEnabled = false
			want := mustJSON(t, eng.EvaluateAreaWindow(sc.qth, sc.surroundings, minutes, -15, win, now, sc.area))
			plain := mustJSON(t, eng.EvaluateArea(sc.qth, sc.surroundings, minutes, -15, win.msgs, now, sc.area))
			if plain != want {
				t.Fatalf("round %d %+v: unindexed windows disagree", round, sc)
			}
			if round == 0 && sc.area != nil && sc.area.Radius == 2 && sc.qth == "JO32" {
				var probe struct {
					Bands []interface{} `json:"bands"`
				}
				if err := json.Unmarshal([]byte(want), &probe); err != nil || len(probe.Bands) < 2 {
					t.Fatalf("scenario evaluates to %d bands (err %v): the comparison would be vacuous", len(probe.Bands), err)
				}
			}
			areaIndexEnabled = true
			got := mustJSON(t, eng.EvaluateAreaWindow(sc.qth, sc.surroundings, minutes, -15, win, now, sc.area))
			if got != want {
				t.Fatalf("round %d minutes=%d qth=%s surr=%v area=%v: indexed result differs\n got  %.400s\n want %.400s", round, minutes, sc.qth, sc.surroundings, sc.area, got, want)
			}
		}
		// New arrivals, then drop part of the front, before the next round.
		hist = append(hist, randomWindowMsgs(rng, 2000, now, now+60)...)
		if round%2 == 1 {
			drop := len(hist) / 5
			hist = hist[drop:]
			baseSeq += uint64(drop)
		}
	}
	areaIndexEnabled = true
	if len(areaIdx.m) < 6 {
		t.Fatalf("index holds %d keys: the indexed path was not exercised", len(areaIdx.m))
	}
}

func TestAreaIndexPositionsAreIncrementalAndAscending(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	now := time.Now().Unix()
	msgs := randomWindowMsgs(rng, 20000, now-900, now)
	k, ok := areaIndexKeyFor("JO32", false, explicitLiveArea("JO32", 0), "")
	if !ok {
		t.Fatal("key not indexable")
	}
	c := &areaIndexCache{m: map[areaIndexKey]*areaIndexEntry{}}
	pos, ok := c.positions(k, historyWindow{msgs: msgs, firstSeq: 1, hubFirst: 1})
	if !ok || len(pos) == 0 || len(pos) >= len(msgs)/2 {
		t.Fatalf("positions: ok=%v n=%d of %d", ok, len(pos), len(msgs))
	}
	for i, p := range pos {
		if i > 0 && p <= pos[i-1] {
			t.Fatal("positions not strictly ascending")
		}
		if !k.wants(&msgs[p]) {
			t.Fatalf("position %d does not match the key", p)
		}
	}
	// Every matching message is listed (nothing missed).
	want := 0
	for i := range msgs {
		if k.wants(&msgs[i]) {
			want++
		}
	}
	if want != len(pos) {
		t.Fatalf("listed %d, brute force finds %d", len(pos), want)
	}
	// New arrivals: only the tail is tested, the earlier matches are kept.
	more := append(append([]MQTTMessage{}, msgs...), randomWindowMsgs(rng, 500, now, now+30)...)
	pos2, ok := c.positions(k, historyWindow{msgs: more, firstSeq: 1, hubFirst: 1})
	if !ok || len(pos2) < len(pos) {
		t.Fatalf("incremental positions shrank: %d < %d", len(pos2), len(pos))
	}
	if e := c.m[k]; e.scanned != uint64(len(more)) {
		t.Fatalf("scanned %d, want %d", e.scanned, len(more))
	}
	// A window that starts later (front dropped) lists positions relative to it.
	drop := 4000
	pos3, ok := c.positions(k, historyWindow{msgs: more[drop:], firstSeq: uint64(drop) + 1, hubFirst: 1})
	if !ok {
		t.Fatal("not ok after front drop")
	}
	for _, p := range pos3 {
		if !k.wants(&more[drop+int(p)]) {
			t.Fatalf("shifted position %d does not match", p)
		}
	}
}

func TestAreaIndexSkippedWhenMostMessagesMatch(t *testing.T) {
	now := time.Now().Unix()
	msgs := make([]MQTTMessage, 300)
	for i := range msgs {
		msgs[i] = MQTTMessage{T: now, B: "20m", MD: "FT8", SC: "A", RC: "B", SL: "JO32aa", RL: "JO32bb"}
	}
	for i := 200; i < 300; i++ {
		msgs[i].SL, msgs[i].RL = "FN20aa", "FN20bb"
	}
	k, _ := areaIndexKeyFor("JO32", false, explicitLiveArea("JO32", 0), "")
	c := &areaIndexCache{m: map[areaIndexKey]*areaIndexEntry{}}
	if _, ok := c.positions(k, historyWindow{msgs: msgs, firstSeq: 1, hubFirst: 1}); ok {
		t.Fatal("index used although two thirds of the window match")
	}
}

func TestAreaIndexEvictsLeastRecentlyUsed(t *testing.T) {
	c := &areaIndexCache{m: map[areaIndexKey]*areaIndexEntry{}}
	for i := 0; i < areaIndexMaxEntries+5; i++ {
		c.entry(areaIndexKey{x: i})
	}
	if len(c.m) != areaIndexMaxEntries {
		t.Fatalf("cache holds %d entries, want %d", len(c.m), areaIndexMaxEntries)
	}
	if _, ok := c.m[areaIndexKey{x: 0}]; ok {
		t.Fatal("oldest entry survived eviction")
	}
}
