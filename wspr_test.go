package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBandFromWSPR(t *testing.T) {
	cases := []struct {
		band int
		want string
	}{
		{1, "160m"},
		{3, "80m"},
		{5, "60m"},
		{7, "40m"},
		{10, "30m"},
		{14, "20m"},
		{18, "17m"},
		{21, "15m"},
		{24, "12m"},
		{28, "10m"},
		{50, "6m"},
		{70, "4m"},
		{144, "2m"},
		{0, ""},   // MF
		{-1, ""},  // LF
		{432, ""}, // 70cm
		{1296, ""}, // 23cm
	}
	for _, c := range cases {
		if got := bandFromWSPR(c.band); got != c.want {
			t.Errorf("bandFromWSPR(%d) = %q, want %q", c.band, got, c.want)
		}
	}
}

func TestParseWSPRTime(t *testing.T) {
	// 2026-08-08 21:26:00 UTC
	ts, err := parseWSPRTime("2026-08-08 21:26:00")
	if err != nil {
		t.Fatalf("parseWSPRTime: %v", err)
	}
	if ts != 1786224360 {
		t.Errorf("parseWSPRTime = %d, want 1786224360", ts)
	}
	if _, err := parseWSPRTime("not a time"); err == nil {
		t.Error("parseWSPRTime should error on garbage")
	}
}

func TestIsWSPRSpotUsableForLive(t *testing.T) {
	// Valid band + valid receiver locator → usable.
	if !isWSPRSpotUsableForLive(MQTTMessage{B: "20m", RL: "JO31"}) {
		t.Error("20m + JO31 should be usable")
	}
	// Missing band → not usable.
	if isWSPRSpotUsableForLive(MQTTMessage{B: "", RL: "JO31"}) {
		t.Error("empty band should not be usable")
	}
	// Missing/invalid receiver locator → not usable.
	if isWSPRSpotUsableForLive(MQTTMessage{B: "20m", RL: ""}) {
		t.Error("empty receiver locator should not be usable")
	}
	if isWSPRSpotUsableForLive(MQTTMessage{B: "20m", RL: "W1AW"}) {
		t.Error("callsign as receiver locator should not be usable")
	}
}

func TestIsNonConditionsModeWSPR(t *testing.T) {
	// WSPR must be excluded from the FT8-calibrated conditions accumulator.
	if !isNonConditionsMode("WSPR") {
		t.Error("isNonConditionsMode(WSPR) = false, want true (excluded from FT8 baseline)")
	}
	if !isNonConditionsMode("wspr") {
		t.Error("isNonConditionsMode(wspr) = false, want true (case-insensitive)")
	}
}

func TestHandleWSPRSpotTXPower(t *testing.T) {
	// U1: TXPower from wsprSpot.Power must land on the MQTTMessage so the
	// nowcast can compute SSB/CW viability from SNR+Power. We verify via the
	// live broadcast path: handleWSPRSpot appends to hub.history, where we
	// can read TXPower back.
	savedClients := hub.clients
	savedHistory := hub.history
	defer func() {
		hub.Lock()
		hub.clients = savedClients
		hub.history = savedHistory
		hub.Unlock()
	}()
	hub.Lock()
	hub.clients = map[*Client]bool{}
	hub.history = nil
	hub.Unlock()

	cfg := wsprConfig{Enabled: true, Endpoint: "http://localhost", PollSeconds: 60, Verbose: false}
	now := int64(1786224360)

	// Happy path: Power 20W = 43 dBm → TXPower 43.
	handleWSPRSpot(wsprSpot{
		Time: "2026-08-08 21:26:00", Band: 14, RxSign: "DL1ABC", RxLoc: "JO31",
		TxSign: "KF5XYZ", TxLoc: "EM12", Distance: 8000, Frequency: 14097000,
		Power: 43, SNR: 5,
	}, now, cfg)
	hub.RLock()
	if len(hub.history) != 1 {
		hub.RUnlock()
		t.Fatalf("expected 1 spot in hub.history, got %d", len(hub.history))
	}
	if got := hub.history[0].TXPower; got != 43 {
		t.Errorf("TXPower = %d, want 43 (20W in dBm)", got)
	}
	if got := hub.history[0].Source; got != "wspr" {
		t.Errorf("Source = %q, want wspr", got)
	}
	hub.RUnlock()

	// Edge case: Power 0 (missing) → TXPower 0.
	hub.Lock()
	hub.history = nil
	hub.Unlock()
	handleWSPRSpot(wsprSpot{
		Time: "2026-08-08 21:26:00", Band: 14, RxSign: "DL1ABC", RxLoc: "JO31",
		TxSign: "KF5XYZ", TxLoc: "EM12", Distance: 8000, Frequency: 14097000,
		Power: 0, SNR: 2,
	}, now, cfg)
	hub.RLock()
	if len(hub.history) != 1 {
		hub.RUnlock()
		t.Fatalf("expected 1 spot after Power=0 case, got %d", len(hub.history))
	}
	if got := hub.history[0].TXPower; got != 0 {
		t.Errorf("TXPower = %d, want 0 for missing power", got)
	}
	hub.RUnlock()
}

