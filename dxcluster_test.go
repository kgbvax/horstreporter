package main

import (
	"bufio"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestParseDXClusterSpot(t *testing.T) {
	line := "DX de DL1ABC: 14074.0 K1XYZ FT8 cq test"
	spot, ok := parseDXClusterSpot(line, 123456789)
	if !ok {
		t.Fatalf("expected line to parse")
	}
	if spot.Spotter != "DL1ABC" {
		t.Fatalf("unexpected spotter: %q", spot.Spotter)
	}
	if spot.DXCall != "K1XYZ" {
		t.Fatalf("unexpected dx call: %q", spot.DXCall)
	}
	if spot.FrequencyKHz != 14074.0 {
		t.Fatalf("unexpected frequency: %v", spot.FrequencyKHz)
	}
	if spot.Comment != "FT8 cq test" {
		t.Errorf("unexpected comment: %q (group 4 is the whole tail after the dx call)", spot.Comment)
	}
	if spot.ObservedAt != 123456789 {
		t.Errorf("unexpected observedAt: %d", spot.ObservedAt)
	}
}

func TestParseDXClusterSpotEdgeCases(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		wantOK bool
	}{
		{name: "canonical line", line: "DX de W3LPL:     14074.0  K1XYZ      FT8  599  JO62", wantOK: true},
		// The DX-cluster spotter class has no '#': skimmer-style suffixes
		// (W3LPL-#) are an RBN-only shape.
		{name: "skimmer #-suffix spotter rejected", line: "DX de W3LPL-#: 14074.0 K1XYZ cq", wantOK: false},
		{name: "portable spotter accepted", line: "DX de DL/W1ABC:  7074.0  K1XYZ/P   CW   cq", wantOK: true},
		{name: "lowercase line parsed and uppercased", line: "dx de dl1abc: 14074.0 k1xyz cq", wantOK: true},
		{name: "plain chatter rejected", line: "Welcome to DB0ERF", wantOK: false},
		{name: "login prompt rejected", line: "login:", wantOK: false},
		{name: "blank line rejected", line: "", wantOK: false},
		{name: "zero frequency rejected", line: "DX de DL1ABC: 0 K1XYZ cq", wantOK: false},
		{name: "negative frequency rejected", line: "DX de DL1ABC: -14074 K1XYZ cq", wantOK: false},
		{name: "non-numeric frequency rejected", line: "DX de DL1ABC: abc K1XYZ cq", wantOK: false},
		{name: "missing dx call rejected", line: "DX de DL1ABC: 14074.0 cq", wantOK: false},
		{name: "missing spotter rejected", line: "DX de : 14074.0 K1XYZ cq", wantOK: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := parseDXClusterSpot(tc.line, 1000)
			if ok != tc.wantOK {
				t.Fatalf("parseDXClusterSpot(%q) ok = %v, want %v", tc.line, ok, tc.wantOK)
			}
		})
	}

	// Uppercasing is part of the parse contract.
	if s, ok := parseDXClusterSpot("dx de dl1abc: 14074.0 k1xyz cq", 1000); !ok || s.Spotter != "DL1ABC" || s.DXCall != "K1XYZ" {
		t.Fatalf("expected uppercased spotter/dx call, got %+v ok=%v", s, ok)
	}
}

func TestBandFromFrequencyKHz(t *testing.T) {
	cases := []struct {
		freq float64
		band string
	}{
		{14074.0, "20m"},
		{7074.0, "40m"},
		{50100.0, "6m"},
		{999.0, ""},
		// Band edges from the implementation's switch.
		{1800, "160m"}, {1999.9, "160m"}, {2000, ""},
		{3500, "80m"}, {7000, "40m"}, {7299.9, "40m"}, {7300, ""},
		{10100, "30m"}, {14000, "20m"}, {14350, ""},
		{18068, "17m"}, {21000, "15m"}, {24890, "12m"},
		{28000, "10m"}, {29699, "10m"}, {29700, ""},
		{50000, "6m"}, {70000, "4m"}, {144000, "2m"}, {147999.9, "2m"}, {148000, ""},
		{430000, "70cm"}, {435000, "70cm"}, {439999.9, "70cm"}, {440000, ""},
		{5250, "60m"}, {5449.9, "60m"},
	}
	for _, tc := range cases {
		if got := bandFromFrequencyKHz(tc.freq); got != tc.band {
			t.Errorf("bandFromFrequencyKHz(%v) = %q, want %q", tc.freq, tc.band, got)
		}
	}
}

