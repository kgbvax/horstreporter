package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// unixAt is day dayIndex at hh:mm UTC.
func unixAt(dayIndex int64, hh, mm int) int64 {
	return dayIndex*86400 + int64(hh*3600+mm*60)
}

func TestFromHereWindowSegments(t *testing.T) {
	const day = int64(20730)
	cases := []struct {
		name    string
		now     int64
		minutes int
		want    []windowSegment
	}{
		{"inside one slot", unixAt(day, 21, 50), 15, []windowSegment{{Slot: 43, Weight: 0.5}}},
		{"straddles two slots", unixAt(day, 21, 40), 15, []windowSegment{{Slot: 42, Weight: 5.0 / 30}, {Slot: 43, Weight: 10.0 / 30}}},
		{"ends on a boundary", unixAt(day, 22, 0), 30, []windowSegment{{Slot: 43, Weight: 1}}},
		{"crosses midnight", unixAt(day, 0, 5), 15, []windowSegment{{Slot: 47, DayOffset: -1, Weight: 10.0 / 30}, {Slot: 0, Weight: 5.0 / 30}}},
		{"no window", unixAt(day, 12, 0), 0, nil},
	}
	for _, tc := range cases {
		got := fromHereWindowSegments(tc.now, tc.minutes)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
		for i := range got {
			if got[i].Slot != tc.want[i].Slot || got[i].DayOffset != tc.want[i].DayOffset || math.Abs(got[i].Weight-tc.want[i].Weight) > 1e-9 {
				t.Fatalf("%s: segment %d = %+v, want %+v", tc.name, i, got[i], tc.want[i])
			}
		}
	}
}

// fullCoverage marks every slot of days [from, to] as ingested.
func fullCoverage(from, to int64) map[fromHereDaySlot]bool {
	cov := map[fromHereDaySlot]bool{}
	for d := from; d <= to; d++ {
		for s := 0; s < 48; s++ {
			cov[fromHereDaySlot{Day: d, Slot: s}] = true
		}
	}
	return cov
}

func TestComputeFromHereNormalsWeightsSlotsAndCountsZeroDays(t *testing.T) {
	const today = int64(20730)
	now := unixAt(today, 21, 40) // 5 min of slot 42 + 10 min of slot 43
	cov := fullCoverage(today-40, today)
	delete(cov, fromHereDaySlot{Day: 20710, Slot: 43}) // no ingest: day 20710 drops out
	rows := []fromHereCountRow{
		{Band: "20m", Region: "NA", Slot: 42, Day: 20720, Count: 60},  // 60 × 5/30 = 10
		{Band: "20m", Region: "NA", Slot: 43, Day: 20720, Count: 30},  // 30 × 10/30 = 10
		{Band: "20m", Region: "NA", Slot: 43, Day: 20710, Count: 900}, // day without coverage
		{Band: "20m", Region: "NA", Slot: 43, Day: today, Count: 900}, // today is not a reference day
		{Band: "20m", Region: "NA", Slot: 44, Day: 20721, Count: 900}, // slot outside the window
	}
	n := computeFromHereNormals(rows, cov, now, 15)
	if n.SampleDays != fromHereNormalDays-1 {
		t.Fatalf("SampleDays = %d, want %d", n.SampleDays, fromHereNormalDays-1)
	}
	got := n.ByCell[propIntelCellKey{band: "20m", region: "NA"}]
	days := float64(n.SampleDays)
	wantMean := 20 / days
	if math.Abs(got.Expected-wantMean) > 1e-9 {
		t.Fatalf("Expected = %v, want %v (one day of 20 over %v days, zeros included)", got.Expected, wantMean, days)
	}
	wantSD := math.Sqrt((math.Pow(20-wantMean, 2) + (days-1)*math.Pow(wantMean, 2)) / (days - 1))
	if math.Abs(got.StdDev-wantSD) > 1e-9 {
		t.Fatalf("StdDev = %v, want %v", got.StdDev, wantSD)
	}
}

