package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// withHubHistory swaps hub.history for the test and resets the live-area
// decision cache and the history-complete marker before and after.
func withHubHistory(t *testing.T, h []MQTTMessage) {
	t.Helper()
	reset := func() {
		liveHistoryCompleteSince.Store(0)
		liveAreaCache.Lock()
		liveAreaCache.m = make(map[string]liveAreaCacheEntry)
		liveAreaCache.Unlock()
	}
	hub.Lock()
	orig := hub.history
	hub.history = h
	hub.Unlock()
	reset()
	t.Cleanup(func() {
		hub.Lock()
		hub.history = orig
		hub.Unlock()
		reset()
	})
}

// laBandsByName indexes a response's bands: bands with equal scores are sorted
// in map-iteration order, so band slices are compared by name.
func laBandsByName(resp dxConditionsResponse) map[string]dxBandCondition {
	out := make(map[string]dxBandCondition, len(resp.Bands))
	for _, b := range resp.Bands {
		out[b.Band] = b
	}
	return out
}

func laBand(t *testing.T, resp dxConditionsResponse, band string) (dxBandCondition, bool) {
	t.Helper()
	for _, b := range resp.Bands {
		if b.Band == band {
			return b, true
		}
	}
	return dxBandCondition{}, false
}

// A sparse square (FN76OJ) has one band at home and the rest one to three
// squares away: today it sees one band, widened it sees all of them.
func TestEvaluateAreaWidensSparseSquare(t *testing.T) {
	liveHistoryCompleteSince.Store(0)
	e := newDxBaselineEngine("")
	history := laConcat(laMsgs("20m", 0, 30), laMsgs("40m", 1, 30), laMsgs("15m", 2, 30), laMsgs("30m", 3, 5))

	legacy := e.Evaluate("FN76OJ", false, 20, -24, history, laNow)
	if len(legacy.Bands) != 1 || legacy.Bands[0].Band != "20m" || legacy.Area != nil {
		t.Fatalf("legacy view should see only 20m and carry no area, got %d bands area=%v", len(legacy.Bands), legacy.Area)
	}

	area, decided := resolveLiveAreaFrom("FN76OJ", false, laNow, history)
	if !decided || area.Radius != 2 || !area.Widened {
		t.Fatalf("resolved area: %+v decided=%v, want radius 2 widened", area, decided)
	}
	wide := e.EvaluateArea("FN76OJ", false, 20, -24, history, laNow, area)
	if wide.Area != area {
		t.Fatal("response must carry the area")
	}
	if _, ok := laBand(t, wide, "30m"); ok {
		t.Fatal("30m links sit 3 squares away, outside the 5×5 block")
	}
	for band, links := range map[string]int{"20m": 30, "40m": 30, "15m": 30} {
		b, ok := laBand(t, wide, band)
		if !ok || b.CurrentLinks == 0 {
			t.Fatalf("%s missing from the widened view (links=%d)", band, b.CurrentLinks)
		}
		_ = links
		var sum float64
		for _, v := range b.ActivityByBin {
			sum += v
		}
		if sum <= 0 {
			t.Errorf("%s: activity series is empty in the widened view", band)
		}
	}
	if b, _ := laBand(t, wide, "20m"); b.AreaWidened {
		t.Error("20m is full at home, so it must not be marked widened")
	}
	if b, _ := laBand(t, wide, "40m"); !b.AreaWidened {
		t.Error("40m only has a sample through widening")
	}
}

// A dense area resolves to the base radius and must not change any band.
func TestEvaluateAreaDenseAreaMatchesLegacy(t *testing.T) {
	liveHistoryCompleteSince.Store(0)
	e := newDxBaselineEngine("")
	history := laConcat(laMsgs("20m", 0, 30), laMsgs("40m", 0, 30), laMsgs("15m", 0, 30))
	area, _ := resolveLiveAreaFrom("FN76OJ", false, laNow, history)
	if area.Radius != 0 || area.Widened {
		t.Fatalf("dense area widened: %+v", area)
	}
	legacy := e.Evaluate("FN76OJ", false, 20, -24, history, laNow)
	withArea := e.EvaluateArea("FN76OJ", false, 20, -24, history, laNow, area)
	if !reflect.DeepEqual(laBandsByName(legacy), laBandsByName(withArea)) || legacy.OverallScore != withArea.OverallScore {
		t.Fatal("a radius-0 area must give exactly the legacy result")
	}
}

