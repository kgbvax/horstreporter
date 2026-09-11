package main

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"
)

func TestParseRBNSpot(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		wantOk bool
		want   rbnSpot
	}{
		{
			name:   "canonical CW line with skimmer -# suffix and trailing grid/time",
			line:   "DX de W3LPL-#:      14024.0  N0CALL         CW   22 dB   EN91   1234 Z",
			wantOk: true,
			want: rbnSpot{
				Skimmer:      "W3LPL-#",
				DXCall:       "N0CALL",
				FrequencyKHz: 14024.0,
				Mode:         "CW",
				DB:           22,
				ObservedAt:   1700000000,
			},
		},
		{
			name:   "RTTY line with numeric skimmer suffix",
			line:   "DX de OH8X-1:   7035.0  W1XYZ         RTTY  15 dB   KP20   0302 Z",
			wantOk: true,
			want: rbnSpot{
				Skimmer:      "OH8X-1",
				DXCall:       "W1XYZ",
				FrequencyKHz: 7035.0,
				Mode:         "RTTY",
				DB:           15,
				ObservedAt:   1700000000,
			},
		},
		{
			name:   "lowercase mode accepted and canonicalized to upper",
			line:   "DX de K1TTT-#:    14024.0  N0CALL         cw  22 dB   EN91   1234 Z",
			wantOk: true,
			want: rbnSpot{
				Skimmer:      "K1TTT-#",
				DXCall:       "N0CALL",
				FrequencyKHz: 14024.0,
				Mode:         "CW",
				DB:           22,
				ObservedAt:   1700000000,
			},
		},
		{name: "blank line", line: "", wantOk: false},
		{name: "login prompt is not a spot", line: "callsign:", wantOk: false},
		{name: "announcement is not a spot", line: "Welcome to the Reverse Beacon Network", wantOk: false},
		{name: "missing dB token", line: "DX de W3LPL-#: 14024.0 N0CALL CW 22 EN91 1234 Z", wantOk: false},
		{name: "non-numeric frequency rejected", line: "DX de W3LPL-#: abc N0CALL CW 22 dB EN91 1234 Z", wantOk: false},
		{name: "missing mode token rejected", line: "DX de W3LPL-#: 14024.0 N0CALL 22 dB EN91 1234 Z", wantOk: false},
		{
			name:   "JT65 mode parsed (previously dropped)",
			line:   "DX de W3LPL-#:      14076.0  N0CALL         JT65  18 dB   EN91   1234 Z",
			wantOk: true,
			want: rbnSpot{
				Skimmer:      "W3LPL-#",
				DXCall:       "N0CALL",
				FrequencyKHz: 14076.0,
				Mode:         "JT65",
				DB:           18,
				ObservedAt:   1700000000,
			},
		},
		{
			name:   "JS8 mode parsed (previously dropped)",
			line:   "DX de W3LPL-#:      14078.0  N0CALL         JS8   12 dB   EN91   1234 Z",
			wantOk: true,
			want: rbnSpot{
				Skimmer:      "W3LPL-#",
				DXCall:      "N0CALL",
				FrequencyKHz: 14078.0,
				Mode:         "JS8",
				DB:           12,
				ObservedAt:   1700000000,
			},
		},
		{
			name:   "OLIVIA mode parsed (previously dropped)",
			line:   "DX de W3LPL-#:      14072.0  N0CALL         OLIVIA  14 dB   EN91   1234 Z",
			wantOk: true,
			want: rbnSpot{
				Skimmer:      "W3LPL-#",
				DXCall:      "N0CALL",
				FrequencyKHz: 14072.0,
				Mode:         "OLIVIA",
				DB:           14,
				ObservedAt:   1700000000,
			},
		},
		{
			name:   "MSK144 mode parsed (previously dropped)",
			line:   "DX de W3LPL-#:      50260.0  N0CALL         MSK144  9 dB   EN91   1234 Z",
			wantOk: true,
			want: rbnSpot{
				Skimmer:      "W3LPL-#",
				DXCall:      "N0CALL",
				FrequencyKHz: 50260.0,
				Mode:         "MSK144",
				DB:           9,
				ObservedAt:   1700000000,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseRBNSpot(tc.line, 1700000000)
			if ok != tc.wantOk {
				t.Fatalf("parseRBNSpot(%q) ok = %v, want %v", tc.line, ok, tc.wantOk)
			}
			if !ok {
				return
			}
			if got != tc.want {
				t.Fatalf("parseRBNSpot(%q) = %+v, want %+v", tc.line, got, tc.want)
			}
		})
	}
}

