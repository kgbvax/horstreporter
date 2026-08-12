package main

import (
	"math"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestPropIntelSparseWSPR verifies the sparse-WSPRnet case (AE1 in the plan):
// a cell with only 2 WSPR spots, no RBN/PSKREPORTER, has P(open) > 0 and
// confidence < 0.3, with sources = ["wspr"].
func TestPropIntelSparseWSPR(t *testing.T) {
	e := &propIntelEngine{}
	now := int64(1700000000)
	history := []MQTTMessage{
		{RP: -20, T: now - 60, SC: "G0A", SL: "JO30", RC: "DL1A", RL: "JO62", B: "20m", MD: "WSPR", Source: "wspr"},
		{RP: -22, T: now - 30, SC: "G0B", SL: "JO31", RC: "DL1B", RL: "JO62", B: "20m", MD: "WSPR", Source: "wspr"},
	}
	resp := e.Evaluate("JO62", false, 15, -15, history, now)
	cell := findCell(t, resp, "20m", "EU")
	if cell == nil {
		t.Fatalf("expected 20m/EU cell")
	}
	if cell.POpen <= 0 {
		t.Errorf("P(open) = %v, want > 0", cell.POpen)
	}
	if cell.Confidence >= 0.3 {
		t.Errorf("confidence = %v, want < 0.3 for 2-sender single-source cell", cell.Confidence)
	}
	if got := sourcesString(cell); got != "wspr" {
		t.Errorf("sources = %q, want \"wspr\"", got)
	}
}

// TestPropIntelDenseRBN verifies the dense-RBN case: 30 RBN spots, no other
// sources, has high P(open) and moderate confidence (single-source discount),
// with sources = ["rbn"].
func TestPropIntelDenseRBN(t *testing.T) {
	e := &propIntelEngine{}
	now := int64(1700000000)
	history := make([]MQTTMessage, 0, 30)
	for i := 0; i < 30; i++ {
		call := "W1" + string(rune('A'+i/26)) + string(rune('A'+i%26))
		history = append(history, MQTTMessage{
			RP: -10, T: now - int64(i)*30, SC: call, SL: "JO40", RC: "DL1A", RL: "JO62", B: "20m", MD: "CW", Source: "rbn",
		})
	}
	resp := e.Evaluate("JO62", false, 15, -15, history, now)
	cell := findCell(t, resp, "20m", "EU")
	if cell == nil {
		t.Fatalf("expected 20m/EU cell")
	}
	if cell.POpen < 0.9 {
		t.Errorf("P(open) = %v, want high (≥0.9) for 30-spot cell", cell.POpen)
	}
	// Single-source discount: should be moderate, not near 1.
	if cell.Confidence > 0.7 {
		t.Errorf("confidence = %v, want moderate (≤0.7) for single-source cell", cell.Confidence)
	}
	if cell.Confidence < 0.4 {
		t.Errorf("confidence = %v, want at least moderate (≥0.4) for dense single-source cell", cell.Confidence)
	}
	if got := sourcesString(cell); got != "rbn" {
		t.Errorf("sources = %q, want \"rbn\"", got)
	}
}

// TestPropIntelMultiSource verifies the multi-source case: 10 RBN + 15
// PSKREPORTER + 2 WSPR, with 5 senders appearing in both RBN and PSKREPORTER,
// yields unique senders = 22 and sources = ["rbn","pskreporter","wspr"] with
// high confidence.
func TestPropIntelMultiSource(t *testing.T) {
	e := &propIntelEngine{}
	now := int64(1700000000)
	history := make([]MQTTMessage, 0, 27)
	// 5 senders appear in both RBN and PSKReporter (counted once each).
	for i := 0; i < 5; i++ {
		call := "DUAL" + string(rune('A'+i))
		history = append(history, MQTTMessage{RP: -10, T: now - 60, SC: call, SL: "JO40", RC: "DL1A", RL: "JO62", B: "20m", MD: "CW", Source: "rbn"})
		history = append(history, MQTTMessage{RP: -8, T: now - 50, SC: call, SL: "JO40", RC: "DL1A", RL: "JO62", B: "20m", MD: "FT8", Source: "mqtt"})
	}
	// 5 RBN-only senders.
	for i := 0; i < 5; i++ {
		call := "RBN" + string(rune('A'+i))
		history = append(history, MQTTMessage{RP: -10, T: now - 40, SC: call, SL: "JO41", RC: "DL1A", RL: "JO62", B: "20m", MD: "CW", Source: "rbn"})
	}
	// 10 PSKReporter-only senders.
	for i := 0; i < 10; i++ {
		call := "PSK" + string(rune('A'+i))
		history = append(history, MQTTMessage{RP: -8, T: now - 30, SC: call, SL: "JO42", RC: "DL1A", RL: "JO62", B: "20m", MD: "FT8", Source: "mqtt"})
	}
	// 2 WSPR senders.
	for i := 0; i < 2; i++ {
		call := "WSP" + string(rune('A'+i))
		history = append(history, MQTTMessage{RP: -20, T: now - 20, SC: call, SL: "JO43", RC: "DL1A", RL: "JO62", B: "20m", MD: "WSPR", Source: "wspr"})
	}

	resp := e.Evaluate("JO62", false, 15, -15, history, now)
	cell := findCell(t, resp, "20m", "EU")
	if cell == nil {
		t.Fatalf("expected 20m/EU cell")
	}
	// 5 dual + 5 rbn-only + 10 psk-only + 2 wspr = 22 unique senders.
	if got := sourcesString(cell); got != "pskreporter,rbn,wspr" {
		t.Errorf("sources = %q, want \"pskreporter,rbn,wspr\"", got)
	}
	if cell.Confidence < 0.7 {
		t.Errorf("confidence = %v, want high (≥0.7) for 22-sender 3-source cell", cell.Confidence)
	}
}

// TestPropIntelForecastSlope verifies that a band with a rising sparkline
// (last 4 bins > first 4 bins) produces forecast P(open) > nowcast P(open).
func TestPropIntelForecastSlope(t *testing.T) {
	dir := t.TempDir()
	baseline := newDxBaselineEngine(filepath.Join(dir, "dx_baseline.json"))
	if err := baseline.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	e := &propIntelEngine{baseline: baseline}
	now := int64(1700000000)
	// Build a history where spots are concentrated in the last quarter of a
	// 60-minute window so the sparkline rises. 60 min window → 12 bins of 5 min.
	// Put 2 spots in the first 20 min (bins 0-3) and 20 spots in the last 20 min (bins 8-11).
	// Observe each spot so the event ring (used by buildBandActivityByBin) is
	// populated — Evaluate's sparkline comes from the event ring, not the
	// history parameter.
	history := make([]MQTTMessage, 0, 22)
	for i := 0; i < 2; i++ {
		m := MQTTMessage{RP: -8, T: now - 50*60 + int64(i)*60, SC: "A", SL: "JO40", RC: "DL1A", RL: "JO62", B: "20m", MD: "FT8"}
		history = append(history, m)
		baseline.Observe(m)
	}
	for i := 0; i < 20; i++ {
		m := MQTTMessage{RP: -8, T: now - 10*60 + int64(i)*30, SC: "B", SL: "JO41", RC: "DL1A", RL: "JO62", B: "20m", MD: "FT8"}
		history = append(history, m)
		baseline.Observe(m)
	}
	resp := e.Evaluate("JO62", false, 60, -15, history, now)
	cell := findCell(t, resp, "20m", "EU")
	if cell == nil {
		t.Fatalf("expected 20m/EU cell")
	}
	if cell.Forecast.POpen <= cell.Nowcast.POpen {
		t.Errorf("forecast P(open) = %v, nowcast = %v; want forecast > nowcast for rising sparkline",
			cell.Forecast.POpen, cell.Nowcast.POpen)
	}
}

// TestPropIntelForecastVolatility verifies that a band with high sparkline
// variance produces forecast confidence < nowcast confidence.
func TestPropIntelForecastVolatility(t *testing.T) {
	dir := t.TempDir()
	baseline := newDxBaselineEngine(filepath.Join(dir, "dx_baseline.json"))
	if err := baseline.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	e := &propIntelEngine{baseline: baseline}
	now := int64(1700000000)
	// Alternating bursts and gaps across a 60-min window → high bin-to-bin
	// variance in the sparkline. Observe each spot to populate the event ring.
	history := make([]MQTTMessage, 0, 40)
	for bin := 0; bin < 12; bin++ {
		if bin%2 == 0 {
			// Burst bin: 4 spots.
			for i := 0; i < 4; i++ {
				m := MQTTMessage{RP: -8, T: now - int64((11-bin)*5*60) + int64(i)*30, SC: "X", SL: "JO40", RC: "DL1A", RL: "JO62", B: "20m", MD: "FT8"}
				history = append(history, m)
				baseline.Observe(m)
			}
		}
	}
	resp := e.Evaluate("JO62", false, 60, -15, history, now)
	cell := findCell(t, resp, "20m", "EU")
	if cell == nil {
		t.Fatalf("expected 20m/EU cell")
	}
	if cell.Forecast.Confidence >= cell.Nowcast.Confidence {
		t.Errorf("forecast confidence = %v, nowcast = %v; want forecast < nowcast for volatile sparkline",
			cell.Forecast.Confidence, cell.Nowcast.Confidence)
	}
}

// TestPropIntelEmptyHistory verifies that with no spots in the window, the
// response has no cells (all cells absent).
func TestPropIntelEmptyHistory(t *testing.T) {
	e := &propIntelEngine{}
	now := int64(1700000000)
	resp := e.Evaluate("JO62", false, 15, -15, nil, now)
	if len(resp.Cells) != 0 {
		t.Errorf("expected 0 cells for empty history, got %d", len(resp.Cells))
	}
	if len(resp.Regions) != 11 {
		t.Errorf("expected 11 regions in response, got %d", len(resp.Regions))
	}
}

// TestPropIntelQTHResolution verifies that a callsign QTH resolves via the
// existing matchCall logic and a locator QTH is used directly.
func TestPropIntelQTHResolution(t *testing.T) {
	e := &propIntelEngine{}
	now := int64(1700000000)
	history := []MQTTMessage{
		{RP: -8, T: now - 30, SC: "DL1ABC", SL: "JO62", RC: "W1AW", RL: "FN31", B: "20m", MD: "FT8"},
	}
	// Callsign QTH: W1AW is the receiver, so the remote is the sender (DL1ABC, JO62 → EU).
	resp := e.Evaluate("W1AW", false, 15, -15, history, now)
	cell := findCell(t, resp, "20m", "EU")
	if cell == nil {
		t.Fatalf("expected 20m/EU cell for callsign QTH W1AW (remote DL1ABC in JO62)")
	}
	// Locator QTH: FN31 is the receiver end, remote is JO62 → EU.
	resp2 := e.Evaluate("FN31", false, 15, -15, history, now)
	cell2 := findCell(t, resp2, "20m", "EU")
	if cell2 == nil {
		t.Fatalf("expected 20m/EU cell for locator QTH FN31")
	}
}

// TestPropIntelAllRegionsPresent verifies that the regions list always
// contains all 11 regions, even when no spots are present.
func TestPropIntelAllRegionsPresent(t *testing.T) {
	e := &propIntelEngine{}
	now := int64(1700000000)
	resp := e.Evaluate("JO62", false, 15, -15, nil, now)
	want := map[string]bool{
		"EU": true, "NA": true, "SA": true, "AF": true, "AS": true, "OC": true,
		"AN": true, "JA": true, "VK": true, "KH6": true, "CAR": true,
	}
	if len(resp.Regions) != len(want) {
		t.Fatalf("expected %d regions, got %d", len(want), len(resp.Regions))
	}
	for _, r := range resp.Regions {
		if !want[r] {
			t.Errorf("unexpected region %q", r)
		}
	}
}

// TestPropIntelConfidenceModel is a focused table test for the
// cellConfidence function covering the plan's confidence scenarios.
func TestPropIntelConfidenceModel(t *testing.T) {
	cases := []struct {
		name        string
		senders     int
		sources     int
		minConf     float64
		maxConf     float64
		description string
	}{
		{"sparse wspr 2 senders 1 source", 2, 1, 0, 0.3, "low"},
		{"dense rbn 30 senders 1 source", 30, 1, 0.4, 0.7, "moderate (single-source discount)"},
		{"multi 22 senders 3 sources", 22, 3, 0.7, 1.0, "high"},
		{"multi 10 senders 2 sources", 10, 2, 0.6, 1.0, "high"},
		{"empty", 0, 0, 0, 0, "zero"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cellConfidence(tc.senders, tc.sources)
			if got < tc.minConf || got > tc.maxConf {
				t.Errorf("cellConfidence(%d, %d) = %v, want in [%v, %v] (%s)",
					tc.senders, tc.sources, got, tc.minConf, tc.maxConf, tc.description)
			}
		})
	}
}

