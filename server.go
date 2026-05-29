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

type countingResponseWriter struct {
	http.ResponseWriter
	bytesWritten int64
}

func (w *countingResponseWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.bytesWritten += int64(n)
	return n, err
}

func (w *countingResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

type streamSpot struct {
	Lat        float64 `json:"lat"`
	Lng        float64 `json:"lng"`
	SNR        int     `json:"snr"`
	AgeSeconds int64   `json:"ageSeconds"`
	Locator    string  `json:"locator"`
	Band       string  `json:"band"`
}

type squareDetailReport struct {
	Sender   string `json:"sender"`
	Receiver string `json:"receiver"`
	Band     string `json:"band"`
	SNR      int    `json:"snr"`
}

type squareDetailsResponse struct {
	Locator    string               `json:"locator"`
	Count      int                  `json:"count"`
	MinSNR     int                  `json:"min_snr"`
	MaxSNR     int                  `json:"max_snr"`
	AvgSNR     float64              `json:"avg_snr"`
	BestBand   string               `json:"best_band"`
	BandCounts map[string]int       `json:"band_counts,omitempty"`
	TopReports []squareDetailReport `json:"top_reports"`
}

func streamHandler(w http.ResponseWriter, r *http.Request) {
	target, surroundings := resolveTargetQuery(r)
	minutesStr := r.URL.Query().Get("minutes")

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
	streamAccounting.startSession()
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

	var cw *countingResponseWriter
	defer func() {
		if cw != nil {
			streamAccounting.completeSession(cw.bytesWritten)
		}
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

	cw = &countingResponseWriter{ResponseWriter: w}
	var writer io.Writer = cw
	var gz *gzip.Writer

	if compressStream && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		gz = gzip.NewWriter(cw)
		writer = gz
		defer gz.Close()
	}

	for _, spot := range historySpots {
		b, _ := json.Marshal(toStreamSpot(spot))
		fmt.Fprintf(writer, "data: %s\n\n", string(b))
	}
	fmt.Fprintf(writer, "event: history_end\ndata: {}\n\n")

	if gz != nil {
		gz.Flush()
	}
	cw.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case spot, ok := <-client.send:
			if !ok {
				return
			}
			b, _ := json.Marshal(toStreamSpot(spot))
			fmt.Fprintf(writer, "data: %s\n\n", string(b))
			if gz != nil {
				gz.Flush()
			}
			cw.Flush()
		}
	}
}

func squareDetailsHandler(w http.ResponseWriter, r *http.Request) {
	target, surroundings := resolveTargetQuery(r)
	locator := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("locator")))
	if locator == "" {
		http.Error(w, "locator required", http.StatusBadRequest)
		return
	}
	if !isLocator(locator) {
		http.Error(w, "valid locator required", http.StatusBadRequest)
		return
	}

	minutes, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("minutes")))
	if err != nil || minutes <= 0 {
		minutes = 15
	}
	if minutes > 60 {
		minutes = 60
	}

	minSnrMode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("min_snr_mode")))
	ssbMinDb := parseIntDefault(r.URL.Query().Get("ssb_min_db"), 0)
	cwMinDb := parseIntDefault(r.URL.Query().Get("cw_min_db"), -15)
	selectedBand := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("selected_band")))
	enabledBands := parseEnabledBands(r.URL.Query().Get("enabled_bands"))

	hub.RLock()
	historyCopy := make([]MQTTMessage, len(hub.history))
	copy(historyCopy, hub.history)
	hub.RUnlock()

	resp := buildSquareDetailsResponse(target, surroundings, locator, minutes, minSnrMode, ssbMinDb, cwMinDb, selectedBand, enabledBands, historyCopy, time.Now().Unix())
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func resolveTargetQuery(r *http.Request) (string, bool) {
	target := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("target")))
	surroundings := r.URL.Query().Get("surroundings") == "true"
	if target == "" {
		if call := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("callsign"))); call != "" {
			target = call
		} else if loc := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("locator"))); loc != "" {
			target = loc
		}
	}
	return target, surroundings
}

func toStreamSpot(spot Spot) streamSpot {
	return streamSpot{
		Lat:        spot.Lat,
		Lng:        spot.Lng,
		SNR:        spot.SNR,
		AgeSeconds: spot.AgeSeconds,
		Locator:    spot.Locator,
		Band:       spot.Band,
	}
}

func parseIntDefault(raw string, fallback int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
		return v
	}
	return fallback
}

func parseEnabledBands(raw string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		band := strings.ToLower(strings.TrimSpace(part))
		if band == "" {
			continue
		}
		set[band] = struct{}{}
	}
	return set
}

func bandAllowed(band, selectedBand string, enabledBands map[string]struct{}) bool {
	if selectedBand != "" && selectedBand != "all" && band != selectedBand {
		return false
	}
	if len(enabledBands) == 0 {
		return true
	}
	_, ok := enabledBands[strings.ToLower(strings.TrimSpace(band))]
	return ok
}

