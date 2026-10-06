package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestComputeFromHereDayCurves(t *testing.T) {
	sums := []fromHereSlotSum{
		{Band: "20m", Region: "NA", Slot: 0, Sum: 280},
		{Band: "20m", Region: "NA", Slot: 47, Sum: 14},
		{Band: "20m", Region: "NA", Slot: 5, Sum: 99}, // no covered day: stays 0
		{Band: "70cm", Region: "EU", Slot: 3, Sum: 50}, // out of scope
		{Band: "20m", Region: "JA", Slot: 48, Sum: 1},  // bad slot
	}
	covered := map[int]int{0: 28, 47: 7, 3: 28}
	got := computeFromHereDayCurves(sums, covered)
	c := got.ByCell[propIntelCellKey{band: "20m", region: "NA"}]
	if len(c) != 48 || c[0] != 10 || c[47] != 2 || c[5] != 0 {
		t.Fatalf("20m NA curve = %v", c)
	}
	if len(got.ByCell) != 1 {
		t.Fatalf("only in-scope cells with valid slots: %v", got.ByCell)
	}
}

func TestFromHereTrendBins(t *testing.T) {
	now := time.Now().Unix()
	history := []MQTTMessage{
		pskrSpot(now-10, "20m", "JO32AB", "FN31AB", -5),   // last bin
		pskrSpot(now-1000, "20m", "JO32AB", "FN31AB", -5), // bin 8
		pskrSpot(now-3599, "20m", "JO32AB", "FN31AB", -5), // first bin
		pskrSpot(now-4000, "20m", "JO32AB", "FN31AB", -5), // older than the hour
		pskrSpot(now+5, "20m", "JO32AB", "FN31AB", -5),    // future
		pskrSpot(now-20, "20m", "JN58AA", "FN42AA", -5),   // global mesh
		pskrSpot(now-30, "80m", "JO31AA", "JO33BB", -5),   // both ends in the area: twice
		{Source: "wspr", T: now - 20, B: "20m", SC: "RX", SL: "JO32CD", RC: "TX", RL: "FN31AB", RP: -10, TXPower: 37},
		{Source: "dxcluster", T: now - 40, B: "20m", SC: "DX", SL: "FN31AB", RC: "SPOTTER", RL: "JO32AA"},
	}
	trend := fromHereTrend(history, v2Spots("wspr", "pskr", "dxcluster"), qthSquares("JO32", true), nil, now)
	na := trend[propIntelCellKey{band: "20m", region: "NA"}]
	want := []int{1, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 2}
	if len(na) != fromHereTrendBins {
		t.Fatalf("20m NA trend = %v", na)
	}
	for i := range want {
		if na[i] != want[i] {
			t.Fatalf("20m NA trend = %v, want %v (pskr + dxcluster, no wspr, no mesh)", na, want)
		}
	}
	if eu := trend[propIntelCellKey{band: "80m", region: "EU"}]; eu[11] != 2 {
		t.Fatalf("a path inside the area counts once per end: %v", eu)
	}
	if len(trend) != 2 {
		t.Fatalf("only cells with spots: %v", trend)
	}
	if got := fromHereTrend(history, v2Spots("wspr", "rbn"), qthSquares("JO32", true), nil, now); len(got) != 0 {
		t.Fatalf("no normal sources selected, no trend: %v", got)
	}
}

func TestFromHereTrendMatchesExpectedSpots(t *testing.T) {
	now := time.Now().Unix()
	history := fromHereFixture(now)
	normals := &fromHereNormals{SampleDays: 28, ByCell: map[propIntelCellKey]fromHereNormal{
		{band: "20m", region: "NA"}: {Expected: 3, StdDev: 1, SampleDays: 28},
	}}
	resp := propIntelV2.EvaluateV2FromHereDetail("JO32", true, 15, v2Spots("wspr", "pskr"), nil, nil, history, now, propIntelAtypicalZThreshold, nil,
		propIntelV2FromHere{normals: normals, trend: true, trendHistory: history})
	c := findV2Cell(t, resp, "20m", "NA")
	if len(c.Trend) != fromHereTrendBins || c.ExpectedSpots == nil {
		t.Fatalf("cell %+v", c)
	}
	if last3 := c.Trend[9] + c.Trend[10] + c.Trend[11]; last3 != *c.ExpectedSpots {
		t.Fatalf("the last three bins are the 15-minute window: %v vs %d", c.Trend, *c.ExpectedSpots)
	}
	if resp.TrendBinMinutes != 5 {
		t.Fatalf("trend_bin_minutes = %d", resp.TrendBinMinutes)
	}
}

