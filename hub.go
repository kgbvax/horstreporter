package main

import (
	"strings"
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

// broadcastWSPRToAll sends a WSPR spot to every connected client regardless of
// their QTH. WSPR is a global propagation reference — it shows which bands
// have paths open right now, not just paths involving the operator's own
// station. The client-side show-wspr-spots toggle lets users hide them.
func (h *Hub) broadcastWSPRToAll(m MQTTMessage) {
	h.Lock()
	h.history = append(h.history, m)
	clients := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.Unlock()

	now := time.Now().Unix()
	for _, client := range clients {
		spot := Spot{
			Lat:             0,
			Lng:             0,
			SNR:             m.RP,
			AgeSeconds:      ageClamped(now, m.T),
			Locator:         strings.ToUpper(strings.TrimSpace(m.RL)),
			ReporterLocator: strings.ToUpper(strings.TrimSpace(m.SL)),
			SourceType:      "wspr",
			Band:            m.B,
			Sender:          m.SC,
			Receiver:        m.RC,
		}
		lat, lng := locatorToLatLng(spot.Locator)
		spot.Lat = lat
		spot.Lng = lng
		safeSend(client.send, spot)
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
