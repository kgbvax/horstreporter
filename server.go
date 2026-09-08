package main

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// pushSubscribeRequest is the JSON body of POST /api/push/subscribe.
// The browser sends its PushSubscription object (endpoint + keys) and
// the operator's per-(band × region) enable preferences. A re-POST of an
// existing endpoint updates the preferences in place (idempotent).
type pushSubscribeRequest struct {
	Endpoint    string               `json:"endpoint"`
	Keys        pushSubscriptionKeys `json:"keys"`
	QTH         string               `json:"qth"`
	Preferences map[string]bool      `json:"preferences"`
}

// pushUnsubscribeRequest is the JSON body of POST /api/push/unsubscribe.
type pushUnsubscribeRequest struct {
	Endpoint string `json:"endpoint"`
}

// pushVAPIDPublicKeyHandler serves the server's VAPID public key (base64url)
// for the frontend subscription flow. The private key is NEVER exposed
// here. Returns 503 when push is not configured.
func pushVAPIDPublicKeyHandler(w http.ResponseWriter, r *http.Request) {
	if !pushStore.isEnabled() {
		http.Error(w, "push not configured", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		PublicKey string `json:"public_key"`
	}{PublicKey: pushStore.publicKey()})
}

// pushSubscribeHandler stores (or updates) a browser Push API
// subscription. Validates the subscription shape (HTTPS endpoint +
// p256dh + auth keys) before storing. Per-client-IP rate limiting
// (pushSubscribeRatePerHour / hour) mitigates abuse from unauthenticated
// clients. A re-POST of an existing endpoint is a no-op update (the plan's
// idempotent re-subscription requirement).
func pushSubscribeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pushStore.isEnabled() {
		http.Error(w, "push not configured", http.StatusServiceUnavailable)
		return
	}
	if !pushRateLimiter.allow(clientIPFromRequest(r)) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	var req pushSubscribeRequest
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	sub := &pushSubscription{
		Endpoint:    strings.TrimSpace(req.Endpoint),
		Keys:        req.Keys,
		QTH:         strings.ToUpper(strings.TrimSpace(req.QTH)),
		Preferences: req.Preferences,
	}
	if sub.Preferences == nil {
		sub.Preferences = map[string]bool{}
	}
	if err := validatePushSubscription(sub); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pushStore.add(sub)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(struct {
		OK         bool   `json:"ok"`
		Endpoint   string `json:"endpoint"`
		Registered bool   `json:"registered"`
	}{OK: true, Endpoint: sub.Endpoint, Registered: true})
}

// pushUnsubscribeHandler removes a subscription by endpoint. No-op when
// the endpoint was never stored (the browser may unsubscribe after a
// server restart that already lost the record).
func pushUnsubscribeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pushStore.isEnabled() {
		// Still accept unsubscriptions when push is disabled so the
		// browser can clean up its side without a 503.
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			OK bool `json:"ok"`
		}{OK: true})
		return
	}
	var req pushUnsubscribeRequest
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	pushStore.remove(strings.TrimSpace(req.Endpoint))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(struct {
		OK bool `json:"ok"`
	}{OK: true})
}

// pushSubscriptionStatusHandler reports whether a given endpoint is
// currently registered server-side. Used by the frontend's
// re-subscription flow: on panel open the browser queries its existing
// subscription endpoint here; a 404 triggers a re-POST to
// /api/push/subscribe (the plan's restart-recovery requirement).
func pushSubscriptionStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !pushStore.isEnabled() {
		http.Error(w, "push not configured", http.StatusServiceUnavailable)
		return
	}
	endpoint := strings.TrimSpace(r.URL.Query().Get("endpoint"))
	if endpoint == "" {
		http.Error(w, "endpoint required", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		Registered bool `json:"registered"`
	}{Registered: pushStore.has(endpoint)})
}

