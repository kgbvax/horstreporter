package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WaveLogGate broadcasts live radio status on a fixed WebSocket port (54322),
// separate from its HTTP tune callback (54321). See the WaveLogGate manual:
// https://github.com/wavelog/WaveLogGate — "WebSocket broadcast".
const waveLogGateWSPort = 54322

// rigRadioState is an immutable snapshot of the rig's last-known operating state,
// sourced from WaveLogGate's WebSocket broadcast. FreqRxHz/ModeRx are populated
// only during split operation (a distinct RX VFO).
type rigRadioState struct {
	FreqHz    int64
	Mode      string
	FreqRxHz  int64 // split RX frequency; 0 when not split
	ModeRx    string
	Power     float64
	Radio     string
	Split     bool
	Online    bool // WS connected AND at least one status seen
	UpdatedAt time.Time
}

// wlgWSMessage mirrors WaveLogGate's RadioStatusMsg (internal/ws/server.go). The
// "welcome" handshake message carries only type+message; radio updates carry a
// non-zero frequency.
type wlgWSMessage struct {
	Type        string  `json:"type"`
	Frequency   int64   `json:"frequency"`
	Mode        string  `json:"mode"`
	Power       float64 `json:"power"`
	Radio       string  `json:"radio"`
	Timestamp   int64   `json:"timestamp"`
	FrequencyRx int64   `json:"frequency_rx"`
	ModeRx      string  `json:"mode_rx"`
}

// waveLogGateWS maintains a persistent WebSocket subscription to WaveLogGate and
// caches the latest radio status behind a mutex. It auto-reconnects with backoff.
type waveLogGateWS struct {
	wsURL string

	mu        sync.RWMutex
	state     rigRadioState
	connected bool
}

// deriveWaveLogGateWSURL turns the tune-callback base URL (e.g.
// http://127.0.0.1:54321) into the WebSocket status URL (ws://127.0.0.1:54322).
// The WS port is fixed by WaveLogGate; only the host is taken from the base.
func deriveWaveLogGateWSURL(base string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return "", fmt.Errorf("empty WaveLogGate base URL")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid WaveLogGate URL %q: %w", base, err)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("WaveLogGate URL %q has no host", base)
	}
	scheme := "ws"
	if strings.EqualFold(u.Scheme, "https") || strings.EqualFold(u.Scheme, "wss") {
		scheme = "wss"
	}
	return fmt.Sprintf("%s://%s/", scheme, net.JoinHostPort(host, strconv.Itoa(waveLogGateWSPort))), nil
}

func newWaveLogGateWS(base string) *waveLogGateWS {
	wsURL, err := deriveWaveLogGateWSURL(base)
	if err != nil {
		log.Printf("[WARN] WaveLogGate live status disabled: %v", err)
		return nil
	}
	c := &waveLogGateWS{wsURL: wsURL}
	go c.runLoop()
	log.Printf("[INFO] WaveLogGate live status: subscribing to %s", wsURL)
	return c
}

// runLoop keeps a WebSocket connection alive, reconnecting with capped backoff.
func (c *waveLogGateWS) runLoop() {
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		if err := c.connectOnce(); err != nil {
			if debugExternalEnabled() {
				log.Printf("[DEBUG] WaveLogGate WS connect failed: %v (retry in %s)", err, backoff)
			}
			c.setDisconnected()
			time.Sleep(backoff)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		backoff = time.Second // reset after a clean session
	}
}

// connectOnce dials, reads until the connection drops, then returns.
func (c *waveLogGateWS) connectOnce() error {
	dialer := websocket.Dialer{HandshakeTimeout: 8 * time.Second}
	conn, _, err := dialer.Dial(c.wsURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	// A generous read deadline reset via pong/any-message keeps idle-but-healthy
	// links up; WaveLogGate pushes on every rig change (may be idle for a while).
	conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		c.applyMessage(data)
	}
}

func (c *waveLogGateWS) applyMessage(data []byte) {
	var msg wlgWSMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}
	// Only radio-status frames carry a frequency; skip welcome / other types.
	if strings.EqualFold(msg.Type, "welcome") || msg.Frequency <= 0 {
		// Still mark connected on the welcome handshake so "online" flips promptly.
		if strings.EqualFold(msg.Type, "welcome") {
			c.mu.Lock()
			c.connected = true
			c.mu.Unlock()
		}
		return
	}

	split := msg.FrequencyRx > 0 && msg.FrequencyRx != msg.Frequency
	st := rigRadioState{
		FreqHz:    msg.Frequency,
		Mode:      strings.ToUpper(strings.TrimSpace(msg.Mode)),
		Power:     msg.Power,
		Radio:     strings.TrimSpace(msg.Radio),
		Split:     split,
		Online:    true,
		UpdatedAt: time.Now(),
	}
	if split {
		st.FreqRxHz = msg.FrequencyRx
		st.ModeRx = strings.ToUpper(strings.TrimSpace(msg.ModeRx))
	}

	c.mu.Lock()
	c.connected = true
	c.state = st
	c.mu.Unlock()
}

func (c *waveLogGateWS) setDisconnected() {
	c.mu.Lock()
	c.connected = false
	c.state.Online = false
	c.mu.Unlock()
}

// RadioState returns the cached radio status. The second return is false when no
// status has ever been received (so callers can omit the block entirely).
func (c *waveLogGateWS) RadioState() (rigRadioState, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	st := c.state
	st.Online = c.connected && st.FreqHz > 0
	return st, st.FreqHz > 0
}