func TestEvaluateV2FromHereSilentCells(t *testing.T) {
	now := time.Now().Unix()
	history := fromHereFixture(now)
	normals := &fromHereNormals{SampleDays: 28, ByCell: map[propIntelCellKey]fromHereNormal{
		{band: "20m", region: "NA"}:  {Expected: 3, StdDev: 1, SampleDays: 28},  // live: never silent
		{band: "15m", region: "JA"}:  {Expected: 12, StdDev: 3, SampleDays: 28}, // normally busy, nothing now
		{band: "12m", region: "JA"}:  {Expected: 2, StdDev: 1, SampleDays: 28},  // under the floor
		{band: "70cm", region: "EU"}: {Expected: 50, StdDev: 1, SampleDays: 28},
		{band: "20m", region: "XX"}:  {Expected: 50, StdDev: 1, SampleDays: 28},
	}}
	plain := propIntelV2.EvaluateV2FromHere("JO32", true, 15, v2Spots("pskr"), nil, nil, history, now, propIntelAtypicalZThreshold, nil, normals)
	for _, c := range plain.Cells {
		if c.Silent || c.NormalDay != nil || c.Trend != nil {
			t.Fatalf("detail is opt-in: %+v", c)
		}
	}
	curve := make([]float64, 48)
	curve[10] = 24
	fh := propIntelV2FromHere{
		normals:    normals,
		withSilent: true,
		dayCurves:  &fromHereDayCurves{ByCell: map[propIntelCellKey][]float64{{band: "15m", region: "JA"}: curve}},
		trend:      true,
	}
	resp := propIntelV2.EvaluateV2FromHereDetail("JO32", true, 15, v2Spots("pskr"), nil, nil, history, now, propIntelAtypicalZThreshold, nil, fh)
	silent := map[string]propIntelV2Cell{}
	for _, c := range resp.Cells {
		if c.Silent {
			silent[c.Band+"/"+c.Region] = c
		}
	}
	if len(silent) != 1 {
		t.Fatalf("want exactly 15m/JA silent, got %v", silent)
	}
	c := silent["15m/JA"]
	if !c.FromHere || c.SpotCount != 0 || *c.Expected != 12 || *c.ExpectedSpots != 0 || c.PerSource == nil || c.ActiveSources == nil {
		t.Fatalf("unexpected silent cell %+v", c)
	}
	if len(c.NormalDay) != 48 || c.NormalDay[10] != 24 || len(c.Trend) != fromHereTrendBins {
		t.Fatalf("silent cells carry the requested detail: %+v", c)
	}
	if got := resp.applyFromHere(true); len(got.Cells) != len(resp.Cells) {
		t.Fatal("from_here must keep silent cells")
	}
	raw, _ := json.Marshal(c)
	for _, want := range []string{`"silent":true`, `"expected":12`, `"expected_spots":0`, `"sources":[]`, `"trend":[0,0,0,0,0,0,0,0,0,0,0,0]`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("silent cell JSON %s lacks %s", raw, want)
		}
	}
	live := findV2Cell(t, resp, "20m", "NA")
	if live.NormalDay != nil {
		t.Fatalf("no curve loaded for 20m NA: %+v", live.NormalDay)
	}

	// PSKReporter not selected: none of the detail applies.
	wspr := propIntelV2.EvaluateV2FromHereDetail("JO32", true, 15, v2Spots("wspr"), nil, nil, history, now, propIntelAtypicalZThreshold, nil, fh)
	for _, c := range wspr.Cells {
		if c.Silent || c.Trend != nil || c.NormalDay != nil {
			t.Fatalf("without pskr there is no detail: %+v", c)
		}
	}
	if fromHereSilentCells(nil, nil) != nil || fromHereSilentCells(&fromHereNormals{SampleDays: 1}, nil) != nil {
		t.Fatal("no normal, no silent cells")
	}
}

// fakeDaySource serves fixed day sums after an optional delay.
type fakeDaySource struct {
	calls   atomic.Int32
	delay   time.Duration
	err     error
	sums    []fromHereSlotSum
	covered map[int]int
	bands   []string
	days    [2]int64
}

func (f *fakeDaySource) FromHereDaySums(ctx context.Context, grids, bands []string, dayFrom, dayTo int64) ([]fromHereSlotSum, map[int]int, error) {
	f.calls.Add(1)
	f.bands, f.days = bands, [2]int64{dayFrom, dayTo}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return f.sums, f.covered, f.err
}

func withFromHereDaySource(t *testing.T, src fromHereDaySumsSource) {
	t.Helper()
	saved := fromHereDaySource
	fromHereDaySource = func() fromHereDaySumsSource { return src }
	fromHereDayCache.reset()
	t.Cleanup(func() {
		fromHereDaySource = saved
		fromHereDayCache.reset()
	})
}

func TestFromHereDayCurvesForCachesPerDay(t *testing.T) {
	now := time.Now().Unix()
	today := utcDayIndex(now)
	src := &fakeDaySource{sums: []fromHereSlotSum{{Band: "40m", Region: "EU", Slot: 3, Sum: 56}}, covered: map[int]int{3: 28}}
	withFromHereDaySource(t, src)
	got := fromHereDayCurvesFor([]string{"JO32", "JO31"}, now, time.Second)
	if got == nil || got.ByCell[propIntelCellKey{band: "40m", region: "EU"}][3] != 2 {
		t.Fatalf("day curves = %+v", got)
	}
	if fromHereDayCurvesFor([]string{"JO31", "JO32"}, now, 0) == nil {
		t.Fatal("a cached curve needs no wait")
	}
	if c := src.calls.Load(); c != 1 {
		t.Fatalf("one fetch per area and day, got %d", c)
	}
	if src.days != [2]int64{today - fromHereNormalDays, today - 1} || len(src.bands) != len(inScopeBandNames) {
		t.Fatalf("query range %v bands %v", src.days, src.bands)
	}
}