// pushTrustedProxies is the allowlist of CIDR ranges whose
// X-Forwarded-For header is trusted for push rate-limiting. When empty,
// X-Forwarded-For is never honored — the client IP is taken from RemoteAddr.
// This prevents a client from spoofing X-Forwarded-For to rotate the
// rate-limit key and bypass the per-IP subscribe cap. Configured via
// -push-trusted-proxy-cidr (main.go) and only meaningful when push is enabled.
var pushTrustedProxies []*net.IPNet

// setPushTrustedProxies parses a list of CIDR strings into the trusted-proxy
// allowlist. Invalid CIDRs return an error (fatal at startup). An empty list
// (or all-empty entries) clears the allowlist, disabling XFF trust.
func setPushTrustedProxies(cidrs []string) error {
	var nets []*net.IPNet
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		_, ipnet, err := net.ParseCIDR(c)
		if err != nil {
			return fmt.Errorf("bad CIDR %q: %w", c, err)
		}
		nets = append(nets, ipnet)
	}
	pushTrustedProxies = nets
	return nil
}

// remoteAddrHost strips the port from r.RemoteAddr and returns the host
// (IP literal). Works for both "host:port" and "[ipv6]:port".
func remoteAddrHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// clientIPFromRequest extracts the client IP for rate limiting. It honors
// X-Forwarded-For (leftmost entry) ONLY when the direct TCP peer (RemoteAddr)
// is in the configured trusted-proxy allowlist (pushTrustedProxies); otherwise
// it uses RemoteAddr. This prevents spoofed X-Forwarded-For from rotating the
// rate-limit key when the server is reachable directly. Strips the port.
func clientIPFromRequest(r *http.Request) string {
	peer := remoteAddrHost(r)
	if len(pushTrustedProxies) > 0 {
		if ip := net.ParseIP(peer); ip != nil {
			for _, n := range pushTrustedProxies {
				if n.Contains(ip) {
					if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
						if idx := strings.IndexByte(xff, ','); idx >= 0 {
							return strings.TrimSpace(xff[:idx])
						}
						return strings.TrimSpace(xff)
					}
					break
				}
			}
		}
	}
	return peer
}

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
	Lat             float64 `json:"lat"`
	Lng             float64 `json:"lng"`
	SNR             int     `json:"snr"`
	AgeSeconds      int64   `json:"ageSeconds"`
	Locator         string  `json:"locator"`
	ReporterLocator string  `json:"reporterLocator,omitempty"`
	SourceType      string  `json:"sourceType,omitempty"`
	Band            string  `json:"band"`
	Sender          string  `json:"sender,omitempty"`
	Receiver        string  `json:"receiver,omitempty"`
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

// maxAreaRings caps the configurable area-of-interest radius (in grid-square
// rings) for region feeds. ~30 rings ≈ ±30° latitude — continental scale —
// while keeping the match O(1) per spot.
const maxAreaRings = 30