// stubResolver is a fake CallsignLocatorResolver so handleDXClusterSpot /
// handleRBNSpot locator enrichment can be driven without QRZ.
type stubResolver struct {
	locators map[string]string
	infos    map[string]CallsignInfo
}

func (s *stubResolver) LookupLocator(callsign string) (string, error) {
	if loc, ok := s.locators[callsign]; ok {
		return loc, nil
	}
	return "", io.EOF
}

func (s *stubResolver) LookupInfo(callsign string) (CallsignInfo, error) {
	if info, ok := s.infos[callsign]; ok {
		return info, nil
	}
	return CallsignInfo{}, io.EOF
}

// isolateHubForIngest replaces hub.clients/history with fresh empties for the
// duration of fn (and restores them), mirroring the withHubSnapshot pattern.
// It also nils dxBaseline unless the caller wants one, since handle*Spot
// persist through it.
func isolateHubForIngest(t *testing.T, withBaseline bool) (engine *DxBaselineEngine) {
	t.Helper()
	hub.Lock()
	savedClients := hub.clients
	savedHistory := hub.history
	hub.clients = make(map[*Client]bool)
	hub.history = nil
	hub.Unlock()
	savedBaseline := dxBaseline
	if withBaseline {
		dxBaseline = newDxBaselineEngine("")
		engine = dxBaseline
	} else {
		dxBaseline = nil
	}
	t.Cleanup(func() {
		hub.Lock()
		hub.clients = savedClients
		hub.history = savedHistory
		hub.Unlock()
		dxBaseline = savedBaseline
	})
	return engine
}

func TestIsDXClusterSpotUsableForLive(t *testing.T) {
	tests := []struct {
		name string
		m    MQTTMessage
		want bool
	}{
		{"valid band + valid DX locator", MQTTMessage{B: "20m", RL: "JO62QM"}, true},
		{"lowercase locator accepted", MQTTMessage{B: "20m", RL: "jo62qm"}, true},
		{"empty band rejected", MQTTMessage{B: "", RL: "JO62QM"}, false},
		{"whitespace band rejected", MQTTMessage{B: "   ", RL: "JO62QM"}, false},
		{"empty DX locator rejected", MQTTMessage{B: "20m", RL: ""}, false},
		{"callsign instead of locator rejected", MQTTMessage{B: "20m", RL: "K1XYZ"}, false},
		// Spotter locator alone is not enough: the DX/RX locator is required.
		{"spotter locator only rejected", MQTTMessage{B: "20m", RL: "", SL: "JO62"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDXClusterSpotUsableForLive(tc.m); got != tc.want {
				t.Fatalf("isDXClusterSpotUsableForLive(%+v) = %v, want %v", tc.m, got, tc.want)
			}
		})
	}
}