// --- U3: WSPR HTTP poller (mock wspr.live via httptest) --------------------

// isolateWSPRPoll saves/restores the WSPR accounting counters and isolates the
// hub for a poller test.
func isolateWSPRPoll(t *testing.T) (before [7]int64) {
	t.Helper()
	hub.Lock()
	savedClients := hub.clients
	savedHistory := hub.history
	hub.clients = make(map[*Client]bool)
	hub.history = nil
	hub.Unlock()
	savedBaseline := dxBaseline
	dxBaseline = nil
	t.Cleanup(func() {
		hub.Lock()
		hub.clients = savedClients
		hub.history = savedHistory
		hub.Unlock()
		dxBaseline = savedBaseline
	})
	a, b, c, d, e, f, g := wsprAccounting.snapshot()
	return [7]int64{a, b, c, d, e, f, g}
}

func wsprDelta(after [7]int64, before [7]int64) (attempts, failures, rowsSeen, parsed, persisted, forwarded, droppedNoLoc int64) {
	for i := range after {
		after[i] -= before[i]
	}
	return after[0], after[1], after[2], after[3], after[4], after[5], after[6]
}

func wsprSnap() [7]int64 {
	a, b, c, d, e, f, g := wsprAccounting.snapshot()
	return [7]int64{a, b, c, d, e, f, g}
}

const wsprFixtureEnvelope = `{
  "data": [
    {
      "time": "2026-08-08 21:26:00",
      "band": 14,
      "rx_sign": "dl1abc",
      "rx_loc": "JO31",
      "tx_sign": "KF5XYZ",
      "tx_loc": "EM12",
      "distance": 8000,
      "frequency": 14097000,
      "power": 43,
      "snr": 5
    },
    {
      "time": "2026-08-08 21:28:00",
      "band": 14,
      "rx_sign": "DL2ABC",
      "rx_loc": "JO32",
      "tx_sign": "",
      "tx_loc": "",
      "distance": 100,
      "frequency": 14097000,
      "power": 0,
      "snr": 2
    },
    {
      "time": "2026-08-08 21:30:00",
      "band": 999,
      "rx_sign": "DL3ABC",
      "rx_loc": "JO33",
      "tx_sign": "K5XYZ",
      "tx_loc": "EM10",
      "distance": 5000,
      "frequency": 50293000,
      "power": 30,
      "snr": 1
    },
    {
      "time": "2026-08-08 21:32:00",
      "band": 7,
      "rx_sign": "DL4ABC",
      "rx_loc": "JO40",
      "tx_sign": "K6XYZ",
      "tx_loc": "EM13",
      "distance": 7000,
      "frequency": 7040123,
      "power": 33,
      "snr": 0
    }
  ]
}`

func TestFetchWSPRSpotsParsesRows(t *testing.T) {
	before := isolateWSPRPoll(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q, want application/json", got)
		}
		if got := r.Header.Get("User-Agent"); got != "horstreporter/1.0" {
			t.Errorf("User-Agent = %q, want horstreporter/1.0", got)
		}
		if q := r.URL.Query().Get("query"); !strings.Contains(q, "wspr.rx") || !strings.Contains(q, "FORMAT JSON") {
			t.Errorf("query = %q, want a wspr.rx ClickHouse FORMAT JSON query", q)
		}
		_, _ = w.Write([]byte(wsprFixtureEnvelope))
	}))
	defer srv.Close()

	fetchWSPRSpots(srv.Client(), srv.URL, wsprConfig{Enabled: true})

	attempts, failures, rowsSeen, parsed, _, forwarded, _ := wsprDelta(wsprSnap(), before)
	if attempts != 1 {
		t.Errorf("pollAttempts delta = %d, want 1", attempts)
	}
	if failures != 0 {
		t.Errorf("pollFailures delta = %d, want 0", failures)
	}
	if rowsSeen != 4 {
		t.Errorf("rowsSeen delta = %d, want 4 (all rows iterated)", rowsSeen)
	}
	if parsed != 2 {
		t.Errorf("parsedSpots delta = %d, want 2 (row 2 missing tx_sign, row 3 unknown band)", parsed)
	}
	if forwarded != 2 {
		t.Errorf("forwardedSpots delta = %d, want 2 (rows 1 and 4 have valid rx locators)", forwarded)
	}

	hub.RLock()
	defer hub.RUnlock()
	if len(hub.history) != 2 {
		t.Fatalf("expected 2 live-forwarded WSPR spots, got %d", len(hub.history))
	}
	m := hub.history[0]
	if m.Source != "wspr" || m.MD != "WSPR" || m.B != "20m" {
		t.Errorf("Source/MD/B = %q/%q/%q, want wspr/WSPR/20m", m.Source, m.MD, m.B)
	}
	if m.SC != "DL1ABC" || m.RC != "KF5XYZ" {
		t.Errorf("SC/RC = %q/%q, want DL1ABC/KF5XYZ (receiver→transmitter, uppercased)", m.SC, m.RC)
	}
	if m.SL != "JO31" || m.RL != "EM12" {
		t.Errorf("SL/RL = %q/%q, want JO31/EM12", m.SL, m.RL)
	}
	// Hz → kHz conversion.
	if m.F != 14097.0 {
		t.Errorf("F = %v, want 14097 (Hz→kHz)", m.F)
	}
	if m.RP != 5 {
		t.Errorf("RP = %d, want 5 (snr)", m.RP)
	}
	if m.TXPower != 43 {
		t.Errorf("TXPower = %d, want 43", m.TXPower)
	}
	// Parsed ClickHouse DateTime string, not "now".
	if m.T != 1786224360 {
		t.Errorf("T = %d, want 1786224360 (parsed from the fixture time)", m.T)
	}
}