func streamHandler(w http.ResponseWriter, r *http.Request) {
	qth, surroundings := resolveQTHQuery(r)
	minutesStr := r.URL.Query().Get("minutes")

	if qth == "" {
		http.Error(w, "qth required", http.StatusBadRequest)
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

	var qthSet []string
	if surroundings && isLocator(qth) {
		qthSet = getSurroundingSquares(qth)
	} else {
		qthSet = []string{qth}
	}

	client := &Client{
		qthSet: qthSet,
		send:   make(chan Spot, 10000), // Buffer to handle initial history dump
	}

	// Optional configurable "area of interest": rings>0 with a locator qth
	// matches any sender/receiver within `rings` grid-squares of the qth,
	// for region feeds (e.g. horstprop). Read-only; default behaviour unchanged.
	if rings := parseIntDefault(r.URL.Query().Get("rings"), 0); rings > 0 && isLocator(qth) {
		if rings > maxAreaRings {
			rings = maxAreaRings
		}
		if cx, cy, ok := locatorSquareXY(qth); ok {
			client.areaActive = true
			client.areaX, client.areaY, client.areaRings = cx, cy, rings
		}
	}

	filter := newStreamClientFilter(r)

	now := time.Now().Unix()
	cutoff := now - historySeconds

	// Add client and copy the relevant history window under the write lock.
	// matchAndCreateSpot processing happens outside the lock so broadcastMsg
	// is not stalled for the duration of the history scan.
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
	logInfo("New client stream started for qth: %v (History: %d mins)", qthSet, minutes)

	idx := sort.Search(len(hub.history), func(i int) bool {
		return hub.history[i].T >= cutoff
	})
	historyWindow := make([]MQTTMessage, len(hub.history)-idx)
	copy(historyWindow, hub.history[idx:])
	hub.Unlock()

	var historySpots []Spot
	for _, msg := range historyWindow {
		if spot, ok := matchAndCreateSpot(client, msg, now); ok && filter.spotAllowed(spot) {
			historySpots = append(historySpots, spot)
		}
	}

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
		logInfo("Client stream closed for qth: %v", qthSet)
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
			if !filter.spotAllowed(spot) {
				continue
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
	qth, surroundings := resolveQTHQuery(r)
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

	now := time.Now().Unix()
	cutoff := now - int64(minutes*60)
	hub.RLock()
	idx := sort.Search(len(hub.history), func(i int) bool {
		return hub.history[i].T >= cutoff
	})
	historyCopy := make([]MQTTMessage, len(hub.history)-idx)
	copy(historyCopy, hub.history[idx:])
	hub.RUnlock()

	resp := buildSquareDetailsResponse(qth, surroundings, locator, minutes, minSnrMode, ssbMinDb, cwMinDb, selectedBand, enabledBands, historyCopy, now)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func resolveQTHQuery(r *http.Request) (string, bool) {
	qth := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("qth")))
	surroundings := r.URL.Query().Get("surroundings") == "true"
	if qth == "" {
		if call := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("callsign"))); call != "" {
			qth = call
		} else if loc := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("locator"))); loc != "" {
			qth = loc
		}
	}
	return qth, surroundings
}

func toStreamSpot(spot Spot) streamSpot {
	s := streamSpot{
		Lat:             spot.Lat,
		Lng:             spot.Lng,
		SNR:             spot.SNR,
		AgeSeconds:      spot.AgeSeconds,
		Locator:         spot.Locator,
		ReporterLocator: spot.ReporterLocator,
		SourceType:      spot.SourceType,
		Band:            spot.Band,
	}
	// Only DX cluster spots need callsigns on the wire (hover tooltip on the
	// cluster markers); regular spots stay trimmed.
	if spot.SourceType == "dxcluster" {
		s.Sender = spot.Sender
		s.Receiver = spot.Receiver
	}
	return s
}

// streamClientFilter carries the band/SNR filters the browser requests so the
// server can avoid sending spots the client will immediately discard. This cuts
// both server-side serialization cost and the on-the-wire byte count.
type streamClientFilter struct {
	enabledBands map[string]struct{}
	minSnrMode   string
	ssbMinDb     int
	cwMinDb      int
}

func newStreamClientFilter(r *http.Request) streamClientFilter {
	return streamClientFilter{
		enabledBands: parseEnabledBands(r.URL.Query().Get("enabled_bands")),
		minSnrMode:   strings.ToLower(strings.TrimSpace(r.URL.Query().Get("min_snr_mode"))),
		ssbMinDb:     parseIntDefault(r.URL.Query().Get("ssb_min_db"), 0),
		cwMinDb:      parseIntDefault(r.URL.Query().Get("cw_min_db"), -15),
	}
}