func TestComputeFromHereNormalsAcrossMidnight(t *testing.T) {
	const today = int64(20730)
	now := unixAt(today, 0, 5) // 10 min of yesterday's slot 47 + 5 min of slot 0
	rows := []fromHereCountRow{
		{Band: "40m", Region: "EU", Slot: 47, Day: 20719, Count: 30}, // reference day 20720's evening before
		{Band: "40m", Region: "EU", Slot: 0, Day: 20720, Count: 60},
	}
	n := computeFromHereNormals(rows, fullCoverage(today-40, today), now, 15)
	got := n.ByCell[propIntelCellKey{band: "40m", region: "EU"}]
	want := 20.0 / float64(fromHereNormalDays) // 30×10/30 + 60×5/30 on one day
	if math.Abs(got.Expected-want) > 1e-9 {
		t.Fatalf("Expected = %v, want %v", got.Expected, want)
	}
}

func TestComputeFromHereNormalsWithoutCoverage(t *testing.T) {
	now := unixAt(20730, 12, 10)
	n := computeFromHereNormals([]fromHereCountRow{{Band: "20m", Region: "NA", Slot: 24, Day: 20720, Count: 5}}, nil, now, 15)
	if n.SampleDays != 0 || len(n.ByCell) != 0 {
		t.Fatalf("no coverage must mean no normal, got %+v", n)
	}
}

func TestFromHereRemoteEnds(t *testing.T) {
	qthSet := qthSquares("JO32", true)
	pskr := func(sl, rl string) *MQTTMessage {
		return &MQTTMessage{SC: "TX", SL: sl, RC: "RX", RL: rl}
	}
	cases := []struct {
		name string
		m    *MQTTMessage
		a, b string
	}{
		{"sender in the area", pskr("JO32AB", "FN31AB"), "", "FN31AB"},
		{"receiver in the area", pskr("FN31AB", "JO31CD"), "FN31AB", ""},
		{"both ends, two squares", pskr("JO31AA", "JO33BB"), "JO31AA", "JO33BB"},
		{"both ends, one square", pskr("JO32AA", "JO32BB"), "JO32AA", ""},
		{"neither end", pskr("FN31AB", "PM95AA"), "", ""},
	}
	for _, tc := range cases {
		a, b := fromHereRemoteEnds(tc.m, qthSet, nil, "rc")
		if a != tc.a || b != tc.b {
			t.Fatalf("%s: got (%q, %q), want (%q, %q)", tc.name, a, b, tc.a, tc.b)
		}
	}

	// WSPR keys the receiver on SC/SL.
	wspr := &MQTTMessage{SC: "RX", SL: "JO32AB", RC: "TX", RL: "FN31AB"}
	if a, b := fromHereRemoteEnds(wspr, qthSet, nil, "sc"); a != "FN31AB" || b != "" {
		t.Fatalf("wspr receiver in area: got (%q, %q)", a, b)
	}
	// Callsign QTH matches by call.
	byCall := &MQTTMessage{SC: "DL1ABC", SL: "JN58AA", RC: "RX", RL: "FN31AB"}
	if a, b := fromHereRemoteEnds(byCall, []string{"DL1ABC"}, nil, "rc"); a != "" || b != "FN31AB" {
		t.Fatalf("callsign QTH: got (%q, %q)", a, b)
	}
	// A widened area matches by square distance.
	area := explicitLiveArea("JO32", 2)
	if a, b := fromHereRemoteEnds(pskr("JO52AA", "FN31AB"), qthSet, area, "rc"); b != "FN31AB" || a != "" {
		t.Fatalf("area of radius 2 must contain JO52: got (%q, %q)", a, b)
	}
}

func TestFromHereAreaGrids(t *testing.T) {
	if got := fromHereAreaGrids("JO32", false, nil); len(got) != 1 || got[0] != "JO32" {
		t.Fatalf("own square: %v", got)
	}
	if got := fromHereAreaGrids("JO32ab", true, nil); len(got) != 9 {
		t.Fatalf("surroundings: %v", got)
	}
	if got := fromHereAreaGrids("JO32", false, explicitLiveArea("JO32", 2)); len(got) != 25 {
		t.Fatalf("radius 2: %d squares", len(got))
	}
	if got := fromHereAreaGrids("JO32", false, explicitLiveArea("JO32", fromHereMaxRadius+1)); got != nil {
		t.Fatalf("wider than fromHereMaxRadius must have no normal: %d squares", len(got))
	}
	if got := fromHereAreaGrids("DL1ABC", true, nil); got != nil {
		t.Fatalf("callsign QTH must have no area grids: %v", got)
	}
}

