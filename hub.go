package main

import (
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
	history []MQTTMessage
}

var hub = &Hub{
	clients: make(map[*Client]bool),
	history: make([]MQTTMessage, 0),
}

func (h *Hub) broadcastMsg(m MQTTMessage) {
	h.Lock()
	h.history = append(h.history, m)
	clients := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.Unlock()

	now := time.Now().Unix()
	for _, client := range clients {
		if spot, ok := matchAndCreateSpot(client, m, now); ok {
			safeSend(client.send, spot)
		}
	}
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