// Radius 1 is the 3×3 surroundings block, whichever way it was asked for.
func TestEvaluateAreaRadiusOneEqualsSurroundings(t *testing.T) {
	liveHistoryCompleteSince.Store(0)
	e := newDxBaselineEngine("")
	history := laConcat(laMsgs("20m", 0, 12), laMsgs("40m", 1, 12), laMsgs("15m", 2, 12))
	legacy := e.Evaluate("FN76OJ", true, 20, -24, history, laNow)
	viaArea := e.EvaluateArea("FN76OJ", false, 20, -24, history, laNow, explicitLiveArea("FN76OJ", 1))
	if !reflect.DeepEqual(laBandsByName(legacy), laBandsByName(viaArea)) {
		t.Fatal("rings=1 must equal surroundings=true")
	}
	if len(viaArea.Bands) != 2 {
		t.Fatalf("expected 20m and 40m only, got %d bands", len(viaArea.Bands))
	}
}

func TestHotBandsAreaEchoesArea(t *testing.T) {
	liveHistoryCompleteSince.Store(0)
	e := newDxBaselineEngine("")
	area := explicitLiveArea("FN76OJ", 2)
	resp := e.HotBandsArea("FN76OJ", false, 20, -24, "", nil, laNow, area)
	if resp.Area != area {
		t.Fatalf("hot bands must echo the area, got %v", resp.Area)
	}
	if plain := e.HotBands("FN76OJ", false, 20, -24, "", nil, laNow); plain.Area != nil {
		t.Fatalf("no area requested, got %v", plain.Area)
	}
}

func TestBuildActivityByBinFromHistory(t *testing.T) {
	area := explicitLiveArea("FN76", 2)
	now := laNow
	in := func(ago int64, band, loc string, rp int) MQTTMessage {
		return MQTTMessage{T: now - ago, B: band, SL: loc + "AA", RL: "JO32AB", RP: rp}
	}
	history := []MQTTMessage{
		in(10, "20m", "FN76", -10),   // newest bin
		in(20, "20m", "FN77", -10),   // newest bin
		in(1190, "20m", "FN78", -10), // oldest bin (window 20 min = 1200 s)
		in(300, "20m", "FN76", -30),  // below the SNR floor
		in(300, "20m", "FN79", -10),  // outside the block
		in(300, "13cm", "FN76", -10), // out-of-scope band
		in(3000, "20m", "FN76", -10), // outside the window
		in(50, "40m", "FN75", -10),
	}
	got := buildActivityByBinFromHistory(history, area, -24, 20, now)
	// 12 bins of 100 s: rates are per minute, so one spot = 0.6.
	if len(got["20m"]) != activityBins || got["20m"][activityBins-1] != 2*0.6 || got["20m"][0] != 0.6 {
		t.Fatalf("20m series = %v", got["20m"])
	}
	var rest float64
	for _, v := range got["20m"][1 : activityBins-1] {
		rest += v
	}
	if rest != 0 {
		t.Fatalf("20m middle bins must be empty, series = %v", got["20m"])
	}
	if _, ok := got["13cm"]; ok {
		t.Fatal("out-of-scope band must not appear")
	}
	if got["40m"][activityBins-1] != 0.6 {
		t.Fatalf("40m series = %v", got["40m"])
	}
	if len(buildActivityByBinFromHistory(history, nil, -24, 20, now)) != 0 {
		t.Fatal("nil area yields no series")
	}
}