func TestHandleDXClusterSpotPopulatesLiveFields(t *testing.T) {
	isolateHubForIngest(t, false)

	resolver := &stubResolver{
		locators: map[string]string{"W3LPL-#": "FM18"},
		infos: map[string]CallsignInfo{
			"K1XYZ": {Locator: "fn31pr", Name: "Joe", Country: "United States"},
		},
	}

	before, _, _, _, _, forwarded, droppedNoLoc := dxClusterAccounting.snapshot()
	// Calls arrive here already uppercased by parseDXClusterSpot.
	handleDXClusterSpot(dxClusterSpot{
		Spotter:      "W3LPL-#",
		DXCall:       "K1XYZ",
		FrequencyKHz: 14074.0,
		Comment:      "cq dx",
		ObservedAt:   1700000000,
	}, resolver, nil)
	afterAttempts, _, _, _, _, afterForwarded, afterDropped := dxClusterAccounting.snapshot()

	if got := afterForwarded - forwarded; got != 1 {
		t.Fatalf("forwardedSpots delta = %d, want 1", got)
	}
	if got := afterDropped - droppedNoLoc; got != 0 {
		t.Fatalf("droppedNoLoc delta = %d, want 0", got)
	}
	if got := afterAttempts - before; got != 0 {
		t.Errorf("connectAttempts changed by %d", got)
	}

	hub.RLock()
	defer hub.RUnlock()
	if len(hub.history) != 1 {
		t.Fatalf("expected 1 forwarded spot in hub.history, got %d", len(hub.history))
	}
	m := hub.history[0]
	if m.SC != "W3LPL-#" {
		t.Errorf("SC = %q, want W3LPL-# (spotter, uppercased)", m.SC)
	}
	if m.RC != "K1XYZ" {
		t.Errorf("RC = %q, want K1XYZ (dx call)", m.RC)
	}
	if m.SL != "FM18" {
		t.Errorf("SL = %q, want FM18 (uppercased spotter locator)", m.SL)
	}
	if m.RL != "FN31PR" {
		t.Errorf("RL = %q, want FN31PR (uppercased dx locator)", m.RL)
	}
	if m.B != "20m" {
		t.Errorf("B = %q, want 20m", m.B)
	}
	if m.MD != "DXCLUSTER" {
		t.Errorf("MD = %q, want DXCLUSTER", m.MD)
	}
	if m.Source != "dxcluster" {
		t.Errorf("Source = %q, want dxcluster", m.Source)
	}
	if m.F != 14074.0 {
		t.Errorf("F = %v, want 14074", m.F)
	}
	if m.CM != "cq dx" {
		t.Errorf("CM = %q, want 'cq dx'", m.CM)
	}
	if m.T != 1700000000 {
		t.Errorf("T = %d, want 1700000000", m.T)
	}
	if m.RP != 0 {
		t.Errorf("RP = %d, want 0 (dx-cluster spots are baseline-benign)", m.RP)
	}
	if m.OpName != "Joe" {
		t.Errorf("OpName = %q, want Joe", m.OpName)
	}
	if m.Country != "United States" {
		t.Errorf("Country = %q, want from QRZ fallback (no cty resolver)", m.Country)
	}
}

func TestHandleDXClusterSpotDropsWithoutLocator(t *testing.T) {
	isolateHubForIngest(t, false)

	// No resolver → no locators at all → unusable for live.
	_, _, _, _, _, _, droppedBefore := dxClusterAccounting.snapshot()
	handleDXClusterSpot(dxClusterSpot{
		Spotter: "W3LPL", DXCall: "K1XYZ", FrequencyKHz: 14074.0, ObservedAt: 1000,
	}, nil, nil)
	_, _, _, _, _, _, droppedAfter := dxClusterAccounting.snapshot()
	if got := droppedAfter - droppedBefore; got != 1 {
		t.Fatalf("droppedNoLoc delta = %d, want 1", got)
	}
	hub.RLock()
	defer hub.RUnlock()
	if len(hub.history) != 0 {
		t.Fatalf("expected dropped spot not to reach hub.history, got %d entries", len(hub.history))
	}
}

func TestHandleDXClusterSpotDropsOffBandFrequency(t *testing.T) {
	isolateHubForIngest(t, false)

	// 14.074 MHz is in-band, 14.9 MHz sits in the 20m→12m gap → band "" → unusable.
	resolver := &stubResolver{
		infos: map[string]CallsignInfo{"K1XYZ": {Locator: "FN31"}},
	}
	_, _, _, _, _, _, droppedBefore := dxClusterAccounting.snapshot()
	handleDXClusterSpot(dxClusterSpot{
		Spotter: "W3LPL", DXCall: "K1XYZ", FrequencyKHz: 14900.0, ObservedAt: 1000,
	}, resolver, nil)
	_, _, _, _, _, _, droppedAfter := dxClusterAccounting.snapshot()
	if got := droppedAfter - droppedBefore; got != 1 {
		t.Fatalf("droppedNoLoc delta = %d, want 1 (out-of-band frequency → empty band)", got)
	}
	hub.RLock()
	defer hub.RUnlock()
	if len(hub.history) != 0 {
		t.Fatalf("expected empty hub.history, got %d", len(hub.history))
	}
}