// pskrSpot is a PSKReporter FT8 report, sender → receiver (RC/RL = receiver).
func pskrSpot(ts int64, band, senderLoc, receiverLoc string, snr int) MQTTMessage {
	return MQTTMessage{T: ts, B: band, MD: "FT8", SC: "TX", SL: senderLoc, RC: "RX", RL: receiverLoc, RP: snr}
}

func fromHereFixture(now int64) []MQTTMessage {
	h := []MQTTMessage{}
	// Six weak from-here reports, JO32 heard in North America.
	for i := 0; i < 6; i++ {
		h = append(h, pskrSpot(now-int64(10+i), "20m", "JO32AB", "FN31AB", -20))
	}
	// Strong global-mesh reports into North America from outside the area.
	for i := 0; i < 50; i++ {
		h = append(h, pskrSpot(now-int64(10+i), "20m", "JN58AA", "FN42AA", 10))
	}
	// One from-here WSPR report (receiver in JO32, sender in NA).
	h = append(h, MQTTMessage{Source: "wspr", T: now - 20, B: "20m", MD: "WSPR", SC: "RX", SL: "JO32CD", RC: "TX", RL: "FN31AB", RP: -10, TXPower: 37})
	return h
}

func TestEvaluateV2FromHereCountsOnlyAreaSpots(t *testing.T) {
	now := time.Now().Unix()
	history := fromHereFixture(now)
	profiles := v2Spots("wspr", "pskr")

	global := propIntelV2.EvaluateV2Area("JO32", true, 15, profiles, nil, nil, history, now, propIntelAtypicalZThreshold, nil)
	g := findV2Cell(t, global, "20m", "NA")
	if g.SpotCount != 57 || !g.SSBOpen {
		t.Fatalf("global view keeps its mesh counts: %+v", g)
	}

	resp := propIntelV2.EvaluateV2FromHere("JO32", true, 15, profiles, nil, nil, history, now, propIntelAtypicalZThreshold, nil, nil, false)
	if !resp.FromHere {
		t.Fatal("from-here response must say so")
	}
	if len(resp.Cells) != 1 {
		t.Fatalf("only the from-here cell may appear, got %+v", resp.Cells)
	}
	c := resp.Cells[0]
	if c.Band != "20m" || c.Region != "NA" || !c.FromHere {
		t.Fatalf("unexpected cell %+v", c)
	}
	if c.SpotCount != 7 {
		t.Fatalf("spot_count = %d, want 7 (6 pskr + 1 wspr from the area)", c.SpotCount)
	}
	if findV2SourceCell(t, c, "pskr").SpotCount != 6 || findV2SourceCell(t, c, "wspr").SpotCount != 1 {
		t.Fatalf("per-source counts must be from-here only: %+v", c.PerSource)
	}
	if c.SSBOpen {
		t.Fatal("SSB open must come from from-here reports only (the strong ones are global)")
	}
	if c.Expected != nil || c.ExpectedSpots != nil || c.Atypical != nil {
		t.Fatalf("without a normal there is no expected or atypical: %+v", c)
	}

	// The handler's from-here filter keeps everything.
	if got := resp.applyFromHere(true); len(got.Cells) != 1 {
		t.Fatalf("applyFromHere dropped from-here cells: %+v", got.Cells)
	}
}

func TestEvaluateV2FromHereLocalPathCountsOncePerEnd(t *testing.T) {
	now := time.Now().Unix()
	history := []MQTTMessage{
		pskrSpot(now-10, "80m", "JO31AA", "JO33BB", -5), // both ends in the area, two squares
		pskrSpot(now-11, "80m", "JO32AA", "JO32BB", -5), // both ends in one square
	}
	resp := propIntelV2.EvaluateV2FromHere("JO32", true, 15, v2Spots("pskr"), nil, nil, history, now, propIntelAtypicalZThreshold, nil, nil, false)
	c := findV2Cell(t, resp, "80m", "EU")
	if c.SpotCount != 3 {
		t.Fatalf("spot_count = %d, want 3 (2 + 1, mirroring the baseline keys)", c.SpotCount)
	}
}

