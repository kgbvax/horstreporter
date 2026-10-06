package main

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// withHistoryState snapshots the package-level state /api/history touches
// (hub, rate limiter, bundle cache) and restores it after the test.
func withHistoryState(t *testing.T, fn func()) {
	t.Helper()
	withHubSnapshot(t, func() {
		historyRateLimiter.mu.Lock()
		origCounts := historyRateLimiter.counts
		historyRateLimiter.counts = make(map[string]*historyIPWindow)
		historyRateLimiter.mu.Unlock()

		historyBundleCache.mu.Lock()
		origEntries := historyBundleCache.entries
		origEviction := historyBundleCache.eviction
		historyBundleCache.entries = make(map[string]*historyCacheEntry)
		historyBundleCache.eviction = nil
		historyBundleCache.mu.Unlock()

		origDxClusterEnabled := dxClusterEnabled
		defer func() {
			historyRateLimiter.mu.Lock()
			historyRateLimiter.counts = origCounts
			historyRateLimiter.mu.Unlock()
			historyBundleCache.mu.Lock()
			historyBundleCache.entries = origEntries
			historyBundleCache.eviction = origEviction
			historyBundleCache.mu.Unlock()
			dxClusterEnabled = origDxClusterEnabled
		}()

		fn()
	})
}

