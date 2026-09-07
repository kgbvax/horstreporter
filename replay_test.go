package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestReplaySNRFilterExemptsWSPRLikeLive(t *testing.T) {
	// The live stream exempts DX-cluster/WSPR from the SNR threshold
	// (streamClientFilter.spotAllowed: different scales); replay must match —
	// else the app's default ssb/0dB filter empties every replay bucket.
	saved := replayLoader
	defer func() { replayLoader = saved }()
	bucketEnd := int64(1757100000)
	replayLoader = func(start, end int64, sources []string, callsignFilter string, prefixes []string, limit int) ([]MQTTMessage, bool, error) {
		return []MQTTMessage{
			{T: bucketEnd - 60, SC: "DL9ET", SL: "JO62QM", RC: "HB9ABC", RL: "JN37QM", B: "20m", RP: -20, Source: "mqtt"},
			{T: bucketEnd - 60, SC: "K1ABC", SL: "FN30AA", RC: "W2XYZ", RL: "JO62QM", B: "20m", RP: -22, Source: "wspr"},
		}, false, nil
	}

	req := httptest.NewRequest("GET", "/api/replay/spots?qth=JO62QM&bucket_end=1757100000&bucket_seconds=1800&min_snr_mode=ssb&ssb_min_db=0", nil)
	data, err := buildReplayBucket("JO62QM", false, bucketEnd, 1800, []string{"mqtt", "wspr"}, 1000, req)
	if err != nil {
		t.Fatalf("buildReplayBucket: %v", err)
	}
	var resp struct {
		Spots []streamSpot `json:"spots"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// The mqtt spot (-20 dB) is below the ssb threshold (0 dB) and must be
	// dropped; the wspr spot (-22 dB) must survive the very same filter.
	if len(resp.Spots) != 1 || resp.Spots[0].SourceType != "wspr" {
		t.Fatalf("spots = %+v, want only the exempted wspr spot", resp.Spots)
	}
}

func TestReplayRingsUsesUnfilteredLoad(t *testing.T) {
	// rings (area of interest) intentionally bypasses the SQL prefilter: the
	// loader must be called with EMPTY filter args, and the SQL's no-prefilter
	// disjunct then passes everything (Go-side matchAndCreateSpot applies the
	// area filter). Regression: the old predicate returned 0 rows whenever
	// both filters were empty, so rings>0 replay buckets were always count=0.
	saved := replayLoader
	defer func() { replayLoader = saved }()
	var gotCallsign string
	var gotPrefixes []string
	replayLoader = func(start, end int64, sources []string, callsignFilter string, prefixes []string, limit int) ([]MQTTMessage, bool, error) {
		gotCallsign, gotPrefixes = callsignFilter, prefixes
		return nil, false, nil
	}
	req := httptest.NewRequest("GET", "/api/replay/spots?qth=JO62QM&bucket_end=1757100000&bucket_seconds=1800&rings=3", nil)
	if _, err := buildReplayBucket("JO62QM", false, 1757100000, 1800, []string{"mqtt"}, 1000, req); err != nil {
		t.Fatalf("buildReplayBucket: %v", err)
	}
	if gotCallsign != "" || len(gotPrefixes) != 0 {
		t.Fatalf("rings path must skip the SQL prefilter, got callsign=%q prefixes=%v", gotCallsign, gotPrefixes)
	}
}

func TestReplaySourcesFromQuery(t *testing.T) {
	// No toggles at all: everything included (mqtt is always the main feed).
	all := replaySourcesFromQuery(map[string][]string{})
	if len(all) != 4 {
		t.Fatalf("default sources = %v, want 4 entries", all)
	}

	// Explicit "false"/"0" drops a source; "true"/"1"/absent keeps it.
	onlyMqtt := replaySourcesFromQuery(map[string][]string{
		"include_dxcluster": {"false"},
		"include_rbn":       {"0"},
		"include_wspr":      {"false"},
	})
	if len(onlyMqtt) != 1 || onlyMqtt[0] != "mqtt" {
		t.Fatalf("onlyMqtt = %v, want [mqtt]", onlyMqtt)
	}

	keepDx := replaySourcesFromQuery(map[string][]string{
		"include_dxcluster": {"true"},
		"include_rbn":       {"false"},
		"include_wspr":      {"false"},
	})
	if len(keepDx) != 2 || keepDx[0] != "dxcluster" || keepDx[1] != "mqtt" {
		t.Fatalf("keepDx = %v, want [dxcluster mqtt]", keepDx)
	}

	// Empty-string values count as absent (default true).
	empties := replaySourcesFromQuery(map[string][]string{
		"include_dxcluster": {""},
		"include_rbn":       {"false"},
	})
	if len(empties) != 3 {
		t.Fatalf("empties = %v, want 3 entries", empties)
	}
}

func TestReplayBucketSeconds(t *testing.T) {
	cases := []struct {
		raw  string
		want int64
	}{
		{"", 1800},
		{"1800", 1800},
		{"900", 900},
		{"60", 60},
		{"30", 60},     // below floor clamps up
		{"7200", 3600}, // above ceiling clamps down
		{"abc", 1800},  // parseIntDefault fallback
		{"-5", 1800},   // non-positive falls back to default
	}
	for _, c := range cases {
		if got := replayBucketSeconds(c.raw); got != c.want {
			t.Errorf("replayBucketSeconds(%q) = %d, want %d", c.raw, got, c.want)
		}
	}
}

func TestReplayAgeSemantics(t *testing.T) {
	// Spots replayed through matchAndCreateSpot with the bucket end as `now`
	// carry ages relative to that bucket end, not to wall-clock now.
	client := &Client{qthSet: []string{"JO62"}}
	bucketEnd := int64(1757100000)
	msg := MQTTMessage{T: bucketEnd - 600, SC: "DL9ET", SL: "JO62QM", RC: "HB9ABC", RL: "JN37QM", B: "20m", RP: 10}
	spot, ok := matchAndCreateSpot(client, msg, bucketEnd)
	if !ok {
		t.Fatal("matchAndCreateSpot did not match qth JO62")
	}
	if spot.AgeSeconds != 600 {
		t.Fatalf("AgeSeconds = %d, want 600 (bucket-relative)", spot.AgeSeconds)
	}
}

func TestReplayHandlersUnavailableWithoutStore(t *testing.T) {
	// Package-level tests may run with a global dxBaseline set by other tests;
	// force the nil-store path.
	saved := dxBaseline
	dxBaseline = nil
	defer func() { dxBaseline = saved }()

	req := httptest.NewRequest("GET", "/api/replay/spots?qth=JO62", nil)
	rec := httptest.NewRecorder()
	replaySpotsHandler(rec, req)
	if rec.Code != 503 {
		t.Fatalf("replaySpotsHandler with nil store: code %d, want 503", rec.Code)
	}

	req = httptest.NewRequest("GET", "/api/replay/histogram?qth=JO62", nil)
	rec = httptest.NewRecorder()
	replayHistogramHandler(rec, req)
	if rec.Code != 503 {
		t.Fatalf("replayHistogramHandler with nil store: code %d, want 503", rec.Code)
	}
}

func TestReplayCacheKeyCanonical(t *testing.T) {
	r1 := httptest.NewRequest("GET", "/api/replay/spots?qth=JO62&bucket_end=123&limit=100", nil)
	r2 := httptest.NewRequest("GET", "/api/replay/spots?limit=100&bucket_end=123&qth=JO62", nil)
	if replayCacheKeyFor(r1) != replayCacheKeyFor(r2) {
		t.Fatal("differently ordered identical queries should share a cache key")
	}
	r3 := httptest.NewRequest("GET", "/api/replay/spots?qth=JO62&bucket_end=456&limit=100", nil)
	if replayCacheKeyFor(r1) == replayCacheKeyFor(r3) {
		t.Fatal("different bucket_end must not share a cache key")
	}
}

func TestReplayFetchGroupDedupe(t *testing.T) {
	// Second concurrent caller with the same key waits for the leader's result.
	calls := 0
	key := "test-dedupe-key"
	done := make(chan struct{})
	started := make(chan struct{})
	go func() {
		data, err := replayFetchWithCache(key, func() ([]byte, error) {
			calls++
			close(started)
			<-done
			return []byte("leader"), nil
		})
		if string(data) != "leader" || err != nil {
			t.Errorf("leader got %q, %v", data, err)
		}
	}()
	<-started
	go func() {
		data, err := replayFetchWithCache(key, func() ([]byte, error) {
			calls++
			return []byte("should-not-run"), nil
		})
		if string(data) != "leader" || err != nil {
			t.Errorf("waiter got %q, %v", data, err)
		}
	}()
	// Give the waiter a moment to join the group, then finish the leader.
	done <- struct{}{}
	// After completion, a third call hits the cache without calling fn.
	data, err := replayFetchWithCache(key, func() ([]byte, error) {
		calls++
		return []byte("should-not-run"), nil
	})
	if string(data) != "leader" || err != nil {
		t.Errorf("cached call got %q, %v", data, err)
	}
	if calls != 1 {
		t.Fatalf("fn ran %d times, want 1", calls)
	}
}

func TestBandSQLFilter(t *testing.T) {
	if got := bandSQLFilter("20m", nil); got != "20m" {
		t.Fatalf("selected_band should win, got %q", got)
	}
	if got := bandSQLFilter("", map[string]struct{}{"40m": {}}); got != "40m" {
		t.Fatalf("single enabled band should apply, got %q", got)
	}
	if got := bandSQLFilter("", map[string]struct{}{"40m": {}, "20m": {}}); got != "" {
		t.Fatalf("multi-band must fall back to all bands, got %q", got)
	}
	if got := bandSQLFilter("", nil); got != "" {
		t.Fatalf("no filter should be empty, got %q", got)
	}
}
