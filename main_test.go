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
	"strconv"
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
		client := &Client{qthSet: []string{"W1AW"}}
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
		client := &Client{qthSet: []string{"FN31"}}
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

	resp, err := http.Get(server.URL + "?qth=W1AW")
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
	if !strings.HasPrefix(line1, "data: {") || !strings.Contains(line1, `"locator":"FN20"`) || !strings.Contains(line1, `"band":"20m"`) {
		t.Errorf("Expected trimmed spot data for the matched square, got: %q", line1)
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
	origDxBaseline := dxBaseline
	defer func() { dxBaseline = origDxBaseline }()

	dxBaseline = newDxBaselineEngine("")
	now := time.Now().Unix()
	dxBaseline.Observe(MQTTMessage{
		T:  now - 7200,
		SC: "W1AW",
		RC: "K1JT",
		SL: "FN31",
		RL: "FN20",
		RP: -15,
		B:  "20m",
		MD: "FT8",
	})
	dxBaseline.Observe(MQTTMessage{
		T:  now - 300,
		SC: "W1AW",
		RC: "DL1ABC",
		SL: "FN31",
		RL: "JO32",
		RP: -6,
		B:  "20m",
		MD: "FT8",
	})

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
			T:  now - 600, // ~10 minutes ago
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
	if stats["history_retention_minutes"].(float64) <= 0 {
		t.Fatalf("expected history_retention_minutes to be reported, got %v", stats["history_retention_minutes"])
	}
	if got := stats["dx_baseline_event_count"].(float64); got != 2 {
		t.Fatalf("expected dx_baseline_event_count to be 2, got %v", got)
	}
	baselineHistoryMins := stats["dx_baseline_history_minutes"].(float64)
	if baselineHistoryMins < 114 || baselineHistoryMins > 116 {
		t.Fatalf("expected dx_baseline_history_minutes ~115, got %v", baselineHistoryMins)
	}
	if got := stats["dx_baseline_max_events"].(float64); got != float64(defaultDxBaselineMaxEvents) {
		t.Fatalf("expected dx_baseline_max_events=%d, got %v", defaultDxBaselineMaxEvents, got)
	}
	if _, ok := stats["sessions_total"].(float64); !ok {
		t.Fatalf("expected sessions_total in stats, got %T", stats["sessions_total"])
	}
	if _, ok := stats["session_bytes_total"].(float64); !ok {
		t.Fatalf("expected session_bytes_total in stats, got %T", stats["session_bytes_total"])
	}
	if _, ok := stats["session_bytes_avg"].(float64); !ok {
		t.Fatalf("expected session_bytes_avg in stats, got %T", stats["session_bytes_avg"])
	}
}

func TestDefaultDxBaselineMaxEventsIsOneMillion(t *testing.T) {
	if defaultDxBaselineMaxEvents != 1000000 {
		t.Fatalf("expected default dx baseline max events to be 1000000, got %d", defaultDxBaselineMaxEvents)
	}
}

func TestPruneLiveHistoryUsesConfiguredRetention(t *testing.T) {
	hub.Lock()
	origHistory := hub.history
	hub.history = []MQTTMessage{
		{T: 100, SC: "A", RC: "B", SL: "FN31", RL: "FN20", RP: -10, B: "20m", MD: "FT8"},
		{T: 2000, SC: "A", RC: "B", SL: "FN31", RL: "FN20", RP: -10, B: "20m", MD: "FT8"},
	}
	hub.Unlock()
	defer func() {
		hub.Lock()
		hub.history = origHistory
		hub.Unlock()
	}()

	pruneLiveHistory(2400, 10)

	hub.RLock()
	defer hub.RUnlock()
	if len(hub.history) != 1 {
		t.Fatalf("expected 1 retained history item, got %d", len(hub.history))
	}
	if hub.history[0].T != 2000 {
		t.Fatalf("expected the newer history item to remain, got %d", hub.history[0].T)
	}
}

// TestPruneLiveHistoryResliceInPlace verifies the in-place reslice retains the
// correct live elements (and only them) across a partial prune, including that
// a subsequent append still works after the prefix was dropped.
func TestPruneLiveHistoryResliceInPlace(t *testing.T) {
	hub.Lock()
	origHistory := hub.history
	hub.history = []MQTTMessage{
		{T: 100, SC: "old1"},
		{T: 200, SC: "old2"},
		{T: 300, SC: "old3"},
		{T: 2000, SC: "keep1"},
		{T: 2100, SC: "keep2"},
	}
	hub.Unlock()
	defer func() {
		hub.Lock()
		hub.history = origHistory
		hub.Unlock()
	}()

	// cutoff = 2400 - 600 = 1800 → drops the three T<1800 entries.
	pruneLiveHistory(2400, 10)

	hub.RLock()
	if len(hub.history) != 2 {
		t.Fatalf("expected 2 retained, got %d (%v)", len(hub.history), hub.history)
	}
	if hub.history[0].T != 2000 || hub.history[1].T != 2100 {
		t.Fatalf("retained wrong elements: %v", hub.history)
	}
	hub.RUnlock()

	// Append after the reslice must still work (the resliced header is appendable).
	hub.Lock()
	hub.history = append(hub.history, MQTTMessage{T: 2500, SC: "new"})
	hub.Unlock()

	hub.RLock()
	if len(hub.history) != 3 || hub.history[2].T != 2500 {
		t.Fatalf("post-prune append failed: %v", hub.history)
	}
	hub.RUnlock()
}