func TestIsNonConditionsMode(t *testing.T) {
	keep := []string{"", "FT8", "FT4", "DXCLUSTER", "ft8", "ft4", "dxcluster"}
	exclude := []string{"CW", "RTTY", "PSK31", "PSK", "SSB", "AM", "FM", "DIGI", "cw", "rtty"}
	for _, md := range keep {
		if isNonConditionsMode(md) {
			t.Errorf("isNonConditionsMode(%q) = true, want false (kept in FT8 accumulator)", md)
		}
	}
	for _, md := range exclude {
		if !isNonConditionsMode(md) {
			t.Errorf("isNonConditionsMode(%q) = false, want true (excluded from FT8 accumulator)", md)
		}
	}
}

func TestSourceTypeForMessage(t *testing.T) {
	tests := []struct {
		name string
		m    MQTTMessage
		want string
	}{
		{name: "explicit rbn source wins regardless of mode", m: MQTTMessage{Source: "rbn", MD: "CW"}, want: "rbn"},
		{name: "explicit dxcluster source", m: MQTTMessage{Source: "dxcluster", MD: "DXCLUSTER"}, want: "dxcluster"},
		{name: "explicit mqtt source", m: MQTTMessage{Source: "mqtt", MD: "FT8"}, want: "mqtt"},
		{name: "source is case-insensitive", m: MQTTMessage{Source: "RBN"}, want: "rbn"},
		{name: "no source: DXCLUSTER mode fallback", m: MQTTMessage{MD: "DXCLUSTER"}, want: "dxcluster"},
		{name: "no source: anything else defaults to mqtt", m: MQTTMessage{MD: "CW"}, want: "mqtt"},
		{name: "empty message defaults to mqtt", m: MQTTMessage{}, want: "mqtt"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sourceTypeForMessage(tc.m); got != tc.want {
				t.Fatalf("sourceTypeForMessage() = %q, want %q", got, tc.want)
			}
		})
	}
}

// --- U3: RBN prompt handling, TCP session, and spot handling ---------------