func buildSquareDetailsResponse(target string, surroundings bool, locator string, minutes int, minSnrMode string, ssbMinDb, cwMinDb int, selectedBand string, enabledBands map[string]struct{}, history []MQTTMessage, now int64) squareDetailsResponse {
	resp := squareDetailsResponse{
		Locator:    locator,
		BandCounts: make(map[string]int),
		TopReports: []squareDetailReport{},
	}

	if target == "" || locator == "" {
		return resp
	}

	targets := []string{target}
	if surroundings && isLocator(target) {
		targets = getSurroundingSquares(target)
	}

	client := &Client{targets: targets}
	cutoff := now - int64(minutes*60)
	seenPairs := make(map[string]struct{})
	var sumSNR int
	first := true
	type report struct {
		sender   string
		receiver string
		band     string
		snr      int
	}
	var reports []report

	for _, m := range history {
		if m.T < cutoff || m.T > now {
			continue
		}
		if minSnrMode == "ssb" && m.RP < ssbMinDb {
			continue
		}
		if minSnrMode == "cw" && m.RP < cwMinDb {
			continue
		}
		if !bandAllowed(strings.ToLower(strings.TrimSpace(m.B)), selectedBand, enabledBands) {
			continue
		}

		spot, ok := matchAndCreateSpot(client, m, now)
		if !ok || !strings.HasPrefix(strings.ToUpper(spot.Locator), locator) {
			continue
		}

		if first {
			resp.MinSNR = spot.SNR
			resp.MaxSNR = spot.SNR
			first = false
		} else {
			if spot.SNR < resp.MinSNR {
				resp.MinSNR = spot.SNR
			}
			if spot.SNR > resp.MaxSNR {
				resp.MaxSNR = spot.SNR
			}
		}
		resp.Count++
		sumSNR += spot.SNR
		resp.BandCounts[spot.Band]++
		pairKey := spot.Sender + "|" + spot.Receiver + "|" + spot.Band
		if _, exists := seenPairs[pairKey]; exists {
			continue
		}
		seenPairs[pairKey] = struct{}{}
		reports = append(reports, report{sender: spot.Sender, receiver: spot.Receiver, band: spot.Band, snr: spot.SNR})
	}

	if resp.Count == 0 {
		resp.MinSNR = 0
		resp.MaxSNR = 0
		resp.BestBand = ""
		resp.TopReports = []squareDetailReport{}
		return resp
	}

	resp.AvgSNR = float64(sumSNR) / float64(resp.Count)
	bestBand := ""
	bestCount := 0
	for band, count := range resp.BandCounts {
		if count > bestCount {
			bestBand = band
			bestCount = count
		}
	}
	resp.BestBand = bestBand

	sort.Slice(reports, func(i, j int) bool {
		if reports[i].snr == reports[j].snr {
			return reports[i].band < reports[j].band
		}
		return reports[i].snr > reports[j].snr
	})
	for i := 0; i < len(reports) && i < 10; i++ {
		resp.TopReports = append(resp.TopReports, squareDetailReport{
			Sender:   reports[i].sender,
			Receiver: reports[i].receiver,
			Band:     reports[i].band,
			SNR:      reports[i].snr,
		})
	}
	return resp
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
	started, completed, active, bytesTotal, avgBytes := streamAccounting.snapshot()
	dxBaselineEventCount := 0
	dxBaselineHistoryMinutes := 0
	if dxBaseline != nil {
		_, dxBaselineEventCount, dxBaselineHistoryMinutes = dxBaseline.Stats(time.Now().Unix())
	}

	stats := struct {
		ActiveConnections    int     `json:"active_connections"`
		HistorySize          int     `json:"history_size"`
		HistoryMinutes       int     `json:"history_minutes"`
		HistoryRetentionMins int     `json:"history_retention_minutes"`
		HistorySizeKB        int64   `json:"history_size_kb"`
		SessionsTotal        int64   `json:"sessions_total"`
		SessionsCompleted    int64   `json:"sessions_completed"`
		SessionsActive       int64   `json:"sessions_active"`
		SessionBytesTotal    int64   `json:"session_bytes_total"`
		SessionBytesAvg      float64 `json:"session_bytes_avg"`
		DxBaselineEventCount int     `json:"dx_baseline_event_count"`
		DxBaselineHistoryM   int     `json:"dx_baseline_history_minutes"`
		DxBaselineMaxEvents  int     `json:"dx_baseline_max_events"`
	}{
		ActiveConnections:    numClients,
		HistorySize:          historySize,
		HistoryMinutes:       historyMinutes,
		HistoryRetentionMins: liveHistoryRetentionMinutes,
		HistorySizeKB:        historySizeBytes / 1024,
		SessionsTotal:        started,
		SessionsCompleted:    completed,
		SessionsActive:       active,
		SessionBytesTotal:    bytesTotal,
		SessionBytesAvg:      avgBytes,
		DxBaselineEventCount: dxBaselineEventCount,
		DxBaselineHistoryM:   dxBaselineHistoryMinutes,
		DxBaselineMaxEvents:  dxBaselineMaxEvents,
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

	cwMinDb := defaultDxCwViableMinDb
	if raw := strings.TrimSpace(r.URL.Query().Get("cw_min_db")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil {
			cwMinDb = v
		}
	}

	surroundings := r.URL.Query().Get("surroundings") == "true"

	hub.RLock()
	historyCopy := make([]MQTTMessage, len(hub.history))
	copy(historyCopy, hub.history)
	hub.RUnlock()

	resp := dxConditionsResponse{
		Target:           target,
		Surroundings:     surroundings,
		WindowMinutes:    minutes,
		CwMinDb:          cwMinDb,
		CurrentSlotOfDay: utcSlotOfDay(time.Now().Unix()),
		GeneratedAt:      time.Now().Unix(),
		BaselineBuckets:  0,
		BaselineEventCnt: 0,
		BaselineHistoryM: 0,
		OverallScore:     0,
		Confidence:       0,
		Condition:        "Poor",
		BestBands:        []string{},
		Bands:            []dxBandCondition{},
	}

	if dxBaseline != nil {
		resp = dxBaseline.Evaluate(target, surroundings, minutes, cwMinDb, historyCopy, time.Now().Unix())
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