func TestBuildRawSpotInsertSQL(t *testing.T) {
	chunk := []rawSpotRow{
		{m: MQTTMessage{T: 100, RP: -8}, band: "20m", source4: "FN31", lat: 41.7, lon: -72.7, sc: "W1AW", rc: "DL1A", sl: "FN31", rl: "JO32", md: "FT8"},
		{m: MQTTMessage{T: 200, RP: -12}, band: "40m", source4: "JO32", lat: 50.1, lon: 8.0, sc: "DL2B", rc: "W1AW", sl: "JO32", rl: "FN31", md: "CW"},
	}
	q, args := buildRawSpotInsertSQL(chunk)

	const cols = 15
	if len(args) != len(chunk)*cols {
		t.Fatalf("args len = %d, want %d", len(args), len(chunk)*cols)
	}
	// Row 0 layout: T, band, sc, rc, sl, rl, md, RP, source4, lat, lon, "mqtt", "", nil, "".
	if args[0] != int64(100) || args[1] != "20m" || args[2] != "W1AW" || args[8] != "FN31" {
		t.Fatalf("row0 args mismatch: %v", args[:9])
	}
	if args[9] != 41.7 || args[10] != -72.7 {
		t.Fatalf("row0 lat/lon args mismatch: %v %v", args[9], args[10])
	}
	if args[11] != "mqtt" || args[12] != "" || args[13] != (*float64)(nil) || args[14] != "" {
		t.Fatalf("row0 trailing args mismatch: %v", args[11:15])
	}
	// Row 1 starts at offset 15.
	if args[15] != int64(200) || args[16] != "40m" {
		t.Fatalf("row1 start args mismatch: %v %v", args[15], args[16])
	}

	// Placeholder indices: row0 uses $1..$15 (geom CASE at col 10 refs $10=lat,$11=lon);
	// row1 uses $16..$30. Spot-check the geom expression and the row1 start.
	if !strings.Contains(q, "ST_SetSRID(ST_MakePoint($11, $10), 4326)") {
		t.Fatalf("row0 geom expression missing/wrong: %s", q)
	}
	if !strings.Contains(q, "WHEN $10::float8 = 0 AND $11::float8 = 0") {
		t.Fatalf("row0 geom NULL-guard missing/wrong: %s", q)
	}
	if !strings.Contains(q, "($16,$17") {
		t.Fatalf("row1 VALUES tuple missing: %s", q)
	}
	if !strings.Contains(q, "ST_SetSRID(ST_MakePoint($26, $25), 4326)") {
		t.Fatalf("row1 geom expression missing/wrong (expected $26/$25): %s", q)
	}
	// Two VALUE tuples ⇒ exactly one comma between tuples (no trailing comma).
	if strings.Count(q, "),(") != 1 {
		t.Fatalf("expected 1 tuple-separator, got %d in: %s", strings.Count(q, "),("), q)
	}
}

func TestDxBaselineEventCapIsConfigurable(t *testing.T) {
	origCap := dxBaselineMaxEvents
	dxBaselineMaxEvents = 2
	defer func() { dxBaselineMaxEvents = origCap }()

	engine := newDxBaselineEngine("")
	now := time.Now().Unix()
	for i := 0; i < 3; i++ {
		engine.Observe(MQTTMessage{
			T:  now - int64(i*60),
			SC: "W1AW",
			RC: "K1JT",
			SL: "FN31",
			RL: "FN20",
			RP: -10,
			B:  "20m",
			MD: "FT8",
		})
	}

	if got := len(engine.events); got != 2 {
		t.Fatalf("expected baseline event cap to retain 2 events, got %d", got)
	}
}

func TestDxBaselineEventCapRetainsNewestInOrder(t *testing.T) {
	origCap := dxBaselineMaxEvents
	dxBaselineMaxEvents = 2
	defer func() { dxBaselineMaxEvents = origCap }()

	engine := newDxBaselineEngine("")
	base := time.Now().Unix()
	for i := 0; i < 3; i++ {
		engine.Observe(MQTTMessage{
			T:  base + int64(i),
			SC: "W1AW",
			RC: "K1JT",
			SL: "FN31",
			RL: "FN20",
			RP: -8,
			B:  "20m",
			MD: "FT8",
		})
	}

	engine.mu.RLock()
	events := engine.snapshotEventsLocked()
	engine.mu.RUnlock()

	if len(events) != 2 {
		t.Fatalf("expected 2 retained events, got %d", len(events))
	}
	if events[0].T != base+1 || events[1].T != base+2 {
		t.Fatalf("expected newest ordered events [%d,%d], got [%d,%d]", base+1, base+2, events[0].T, events[1].T)
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
		client := &Client{qthSet: []string{}}
		_, ok := matchAndCreateSpot(client, MQTTMessage{}, now)
		if ok {
			t.Fatal("expected no match when client has no targets")
		}
	})

	t.Run("Receiver-side match uses sender locator", func(t *testing.T) {
		client := &Client{qthSet: []string{"K1JT"}}
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
		client := &Client{qthSet: []string{"W1AW"}}
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
		client := &Client{qthSet: []string{"W1AW"}}
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
			if !strings.HasPrefix(line, "data: {") || !strings.Contains(line, `"locator":"FN20"`) {
				t.Fatalf("expected trimmed stream data with locator FN20, got %q", line)
			}
		})
	})
}

