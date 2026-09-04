package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func v2Spots(profiles ...string) []propIntelSourceProfile {
	out := []propIntelSourceProfile{}
	for _, name := range profiles {
		out = append(out, propIntelProfilesByName[name])
	}
	return out
}

func findV2Cell(t *testing.T, resp propIntelV2Response, band, regionCode string) propIntelV2Cell {
	t.Helper()
	for _, c := range resp.Cells {
		if c.Band == band && c.Region == regionCode {
			return c
		}
	}
	t.Fatalf("v2 cell %s/%s not found", band, regionCode)
	return propIntelV2Cell{}
}

func findV2SourceCell(t *testing.T, c propIntelV2Cell, source string) propIntelV2SourceCell {
	t.Helper()
	for _, s := range c.PerSource {
		if s.Source == source {
			return s
		}
	}
	t.Fatalf("source %s not in cell %s/%s", source, c.Band, c.Region)
	return propIntelV2SourceCell{}
}

// TestV2WsprBudgetParity: the same fixture must produce identical ssb/cw
// flags through v1 Evaluate and v2 EvaluateV2 (sources=wspr only).
func TestV2WsprBudgetParity(t *testing.T) {
	now := time.Now().Unix()
	// Strong path: SNR +5, 20W (43 dBm) → effective 12 dB → SSB+CW open.
	history := []MQTTMessage{
		makeWSPRSpot(now-100, "20m", "JO62qm", "FN31ab", 5, 43),
	}

	v1 := propIntel.Evaluate("JO62", false, 15, defaultDxCwViableMinDb, history, now, propIntelAtypicalZThreshold)
	v2 := propIntelV2.EvaluateV2("JO62", false, 15, v2Spots("wspr"), nil, nil, history, now, propIntelAtypicalZThreshold)

	// Operator at JO62 is the RL end; remote is SL = FN31 (NA).
	c1 := findCell(t, v1, "20m", "NA")
	c2 := findV2Cell(t, v2, "20m", "NA")
	if c1.SSBOpen != c2.SSBOpen || c1.CWOpen != c2.CWOpen {
		t.Fatalf("parity mismatch: v1 ssb=%v cw=%v, v2 ssb=%v cw=%v",
			c1.SSBOpen, c1.CWOpen, c2.SSBOpen, c2.CWOpen)
	}
	if c1.SpotCount != c2.SpotCount || c1.FromHere != c2.FromHere {
		t.Fatalf("parity mismatch: v1 spots=%d fh=%v, v2 spots=%d fh=%v",
			c1.SpotCount, c1.FromHere, c2.SpotCount, c2.FromHere)
	}
}

// TestV2PskrFloors: pskr SNR-floor semantics per level.
func TestV2PskrFloors(t *testing.T) {
	now := time.Now().Unix()
	spot := func(snr int) MQTTMessage {
		// pskr: SC/SL = transmitter (DX side), RC/RL = reporter.
		return MQTTMessage{Source: "mqtt", B: "20m", T: now - 100,
			SC: "DX", SL: "FN31ab", RC: "OP", RL: "JO62qm", RP: snr, MD: "FT8"}
	}
	cases := []struct {
		snr                        int
		open, cw, ssb              bool
	}{
		{-23, true, false, false}, // digital only
		{-10, true, true, false},  // cw open
		{0, true, true, true},     // ssb open
	}
	for _, tc := range cases {
		resp := propIntelV2.EvaluateV2("JO62", false, 15, v2Spots("pskr"), nil, nil, []MQTTMessage{spot(tc.snr)}, now, propIntelAtypicalZThreshold)
		c := findV2Cell(t, resp, "20m", "NA") // operator at JO62 is the reporter end → remote is FN31 (NA)
		s := findV2SourceCell(t, c, "pskr")
		if s.Open != tc.open {
			t.Fatalf("snr=%d: open=%v want %v", tc.snr, s.Open, tc.open)
		}
		if s.CWOpen == nil || *s.CWOpen != tc.cw {
			t.Fatalf("snr=%d: cw=%v want %v", tc.snr, s.CWOpen, tc.cw)
		}
		if s.SSBOpen == nil || *s.SSBOpen != tc.ssb {
			t.Fatalf("snr=%d: ssb=%v want %v", tc.snr, s.SSBOpen, tc.ssb)
		}
		if !s.UnknownPower {
			t.Fatalf("snr=%d: pskr must carry unknown_power", tc.snr)
		}
		if s.OpenBasis != "snr_floor" {
			t.Fatalf("snr=%d: basis=%q", tc.snr, s.OpenBasis)
		}
	}
}

