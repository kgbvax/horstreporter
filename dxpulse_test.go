package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestBuildDxPulseMatrixQuality(t *testing.T) {
	now := time.Date(2026, time.May, 28, 12, 10, 0, 0, time.UTC).Unix()
	current := []MQTTMessage{
		{T: now - 60, SC: "W1AW", RC: "DL1AAA", SL: "FN31", RL: "JO32", B: "20m", MD: "FT8", RP: -9},
		{T: now - 120, SC: "W1AW", RC: "DL1AAB", SL: "FN31", RL: "JO33", B: "20m", MD: "FT8", RP: -7},
		{T: now - 180, SC: "W1AW", RC: "DL1AAC", SL: "FN31", RL: "JO34", B: "20m", MD: "FT8", RP: -8},
		{T: now - 240, SC: "W1AW", RC: "DL1AAD", SL: "FN31", RL: "JO32", B: "20m", MD: "FT8", RP: -10},
		{T: now - 300, SC: "W1AW", RC: "DL1AAE", SL: "FN31", RL: "JO33", B: "20m", MD: "FT8", RP: -6},
		{T: now - 360, SC: "W1AW", RC: "DL1AAF", SL: "FN31", RL: "JO34", B: "20m", MD: "FT8", RP: -11},
	}

	resp := buildDxPulseMatrix("FN31", false, "quality", 15, 45, current, nil, false, now)
	if resp.Mode != dxPulseModeQuality {
		t.Fatalf("expected quality mode, got %q", resp.Mode)
	}
	if !reflect.DeepEqual(resp.Bands, dxPulseDefaultVisibleBands) {
		t.Fatalf("expected stable default bands %v, got %v", dxPulseDefaultVisibleBands, resp.Bands)
	}

	cell := findDxPulseCell(resp, "20m", "EU")
	if cell == nil {
		t.Fatal("expected 20m/EU cell")
	}
	if cell.CurrentSpotCount != 6 {
		t.Fatalf("expected 6 current spots, got %d", cell.CurrentSpotCount)
	}
	if cell.CurrentUniquePaths != 6 {
		t.Fatalf("expected 6 unique paths, got %d", cell.CurrentUniquePaths)
	}
	if cell.CurrentUniqueGrids != 3 {
		t.Fatalf("expected 3 unique remote grids, got %d", cell.CurrentUniqueGrids)
	}
	if cell.State != "good" {
		t.Fatalf("expected state good, got %q", cell.State)
	}
	if cell.Confidence <= 0 {
		t.Fatalf("expected confidence > 0, got %f", cell.Confidence)
	}
}

func TestBuildDxPulseMatrixKeepsDefaultBandRowsWithoutData(t *testing.T) {
	now := time.Date(2026, time.May, 28, 12, 10, 0, 0, time.UTC).Unix()
	current := []MQTTMessage{
		{T: now - 60, SC: "W1AW", RC: "DL1AAA", SL: "FN31", RL: "JO32", B: "20m", MD: "FT8", RP: -9},
	}

	resp := buildDxPulseMatrix("FN31", false, "quality", 5, 45, current, nil, false, now)
	if !reflect.DeepEqual(resp.Bands, dxPulseDefaultVisibleBands) {
		t.Fatalf("expected stable default bands %v, got %v", dxPulseDefaultVisibleBands, resp.Bands)
	}

	cell := findDxPulseCell(resp, "60m", "EU")
	if cell == nil {
		t.Fatal("expected empty 60m/EU cell to be present")
	}
	if cell.State != "none" {
		t.Fatalf("expected empty 60m/EU state none, got %q", cell.State)
	}
	if cell.Label != "No propagation" {
		t.Fatalf("expected empty 60m/EU label 'No propagation', got %q", cell.Label)
	}
	if cell.CurrentSpotCount != 0 {
		t.Fatalf("expected empty 60m/EU spot count 0, got %d", cell.CurrentSpotCount)
	}
}

