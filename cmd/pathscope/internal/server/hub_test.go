package server

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestHubBroadcastDelivers confirms every subscribed client receives a
// single broadcast.
func TestHubBroadcastDelivers(t *testing.T) {
	t.Parallel()
	h := NewHub()
	clients := []*Client{h.Subscribe(), h.Subscribe(), h.Subscribe()}
	h.Broadcast(GlanceResponse{ObservedAt: time.Unix(1, 0)})
	for i, c := range clients {
		select {
		case got := <-c.send:
			if got.ObservedAt.Unix() != 1 {
				t.Fatalf("client %d got unexpected payload: %+v", i, got)
			}
		default:
			t.Fatalf("client %d did not receive the broadcast", i)
		}
	}
	// No further broadcasts — each channel should be empty.
	for i, c := range clients {
		if got := len(c.send); got != 0 {
			t.Fatalf("client %d has %d extra messages buffered", i, got)
		}
	}
}

// TestHubUnsubscribeIdempotent confirms a second Unsubscribe call with
// the same client does not panic on close-of-closed-channel or on a
// deleted-map-key double delete.
func TestHubUnsubscribeIdempotent(t *testing.T) {
	t.Parallel()
	h := NewHub()
	c := h.Subscribe()
	h.Unsubscribe(c)
	h.Unsubscribe(c) // must not panic
	h.Unsubscribe(c) // must not panic
	if n := h.ClientCount(); n != 0 {
		t.Fatalf("expected 0 clients after unsubscribe, got %d", n)
	}
}

// TestHubBroadcastDropsWhenFull confirms the broadcaster does not block
// when a subscriber's channel is full — the message is dropped and the
// loop continues.
func TestHubBroadcastDropsWhenFull(t *testing.T) {
	t.Parallel()
	h := NewHub()
	c := h.Subscribe()
	// Fill the buffer (capacity 4).
	for i := 0; i < 4; i++ {
		h.Broadcast(GlanceResponse{ObservedAt: time.Unix(int64(i), 0)})
	}
	// One more broadcast must not block. Run it in a goroutine with a
	// short timer to surface any hang.
	done := make(chan struct{})
	go func() {
		h.Broadcast(GlanceResponse{ObservedAt: time.Unix(99, 0)})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("broadcast blocked when subscriber channel was full")
	}
	// Channel should still hold 4 items (oldest events kept, newest
	// dropped). This documents the "drop new" policy: a paused browser
	// gets its oldest data, not the freshest.
	if got := len(c.send); got != 4 {
		t.Fatalf("expected 4 buffered items after overflow, got %d", got)
	}
}

// TestSafeSendClosedChannelAbsorbed confirms safeSend doesn't panic when
// the channel was closed concurrently.
func TestSafeSendClosedChannelAbsorbed(t *testing.T) {
	t.Parallel()
	ch := make(chan GlanceResponse, 1)
	close(ch)
	// This must not panic. safeSend's defer recover() is the guard.
	safeSend(ch, GlanceResponse{ObservedAt: time.Unix(1, 0)})
}

// TestHubBroadcastConcurrent is the -race guard. It drives many
// producers and consumers at the same hub and verifies no panic, no
// race, and a sensible delivery count.
func TestHubBroadcastConcurrent(t *testing.T) {
	t.Parallel()
	h := NewHub()
	const (
		nSubs      = 10
		nProducers = 4
		nPerProd   = 250
	)
	subs := make([]*Client, nSubs)
	for i := range subs {
		subs[i] = h.Subscribe()
	}
	var received int64
	var consumerWG sync.WaitGroup
	consumerWG.Add(nSubs)
	for _, c := range subs {
		go func(c *Client) {
			defer consumerWG.Done()
			for range c.send {
				atomic.AddInt64(&received, 1)
			}
		}(c)
	}
	var producerWG sync.WaitGroup
	producerWG.Add(nProducers)
	for p := 0; p < nProducers; p++ {
		go func() {
			defer producerWG.Done()
			for i := 0; i < nPerProd; i++ {
				h.Broadcast(GlanceResponse{ObservedAt: time.Unix(int64(i), 0)})
			}
		}()
	}
	// Wait for producers, then close every channel — the consumers
	// exit via `for range` on a closed channel.
	producerWG.Wait()
	for _, c := range subs {
		h.Unsubscribe(c)
	}
	// Bounded wait on the consumers — a regression surfaces here as a
	// real test failure instead of a hung test binary.
	done := make(chan struct{})
	go func() { consumerWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("consumer goroutines did not exit after Unsubscribe")
	}
	if atomic.LoadInt64(&received) == 0 {
		t.Fatalf("no deliveries observed under concurrent broadcast")
	}
}