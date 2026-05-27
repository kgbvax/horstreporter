package main

import (
	"bufio"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIsLocator(t *testing.T) {
	tests := []struct {
		name string
		loc  string
		want bool
	}{
		{"Valid 4-char", "JO32", true},
		{"Valid 6-char", "FN31AB", true},
		{"Valid edge case A0", "AA00", true},
		{"Valid edge case R9", "RR99", true},
		{"Too short", "JO3", false},
		{"Invalid letters", "ZZ32", false},
		{"Invalid numbers", "JOA2", false},
		{"Empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isLocator(tt.loc); got != tt.want {
				t.Errorf("isLocator(%q) = %v, want %v", tt.loc, got, tt.want)
			}
		})
	}
}

func TestMatchCall(t *testing.T) {
	tests := []struct {
		name     string
		spotCall string
		target   string
		want     bool
	}{
		{"Exact match", "W1AW", "W1AW", true},
		{"Suffix match", "W1AW/P", "W1AW", true},
		{"Prefix match", "DL/W1AW", "W1AW", true},
		{"Prefix and suffix match", "DL/W1AW/P", "W1AW", true},
		{"No match substring", "W1AWA", "W1AW", false},
		{"No match partial", "W1AW", "W1A", false},
		{"No match different", "K1JT", "W1AW", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchCall(tt.spotCall, tt.target); got != tt.want {
				t.Errorf("matchCall(%q, %q) = %v, want %v", tt.spotCall, tt.target, got, tt.want)
			}
		})
	}
}

func TestLocatorToLatLng(t *testing.T) {
	tests := []struct {
		name    string
		locator string
		wantLat float64
		wantLng float64
	}{
		{"4-char locator", "JO32", 52.5, 7.0},
		{"2-char locator", "JO", 55.0, 10.0},
		{"Invalid/Empty", "", 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lat, lng := locatorToLatLng(tt.locator)
			// Using a small tolerance for floating-point comparison
			if math.Abs(lat-tt.wantLat) > 0.001 || math.Abs(lng-tt.wantLng) > 0.001 {
				t.Errorf("locatorToLatLng(%q) = (%v, %v), want (%v, %v)", tt.locator, lat, lng, tt.wantLat, tt.wantLng)
			}
		})
	}
}

func TestGetSurroundingSquares(t *testing.T) {
	res := getSurroundingSquares("JO32")
	if len(res) != 9 {
		t.Fatalf("expected 9 squares, got %d: %v", len(res), res)
	}

	foundCenter := false
	for _, s := range res {
		if s == "JO32" {
			foundCenter = true
			break
		}
	}
	if !foundCenter {
		t.Errorf("expected to find center square JO32 in results")
	}
}

func TestMatchAndCreateSpot(t *testing.T) {
	now := int64(100000)

	msg := MQTTMessage{
		SC: "W1AW",
		RC: "K1JT",
		SL: "FN31",
		RL: "FN20",
		RP: -15,
		T:  99990,
		B:  "20m",
		MD: "FT8",
	}

	t.Run("Match by Sender Callsign", func(t *testing.T) {
		client := &Client{targets: []string{"W1AW"}}
		spot, ok := matchAndCreateSpot(client, msg, now)
		if !ok {
			t.Fatal("expected spot to match")
		}
		if spot.Sender != "W1AW" {
			t.Errorf("expected sender W1AW, got %q", spot.Sender)
		}
		if spot.AgeSeconds != 10 {
			t.Errorf("expected age 10s, got %d", spot.AgeSeconds)
		}
		if spot.Locator != "FN20" {
			t.Errorf("expected remote locator FN20 (receiver), got %q", spot.Locator)
		}
	})

	t.Run("Match by Sender Locator", func(t *testing.T) {
		client := &Client{targets: []string{"FN31"}}
		spot, ok := matchAndCreateSpot(client, msg, now)
		if !ok {
			t.Fatal("expected spot to match target locator FN31")
		}
		if spot.Locator != "FN20" {
			t.Errorf("expected remote locator to be FN20, got %q", spot.Locator)
		}
	})
}

