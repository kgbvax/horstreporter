package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

// makeWSPRSpot builds a WSPR MQTTMessage for testing. powerDbm is the TX
// power in dBm (wspr.live convention: 20W ≈ 43 dBm, 5W ≈ 37 dBm, 0.1W ≈ 20 dBm).
func makeWSPRSpot(ts int64, band, senderLoc, receiverLoc string, snr, powerDbm int) MQTTMessage {
	return MQTTMessage{
		RP:      snr,
		T:       ts,
		SC:      "RXCALL",
		SL:      receiverLoc,
		RC:      "TXCALL",
		RL:      senderLoc,
		B:       band,
		MD:      "WSPR",
		Source:  "wspr",
		TXPower: powerDbm,
	}
}

// findCell returns the cell for (band, region) or nil.
func findCell(t *testing.T, resp propIntelResponse, band, regionCode string) propIntelCell {
	t.Helper()
	for _, c := range resp.Cells {
		if c.Band == band && c.Region == regionCode {
			return c
		}
	}
	t.Fatalf("cell %s/%s not found", band, regionCode)
	return propIntelCell{}
}

// TestWsprNowcastSSBCWViability verifies AE1: a strong path (SNR +5, 20W=43dBm)
// → effective SNR = 5 + (50-43) = 12 dB → SSB=true (≥10), CW=true (≥-5).
func TestWsprNowcastSSBCWViability(t *testing.T) {
	e := &propIntelEngine{}
	now := int64(1700000000)
	spot := makeWSPRSpot(now, "20m", "JO62", "JO31", 5, 43) // 20W = 43 dBm
	history := []MQTTMessage{spot}

	resp := e.Evaluate("JO31", false, 15, -15, history, now, 2.0)
	cell := findCell(t, resp, "20m", "EU")
	if !cell.SSBOpen {
		t.Error("SSBOpen = false, want true for effective SNR 12 dB")
	}
	if !cell.CWOpen {
		t.Error("CWOpen = false, want true for effective SNR 12 dB")
	}
}

// TestWsprNowcastCWOnly verifies AE2: a weak path (SNR -8, 5W=37dBm) →
// effective SNR = -8 + (50-37) = 5 dB → SSB=false (<10), CW=true (≥-5).
func TestWsprNowcastCWOnly(t *testing.T) {
	e := &propIntelEngine{}
	now := int64(1700000000)
	spot := makeWSPRSpot(now, "30m", "JO50", "JO31", -8, 37) // 5W = 37 dBm, JO50=EU
	history := []MQTTMessage{spot}

	resp := e.Evaluate("JO31", false, 15, -15, history, now, 2.0)
	cell := findCell(t, resp, "30m", "EU")
	if cell.SSBOpen {
		t.Error("SSBOpen = true, want false for effective SNR 5 dB")
	}
	if !cell.CWOpen {
		t.Error("CWOpen = false, want true for effective SNR 5 dB")
	}
}

// TestWsprNowcastMissingPower verifies AE3: TXPower=0 → cell exists but
// SSB/CW flags are false.
func TestWsprNowcastMissingPower(t *testing.T) {
	e := &propIntelEngine{}
	now := int64(1700000000)
	spot := makeWSPRSpot(now, "40m", "JO31", "JO31", 2, 0) // 0 dBm → missing
	history := []MQTTMessage{spot}

	resp := e.Evaluate("JO31", false, 15, -15, history, now, 2.0)
	cell := findCell(t, resp, "40m", "EU")
	if cell.SSBOpen {
		t.Error("SSBOpen = true, want false when TXPower=0")
	}
	if cell.CWOpen {
		t.Error("CWOpen = true, want false when TXPower=0")
	}
	if cell.SpotCount != 1 {
		t.Errorf("SpotCount = %d, want 1", cell.SpotCount)
	}
}