func TestEvaluateV2FromHereNormalAndSurge(t *testing.T) {
	now := time.Now().Unix()
	history := fromHereFixture(now)
	normals := &fromHereNormals{SampleDays: 28, ByCell: map[propIntelCellKey]fromHereNormal{
		{band: "20m", region: "NA"}: {Expected: 2, StdDev: 0.5, SampleDays: 28},
	}}
	resp := propIntelV2.EvaluateV2FromHere("JO32", true, 15, v2Spots("wspr", "pskr"), nil, nil, history, now, propIntelAtypicalZThreshold, nil, normals, false)
	c := findV2Cell(t, resp, "20m", "NA")
	if c.Expected == nil || *c.Expected != 2 {
		t.Fatalf("expected = %v, want 2", c.Expected)
	}
	if c.ExpectedSpots == nil || *c.ExpectedSpots != 6 {
		t.Fatalf("expected_spots = %v, want 6 (PSKReporter only, the normal's basis)", c.ExpectedSpots)
	}
	if c.Atypical == nil || c.Atypical.ZScore != 8 {
		t.Fatalf("z = (6 - 2) / 0.5 = 8 must flag a surge, got %+v", c.Atypical)
	}
	if s := findV2SourceCell(t, c, "pskr"); s.Atypical == nil || s.SampleDays != 28 {
		t.Fatalf("the pskr source carries the surge and the normal's depth: %+v", s)
	}
	if s := findV2SourceCell(t, c, "wspr"); s.Atypical != nil {
		t.Fatalf("wspr has no from-here normal, so no surge: %+v", s)
	}
	if c.AtypicalAgreement != 0.5 {
		t.Fatalf("atypical_agreement = %v, want 0.5 (1 of 2 sources)", c.AtypicalAgreement)
	}

	// Below the threshold: expected but no surge.
	calm := &fromHereNormals{SampleDays: 28, ByCell: map[propIntelCellKey]fromHereNormal{
		{band: "20m", region: "NA"}: {Expected: 5, StdDev: 2, SampleDays: 28},
	}}
	c = findV2Cell(t, propIntelV2.EvaluateV2FromHere("JO32", true, 15, v2Spots("pskr"), nil, nil, history, now, propIntelAtypicalZThreshold, nil, calm, false), "20m", "NA")
	if c.Expected == nil || c.Atypical != nil {
		t.Fatalf("a normal cell has expected and no atypical: %+v", c)
	}

	// A cell the reference days never saw: expected 0, no z.
	none := &fromHereNormals{SampleDays: 28, ByCell: map[propIntelCellKey]fromHereNormal{}}
	c = findV2Cell(t, propIntelV2.EvaluateV2FromHere("JO32", true, 15, v2Spots("pskr"), nil, nil, history, now, propIntelAtypicalZThreshold, nil, none, false), "20m", "NA")
	if c.Expected == nil || *c.Expected != 0 || c.Atypical != nil {
		t.Fatalf("unseen cell: expected 0, no atypical, got %+v", c)
	}

	// Too few live reports for a surge, however small the normal's spread.
	sparse := &fromHereNormals{SampleDays: 28, ByCell: map[propIntelCellKey]fromHereNormal{
		{band: "20m", region: "NA"}: {Expected: 0.2, StdDev: 0.4, SampleDays: 28},
	}}
	few := []MQTTMessage{pskrSpot(now-10, "20m", "JO32AB", "FN31AB", -10), pskrSpot(now-11, "20m", "JO32AB", "FN31AB", -10)}
	c = findV2Cell(t, propIntelV2.EvaluateV2FromHere("JO32", true, 15, v2Spots("pskr"), nil, nil, few, now, propIntelAtypicalZThreshold, nil, sparse, false), "20m", "NA")
	if c.Atypical != nil {
		t.Fatalf("%d reports must not make a surge (min %d): %+v", 2, fromHereSurgeMinSpots, c.Atypical)
	}

	// Too few reference days: no normal at all.
	thin := &fromHereNormals{SampleDays: propIntelMinSampleDays - 1, ByCell: normals.ByCell}
	c = findV2Cell(t, propIntelV2.EvaluateV2FromHere("JO32", true, 15, v2Spots("pskr"), nil, nil, history, now, propIntelAtypicalZThreshold, nil, thin, false), "20m", "NA")
	if c.Expected != nil || c.Atypical != nil {
		t.Fatalf("under %d sample days there is no normal: %+v", propIntelMinSampleDays, c)
	}

	// PSKReporter not selected: the normal does not describe what is shown.
	c = findV2Cell(t, propIntelV2.EvaluateV2FromHere("JO32", true, 15, v2Spots("wspr"), nil, nil, history, now, propIntelAtypicalZThreshold, nil, normals, false), "20m", "NA")
	if c.Expected != nil || c.Atypical != nil {
		t.Fatalf("without pskr selected there is no expected: %+v", c)
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
	}}
	plain := propIntelV2.EvaluateV2FromHere("JO32", true, 15, v2Spots("pskr"), nil, nil, history, now, propIntelAtypicalZThreshold, nil, normals, false)
	for _, c := range plain.Cells {
		if c.Silent {
			t.Fatalf("silent cells are opt-in: %+v", c)
		}
	}
	resp := propIntelV2.EvaluateV2FromHere("JO32", true, 15, v2Spots("pskr"), nil, nil, history, now, propIntelAtypicalZThreshold, nil, normals, true)
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
	if got := resp.applyFromHere(true); len(got.Cells) != len(resp.Cells) {
		t.Fatal("from_here must keep silent cells")
	}
	raw, _ := json.Marshal(c)
	for _, want := range []string{`"silent":true`, `"expected":12`, `"expected_spots":0`, `"sources":[]`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("silent cell JSON %s lacks %s", raw, want)
		}
	}
}