func TestStreamHandlerIntegration(t *testing.T) {
	// Seed the global hub history with a known message
	hub.Lock()
	hub.history = []MQTTMessage{
		{
			SC: "W1AW",
			RC: "K1JT",
			SL: "FN31",
			RL: "FN20",
			RP: -15,
			T:  time.Now().Unix(),
			B:  "20m",
			MD: "FT8",
		},
	}
	hub.Unlock()

	server := httptest.NewServer(http.HandlerFunc(streamHandler))
	defer server.Close()

	resp, err := http.Get(server.URL + "?target=W1AW")
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200 OK, got %d", resp.StatusCode)
	}
	if ctype := resp.Header.Get("Content-Type"); ctype != "text/event-stream" {
		t.Errorf("Expected Content-Type text/event-stream, got %q", ctype)
	}

	reader := bufio.NewReader(resp.Body)

	// Read the first line of the stream (should be our spot data)
	line1, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("Failed to read from stream: %v", err)
	}
	if !strings.HasPrefix(line1, "data: {") || !strings.Contains(line1, `"sender":"W1AW"`) {
		t.Errorf("Expected spot data for W1AW, got: %q", line1)
	}

	// Skip the blank line after data
	_, _ = reader.ReadString('\n')

	// Read the next line (should be the history_end event)
	line3, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("Failed to read from stream: %v", err)
	}
	if !strings.HasPrefix(line3, "event: history_end") {
		t.Errorf("Expected event: history_end, got: %q", line3)
	}
}

func TestStatsHandlerIntegration(t *testing.T) {
	// Seed the global hub history and clients
	hub.Lock()
	hub.clients = make(map[*Client]bool)
	hub.clients[&Client{}] = true
	hub.clients[&Client{}] = true

	hub.history = []MQTTMessage{
		{
			SC: "W1AW",
			RC: "K1JT",
			SL: "FN31",
			RL: "FN20",
			RP: -15,
			T:  time.Now().Unix() - 600, // ~10 minutes ago
			B:  "20m",
			MD: "FT8",
		},
	}
	hub.Unlock()

	server := httptest.NewServer(http.HandlerFunc(statsHandler))
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200 OK, got %d", resp.StatusCode)
	}
	if ctype := resp.Header.Get("Content-Type"); ctype != "application/json" {
		t.Errorf("Expected Content-Type application/json, got %q", ctype)
	}

	var stats map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		t.Fatalf("Failed to decode JSON: %v", err)
	}

	if stats["active_connections"].(float64) != 2 {
		t.Errorf("Expected 2 active connections, got %v", stats["active_connections"])
	}
	if stats["history_size"].(float64) != 1 {
		t.Errorf("Expected history size 1, got %v", stats["history_size"])
	}

	// Verify that the minutes calculation is approximately correct (~10 mins)
	historyMins := stats["history_minutes"].(float64)
	if historyMins < 9 || historyMins > 11 {
		t.Errorf("Expected history minutes ~10, got %v", historyMins)
	}
}

func withHubSnapshot(t *testing.T, fn func()) {
	t.Helper()

	hub.Lock()
	origClients := hub.clients
	origHistory := hub.history
	hub.clients = make(map[*Client]bool)
	hub.history = make([]MQTTMessage, 0)
	hub.Unlock()

	origMaxClients := maxClients
	defer func() {
		hub.Lock()
		hub.clients = origClients
		hub.history = origHistory
		hub.Unlock()
		maxClients = origMaxClients
	}()

	fn()
}