// TestWsprNowcastRisingButTypical verifies AE7: rising slope but rate within
// 1 stddev of climatology mean → Rising=true, Atypical=nil.
func TestWsprNowcastRisingButTypical(t *testing.T) {
	e := &propIntelEngine{}
	now := int64(1700000000)
	// 2 spots in the first 7.5 min, 5 in the second → ratio 2.5 ≥ 1.5 → rising.
	history := []MQTTMessage{}
	for i := 0; i < 2; i++ {
		history = append(history, makeWSPRSpot(now-int64(12*60)+int64(i*60), "20m", "JO62", "JO31", 5, 43))
	}
	for i := 0; i < 5; i++ {
		history = append(history, makeWSPRSpot(now-int64(3*60)+int64(i*60), "20m", "JO62", "JO31", 5, 43))
	}

	resp := e.Evaluate("JO31", false, 15, -15, history, now, 2.0)
	cell := findCell(t, resp, "20m", "EU")
	if !cell.Rising {
		t.Error("Rising = false, want true (second-half/first-half ratio ≥ 1.5)")
	}
	// No climatology available in this test → Atypical should be nil.
	if cell.Atypical != nil {
		t.Errorf("Atypical = %v, want nil (no climatology)", cell.Atypical)
	}
}

// TestWsprNowcastFromHere verifies AE9: a cell where the operator's QTH is one
// end → FromHere=true.
func TestWsprNowcastFromHere(t *testing.T) {
	e := &propIntelEngine{}
	now := int64(1700000000)
	// Spot from operator (JO31) to US station (FN31) — operator is receiver.
	spot := makeWSPRSpot(now, "40m", "FN31", "JO31", 5, 43) // 20W, operator is receiver
	history := []MQTTMessage{spot}

	resp := e.Evaluate("JO31", false, 15, -15, history, now, 2.0)
	cell := findCell(t, resp, "40m", "NA")
	if !cell.FromHere {
		t.Error("FromHere = false, want true (operator QTH JO31 is one end)")
	}
}

// TestWsprNowcastFromHereNotGlobal verifies AE9: a cell with no operator-end
// path → FromHere=false.
func TestWsprNowcastFromHereNotGlobal(t *testing.T) {
	e := &propIntelEngine{}
	now := int64(1700000000)
	// Spot from JA to EU — operator QTH is in NA, neither end matches.
	spot := makeWSPRSpot(now, "20m", "PM95", "JO31", 5, 43) // JA→EU, 20W
	history := []MQTTMessage{spot}

	resp := e.Evaluate("FN31", false, 15, -15, history, now, 2.0)
	// The cell should be 20m/EU (receiver region) with FromHere=false.
	cell := findCell(t, resp, "20m", "EU")
	if cell.FromHere {
		t.Error("FromHere = true, want false (operator QTH FN31 is not an end)")
	}
}

// TestWsprNowcastEdgeNoClimatology verifies that a cell with no climatology
// entries has Atypical=nil (insufficient data).
func TestWsprNowcastEdgeNoClimatology(t *testing.T) {
	e := &propIntelEngine{}
	now := int64(1700000000)
	spot := makeWSPRSpot(now, "15m", "JO62", "JO31", 5, 43)
	history := []MQTTMessage{spot}

	resp := e.Evaluate("JO31", false, 15, -15, history, now, 2.0)
	cell := findCell(t, resp, "15m", "EU")
	if cell.Atypical != nil {
		t.Error("Atypical should be nil when climatology has no entries")
	}
}

// TestWsprNowcastAtypicalWithClimatology verifies the atypical z-score
// fires when the live rate exceeds the climatology mean by >= threshold.
// Seeds wsprClimatology with per-day counts, then sends a burst of WSPR
// spots that exceed the typical rate.
func TestWsprNowcastAtypicalWithClimatology(t *testing.T) {
	// Seed the WSPR climatology global with 10 days of data for 20m/EU
	// at the slot corresponding to our test timestamp.
	now := int64(1700000000)
	slot := utcSlotOfDay(now)
	today := utcDayIndex(now)
	savedClim := wsprClimatology
	defer func() { wsprClimatology = savedClim }()
	wsprClimatology = newWsprClimatologyEngine("")
	dayCounts := make(map[int64]int64)
	for d := today - 10; d < today; d++ {
		dayCounts[d] = int64(3 + (d % 4)) // varying counts: 3-6 per day
	}
	wsprClimatology.buckets[wsprClimatologyKey("20m", slot, "EU")] = &wsprClimatologyBucket{
		Band: "20m", SlotOfDay: slot, Region: "EU", Count: 50, DayCounts: dayCounts,
	}

	// Send 20 WSPR spots in 15 min — extrapolated to 40/slot, well above
	// the mean of 5 with nonzero stddev.
	e := &propIntelEngine{}
	var history []MQTTMessage
	for i := 0; i < 20; i++ {
		history = append(history, makeWSPRSpot(now-int64(i*30), "20m", "JO62", "JO31", 5, 43))
	}

	resp := e.Evaluate("JO31", false, 15, -15, history, now, 2.0)
	cell := findCell(t, resp, "20m", "EU")
	if cell.Atypical == nil {
		t.Fatal("Atypical = nil, want non-nil (live rate exceeds climatology mean)")
	}
	if cell.Atypical.ZScore < 2.0 {
		t.Errorf("ZScore = %f, want >= 2.0", cell.Atypical.ZScore)
	}
	if cell.Atypical.Confidence <= 0 {
		t.Errorf("Confidence = %f, want > 0", cell.Atypical.Confidence)
	}
}

