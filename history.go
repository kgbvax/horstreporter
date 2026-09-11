package main

// history.go — GET /api/history: time-travel backend. Serves one gzipped JSON
// bundle of spots for a [t0,t1] window from the Postgres archive
// (dx_raw_spots, token-filtered pushdown via loadSpotsRangeForTargets) or, on
// deployments without Postgres, from the in-memory hub.history (its rolling
// retention window bounds how far back the fallback can reach). The handler
// re-uses matchAndCreateSpot + streamClientFilter so a past moment is filtered
// byte-identically to a live moment; per-spot absolute timestamps (t) let the
// frontend slice trailing 15-min moments from one fetch.

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// historyMaxSpanSeconds is the hard cap on a single /api/history window: the
// 24h time-travel depth decided in the feature spec (scrub range presets 1h /
// 6h / 24h).
const historyMaxSpanSeconds = int64(24 * 60 * 60)

// historyMaxSpotCount bounds a response payload. A QTH-filtered 24h window is
// realistically a few thousand spots; 50k is an abuse-margin ceiling. When
// exceeded the bundle is truncated oldest-first (chronology is preserved, the
// newest spots near the live edge survive) and `truncated: true` tells the
// client its bundle is partial.
const historyMaxSpotCount = 50_000

// historyRatePerHour bounds /api/history requests per client IP. A 24h
// animated session legitimately walks ~24 one-hour chunks (plus prefetch);
// 120/h keeps well ahead of that while still bounding abuse. Bundles are far
// heavier than an SSE handshake, so the limit stays stricter than the
// push-subscribe rate.
const historyRatePerHour = 120

// historyMaxConcurrent caps simultaneous in-flight /api/history requests
// globally. Each holds a Postgres connection (archive path) for up to
// historyQueryTimeout; the ceiling keeps a stampede of parallel fetches from
// starving the ingest/baseline flush paths on the memory-constrained prod box.
// 4 covers a scrubbing session (rapid slider steps overlap ~2 queries) plus a
// prefetch, with headroom for a second operator.
const historyMaxConcurrent = 4

// historyCacheMaxEntries bounds the server-side LRU over immutable-past
// bundles; a handful of hot ranges covers realistic multi-operator sharing.
const historyCacheMaxEntries = 16

// historyCacheTTL bounds a cached bundle's freshness, so a bundle whose window
// includes the live edge re-serves only briefly.
const historyCacheTTL = 10 * time.Minute

// historyLiveEdgeSeconds: windows ending this close to wall-clock now are
// treated as live-edge (still cached, but the short TTL keeps them fresh).
const historyLiveEdgeSeconds = int64(120)

// historySemaphore is the global concurrency ceiling for /api/history.
type historySemaphore chan struct{}

var historyGate = make(historySemaphore, historyMaxConcurrent)

type historySpot struct {
	T               int64   `json:"t"`
	Lat             float64 `json:"lat"`
	Lng             float64 `json:"lng"`
	SNR             int     `json:"snr"`
	Locator         string  `json:"locator"`
	ReporterLocator string  `json:"reporterLocator,omitempty"`
	SourceType      string  `json:"sourceType,omitempty"`
	Band            string  `json:"band"`
	Sender          string  `json:"sender,omitempty"`
	Receiver        string  `json:"receiver,omitempty"`
}

type historyResponse struct {
	QTH       string        `json:"qth"`
	T0        int64         `json:"t0"`
	T1        int64         `json:"t1"`
	Generated int64         `json:"generated_at"`
	Truncated bool          `json:"truncated"`
	Count     int           `json:"count"`
	Spots     []historySpot `json:"spots"`
}

// historyLimiter is the per-IP rate limiter for /api/history. Mirrors
// pushIPRateLimiter's sliding-hour window (same shape, own namespace so the
// limits are independent).
type historyLimiter struct {
	counts map[string]*historyIPWindow
	mu     sync.Mutex
}

type historyIPWindow struct {
	windowStart time.Time
	count       int
}