func TestFetchWSPRSpotsUnparseableTimeFallsBackToNow(t *testing.T) {
	isolateWSPRPoll(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"time":"garbage","band":14,"rx_sign":"DL1ABC","rx_loc":"JO31","tx_sign":"K1XYZ","tx_loc":"EM12","frequency":14097000,"power":43,"snr":5}]}`))
	}))
	defer srv.Close()

	nowBefore := time.Now().Unix()
	fetchWSPRSpots(srv.Client(), srv.URL, wsprConfig{Enabled: true})

	hub.RLock()
	defer hub.RUnlock()
	if len(hub.history) != 1 {
		t.Fatalf("expected 1 forwarded spot, got %d", len(hub.history))
	}
	nowAfter := time.Now().Unix()
	if got := hub.history[0].T; got < nowBefore || got > nowAfter {
		t.Fatalf("T = %d, want defaulted to now [%d..%d] for an unparseable time", got, nowBefore, nowAfter)
	}
}

func TestFetchWSPRSpotsDegradesOnNon200(t *testing.T) {
	before := isolateWSPRPoll(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	fetchWSPRSpots(srv.Client(), srv.URL, wsprConfig{Enabled: true})

	attempts, failures, rowsSeen, parsed, _, forwarded, _ := wsprDelta(wsprSnap(), before)
	if attempts != 1 || failures != 1 {
		t.Fatalf("attempts/failures delta = %d/%d, want 1/1", attempts, failures)
	}
	if rowsSeen != 0 || parsed != 0 || forwarded != 0 {
		t.Fatalf("expected no rows parsed or forwarded on HTTP 500, got %d/%d/%d", rowsSeen, parsed, forwarded)
	}
	hub.RLock()
	defer hub.RUnlock()
	if len(hub.history) != 0 {
		t.Fatalf("expected no spots on HTTP 500, got %d", len(hub.history))
	}
}

func TestFetchWSPRSpotsDegradesOnMalformedJSON(t *testing.T) {
	before := isolateWSPRPoll(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>gateway error page</html>"))
	}))
	defer srv.Close()

	fetchWSPRSpots(srv.Client(), srv.URL, wsprConfig{Enabled: true})

	attempts, failures, rowsSeen, parsed, _, _, _ := wsprDelta(wsprSnap(), before)
	if attempts != 1 || failures != 1 {
		t.Fatalf("attempts/failures delta = %d/%d, want 1/1", attempts, failures)
	}
	if rowsSeen != 0 || parsed != 0 {
		t.Fatalf("expected no rows parsed from malformed JSON, got %d/%d", rowsSeen, parsed)
	}
}

func TestFetchWSPRSpotsEmptyResultIsNotAFailure(t *testing.T) {
	before := isolateWSPRPoll(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	fetchWSPRSpots(srv.Client(), srv.URL, wsprConfig{Enabled: true})

	attempts, failures, rowsSeen, _, _, _, _ := wsprDelta(wsprSnap(), before)
	if attempts != 1 || failures != 0 {
		t.Fatalf("attempts/failures delta = %d/%d, want 1/0 (empty result is a valid poll)", attempts, failures)
	}
	if rowsSeen != 0 {
		t.Fatalf("rowsSeen delta = %d, want 0", rowsSeen)
	}
}

func TestFetchWSPRSpotsRecoversAfterFailure(t *testing.T) {
	// Degradation must not wedge the poller: a failing poll followed by a
	// good one still ingests (log + skip, never crash).
	before := isolateWSPRPoll(t)

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(wsprFixtureEnvelope))
	}))
	defer srv.Close()

	fetchWSPRSpots(srv.Client(), srv.URL, wsprConfig{Enabled: true})
	fetchWSPRSpots(srv.Client(), srv.URL, wsprConfig{Enabled: true})

	attempts, failures, _, _, _, _, _ := wsprDelta(wsprSnap(), before)
	if attempts != 2 || failures != 1 {
		t.Fatalf("attempts/failures delta = %d/%d, want 2/1", attempts, failures)
	}
	hub.RLock()
	defer hub.RUnlock()
	if len(hub.history) != 2 {
		t.Fatalf("expected the second poll to ingest 2 spots, got %d", len(hub.history))
	}
}