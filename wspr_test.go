package main

import "testing"

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

	// Happy path: Power 20 W (20000 mW) → TXPower 20000.
	handleWSPRSpot(wsprSpot{
		Time: "2026-08-08 21:26:00", Band: 14, RxSign: "DL1ABC", RxLoc: "JO31",
		TxSign: "KF5XYZ", TxLoc: "EM12", Distance: 8000, Frequency: 14097000,
		Power: 20000, SNR: 5,
	}, now, cfg)
	hub.RLock()
	if len(hub.history) != 1 {
		hub.RUnlock()
		t.Fatalf("expected 1 spot in hub.history, got %d", len(hub.history))
	}
	if got := hub.history[0].TXPower; got != 20000 {
		t.Errorf("TXPower = %d, want 20000", got)
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