// TestAtypicalConfidence verifies the confidence curve scales from 0.3 at 1
// day to 1.0 at propIntelMatureSampleDays.
func TestAtypicalConfidence(t *testing.T) {
	if c := atypicalConfidence(0); c != 0.3 {
		t.Errorf("atypicalConfidence(0) = %f, want 0.3", c)
	}
	if c := atypicalConfidence(1); c <= 0.3 {
		t.Errorf("atypicalConfidence(1) = %f, want > 0.3", c)
	}
	if c := atypicalConfidence(propIntelMatureSampleDays); c != 1.0 {
		t.Errorf("atypicalConfidence(%d) = %f, want 1.0", propIntelMatureSampleDays, c)
	}
	// AE8: 5 days → roughly 0.42.
	c5 := atypicalConfidence(5)
	if c5 < 0.4 || c5 > 0.45 {
		t.Errorf("atypicalConfidence(5) = %f, want roughly 0.42", c5)
	}
}

// TestAssignFlavorUnavailable verifies AE6/R9: FT8 climatology unavailable →
// "atypical-wspr-only" with ft8Ref "unavailable".
func TestAssignFlavorUnavailable(t *testing.T) {
	flavor, ref := assignFlavor("17m", "OC", 10, nil)
	if flavor != "atypical-wspr-only" {
		t.Errorf("flavor = %q, want atypical-wspr-only", flavor)
	}
	if ref != "unavailable" {
		t.Errorf("ft8Ref = %q, want unavailable", ref)
	}
}

// TestAssignFlavorBoth verifies AE4: FT8 also atypical → "atypical-both".
func TestAssignFlavorBoth(t *testing.T) {
	ft8Clim := map[regionBaselineKey]regionCalendarStatRow{
		{band: "15m", region: "SA", slot: 10}: {
			Mean: 5, StdDev: 2, Today: 20, SampleDays: 30,
		},
	}
	flavor, ref := assignFlavor("15m", "SA", 10, ft8Clim)
	if flavor != "atypical-both" {
		t.Errorf("flavor = %q, want atypical-both", flavor)
	}
	if ref != "available" {
		t.Errorf("ft8Ref = %q, want available", ref)
	}
}

// TestAssignFlavorSilentFT8 verifies AE5: FT8 absent → "atypical-wspr-silent-ft8".
func TestAssignFlavorSilentFT8(t *testing.T) {
	ft8Clim := map[regionBaselineKey]regionCalendarStatRow{
		{band: "10m", region: "AF", slot: 10}: {
			Mean: 10, StdDev: 3, Today: 0, SampleDays: 30,
		},
	}
	flavor, ref := assignFlavor("10m", "AF", 10, ft8Clim)
	if flavor != "atypical-wspr-silent-ft8" {
		t.Errorf("flavor = %q, want atypical-wspr-silent-ft8", flavor)
	}
	if ref != "available" {
		t.Errorf("ft8Ref = %q, want available", ref)
	}
}