func TestDXClusterBackoffConstants(t *testing.T) {
	// The displacement-loop fix: short-lived sessions (login rejections) must
	// back off, bounded by a max delay. Pin the constants.
	if dxClusterMinHealthySession != 60*time.Second {
		t.Fatalf("dxClusterMinHealthySession = %v, want 60s", dxClusterMinHealthySession)
	}
	if dxClusterMaxReconnectDelay != 15*time.Minute {
		t.Fatalf("dxClusterMaxReconnectDelay = %v, want 15m", dxClusterMaxReconnectDelay)
	}
}

// newPipeLoginConn builds a net.Pipe pair for dxClusterLogin tests. The
// server end runs script (its bufio.Reader reads the client's commands).
func newPipeLoginConn(t *testing.T) (clientConn net.Conn, serverConn net.Conn, serverReader *bufio.Reader) {
	t.Helper()
	clientConn, serverConn = net.Pipe()
	serverReader = bufio.NewReader(serverConn)
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})
	return clientConn, serverConn, serverReader
}

func TestDXClusterLoginSendsCallsignThenPassword(t *testing.T) {
	clientConn, serverConn, serverReader := newPipeLoginConn(t)

	done := make(chan error, 1)
	go func() {
		done <- dxClusterLogin(clientConn, bufio.NewReader(clientConn), "DL1ABC", "s3cret", false)
	}()

	// Server side: prompt for login, read the callsign, prompt for password,
	// read the password — proving the ordering.
	_ = serverConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := serverConn.Write([]byte("login: ")); err != nil {
		t.Fatalf("write login prompt: %v", err)
	}
	_ = serverConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	user, err := serverReader.ReadString('\n')
	if err != nil {
		t.Fatalf("read username: %v", err)
	}
	if got := strings.TrimRight(user, "\r\n"); got != "DL1ABC" {
		t.Fatalf("username sent = %q, want DL1ABC", got)
	}
	_ = serverConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := serverConn.Write([]byte("Please enter your password: ")); err != nil {
		t.Fatalf("write password prompt: %v", err)
	}
	_ = serverConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	pass, err := serverReader.ReadString('\n')
	if err != nil {
		t.Fatalf("read password: %v", err)
	}
	if got := strings.TrimRight(pass, "\r\n"); got != "s3cret" {
		t.Fatalf("password sent = %q, want s3cret", got)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("dxClusterLogin returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dxClusterLogin did not complete")
	}
}

func TestDXClusterLoginProceedsWithoutPasswordPrompt(t *testing.T) {
	// Unregistered calls: the server never sends a password prompt — the login
	// must still succeed (read-only feed) without sending a password. The
	// server closes after the username, which is how a dead-end server behaves
	// and exercises the bounded-wait path without a 6s wall.
	clientConn, serverConn, serverReader := newPipeLoginConn(t)

	done := make(chan error, 1)
	go func() {
		done <- dxClusterLogin(clientConn, bufio.NewReader(clientConn), "DL1ABC", "", false)
	}()

	_ = serverConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := serverConn.Write([]byte("login: ")); err != nil {
		t.Fatalf("write login prompt: %v", err)
	}
	_ = serverConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := serverReader.ReadString('\n'); err != nil {
		t.Fatalf("read username: %v", err)
	}
	// No password prompt — just close. (Closing the server end unblocks the
	// password-phase read with EOF, so this exercises the "prompt never
	// arrives → proceed" branch without waiting out the 6s deadline.)
	_ = serverConn.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected login to proceed without password prompt, got error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dxClusterLogin did not complete")
	}
}

