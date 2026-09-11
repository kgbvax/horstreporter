package main

import (
	"testing"
	"time"
)

// TestAgeClamped pins the spot-age clamp used by the WSPR broadcast path:
// future timestamps (clock skew) clamp to 0, past timestamps pass through.
func TestAgeClamped(t *testing.T) {
	const now = int64(1700000000)
	tests := []struct {
		name   string
		now, t int64
		want   int64
	}{
		{"spot in the past", now, now - 120, 120},
		{"spot right now", now, now, 0},
		{"spot in the future clamps to 0", now, now + 120, 0},
		{"zero timestamp clamps to 0", now, 0, now},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ageClamped(tc.now, tc.t); got != tc.want {
				t.Errorf("ageClamped(%d, %d) = %d, want %d", tc.now, tc.t, got, tc.want)
			}
		})
	}
}

// TestSafeSend verifies the non-blocking delivery contract: buffered delivery,
// drop when full (never blocks), and a silently absorbed send on a closed
// channel (client disconnecting concurrently).
func TestSafeSend(t *testing.T) {
	t.Run("delivers to a buffered channel", func(t *testing.T) {
		ch := make(chan Spot, 1)
		want := Spot{Band: "20m", Sender: "DK1ABC"}
		safeSend(ch, want)
		select {
		case got := <-ch:
			if got != want {
				t.Errorf("received %+v, want %+v", got, want)
			}
		default:
			t.Fatalf("spot was not delivered")
		}
	})

	t.Run("drops when the channel is full without blocking", func(t *testing.T) {
		ch := make(chan Spot, 1)
		safeSend(ch, Spot{Band: "20m"})
		done := make(chan struct{})
		go func() {
			defer close(done)
			safeSend(ch, Spot{Band: "40m"}) // must take the default branch
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("safeSend blocked on a full channel")
		}
		if len(ch) != 1 {
			t.Errorf("channel length = %d, want 1 (overflow spot dropped)", len(ch))
		}
	})

	t.Run("absorbs a send on a closed channel", func(t *testing.T) {
		ch := make(chan Spot, 1)
		close(ch)
		done := make(chan struct{})
		go func() {
			defer close(done)
			safeSend(ch, Spot{Band: "20m"}) // panic recovered inside safeSend
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("safeSend did not survive a closed channel")
		}
	})
}

// TestHubBroadcastMsg exercises the broadcast funnel: the message lands in the
// rolling history, a client whose qth matches receives a matched Spot, and a
// non-matching client receives nothing. Global state (hub, propBaseline) is
// swapped for the duration and restored afterwards, following the pattern in
// ingest_footprint_test.go / main_test.go.
func TestHubBroadcastMsg(t *testing.T) {
	savedHub := hub
	savedPropBaseline := propBaseline
	defer func() {
		hub = savedHub
		propBaseline = savedPropBaseline
	}()

	matched := &Client{
		qthSet: []string{"JO62"},
		send:   make(chan Spot, 4),
	}
	other := &Client{
		qthSet: []string{"VK3"},
		send:   make(chan Spot, 4),
	}
	hub = &Hub{
		clients: map[*Client]bool{matched: true, other: true},
		history: make([]MQTTMessage, 0),
	}
	// propBaseline.Observe is nil-safe; the engine isn't under test here.
	propBaseline = nil

	now := time.Now().Unix()
	hub.broadcastMsg(MQTTMessage{
		T:  now - 10,
		SC: "DK1ABC", RC: "W1XYZ",
		SL: "JO62AB", RL: "FN31AB",
		RP: -10, B: "20m", MD: "FT8",
	})

	if len(hub.history) != 1 {
		t.Fatalf("history length = %d, want 1", len(hub.history))
	}
	if got := hub.history[0].RL; got != "FN31AB" {
		t.Errorf("history[0].RL = %q, want FN31AB", got)
	}

	select {
	case spot := <-matched.send:
		if spot.Locator != "FN31AB" {
			t.Errorf("matched spot locator = %q, want FN31AB", spot.Locator)
		}
		if spot.Band != "20m" || spot.Sender != "DK1ABC" {
			t.Errorf("matched spot = %+v, want band/sender preserved", spot)
		}
	default:
		t.Fatalf("matching client received no spot")
	}

	if len(other.send) != 0 {
		t.Errorf("non-matching client received %d spots, want 0", len(other.send))
	}
}