func TestAwaitRBNDetectsCallPrompt(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	done := make(chan error, 1)
	go func() {
		done <- awaitRBNPrompt(bufio.NewReader(clientConn))
	}()
	go func() {
		_ = serverConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, _ = serverConn.Write([]byte("Please enter your call, e.g. DL1ABC: "))
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("awaitRBNPrompt error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("awaitRBNPrompt did not return when the prompt arrived")
	}
}

func TestAwaitRBNPromptCaseInsensitiveAndMidStream(t *testing.T) {
	// The needle "your call" must match lowercased input inside a stream of
	// unrelated bytes (the accumulated 256-byte tail scan).
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	done := make(chan error, 1)
	go func() {
		done <- awaitRBNPrompt(bufio.NewReader(clientConn))
	}()
	go func() {
		payload := strings.Repeat("Welcome ", 30) + "ENTER Your Call: "
		_ = serverConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, _ = serverConn.Write([]byte(payload))
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("awaitRBNPrompt error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("awaitRBNPrompt did not return")
	}
}

func TestAwaitRBNPromptEndsOnClosedConnection(t *testing.T) {
	// "No hang when the prompt never arrives": a closed connection must end
	// the wait with an error. (The real path is additionally bounded by the
	// caller's SetReadDeadline; see the DX-cluster deadline test for the
	// timeout-vs-EOF characterization.)
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	_ = serverConn.Close()

	done := make(chan error, 1)
	go func() {
		done <- awaitRBNPrompt(bufio.NewReader(clientConn))
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error on closed connection, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("awaitRBNPrompt did not return after the connection closed")
	}
}

func TestRunRBNSessionHandshakeAndSpots(t *testing.T) {
	isolateHubForIngest(t, false)

	const callsign = "DL1ABC"
	script := func(conn net.Conn, reader *bufio.Reader) error {
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := conn.Write([]byte("Please enter your call: ")); err != nil {
			return err
		}
		call, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		if got := strings.TrimRight(call, "\r\n"); got != callsign {
			return &testScriptError{step: "callsign", got: got, want: callsign}
		}
		if _, err := conn.Write([]byte(
			"Welcome to the Reverse Beacon Network\r\n" +
				"DX de W3LPL-#:      14024.0  N0CALL         CW   22 dB   EN91   1234 Z\r\n" +
				"\r\n" +
				"DX de OH2X-#: 7035.0 W1XYZ CW broken\r\n")); err != nil {
			return err
		}
		return conn.Close()
	}
	addr, serverDone := startTestTCPServer(t, script)

	_, _, _, parsedBefore, _, _, droppedBefore := rbnAccounting.snapshot()
	cfg := rbnConfig{Enabled: true, Callsign: callsign}
	err := runRBNSession(addr, cfg)

	// The relay closed the stream → the session reports it for the reconnect loop.
	if err == nil || !strings.Contains(err.Error(), "rbn connection closed") {
		t.Fatalf("expected 'rbn connection closed' after EOF, got %v", err)
	}
	select {
	case serr := <-serverDone:
		if serr != nil {
			t.Fatalf("server script failed: %v", serr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server script did not finish")
	}

	_, _, _, parsed, _, _, dropped := rbnAccounting.snapshot()
	if got := parsed - parsedBefore; got != 1 {
		t.Errorf("parsedSpots delta = %d, want 1", got)
	}
	// No resolver → no DX locator → dropped from live (but still parsed).
	if got := dropped - droppedBefore; got != 1 {
		t.Errorf("droppedNoLoc delta = %d, want 1", got)
	}
}

func TestRunRBNSessionRequiresCallsign(t *testing.T) {
	isolateHubForIngest(t, false)

	// No callsign configured: the relay's prompt is drained, no callsign is
	// sent, and the session returns the configuration error (the reconnect
	// loop keeps retrying until one is set).
	script := func(conn net.Conn, reader *bufio.Reader) error {
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := conn.Write([]byte("Please enter your call: ")); err != nil {
			return err
		}
		call, err := reader.ReadString('\n')
		if err == nil && call != "" {
			return &testScriptError{step: "callsign must not be sent", got: strings.TrimRight(call, "\r\n"), want: ""}
		}
		return nil
	}
	addr, serverDone := startTestTCPServer(t, script)

	cfg := rbnConfig{Enabled: true} // no callsign
	err := runRBNSession(addr, cfg)
	if err == nil || !strings.Contains(err.Error(), "none configured") {
		t.Fatalf("expected 'none configured' error, got %v", err)
	}
	select {
	case serr := <-serverDone:
		if serr != nil {
			t.Fatalf("server script failed: %v", serr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server script did not finish")
	}
}

func TestHandleRBNSpotPopulatesLiveFields(t *testing.T) {
	isolateHubForIngest(t, false)

	resolver := &stubResolver{
		locators: map[string]string{"W3LPL-#": "FM18"},
		infos:    map[string]CallsignInfo{"N0CALL": {Locator: "fn31pr", Name: "Bob", Country: "United States"}},
	}

	_, _, _, _, _, forwardedBefore, droppedBefore := rbnAccounting.snapshot()
	handleRBNSpot(rbnSpot{
		Skimmer:      "W3LPL-#",
		DXCall:       "N0CALL",
		FrequencyKHz: 14024.0,
		Mode:         "CW",
		DB:           22,
		ObservedAt:   1700000000,
	}, resolver, nil)
	_, _, _, _, _, forwardedAfter, droppedAfter := rbnAccounting.snapshot()
	if got := forwardedAfter - forwardedBefore; got != 1 {
		t.Fatalf("forwardedSpots delta = %d, want 1", got)
	}
	if got := droppedAfter - droppedBefore; got != 0 {
		t.Fatalf("droppedNoLoc delta = %d, want 0", got)
	}

	hub.RLock()
	defer hub.RUnlock()
	if len(hub.history) != 1 {
		t.Fatalf("expected 1 forwarded RBN spot, got %d", len(hub.history))
	}
	m := hub.history[0]
	if m.SC != "W3LPL-#" || m.RC != "N0CALL" {
		t.Errorf("SC/RC = %q/%q, want W3LPL-#/N0CALL (skimmer→DX)", m.SC, m.RC)
	}
	if m.RL != "FN31PR" || m.SL != "FM18" {
		t.Errorf("RL/SL = %q/%q, want FN31PR/FM18", m.RL, m.SL)
	}
	if m.MD != "CW" {
		t.Errorf("MD = %q, want CW (real over-the-air mode)", m.MD)
	}
	if m.RP != 22 {
		t.Errorf("RP = %d, want 22 (skimmer dB)", m.RP)
	}
	if m.Source != "rbn" {
		t.Errorf("Source = %q, want rbn", m.Source)
	}
	if m.B != "20m" {
		t.Errorf("B = %q, want 20m", m.B)
	}
	if m.F != 14024.0 {
		t.Errorf("F = %v, want 14024", m.F)
	}
	if m.OpName != "Bob" {
		t.Errorf("OpName = %q, want Bob", m.OpName)
	}
}

func TestHandleRBNSpotDropsWithoutDXLocator(t *testing.T) {
	isolateHubForIngest(t, false)

	_, _, _, _, _, forwardedBefore, droppedBefore := rbnAccounting.snapshot()
	// No resolver at all → no locators → unusable for live.
	handleRBNSpot(rbnSpot{Skimmer: "W3LPL", DXCall: "N0CALL", FrequencyKHz: 14024.0, Mode: "CW", DB: 20, ObservedAt: 1000}, nil, nil)
	_, _, _, _, _, forwardedAfter, droppedAfter := rbnAccounting.snapshot()
	if got := droppedAfter - droppedBefore; got != 1 {
		t.Fatalf("droppedNoLoc delta = %d, want 1", got)
	}
	if got := forwardedAfter - forwardedBefore; got != 0 {
		t.Fatalf("forwardedSpots delta = %d, want 0", got)
	}

	hub.RLock()
	defer hub.RUnlock()
	if len(hub.history) != 0 {
		t.Fatalf("expected dropped RBN spot not to reach hub.history, got %d", len(hub.history))
	}
}

func TestIsRBNSpotUsableForLive(t *testing.T) {
	if !isRBNSpotUsableForLive(MQTTMessage{B: "20m", RL: "JO62"}) {
		t.Error("band + DX locator should be usable")
	}
	// Skimmer locator alone is not enough (mirrors the DX-cluster rule).
	if isRBNSpotUsableForLive(MQTTMessage{B: "20m", RL: "", SL: "JO62"}) {
		t.Error("skimmer locator alone should not be usable")
	}
	if isRBNSpotUsableForLive(MQTTMessage{B: "", RL: "JO62"}) {
		t.Error("empty band should not be usable")
	}
	if isRBNSpotUsableForLive(MQTTMessage{B: "20m", RL: "W1AW"}) {
		t.Error("callsign as locator should not be usable")
	}
}