var historyRateLimiter = &historyLimiter{
	counts: make(map[string]*historyIPWindow),
}

func (rl *historyLimiter) allow(ip string) bool {
	if ip == "" {
		return true
	}
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if len(rl.counts) > 10000 {
		for k, v := range rl.counts {
			if now.Sub(v.windowStart) >= time.Hour {
				delete(rl.counts, k)
			}
		}
	}
	w, ok := rl.counts[ip]
	if !ok || now.Sub(w.windowStart) >= time.Hour {
		rl.counts[ip] = &historyIPWindow{windowStart: now, count: 1}
		return true
	}
	if w.count >= historyRatePerHour {
		return false
	}
	w.count++
	return true
}

// historyCache is the server-side LRU over (canonical-query → gzipped body).
// The handler normalizes every input before the lookup (qth uppercased, span
// clamped, bounds reordered), so two spellings of the same logical request
// share one entry.
type historyCache struct {
	mu       sync.Mutex
	entries  map[string]*historyCacheEntry
	eviction []string
}

type historyCacheEntry struct {
	body     []byte
	cachedAt time.Time
}

var historyBundleCache = &historyCache{
	entries: make(map[string]*historyCacheEntry),
}

func (c *historyCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if time.Since(e.cachedAt) > historyCacheTTL {
		delete(c.entries, key)
		return nil, false
	}
	return e.body, true
}

func (c *historyCache) put(key string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		e.body = body
		e.cachedAt = time.Now()
		return
	}
	c.entries[key] = &historyCacheEntry{body: body, cachedAt: time.Now()}
	c.eviction = append(c.eviction, key)
	for len(c.entries) > historyCacheMaxEntries {
		oldest := c.eviction[0]
		c.eviction = c.eviction[1:]
		delete(c.entries, oldest)
	}
}

// historyClientFilter resolves the query params into the QTH token set +
// streamClientFilter, exactly as /api/stream does (resolveQTHQuery +
// surroundings expansion + rings area-of-interest), so a past moment matches
// the same spot set the live stream would.
func historyClientFilter(r *http.Request) *Client {
	qth, surroundings := resolveQTHQuery(r)
	client := &Client{}
	if qth != "" {
		if surroundings && isLocator(qth) {
			client.qthSet = getSurroundingSquares(qth)
		} else {
			client.qthSet = []string{qth}
		}
	}
	if rings := parseIntDefault(r.URL.Query().Get("rings"), 0); rings > 0 && isLocator(qth) {
		if rings > maxAreaRings {
			rings = maxAreaRings
		}
		if cx, cy, ok := locatorSquareXY(qth); ok {
			client.areaActive = true
			client.areaX, client.areaY, client.areaRings = cx, cy, rings
		}
	}
	return client
}