func TestMatchAndCreateSpotEdgeCases(t *testing.T) {
	now := int64(200000)

	t.Run("No targets configured", func(t *testing.T) {
		client := &Client{targets: []string{}}
		_, ok := matchAndCreateSpot(client, MQTTMessage{}, now)
		if ok {
			t.Fatal("expected no match when client has no targets")
		}
	})

	t.Run("Receiver-side match uses sender locator", func(t *testing.T) {
		client := &Client{targets: []string{"K1JT"}}
		msg := MQTTMessage{
			SC: "W1AW",
			RC: "K1JT",
			SL: "FN31",
			RL: "FN20",
			RP: -5,
			T:  now - 30,
			B:  "20m",
			MD: "FT8",
		}

		spot, ok := matchAndCreateSpot(client, msg, now)
		if !ok {
			t.Fatal("expected receiver-side match")
		}
		if spot.Locator != "FN31" {
			t.Fatalf("expected remote locator FN31 for receiver-side match, got %q", spot.Locator)
		}
	})

	t.Run("Drops match when remote locator is empty", func(t *testing.T) {
		client := &Client{targets: []string{"W1AW"}}
		msg := MQTTMessage{
			SC: "W1AW",
			RC: "K1JT",
			SL: "FN31",
			RL: "",
			RP: -10,
			T:  now - 5,
			B:  "40m",
			MD: "FT8",
		}

		_, ok := matchAndCreateSpot(client, msg, now)
		if ok {
			t.Fatal("expected spot to be dropped when remote locator is empty")
		}
	})

	t.Run("Future timestamps clamp age to zero", func(t *testing.T) {
		client := &Client{targets: []string{"W1AW"}}
		msg := MQTTMessage{
			SC: "W1AW",
			RC: "K1JT",
			SL: "FN31",
			RL: "FN20",
			RP: -12,
			T:  now + 60,
			B:  "20m",
			MD: "FT8",
		}

		spot, ok := matchAndCreateSpot(client, msg, now)
		if !ok {
			t.Fatal("expected match")
		}
		if spot.AgeSeconds != 0 {
			t.Fatalf("expected age to be clamped to 0, got %d", spot.AgeSeconds)
		}
	})
}

func TestStreamHandlerValidationAndCompatibility(t *testing.T) {
	withHubSnapshot(t, func() {
		server := httptest.NewServer(http.HandlerFunc(streamHandler))
		defer server.Close()

		t.Run("Returns 400 when target and compatibility params are missing", func(t *testing.T) {
			resp, err := http.Get(server.URL)
			if err != nil {
				t.Fatalf("Failed to make request: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", resp.StatusCode)
			}
		})

		t.Run("Accepts callsign fallback parameter for older clients", func(t *testing.T) {
			hub.Lock()
			hub.history = []MQTTMessage{{
				SC: "W1AW",
				RC: "K1JT",
				SL: "FN31",
				RL: "FN20",
				RP: -10,
				T:  time.Now().Unix(),
				B:  "20m",
				MD: "FT8",
			}}
			hub.Unlock()

			resp, err := http.Get(server.URL + "?callsign=W1AW")
			if err != nil {
				t.Fatalf("Failed to make request: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected 200, got %d", resp.StatusCode)
			}

			reader := bufio.NewReader(resp.Body)
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("Failed to read stream line: %v", err)
			}
			if !strings.HasPrefix(line, "data: {") || !strings.Contains(line, `"sender":"W1AW"`) {
				t.Fatalf("expected sender W1AW in first data event, got %q", line)
			}
		})
	})
}

func TestStreamHandlerMaxClientsCapacity(t *testing.T) {
	withHubSnapshot(t, func() {
		maxClients = 1

		hub.Lock()
		hub.clients[&Client{send: make(chan Spot, 1)}] = true
		hub.Unlock()

		req := httptest.NewRequest(http.MethodGet, "/api/stream?target=W1AW", nil)
		rec := httptest.NewRecorder()

		streamHandler(rec, req)

		res := rec.Result()
		defer res.Body.Close()

		if res.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", res.StatusCode)
		}

		if ctype := res.Header.Get("Content-Type"); ctype != "text/event-stream" {
			t.Fatalf("expected text/event-stream content type, got %q", ctype)
		}

		bodyBytes, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatalf("failed to read response body: %v", err)
		}
		body := string(bodyBytes)

		expected := "event: server_error"
		if !strings.Contains(body, expected) {
			t.Fatalf("expected %q in body, got: %s", expected, body)
		}
		if !strings.Contains(body, "Server is at capacity") {
			t.Fatalf("expected capacity message in body, got: %s", body)
		}
	})
}

func TestDxConditionsHandlerRequiresTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(dxConditionsHandler))
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestDxConditionsHandlerReturnsScoredPayload(t *testing.T) {
	origDx := dxBaseline
	defer func() {
		dxBaseline = origDx
	}()

	dxBaseline = newDxBaselineEngine("")

	now := time.Now().Unix()
	hour := utcHourOfWeek(now)
	band := normalizeBand("20m")

	for i := 0; i < 800; i++ {
		key := baselineKey(band, hour, 1, 2)
		if dxBaseline.buckets[key] == nil {
			dxBaseline.buckets[key] = &baselineBucket{Band: band, HourOfWeek: hour, DistanceTier: 1, SnrTier: 2}
		}
		dxBaseline.buckets[key].Count++
	}

	hub.Lock()
	origHistory := hub.history
	hub.history = []MQTTMessage{
		{
			SC: "W1AW",
			RC: "K1JT",
			SL: "FN31",
			RL: "FN20",
			RP: -9,
			T:  now - 30,
			B:  "20m",
			MD: "FT8",
		},
		{
			SC: "W1AW",
			RC: "N0CALL",
			SL: "FN31",
			RL: "EM10",
			RP: -6,
			T:  now - 20,
			B:  "20m",
			MD: "FT8",
		},
	}
	hub.Unlock()
	defer func() {
		hub.Lock()
		hub.history = origHistory
		hub.Unlock()
	}()

	server := httptest.NewServer(http.HandlerFunc(dxConditionsHandler))
	defer server.Close()

	resp, err := http.Get(server.URL + "?target=W1AW&minutes=15")
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var payload map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode payload: %v", err)
	}

	if payload["target"] != "W1AW" {
		t.Fatalf("expected target W1AW, got %v", payload["target"])
	}

	if _, ok := payload["overall_score"].(float64); !ok {
		t.Fatalf("expected overall_score float in response, got %T", payload["overall_score"])
	}
	if _, ok := payload["baseline_history_minutes"].(float64); !ok {
		t.Fatalf("expected baseline_history_minutes in response, got %T", payload["baseline_history_minutes"])
	}
	if _, ok := payload["baseline_event_count"].(float64); !ok {
		t.Fatalf("expected baseline_event_count in response, got %T", payload["baseline_event_count"])
	}
	if payload["cw_min_db"].(float64) != -15 {
		t.Fatalf("expected cw_min_db default -15, got %v", payload["cw_min_db"])
	}

	bands, ok := payload["bands"].([]interface{})
	if !ok {
		t.Fatalf("expected bands list, got %T", payload["bands"])
	}
	if len(bands) == 0 {
		t.Fatalf("expected at least one band condition")
	}
}

func TestDxBucketTiering(t *testing.T) {
	if got := snrTierFromDb(-19); got != 0 {
		t.Fatalf("expected snr tier 0, got %d", got)
	}
	if got := snrTierFromDb(-12); got != 1 {
		t.Fatalf("expected snr tier 1, got %d", got)
	}
	if got := snrTierFromDb(-5); got != 2 {
		t.Fatalf("expected snr tier 2, got %d", got)
	}
	if got := snrTierFromDb(3); got != 3 {
		t.Fatalf("expected snr tier 3, got %d", got)
	}

	if got := distanceTierForLocators("JO32", "JO32"); got != 0 {
		t.Fatalf("expected local distance tier 0, got %d", got)
	}
	if got := distanceTierForLocators("JO32", "FN31"); got < 3 {
		t.Fatalf("expected long-distance tier >=3, got %d", got)
	}
}

