package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

func streamHandler(w http.ResponseWriter, r *http.Request) {
	target := strings.ToUpper(r.URL.Query().Get("target"))
	minutesStr := r.URL.Query().Get("minutes")
	surroundings := r.URL.Query().Get("surroundings") == "true"

	// Fallback for cached frontend clients that still send callsign/locator params
	if target == "" {
		if call := r.URL.Query().Get("callsign"); call != "" {
			target = strings.ToUpper(call)
		} else if loc := r.URL.Query().Get("locator"); loc != "" {
			target = strings.ToUpper(loc)
		}
	}

	if target == "" {
		http.Error(w, "target required", http.StatusBadRequest)
		return
	}

	minutes, err := strconv.Atoi(minutesStr)
	if err != nil || minutes <= 0 {
		minutes = 15
	}
	if minutes > 60 {
		minutes = 60
	}
	historySeconds := int64(minutes * 60)

	var targets []string
	if surroundings && isLocator(target) {
		targets = getSurroundingSquares(target)
	} else {
		targets = []string{target}
	}

	client := &Client{
		targets: targets,
		send:    make(chan Spot, 10000), // Buffer to handle initial history dump
	}

	hub.Lock()
	if maxClients > 0 && len(hub.clients) >= maxClients {
		hub.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		fmt.Fprintf(w, "event: server_error\ndata: Server is at capacity. Please try again later.\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return
	}
	hub.clients[client] = true
	logInfo("New client stream started for targets: %v (History: %d mins)", targets, minutes)

	now := time.Now().Unix()
	cutoff := now - historySeconds
	var historySpots []Spot

	idx := sort.Search(len(hub.history), func(i int) bool {
		return hub.history[i].T >= cutoff
	})

	for i := idx; i < len(hub.history); i++ {
		if spot, ok := matchAndCreateSpot(client, hub.history[i], now); ok {
			historySpots = append(historySpots, spot)
		}
	}
	hub.Unlock()

	defer func() {
		hub.Lock()
		if _, ok := hub.clients[client]; ok {
			delete(hub.clients, client)
			close(client.send)
		}
		hub.Unlock()
		logInfo("Client stream closed for targets: %v", targets)
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	var writer io.Writer = w
	var gz *gzip.Writer

	if compressStream && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		gz = gzip.NewWriter(w)
		writer = gz
		defer gz.Close()
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}

	for _, spot := range historySpots {
		b, _ := json.Marshal(spot)
		fmt.Fprintf(writer, "data: %s\n\n", string(b))
	}
	fmt.Fprintf(writer, "event: history_end\ndata: {}\n\n")

	if gz != nil {
		gz.Flush()
	}
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case spot, ok := <-client.send:
			if !ok {
				return
			}
			b, _ := json.Marshal(spot)
			fmt.Fprintf(writer, "data: %s\n\n", string(b))
			if gz != nil {
				gz.Flush()
			}
			flusher.Flush()
		}
	}
}

func statsHandler(w http.ResponseWriter, r *http.Request) {
	hub.RLock()
	numClients := len(hub.clients)
	historySize := len(hub.history)
	var historyMinutes int
	var historySizeBytes int64
	if historySize > 0 {
		oldest := hub.history[0].T
		now := time.Now().Unix()
		historyMinutes = int((now - oldest) / 60)
		for _, m := range hub.history {
			// Estimate memory footprint: base struct size (~112 bytes) + string lengths
			historySizeBytes += 112 + int64(len(m.SC)+len(m.SL)+len(m.RC)+len(m.RL)+len(m.B)+len(m.MD))
		}
	}
	hub.RUnlock()

	stats := struct {
		ActiveConnections int   `json:"active_connections"`
		HistorySize       int   `json:"history_size"`
		HistoryMinutes    int   `json:"history_minutes"`
		HistorySizeKB     int64 `json:"history_size_kb"`
	}{
		ActiveConnections: numClients,
		HistorySize:       historySize,
		HistoryMinutes:    historyMinutes,
		HistorySizeKB:     historySizeBytes / 1024,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func dxConditionsHandler(w http.ResponseWriter, r *http.Request) {
	target := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("target")))
	if target == "" {
		if call := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("callsign"))); call != "" {
			target = call
		} else if loc := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("locator"))); loc != "" {
			target = loc
		}
	}

	if target == "" {
		http.Error(w, "target required", http.StatusBadRequest)
		return
	}

	minutes := defaultDxWindowMinutes
	if raw := strings.TrimSpace(r.URL.Query().Get("minutes")); raw != "" {
		if m, err := strconv.Atoi(raw); err == nil && m > 0 {
			minutes = m
		}
	}
	if minutes > maxDxWindowMinutes {
		minutes = maxDxWindowMinutes
	}

	surroundings := r.URL.Query().Get("surroundings") == "true"

	hub.RLock()
	historyCopy := make([]MQTTMessage, len(hub.history))
	copy(historyCopy, hub.history)
	hub.RUnlock()

	resp := dxConditionsResponse{
		Target:          target,
		Surroundings:    surroundings,
		WindowMinutes:   minutes,
		CurrentHourOfWk: utcHourOfWeek(time.Now().Unix()),
		GeneratedAt:     time.Now().Unix(),
		BaselineBuckets: 0,
		OverallScore:    0,
		Confidence:      0,
		Condition:       "Poor",
		BestBands:       []string{},
		Bands:           []dxBandCondition{},
	}

	if dxBaseline != nil {
		resp = dxBaseline.Evaluate(target, surroundings, minutes, historyCopy, time.Now().Unix())
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// noCache is a middleware that sets headers to prevent caching of static files.
// This is useful for development to ensure the latest files are always served.
func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate") // HTTP 1.1.
		w.Header().Set("Pragma", "no-cache")                                   // HTTP 1.0.
		w.Header().Set("Expires", "0")                                         // Proxies.
		h.ServeHTTP(w, r)
	})
}
