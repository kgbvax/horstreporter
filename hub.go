package main

import (
	"strconv"
	"sync"
	"time"
)

type Client struct {
	qthSet []string
	send   chan Spot

	// Optional "area of interest" filter (a configurable-size region around a
	// home square), used by region feeds such as horstprop. When areaActive is
	// true a spot also matches if its sender or receiver locator falls within
	// areaRings grid-squares (Chebyshev distance) of (areaX, areaY). This is
	// additive to qthSet and is inert (no behaviour change) when areaActive
	// is false.
	areaActive bool
	areaX      int
	areaY      int
	areaRings  int
}

type Hub struct {
	sync.RWMutex
	clients map[*Client]bool
	// history is append-only: elements are never written in place. New spots
	// are appended, expired ones are dropped by reslicing the front
	// (dropFrontLocked) and the startup backfill swaps the whole slice
	// (replaceHistoryLocked). That makes windowFromLocked's zero-copy views
	// safe to read without the lock — a concurrent append writes past the
	// view's length (or into a fresh backing array), never into the view.
	history []MQTTMessage
	// baseSeq is the count of messages that were in history before
	// history[0]: history[i] has sequence number baseSeq+i+1 (1-based, so a
	// client's "last seen" of 0 means nothing seen). It lets resuming clients
	// ask for "everything after seq N" without a per-message field.
	baseSeq uint64
}

// hubEpoch identifies this process's sequence space. Sequence numbers restart
// after a process restart, so resume ids carry the epoch and a mismatch falls
// back to a full history dump.
var hubEpoch = strconv.FormatInt(time.Now().UnixNano(), 36)

// highSeqLocked is the sequence number of the newest history message (0 when
// nothing has ever been recorded). Caller holds h's lock.
func (h *Hub) highSeqLocked() uint64 { return h.baseSeq + uint64(len(h.history)) }

// dropFrontLocked removes the n oldest history messages, keeping the
// sequence numbers of the remainder stable. Caller holds the write lock.
func (h *Hub) dropFrontLocked(n int) {
	if n <= 0 {
		return
	}
	if n >= len(h.history) {
		h.baseSeq += uint64(len(h.history))
		h.history = make([]MQTTMessage, 0)
		return
	}
	h.baseSeq += uint64(n)
	h.history = h.history[n:]
}

// windowFromLocked returns history[idx:] as a read-only view without copying.
// The capacity is clamped so an append to the view can never write into the
// hub's backing array. The caller must not modify the elements; it may keep
// the view after releasing the lock (see the history invariant above) — it
// only pins the old backing array for as long as it holds it. Caller holds
// the lock (read or write).
func (h *Hub) windowFromLocked(idx int) []MQTTMessage {
	n := len(h.history)
	return h.history[idx:n:n]
}

// replaceHistoryLocked swaps in a new history (startup backfill). The old
// sequence range is retired so ids never repeat. Caller holds the write lock.
func (h *Hub) replaceHistoryLocked(merged []MQTTMessage) {
	h.baseSeq += uint64(len(h.history))
	h.history = merged
}

var hub = &Hub{
	clients: make(map[*Client]bool),
	history: make([]MQTTMessage, 0),
}

// propBaseline is the unified per-source band × region climatology engine
// (prop_baseline.go), observed by the broadcast funnel so every ingest
// contributes. Separate engine + table from the v1 WsprClimatologyEngine —
// do NOT merge the Observe paths (WSPR counts would double).
var propBaseline *propBaselineEngine

func (h *Hub) broadcastMsg(m MQTTMessage) {
	h.Lock()
	h.history = append(h.history, m)
	seq := h.highSeqLocked()
	propBaseline.Observe(m)
	clients := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.Unlock()

	now := time.Now().Unix()
	for _, client := range clients {
		if spot, ok := matchAndCreateSpot(client, m, now); ok {
			spot.Seq = seq
			safeSend(client.send, spot)
		}
	}
}

// ageClamped returns the age in seconds, clamped to ≥0.
func ageClamped(now, t int64) int64 {
	age := now - t
	if age < 0 {
		return 0
	}
	return age
}

// safeSend delivers a spot to a client channel without blocking.
// A closed channel (client disconnecting concurrently) is silently absorbed.
func safeSend(ch chan Spot, spot Spot) {
	defer func() { recover() }()
	select {
	case ch <- spot:
	default:
	}
}