// TestPropIntelPoissonPOpen verifies the Poisson P(open) formula.
func TestPropIntelPoissonPOpen(t *testing.T) {
	cases := []struct {
		rate float64
		slot float64
		want float64
		eps  float64
	}{
		{0, 15, 0, 0.001},              // zero rate → P=0
		{1, 15, 1 - math.Exp(-0.25), 0.001}, // λ=0.25 → 1-e^-0.25
		{4, 15, 1 - math.Exp(-1), 0.001},   // λ=1 → 1-e^-1 ≈ 0.632
		{10, 15, 1 - math.Exp(-2.5), 0.001}, // λ=2.5 → ≈0.918
	}
	for _, tc := range cases {
		got := poissonPOpen(tc.rate, tc.slot)
		if absFloat(got-tc.want) > tc.eps {
			t.Errorf("poissonPOpen(%v, %v) = %v, want %v ±%v", tc.rate, tc.slot, got, tc.want, tc.eps)
		}
	}
}

// TestPropIntelSparklineSlope verifies the slope computation.
func TestPropIntelSparklineSlope(t *testing.T) {
	// Rising: last 4 bins all 80, first 4 all 20 → delta 60 over 60 min.
	rising := []float64{20, 20, 20, 20, 50, 50, 60, 70, 80, 80, 80, 80}
	slope := sparklineSlopePerHour(rising, 60)
	if slope <= 0 {
		t.Errorf("sparklineSlopePerHour(rising) = %v, want > 0", slope)
	}
	// Falling: first 4 high, last 4 low.
	falling := []float64{80, 80, 80, 80, 50, 50, 40, 30, 20, 20, 20, 20}
	slope = sparklineSlopePerHour(falling, 60)
	if slope >= 0 {
		t.Errorf("sparklineSlopePerHour(falling) = %v, want < 0", slope)
	}
	// Flat: all equal.
	flat := []float64{50, 50, 50, 50, 50, 50, 50, 50, 50, 50, 50, 50}
	slope = sparklineSlopePerHour(flat, 60)
	if slope != 0 {
		t.Errorf("sparklineSlopePerHour(flat) = %v, want 0", slope)
	}
	// Too short: nil/short sparkline → 0.
	if s := sparklineSlopePerHour([]float64{1, 2, 3}, 60); s != 0 {
		t.Errorf("sparklineSlopePerHour(short) = %v, want 0", s)
	}
}