// TestV2GlobalMinSnOverrides: the ssb_min_db/cw_min_db params (the UI's
// global Min SNR control) replace per-source floors for SNR-floored sources.
func TestV2GlobalMinSnOverrides(t *testing.T) {
	now := time.Now().Unix()
	spot := func(snr int) MQTTMessage {
		return MQTTMessage{Source: "mqtt", B: "20m", T: now - 100,
			SC: "DX", SL: "FN31ab", RC: "OP", RL: "JO62qm", RP: snr, MD: "FT8"}
	}
	iptr := func(v int) *int { return &v }

	// snr=? sits between the pskr profile defaults and the overrides below.
	seen := func(ssbOv, cwOv *int, snr int) (open, cw, ssb bool) {
		resp := propIntelV2.EvaluateV2("JO62", false, 15, v2Spots("pskr"), ssbOv, cwOv, []MQTTMessage{spot(snr)}, now, propIntelAtypicalZThreshold)
		c := findV2Cell(t, resp, "20m", "NA")
		s := findV2SourceCell(t, c, "pskr")
		return s.Open, s.CWOpen != nil && *s.CWOpen, s.SSBOpen != nil && *s.SSBOpen
	}

	// Profile defaults (prop_intel_sources.go): ssb floor -5, cw floor -18.
	// snr=-7 sits between: cw open, ssb not.
	open, cw, ssb := seen(nil, nil, -7)
	if !open || !cw || ssb {
		t.Fatalf("snr=-7 defaults: open=%v cw=%v ssb=%v (want open+cw)", open, cw, ssb)
	}
	// cw_min_db=-2 raises the CW floor above the report: cw flag flips off.
	open, cw, ssb = seen(nil, iptr(-2), -7)
	if !open || cw || ssb {
		t.Fatalf("snr=-7 cw_override=-2: open=%v cw=%v ssb=%v (want open, no cw)", open, cw, ssb)
	}
	// ssb_min_db=0: -7 is below the phone floor…
	open, cw, ssb = seen(iptr(0), nil, -7)
	if !open || !cw || ssb {
		t.Fatalf("snr=-7 ssb_override=0: open=%v cw=%v ssb=%v", open, cw, ssb)
	}
	// …while ssb_min_db=-10 opens it.
	_, _, ssb = seen(iptr(-10), nil, -7)
	if !ssb {
		t.Fatalf("snr=-7 ssb_override=-10: ssb must open")
	}
}

// TestV2RbnNeverSSB: RBN is a CW skimmer — ssb_open is nil (not false).
func TestV2RbnNeverSSB(t *testing.T) {
	now := time.Now().Unix()
	// rbn: SC/SL = skimmer (receiver), RC/RL = DX.
	m := MQTTMessage{Source: "rbn", B: "20m", T: now - 100,
		SC: "SKIM", SL: "JO62qm", RC: "DX", RL: "FN31ab", RP: 30, MD: "CW"}
	resp := propIntelV2.EvaluateV2("JO62", false, 15, v2Spots("rbn"), nil, nil, []MQTTMessage{m}, now, propIntelAtypicalZThreshold)
	c := findV2Cell(t, resp, "20m", "NA")
	s := findV2SourceCell(t, c, "rbn")
	if s.SSBOpen != nil {
		t.Fatalf("rbn ssb_open must be nil, got %v", *s.SSBOpen)
	}
	if s.CWOpen == nil || !*s.CWOpen {
		t.Fatalf("rbn cw_open must be true at +30 dB")
	}
	if !s.Open {
		t.Fatalf("rbn open must be true at +30 dB")
	}

	// Nil vs false serialization: ssb_open must be absent from the JSON.
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "ssb_open") {
		t.Fatalf("nil ssb_open must serialize as absent: %s", raw)
	}
	if !strings.Contains(string(raw), `"cw_open":true`) {
		t.Fatalf("cw_open must serialize: %s", raw)
	}
}

// TestV2DxclusterPresence: open = ≥2 spots; no SNR flags ever.
func TestV2DxclusterPresence(t *testing.T) {
	now := time.Now().Unix()
	spot := func(dt int64) MQTTMessage {
		return MQTTMessage{Source: "dxcluster", B: "20m", T: now - dt,
			SC: "SPOT", SL: "JO62qm", RC: "DX", RL: "FN31ab", MD: ""}
	}

	resp := propIntelV2.EvaluateV2("JO62", false, 15, v2Spots("dxcluster"), nil, nil, []MQTTMessage{spot(100)}, now, propIntelAtypicalZThreshold)
	c := findV2Cell(t, resp, "20m", "NA")
	s := findV2SourceCell(t, c, "dxcluster")
	if s.Open {
		t.Fatalf("1 cluster spot must not open the cell")
	}
	if s.SSBOpen != nil || s.CWOpen != nil || s.OpenBasis != "presence" {
		t.Fatalf("dxcluster must have nil mode flags and presence basis: %+v", s)
	}

	resp = propIntelV2.EvaluateV2("JO62", false, 15, v2Spots("dxcluster"), nil, nil, []MQTTMessage{spot(100), spot(50)}, now, propIntelAtypicalZThreshold)
	c = findV2Cell(t, resp, "20m", "NA")
	s = findV2SourceCell(t, c, "dxcluster")
	if !s.Open {
		t.Fatalf("2 cluster spots must open the cell")
	}
}

