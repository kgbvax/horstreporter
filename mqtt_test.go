package main

import (
	"testing"
	"time"
)

// U3: tests for ingestPSKRMessage — the MQTT msgHandler body. Drives the
// production ingest entry (payload parse, topic backfill, FT8/FT4 fan-out to
// dxBaseline + hub) without a broker, per the pattern of
// ingest_footprint_test.go but with behavior assertions instead of
// performance accounting.

// isolateMQTTIngest wires a fresh baseline engine + empty hub for an ingest
// test and restores the originals afterwards.
func isolateMQTTIngest(t *testing.T) (engine *DxBaselineEngine) {
	t.Helper()
	hub.Lock()
	savedClients := hub.clients
	savedHistory := hub.history
	hub.clients = make(map[*Client]bool)
	hub.history = nil
	hub.Unlock()
	savedBaseline := dxBaseline
	dxBaseline = newDxBaselineEngine("")
	t.Cleanup(func() {
		hub.Lock()
		hub.clients = savedClients
		hub.history = savedHistory
		hub.Unlock()
		dxBaseline = savedBaseline
	})
	return dxBaseline
}

// topicPSKR builds a pskr/filter/v2 topic:
// pskr/filter/v2/{band}/{mode}/{senderCall}/{receiverCall}/{senderLocator}/{receiverLocator}
func pskrTopic(band, mode, sc, rc, sl, rl string) string {
	return "pskr/filter/v2/" + band + "/" + mode + "/" + sc + "/" + rc + "/" + sl + "/" + rl
}

func TestIngestPSKRMessageTopicBackfillHappyPath(t *testing.T) {
	isolateMQTTIngest(t)

	// Dominant real-feed shape: payload is only {t,rp}; band/mode/calls/
	// locators come from the topic.
	topic := pskrTopic("20m", "FT8", "DK1.ABC", "w1xyz", "JO62rq", "FN31pr")
	ingestPSKRMessage(topic, []byte(`{"t":1700000000,"rp":-10}`))

	hub.RLock()
	defer hub.RUnlock()
	if len(hub.history) != 1 {
		t.Fatalf("expected 1 ingested spot, got %d", len(hub.history))
	}
	m := hub.history[0]
	if m.B != "20m" {
		t.Errorf("B = %q, want 20m (topic backfill)", m.B)
	}
	if m.MD != "FT8" {
		t.Errorf("MD = %q, want FT8 (topic backfill, uppercased)", m.MD)
	}
	// Dots in topic-encoded callsigns are restored to slashes.
	if m.SC != "DK1/ABC" {
		t.Errorf("SC = %q, want DK1/ABC (dot→slash restore)", m.SC)
	}
	if m.RC != "W1XYZ" {
		t.Errorf("RC = %q, want W1XYZ (uppercased)", m.RC)
	}
	if m.SL != "JO62RQ" || m.RL != "FN31PR" {
		t.Errorf("SL/RL = %q/%q, want JO62RQ/FN31PR", m.SL, m.RL)
	}
	if m.T != 1700000000 {
		t.Errorf("T = %d, want 1700000000 (payload fast path)", m.T)
	}
	if m.RP != -10 {
		t.Errorf("RP = %d, want -10", m.RP)
	}
}

func TestIngestPSKRMessageObservesBaseline(t *testing.T) {
	engine := isolateMQTTIngest(t)

	topic := pskrTopic("20m", "FT8", "DK1ABC", "W1XYZ", "JO62rq", "FN31pr")
	ingestPSKRMessage(topic, []byte(`{"t":1700000000,"rp":-10}`))

	engine.mu.RLock()
	defer engine.mu.RUnlock()
	if engine.lastEventAt != 1700000000 {
		t.Fatalf("expected baseline engine to observe the FT8 spot (lastEventAt=1700000000), got %d", engine.lastEventAt)
	}
}

func TestIngestPSKRMessageModeFilterFT8FT4Only(t *testing.T) {
	engine := isolateMQTTIngest(t)

	// FT8 and FT4 fan out to the baseline + hub; every other mode is ignored
	// (PSKReporter carries all modes on pskr/filter/v2).
	ft8 := pskrTopic("20m", "FT8", "DK1ABC", "W1XYZ", "JO62", "FN31")
	ft4 := pskrTopic("40m", "FT4", "DK2ABC", "W2XYZ", "JO62", "FN31")
	cw := pskrTopic("40m", "CW", "DK3ABC", "W3XYZ", "JO62", "FN31")
	wspr := pskrTopic("40m", "wspr", "DK4ABC", "W4XYZ", "JO62", "FN31")

	ingestPSKRMessage(ft8, []byte(`{"t":1700000000,"rp":-8}`))
	ingestPSKRMessage(ft4, []byte(`{"t":1700000000,"rp":-9}`))
	ingestPSKRMessage(cw, []byte(`{"t":1700000000,"rp":-5}`))
	ingestPSKRMessage(wspr, []byte(`{"t":1700000000,"rp":-20}`))

	hub.RLock()
	defer hub.RUnlock()
	if len(hub.history) != 2 {
		t.Fatalf("expected only FT8+FT4 to reach hub.history, got %d entries", len(hub.history))
	}
	if hub.history[0].MD != "FT8" || hub.history[1].MD != "FT4" {
		t.Fatalf("unexpected ingested modes: %q, %q", hub.history[0].MD, hub.history[1].MD)
	}
	if hub.history[0].B != "20m" || hub.history[1].B != "40m" {
		t.Fatalf("unexpected bands: %q, %q", hub.history[0].B, hub.history[1].B)
	}

	engine.mu.RLock()
	defer engine.mu.RUnlock()
	if engine.lastEventAt != 1700000000 {
		t.Fatalf("baseline observed %d, want exactly the two FT8/FT4 spots", engine.lastEventAt)
	}
}