func TestBuildDxPulseMatrixAnomaly(t *testing.T) {
	now := time.Date(2026, time.May, 28, 12, 10, 0, 0, time.UTC).Unix()
	current := []MQTTMessage{
		{T: now - 60, SC: "W1AW", RC: "DL1AAA", SL: "FN31", RL: "JO32", B: "20m", MD: "FT8", RP: -9},
		{T: now - 120, SC: "W1AW", RC: "DL1AAB", SL: "FN31", RL: "JO33", B: "20m", MD: "FT8", RP: -7},
		{T: now - 180, SC: "W1AW", RC: "DL1AAC", SL: "FN31", RL: "JO34", B: "20m", MD: "FT8", RP: -8},
		{T: now - 240, SC: "W1AW", RC: "DL1AAD", SL: "FN31", RL: "JO32", B: "20m", MD: "FT8", RP: -10},
		{T: now - 300, SC: "W1AW", RC: "DL1AAE", SL: "FN31", RL: "JO33", B: "20m", MD: "FT8", RP: -6},
		{T: now - 360, SC: "W1AW", RC: "DL1AAF", SL: "FN31", RL: "JO34", B: "20m", MD: "FT8", RP: -11},
	}

	baseline := make([]MQTTMessage, 0, 24)
	for d := 1; d <= 20; d++ {
		baseline = append(baseline, MQTTMessage{
			T:  now - int64(d*24*60*60) - 120,
			SC: "W1AW",
			RC: "DL1ZZZ",
			SL: "FN31",
			RL: "JO32",
			B:  "20m",
			MD: "FT8",
			RP: -12,
		})
	}

	resp := buildDxPulseMatrix("FN31", false, "anomaly", 15, 20, current, baseline, true, now)
	if !resp.BaselineAvailable {
		t.Fatal("expected baseline to be available")
	}
	cell := findDxPulseCell(resp, "20m", "EU")
	if cell == nil {
		t.Fatal("expected 20m/EU cell")
	}
	if cell.State != "far_above" {
		t.Fatalf("expected far_above state, got %q", cell.State)
	}
	if cell.BaselineRatio <= 2.0 {
		t.Fatalf("expected baseline ratio > 2, got %f", cell.BaselineRatio)
	}
	if cell.BaselineSupport != 20 {
		t.Fatalf("expected baseline support 20, got %d", cell.BaselineSupport)
	}
}

func TestDxPulseMatrixHandlerRequiresLocator(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(dxPulseMatrixHandler))
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestDxPulseMatrixHandlerQualityResponse(t *testing.T) {
	withHubSnapshot(t, func() {
		now := time.Now().Unix()
		hub.Lock()
		hub.history = []MQTTMessage{
			{T: now - 60, SC: "W1AW", RC: "DL1AAA", SL: "FN31", RL: "JO32", B: "20m", MD: "FT8", RP: -9},
			{T: now - 120, SC: "W1AW", RC: "DL1AAB", SL: "FN31", RL: "JO33", B: "20m", MD: "FT8", RP: -7},
			{T: now - 180, SC: "W1AW", RC: "DL1AAC", SL: "FN31", RL: "JO34", B: "20m", MD: "FT8", RP: -8},
		}
		hub.Unlock()

		server := httptest.NewServer(http.HandlerFunc(dxPulseMatrixHandler))
		defer server.Close()

		resp, err := http.Get(server.URL + "?target=FN31&mode=quality&minutes=15")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		var payload dxPulseMatrixResponse
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			t.Fatalf("decode failed: %v", err)
		}
		if payload.Target != "FN31" {
			t.Fatalf("expected target FN31, got %q", payload.Target)
		}
		cell := findDxPulseCell(payload, "20m", "EU")
		if cell == nil {
			t.Fatal("expected 20m/EU cell in response")
		}
		if cell.CurrentSpotCount != 3 {
			t.Fatalf("expected 3 spots, got %d", cell.CurrentSpotCount)
		}
	})
}