func TestSquareDetailsHandlerAggregatesHoverDetails(t *testing.T) {
	withHubSnapshot(t, func() {
		now := time.Now().Unix()
		hub.Lock()
		hub.history = []MQTTMessage{
			{
				SC: "W1AW",
				RC: "K1JT",
				SL: "FN31",
				RL: "FN20",
				RP: -8,
				T:  now - 30,
				B:  "20m",
				MD: "FT8",
			},
			{
				SC: "W1AW",
				RC: "N0CALL",
				SL: "FN31",
				RL: "FN20",
				RP: -4,
				T:  now - 20,
				B:  "20m",
				MD: "FT8",
			},
			{
				SC: "W1AW",
				RC: "DL1ABC",
				SL: "FN31",
				RL: "FN20",
				RP: -2,
				T:  now - 10,
				B:  "40m",
				MD: "FT8",
			},
		}
		hub.Unlock()

		server := httptest.NewServer(http.HandlerFunc(squareDetailsHandler))
		defer server.Close()

		resp, err := http.Get(server.URL + "?qth=W1AW&locator=FN20&minutes=15&min_snr_mode=cw&cw_min_db=-15&selected_band=all&enabled_bands=20m,40m")
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		var payload squareDetailsResponse
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode payload: %v", err)
		}

		if payload.Locator != "FN20" {
			t.Fatalf("expected locator FN20, got %q", payload.Locator)
		}
		if payload.Count != 3 {
			t.Fatalf("expected 3 matching reports, got %d", payload.Count)
		}
		if payload.BestBand != "20m" {
			t.Fatalf("expected best band 20m, got %q", payload.BestBand)
		}
		if len(payload.TopReports) == 0 {
			t.Fatal("expected top reports in payload")
		}
	})
}