// TestPropIntelResponseRegionNamesAndBandOrder verifies the payload fields
// the mobile app fills its matrix canvas from: region_names covers all 11
// region codes, band_order is the full canonical list, and bands (with-data
// only) is a subset of it.
func TestPropIntelResponseRegionNamesAndBandOrder(t *testing.T) {
	e := &propIntelEngine{}
	now := int64(1700000000)
	history := []MQTTMessage{makeWSPRSpot(now, "20m", "JO62", "JO31", 5, 43)}

	resp := e.Evaluate("JO31", false, 15, -15, history, now, 2.0)

	if len(resp.RegionNames) != len(resp.Regions) {
		t.Errorf("RegionNames has %d entries, want %d (one per region)", len(resp.RegionNames), len(resp.Regions))
	}
	for _, code := range resp.Regions {
		if name, ok := resp.RegionNames[code]; !ok || name == "" {
			t.Errorf("RegionNames[%q] = %q, ok=%v; want a non-empty display name", code, name, ok)
		}
	}
	if len(resp.BandOrder) != len(propIntelBandOrder) {
		t.Fatalf("BandOrder has %d entries, want %d", len(resp.BandOrder), len(propIntelBandOrder))
	}
	for i, b := range resp.BandOrder {
		if b != propIntelBandOrder[i] {
			t.Errorf("BandOrder[%d] = %q, want %q", i, b, propIntelBandOrder[i])
		}
	}
	orderSet := make(map[string]bool, len(resp.BandOrder))
	for _, b := range resp.BandOrder {
		orderSet[b] = true
	}
	for _, b := range resp.Bands {
		if !orderSet[b] {
			t.Errorf("Bands contains %q, which is not in BandOrder", b)
		}
	}
	if len(resp.Bands) != 1 || resp.Bands[0] != "20m" {
		t.Errorf("Bands = %v, want [20m] (bands with live data only)", resp.Bands)
	}
}

// TestPropIntelSummarizeQuiet verifies the empty-window summary: quiet kind
// with the explicit no-paths headline, empty grid and band list.
func TestPropIntelSummarizeQuiet(t *testing.T) {
	resp := propIntelResponse{Bands: []string{}, Cells: []propIntelCell{}}
	sum := propIntelSummarize(resp)
	if sum.HeadlineKind != "quiet" {
		t.Errorf("HeadlineKind = %q, want quiet", sum.HeadlineKind)
	}
	if sum.Headline != "No WSPR paths in the window" {
		t.Errorf("Headline = %q, want the no-paths headline", sum.Headline)
	}
	if len(sum.Grid) != 0 || len(sum.TopBands) != 0 {
		t.Errorf("Grid/TopBands = %d/%d entries, want 0/0", len(sum.Grid), len(sum.TopBands))
	}
}

// TestPropIntelSummarizeActiveQuiet verifies the busy-but-boring case: cells
// exist, nothing rising or atypical → quiet kind with the band count.
func TestPropIntelSummarizeActiveQuiet(t *testing.T) {
	resp := propIntelResponse{
		Bands: []string{"20m", "40m"},
		Cells: []propIntelCell{
			{Band: "20m", Region: "EU", SpotCount: 5},
			{Band: "40m", Region: "NA", SpotCount: 3},
		},
	}
	sum := propIntelSummarize(resp)
	if sum.HeadlineKind != "quiet" {
		t.Errorf("HeadlineKind = %q, want quiet", sum.HeadlineKind)
	}
	if sum.Headline != "2 bands active, none rising" {
		t.Errorf("Headline = %q, want the band count headline", sum.Headline)
	}
}

// TestPropIntelSummarizeRisingHeadline verifies the rising headline prefers
// open paths (a rising open cell beats a rising-but-closed busier cell).
func TestPropIntelSummarizeRisingHeadline(t *testing.T) {
	resp := propIntelResponse{
		Bands: []string{"20m", "40m"},
		Cells: []propIntelCell{
			{Band: "20m", Region: "EU", SSBOpen: true, Rising: true, SpotCount: 5},
			{Band: "40m", Region: "NA", Rising: true, SpotCount: 50},
		},
	}
	sum := propIntelSummarize(resp)
	if sum.HeadlineKind != "rising" {
		t.Errorf("HeadlineKind = %q, want rising", sum.HeadlineKind)
	}
	if sum.Headline != "20m rising toward Europe" {
		t.Errorf("Headline = %q, want the open rising cell's headline", sum.Headline)
	}
}