// TestV2Agreement: rollup ORs the flags and computes the open agreement.
func TestV2Agreement(t *testing.T) {
	now := time.Now().Unix()
	history := []MQTTMessage{
		// wspr open (strong budget path), remote FN31.
		{Source: "wspr", B: "20m", T: now - 100, SC: "OP", SL: "JO62qm", RC: "TX", RL: "FN31ab", RP: 5, TXPower: 43},
		// pskr closed (below the -24 dB digital floor), remote FN31.
		{Source: "mqtt", B: "20m", T: now - 100, SC: "DX", SL: "FN31ab", RC: "OP", RL: "JO62qm", RP: -30, MD: "FT8"},
		// rbn open, remote FN31.
		{Source: "rbn", B: "20m", T: now - 100, SC: "SKIM", SL: "JO62qm", RC: "DX", RL: "FN31ab", RP: 20, MD: "CW"},
	}
	resp := propIntelV2.EvaluateV2("JO62", false, 15, v2Spots("wspr", "pskr", "rbn"), nil, nil, history, now, propIntelAtypicalZThreshold)
	c := findV2Cell(t, resp, "20m", "NA")

	if got := strings.Join(c.ActiveSources, ","); got != "wspr,pskr,rbn" {
		t.Fatalf("active sources canonical order: %q", got)
	}
	if !c.Open || c.OpenAgreement != 0.67 {
		t.Fatalf("open rollup: open=%v agreement=%v, want open=true agreement=0.67", c.Open, c.OpenAgreement)
	}
	if c.SpotCount != 3 {
		t.Fatalf("spot_count rollup: %d", c.SpotCount)
	}
}

// TestV2AtypicalPerSource: surge detection uses each source's own climatology
// and the rollup picks the highest-confidence atypical.
func TestV2AtypicalPerSource(t *testing.T) {
	now := time.Now().Unix()
	slot := utcSlotOfDay(now)

	// Seed the climatology singleton: low historical counts for both wspr and
	// pskr in (20m, NA, current slot) across 3 past days (same time-of-day).
	saved := propBaseline
	defer func() { propBaseline = saved }()
	propBaseline = newPropBaselineEngine("", "")
	for _, c := range []int64{1, 2, 4} { // mean 2.33, stddev ~1.53
		for d := int64(1); d <= 3; d++ {
			ts := now - d*86400
			if utcSlotOfDay(ts) != slot {
				t.Fatalf("test setup: slot drift (%d vs %d)", utcSlotOfDay(ts), slot)
			}
			_ = c
		}
	}
	// Observed counts per day: 1, 2, 4 → nonzero stddev. The climatology keys
	// by the RECEIVER side's region (global mesh), while the live from-here
	// cell keys by the remote end — so seed history with receivers in NA to
	// match the live NA cell.
	dayCounts := []int64{1, 2, 4}
	for di, c := range dayCounts {
		ts := now - int64(di+1)*86400
		for i := int64(0); i < c; i++ {
			// wspr: SL = receiver → FN31 (NA).
			propBaseline.Observe(MQTTMessage{Source: "wspr", B: "20m", T: ts, SC: "RX", SL: "FN31ab", RC: "TX", RL: "JO62qm"})
			// pskr: RL = receiver/reporter → FN31 (NA).
			propBaseline.Observe(MQTTMessage{Source: "mqtt", B: "20m", T: ts, SC: "TX", SL: "JO62qm", RC: "RX", RL: "FN31ab", MD: "FT8"})
		}
	}

	// Live: 10 wspr + 10 pskr spots in that cell → rate 20/(30min-slot) each.
	history := []MQTTMessage{}
	for i := 0; i < 10; i++ {
		history = append(history,
			MQTTMessage{Source: "wspr", B: "20m", T: now - int64(i)*10, SC: "OP", SL: "JO62qm", RC: "TX" + itoa(i), RL: "FN31ab", TXPower: 43, RP: 5},
			MQTTMessage{Source: "mqtt", B: "20m", T: now - int64(i)*10, SC: "DX" + itoa(i), SL: "FN31ab", RC: "OP", RL: "JO62qm", MD: "FT8"},
		)
	}
	resp := propIntelV2.EvaluateV2("JO62", false, 15, v2Spots("wspr", "pskr"), nil, nil, history, now, propIntelAtypicalZThreshold)
	c := findV2Cell(t, resp, "20m", "NA")
	if c.Atypical == nil {
		t.Fatalf("surge over climatology must flag atypical; cell=%+v", c)
	}
	if c.AtypicalAgreement != 1.0 {
		t.Fatalf("both sources atypical → agreement 1.0, got %v", c.AtypicalAgreement)
	}
	for _, name := range []string{"wspr", "pskr"} {
		s := findV2SourceCell(t, c, name)
		if s.Atypical == nil || s.SampleDays < propIntelMinSampleDays {
			t.Fatalf("%s must carry its own atypical with sample days: %+v", name, s)
		}
	}
}

