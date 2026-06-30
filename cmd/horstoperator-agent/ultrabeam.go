package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// beamController abstracts the UltraBeam control surface so the HTTP handlers
// depend on an interface rather than the concrete MQTT client. This mirrors
// rigController (rig.go) and lets handleBeam be unit-tested with a fake that
// records published payloads, with no live broker.
type beamController interface {
	// Publish sets the UltraBeam beam direction by publishing a canonical
	// ubctrl mode (forward|reverse|bidirectional) to <prefix>/command/mode.
	// It returns an error when the command could not be enqueued on a live
	// broker connection (so the caller can report "command not sent" rather
	// than a false success — MQTT QoS 0 is otherwise fire-and-forget).
	Publish(mode string) error
	// Status returns a snapshot of the last-known UltraBeam state.
	Status() ultrabeamState
	// Online reports whether the agent currently has a live view of the
	// UltraBeam (broker connected AND last availability not offline).
	Online() bool
}

// ultrabeamState is an immutable snapshot of the UltraBeam's last-known state,
// served over /v1/antenna/state and used for the capability gate.
type ultrabeamState struct {
	// Mode is the canonical ubctrl beam direction: forward|reverse|bidirectional.
	Mode string
	// Online is the derived liveness: broker connected AND availability online.
	Online bool
	// UpdatedAt is when a status message last refreshed the cache. Surfaced for
	// diagnostics only — it is NOT used to flip Online, because ubctrl status is
	// change-driven (an idle healthy antenna emits nothing) so message recency
	// cannot distinguish idle-healthy from dead.
	UpdatedAt time.Time
	// LastErr holds the last controller error string (ubctrl status/raw.last_error).
	LastErr string
}

const defaultUltrabeamTopicPrefix = "ubctrl"

// ultrabeamClient is the agent's MQTT client for the UltraBeam RCU-06 antenna
// controller. It subscribes to retained status topics, caches the current beam
// direction + availability, and publishes beam-direction commands. It is the
// repo's first MQTT publisher; it mirrors the backend's client-options idiom
// (mqtt.go) but adds broker URL + auth, which the backend never needed.
type ultrabeamClient struct {
	client mqtt.Client
	prefix string

	// connectedFn reports broker liveness; defaults to client.IsConnected but is
	// overridable in tests so Online() logic can be exercised without a broker.
	connectedFn func() bool

	mu                 sync.RWMutex
	mode               string
	availabilityOnline bool
	updatedAt          time.Time
	lastErr            string
}

// resolveUltrabeamPrefix returns the configured ubctrl topic prefix, falling
// back to the default. Pure (no client), so it is unit-testable without dialing.
func resolveUltrabeamPrefix(cfg serviceConfig) string {
	prefix := strings.TrimRight(strings.TrimSpace(cfg.UBTopicPrefix), "/")
	if prefix == "" {
		return defaultUltrabeamTopicPrefix
	}
	return prefix
}

// newUltrabeamClient builds and (asynchronously) connects an UltraBeam MQTT
// client from config. Connection is non-blocking: paho's connect-retry +
// auto-reconnect keep trying in the background so a broker that is down at
// startup never blocks the agent or its existing rotor/rig behavior.
func newUltrabeamClient(cfg serviceConfig) *ultrabeamClient {
	uc := &ultrabeamClient{
		prefix: resolveUltrabeamPrefix(cfg),
		mode:   "forward",
	}

	clientID := strings.TrimSpace(cfg.UBClientID)
	if clientID == "" {
		// Must differ from ubctrl's own client ID (default "ubctrl"); two clients
		// sharing one ID repeatedly disconnect each other (ubctrl API §1).
		clientID = fmt.Sprintf("horstoperator-%d", time.Now().UnixNano())
	}

	opts := mqtt.NewClientOptions().AddBroker(strings.TrimSpace(cfg.UBBrokerURL))
	opts.SetClientID(clientID)
	opts.SetCleanSession(true)
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true)
	opts.SetConnectRetryInterval(10 * time.Second)
	if u := strings.TrimSpace(cfg.UBUsername); u != "" {
		opts.SetUsername(u)
	}
	if cfg.UBPassword != "" {
		opts.SetPassword(cfg.UBPassword)
	}
	opts.SetOnConnectHandler(func(c mqtt.Client) {
		// Subscribe in the connect handler so a reconnect re-subscribes. Retained
		// status topics deliver current values immediately (ubctrl API §6).
		topic := uc.prefix + "/status/#"
		if token := c.Subscribe(topic, 0, uc.handleMessage); token.Wait() && token.Error() != nil {
			uc.setLastErr(fmt.Sprintf("subscribe %s: %v", topic, token.Error()))
		}
	})
	opts.SetConnectionLostHandler(func(c mqtt.Client, err error) {
		uc.setLastErr(fmt.Sprintf("connection lost: %v", err))
	})

	uc.client = mqtt.NewClient(opts)
	uc.connectedFn = uc.client.IsConnected
	uc.client.Connect() // non-blocking; connect-retry handles a down broker

	return uc
}