// fakeFromHereSource serves fixed rows after an optional delay.
type fakeFromHereSource struct {
	calls atomic.Int32
	delay time.Duration
	err   error
	rows  []fromHereCountRow
	cov   map[fromHereDaySlot]bool
}

func (f *fakeFromHereSource) FromHereCounts(ctx context.Context, grids []string, slots []int, dayFrom, dayTo int64) ([]fromHereCountRow, map[fromHereDaySlot]bool, error) {
	f.calls.Add(1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return f.rows, f.cov, f.err
}

// withFromHereSource swaps the counts source and resets the cache.
func withFromHereSource(t *testing.T, src fromHereCountsSource) {
	t.Helper()
	saved := fromHereSource
	savedWait := fromHereWait
	fromHereSource = func() fromHereCountsSource { return src }
	fromHereCache.reset()
	t.Cleanup(func() {
		fromHereSource = saved
		fromHereWait = savedWait
		fromHereCache.reset()
	})
}

// steadyRows: every slot of every day in range has `count` spots for the cell.
func steadyRows(band, region string, count int64, from, to int64) []fromHereCountRow {
	var rows []fromHereCountRow
	for d := from; d <= to; d++ {
		for s := 0; s < 48; s++ {
			rows = append(rows, fromHereCountRow{Band: band, Region: region, Slot: s, Day: d, Count: count})
		}
	}
	return rows
}

func TestFromHereNormalsForCachesAndShares(t *testing.T) {
	now := time.Now().Unix()
	today := utcDayIndex(now)
	src := &fakeFromHereSource{
		rows: steadyRows("20m", "NA", 30, today-40, today),
		cov:  fullCoverage(today-40, today),
	}
	withFromHereSource(t, src)

	var wg sync.WaitGroup
	results := make([]*fromHereNormals, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = fromHereNormalsFor([]string{"JO32", "JO31"}, now, 15)
		}(i)
	}
	wg.Wait()
	for _, n := range results {
		if n == nil {
			t.Fatal("a fast fetch must return normals")
		}
		if got := n.ByCell[propIntelCellKey{band: "20m", region: "NA"}].Expected; math.Abs(got-15) > 1e-9 {
			t.Fatalf("30 per slot over a 15-minute window = 15, got %v", got)
		}
	}
	// Same area in another order, same slots and day: cached.
	if fromHereNormalsFor([]string{"JO31", "JO32"}, now, 15) == nil {
		t.Fatal("cached normals missing")
	}
	if c := src.calls.Load(); c != 1 {
		t.Fatalf("concurrent and repeated requests must share one fetch, got %d", c)
	}
}