// TestPropIntelCanonicalSource verifies source mapping.
func TestPropIntelCanonicalSource(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		{"rbn", "rbn"},
		{"RBN", "rbn"},
		{"wspr", "wspr"},
		{"dxcluster", "pskreporter"},
		{"mqtt", "pskreporter"},
		{"", "pskreporter"},
	}
	for _, tc := range cases {
		m := MQTTMessage{Source: tc.src}
		if got := canonicalSource(m); got != tc.want {
			t.Errorf("canonicalSource(%q) = %q, want %q", tc.src, got, tc.want)
		}
	}
}

// TestPropIntelResolveRemoteEnd verifies remote-end resolution for sender and
// receiver roles.
func TestPropIntelResolveRemoteEnd(t *testing.T) {
	// QTH is the receiver (DL1A in JO62): remote is the sender (W1AW, FN31 → NA).
	m := MQTTMessage{SC: "W1AW", SL: "FN31", RC: "DL1A", RL: "JO62"}
	loc, call, ok := resolveRemoteEnd(m, []string{"DL1A"})
	if !ok {
		t.Fatalf("expected match")
	}
	if loc != "FN31" {
		t.Errorf("remote locator = %q, want FN31", loc)
	}
	if call != "W1AW" {
		t.Errorf("remote call = %q, want W1AW", call)
	}
	// QTH is the sender (W1AW in FN31): remote is the receiver (DL1A, JO62 → EU).
	loc, call, ok = resolveRemoteEnd(m, []string{"W1AW"})
	if !ok {
		t.Fatalf("expected match")
	}
	if loc != "JO62" {
		t.Errorf("remote locator = %q, want JO62", loc)
	}
	if call != "DL1A" {
		t.Errorf("remote call = %q, want DL1A", call)
	}
	// Locator QTH matching via prefix.
	loc, _, ok = resolveRemoteEnd(m, []string{"JO62"})
	if !ok || loc != "FN31" {
		t.Errorf("locator QTH JO62: remote = %q, ok=%v, want FN31", loc, ok)
	}
	// No match.
	_, _, ok = resolveRemoteEnd(m, []string{"VK2AA"})
	if ok {
		t.Errorf("expected no match for unrelated QTH")
	}
}

