package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Time-travel replay: read-only spot windows served straight from the
// dx_raw_spots Postgres table (60-day retention), one bounded bucket at a
// time. hub.history only holds ~60 minutes, so anything older must come
// from the store. Ingest, SSE fan-out and the baseline engine are untouched.

const (
	// maxReplaySpots caps one bucket response (mirrors the client's
	// MAX_LIVE_SPOTS); truncated=true tells the client it saw a subset.
	maxReplaySpots = 20000
	// maxReplaySpanSeconds bounds how far back the timeline can reach: 48h.
	maxReplaySpanSeconds = int64(48 * 3600)
)

// replayLoader is a package-level seam so tests can swap the Postgres read
// (the handlers never touch hub.history).
var replayLoader = func(start, end int64, sources []string, callsignFilter string, locatorPrefixes []string, limit int) ([]MQTTMessage, bool, error) {
	return dxBaseline.LoadSpotsBetweenSources(start, end, sources, callsignFilter, locatorPrefixes, limit)
}

// replaySourcesFromQuery maps the include_* toggles to source_type values for
// the SQL ANY(...) filter. All default to true, matching capture_snapshot.
func replaySourcesFromQuery(q map[string][]string) []string {
	get := func(name string) bool {
		vals, ok := q[name]
		if !ok || len(vals) == 0 || strings.TrimSpace(vals[0]) == "" {
			return true
		}
		return strings.EqualFold(strings.TrimSpace(vals[0]), "true") || vals[0] == "1"
	}
	var sources []string
	if get("include_dxcluster") {
		sources = append(sources, "dxcluster")
	}
	if get("include_rbn") {
		sources = append(sources, "rbn")
	}
	if get("include_wspr") {
		sources = append(sources, "wspr")
	}
	// mqtt is the default source_type for FT8 spots; it is always included —
	// the frontend has no toggle for it (it is the main feed).
	sources = append(sources, "mqtt")
	return sources
}

func replaySourcesFromRequest(r *http.Request) []string {
	return replaySourcesFromQuery(r.URL.Query())
}

// replayBucketSeconds validates the bucket size: default 30min, clamped to
// [60s, 1h] so a bucket query stays index-friendly.
func replayBucketSeconds(raw string) int64 {
	secs := int64(parseIntDefault(raw, 1800))
	if secs <= 0 {
		secs = 1800
	}
	if secs < 60 {
		secs = 60
	}
	if secs > 3600 {
		secs = 3600
	}
	return secs
}

type replayHistogramBucket struct {
	T     int64 `json:"t"`
	Count int64 `json:"count"`
}

type replayHistogramResponse struct {
	Start         int64                   `json:"start"`
	End           int64                   `json:"end"`
	BucketSeconds int64                   `json:"bucket_seconds"`
	Buckets       []replayHistogramBucket `json:"buckets"`
	Total         int64                   `json:"total"`
	GeneratedAt   int64                   `json:"generated_at"`
}

func replayStoreAvailable() bool {
	return dxBaseline != nil && dxBaseline.Store() != nil
}

func replayUnavailable(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "postgres not enabled"})
}