func TestDxConditionsEvaluateIncludesTrendAndSparkline(t *testing.T) {
	engine := newDxBaselineEngine("")
	now := time.Now().Unix()

	for i := 0; i < 200; i++ {
		engine.Observe(MQTTMessage{
			T:  now - int64(3600+i*20),
			SC: "W1AW",
			RC: "K1JT",
			SL: "FN31",
			RL: "JO32",
			B:  "20m",
			RP: -8,
		})
	}

	history := []MQTTMessage{}
	for i := 0; i < 30; i++ {
		history = append(history, MQTTMessage{
			T:  now - int64(i*40),
			SC: "W1AW",
			RC: "DL1ABC",
			SL: "FN31",
			RL: "JO32",
			B:  "20m",
			RP: -7,
		})
	}

	resp := engine.Evaluate("W1AW", false, 20, -15, history, now)
	if len(resp.Bands) == 0 {
		t.Fatalf("expected at least one band")
	}

	b := resp.Bands[0]
	if b.Band != "20m" {
		t.Fatalf("expected first band to be 20m, got %q", b.Band)
	}
	if b.Trend == "" {
		t.Fatalf("expected trend to be populated")
	}
	if len(b.Sparkline) != dxSparklineBins {
		t.Fatalf("expected %d sparkline points, got %d", dxSparklineBins, len(b.Sparkline))
	}
	if b.UniqueLinks <= 0 {
		t.Fatalf("expected unique link count to be populated")
	}
}

func TestDxConditionsEvaluateIncludesModeStatusAndExtendedMetrics(t *testing.T) {
	engine := newDxBaselineEngine("")
	now := time.Now().Unix()

	for i := 0; i < 300; i++ {
		engine.Observe(MQTTMessage{
			T:  now - int64(7200+i*15),
			SC: "W1AW",
			RC: "DL1ABC",
			SL: "FN31",
			RL: "JO32",
			B:  "20m",
			RP: 2,
		})
	}

	history := make([]MQTTMessage, 0, 20)
	for i := 0; i < 20; i++ {
		history = append(history, MQTTMessage{
			T:  now - int64(i*20),
			SC: "W1AW",
			RC: "DL1ABC",
			SL: "FN31",
			RL: "JO32",
			B:  "20m",
			RP: 3,
		})
	}

	resp := engine.Evaluate("W1AW", false, 20, -15, history, now)
	if len(resp.Bands) == 0 {
		t.Fatalf("expected at least one band")
	}
	b := resp.Bands[0]

	if b.Mode == "" || b.Mode == "none" {
		t.Fatalf("expected viable mode classification, got %q", b.Mode)
	}
	if b.Status == "" {
		t.Fatalf("expected status to be populated")
	}
	if b.SpotsPerMinute <= 0 {
		t.Fatalf("expected spots_per_minute > 0")
	}
	if b.P90DistanceKm <= 0 {
		t.Fatalf("expected p90_distance_km > 0")
	}
	if b.P90Snr <= 0 {
		t.Fatalf("expected p90_snr > 0")
	}
	if b.DominantDirection == "" {
		t.Fatalf("expected dominant_direction to be set")
	}
	if b.Recommendation == "" {
		t.Fatalf("expected recommendation to be set")
	}
}

func TestDxBaselinePersistenceTracksHistorySpan(t *testing.T) {
	now := time.Now().Unix()
	dir := t.TempDir()
	path := filepath.Join(dir, "dx_baseline.json")

	engine := newDxBaselineEngine(path)
	engine.Observe(MQTTMessage{
		T:  now - 7200,
		SC: "W1AW",
		RC: "K1JT",
		SL: "FN31",
		RL: "JO32",
		B:  "20m",
		RP: -9,
	})
	engine.Observe(MQTTMessage{
		T:  now - 300,
		SC: "W1AW",
		RC: "DL1ABC",
		SL: "FN31",
		RL: "JO32",
		B:  "20m",
		RP: -6,
	})

	if err := engine.Save(); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot failed: %v", err)
	}

	var snap baselineSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("unmarshal snapshot failed: %v", err)
	}
	if snap.Version < 3 {
		t.Fatalf("expected version >= 3, got %d", snap.Version)
	}
	if snap.FirstEventAt == 0 || snap.LastEventAt == 0 {
		t.Fatalf("expected first/last event timestamps in snapshot, got %d/%d", snap.FirstEventAt, snap.LastEventAt)
	}

	loaded := newDxBaselineEngine(path)
	if err := loaded.Load(); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	resp := loaded.Evaluate("W1AW", false, 20, -15, nil, now)
	want := int((snap.LastEventAt - snap.FirstEventAt) / 60)
	if resp.BaselineHistoryM != want {
		t.Fatalf("expected baseline history %d min, got %d", want, resp.BaselineHistoryM)
	}
}
