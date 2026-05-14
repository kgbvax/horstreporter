package main

import (
	"sync"
	"time"
)

type Client struct {
	targets []string
	send    chan Spot
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
	defer h.Unlock()

	h.history = append(h.history, m)

	now := time.Now().Unix()
	for client := range h.clients {
		if spot, ok := matchAndCreateSpot(client, m, now); ok {
			select {
			case client.send <- spot:
			default:
				// Client buffer full, spot dropped for this client
			}
		}
	}
}
