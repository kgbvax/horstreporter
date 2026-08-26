package main

import (
	"testing"
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