// handleMessage routes an incoming status message to the cache.
func (c *ultrabeamClient) handleMessage(_ mqtt.Client, msg mqtt.Message) {
	c.applyStatus(msg.Topic(), msg.Payload())
}

// applyStatus updates the cache from one status topic. Split out from
// handleMessage so it is unit-testable without an mqtt.Message. Topics other
// than frequency/availability (raw, motors) are intentionally ignored — only
// beam direction is in scope.
func (c *ultrabeamClient) applyStatus(topic string, payload []byte) {
	switch topic {
	case c.prefix + "/status/frequency":
		var p struct {
			Mode string `json:"mode"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return // ignore malformed; keep prior cached state
		}
		if strings.TrimSpace(p.Mode) == "" {
			return
		}
		c.setMode(normalizeUltrabeamMode(p.Mode))
	case c.prefix + "/status/availability":
		v := strings.ToLower(strings.TrimSpace(string(payload)))
		c.setAvailability(v == "online")
	}
}

func (c *ultrabeamClient) setMode(mode string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mode = mode
	c.updatedAt = time.Now()
}

func (c *ultrabeamClient) setAvailability(online bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.availabilityOnline = online
	c.updatedAt = time.Now()
}

func (c *ultrabeamClient) setLastErr(msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastErr = msg
}

func (c *ultrabeamClient) connected() bool {
	if c.connectedFn == nil {
		return false
	}
	return c.connectedFn()
}

// Status returns a snapshot of the cached UltraBeam state. Online is derived
// from broker connection state plus last availability — never from message
// recency (see ultrabeamState.UpdatedAt).
func (c *ultrabeamClient) Status() ultrabeamState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return ultrabeamState{
		Mode:      c.mode,
		Online:    c.connected() && c.availabilityOnline,
		UpdatedAt: c.updatedAt,
		LastErr:   c.lastErr,
	}
}

func (c *ultrabeamClient) Online() bool {
	return c.Status().Online
}

// Publish sends a canonical beam-direction command to <prefix>/command/mode.
// It waits on the publish token so a dropped broker connection surfaces as an
// error instead of a silent QoS-0 loss.
func (c *ultrabeamClient) Publish(mode string) error {
	canonical := normalizeUltrabeamMode(mode)
	if c.client == nil || !c.connected() {
		return fmt.Errorf("ultrabeam broker not connected")
	}
	token := c.client.Publish(c.prefix+"/command/mode", 0, false, canonical)
	if !token.WaitTimeout(2 * time.Second) {
		return fmt.Errorf("ultrabeam publish timed out")
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("ultrabeam publish failed: %w", err)
	}
	return nil
}

// normalizeUltrabeamMode maps any input (UI labels, aliases, ubctrl values) to
// a canonical ubctrl beam direction: forward|reverse|bidirectional. Unlike the
// agent's normalizeMode (which collapses reverse->backward for the PSTrotator),
// this keeps "reverse" canonical for the UltraBeam contract. Unknown values
// coerce to forward, mirroring ubctrl's own coercion (API §4.2).
func normalizeUltrabeamMode(raw string) string {
	mode, _ := parseUltrabeamMode(raw)
	return mode
}

// parseUltrabeamMode resolves an input to a canonical ubctrl beam direction and
// reports whether the input was a recognized token. Command handlers use the
// ok flag to reject unknown input with 400 rather than silently coercing it to
// forward; status parsing ignores ok and takes ubctrl's coerce-to-forward rule.
func parseUltrabeamMode(raw string) (mode string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "reverse", "180", "180°", "back", "backward":
		return "reverse", true
	case "bidirectional", "bidir", "bi", "bi-directional", "bi-dir":
		return "bidirectional", true
	case "forward", "normal":
		return "forward", true
	default:
		return "forward", false
	}
}