func TestBuildDxPulseSummary(t *testing.T) {
	matrix := dxPulseMatrixResponse{
		Target:            "FN31",
		Mode:              dxPulseModeQuality,
		ModeLabel:         dxPulseModeLabel(dxPulseModeQuality),
		WindowMinutes:     15,
		GeneratedAt:       12345,
		BaselineAvailable: false,
		Bands:             []string{"20m", "15m"},
		Regions:           []string{"EU", "NA"},
		Matrix: [][]dxPulseMatrixCell{
			{
				{Band: "20m", Region: "EU", State: "good", Label: "Good", CurrentSpotCount: 8, CurrentUniquePaths: 6, Confidence: 0.9},
				{Band: "20m", Region: "NA", State: "fair", Label: "Fair", CurrentSpotCount: 2, CurrentUniquePaths: 2, Confidence: 0.4},
			},
			{
				{Band: "15m", Region: "EU", State: "excellent", Label: "Excellent", CurrentSpotCount: 6, CurrentUniquePaths: 5, Confidence: 0.8},
				{Band: "15m", Region: "NA", State: "poor", Label: "Poor", CurrentSpotCount: 1, CurrentUniquePaths: 1, Confidence: 0.2},
			},
		},
	}

	summary := buildDxPulseSummary(matrix)
	if summary.Target != "FN31" {
		t.Fatalf("expected target FN31, got %q", summary.Target)
	}
	if len(summary.BestBands) != 2 {
		t.Fatalf("expected 2 best bands, got %d", len(summary.BestBands))
	}
	if summary.BestBands[0].Band != "20m" {
		t.Fatalf("expected 20m to lead best bands, got %q", summary.BestBands[0].Band)
	}
	if len(summary.TopRegions) == 0 || summary.TopRegions[0].Region != "EU" {
		t.Fatalf("expected EU to lead top regions, got %+v", summary.TopRegions)
	}
	if len(summary.HotCells) == 0 || summary.HotCells[0].Band != "20m" || summary.HotCells[0].Region != "EU" {
		t.Fatalf("expected 20m/EU to lead hot cells, got %+v", summary.HotCells)
	}
}

func TestDxPulseSummaryHandlerQualityResponse(t *testing.T) {
	withHubSnapshot(t, func() {
		now := time.Now().Unix()
		hub.Lock()
		hub.history = []MQTTMessage{
			{T: now - 60, SC: "W1AW", RC: "DL1AAA", SL: "FN31", RL: "JO32", B: "20m", MD: "FT8", RP: -9},
			{T: now - 120, SC: "W1AW", RC: "DL1AAB", SL: "FN31", RL: "JO33", B: "20m", MD: "FT8", RP: -7},
			{T: now - 180, SC: "W1AW", RC: "K1ABC", SL: "FN31", RL: "FN42", B: "15m", MD: "FT8", RP: -6},
		}
		hub.Unlock()

		server := httptest.NewServer(http.HandlerFunc(dxPulseSummaryHandler))
		defer server.Close()

		resp, err := http.Get(server.URL + "?target=FN31&mode=quality&minutes=15")
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		var payload dxPulseSummaryResponse
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			t.Fatalf("decode failed: %v", err)
		}
		if payload.Target != "FN31" {
			t.Fatalf("expected target FN31, got %q", payload.Target)
		}
		if len(payload.BestBands) == 0 || payload.BestBands[0].Band != "20m" {
			t.Fatalf("expected 20m to appear in best bands, got %+v", payload.BestBands)
		}
		if len(payload.TopRegions) == 0 || payload.TopRegions[0].Region != "EU" {
			t.Fatalf("expected EU in top regions, got %+v", payload.TopRegions)
		}
		if len(payload.HotCells) == 0 {
			t.Fatal("expected at least one hot cell")
		}
	})
}