// replayHistogramHandler serves GET /api/replay/histogram: per-bucket GLOBAL
// spot counts over the requested range (density guide for the timeline
// scrubber; QTH matching happens per bucket in /api/replay/spots).
func replayHistogramHandler(w http.ResponseWriter, r *http.Request) {
	if !replayStoreAvailable() {
		replayUnavailable(w)
		return
	}
	if histQth, _ := resolveQTHQuery(r); histQth == "" {
		// parity with the other endpoints: qth must be present (even though
		// the histogram itself is global)
		http.Error(w, "qth required", http.StatusBadRequest)
		return
	}

	now := time.Now().Unix()
	end := now
	if raw := strings.TrimSpace(r.URL.Query().Get("end")); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			end = parsed
		}
	}
	if end > now {
		end = now
	}
	bucketSeconds := replayBucketSeconds(r.URL.Query().Get("bucket_seconds"))
	start := end - maxReplaySpanSeconds
	if raw := strings.TrimSpace(r.URL.Query().Get("start")); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 && parsed < end {
			start = parsed
		}
	}
	if end-start > maxReplaySpanSeconds {
		start = end - maxReplaySpanSeconds
	}

	sources := replaySourcesFromRequest(r)
	selectedBand := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("selected_band")))
	enabledBands := parseEnabledBands(r.URL.Query().Get("enabled_bands"))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := dxBaseline.Store().pool.Query(ctx, `
		SELECT (spot_time / $3) * $3 AS b, count(*)
		FROM dx_raw_spots
		WHERE spot_time >= $1 AND spot_time < $2
		  AND LOWER(COALESCE(source_type, 'mqtt')) = ANY($4)
		  AND ($5::text = '' OR LOWER(band) = $5)
		GROUP BY b ORDER BY b ASC
	`, start, end, bucketSeconds, sources, bandSQLFilter(selectedBand, enabledBands))
	if err != nil {
		http.Error(w, "histogram query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	buckets := make([]replayHistogramBucket, 0, 64)
	var total int64
	for rows.Next() {
		var b replayHistogramBucket
		if err := rows.Scan(&b.T, &b.Count); err != nil {
			http.Error(w, "histogram scan failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		buckets = append(buckets, b)
		total += b.Count
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "histogram query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	resp := replayHistogramResponse{
		Start:         start,
		End:           end,
		BucketSeconds: bucketSeconds,
		Buckets:       buckets,
		Total:         total,
		GeneratedAt:   time.Now().Unix(),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// bandSQLFilter returns the band predicate value for the histogram query:
// a selected_band wins; otherwise the enabled_bands set applies only when it
// names exactly one band (the SQL shape is single-band; the multi-band case
// falls back to all bands, and the per-bucket spot fetch re-applies the full
// filter set in Go).
func bandSQLFilter(selectedBand string, enabledBands map[string]struct{}) string {
	if selectedBand != "" {
		return selectedBand
	}
	if len(enabledBands) == 1 {
		for band := range enabledBands {
			return band
		}
	}
	return ""
}

type replaySpotsResponse struct {
	QTH           string       `json:"qth"`
	Surroundings  bool         `json:"surroundings"`
	BucketEnd     int64        `json:"bucket_end"`
	BucketSeconds int64        `json:"bucket_seconds"`
	Count         int          `json:"count"`
	Truncated     bool         `json:"truncated"`
	Spots         []streamSpot `json:"spots"`
}

func toReplaySpot(spot Spot, t int64) streamSpot {
	s := toStreamSpot(spot)
	s.SpotTime = t
	return s
}

// Single-entry response cache + in-flight dedupe for /api/replay/spots.
// Playback, one-step-ahead prefetch and a second tab all request the same
// bucket; they share one goroutine and one JSON blob (worst case ~2-4 MB),
// keeping prod RAM flat.
var (
	replayCacheMu       sync.Mutex
	replayCacheKey      string
	replayCacheData     []byte
	replayInflightKey   string
	replayInflightGroup *replayFetchGroup
)

type replayFetchGroup struct {
	done chan struct{}
	data []byte
	err  error
}

// replayCacheKeyFor canonicalizes the query so differently-ordered params hit
// the same cache entry.
func replayCacheKeyFor(r *http.Request) string {
	vals := r.URL.Query()
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(r.URL.Path)
	for _, k := range keys {
		vs := vals[k]
		sort.Strings(vs)
		for _, v := range vs {
			b.WriteByte('&')
			b.WriteString(k)
			b.WriteByte('=')
			b.WriteString(v)
		}
	}
	return b.String()
}

func replayCacheGet(key string) ([]byte, bool) {
	replayCacheMu.Lock()
	defer replayCacheMu.Unlock()
	if key == replayCacheKey && replayCacheData != nil {
		return replayCacheData, true
	}
	return nil, false
}

// replayFetchWithCache returns the cached JSON for key, joins an in-flight
// identical fetch, or runs fn() as the single leader and caches the result.
func replayFetchWithCache(key string, fn func() ([]byte, error)) ([]byte, error) {
	if data, ok := replayCacheGet(key); ok {
		return data, nil
	}
	replayCacheMu.Lock()
	if replayInflightGroup != nil && replayInflightKey == key {
		g := replayInflightGroup
		replayCacheMu.Unlock()
		<-g.done
		return g.data, g.err
	}
	g := &replayFetchGroup{done: make(chan struct{})}
	replayInflightKey = key
	replayInflightGroup = g
	replayCacheMu.Unlock()

	g.data, g.err = fn()
	close(g.done)

	replayCacheMu.Lock()
	replayInflightKey = ""
	replayInflightGroup = nil
	if g.err == nil {
		replayCacheKey = key
		replayCacheData = g.data
	}
	replayCacheMu.Unlock()
	return g.data, g.err
}

// replaySpotsHandler serves GET /api/replay/spots: one half-open bucket
// [bucket_end - bucket_seconds, bucket_end) replayed through the same
// QTH/filter chain as /api/capture_snapshot, with ages relative to
// bucket_end so the client's age-based rendering stays coherent.
func replaySpotsHandler(w http.ResponseWriter, r *http.Request) {
	if !replayStoreAvailable() {
		replayUnavailable(w)
		return
	}
	qth, surroundings := resolveQTHQuery(r)
	if qth == "" {
		http.Error(w, "qth required", http.StatusBadRequest)
		return
	}

	now := time.Now().Unix()
	bucketEnd := now
	if raw := strings.TrimSpace(r.URL.Query().Get("bucket_end")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			http.Error(w, "bucket_end must be a unix timestamp", http.StatusBadRequest)
			return
		}
		bucketEnd = parsed
	}
	if bucketEnd > now {
		bucketEnd = now
	}
	bucketSeconds := replayBucketSeconds(r.URL.Query().Get("bucket_seconds"))
	limit := parseIntDefault(r.URL.Query().Get("limit"), maxReplaySpots)
	if limit <= 0 || limit > maxReplaySpots {
		limit = maxReplaySpots
	}
	sources := replaySourcesFromRequest(r)

	key := replayCacheKeyFor(r)
	data, err := replayFetchWithCache(key, func() ([]byte, error) {
		return buildReplayBucket(qth, surroundings, bucketEnd, bucketSeconds, sources, limit, r)
	})
	if err != nil {
		http.Error(w, "replay query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

// buildReplayBucket loads one bucket from Postgres and shapes the response
// (separate function so the cache wraps only the expensive part).
func buildReplayBucket(qth string, surroundings bool, bucketEnd, bucketSeconds int64, sources []string, limit int, r *http.Request) ([]byte, error) {
	var qthSet []string
	if surroundings && isLocator(qth) {
		qthSet = getSurroundingSquares(qth)
	} else {
		qthSet = []string{qth}
	}
	client := &Client{qthSet: qthSet}
	rings := parseIntDefault(r.URL.Query().Get("rings"), 0)
	// Optional configurable "area of interest", same as streamHandler.
	if rings > 0 && isLocator(qth) {
		if rings > maxAreaRings {
			rings = maxAreaRings
		}
		if cx, cy, ok := locatorSquareXY(qth); ok {
			client.areaActive = true
			client.areaX, client.areaY, client.areaRings = cx, cy, rings
		}
	}

	// Push the QTH prefilter into SQL so the LIMIT cap spans the whole bucket
	// instead of the first ~minute of a hot global window (~600k rows/30min on
	// prod). A rings-based area of interest can't be expressed as LIKE
	// prefixes, so it falls back to the unfiltered (time-truncated) load.
	var callsignFilter string
	var locatorPrefixes []string
	if rings == 0 {
		if isLocator(qth) {
			if surroundings {
				for _, sq := range getSurroundingSquares(qth) {
					locatorPrefixes = append(locatorPrefixes, sq+"%")
				}
			} else {
				locatorPrefixes = []string{qth + "%"}
			}
		} else {
			callsignFilter = qth
		}
	}

	minSnrMode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("min_snr_mode")))
	ssbMinDb := parseIntDefault(r.URL.Query().Get("ssb_min_db"), 0)
	cwMinDb := parseIntDefault(r.URL.Query().Get("cw_min_db"), -15)
	selectedBand := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("selected_band")))
	enabledBands := parseEnabledBands(r.URL.Query().Get("enabled_bands"))

	bucketStart := bucketEnd - bucketSeconds
	msgs, truncated, err := replayLoader(bucketStart, bucketEnd, sources, callsignFilter, locatorPrefixes, limit)
	if err != nil {
		return nil, err
	}

	spots := make([]streamSpot, 0, len(msgs))
	for _, msg := range msgs {
		spot, ok := matchAndCreateSpot(client, msg, bucketEnd)
		if !ok {
			continue
		}
		if minSnrMode == "ssb" && spot.SNR < ssbMinDb {
			continue
		}
		if minSnrMode == "cw" && spot.SNR < cwMinDb {
			continue
		}
		if !bandAllowed(strings.ToLower(strings.TrimSpace(spot.Band)), selectedBand, enabledBands) {
			continue
		}
		spots = append(spots, toReplaySpot(spot, msg.T))
	}

	sort.Slice(spots, func(i, j int) bool {
		if spots[i].AgeSeconds != spots[j].AgeSeconds {
			return spots[i].AgeSeconds < spots[j].AgeSeconds
		}
		if spots[i].Locator != spots[j].Locator {
			return spots[i].Locator < spots[j].Locator
		}
		if spots[i].Band != spots[j].Band {
			return spots[i].Band < spots[j].Band
		}
		if spots[i].SNR != spots[j].SNR {
			return spots[i].SNR > spots[j].SNR
		}
		return spots[i].ReporterLocator < spots[j].ReporterLocator
	})

	resp := replaySpotsResponse{
		QTH:           qth,
		Surroundings:  surroundings,
		BucketEnd:     bucketEnd,
		BucketSeconds: bucketSeconds,
		Count:         len(spots),
		Truncated:     truncated,
		Spots:         spots,
	}
	return json.Marshal(resp)
}