// TestV2ColdStartNoAtypical: without climatology depth (< 3 sample days) no
// atypical fires even on a huge count.
func TestV2ColdStartNoAtypical(t *testing.T) {
	now := time.Now().Unix()
	saved := propBaseline
	defer func() { propBaseline = saved }()
	propBaseline = newPropBaselineEngine("", "")

	history := []MQTTMessage{}
	for i := 0; i < 50; i++ {
		history = append(history, MQTTMessage{Source: "rbn", B: "20m", T: now - int64(i)*5,
			SC: "SKIM", SL: "JO62qm", RC: "DX" + itoa(i), RL: "FN31ab", RP: 30, MD: "CW"})
	}
	resp := propIntelV2.EvaluateV2("JO62", false, 15, v2Spots("rbn"), nil, nil, history, now, propIntelAtypicalZThreshold)
	c := findV2Cell(t, resp, "20m", "NA")
	if c.Atypical != nil {
		t.Fatalf("cold start must suppress atypical: %+v", c.Atypical)
	}
}

// TestV2FromHereFilter: applyFromHere on the v2 response.
func TestV2FromHereFilter(t *testing.T) {
	now := time.Now().Unix()
	history := []MQTTMessage{
		// from-here (operator at JO62)
		{Source: "wspr", B: "20m", T: now - 100, SC: "OP", SL: "JO62qm", RC: "TX", RL: "FN31ab", RP: 5, TXPower: 43},
		// global-mesh (neither end is the operator), receiver JO62-ish in NA
		{Source: "wspr", B: "40m", T: now - 100, SC: "RX", SL: "FN31ab", RC: "TX", RL: "EM10ab", RP: 5, TXPower: 43},
	}
	resp := propIntelV2.EvaluateV2("JO62", false, 15, v2Spots("wspr"), nil, nil, history, now, propIntelAtypicalZThreshold)
	if len(resp.Cells) != 2 {
		t.Fatalf("unfiltered response wants 2 cells, got %d", len(resp.Cells))
	}
	filtered := resp.applyFromHere(true)
	if len(filtered.Cells) != 1 || !filtered.Cells[0].FromHere {
		t.Fatalf("from-here filter broken: %+v", filtered.Cells)
	}
}

// TestV2SummaryAdditiveFields: the v2 summary keeps the v1 shape and adds
// sa/oa/src on grid cells.
func TestV2SummaryAdditiveFields(t *testing.T) {
	now := time.Now().Unix()
	history := []MQTTMessage{
		{Source: "wspr", B: "20m", T: now - 100, SC: "OP", SL: "JO62qm", RC: "TX", RL: "FN31ab", RP: 5, TXPower: 43},
		{Source: "mqtt", B: "20m", T: now - 90, SC: "DX", SL: "FN31ab", RC: "OP", RL: "JO62qm", RP: 0, MD: "FT8"},
	}
	resp := propIntelV2.EvaluateV2("JO62", false, 15, v2Spots("wspr", "pskr"), nil, nil, history, now, propIntelAtypicalZThreshold)
	sum := propIntelV2Summarize(resp)
	if len(sum.Grid) == 0 {
		t.Fatalf("summary grid empty")
	}
	g := sum.Grid[0]
	if g.SourceCount != 2 || len(g.ActiveSources) != 2 {
		t.Fatalf("additive fields: src=%d sa=%v", g.SourceCount, g.ActiveSources)
	}
	if g.OpenAgreement != 1.0 {
		t.Fatalf("both sources open → oa=1.0, got %v", g.OpenAgreement)
	}
	if g.Flags&propIntelFlagSSB == 0 || g.Flags&propIntelFlagFromHere == 0 {
		t.Fatalf("bitmask flags lost: %b", g.Flags)
	}
	if g.Intensity != 1.0 {
		t.Fatalf("single cell is the max → intensity 1.0, got %v", g.Intensity)
	}
	if sum.Headline == "" || sum.HeadlineKind == "" {
		t.Fatalf("headline must be set: %+v", sum)
	}
}