// historyTargetTokens reduces the client's qthSet to the distinct tokens the
// SQL pushdown can express: locator-prefix arms + exact callsign arms.
func historyTargetTokens(client *Client) []string {
	seen := make(map[string]struct{}, len(client.qthSet))
	tokens := make([]string, 0, len(client.qthSet))
	for _, t := range client.qthSet {
		u := strings.ToUpper(strings.TrimSpace(t))
		if u == "" {
			continue
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		tokens = append(tokens, u)
	}
	return tokens
}

// historySpotsFromMessages converts matched messages into wire spots,
// re-using matchAndCreateSpot for byte-identical live-parity semantics. Ages
// are relative to t1 (the playhead anchor the client requested), matching how
// the live stream stamps ages relative to its own now.
func historySpotsFromMessages(client *Client, filter streamClientFilter, msgs []MQTTMessage) []historySpot {
	now := time.Now().Unix()
	spots := make([]historySpot, 0, len(msgs))
	for i := range msgs {
		m := msgs[i]
		spot, ok := matchAndCreateSpot(client, m, now)
		if !ok || !filter.spotAllowed(spot) {
			continue
		}
		spots = append(spots, historySpot{
			T:               m.T,
			Lat:             spot.Lat,
			Lng:             spot.Lng,
			SNR:             spot.SNR,
			Locator:         spot.Locator,
			ReporterLocator: spot.ReporterLocator,
			SourceType:      spot.SourceType,
			Band:            spot.Band,
			Sender:          spot.Sender,
			Receiver:        spot.Receiver,
		})
	}
	return spots
}

// historyMessagesFromHub copies hub.history entries in [t0,t1], sorted by T.
// Used on deployments without Postgres; the rolling retention window bounds
// reach.
func historyMessagesFromHub(t0, t1 int64) []MQTTMessage {
	hub.RLock()
	// hub.history is appended in ingest order, which for backfilled rows is
	// not necessarily time order — copy the window, then sort by T so the
	// bundle is chronological (the frontend timeline slices it by time).
	idx := sort.Search(len(hub.history), func(i int) bool {
		return hub.history[i].T >= t0
	})
	window := make([]MQTTMessage, len(hub.history)-idx)
	copy(window, hub.history[idx:])
	hub.RUnlock()

	out := make([]MQTTMessage, 0, len(window))
	for _, m := range window {
		if m.T <= t1 {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out
}

// oldestHubHistoryTime returns the oldest timestamp hub.history can serve
// (0 when empty) — the honest reach of the no-Postgres fallback.
func oldestHubHistoryTime() int64 {
	hub.RLock()
	defer hub.RUnlock()
	if len(hub.history) == 0 {
		return 0
	}
	return hub.history[0].T
}

// parseUnixParam reads an integer unix-seconds query param (0 when absent or
// unparseable — the handler then applies its defaults/clamps).
func parseUnixParam(r *http.Request, name string) int64 {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return 0
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// resolveHistoryRange validates t0/t1 and normalizes the window: span clamped
// to 24h, t1 floored at t0 (swapped), t0 defaulted to t1-15min, t1 defaulted
// to now.
func resolveHistoryRange(r *http.Request, now int64) (t0, t1 int64) {
	t0 = parseUnixParam(r, "t0")
	t1 = parseUnixParam(r, "t1")
	if t1 <= 0 || t1 > now {
		t1 = now
	}
	if t0 <= 0 {
		t0 = t1 - 15*60
	}
	if t1 < t0 {
		t0, t1 = t1, t0
	}
	if t1-t0 > historyMaxSpanSeconds {
		t0 = t1 - historyMaxSpanSeconds
	}
	return t0, t1
}

// historyCacheKey canonicalizes the effective request (post-validation) so
// equivalent spellings share cache entries.
func historyCacheKey(qth string, t0, t1 int64, filter streamClientFilter, rings int) string {
	var b strings.Builder
	b.WriteString(qth)
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(t0, 10))
	b.WriteByte('|')
	b.WriteString(strconv.FormatInt(t1, 10))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(rings))
	b.WriteByte('|')
	b.WriteString(filter.minSnrMode)
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(filter.ssbMinDb))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(filter.cwMinDb))
	b.WriteByte('|')
	bands := make([]string, 0, len(filter.enabledBands))
	for band := range filter.enabledBands {
		bands = append(bands, band)
	}
	sort.Strings(bands)
	b.WriteString(strings.Join(bands, ","))
	return b.String()
}