// TestPropIntelSummarizeAtypicalWins verifies headline precedence: atypical
// beats rising, and among atypical cells the higher confidence wins even
// with a lower z-score.
func TestPropIntelSummarizeAtypicalWins(t *testing.T) {
	resp := propIntelResponse{
		Bands: []string{"10m", "20m"},
		Cells: []propIntelCell{
			{Band: "20m", Region: "EU", SSBOpen: true, Rising: true, SpotCount: 5},
			{Band: "10m", Region: "SA", Rising: true, SpotCount: 8,
				Atypical: &AtypicalInfo{ZScore: 3.2, Confidence: 0.8, Flavor: "atypical-both"}},
			{Band: "15m", Region: "AF", SpotCount: 2,
				Atypical: &AtypicalInfo{ZScore: 9.9, Confidence: 0.5, Flavor: "atypical-wspr-only"}},
		},
	}
	sum := propIntelSummarize(resp)
	if sum.HeadlineKind != "atypical" {
		t.Errorf("HeadlineKind = %q, want atypical", sum.HeadlineKind)
	}
	if sum.Headline != "10m atypical surge to South America (z=3.2)" {
		t.Errorf("Headline = %q, want the higher-confidence atypical cell", sum.Headline)
	}
}

// TestPropIntelSummarizeGridAndTopBands verifies the compact grid (intensity
// relative to the busiest cell, flag bitmask) and the top-bands aggregation
// (busiest band first, regions busiest-first, mode/rising rollup, max 4).
func TestPropIntelSummarizeGridAndTopBands(t *testing.T) {
	resp := propIntelResponse{
		Bands: []string{"20m", "40m"},
		Cells: []propIntelCell{
			{Band: "20m", Region: "NA", SSBOpen: true, Rising: true, SpotCount: 10},
			{Band: "20m", Region: "EU", CWOpen: true, SpotCount: 5},
			{Band: "40m", Region: "NA", FromHere: true, SpotCount: 3},
		},
	}
	sum := propIntelSummarize(resp)

	if len(sum.Grid) != 3 {
		t.Fatalf("Grid has %d entries, want 3", len(sum.Grid))
	}
	wantGrid := map[string]propIntelGridCell{
		"20m/NA": {Band: "20m", Region: "NA", Intensity: 1.0, Flags: propIntelFlagSSB | propIntelFlagRising},
		"20m/EU": {Band: "20m", Region: "EU", Intensity: 0.5, Flags: propIntelFlagCW},
		"40m/NA": {Band: "40m", Region: "NA", Intensity: 0.3, Flags: propIntelFlagFromHere},
	}
	for _, gc := range sum.Grid {
		want, ok := wantGrid[gc.Band+"/"+gc.Region]
		if !ok {
			t.Errorf("unexpected grid cell %s/%s", gc.Band, gc.Region)
			continue
		}
		if gc.Intensity != want.Intensity {
			t.Errorf("grid %s/%s intensity = %v, want %v", gc.Band, gc.Region, gc.Intensity, want.Intensity)
		}
		if gc.Flags != want.Flags {
			t.Errorf("grid %s/%s flags = %#x, want %#x", gc.Band, gc.Region, gc.Flags, want.Flags)
		}
	}

	if len(sum.TopBands) != 2 {
		t.Fatalf("TopBands has %d entries, want 2", len(sum.TopBands))
	}
	first := sum.TopBands[0]
	if first.Band != "20m" || first.Spots != 15 {
		t.Errorf("TopBands[0] = %+v, want 20m with 15 spots (busiest first)", first)
	}
	if len(first.Regions) != 2 || first.Regions[0] != "NA" || first.Regions[1] != "EU" {
		t.Errorf("20m regions = %v, want [NA EU] busiest-first", first.Regions)
	}
	if !first.SSB || !first.CW || !first.Rising {
		t.Errorf("20m rollup ssb/cw/rising = %v/%v/%v, want all true", first.SSB, first.CW, first.Rising)
	}
	if sum.TopBands[1].Band != "40m" {
		t.Errorf("TopBands[1].Band = %q, want 40m", sum.TopBands[1].Band)
	}
}

// setHubHistory replaces the rolling history for handler tests.
func setHubHistory(t *testing.T, history []MQTTMessage) {
	t.Helper()
	hub.Lock()
	hub.history = history
	hub.Unlock()
	t.Cleanup(func() {
		hub.Lock()
		hub.history = nil
		hub.Unlock()
	})
}