func TestEvaluateV2AreaMatchesWideBlock(t *testing.T) {
	now := time.Now().Unix()
	cx, cy, _ := locatorSquareXY("FN76")
	near := squareXYToLocator(cx+1, cy) + "AA"
	far := squareXYToLocator(cx+2, cy) + "AA"
	// PSKReporter FT8 paths from the neighbourhood of FN76 to Europe.
	history := []MQTTMessage{
		{T: now - 60, B: "20m", MD: "FT8", SC: "K1AAA", SL: near, RC: "DL1XYZ", RL: "JO32AB", RP: -8},
		{T: now - 50, B: "20m", MD: "FT8", SC: "K1BBB", SL: far, RC: "DL1XYZ", RL: "JO32AB", RP: -8},
	}
	profiles := v2Spots("pskr")

	legacy := propIntelV2.EvaluateV2("FN76OJ", false, 15, profiles, nil, nil, history, now, propIntelAtypicalZThreshold)
	if len(legacy.Cells) == 0 {
		t.Fatal("legacy view still places the receiver's region")
	}
	for _, c := range legacy.Cells {
		if c.FromHere {
			t.Fatal("legacy: neither end is in FN76, so nothing is from here")
		}
	}

	area := explicitLiveArea("FN76OJ", 2)
	wide := propIntelV2.EvaluateV2Area("FN76OJ", false, 15, profiles, nil, nil, history, now, propIntelAtypicalZThreshold, area)
	if wide.Area != area {
		t.Fatal("response must carry the area")
	}
	c := findV2Cell(t, wide, "20m", "EU")
	if !c.FromHere || c.SpotCount != 2 {
		t.Fatalf("wide area cell: fromHere=%v spots=%d, want true/2", c.FromHere, c.SpotCount)
	}
	if got := wide.applyFromHere(true); len(got.Cells) != 1 {
		t.Fatalf("from_here filter keeps the widened cell, got %d cells", len(got.Cells))
	}
}

func TestStreamHandlerAnnouncesAreaBeforeHistory(t *testing.T) {
	now := time.Now().Unix()
	withHubHistory(t, []MQTTMessage{
		{SC: "K1AAA", RC: "DL1XYZ", SL: "FN76AB", RL: "JO32AB", RP: -10, T: now - 10, B: "20m", MD: "FT8"},
	})
	server := httptest.NewServer(http.HandlerFunc(streamHandler))
	defer server.Close()

	read := func(query string, n int) []string {
		resp, err := http.Get(server.URL + query)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		reader := bufio.NewReader(resp.Body)
		var lines []string
		for len(lines) < n {
			l, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("read after %v: %v", lines, err)
			}
			if l = strings.TrimRight(l, "\n"); l != "" {
				lines = append(lines, l)
			}
		}
		return lines
	}

	lines := read("?qth=FN76OJ&rings=auto", 4)
	if lines[0] != "event: area" || !strings.HasPrefix(lines[1], "data: {") {
		t.Fatalf("first frame must be the area event, got %q", lines[:2])
	}
	var area liveArea
	if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[1], "data: ")), &area); err != nil {
		t.Fatalf("area payload: %v", err)
	}
	if area.Centre != "FN76" || area.Radius != 0 || area.Widened {
		t.Fatalf("thin feed must stay at the base radius, got %+v", area)
	}
	if !strings.HasPrefix(lines[2], "data: {") || !strings.Contains(lines[2], `"locator":"JO32AB"`) || lines[3] != "event: history_end" {
		t.Fatalf("history follows the area event, got %q", lines[2:])
	}

	lines = read("?qth=FN76OJ&rings=2", 2)
	if lines[0] != "event: area" || !strings.Contains(lines[1], `"radius":2`) || !strings.Contains(lines[1], `"widened":false`) {
		t.Fatalf("explicit rings=2: %q", lines)
	}

	// Without rings the stream is exactly as before: no area event.
	lines = read("?qth=FN76OJ", 1)
	if strings.HasPrefix(lines[0], "event:") {
		t.Fatalf("no rings param must not announce an area, got %q", lines[0])
	}
}