func TestDXClusterLoginPasswordPhaseTimeoutProceeds(t *testing.T) {
	// Characterization of the unregistered-account timeout path: the server
	// stays silent after the login phase, so the 6s password-prompt deadline
	// fires and login proceeds (nil error) instead of hanging. Slow (~6s) —
	// it pins the real deadline, so keep it.
	clientConn, serverConn, serverReader := newPipeLoginConn(t)

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- dxClusterLogin(clientConn, bufio.NewReader(clientConn), "DL1ABC", "", false)
	}()

	_ = serverConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := serverConn.Write([]byte("login: ")); err != nil {
		t.Fatalf("write login prompt: %v", err)
	}
	_ = serverConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := serverReader.ReadString('\n'); err != nil {
		t.Fatalf("read username: %v", err)
	}
	// Keep the connection open but silent (no password prompt ever).

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected silent password phase to proceed, got: %v", err)
		}
		if el := time.Since(start); el < 5*time.Second || el > 10*time.Second {
			t.Errorf("login returned after %v, want ~6s (password-phase deadline)", el)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("dxClusterLogin did not return after silent password phase")
	}
}

func TestDXClusterLoginFailsWithoutLoginPrompt(t *testing.T) {
	clientConn, serverConn, _ := newPipeLoginConn(t)

	done := make(chan error, 1)
	go func() {
		done <- dxClusterLogin(clientConn, bufio.NewReader(clientConn), "DL1ABC", "", false)
	}()

	// Silent server → close. The prompt wait must end with an error (bounded
	// by the read deadline), not hang.
	_ = serverConn.Close()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "never saw login prompt") {
			t.Fatalf("expected 'never saw login prompt' error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dxClusterLogin did not return when the login prompt never arrived")
	}
}

func TestAwaitPromptMatchesAcrossFillerAndCase(t *testing.T) {
	clientConn, serverConn, _ := newPipeLoginConn(t)

	done := make(chan error, 1)
	go func() {
		done <- awaitPrompt(bufio.NewReader(clientConn), "LOGIN:")
	}()

	// The accumulated-tail scan lowercases input, keeps only the last 80
	// bytes, and matches prompts that arrive mid-stream (never
	// newline-terminated).
	payload := strings.Repeat("x", 90) + "LoGiN: "
	go func() {
		_ = serverConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, _ = serverConn.Write([]byte(payload))
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("awaitPrompt error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("awaitPrompt did not return")
	}
}

// startTestTCPServer runs a raw TCP server on 127.0.0.1 and hands the
// connection to script, which performs the scripted handshake. This dials
// localhost only — no external hosts.
func startTestTCPServer(t *testing.T, script func(conn net.Conn, reader *bufio.Reader) error) (addr string, done <-chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	doneCh := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			doneCh <- err
			return
		}
		defer conn.Close()
		doneCh <- script(conn, bufio.NewReader(conn))
	}()
	return ln.Addr().String(), doneCh
}