func TestFromHereDayCurvesForSlowAndFailing(t *testing.T) {
	now := time.Now().Unix()
	slow := &fakeDaySource{delay: 100 * time.Millisecond, covered: map[int]int{}}
	withFromHereDaySource(t, slow)
	if fromHereDayCurvesFor([]string{"JO32"}, now, 0) != nil {
		t.Fatal("a running fetch must not block")
	}
	deadline := time.Now().Add(2 * time.Second)
	for fromHereDayCurvesFor([]string{"JO32"}, now, 20*time.Millisecond) == nil {
		if time.Now().After(deadline) {
			t.Fatal("the background fetch never filled the cache")
		}
	}

	failing := &fakeDaySource{err: errors.New("db down")}
	withFromHereDaySource(t, failing)
	if fromHereDayCurvesFor([]string{"JO32"}, now, time.Second) != nil || fromHereDayCurvesFor([]string{"JO32"}, now, time.Second) != nil {
		t.Fatal("a failed fetch has no curves")
	}
	if c := failing.calls.Load(); c != 1 {
		t.Fatalf("a failure is cached briefly, got %d fetches", c)
	}

	withFromHereDaySource(t, nil)
	fromHereDaySource = func() fromHereDaySumsSource { return nil }
	if fromHereDayCurvesFor([]string{"JO32"}, now, 0) != nil {
		t.Fatal("no store, no curves")
	}
	fromHereDaySource = func() fromHereDaySumsSource { return &fakeDaySource{} }
	if fromHereDayCurvesFor(nil, now, 0) != nil {
		t.Fatal("no area, no curves")
	}
}

func TestFromHereDaySumsWithoutPool(t *testing.T) {
	var s *dxPostgresStore
	if _, _, err := s.FromHereDaySums(context.Background(), []string{"JO32"}, []string{"20m"}, 1, 2); !errors.Is(err, errNoFromHereStore) {
		t.Fatalf("err = %v", err)
	}
}

// TestPropIntelV2HandlerFromHereDetail: the experimental looks' parameters add
// silent cells, day curves and the trend; without them the payload is as before.
func TestPropIntelV2HandlerFromHereDetail(t *testing.T) {
	now := time.Now().Unix()
	today := utcDayIndex(now)
	rows := append(steadyRows("20m", "NA", 30, today-40, today), steadyRows("15m", "JA", 40, today-40, today)...)
	withFromHereSource(t, &fakeFromHereSource{rows: rows, cov: fullCoverage(today-40, today)})
	withFromHereDaySource(t, &fakeDaySource{
		sums:    []fromHereSlotSum{{Band: "20m", Region: "NA", Slot: 0, Sum: 840}, {Band: "15m", Region: "JA", Slot: 1, Sum: 1120}},
		covered: map[int]int{0: 28, 1: 28},
	})

	hub.Lock()
	orig := hub.history
	hub.history = fromHereFixture(now)
	hub.Unlock()
	defer func() {
		hub.Lock()
		hub.history = orig
		hub.Unlock()
	}()

	server := httptest.NewServer(http.HandlerFunc(propIntelV2Handler))
	defer server.Close()
	get := func(query string) propIntelV2Response {
		t.Helper()
		resp, err := http.Get(server.URL + "/api/prop_intel/v2?qth=JO32&surroundings=true&from_here=true&sources=wspr,pskr" + query)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var decoded propIntelV2Response
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			t.Fatal(err)
		}
		return decoded
	}

	plain := get("")
	if len(plain.Cells) != 1 || plain.Cells[0].Trend != nil || plain.Cells[0].NormalDay != nil || plain.TrendBinMinutes != 0 {
		t.Fatalf("default payload unchanged: %+v", plain)
	}

	detail := get("&silent=1&normal_day=1&trend=1")
	if detail.TrendBinMinutes != 5 {
		t.Fatalf("trend_bin_minutes = %d", detail.TrendBinMinutes)
	}
	live := findV2Cell(t, detail, "20m", "NA")
	if len(live.NormalDay) != 48 || live.NormalDay[0] != 30 || len(live.Trend) != 12 || live.Trend[11] != 6 {
		t.Fatalf("live cell detail: normal_day %v trend %v", live.NormalDay, live.Trend)
	}
	silent := findV2Cell(t, detail, "15m", "JA")
	if !silent.Silent || silent.SpotCount != 0 || len(silent.NormalDay) != 48 || silent.NormalDay[1] != 40 {
		t.Fatalf("silent cell: %+v", silent)
	}
}
