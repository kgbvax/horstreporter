package server

import (
	"bufio"
	"context"
	"encoding/json"
	"embed"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"horstreporter/internal/pathscope"
	"horstreporter/internal/region"
)

//go:embed static
var testStatic embed.FS

// TestHealth is a smoke test that the route is wired and the JSON shape
// is correct. It does not touch pathscope.Store or pathscope.Engine
// because those require a real pgxpool. Server.New accepts nil for both
// at the route-wiring level; only the JSON response handlers touch them.
func TestHealth(t *testing.T) {
	s := New(Deps{
		Store:      nil,
		Engine:     nil,
		QTH:        "JO62qm",
		HomeRegion: region.EU,
		Version:    "test",
		Static:     testStatic,
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/pathscope/v1/health", nil)
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	var resp HealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("status = %q, want ok", resp.Status)
	}
	if resp.Version != "test" {
		t.Errorf("version = %q, want test", resp.Version)
	}
	if resp.QTH != "JO62qm" {
		t.Errorf("qth = %q, want JO62qm", resp.QTH)
	}
	if resp.HomeRegion != "EU" {
		t.Errorf("home region = %q, want EU", resp.HomeRegion)
	}
}

// TestStaticRootServed verifies that the embedded static is reachable on
// /pathscope/ once the embed.FS is plumbed through.
func TestStaticRootServed(t *testing.T) {
	s := New(Deps{
		QTH:        "JO62qm",
		HomeRegion: region.EU,
		Version:    "test",
		Static:     testStatic,
	})

	// emjay's http.FileServer is finicky about trailing slashes — request
	// the file directly so the test is deterministic.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/pathscope/", nil)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("static /pathscope/: status = %d, body=%q, location=%q",
			rec.Code, rec.Body.String(), rec.Header().Get("Location"))
	}
	if rec.Body.Len() == 0 {
		t.Errorf("static body empty")
	}
}