func decodeHistoryResponse(t *testing.T, body []byte) historyResponse {
	t.Helper()
	zr, err := gzip.NewReader(strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer zr.Close()
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	var resp historyResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	return resp
}

func getHistory(t *testing.T, url string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", url, nil)
	rec := httptest.NewRecorder()
	historyHandler(rec, req)
	return rec
}

// TestHistoryHandlerRequiresQTH mirrors the stream handler's 400 contract.
func TestHistoryHandlerRequiresQTH(t *testing.T) {
	withHistoryState(t, func() {
		rec := getHistory(t, "/api/history?t0=1&t1=2")
		if rec.Code != 400 {
			t.Fatalf("expected 400, got %d", rec.Code)
		}
	})
}

// TestHistoryHandlerRejectsNonGET: only GET is served (405 otherwise).
func TestHistoryHandlerRejectsNonGET(t *testing.T) {
	withHistoryState(t, func() {
		req := httptest.NewRequest("POST", "/api/history?qth=JO62", nil)
		rec := httptest.NewRecorder()
		historyHandler(rec, req)
		if rec.Code != 405 {
			t.Fatalf("expected 405, got %d", rec.Code)
		}
	})
}

// TestHistoryHandlerFallbackFiltersLikeStream: the no-Postgres path must
// apply matchAndCreateSpot + streamClientFilter exactly as /api/stream does —
// only spots matching the QTH (or its surroundings) and the SNR/band filters
// land in the bundle.
func TestHistoryHandlerFallbackFiltersLikeStream(t *testing.T) {
	withHistoryState(t, func() {
		now := time.Now().Unix()
		hub.Lock()
		hub.history = []MQTTMessage{
			{T: now - 100, SC: "W1AW", RC: "DL1ABC", SL: "FN31", RL: "JO32", RP: -5, B: "20m", MD: "FT8", Source: "mqtt"},
			{T: now - 90, SC: "W1AW", RC: "DL1XYZ", SL: "FN31", RL: "JO43", RP: -5, B: "20m", MD: "FT8", Source: "mqtt"}, // no match
			{T: now - 80, SC: "W1AW", RC: "DL1WEAK", SL: "FN31", RL: "JO32", RP: -30, B: "20m", MD: "FT8", Source: "mqtt"}, // below cw floor
			{T: now - 70, SC: "W1AW", RC: "DL1ABC", SL: "FN31", RL: "JO32", RP: -5, B: "2m", MD: "FT8", Source: "mqtt"}, // filtered band
		}
		hub.Unlock()

		url := "/api/history?qth=JO32&t0=" + itoa64(now-300) + "&t1=" + itoa64(now-10) +
			"&min_snr_mode=cw&cw_min_db=-15&enabled_bands=20m"
		rec := getHistory(t, url)
		if rec.Code != 200 {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		resp := decodeHistoryResponse(t, rec.Body.Bytes())
		if resp.Count != 1 || len(resp.Spots) != 1 {
			t.Fatalf("expected exactly 1 spot, got count=%d", resp.Count)
		}
		sp := resp.Spots[0]
		// The remote end of a matched path is the sender's locator (qth=JO32
		// heard W1AW at FN31), mirroring the live stream's matchAndCreateSpot.
		if sp.Locator != "FN31" || sp.Band != "20m" || sp.SNR != -5 || sp.ReporterLocator != "JO32" {
			t.Fatalf("unexpected spot: %+v", sp)
		}
		if sp.T != now-100 {
			t.Fatalf("expected absolute t=%d, got %d", now-100, sp.T)
		}
		if resp.T0 != now-300 || resp.T1 != now-10 {
			t.Fatalf("window not echoed: t0=%d t1=%d", resp.T0, resp.T1)
		}
	})
}

// TestHistoryHandlerSurroundingsExpansion: surroundings=true must expand a
// locator qth to the 9-square set, exactly like the stream handler.
func TestHistoryHandlerSurroundingsExpansion(t *testing.T) {
	withHistoryState(t, func() {
		now := time.Now().Unix()
		hub.Lock()
		hub.history = []MQTTMessage{
			// JO33 neighbour square of JO32 — matches only with surroundings.
			{T: now - 100, SC: "W1AW", RC: "DL1ABC", SL: "FN31", RL: "JO33", RP: -5, B: "20m", MD: "FT8", Source: "mqtt"},
		}
		hub.Unlock()

		base := "/api/history?qth=JO32&t0=" + itoa64(now-300) + "&t1=" + itoa64(now-10)
		rec := getHistory(t, base)
		resp := decodeHistoryResponse(t, rec.Body.Bytes())
		if resp.Count != 0 {
			t.Fatalf("expected 0 spots without surroundings, got %d", resp.Count)
		}

		rec = getHistory(t, base+"&surroundings=true")
		resp = decodeHistoryResponse(t, rec.Body.Bytes())
		if resp.Count != 1 {
			t.Fatalf("expected 1 spot with surroundings, got %d", resp.Count)
		}
	})
}

// TestHistoryHandlerWindowClamp: span is capped at 24h and t1 is clamped to
// now; the response echoes the normalized window.
func TestHistoryHandlerWindowClamp(t *testing.T) {
	withHistoryState(t, func() {
		now := time.Now().Unix()
		tooDeep := now - 48*60*60
		rec := getHistory(t, "/api/history?qth=JO32&t0="+itoa64(tooDeep)+"&t1="+itoa64(now+9999))
		resp := decodeHistoryResponse(t, rec.Body.Bytes())
		if resp.T1 != now {
			t.Fatalf("t1 should clamp to now: got %d want %d", resp.T1, now)
		}
		if resp.T1-resp.T0 > historyMaxSpanSeconds {
			t.Fatalf("span exceeds 24h: %d", resp.T1-resp.T0)
		}
	})
}

// TestHistoryHandlerFallbackReachHeader: without Postgres, windows predating
// hub.history's oldest spot get an empty bundle plus X-History-Reach.
func TestHistoryHandlerFallbackReachHeader(t *testing.T) {
	withHistoryState(t, func() {
		now := time.Now().Unix()
		hub.Lock()
		hub.history = []MQTTMessage{{T: now - 600, SC: "W1AW", RC: "DL1ABC", SL: "FN31", RL: "JO32", RP: -5, B: "20m", MD: "FT8", Source: "mqtt"}}
		hub.Unlock()

		rec := getHistory(t, "/api/history?qth=JO32&t0="+itoa64(now-7200)+"&t1="+itoa64(now-3600))
		if rec.Code != 200 {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if got := rec.Header().Get("X-History-Reach"); got == "" {
			t.Fatalf("expected X-History-Reach header on out-of-reach fallback window")
		}
		resp := decodeHistoryResponse(t, rec.Body.Bytes())
		if resp.Count != 0 {
			t.Fatalf("expected empty out-of-reach bundle, got %d spots", resp.Count)
		}
	})
}

// TestHistoryHandlerCacheRoundTrip: a fully-past window is cached (second
// request hits the cache); a live-edge window is not cached stale — a
// changed spot set within the TTL edge is re-served.
func TestHistoryHandlerCacheRoundTrip(t *testing.T) {
	withHistoryState(t, func() {
		now := time.Now().Unix()
		hub.Lock()
		hub.history = []MQTTMessage{
			{T: now - 7200, SC: "W1AW", RC: "DL1ABC", SL: "FN31", RL: "JO32", RP: -5, B: "20m", MD: "FT8", Source: "mqtt"},
		}
		hub.Unlock()

		url := "/api/history?qth=JO32&t0=" + itoa64(now-9000) + "&t1=" + itoa64(now-7200)
		rec1 := getHistory(t, url)
		if rec1.Code != 200 {
			t.Fatalf("first request: %d", rec1.Code)
		}
		rec2 := getHistory(t, url)
		if rec2.Code != 200 {
			t.Fatalf("second request: %d", rec2.Code)
		}
		if got := rec2.Header().Get("X-History-Cache"); got != "hit" {
			t.Fatalf("expected cache hit on immutable window, got %q", got)
		}

		// Live-edge window must not be cached beyond the (short) TTL — assert
		// the no-hit marker path: t1 within historyLiveEdgeSeconds of now.
		liveURL := "/api/history?qth=JO32&t0=" + itoa64(now-600) + "&t1=" + itoa64(now)
		recLive := getHistory(t, liveURL)
		if recLive.Code != 200 {
			t.Fatalf("live-edge request: %d", recLive.Code)
		}
		if got := recLive.Header().Get("X-History-Cache"); got == "hit" {
			t.Fatalf("live-edge window unexpectedly served from cache")
		}
	})
}

// TestHistoryHandlerRateLimit: exceeding historyRatePerHour per IP → 429.
func TestHistoryHandlerRateLimit(t *testing.T) {
	withHistoryState(t, func() {
		now := time.Now().Unix()
		url := "/api/history?qth=JO32&t0=" + itoa64(now-600) + "&t1=" + itoa64(now-590)
		for i := 0; i < historyRatePerHour; i++ {
			rec := getHistory(t, url)
			if rec.Code == 429 {
				t.Fatalf("request %d unexpectedly rate limited", i+1)
			}
		}
		rec := getHistory(t, url)
		if rec.Code != 429 {
			t.Fatalf("expected 429 after %d requests, got %d", historyRatePerHour, rec.Code)
		}
	})
}

// TestHistoryMessagesFromHubSortsChronologically: backfilled rows make
// hub.history ingest-ordered, not time-ordered; the bundle must be sorted.
func TestHistoryMessagesFromHubSortsChronologically(t *testing.T) {
	withHistoryState(t, func() {
		now := time.Now().Unix()
		hub.Lock()
		hub.history = []MQTTMessage{
			{T: now - 100, SC: "W1AW", RC: "DL1ABC", SL: "FN31", RL: "JO32", B: "20m", MD: "FT8", Source: "mqtt"},
			{T: now - 300, SC: "W1AW", RC: "DL1DEF", SL: "FN31", RL: "JO32", B: "20m", MD: "FT8", Source: "mqtt"},
			{T: now - 200, SC: "W1AW", RC: "DL1GHI", SL: "FN31", RL: "JO32", B: "20m", MD: "FT8", Source: "mqtt"},
		}
		hub.Unlock()

		msgs := historyMessagesFromHub(now-400, now)
		if len(msgs) != 3 {
			t.Fatalf("expected 3 messages, got %d", len(msgs))
		}
		for i := 1; i < len(msgs); i++ {
			if msgs[i].T < msgs[i-1].T {
				t.Fatalf("bundle not chronological at %d: %d < %d", i, msgs[i].T, msgs[i-1].T)
			}
		}
	})
}

// TestHistoryTargetTokensDedups: qthSet (surroundings expands to 9 squares)
// must reduce to distinct tokens for the SQL pushdown.
func TestHistoryTargetTokensDedups(t *testing.T) {
	client := &Client{qthSet: getSurroundingSquares("JO32")}
	tokens := historyTargetTokens(client)
	if len(tokens) != len(getSurroundingSquares("JO32")) {
		t.Fatalf("expected %d tokens, got %d", len(getSurroundingSquares("JO32")), len(tokens))
	}
	seen := make(map[string]bool)
	for _, tok := range tokens {
		if seen[tok] {
			t.Fatalf("duplicate token %q", tok)
		}
		seen[tok] = true
	}
}

// TestHistoryCacheEviction: LRU evicts beyond historyCacheMaxEntries.
func TestHistoryCacheEviction(t *testing.T) {
	c := &historyCache{entries: make(map[string]*historyCacheEntry)}
	for i := 0; i < historyCacheMaxEntries+5; i++ {
		c.put("key"+string(rune('a'+i)), []byte{byte(i)})
	}
	if len(c.entries) != historyCacheMaxEntries {
		t.Fatalf("expected %d entries, got %d", historyCacheMaxEntries, len(c.entries))
	}
	if _, ok := c.get("key" + string(rune('a'))); ok {
		t.Fatalf("oldest entry should have been evicted")
	}
	if _, ok := c.get("key" + string(rune('a'+historyCacheMaxEntries+4))); !ok {
		t.Fatalf("newest entry should be present")
	}
}

// itoa64 is a tiny helper so the test URLs above stay readable.
func itoa64(v int64) string {
	return strings.TrimSpace(strconv.FormatInt(v, 10))
}