// resetPropIntelSummaryCache clears the summary handler's single-entry cache
// so handler tests don't bleed into each other.
func resetPropIntelSummaryCache() {
	propIntelSummaryCache.mu.Lock()
	propIntelSummaryCache.key = ""
	propIntelSummaryCache.body = nil
	propIntelSummaryCache.at = time.Time{}
	propIntelSummaryCache.mu.Unlock()
}

// TestPropIntelSummaryHandler verifies the summary endpoint end to end:
// seeded history reduces to the widget payload, the qth-less request 400s,
// and the response carries the 60s cache header.
func TestPropIntelSummaryHandler(t *testing.T) {
	resetPropIntelSummaryCache()
	now := time.Now().Unix()
	setHubHistory(t, []MQTTMessage{
		makeWSPRSpot(now-60, "20m", "JO62", "JO31", 5, 43),
		makeWSPRSpot(now-30, "20m", "JO62", "JO31", 5, 43),
	})

	req := httptest.NewRequest("GET", "/api/prop_intel/summary?qth=JO31", nil)
	rr := httptest.NewRecorder()
	propIntelSummaryHandler(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Cache-Control"); got != "max-age=60" {
		t.Errorf("Cache-Control = %q, want max-age=60", got)
	}
	var sum propIntelSummaryResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &sum); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if sum.QTH != "JO31" {
		t.Errorf("QTH = %q, want JO31", sum.QTH)
	}
	if len(sum.Grid) != 1 || sum.Grid[0].Band != "20m" || sum.Grid[0].Region != "EU" {
		t.Errorf("Grid = %+v, want one 20m/EU cell", sum.Grid)
	}
	if len(sum.TopBands) != 1 || sum.TopBands[0].Band != "20m" {
		t.Errorf("TopBands = %+v, want one 20m entry", sum.TopBands)
	}

	req400 := httptest.NewRequest("GET", "/api/prop_intel/summary", nil)
	rr400 := httptest.NewRecorder()
	propIntelSummaryHandler(rr400, req400)
	if rr400.Code != 400 {
		t.Errorf("status without qth = %d, want 400", rr400.Code)
	}
}

// TestPropIntelSummaryHandlerCache verifies the single-entry cache: within
// the TTL a repeat query serves the cached bytes even when history changed,
// and a different query string recomputes.
func TestPropIntelSummaryHandlerCache(t *testing.T) {
	resetPropIntelSummaryCache()
	now := time.Now().Unix()
	setHubHistory(t, []MQTTMessage{makeWSPRSpot(now-60, "20m", "JO62", "JO31", 5, 43)})

	first := httptest.NewRecorder()
	propIntelSummaryHandler(first, httptest.NewRequest("GET", "/api/prop_intel/summary?qth=JO31", nil))
	if first.Code != 200 {
		t.Fatalf("first call status = %d", first.Code)
	}

	// Change the history between calls: a cache hit must not reflect it.
	setHubHistoryWithoutReset := []MQTTMessage{makeWSPRSpot(now-60, "40m", "JO62", "JO31", 5, 43)}
	hub.Lock()
	hub.history = setHubHistoryWithoutReset
	hub.Unlock()

	second := httptest.NewRecorder()
	propIntelSummaryHandler(second, httptest.NewRequest("GET", "/api/prop_intel/summary?qth=JO31", nil))
	if second.Code != 200 {
		t.Fatalf("second call status = %d", second.Code)
	}
	if second.Body.String() != first.Body.String() {
		t.Errorf("cached body changed:\nfirst: %s\nsecond: %s", first.Body.String(), second.Body.String())
	}

	// A different query key recomputes and reflects the new history.
	third := httptest.NewRecorder()
	propIntelSummaryHandler(third, httptest.NewRequest("GET", "/api/prop_intel/summary?qth=JO31&from_here=true", nil))
	if third.Code != 200 {
		t.Fatalf("third call status = %d", third.Code)
	}
	var sum propIntelSummaryResponse
	if err := json.Unmarshal(third.Body.Bytes(), &sum); err != nil {
		t.Fatalf("decode third response: %v", err)
	}
	if len(sum.Grid) != 1 || sum.Grid[0].Band != "40m" {
		t.Errorf("recomputed grid = %+v, want one 40m cell (new history)", sum.Grid)
	}
}