func (f streamClientFilter) spotAllowed(s Spot) bool {
	if len(f.enabledBands) > 0 {
		if _, ok := f.enabledBands[strings.ToLower(strings.TrimSpace(s.Band))]; !ok {
			return false
		}
	}
	// SNR thresholds are calibrated for FT8/MQTT spots; leave DX cluster,
	// WSPR and RBN unfiltered by SNR because they use different scales.
	if s.SourceType != "" && s.SourceType != "mqtt" && s.SourceType != "rbn" {
		return true
	}
	switch f.minSnrMode {
	case "ssb":
		if s.SNR < f.ssbMinDb {
			return false
		}
	case "cw":
		if s.SNR < f.cwMinDb {
			return false
		}
	}
	return true
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

func buildSquareDetailsResponse(qth string, surroundings bool, locator string, minutes int, minSnrMode string, ssbMinDb, cwMinDb int, selectedBand string, enabledBands map[string]struct{}, history []MQTTMessage, now int64) squareDetailsResponse {
	resp := squareDetailsResponse{
		Locator:    locator,
		BandCounts: make(map[string]int),
		TopReports: []squareDetailReport{},
	}

	if qth == "" || locator == "" {
		return resp
	}

	qthSet := []string{qth}
	if surroundings && isLocator(qth) {
		qthSet = getSurroundingSquares(qth)
	}

	client := &Client{qthSet: qthSet}
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
	dxConnAttempts, dxConnected, dxLinesSeen, dxParsed, dxPersisted, dxForwarded, dxDroppedNoLoc := dxClusterAccounting.snapshot()
	rbnAttempts, rbnConnected, rbnLinesSeen, rbnParsed, rbnPersisted, rbnForwarded, rbnDroppedNoLoc := rbnAccounting.snapshot()
	wsprAttempts, wsprFailures, wsprRowsSeen, wsprParsed, wsprPersisted, wsprForwarded, wsprDroppedNoLoc := wsprAccounting.snapshot()
	dxBaselineEventCount := 0
	dxBaselineHistoryMinutes := 0
	if dxBaseline != nil {
		_, dxBaselineEventCount, dxBaselineHistoryMinutes = dxBaseline.Stats(time.Now().Unix())
	}
	propIntelReqs, propIntelErrs, propIntelSurges := propIntelAccounting.snapshot()
	pushSurges, pushSent, pushErrs := pushAccounting.snapshot()
	// Persistence health: dx_raw_spots history depends on the raw flush
	// succeeding; surface last success + failure streak so an outage is
	// visible from the API instead of only in the log.
	var postgres *postgresStatsBlock
	if dxBaseline != nil && dxBaseline.Store() != nil {
		rawOK, rawStreak, baseOK, baseStreak := dxBaseline.Store().FlushHealth()
		postgres = &postgresStatsBlock{
			RawFlushLastOKUnix:      rawOK,
			RawFlushFailStreak:      rawStreak,
			BaselineFlushLastOKUnix: baseOK,
			BaselineFlushFailStreak: baseStreak,
		}
	}

	stats := struct {
		ActiveConnections    int                  `json:"active_connections"`
		HistorySize          int                  `json:"history_size"`
		HistoryMinutes       int                  `json:"history_minutes"`
		HistoryRetentionMins int                  `json:"history_retention_minutes"`
		HistorySizeKB        int64                `json:"history_size_kb"`
		SessionsTotal        int64                `json:"sessions_total"`
		SessionsCompleted    int64                `json:"sessions_completed"`
		SessionsActive       int64                `json:"sessions_active"`
		SessionBytesTotal    int64                `json:"session_bytes_total"`
		SessionBytesAvg      float64              `json:"session_bytes_avg"`
		DxBaselineEventCount int                  `json:"dx_baseline_event_count"`
		DxBaselineHistoryM   int                  `json:"dx_baseline_history_minutes"`
		DxBaselineMaxEvents  int                  `json:"dx_baseline_max_events"`
		DxClusterConnAttempt int64                `json:"dxcluster_connect_attempts"`
		DxClusterConnected   int64                `json:"dxcluster_connected_sessions"`
		DxClusterLinesSeen   int64                `json:"dxcluster_lines_seen"`
		DxClusterParsed      int64                `json:"dxcluster_parsed_spots"`
		DxClusterPersisted   int64                `json:"dxcluster_persisted_spots"`
		DxClusterForwarded   int64                `json:"dxcluster_live_forwarded"`
		DxClusterDroppedLoc  int64                `json:"dxcluster_dropped_no_locator"`
		RbnConnAttempt       int64                `json:"rbn_connect_attempts"`
		RbnConnected         int64                `json:"rbn_connected_sessions"`
		RbnLinesSeen         int64                `json:"rbn_lines_seen"`
		RbnParsed            int64                `json:"rbn_parsed_spots"`
		RbnPersisted         int64                `json:"rbn_persisted_spots"`
		RbnForwarded         int64                `json:"rbn_live_forwarded"`
		RbnDroppedLoc        int64                `json:"rbn_dropped_no_locator"`
		WsprPollAttempts     int64                `json:"wspr_poll_attempts"`
		WsprPollFailures     int64                `json:"wspr_poll_failures"`
		WsprRowsSeen         int64                `json:"wspr_rows_seen"`
		WsprParsed           int64                `json:"wspr_parsed_spots"`
		WsprPersisted        int64                `json:"wspr_persisted_spots"`
		WsprForwarded        int64                `json:"wspr_live_forwarded"`
		WsprDroppedLoc       int64                `json:"wspr_dropped_no_locator"`
		Postgres             *postgresStatsBlock  `json:"postgres,omitempty"`
		PropIntel            *propIntelStatsBlock `json:"prop_intel"`
		Push                 *pushStatsBlock      `json:"push"`
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
		DxClusterConnAttempt: dxConnAttempts,
		DxClusterConnected:   dxConnected,
		DxClusterLinesSeen:   dxLinesSeen,
		DxClusterParsed:      dxParsed,
		DxClusterPersisted:   dxPersisted,
		DxClusterForwarded:   dxForwarded,
		DxClusterDroppedLoc:  dxDroppedNoLoc,
		RbnConnAttempt:       rbnAttempts,
		RbnConnected:         rbnConnected,
		RbnLinesSeen:         rbnLinesSeen,
		RbnParsed:            rbnParsed,
		RbnPersisted:         rbnPersisted,
		RbnForwarded:         rbnForwarded,
		RbnDroppedLoc:        rbnDroppedNoLoc,
		WsprPollAttempts:     wsprAttempts,
		WsprPollFailures:     wsprFailures,
		WsprRowsSeen:         wsprRowsSeen,
		WsprParsed:           wsprParsed,
		WsprPersisted:        wsprPersisted,
		WsprForwarded:        wsprForwarded,
		WsprDroppedLoc:       wsprDroppedNoLoc,
		Postgres:             postgres,
		PropIntel: &propIntelStatsBlock{
			Requests:       propIntelReqs,
			Errors:         propIntelErrs,
			SurgesDetected: propIntelSurges,
		},
		Push: &pushStatsBlock{
			SurgesDetected: pushSurges,
			PushSent:       pushSent,
			PushErrors:     pushErrs,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// propIntelStatsBlock is the prop_intel.* sub-object in /api/stats.
// Mirrors the flat per-ingest counter layout but nested under prop_intel
// to keep the namespace clean (the plan's accounting requirement, U6).
// postgresStatsBlock is the postgres.* sub-object in /api/stats: flush
// health for the two persistence pipelines (raw spots = history archive,
// baseline = band conditions). A nonzero streak or a stale last-ok timestamp
// means persistence is down.
type postgresStatsBlock struct {
	RawFlushLastOKUnix      int64 `json:"raw_flush_last_ok_unix"`
	RawFlushFailStreak      int64 `json:"raw_flush_fail_streak"`
	BaselineFlushLastOKUnix int64 `json:"baseline_flush_last_ok_unix"`
	BaselineFlushFailStreak int64 `json:"baseline_flush_fail_streak"`
}

type propIntelStatsBlock struct {
	Requests       int64 `json:"requests"`
	Errors         int64 `json:"errors"`
	SurgesDetected int64 `json:"surges_detected"`
}

// pushStatsBlock is the push.* sub-object in /api/stats. U6.
type pushStatsBlock struct {
	SurgesDetected int64 `json:"surges_detected"`
	PushSent       int64 `json:"push_sent"`
	PushErrors     int64 `json:"push_errors"`
}

func dxConditionsHandler(w http.ResponseWriter, r *http.Request) {
	qth := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("qth")))
	if qth == "" {
		if call := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("callsign"))); call != "" {
			qth = call
		} else if loc := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("locator"))); loc != "" {
			qth = loc
		}
	}

	if qth == "" {
		http.Error(w, "qth required", http.StatusBadRequest)
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

	now := time.Now().Unix()

	historyCopy, releaseHistory := snapshotHubHistoryWindow(now, minutes)
	defer releaseHistory()

	resp := dxConditionsResponse{
		QTH:              qth,
		Surroundings:     surroundings,
		WindowMinutes:    minutes,
		CwMinDb:          cwMinDb,
		CurrentSlotOfDay: utcSlotOfDay(now),
		GeneratedAt:      now,
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
		resp = dxBaseline.Evaluate(qth, surroundings, minutes, cwMinDb, historyCopy, now)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func hotBandsHandler(w http.ResponseWriter, r *http.Request) {
	qth, surroundings := resolveQTHQuery(r)
	if qth == "" {
		http.Error(w, "qth required", http.StatusBadRequest)
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

	currentBand := normalizeBand(strings.TrimSpace(r.URL.Query().Get("current_band")))

	now := time.Now().Unix()

	historyCopy, releaseHistory := snapshotHubHistoryWindow(now, minutes)
	defer releaseHistory()

	resp := hotBandsResponse{
		QTH:              qth,
		Surroundings:     surroundings,
		CurrentBand:      currentBand,
		CurrentSlotOfDay: utcSlotOfDay(now),
		GeneratedAt:      now,
		Recommendations:  []hotBandRecommendation{},
	}

	if dxBaseline != nil {
		resp = dxBaseline.HotBands(qth, surroundings, minutes, cwMinDb, currentBand, historyCopy, now)
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

// cachedStaticHandler serves the embedded static assets with content-based ETags
// and Cache-Control: no-cache. Embedded files have a zero modtime, so the stock
// http.FileServer emits no validators and browsers fall back to heuristic
// caching — which serves stale JS/HTML after a redeploy. The plain ES-module
// import paths can't be content-hashed into filenames without a build step, so
// instead we attach a per-file content ETag and require revalidation: unchanged
// assets return a cheap 304, and a redeploy (new content -> new ETag) is picked
// up immediately. ETags are precomputed once at startup.
func cachedStaticHandler(staticFS fs.FS) http.Handler {
	etags := make(map[string]string)
	_ = fs.WalkDir(staticFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, readErr := fs.ReadFile(staticFS, p)
		if readErr != nil {
			return nil
		}
		sum := sha256.Sum256(b)
		etags["/"+p] = `"` + hex.EncodeToString(sum[:16]) + `"`
		return nil
	})

	fileServer := http.FileServer(http.FS(staticFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lookup := r.URL.Path
		if lookup == "" || strings.HasSuffix(lookup, "/") {
			lookup += "index.html"
		}
		if etag, ok := etags[lookup]; ok {
			w.Header().Set("ETag", etag)
			w.Header().Set("Cache-Control", "no-cache")
		}
		// http.ServeContent (used by FileServer) honors the ETag we set above for
		// If-None-Match, returning 304 when the client's copy is current.
		fileServer.ServeHTTP(w, r)
	})
}