// encodeHistoryBody marshals + gzips the response into one buffer (the cache
// stores and serves it verbatim).
func encodeHistoryBody(resp *historyResponse) ([]byte, error) {
	raw, err := json.Marshal(resp)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// loadHistoryMessages prefers the Postgres archive (token pushdown); a nil
// store returns (nil, nil) and the handler falls back to hub.history.
// includeDXCluster follows the -dx-cluster-enable ingest flag: the cluster
// archive only exists when the ingest runs.
func loadHistoryMessages(t0, t1 int64, tokens []string) ([]MQTTMessage, error) {
	if dxBaseline != nil && dxBaseline.HasSpotStore() {
		return dxBaseline.LoadSpotsRangeForTargets(t0, t1, tokens, dxClusterEnabled)
	}
	return nil, nil
}

// historyHandler serves GET /api/history.
//
// Response: application/json, Content-Encoding: gzip (always — the bundle is
// pre-compressed, not negotiated). Errors: 400 (missing qth), 405 (non-GET),
// 429 (rate limit), 503 (concurrency ceiling). The X-History-Reach header
// (fallback path only) carries the oldest spot time hub.history retains, so
// the client can disable deeper scrubbing honestly.
func historyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	now := time.Now().Unix()
	qth, _ := resolveQTHQuery(r)
	if qth == "" {
		http.Error(w, "qth required", http.StatusBadRequest)
		return
	}
	if !historyRateLimiter.allow(clientIPFromRequest(r)) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	client := historyClientFilter(r)
	if len(client.qthSet) == 0 && !client.areaActive {
		http.Error(w, "qth required", http.StatusBadRequest)
		return
	}
	t0, t1 := resolveHistoryRange(r, now)
	filter := newStreamClientFilter(r)

	rings := 0
	if client.areaActive {
		rings = client.areaRings
	}
	cacheKey := historyCacheKey(qth, t0, t1, filter, rings)
	if body, ok := historyBundleCache.get(cacheKey); ok {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("X-History-Cache", "hit")
		w.Write(body)
		return
	}

	// Concurrency ceiling held only across the archive query + bundle encode;
	// rate limiting and cache lookup stay outside it.
	select {
	case historyGate <- struct{}{}:
		defer func() { <-historyGate }()
	default:
		http.Error(w, "history endpoint busy", http.StatusServiceUnavailable)
		return
	}

	msgs, err := loadHistoryMessages(t0, t1, historyTargetTokens(client))
	if err != nil {
		logInfo("history: archive load failed (%v), falling back to hub.history", err)
		msgs = nil
	}
	if msgs == nil {
		// No Postgres store (nil, nil) or archive error: fall back to the
		// in-memory window. hub.history only retains its rolling window, so
		// windows entirely older than the retained span get an empty bundle
		// plus X-History-Reach (the oldest servable spot time) — the client
		// can disable deeper scrubbing honestly instead of misreading an
		// empty map as "dead bands". Partially overlapping windows serve the
		// overlap (historyMessagesFromHub clamps naturally).
		histStart := oldestHubHistoryTime()
		if histStart == 0 || t1 < histStart {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("X-History-Reach", strconv.FormatInt(histStart, 10))
			body, encErr := encodeHistoryBody(&historyResponse{
				QTH:       qth,
				T0:        t0,
				T1:        t1,
				Generated: now,
				Spots:     []historySpot{},
			})
			if encErr != nil {
				http.Error(w, "encode failed", http.StatusInternalServerError)
				return
			}
			w.Write(body)
			return
		}
		w.Header().Set("X-History-Reach", strconv.FormatInt(histStart, 10))
		msgs = historyMessagesFromHub(t0, t1)
	}

	spots := historySpotsFromMessages(client, filter, msgs)
	truncated := false
	if len(spots) > historyMaxSpotCount {
		// Truncate oldest-first: keep the newest spots (chronology preserved,
		// live edge intact), flag it to the client.
		spots = spots[len(spots)-historyMaxSpotCount:]
		truncated = true
	}
	resp := historyResponse{
		QTH:       qth,
		T0:        t0,
		T1:        t1,
		Generated: now,
		Truncated: truncated,
		Count:     len(spots),
		Spots:     spots,
	}

	body, err := encodeHistoryBody(&resp)
	if err != nil {
		http.Error(w, "encode failed", http.StatusInternalServerError)
		return
	}
	// Cache only windows safely in the immutable past; live-edge windows rely
	// on the short TTL anyway, so excluding them keeps "now-anchored" requests
	// from ever serving stale spots.
	if t1 < now-historyLiveEdgeSeconds {
		historyBundleCache.put(cacheKey, body)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Encoding", "gzip")
	w.Write(body)
}