// TestPropIntelRegionsDedup verifies that the same sender appearing in both
// RBN and PSKReporter is counted once for the unique-sender total.
func TestPropIntelRegionsDedup(t *testing.T) {
	e := &propIntelEngine{}
	now := int64(1700000000)
	history := []MQTTMessage{
		{RP: -10, T: now - 60, SC: "DUAL", SL: "JO40", RC: "DL1A", RL: "JO62", B: "20m", MD: "CW", Source: "rbn"},
		{RP: -8, T: now - 50, SC: "DUAL", SL: "JO40", RC: "DL1A", RL: "JO62", B: "20m", MD: "FT8", Source: "mqtt"},
		{RP: -8, T: now - 40, SC: "ONLY", SL: "JO41", RC: "DL1A", RL: "JO62", B: "20m", MD: "FT8", Source: "mqtt"},
	}
	resp := e.Evaluate("JO62", false, 15, -15, history, now)
	cell := findCell(t, resp, "20m", "EU")
	if cell == nil {
		t.Fatalf("expected 20m/EU cell")
	}
	// 2 unique senders (DUAL counted once + ONLY).
	if got := sourcesString(cell); !strings.Contains(got, "rbn") || !strings.Contains(got, "pskreporter") {
		t.Errorf("sources = %q, want both rbn and pskreporter", got)
	}
	// Confidence should reflect 2 senders (low support) but 2 sources (diversity).
	// 2 sources lifts confidence above the single-source sparse case.
	if cell.Confidence < 0.3 {
		t.Errorf("confidence = %v, want ≥ 0.3 for 2-sender 2-source cell", cell.Confidence)
	}
	if cell.Confidence > 0.6 {
		t.Errorf("confidence = %v, want ≤ 0.6 for 2-sender cell (low support)", cell.Confidence)
	}
}

// findCell returns the cell for (band, region) or nil.
func findCell(t *testing.T, resp propIntelResponse, band, region string) *propIntelCell {
	t.Helper()
	for i := range resp.Cells {
		if resp.Cells[i].Band == band && resp.Cells[i].Region == region {
			return &resp.Cells[i]
		}
	}
	return nil
}

// sourcesString returns the cell's sources as a sorted comma-joined string.
func sourcesString(cell *propIntelCell) string {
	if cell == nil {
		return ""
	}
	cp := append([]string(nil), cell.Sources...)
	sort.Strings(cp)
	return strings.Join(cp, ",")
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}