func TestDxPulseHelperFunctions(t *testing.T) {
	t.Run("normalizes mode labels and region strings", func(t *testing.T) {
		if got := normalizeDxPulseMode(""); got != dxPulseModeQuality {
			t.Fatalf("expected default quality mode, got %q", got)
		}
		if got := normalizeDxPulseMode("relative"); got != dxPulseModeAnomaly {
			t.Fatalf("expected relative to map to anomaly, got %q", got)
		}
		if got := dxPulseModeLabel("absolute"); got != "Quality" {
			t.Fatalf("expected absolute label Quality, got %q", got)
		}
		if got := dxPulseModeLabel("anomaly"); got != "Anomaly" {
			t.Fatalf("expected anomaly label, got %q", got)
		}

		regions := dxPulseRegionStrings()
		if len(regions) != len(dxPulseAllRegions) {
			t.Fatalf("expected %d regions, got %d", len(dxPulseAllRegions), len(regions))
		}
		if regions[0] != string(dxPulseRegionEU) {
			t.Fatalf("expected first region EU, got %q", regions[0])
		}
	})

	t.Run("window slots span midnight correctly", func(t *testing.T) {
		now := time.Date(2026, time.May, 29, 0, 10, 0, 0, time.UTC).Unix()
		got := dxPulseWindowSlots(now, 45)
		want := []int32{0, 46, 47}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("expected slots %v, got %v", want, got)
		}
	})

	t.Run("quality and anomaly cells cover empty and sparse baseline states", func(t *testing.T) {
		now := time.Now().Unix()
		quality := buildDxPulseQualityCell("20m", "EU", nil, now)
		if quality.State != "none" {
			t.Fatalf("expected empty quality cell state none, got %q", quality.State)
		}

		current := &dxPulseCellAccumulator{
			spotCount:         3,
			uniquePaths:       map[string]struct{}{"a": {}, "b": {}, "c": {}},
			uniqueRemoteGrids: map[string]struct{}{"JO32": {}, "JO33": {}},
			sumSNR:            -21,
			snrs:              []int{-8, -7, -6},
			lastSeen:          now - 30,
		}
		anomaly := buildDxPulseAnomalyCell("20m", "EU", current, nil, 45, false, now)
		if anomaly.State != "insufficient_baseline" {
			t.Fatalf("expected sparse baseline state, got %q", anomaly.State)
		}
		if anomaly.CurrentSpotCount != 3 {
			t.Fatalf("expected current spot count 3, got %d", anomaly.CurrentSpotCount)
		}
	})

	t.Run("observation picks remote side and rejects invalid locators", func(t *testing.T) {
		obs, ok := dxPulseObserve(MQTTMessage{
			T:  time.Now().Unix(),
			SC: "DL1ABC",
			RC: "W1AW",
			SL: "JO32",
			RL: "FN31",
			B:  "20m",
			RP: -9,
		}, []string{"FN31"})
		if !ok {
			t.Fatal("expected receiver-side match to be observed")
		}
		if obs.region != dxPulseRegionEU {
			t.Fatalf("expected remote region EU, got %q", obs.region)
		}
		if obs.remote4 != "JO32" {
			t.Fatalf("expected remote4 JO32, got %q", obs.remote4)
		}

		if _, ok := dxPulseObserve(MQTTMessage{SL: "", RL: "FN31", B: "20m"}, []string{"FN31"}); ok {
			t.Fatal("expected invalid locator observation to be rejected")
		}
	})
}

func findDxPulseCell(resp dxPulseMatrixResponse, band, region string) *dxPulseMatrixCell {
	for i := range resp.Matrix {
		for j := range resp.Matrix[i] {
			cell := &resp.Matrix[i][j]
			if cell.Band == band && cell.Region == region {
				return cell
			}
		}
	}
	return nil
}
