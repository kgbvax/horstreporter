package main

import (
	"testing"
	"time"
)

// newTestUltrabeamClient builds a client with no real broker, with the broker
// liveness overridable so Online() logic can be tested without MQTT.
func newTestUltrabeamClient(connected bool) *ultrabeamClient {
	uc := &ultrabeamClient{
		prefix:      defaultUltrabeamTopicPrefix,
		mode:        "forward",
		connectedFn: func() bool { return connected },
	}
	return uc
}

func TestApplyStatusFrequencyUpdatesMode(t *testing.T) {
	uc := newTestUltrabeamClient(true)
	uc.applyStatus("ubctrl/status/frequency", []byte(`{"frequency":21225,"band":"15m","mode":"reverse"}`))
	if got := uc.Status().Mode; got != "reverse" {
		t.Fatalf("mode = %q, want reverse", got)
	}
}

func TestApplyStatusAvailabilityTogglesOnline(t *testing.T) {
	uc := newTestUltrabeamClient(true)

	uc.applyStatus("ubctrl/status/availability", []byte("online"))
	if !uc.Online() {
		t.Fatalf("Online() = false after availability=online (connected), want true")
	}

	uc.applyStatus("ubctrl/status/availability", []byte("offline"))
	if uc.Online() {
		t.Fatalf("Online() = true after availability=offline, want false")
	}
}

func TestApplyStatusMalformedJSONIgnored(t *testing.T) {
	uc := newTestUltrabeamClient(true)
	uc.applyStatus("ubctrl/status/frequency", []byte(`{"frequency":21225,"mode":"bidirectional"}`))
	if got := uc.Status().Mode; got != "bidirectional" {
		t.Fatalf("setup: mode = %q, want bidirectional", got)
	}

	// Malformed payload must not panic and must leave prior state intact.
	uc.applyStatus("ubctrl/status/frequency", []byte(`{not json`))
	if got := uc.Status().Mode; got != "bidirectional" {
		t.Fatalf("mode = %q after malformed payload, want unchanged bidirectional", got)
	}

	// Empty mode is ignored too.
	uc.applyStatus("ubctrl/status/frequency", []byte(`{"frequency":21225,"mode":""}`))
	if got := uc.Status().Mode; got != "bidirectional" {
		t.Fatalf("mode = %q after empty-mode payload, want unchanged bidirectional", got)
	}
}

func TestApplyStatusUnknownModeCoercesToForward(t *testing.T) {
	uc := newTestUltrabeamClient(true)
	uc.applyStatus("ubctrl/status/frequency", []byte(`{"mode":"sideways"}`))
	if got := uc.Status().Mode; got != "forward" {
		t.Fatalf("mode = %q for unknown input, want forward (ubctrl coercion)", got)
	}
}

func TestApplyStatusIgnoresOutOfScopeTopics(t *testing.T) {
	uc := newTestUltrabeamClient(true)
	uc.applyStatus("ubctrl/status/frequency", []byte(`{"mode":"reverse"}`))
	// motors / raw topics are out of scope and must not disturb beam direction.
	uc.applyStatus("ubctrl/status/motors", []byte(`{"moving":true,"motor_bits":3}`))
	if got := uc.Status().Mode; got != "reverse" {
		t.Fatalf("mode = %q after out-of-scope topic, want unchanged reverse", got)
	}
}

func TestOnlineKeysOnConnectionNotRecency(t *testing.T) {
	// Disconnected: Online() is false even if availability last said online.
	disc := newTestUltrabeamClient(false)
	disc.applyStatus("ubctrl/status/availability", []byte("online"))
	if disc.Online() {
		t.Fatalf("Online() = true while disconnected, want false")
	}

	// Connected + availability online but NO recent message (idle-healthy):
	// still online — no false-offline from message recency.
	idle := newTestUltrabeamClient(true)
	idle.setAvailability(true)
	idle.mu.Lock()
	idle.updatedAt = time.Now().Add(-1 * time.Hour) // stale by recency, but healthy
	idle.mu.Unlock()
	if !idle.Online() {
		t.Fatalf("Online() = false for idle-healthy controller, want true (no recency staleness)")
	}
}

func TestPublishWhenDisconnectedReturnsError(t *testing.T) {
	// The concrete client must surface a disconnected broker as an error rather
	// than a silent QoS-0 loss — exercises the real guard, not the fake.
	uc := newTestUltrabeamClient(false) // connectedFn => false, client nil
	if err := uc.Publish("reverse"); err == nil {
		t.Fatal("Publish() returned nil while disconnected, want error")
	}
}

func TestNormalizeUltrabeamMode(t *testing.T) {
	cases := map[string]string{
		"reverse":       "reverse",
		"180":           "reverse",
		"180°":          "reverse",
		"backward":      "reverse",
		"bidirectional": "bidirectional",
		"bi-dir":        "bidirectional",
		"bidir":         "bidirectional",
		"forward":       "forward",
		"normal":        "forward",
		"":              "forward",
		"garbage":       "forward",
		"REVERSE":       "reverse",
		"  Reverse  ":   "reverse",
	}
	for in, want := range cases {
		if got := normalizeUltrabeamMode(in); got != want {
			t.Errorf("normalizeUltrabeamMode(%q) = %q, want %q", in, got, want)
		}
	}
}