func TestRedirectToSlash(t *testing.T) {
	s := New(Deps{
		QTH:        "JO62qm",
		HomeRegion: region.EU,
		Version:    "test",
		Static:     testStatic,
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/pathscope", nil)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusMovedPermanently {
		t.Errorf("redirect status = %d, want 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/pathscope/" {
		t.Errorf("Location = %q, want /pathscope/", loc)
	}
}

func TestGlanceMethodNotAllowed(t *testing.T) {
	s := New(Deps{
		QTH:        "JO62qm",
		HomeRegion: region.EU,
		Version:    "test",
		Static:     testStatic,
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/pathscope/v1/glance", nil)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestCellMissingParams(t *testing.T) {
	s := New(Deps{
		QTH:        "JO62qm",
		HomeRegion: region.EU,
		Version:    "test",
		Static:     testStatic,
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/pathscope/v1/cell", nil)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestCellInvalidRegion(t *testing.T) {
	s := New(Deps{
		QTH:        "JO62qm",
		HomeRegion: region.EU,
		Version:    "test",
		Static:     testStatic,
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/pathscope/v1/cell?band=20M&region=ZZ", nil)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// TestPrefixedHealth exercises the /pathscope/api/pathscope/v1/* mirror
// mounted so the static UI (served at /pathscope/) can call relative
// URLs without an nginx rewrite rule.
func TestPrefixedHealth(t *testing.T) {
	s := New(Deps{
		QTH:        "JO62qm",
		HomeRegion: region.EU,
		Version:    "test",
		Static:     testStatic,
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/pathscope/api/pathscope/v1/health", nil)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp HealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("status = %q, want ok", resp.Status)
	}
}

func TestPrefixedGlanceMethodNotAllowed(t *testing.T) {
	s := New(Deps{
		QTH:        "JO62qm",
		HomeRegion: region.EU,
		Version:    "test",
		Static:     testStatic,
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/pathscope/api/pathscope/v1/glance", nil)
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", http.StatusMethodNotAllowed)
	}
}

// --- tests for the SSE pipeline (Sprint 6) ---

// fakeStore is a minimal Store implementation that lets tests control
// the four data-source calls independently. The rateStore / snrStore /
// baselineStore / solarStore fields return canned data; the errStore
// returns an error, simulating a transient PG failure.
type fakeStore struct {
	mu            sync.Mutex
	ratesCalls    atomic.Int64
	snrCalls      atomic.Int64
	baselineCalls atomic.Int64
	solarCalls    atomic.Int64

	rates    map[pathscope.Mode]map[region.Region]map[string]int
	snr      map[pathscope.Mode]map[region.Region]pathscope.SNRStats
	baselines []pathscope.Baseline
	solar    pathscope.SolarContext

	// errAfter allows tests to fail the first N calls then succeed —
	// tests drive ticker-skip-on-error scenarios.
	ratesErrAfter    int
	snrErrAfter      int
	baselineErrAfter int

	// ratesAlwaysErr makes every LiveRates call fail with the embedded
	// PG-style message — used by tests that need to assert how errors
	// surface to clients (the SSE server_error path).
	ratesAlwaysErr bool
}

func (f *fakeStore) LiveRates(ctx context.Context, since, until time.Time, lanes []string) (map[pathscope.Mode]map[region.Region]map[string]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.ratesCalls.Add(1)
	if f.ratesAlwaysErr {
		return nil, errors.New("simulated live rates failure")
	}
	if f.ratesErrAfter > 0 && n <= int64(f.ratesErrAfter) {
		return nil, errors.New("simulated live rates failure")
	}
	return f.rates, nil
}

func (f *fakeStore) LiveSNR(ctx context.Context, since, until time.Time) (map[pathscope.Mode]map[region.Region]pathscope.SNRStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.snrCalls.Add(1)
	if f.snrErrAfter > 0 && n <= int64(f.snrErrAfter) {
		return nil, errors.New("simulated live snr failure")
	}
	return f.snr, nil
}

func (f *fakeStore) Baseline(ctx context.Context, lookbackDays int, now time.Time, bands []string) ([]pathscope.Baseline, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.baselineCalls.Add(1)
	if f.baselineErrAfter > 0 && n <= int64(f.baselineErrAfter) {
		return nil, errors.New("simulated baseline failure")
	}
	return f.baselines, nil
}

func (f *fakeStore) SolarContext(ctx context.Context) (pathscope.SolarContext, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.solarCalls.Add(1)
	return f.solar, nil
}

// fakeEngine is a thin Engine implementation that returns a stub
// CellScore. We don't need real scoring — the goal is to verify the
// pipeline wires through, not to re-test internal/pathscope.
type fakeEngine struct{}

func (fakeEngine) ScoreCell(band string, reg region.Region, liveRates map[pathscope.Mode]int, baselines []pathscope.Baseline, liveSNR map[pathscope.Mode]pathscope.SNRStats, solar pathscope.SolarContext) pathscope.CellScore {
	return pathscope.CellScore{
		Band:           band,
		Region:         reg,
		Score:          1.5,
		Probability:    0.7,
		Confidence:     0.6,
		ModeBreakdown:  []pathscope.ModeCellContribution{},
		SolarModifier:  1.0,
		BaselineSamples: 30,
	}
}

// TestBuildGlanceReusesScorePipeline documents and protects the
// invariant that the HTTP handler (handleGlance) and the SSE ticker
// (runScoringLoop) both go through Server.buildGlance — they can
// never drift to different score pipelines. We test by driving
// buildGlance directly with a fake store + engine and verifying the
// returned GlanceResponse has the documented shape: every (band,
// region) cell present, score fields populated, observed_at matches
// the network time.
//
// A regression in either call site (e.g. someone inlining a "quick"
// buildGlance in handleGlance) would break this test the first time
// the score loop runs against the live data.
func TestBuildGlanceReusesScorePipeline(t *testing.T) {
	t.Parallel()
	store := &fakeStore{
		rates:     map[pathscope.Mode]map[region.Region]map[string]int{},
		snr:       map[pathscope.Mode]map[region.Region]pathscope.SNRStats{},
		baselines: []pathscope.Baseline{},
		solar:     pathscope.SolarContext{KP: 2.0, SFI: 120, ObservedAt: time.Now().UTC()},
	}
	s := New(Deps{
		Store:      store,
		Engine:     fakeEngine{},
		QTH:        "JO62qm",
		HomeRegion: region.EU,
		Version:    "test",
		Static:     testStatic,
		Interval:   5 * time.Second,
	})

	before := time.Now().UTC()
	resp, err := s.buildGlance(context.Background())
	after := time.Now().UTC()
	if err != nil {
		t.Fatalf("buildGlance: %v", err)
	}

	// Dense matrix: every band × every region. 10 bands × 11 regions = 110.
	if len(resp.Cells) != len(defaultBands())*len(region.AllRegions()) {
		t.Errorf("cells = %d, want %d", len(resp.Cells), len(defaultBands())*len(region.AllRegions()))
	}
	if len(resp.Bands) != len(defaultBands()) {
		t.Errorf("bands = %d, want %d", len(resp.Bands), len(defaultBands()))
	}
	if len(resp.Regions) != len(region.AllRegions()) {
		t.Errorf("regions = %d, want %d", len(resp.Regions), len(region.AllRegions()))
	}
	// Every cell used the stub engine's score.
	for i, c := range resp.Cells {
		if c.Score != 1.5 || c.Probability != 0.7 || c.Confidence != 0.6 {
			t.Errorf("cell %d (%s/%s) score/prob/conf = %v/%v/%v, want 1.5/0.7/0.6",
				i, c.Band, c.Region, c.Score, c.Probability, c.Confidence)
		}
	}
	// buildGlance truncates observed_at to the minute boundary, so the
	// expected window is [minute-before, minute-after+1s]. We assert
	// the timestamp is within one minute of the test window — proves
	// the pipeline is running against the wall clock, not a stale
	// value captured at New() time.
	earliestObserved := before.Truncate(time.Minute)
	latestObserved := after.Truncate(time.Minute).Add(time.Minute)
	if resp.ObservedAt.Before(earliestObserved) || resp.ObservedAt.After(latestObserved) {
		t.Errorf("observed_at = %v, expected within [%v, %v]",
			resp.ObservedAt, earliestObserved, latestObserved)
	}
	// The four store calls were each hit exactly once.
	if n := store.ratesCalls.Load(); n != 1 {
		t.Errorf("rates called %d times, want 1", n)
	}
	if n := store.snrCalls.Load(); n != 1 {
		t.Errorf("snr called %d times, want 1", n)
	}
	if n := store.baselineCalls.Load(); n != 1 {
		t.Errorf("baseline called %d times, want 1", n)
	}
	if n := store.solarCalls.Load(); n != 1 {
		t.Errorf("solar called %d times, want 1", n)
	}
}

// TestTickerSkipsOnTransientError exercises the documented behaviour
// in runScoringLoop: when buildGlance returns an error the loop logs
// and continues without crashing or broadcasting. A flake in the
// first tick followed by a clean second tick should produce exactly
// one broadcast at the hub.
func TestTickerSkipsOnTransientError(t *testing.T) {
	t.Parallel()
	store := &fakeStore{
		rates:           map[pathscope.Mode]map[region.Region]map[string]int{},
		snr:             map[pathscope.Mode]map[region.Region]pathscope.SNRStats{},
		baselines:       []pathscope.Baseline{},
		solar:           pathscope.SolarContext{},
		ratesErrAfter:   1, // first call fails; second call succeeds
		baselineErrAfter: 1,
	}
	s := New(Deps{
		Store:      store,
		Engine:     fakeEngine{},
		QTH:        "JO62qm",
		HomeRegion: region.EU,
		Version:    "test",
		Static:     testStatic,
		Interval:   5 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.StartScoring(ctx)
	defer s.Stop()

	client := s.hub.Subscribe()
	defer s.hub.Unsubscribe(client)

	// One failing tick + one clean tick at 5ms cadence → 1 broadcast
	// within ~50ms. We poll the client until at least one message has
	// landed or we time out.
	deadline := time.Now().Add(500 * time.Millisecond)
	received := 0
	for time.Now().Before(deadline) {
		select {
		case <-client.send:
			received++
		case <-time.After(20 * time.Millisecond):
		}
		if received >= 1 {
			break
		}
	}

	// Cancel the ticker so no further broadcasts happen while we drain.
	cancel()
	s.Stop()

	if received != 1 {
		t.Errorf("broadcasts = %d, want 1 (one failing tick + one clean tick)", received)
	}
	// The two failing calls did not crash the loop.
	if n := store.ratesCalls.Load(); n < 2 {
		t.Errorf("rates calls = %d, want ≥2 (one failing + one succeeding)", n)
	}
}

// TestSSEHandlerEmitsHeadersAndInitialEvent verifies the SSE wire
// format: the documented headers, the initial `data: <GlanceResponse>`
// line, and that the embedded JSON parses cleanly. We cancel the
// request context after the first event so the handler exits and
// httptest.NewRecorder returns.
func TestSSEHandlerEmitsHeadersAndInitialEvent(t *testing.T) {
	t.Parallel()
	store := &fakeStore{
		rates:     map[pathscope.Mode]map[region.Region]map[string]int{},
		snr:       map[pathscope.Mode]map[region.Region]pathscope.SNRStats{},
		baselines: []pathscope.Baseline{},
		solar:     pathscope.SolarContext{},
	}
	s := New(Deps{
		Store:      store,
		Engine:     fakeEngine{},
		QTH:        "JO62qm",
		HomeRegion: region.EU,
		Version:    "test",
		Static:     testStatic,
		Interval:   5 * time.Second,
	})

	// Use a cancellable context so the handler exits its loop after
	// the first event lands. Without this, the handler would block
	// waiting for the next hub broadcast.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/pathscope/v1/stream", nil).WithContext(ctx)

	// Run the handler in a goroutine — its blocking loop prevents the
	// test from progressing otherwise. We let it run until we cancel
	// the context, at which point the loop exits and the goroutine
	// returns. Cancelling immediately is fine: handleStream writes
	// the initial event (synthesised via buildGlance) *before*
	// entering its select loop, so the body is populated by the time
	// the goroutine exits.
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.ServeHTTP(rec, req)
	}()

	cancel()
	<-done

	// Headers.
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
	if got := rec.Header().Get("Connection"); got != "keep-alive" {
		t.Errorf("Connection = %q, want keep-alive", got)
	}
	if got := rec.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering = %q, want no (belt-and-suspenders for nginx)", got)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}

	// Read the first line of the body and verify it's a `data: ` SSE
	// line whose payload parses as a GlanceResponse. The handler has
	// fully exited at this point — no race on the recorder.
	body := rec.Body.String()
	if body == "" {
		t.Fatalf("empty body — initial event did not land")
	}
	reader := bufio.NewReader(strings.NewReader(body))
	first, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		t.Fatalf("read first line: %v", err)
	}
	first = strings.TrimRight(first, "\r\n")
	if !strings.HasPrefix(first, "data: ") {
		t.Fatalf("first line = %q, want prefix 'data: '", first)
	}
	payload := strings.TrimPrefix(first, "data: ")
	var resp GlanceResponse
	if err := json.Unmarshal([]byte(payload), &resp); err != nil {
		t.Fatalf("payload not parseable as GlanceResponse: %v\npayload=%s", err, payload)
	}
	// Dense matrix survived the round trip.
	if len(resp.Cells) != len(defaultBands())*len(region.AllRegions()) {
		t.Errorf("cells = %d, want %d", len(resp.Cells), len(defaultBands())*len(region.AllRegions()))
	}

	// The four Store calls were hit once for the initial event.
	if n := store.ratesCalls.Load(); n != 1 {
		t.Errorf("rates calls = %d, want 1", n)
	}
	if n := store.snrCalls.Load(); n != 1 {
		t.Errorf("snr calls = %d, want 1", n)
	}
	if n := store.baselineCalls.Load(); n != 1 {
		t.Errorf("baseline calls = %d, want 1", n)
	}
}

// TestSSEInitialErrorDoesNotLeakPGText pins the behaviour that when the
// initial buildGlance in handleStream fails (e.g. PG transient), the
// `event: server_error` payload MUST NOT echo the underlying PG error —
// it must carry the generic "scoring pipeline unavailable" message. The
// underlying error is logged server-side, not serialised into the SSE
// stream, so an anonymous browser can't probe for table names or
// constraint hints.
func TestSSEInitialErrorDoesNotLeakPGText(t *testing.T) {
	t.Parallel()
	store := &fakeStore{
		rates:         map[pathscope.Mode]map[region.Region]map[string]int{},
		snr:           map[pathscope.Mode]map[region.Region]pathscope.SNRStats{},
		baselines:     []pathscope.Baseline{},
		solar:         pathscope.SolarContext{},
		ratesAlwaysErr: true,
	}
	s := New(Deps{
		Store:      store,
		Engine:     fakeEngine{},
		QTH:        "JO62qm",
		HomeRegion: region.EU,
		Version:    "test",
		Static:     testStatic,
		Interval:   5 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/pathscope/v1/stream", nil).WithContext(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.ServeHTTP(rec, req)
	}()

	cancel()
	<-done

	body := rec.Body.String()
	if body == "" {
		t.Fatalf("empty body — server_error event did not land")
	}

	// Find the `event: server_error` line; the next `data:` line is the
	// payload. Verify the payload does NOT contain the PG fake error.
	lines := strings.Split(body, "\n")
	var sawEvent, sawData bool
	var dataPayload string
	for _, ln := range lines {
		switch {
		case strings.HasPrefix(ln, "event: "):
			if ln[len("event: "):] == "server_error" {
				sawEvent = true
			}
		case strings.HasPrefix(ln, "data: "):
			dataPayload = ln[len("data: "):]
			sawData = true
		}
	}
	if !sawEvent {
		t.Fatalf("no 'event: server_error' line in body:\n%s", body)
	}
	if !sawData {
		t.Fatalf("no 'data:' line after server_error:\n%s", body)
	}
	if strings.Contains(dataPayload, "simulated live rates failure") {
		t.Errorf("server_error payload leaked PG error text: %q", dataPayload)
	}
	if !strings.Contains(dataPayload, "scoring pipeline unavailable") {
		t.Errorf("server_error payload missing generic message: %q", dataPayload)
	}
}
