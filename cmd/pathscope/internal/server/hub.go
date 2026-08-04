package server

import (
	"sync"
)

// Client is a single SSE subscriber on the Pathscope glance hub. The send
// channel carries the most recent GlanceResponse; a per-client buffer (4)
// absorbs short browser pauses without forcing the broadcaster to block.
// When the buffer fills, the broadcaster drops the OLDEST pending event
// (see safeSend below) — the matrix is fully derived state, so missing
// one event is invisible to the user as long as the next one arrives.
type Client struct {
	send chan GlanceResponse
}

// Hub fans a single in-flight GlanceResponse out to every subscribed
// browser. It is intentionally minimal — no history slice (late
// subscribers get the current payload synthesised on connect, not a
// replay), no per-client filter (every client sees the same payload), and
// no capacity cap (Pathscope is a single-user dashboard).
//
// The locking pattern (Lock → snapshot → Unlock → deliver without the
// lock) is borrowed from the main repo's hub.go so the two stay
// structurally consistent.
type Hub struct {
	sync.RWMutex
	clients map[*Client]bool
}

// NewHub returns an empty hub ready for Subscribe calls.
func NewHub() *Hub {
	return &Hub{clients: make(map[*Client]bool)}
}

// Subscribe registers a new client and returns it. The caller is
// responsible for calling Unsubscribe when the connection drops —
// typically via `defer hub.Unsubscribe(c)` in the HTTP handler.
func (h *Hub) Subscribe() *Client {
	c := &Client{send: make(chan GlanceResponse, 4)}
	h.Lock()
	h.clients[c] = true
	h.Unlock()
	return c
}

// Unsubscribe removes the client from the hub and closes its send
// channel. It is idempotent: a second call with the same client is a
// no-op (no panic on close-of-closed-channel, no double-delete). This
// matters because the HTTP handler's `defer` will fire even if the loop
// exits via `case g, ok := <-client.send: if !ok { return }`.
func (h *Hub) Unsubscribe(c *Client) {
	h.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
	}
	h.Unlock()
}

// Broadcast delivers one GlanceResponse to every subscribed client. The
// snapshot-then-deliver pattern means a slow client can never block the
// ticker; it just gets dropped events until its consumer catches up.
func (h *Hub) Broadcast(g GlanceResponse) {
	h.RLock()
	clients := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.RUnlock()

	for _, c := range clients {
		safeSend(c.send, g)
	}
}

// ClientCount returns the number of currently subscribed clients. Useful
// for the SSE handler's initial-event fallback and for any operator-side
// observability.
func (h *Hub) ClientCount() int {
	h.RLock()
	defer h.RUnlock()
	return len(h.clients)
}

// safeSend delivers one GlanceResponse to a client channel without
// blocking and without panicking if the channel was closed concurrently
// by Unsubscribe. The recover() guards the close-of-closed case; the
// default branch handles the full-buffer case by dropping the message.
//
// Why drop-on-full rather than block-on-full: a paused browser can fill
// its buffer in seconds; if the broadcaster blocks, the ticker falls
// behind. The next broadcast will replace the dropped one with a fresh
// payload, so the user sees a slightly stale matrix catch up — no
// visual artefact.
func safeSend(ch chan GlanceResponse, g GlanceResponse) {
	defer func() { recover() }()
	select {
	case ch <- g:
	default:
	}
}