func TestFromHereNormalsForSlowFetchFillsCacheInBackground(t *testing.T) {
	now := time.Now().Unix()
	today := utcDayIndex(now)
	src := &fakeFromHereSource{delay: 150 * time.Millisecond, rows: steadyRows("40m", "EU", 10, today-40, today), cov: fullCoverage(today-40, today)}
	withFromHereSource(t, src)
	fromHereWait = 20 * time.Millisecond

	if n := fromHereNormalsFor([]string{"JO32"}, now, 15); n != nil {
		t.Fatal("a fetch slower than the wait must not block the request")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if n := fromHereNormalsFor([]string{"JO32"}, now, 15); n != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the background fetch never filled the cache")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if c := src.calls.Load(); c != 1 {
		t.Fatalf("polling during a slow fetch must not start new ones, got %d", c)
	}
}

func TestFromHereNormalsForErrorsAreCachedBriefly(t *testing.T) {
	now := time.Now().Unix()
	src := &fakeFromHereSource{err: errors.New("db down")}
	withFromHereSource(t, src)
	if fromHereNormalsFor([]string{"JO32"}, now, 15) != nil || fromHereNormalsFor([]string{"JO32"}, now, 15) != nil {
		t.Fatal("a failed fetch has no normals")
	}
	if c := src.calls.Load(); c != 1 {
		t.Fatalf("a failure must not be retried on every request, got %d fetches", c)
	}
}

func TestFromHereNormalsForWithoutStoreOrArea(t *testing.T) {
	withFromHereSource(t, nil)
	fromHereSource = func() fromHereCountsSource { return nil }
	if fromHereNormalsFor([]string{"JO32"}, time.Now().Unix(), 15) != nil {
		t.Fatal("no store, no normals")
	}
	fromHereSource = func() fromHereCountsSource { return &fakeFromHereSource{} }
	if fromHereNormalsFor(nil, time.Now().Unix(), 15) != nil {
		t.Fatal("no area grids, no normals")
	}
}

// TestPropIntelV2HandlerFromHereNormals runs the from-here view end to end:
// global spots are excluded, the live cell carries its area normal and the
// silent cell appears with silent=1.
func TestPropIntelV2HandlerFromHereNormals(t *testing.T) {
	now := time.Now().Unix()
	today := utcDayIndex(now)
	rows := append(steadyRows("20m", "NA", 30, today-40, today), steadyRows("15m", "JA", 40, today-40, today)...)
	withFromHereSource(t, &fakeFromHereSource{rows: rows, cov: fullCoverage(today-40, today)})

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
	resp, err := http.Get(server.URL + "/api/prop_intel/v2?qth=JO32&surroundings=true&from_here=true&silent=1&sources=wspr,pskr")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var decoded propIntelV2Response
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	live := findV2Cell(t, decoded, "20m", "NA")
	if live.SpotCount != 7 || live.Expected == nil || *live.Expected != 15 || live.ExpectedSpots == nil || *live.ExpectedSpots != 6 {
		t.Fatalf("live from-here cell: %+v", live)
	}
	silent := findV2Cell(t, decoded, "15m", "JA")
	if !silent.Silent || *silent.Expected != 20 {
		t.Fatalf("silent cell: %+v", silent)
	}

	// Without from_here the global view is unchanged: mesh counts, no normal.
	resp2, err := http.Get(server.URL + "/api/prop_intel/v2?qth=JO32&surroundings=true&silent=1&sources=wspr,pskr")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var global propIntelV2Response
	if err := json.NewDecoder(resp2.Body).Decode(&global); err != nil {
		t.Fatal(err)
	}
	g := findV2Cell(t, global, "20m", "NA")
	if g.SpotCount != 57 || g.Expected != nil || g.Silent {
		t.Fatalf("global view must keep mesh counts and carry no normal: %+v", g)
	}
	for _, c := range global.Cells {
		if c.Silent {
			t.Fatalf("silent cells are a from-here feature: %+v", c)
		}
	}
}