func TestRunDXClusterSessionHandshakeAndSpots(t *testing.T) {
	isolateHubForIngest(t, false)

	const username = "DL1ABC"
	script := func(conn net.Conn, reader *bufio.Reader) error {
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := conn.Write([]byte("login: ")); err != nil {
			return err
		}
		user, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		if got := strings.TrimRight(user, "\r\n"); got != username {
			return &testScriptError{step: "username", got: got, want: username}
		}
		if _, err := conn.Write([]byte("password: ")); err != nil {
			return err
		}
		pass, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		if got := strings.TrimRight(pass, "\r\n"); got != "s3cret" {
			return &testScriptError{step: "password", got: got, want: "s3cret"}
		}
		// The session requests pagination + a startup snapshot right after login.
		cmd1, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		if got := strings.TrimRight(cmd1, "\r\n"); got != "set/page 200" {
			return &testScriptError{step: "first command", got: got, want: "set/page 200"}
		}
		cmd2, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		if got := strings.TrimRight(cmd2, "\r\n"); got != "sh/dx 150" {
			return &testScriptError{step: "second command", got: got, want: "sh/dx 150"}
		}
		// Stream spots + chatter, then close: the session ends with
		// "dx cluster connection closed" (scanner saw a clean EOF).
		if _, err := conn.Write([]byte("Welcome to DB0ERF\n" +
			"DX de W3LPL:       14074.0  K1XYZ      FT8  599  JO62  1234 Z\n" +
			"\n" +
			"DX de broken-line\n")); err != nil {
			return err
		}
		return conn.Close()
	}

	addr, serverDone := startTestTCPServer(t, script)

	_, _, linesBefore, parsedBefore, _, forwarded, droppedBefore := dxClusterAccounting.snapshot()
	cfg := dxClusterConfig{
		Enabled:  true,
		Username: username,
		Password: "s3cret",
	}
	err := runDXClusterSession(addr, cfg)

	if err == nil || !strings.Contains(err.Error(), "dx cluster connection closed") {
		t.Fatalf("expected 'dx cluster connection closed' after EOF, got %v", err)
	}
	select {
	case serr := <-serverDone:
		if serr != nil {
			t.Fatalf("server script failed: %v", serr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server script did not finish")
	}

	_, _, linesSeen, parsed, _, forwardedAfter, dropped := dxClusterAccounting.snapshot()
	if got := linesSeen - linesBefore; got != 4 {
		t.Errorf("linesSeen delta = %d, want 4 (spots, chatter, empty, broken)", got)
	}
	if got := parsed - parsedBefore; got != 1 {
		t.Errorf("parsedSpots delta = %d, want 1", got)
	}
	// No resolver configured → the spot has no locator → dropped from live.
	if got := dropped - droppedBefore; got != 1 {
		t.Errorf("droppedNoLoc delta = %d, want 1", got)
	}
	if got := forwardedAfter - forwarded; got != 0 {
		t.Errorf("forwardedSpots delta = %d, want 0", got)
	}
	if got := len(hub.history); got != 0 {
		t.Errorf("hub.history = %d spots, want 0 (no locator, nothing forwarded)", got)
	}
}

func TestRunDXClusterSessionAnonymousSkipsLogin(t *testing.T) {
	// No username configured: no prompt handshake, the two commands go out
	// immediately (and the session still streams + drops locator-less spots).
	isolateHubForIngest(t, false)

	script := func(conn net.Conn, reader *bufio.Reader) error {
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		cmd1, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		if got := strings.TrimRight(cmd1, "\r\n"); got != "set/page 200" {
			return &testScriptError{step: "first command", got: got, want: "set/page 200"}
		}
		cmd2, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		if got := strings.TrimRight(cmd2, "\r\n"); got != "sh/dx 150" {
			return &testScriptError{step: "second command", got: got, want: "sh/dx 150"}
		}
		if _, err := conn.Write([]byte("login: \nDX de W3LPL: 14074.0 K1XYZ FT8 599 JO62\n")); err != nil {
			return err
		}
		return conn.Close()
	}
	addr, serverDone := startTestTCPServer(t, script)

	cfg := dxClusterConfig{Enabled: true} // no username/password
	err := runDXClusterSession(addr, cfg)
	if err == nil || !strings.Contains(err.Error(), "dx cluster connection closed") {
		t.Fatalf("expected 'dx cluster connection closed', got %v", err)
	}
	select {
	case serr := <-serverDone:
		if serr != nil {
			t.Fatalf("server script failed: %v", serr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server script did not finish")
	}
	// The spot line was parsed (handled → dropped without a resolver), and the
	// "login: " chatter line is never a spot.
	hub.RLock()
	defer hub.RUnlock()
	if len(hub.history) != 0 {
		t.Fatalf("expected no live spots without a resolver, got %d", len(hub.history))
	}
}

func TestRunDXClusterSessionDialFailure(t *testing.T) {
	// Port 1 on loopback has no listener — the dial must fail fast with an
	// error (no hang, no panic).
	err := runDXClusterSession("127.0.0.1:1", dxClusterConfig{Enabled: true})
	if err == nil {
		t.Fatal("expected dial error for unreachable endpoint, got nil")
	}
}

type testScriptError struct {
	step string
	got  string
	want string
}

func (e *testScriptError) Error() string {
	return e.step + ": got " + e.got + ", want " + e.want
}