func TestStreamHandlerMaxClientsCapacity(t *testing.T) {
	withHubSnapshot(t, func() {
		maxClients = 1

		hub.Lock()
		hub.clients[&Client{send: make(chan Spot, 1)}] = true
		hub.Unlock()

		req := httptest.NewRequest(http.MethodGet, "/api/stream?qth=W1AW", nil)
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

func TestCaptureSnapshotHandlerRequiresTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(captureSnapshotHandler))
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

func TestCaptureSnapshotHandlerReturnsFilteredSpots(t *testing.T) {
	withHubSnapshot(t, func() {
		now := time.Now().Unix()
		hub.Lock()
		hub.history = []MQTTMessage{
			{
				SC: "W1AW",
				RC: "K1JT",
				SL: "FN31",
				RL: "FN20",
				RP: -2,
				T:  now - 20,
				B:  "20m",
				MD: "FT8",
			},
			{
				SC: "W1AW",
				RC: "DL1ABC",
				SL: "FN31",
				RL: "JO32",
				RP: -12,
				T:  now - 40,
				B:  "40m",
				MD: "FT8",
			},
			{
				SC: "W1AW",
				RC: "N0CALL",
				SL: "FN31",
				RL: "EM10",
				RP: -4,
				T:  now - 50,
				B:  "20m",
				MD: "DXCLUSTER",
			},
		}
		hub.Unlock()

		server := httptest.NewServer(http.HandlerFunc(captureSnapshotHandler))
		defer server.Close()

		url := server.URL + "?qth=W1AW&snapshot_at=" + strconv.FormatInt(now, 10) + "&minutes=15&min_snr_mode=ssb&ssb_min_db=-6&selected_band=20m&enabled_bands=20m&include_dxcluster=false"
		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		var payload captureSnapshotResponse
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode payload: %v", err)
		}

		if payload.QTH != "W1AW" {
			t.Fatalf("expected target W1AW, got %q", payload.QTH)
		}
		if payload.Count != 1 {
			t.Fatalf("expected 1 filtered spot, got %d", payload.Count)
		}
		if len(payload.Spots) != 1 {
			t.Fatalf("expected exactly one spot in payload, got %d", len(payload.Spots))
		}
		if payload.Spots[0].Locator != "FN20" {
			t.Fatalf("expected retained spot locator FN20, got %q", payload.Spots[0].Locator)
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
	hour := utcSlotOfDay(now)
	band := normalizeBand("20m")

	for i := 0; i < 800; i++ {
		key := baselineKey(band, hour, 1, 2)
		if dxBaseline.buckets[key] == nil {
			dxBaseline.buckets[key] = &baselineBucket{Band: band, SlotOfDay: hour, DistanceTier: 1, SnrTier: 2}
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

	resp, err := http.Get(server.URL + "?qth=W1AW&minutes=15")
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

	if payload["qth"] != "W1AW" {
		t.Fatalf("expected qth W1AW, got %v", payload["qth"])
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

func TestSumPairs(t *testing.T) {
	if got := sumPairs(nil); got != 0 {
		t.Fatalf("nil → 0, got %d", got)
	}
	pairs := []baselinePair{{Count: 10}, {Count: 0}, {Count: 5}, {Count: -3}}
	if got := sumPairs(pairs); got != 12 {
		t.Fatalf("sum = %d, want 12", got)
	}
}

func TestPairsForBandSlot(t *testing.T) {
	target := map[bandSlotKey][]baselinePair{
		{Band: "20m", Slot: 10}: {{DistanceTier: 1, SnrTier: 2, Count: 50}},
	}
	global := map[bandSlotKey][]baselinePair{
		{Band: "20m", Slot: 10}: {{DistanceTier: 0, SnrTier: 0, Count: 7}}, // shadowed by target
		{Band: "40m", Slot: 3}:  {{DistanceTier: 2, SnrTier: 1, Count: 9}},
	}

	// Cluster wins when present → used=true, cluster pairs.
	p, used := pairsForBandSlot(target, global, "20m", 10)
	if !used || len(p) != 1 || p[0].Count != 50 {
		t.Fatalf("20m/10: expected cluster(50) used=true, got %v used=%v", p, used)
	}
	// No cluster row → global fallback, used=false.
	p, used = pairsForBandSlot(target, global, "40m", 3)
	if used || len(p) != 1 || p[0].Count != 9 {
		t.Fatalf("40m/3: expected global(9) used=false, got %v used=%v", p, used)
	}
	// Neither → nil, false.
	p, used = pairsForBandSlot(target, global, "80m", 0)
	if used || p != nil {
		t.Fatalf("80m/0: expected nil/false, got %v used=%v", p, used)
	}
	// Nil cluster index → global fallback, used=false.
	p, used = pairsForBandSlot(nil, global, "40m", 3)
	if used || len(p) != 1 || p[0].Count != 9 {
		t.Fatalf("nil cluster: expected global(9) used=false, got %v used=%v", p, used)
	}
}

func TestQuantilesFromPairs(t *testing.T) {
	// Empty / below-support → not ok.
	if _, _, ok := quantilesFromPairs(nil); ok {
		t.Fatalf("nil pairs → ok=true, want false")
	}
	if _, _, ok := quantilesFromPairs([]baselinePair{{DistanceTier: 0, SnrTier: 0, Count: 100}}); ok {
		t.Fatalf("totalWeight < dxMinBaselineQuantileSupport → ok=true, want false")
	}

	// 4 pairs, equal weight 100 (total 400 ≥ 200). With maxCount=100 every
	// activityNorm=1, so score = (0.45*tier/4 + 0.35 + 0.20*snr/3)*100.
	// tiers 0..3, snr 0 → scores 35, 46.25, 57.5, 68.75. q25Target=100, q75Target=300
	// → q25 = score@cum≥100 = 35; q75 = score@cum≥300 = 57.5.
	// Input deliberately reverse-sorted to exercise the insertion sort.
	pairs := []baselinePair{
		{DistanceTier: 3, SnrTier: 0, Count: 100},
		{DistanceTier: 2, SnrTier: 0, Count: 100},
		{DistanceTier: 1, SnrTier: 0, Count: 100},
		{DistanceTier: 0, SnrTier: 0, Count: 100},
	}
	q25, q75, ok := quantilesFromPairs(pairs)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if math.Abs(q25-35.0) > 1e-6 {
		t.Fatalf("q25 = %f, want 35", q25)
	}
	if math.Abs(q75-57.5) > 1e-6 {
		t.Fatalf("q75 = %f, want 57.5", q75)
	}
}

func TestNormalizeSeriesTo100(t *testing.T) {
	// Empty / all-zero / negative-max series → all zeros (no division by zero).
	if got := normalizeSeriesTo100(nil); len(got) != 0 {
		t.Fatalf("nil input → empty, got %v", got)
	}
	for _, in := range [][]float64{{0, 0, 0}, {-1, -2, -3}} {
		got := normalizeSeriesTo100(in)
		for i, v := range got {
			if v != 0 {
				t.Fatalf("%v → expected all zeros, got %v (idx %d = %f)", in, got, i, v)
			}
		}
	}

	// Max value scales to exactly 100; others proportionally; input unmutated.
	in := []float64{1, 4, 2, 0}
	got := normalizeSeriesTo100(in)
	want := []float64{25, 100, 50, 0}
	if len(got) != len(want) {
		t.Fatalf("length changed: %d → %d", len(in), len(got))
	}
	for i, w := range want {
		if math.Abs(got[i]-w) > 1e-6 {
			t.Fatalf("normalizeSeriesTo100(%v)[%d] = %f, want %f", in, i, got[i], w)
		}
	}
	if in[1] != 4 {
		t.Fatalf("input slice was mutated: %v", in)
	}
}

func TestBuildBandActivityByBin(t *testing.T) {
	// 20-min window → 12 bins of 100s each (binMinutes = 100/60).
	const now int64 = 1_000_000
	const minutes = 20
	const binSec = int64(minutes * 60 / 12)
	binMinutes := float64(binSec) / 60.0

	evt := func(t int64, band, sc, rc, sl, rl string, rp int) dxObservedEvent {
		return dxObservedEvent{T: t, B: band, SC: sc, RC: rc, SL: sl, RL: rl, RP: rp}
	}
	events := []dxObservedEvent{
		// Three reports in the newest bin (idx 11).
		evt(now-10, "20m", "W1AW", "DL1ABC", "FN31", "JO32", -8),
		evt(now-20, "20m", "W1AW", "DL2XYZ", "FN31", "JO33", -8),
		evt(now-30, "20m", "W1AW", "DL3ZZZ", "FN31", "JO42", -8),
		// One report in the oldest bin (idx 0).
		evt(now-1190, "20m", "W1AW", "DL4QQQ", "FN31", "JO32", -8),
		// Out of window (older than 20 min): ignored.
		evt(now-2000, "20m", "W1AW", "DL5OLD", "FN31", "JO32", -8),
		// Different band: ignored.
		evt(now-10, "40m", "W1AW", "DL6BND", "FN31", "JO32", -8),
		// Does not match target: ignored.
		evt(now-10, "20m", "K9ZZZ", "DL7NOM", "FN20", "JO32", -8),
		// Below CW SNR gate (-15): ignored.
		evt(now-10, "20m", "W1AW", "DL8SNR", "FN31", "JO32", -25),
	}

	got := buildBandActivityByBin(events, []string{"W1AW"}, "20m", -15, minutes, now)
	if len(got) != 12 {
		t.Fatalf("expected 12 bins, got %d", len(got))
	}
	wantNewest := 3.0 / binMinutes
	wantOldest := 1.0 / binMinutes
	if math.Abs(got[11]-wantNewest) > 1e-6 {
		t.Fatalf("newest bin: want %.4f, got %.4f", wantNewest, got[11])
	}
	if math.Abs(got[0]-wantOldest) > 1e-6 {
		t.Fatalf("oldest bin: want %.4f, got %.4f", wantOldest, got[0])
	}
	for i, v := range got {
		if i == 0 || i == 11 {
			continue
		}
		if v != 0 {
			t.Fatalf("bin %d: expected 0, got %f", i, v)
		}
	}

	// Empty events → all-zero, full-length series.
	empty := buildBandActivityByBin(nil, []string{"W1AW"}, "20m", -15, minutes, now)
	if len(empty) != 12 || empty[0] != 0 {
		t.Fatalf("expected 12 zero bins for empty events, got %v", empty)
	}

	// minutes<=0 → all-zero series (no divide-by-zero).
	zero := buildBandActivityByBin(events, []string{"W1AW"}, "20m", -15, 0, now)
	if len(zero) != 12 || zero[0] != 0 {
		t.Fatalf("expected 12 zero bins for minutes<=0, got %v", zero)
	}

	// Window scales with minutes: at 120 min the same recent events still land
	// in the newest bin, but an event ~1h old is now in-window (would be ignored
	// at 20 min).
	wide := buildBandActivityByBin(events, []string{"W1AW"}, "20m", -15, 120, now)
	if len(wide) != 12 {
		t.Fatalf("expected 12 bins at 120 min, got %d", len(wide))
	}
	// The 1h-old event (now-2000 ~ 33 min) is in-window at 120 min and matches:
	// it must land somewhere non-zero, so the 120-min series has more non-zero
	// bins than the 20-min one.
	nonZero20 := 0
	for _, v := range got {
		if v > 0 {
			nonZero20++
		}
	}
	nonZero120 := 0
	for _, v := range wide {
		if v > 0 {
			nonZero120++
		}
	}
	if nonZero120 <= nonZero20 {
		t.Fatalf("expected wider window to expose more bins: 120m=%d 20m=%d", nonZero120, nonZero20)
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

func TestServerHelperFunctions(t *testing.T) {
	t.Run("resolveQTHQuery honors compatibility params and surroundings", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/stream?callsign=w1aw&surroundings=true", nil)
		target, surroundings := resolveQTHQuery(req)
		if target != "W1AW" {
			t.Fatalf("expected W1AW target, got %q", target)
		}
		if !surroundings {
			t.Fatal("expected surroundings=true")
		}
	})

	t.Run("parseEnabledBands and bandAllowed normalize input", func(t *testing.T) {
		enabled := parseEnabledBands("20m, 40M ,,6m")
		if len(enabled) != 3 {
			t.Fatalf("expected 3 enabled bands, got %d", len(enabled))
		}
		if !bandAllowed("20m", "all", enabled) {
			t.Fatal("expected 20m to be allowed")
		}
		if bandAllowed("15m", "all", enabled) {
			t.Fatal("expected 15m to be filtered out by enabled bands")
		}
		if bandAllowed("40m", "20m", enabled) {
			t.Fatal("expected selectedBand to override and reject 40m")
		}
	})

	t.Run("toStreamSpot trims spot payload for SSE", func(t *testing.T) {
		spot := Spot{Lat: 1.2, Lng: 3.4, SNR: -7, AgeSeconds: 30, Locator: "JO32", Band: "20m"}
		stream := toStreamSpot(spot)
		if stream.Locator != "JO32" || stream.Band != "20m" || stream.SNR != -7 {
			t.Fatalf("unexpected stream spot payload: %+v", stream)
		}
	})

	t.Run("toStreamSpot includes callsigns only for dxcluster spots", func(t *testing.T) {
		// The DX cluster marker tooltip needs the DX/spotter callsigns; regular
		// spots stay trimmed (senders are not needed by any client-side render).
		dxc := toStreamSpot(Spot{SourceType: "dxcluster", Sender: "DL1ABC", Receiver: "JA1XYZ"})
		if dxc.Sender != "DL1ABC" || dxc.Receiver != "JA1XYZ" {
			t.Fatalf("expected dxcluster callsigns on the wire, got %+v", dxc)
		}
		regular := toStreamSpot(Spot{Sender: "DL1ABC", Receiver: "JA1XYZ"})
		if regular.Sender != "" || regular.Receiver != "" {
			t.Fatalf("expected regular spot to stay trimmed, got %+v", regular)
		}
	})

	t.Run("noCache adds cache busting headers", func(t *testing.T) {
		h := noCache(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/static/app.js", nil)
		h.ServeHTTP(rec, req)
		res := rec.Result()
		defer res.Body.Close()

		if got := res.Header.Get("Cache-Control"); got != "no-cache, no-store, must-revalidate" {
			t.Fatalf("unexpected Cache-Control: %q", got)
		}
		if got := res.Header.Get("Pragma"); got != "no-cache" {
			t.Fatalf("unexpected Pragma: %q", got)
		}
		if got := res.Header.Get("Expires"); got != "0" {
			t.Fatalf("unexpected Expires: %q", got)
		}
	})
}

func TestBuildSquareDetailsResponseEmptyAndFiltered(t *testing.T) {
	now := time.Now().Unix()
	history := []MQTTMessage{
		{T: now - 30, SC: "W1AW", RC: "DL1ABC", SL: "FN31", RL: "JO32", RP: -12, B: "20m", MD: "FT8"},
		{T: now - 20, SC: "W1AW", RC: "DL1XYZ", SL: "FN31", RL: "JO33", RP: -4, B: "40m", MD: "FT8"},
	}

	t.Run("returns empty payload when target missing", func(t *testing.T) {
		resp := buildSquareDetailsResponse("", false, "JO32", 15, "none", 0, -15, "all", nil, history, now)
		if resp.Count != 0 || resp.BestBand != "" || len(resp.TopReports) != 0 {
			t.Fatalf("expected empty response, got %+v", resp)
		}
	})

	t.Run("popup stats use all square spots regardless of current band and snr filters", func(t *testing.T) {
		resp := buildSquareDetailsResponse("W1AW", false, "JO32", 15, "ssb", -10, -15, "20m", parseEnabledBands("20m"), history, now)
		if resp.Count != 1 || resp.BestBand != "20m" || resp.MinSNR != -12 || resp.MaxSNR != -12 {
			t.Fatalf("expected square popup stats to include the JO32 spot despite active filters, got %+v", resp)
		}

		resp = buildSquareDetailsResponse("W1AW", false, "JO33", 15, "none", 0, -15, "40m", parseEnabledBands("40m"), history, now)
		if resp.Count != 1 || resp.BestBand != "40m" {
			t.Fatalf("expected JO33 popup stats to reflect its full square contents, got %+v", resp)
		}
	})
}

// TestPropIntelIntegration verifies the U6 integration path:
//   - /api/prop_intel returns the documented response shape (bands, regions, cells).
//   - At least one cell has p_open > 0 when the history window has live spots.
//   - /api/stats reports prop_intel.requests == N after N calls.
//   - When a surge triggers a push (mocked sendFunc), /api/stats reports
//     push.surges_detected >= 1 and push.push_sent >= 1.
//
// The test mirrors the TestStreamHandlerIntegration / TestStatsHandlerIntegration
// pattern: seed hub.history, spin up httptest.NewServer with the handler, make
// real HTTP requests, decode JSON, assert on the shape.
func TestPropIntelIntegration(t *testing.T) {
	// Isolate global state: hub history, prop_intel accounting, push store,
	// and the push accounting counters. Restore on exit.
	hub.Lock()
	origHistory := hub.history
	origClients := hub.clients
	hub.clients = make(map[*Client]bool)
	hub.Unlock()
	defer func() {
		hub.Lock()
		hub.history = origHistory
		hub.clients = origClients
		hub.Unlock()
	}()

	origPropIntelReqs := propIntelAccounting.requests.Load()
	origPropIntelErrs := propIntelAccounting.errors.Load()
	origPropIntelSurges := propIntelAccounting.surgesDetected.Load()
	origPushSurges := pushAccounting.surgesDetected.Load()
	origPushSent := pushAccounting.pushSent.Load()
	origPushErrs := pushAccounting.pushErrors.Load()
	defer func() {
		propIntelAccounting.requests.Store(origPropIntelReqs)
		propIntelAccounting.errors.Store(origPropIntelErrs)
		propIntelAccounting.surgesDetected.Store(origPropIntelSurges)
		pushAccounting.surgesDetected.Store(origPushSurges)
		pushAccounting.pushSent.Store(origPushSent)
		pushAccounting.pushErrors.Store(origPushErrs)
	}()

	resetPushStoreForTest()
	defer resetPushStoreForTest()
	mock := &mockPushSend{}
	newTestPushStore(t, mock)
	defer func() {
		pushStore.configure("", "", "", false)
	}()

	// Seed hub.history with a dense burst of 20m spots into the Caribbean
	// region (CAR) from a JO32 operator. The surge detector compares the
	// live 15-min rate against the trailing 6h baseline; we craft the
	// baseline to be quiet (a few low-rate sub-windows) and the live
	// window to be loud so a surge is flagged.
	now := time.Now().Unix()
	const liveSenders = 12
	history := make([]MQTTMessage, 0, 200)
	// Live nowcast window: 12 unique senders in the last 5 minutes, all
	// 20m → CAR (remote locator in a Caribbean grid square, e.g. FK88).
	// The remote locator must be a valid 4-char Maidenhead (isLocator
	// requires the first 4 chars to be AA00-form). We cycle the 4th
	// char through digits 0-9 then reuse, and the receiver callsign
	// stays unique via a letter suffix so dedup counts 12 senders.
	rlChar := func(i int) byte {
		d := i % 10
		return byte('0' + d)
	}
	for i := 0; i < liveSenders; i++ {
		history = append(history, MQTTMessage{
			T:  now - int64(60+i*5),
			SC: "DL1AAA",
			RC: "FK8" + string(rlChar(i)) + string(rune('A'+i)),
			SL: "JO32",
			RL: "FK8" + string(rlChar(i)) + "A",
			RP: -10,
			B:  "20m",
			MD: "FT8",
		})
	}
	// Trailing 6h baseline: a handful of low-activity 15-min sub-windows
	// with a single unique sender each, so the baseline mean is low and
	// the stddev is nonzero (otherwise the surge guard suppresses the
	// z-score). Spread them across the 6h window excluding the live 15 min.
	for subIdx := 0; subIdx < 12; subIdx++ {
		t0 := now - int64(6*3600) + int64(subIdx*30*60)
		if t0 > now-int64(15*60) {
			t0 = now - int64(6*3600)
		}
		history = append(history, MQTTMessage{
			T:  t0,
			SC: "DL1AAA",
			RC: "FK80XY",
			SL: "JO32",
			RL: "FK80",
			RP: -15,
			B:  "20m",
			MD: "FT8",
		})
	}
	// Pad a couple of zero-activity sub-windows by adding nothing for them;
	// the memory-fallback baseline treats missing sub-windows as rate 0,
	// which both lowers the mean and adds variance (helps the stddev > 0
	// guard).
	hub.Lock()
	hub.history = history
	hub.Unlock()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/prop_intel", propIntelHandler)
	mux.HandleFunc("/api/stats", statsHandler)
	mux.HandleFunc("/api/push/subscribe", pushSubscribeHandler)
	mux.HandleFunc("/api/push/unsubscribe", pushUnsubscribeHandler)
	mux.HandleFunc("/api/push/vapid-public-key", pushVAPIDPublicKeyHandler)
	server := httptest.NewServer(mux)
	defer server.Close()

	// Subscribe to 20m:CAR surges so the mocked sendFunc will fire.
	subBody := `{"endpoint":"https://fcm.googleapis.com/fcm/inttest","keys":{"auth":"auth-key","p256dh":"p256dh-key"},"qth":"JO32","preferences":{"20m:CAR":true}}`
	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/push/subscribe", strings.NewReader(subBody))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("subscribe status = %d, want 200", resp.StatusCode)
	}

	// Make 3 calls to /api/prop_intel. The engine fans out push
	// notifications in a goroutine; we issue the requests serially and
	// then poll the mock until at least one push lands (the goroutine
	// schedule is non-deterministic).
	for i := 0; i < 3; i++ {
		resp, err := http.Get(server.URL + "/api/prop_intel?qth=JO32&minutes=15")
		if err != nil {
			t.Fatalf("prop_intel request %d: %v", i, err)
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("prop_intel request %d status = %d, body=%s", i, resp.StatusCode, body)
		}
		var data struct {
			QTH   string   `json:"qth"`
			Bands []string `json:"bands"`
			Cells []struct {
				Band   string  `json:"band"`
				Region string  `json:"region"`
				POpen  float64 `json:"p_open"`
				Surge  *struct {
					ZScore float64 `json:"z_score"`
					Label  string  `json:"label"`
				} `json:"surge,omitempty"`
			} `json:"cells"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			resp.Body.Close()
			t.Fatalf("prop_intel decode %d: %v", i, err)
		}
		resp.Body.Close()

		if i == 0 {
			// Verify response shape on the first call.
			if data.QTH == "" {
				t.Errorf("prop_intel response qth empty")
			}
			if len(data.Bands) == 0 {
				t.Errorf("prop_intel response bands empty; expected at least 20m")
			}
			// At least one cell must have p_open > 0 (the live window
			// had 12 senders).
			anyPOpen := false
			for _, c := range data.Cells {
				if c.POpen > 0 {
					anyPOpen = true
					break
				}
			}
			if !anyPOpen {
				t.Errorf("prop_intel response: no cell with p_open > 0 (cells=%d)", len(data.Cells))
			}
		}
	}

	// Poll the mock push send for up to 2s — the surge fan-out runs in a
	// goroutine and may land after the HTTP response returns.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && mock.count() < 1 {
		time.Sleep(20 * time.Millisecond)
	}
	if got := mock.count(); got < 1 {
		t.Fatalf("expected at least 1 push sent after surge, got %d (surge may not have fired)", got)
	}

	// Verify /api/stats includes the prop_intel and push blocks.
	statsResp, err := http.Get(server.URL + "/api/stats")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	defer statsResp.Body.Close()
	if statsResp.StatusCode != http.StatusOK {
		t.Fatalf("stats status = %d, want 200", statsResp.StatusCode)
	}
	var stats struct {
		PropIntel *struct {
			Requests       int64 `json:"requests"`
			Errors         int64 `json:"errors"`
			SurgesDetected int64 `json:"surges_detected"`
		} `json:"prop_intel"`
		Push *struct {
			SurgesDetected int64 `json:"surges_detected"`
			PushSent       int64 `json:"push_sent"`
			PushErrors     int64 `json:"push_errors"`
		} `json:"push"`
	}
	if err := json.NewDecoder(statsResp.Body).Decode(&stats); err != nil {
		t.Fatalf("stats decode: %v", err)
	}
	if stats.PropIntel == nil {
		t.Fatalf("stats: prop_intel block missing")
	}
	if stats.Push == nil {
		t.Fatalf("stats: push block missing")
	}
	// 3 calls to /api/prop_intel → requests delta == 3.
	gotReqs := stats.PropIntel.Requests - origPropIntelReqs
	if gotReqs != 3 {
		t.Errorf("stats prop_intel.requests delta = %d, want 3", gotReqs)
	}
	// No 400s in this test → errors delta == 0.
	gotErrs := stats.PropIntel.Errors - origPropIntelErrs
	if gotErrs != 0 {
		t.Errorf("stats prop_intel.errors delta = %d, want 0", gotErrs)
	}
	// The live window was crafted to surge 20m:CAR, so at least one
	// surge should have been detected across the 3 calls.
	gotSurges := stats.PropIntel.SurgesDetected - origPropIntelSurges
	if gotSurges < 1 {
		t.Errorf("stats prop_intel.surges_detected delta = %d, want >= 1", gotSurges)
	}
	// Push accounting: surges_detected >= 1, push_sent >= 1.
	gotPushSurges := stats.Push.SurgesDetected - origPushSurges
	if gotPushSurges < 1 {
		t.Errorf("stats push.surges_detected delta = %d, want >= 1", gotPushSurges)
	}
	gotPushSent := stats.Push.PushSent - origPushSent
	if gotPushSent < 1 {
		t.Errorf("stats push.push_sent delta = %d, want >= 1", gotPushSent)
	}
}

// TestPropIntelStatsAccounting verifies the U6 stats-accounting scenario in
// isolation: after 3 calls to /api/prop_intel, /api/stats reports
// prop_intel.requests == 3 (delta from the pre-call snapshot). It does not
// depend on a surge firing, so it is robust to the goroutine timing that
// TestPropIntelIntegration exercises.
func TestPropIntelStatsAccounting(t *testing.T) {
	hub.Lock()
	origHistory := hub.history
	origClients := hub.clients
	hub.clients = make(map[*Client]bool)
	hub.history = []MQTTMessage{}
	hub.Unlock()
	defer func() {
		hub.Lock()
		hub.history = origHistory
		hub.clients = origClients
		hub.Unlock()
	}()

	origReqs := propIntelAccounting.requests.Load()
	origErrs := propIntelAccounting.errors.Load()
	defer func() {
		propIntelAccounting.requests.Store(origReqs)
		propIntelAccounting.errors.Store(origErrs)
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/prop_intel", propIntelHandler)
	mux.HandleFunc("/api/stats", statsHandler)
	server := httptest.NewServer(mux)
	defer server.Close()

	for i := 0; i < 3; i++ {
		resp, err := http.Get(server.URL + "/api/prop_intel?qth=JO32&minutes=15")
		if err != nil {
			t.Fatalf("prop_intel request %d: %v", i, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	statsResp, err := http.Get(server.URL + "/api/stats")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	defer statsResp.Body.Close()
	var stats struct {
		PropIntel *struct {
			Requests int64 `json:"requests"`
			Errors   int64 `json:"errors"`
		} `json:"prop_intel"`
	}
	if err := json.NewDecoder(statsResp.Body).Decode(&stats); err != nil {
		t.Fatalf("stats decode: %v", err)
	}
	if stats.PropIntel == nil {
		t.Fatalf("stats: prop_intel block missing")
	}
	if got := stats.PropIntel.Requests - origReqs; got != 3 {
		t.Errorf("stats prop_intel.requests delta = %d, want 3", got)
	}
	if got := stats.PropIntel.Errors - origErrs; got != 0 {
		t.Errorf("stats prop_intel.errors delta = %d, want 0", got)
	}
}