func TestStreamAutoAreaMatchesNeighbourSquares(t *testing.T) {
	now := time.Now().Unix()
	cx, cy, _ := locatorSquareXY("FN76")
	var h []MQTTMessage
	for _, b := range []string{"20m", "40m", "15m"} {
		for i := 0; i < 30; i++ {
			h = append(h, MQTTMessage{SC: "K1AAA", RC: "DL1XYZ", SL: squareXYToLocator(cx+2, cy) + "AA", RL: "JO32AB", RP: -10, T: now - 10, B: b, MD: "FT8"})
		}
	}
	withHubHistory(t, h)
	server := httptest.NewServer(http.HandlerFunc(streamHandler))
	defer server.Close()

	resp, err := http.Get(server.URL + "?qth=FN76OJ&rings=auto")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	first, _ := reader.ReadString('\n')
	data, _ := reader.ReadString('\n')
	if strings.TrimSpace(first) != "event: area" || !strings.Contains(data, `"radius":2`) || !strings.Contains(data, `"widened":true`) {
		t.Fatalf("expected a widened radius-2 area, got %q %q", first, data)
	}
	// The spots two squares away are delivered as the operator's own.
	_, _ = reader.ReadString('\n')
	spot, _ := reader.ReadString('\n')
	if !strings.HasPrefix(spot, "data: {") || !strings.Contains(spot, `"locator":"JO32AB"`) {
		t.Fatalf("expected a matched spot after the area event, got %q", spot)
	}
}

func TestDxConditionsHandlerReturnsArea(t *testing.T) {
	origDx := dxBaseline
	t.Cleanup(func() { dxBaseline = origDx })
	dxBaseline = newDxBaselineEngine("")

	now := time.Now().Unix()
	cx, cy, _ := locatorSquareXY("FN76")
	var h []MQTTMessage
	for _, b := range []string{"20m", "40m", "15m"} {
		for i := 0; i < 30; i++ {
			h = append(h, MQTTMessage{SC: "K1AAA", RC: "DL1XYZ", SL: squareXYToLocator(cx+1, cy) + "AA", RL: "JO32AB", RP: -10, T: now - 10, B: b, MD: "FT8"})
		}
	}
	withHubHistory(t, h)
	server := httptest.NewServer(http.HandlerFunc(dxConditionsHandler))
	defer server.Close()

	get := func(query string) map[string]interface{} {
		resp, err := http.Get(server.URL + query)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		var payload map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return payload
	}

	auto := get("?qth=FN76OJ&rings=auto&minutes=15")
	area, ok := auto["area"].(map[string]interface{})
	if !ok || area["centre"] != "FN76" || area["radius"].(float64) != 1 || area["widened"] != true {
		t.Fatalf("rings=auto must widen to radius 1 here, got %v", auto["area"])
	}
	bands, _ := auto["bands"].([]interface{})
	if len(bands) != 3 {
		t.Fatalf("expected 3 bands in the widened view, got %d", len(bands))
	}
	if b := bands[0].(map[string]interface{}); b["area_widened"] != true {
		t.Fatalf("bands that only exist through widening are marked, got %v", b["area_widened"])
	}

	if plain := get("?qth=FN76OJ&minutes=15"); plain["area"] != nil {
		t.Fatalf("no rings param must not add an area, got %v", plain["area"])
	} else if got, _ := plain["bands"].([]interface{}); len(got) != 0 {
		t.Fatalf("neighbour-square spots are invisible without widening, got %d bands", len(got))
	}
}

// A page load fires several endpoints at once; they must share one decision
// (and one history copy) instead of each computing its own.
func TestLiveAreaForConcurrentMissesShareOneDecision(t *testing.T) {
	withHubHistory(t, laConcat(laMsgs("20m", 1, 30), laMsgs("40m", 1, 30), laMsgs("15m", 1, 30)))
	// laMsgs stamps laNow-60; decide as of laNow.
	const n = 16
	got := make([]*liveArea, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			got[i] = liveAreaFor("FN76OJ", false, laNow)
		}(i)
	}
	close(start)
	wg.Wait()
	for i := 1; i < n; i++ {
		if got[i] != got[0] {
			t.Fatalf("goroutine %d got a different decision: %p vs %p", i, got[i], got[0])
		}
	}
	if got[0] == nil || got[0].Radius != 1 || !got[0].Widened {
		t.Fatalf("decision: %+v", got[0])
	}
}