func TestIngestPSKRMessageDefaultsTToNow(t *testing.T) {
	isolateMQTTIngest(t)

	before := time.Now().Unix()
	ingestPSKRMessage(pskrTopic("20m", "FT8", "DK1ABC", "W1XYZ", "JO62", "FN31"), []byte(`{"rp":-8}`))
	hub.RLock()
	defer hub.RUnlock()
	if len(hub.history) != 1 {
		t.Fatalf("expected 1 spot, got %d", len(hub.history))
	}
	now := time.Now().Unix()
	if got := hub.history[0].T; got < before || got > now {
		t.Fatalf("T = %d, want defaulted to now [%d..%d]", got, before, now)
	}
}

func TestIngestPSKRMessageRejections(t *testing.T) {
	isolateMQTTIngest(t)

	cases := []struct {
		name    string
		topic   string
		payload []byte
	}{
		{
			name:    "malformed JSON payload dropped",
			topic:   pskrTopic("20m", "FT8", "DK1ABC", "W1XYZ", "JO62", "FN31"),
			payload: []byte(`{"t":1700000000,"rp":`),
		},
		{
			// Fieldless payload + truncated topic: the backfill cannot find
			// band/mode, so the spot is silently dropped.
			name:    "truncated topic with fieldless payload dropped",
			topic:   "pskr/filter/v2/20m",
			payload: []byte(`{"t":1700000000,"rp":-8}`),
		},
		{
			// Empty payload is not parseable JSON → dropped.
			name:    "empty payload dropped",
			topic:   pskrTopic("20m", "FT8", "DK1ABC", "W1XYZ", "JO62", "FN31"),
			payload: []byte{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ingestPSKRMessage(tc.topic, tc.payload)
			hub.RLock()
			defer hub.RUnlock()
			if len(hub.history) != 0 {
				t.Fatalf("expected no ingested spot, got %d: %+v", len(hub.history), hub.history)
			}
		})
	}
}

func TestIngestPSKRMessageUnknownTopicFieldsDoNotBackfill(t *testing.T) {
	isolateMQTTIngest(t)

	// The topic marks missing values as "unknown"; those parts must not
	// backfill (band/mode do, but callsigns/locators stay empty).
	ingestPSKRMessage("pskr/filter/v2/20m/FT8/unknown/unknown/unknown/unknown", []byte(`{"t":1700000000,"rp":-8}`))

	hub.RLock()
	defer hub.RUnlock()
	if len(hub.history) != 1 {
		t.Fatalf("expected the FT8 spot to be ingested, got %d", len(hub.history))
	}
	m := hub.history[0]
	if m.B != "20m" || m.MD != "FT8" {
		t.Errorf("B/MD = %q/%q, want 20m/FT8 from the topic", m.B, m.MD)
	}
	if m.SC != "" || m.RC != "" || m.SL != "" || m.RL != "" {
		t.Errorf("unknown topic parts leaked into fields: SC=%q RC=%q SL=%q RL=%q", m.SC, m.RC, m.SL, m.RL)
	}
}

func TestIngestPSKRMessageFullJSONPayload(t *testing.T) {
	isolateMQTTIngest(t)

	// Fully JSON-encoded spot: no topic backfill needed (the minority shape
	// on the real feed).
	topic := pskrTopic("unknown", "unknown", "unknown", "unknown", "unknown", "unknown")
	payload := []byte(`{"t":1700000000,"rp":-12,"b":"30m","md":"FT8","sc":"DL1ABC","rc":"K1XYZ","sl":"jo62","rl":"FN31"}`)
	ingestPSKRMessage(topic, payload)

	hub.RLock()
	defer hub.RUnlock()
	if len(hub.history) != 1 {
		t.Fatalf("expected 1 ingested spot, got %d", len(hub.history))
	}
	m := hub.history[0]
	if m.B != "30m" || m.MD != "FT8" || m.SC != "DL1ABC" || m.RC != "K1XYZ" || m.SL != "JO62" || m.RL != "FN31" {
		t.Fatalf("unexpected ingested spot: %+v", m)
	}
	if m.T != 1700000000 || m.RP != -12 {
		t.Fatalf("unexpected t/rp: %d/%d", m.T, m.RP)
	}
}

func TestIngestPSKRMessagePayloadOverridesTopicButBackfillsGaps(t *testing.T) {
	isolateMQTTIngest(t)

	// Mixed shape: payload carries some fields, the topic fills the rest.
	topic := pskrTopic("40m", "FT8", "DK1ABC", "W1XYZ", "unknown", "FN31")
	payload := []byte(`{"t":1700000000,"rp":-6,"b":"30m","sc":"DL9ABC","sl":"JO40"}`)
	ingestPSKRMessage(topic, payload)

	hub.RLock()
	defer hub.RUnlock()
	if len(hub.history) != 1 {
		t.Fatalf("expected 1 ingested spot, got %d", len(hub.history))
	}
	m := hub.history[0]
	if m.B != "30m" {
		t.Errorf("B = %q, want 30m (payload wins)", m.B)
	}
	if m.MD != "FT8" {
		t.Errorf("MD = %q, want FT8 (topic backfill)", m.MD)
	}
	if m.SC != "DL9ABC" {
		t.Errorf("SC = %q, want DL9ABC (payload wins)", m.SC)
	}
	if m.RC != "W1XYZ" {
		t.Errorf("RC = %q, want W1XYZ (topic backfill)", m.RC)
	}
	if m.SL != "JO40" {
		t.Errorf("SL = %q, want JO40 (payload wins)", m.SL)
	}
	if m.RL != "FN31" {
		t.Errorf("RL = %q, want FN31 (topic backfill)", m.RL)
	}
}