// Package server wires the HTTP handlers for the pathscope binary. It owns
// the URL surface (/api/pathscope/v1/* + the embedded static assets) and
// the goroutine-free request lifecycle: read PG, score, render JSON.
//
// The package deliberately stays small. Scoring lives in internal/pathscope;
// region classification lives in internal/region. Anything here is just
// glue.
package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"horstreporter/internal/pathscope"
	"horstreporter/internal/region"
)

// Deps is the read-only dependency bag the server needs at startup. The
// fields are passed by value (not by pointer) so the receiver is a value
// type and the wiring is explicit about lifetimes.
//
// Store and Engine are interfaces (declared below) so the server can be
// tested with fakes; the production *pathscope.Store and
// *pathscope.ScoringEngine satisfy them implicitly — main.go doesn't
// need to change.
type Deps struct {
	Store      Store
	Engine     Engine
	QTH        string         // operator's Maidenhead locator
	HomeRegion region.Region  // derived from QTH
	Version    string
	Static     embed.FS       // embedded static/pathscope
	StaticDir  string         // optional filesystem override for dev
	Interval   time.Duration  // how often the SSE ticker re-scores (default 5s)
}

// Store is the read-only data surface the server needs. *pathscope.Store
// satisfies it. Test fakes implement the same four methods.
type Store interface {
	LiveRates(ctx context.Context, since, until time.Time, lanes []string) (map[pathscope.Mode]map[region.Region]map[string]int, error)
	LiveSNR(ctx context.Context, since, until time.Time) (map[pathscope.Mode]map[region.Region]pathscope.SNRStats, error)
	Baseline(ctx context.Context, lookbackDays int, now time.Time, bands []string) ([]pathscope.Baseline, error)
	SolarContext(ctx context.Context) (pathscope.SolarContext, error)
}

// Engine is the scoring surface. *pathscope.ScoringEngine satisfies it.
type Engine interface {
	ScoreCell(band string, reg region.Region, liveRates map[pathscope.Mode]int, baselines []pathscope.Baseline, liveSNR map[pathscope.Mode]pathscope.SNRStats, solar pathscope.SolarContext) pathscope.CellScore
}

// Server is the http.Handler. It is safe for concurrent use.
type Server struct {
	store       Store
	engine      Engine
	qth         string
	homeRegion  region.Region
	version     string
	static      embed.FS
	staticDir   string
	interval    time.Duration
	hub         *Hub
	mux         *http.ServeMux
	scoringStop context.CancelFunc
	scoringWG   sync.WaitGroup
}

// New returns the configured Server. The mux is built once; routes are
// registered immediately. The hub is created here so any HTTP handler can
// subscribe; the ticker is started separately via StartScoring so the
// call site controls its lifetime.
func New(d Deps) *Server {
	interval := d.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	s := &Server{
		store:      d.Store,
		engine:     d.Engine,
		qth:        d.QTH,
		homeRegion: d.HomeRegion,
		version:    d.Version,
		static:     d.Static,
		staticDir:  d.StaticDir,
		interval:   interval,
		hub:        NewHub(),
	}
	s.mux = http.NewServeMux()
	s.routes()
	return s
}

// ServeHTTP delegates to the internal mux.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// routes wires the URL surface. The binary mounts both the static assets
// AND the API under the same /pathscope/ prefix so it can be reverse-
// proxied behind a single location block. The standalone lookups
// /api/pathscope/v1/* are also wired for direct curl access.
//
// The SSE stream is mounted on both prefixes so the static UI's
// EventSource can use a same-origin URL without an nginx rewrite.
func (s *Server) routes() {
	api := http.NewServeMux()
	api.HandleFunc("/api/pathscope/v1/health", s.handleHealth)
	api.HandleFunc("/api/pathscope/v1/glance", s.handleGlance)
	api.HandleFunc("/api/pathscope/v1/cell", s.handleCell)
	api.HandleFunc("/api/pathscope/v1/stream", s.handleStream)
	s.mux.Handle("/api/pathscope/v1/", api)

	// Static — prefer disk override if PATHSCOPE_STATIC_DIR is set, so a
	// developer can edit HTML/JS without rebuilding.
	if dir := strings.TrimSpace(s.staticDir); dir != "" {
		s.mux.Handle("/pathscope/", http.StripPrefix("/pathscope/", http.FileServer(http.Dir(dir))))
		s.mux.HandleFunc("/pathscope", redirectToSlash)
	} else {
		sub, err := fs.Sub(s.static, "static")
		if err != nil {
			log.Printf("pathscope: embed fs.Sub: %v (static will 404)", err)
		} else {
			s.mux.Handle("/pathscope/", http.StripPrefix("/pathscope/", http.FileServer(http.FS(sub))))
		}
		s.mux.HandleFunc("/pathscope", redirectToSlash)
	}

	// Pathscope-prefixed API mirror so the static UI (served at
	// /pathscope/) can fetch /pathscope/api/... without a rewrite rule.
	s.mux.HandleFunc("/pathscope/api/pathscope/v1/health", s.handleHealth)
	s.mux.HandleFunc("/pathscope/api/pathscope/v1/glance", s.handleGlance)
	s.mux.HandleFunc("/pathscope/api/pathscope/v1/cell", s.handleCell)
	s.mux.HandleFunc("/pathscope/api/pathscope/v1/stream", s.handleStream)
}

// --- handlers ---

// HealthResponse is the wire shape of /api/pathscope/v1/health.
type HealthResponse struct {
	Status     string    `json:"status"`
	Version    string    `json:"version"`
	QTH        string    `json:"qth"`
	HomeRegion string    `json:"home_region"`
	ServerTime time.Time `json:"server_time"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, HealthResponse{
		Status:     "ok",
		Version:    s.version,
		QTH:        s.qth,
		HomeRegion: s.homeRegion.String(),
		ServerTime: time.Now().UTC(),
	})
}

// GlanceResponse is the wire shape of /api/pathscope/v1/glance. It is the
// payload the static UI renders as the 10×11 matrix.
type GlanceResponse struct {
	ObservedAt time.Time             `json:"observed_at"`
	WindowSec  int                   `json:"window_sec"`
	Bands      []string              `json:"bands"`
	Regions    []string              `json:"regions"`
	Cells      []GlanceCell          `json:"cells"`
	Solar      pathscope.SolarContext `json:"solar"`
}

// GlanceCell is one (band, region) entry. The mode breakdown is included
// so the static UI can render mini-bars without a second request.
type GlanceCell struct {
	Band       string                              `json:"band"`
	Region     string                              `json:"region"`
	Score      float64                             `json:"score"`
	Probability float64                            `json:"probability"`
	Confidence float64                             `json:"confidence"`
	ModeBreakdown []pathscope.ModeCellContribution `json:"mode_breakdown"`
	SolarModifier float64                           `json:"solar_modifier"`
	Samples     int                                `json:"baseline_samples"`
}

func (s *Server) handleGlance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	resp, err := s.buildGlance(ctx)
	if err != nil {
		// Log the full error server-side so an operator can diagnose;
		// return a generic envelope to the client so internal error
		// strings (which may carry PG table names or constraint hints)
		// don't leak to anonymous browsers.
		log.Printf("pathscope: glance: %v", err)
		writeError(w, http.StatusInternalServerError, "scoring pipeline unavailable")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// buildGlance runs the full scoring pipeline once and returns the wire
// payload. The HTTP handler and the SSE ticker share this function so
// they can never drift — both go through the same code path from PG to
// GlanceResponse. SolarContext errors are logged and the neutral
// default is used; only the three PG calls return errors.
//
// The returned GlanceResponse is safe to share across subscribers (it is
// fully derived state) and to marshal once and broadcast the bytes —
// but v0 marshals per-subscriber for symmetry with the main repo.
func (s *Server) buildGlance(ctx context.Context) (GlanceResponse, error) {
	now := time.Now().UTC().Truncate(time.Minute)
	since := now.Add(-5 * time.Minute)
	lookbackDays := 30 // v0: month-long baseline window
	bands := defaultBands()

	liveRates, err := s.store.LiveRates(ctx, since, now, nil)
	if err != nil {
		return GlanceResponse{}, fmt.Errorf("live rates: %w", err)
	}
	liveSNR, err := s.store.LiveSNR(ctx, since, now)
	if err != nil {
		return GlanceResponse{}, fmt.Errorf("live snr: %w", err)
	}
	baselines, err := s.store.Baseline(ctx, lookbackDays, now, bands)
	if err != nil {
		return GlanceResponse{}, fmt.Errorf("baseline: %w", err)
	}
	solar, err := s.store.SolarContext(ctx)
	if err != nil {
		// SW is non-fatal — pathscope renders with a neutral modifier
		// when the SW series is missing.
		log.Printf("pathscope: solar context: %v (using neutral)", err)
		solar = pathscope.SolarContext{ObservedAt: now}
	}

	resp := GlanceResponse{
		ObservedAt: now,
		WindowSec:  300,
		Bands:      displayBands(),
		Regions:    regionStrings(),
		Solar:      solar,
	}

	// Score every (band, region) cell. The matrix is dense: every band
	// intersects every region, even if the live rate is zero.
	for _, band := range bands {
		for _, reg := range region.AllRegions() {
			perMode := make(map[pathscope.Mode]int, len(pathscope.AllModes()))
			for _, m := range pathscope.AllModes() {
				if bandCount, ok := liveRates[m][reg]; ok {
					perMode[m] = bandCount[band]
				}
			}
			perModeSNR := make(map[pathscope.Mode]pathscope.SNRStats, len(pathscope.AllModes()))
			for _, m := range pathscope.AllModes() {
				perModeSNR[m] = liveSNR[m][reg]
			}
			cell := s.engine.ScoreCell(band, reg, perMode, baselines, perModeSNR, solar)
			resp.Cells = append(resp.Cells, GlanceCell{
				Band:          strings.ToUpper(cell.Band),
				Region:        cell.Region.String(),
				Score:         cell.Score,
				Probability:   cell.Probability,
				Confidence:    cell.Confidence,
				ModeBreakdown: cell.ModeBreakdown,
				SolarModifier: cell.SolarModifier,
				Samples:       cell.BaselineSamples,
			})
		}
	}
	return resp, nil
}

// CellResponse is the wire shape of /api/pathscope/v1/cell. It is the
// expanded view of one cell — used by the future drill-down page. v0
// returns the same shape as the glance cell, plus a few context fields.
type CellResponse struct {
	ObservedAt    time.Time                       `json:"observed_at"`
	WindowSec     int                             `json:"window_sec"`
	Band          string                          `json:"band"`
	Region        string                          `json:"region"`
	Score         float64                         `json:"score"`
	Probability   float64                         `json:"probability"`
	Confidence    float64                         `json:"confidence"`
	ModeBreakdown []pathscope.ModeCellContribution `json:"mode_breakdown"`
	Solar         pathscope.SolarContext           `json:"solar"`
	LivePerMode   map[string]int                  `json:"live_per_mode"`
	BaselineMean  float64                         `json:"baseline_mean"`
	BaselineStd   float64                         `json:"baseline_std"`
	BaselineN     int                             `json:"baseline_n"`
}

func (s *Server) handleCell(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	band := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("band")))
	regionName := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("region")))
	if band == "" || regionName == "" {
		writeError(w, http.StatusBadRequest, "band and region query parameters are required")
		return
	}
	reg := region.Region(regionName)
	if !reg.IsValid() {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("region %q is not one of the 11 named regions", regionName))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	now := time.Now().UTC().Truncate(time.Minute)
	since := now.Add(-5 * time.Minute)

	liveRates, err := s.store.LiveRates(ctx, since, now, nil)
	if err != nil {
		log.Printf("pathscope: cell %s/%s live rates: %v", band, regionName, err)
		writeError(w, http.StatusInternalServerError, "scoring pipeline unavailable")
		return
	}
	liveSNR, err := s.store.LiveSNR(ctx, since, now)
	if err != nil {
		log.Printf("pathscope: cell %s/%s live snr: %v", band, regionName, err)
		writeError(w, http.StatusInternalServerError, "scoring pipeline unavailable")
		return
	}
	baselines, err := s.store.Baseline(ctx, 30, now, []string{band})
	if err != nil {
		log.Printf("pathscope: cell %s/%s baseline: %v", band, regionName, err)
		writeError(w, http.StatusInternalServerError, "scoring pipeline unavailable")
		return
	}
	solar, err := s.store.SolarContext(ctx)
	if err != nil {
		solar = pathscope.SolarContext{ObservedAt: now}
	}

	perMode := make(map[pathscope.Mode]int, len(pathscope.AllModes()))
	perModeSNR := make(map[pathscope.Mode]pathscope.SNRStats, len(pathscope.AllModes()))
	for _, m := range pathscope.AllModes() {
		perMode[m] = liveRates[m][reg][band]
		perModeSNR[m] = liveSNR[m][reg]
	}
	cell := s.engine.ScoreCell(band, reg, perMode, baselines, perModeSNR, solar)

	// Pick the mode-specific baseline when present; otherwise the
	// aggregate. We surface the aggregate so the drill-down UI can show
	// "expected N spots / 5 min" without making a second request.
	base := pathscope.Baseline{}
	for _, b := range baselines {
		if b.Band == band && b.Region == reg && b.Mode == pathscope.ModeAny {
			base = b
		}
	}
	if !base.IsValid() {
		for _, b := range baselines {
			if b.Band == band && b.Region == reg {
				if base.RateMean == 0 {
					base = b
				} else {
					base.RateMean = (base.RateMean + b.RateMean) / 2
					base.RateStdDev = (base.RateStdDev + b.RateStdDev) / 2
					base.SampleCount += b.SampleCount
				}
			}
		}
	}

	livePerMode := make(map[string]int, len(pathscope.AllModes()))
	for _, m := range pathscope.AllModes() {
		livePerMode[m.String()] = perMode[m]
	}

	writeJSON(w, http.StatusOK, CellResponse{
		ObservedAt:    now,
		WindowSec:     300,
		Band:          strings.ToUpper(band),
		Region:        regionName,
		Score:         cell.Score,
		Probability:   cell.Probability,
		Confidence:    cell.Confidence,
		ModeBreakdown: cell.ModeBreakdown,
		Solar:         solar,
		LivePerMode:   livePerMode,
		BaselineMean:  base.RateMean,
		BaselineStd:   base.RateStdDev,
		BaselineN:     base.SampleCount,
	})
}

// --- helpers ---

// defaultBands is the v0 visible matrix — the 10 most-active HF bands
// from 80m through 6m. 160m and 2200m are excluded because their daily
// variation is too small to be interesting in a heatmap.
//
// The DB stores band names in lowercase (e.g. "20m"); the horstreporter
// UI capitalises them on the way out. Pathscope mirrors DB casing for
// the SQL filter, then presents them in title case ("20M") to the
// front-end.
func defaultBands() []string {
	return []string{
		"80m", "60m", "40m", "30m", "20m",
		"17m", "15m", "12m", "10m", "6m",
	}
}

// displayBands is the title-case form returned in the API so the UI
// doesn't have to do its own title-casing.
func displayBands() []string {
	out := make([]string, len(defaultBands()))
	for i, b := range defaultBands() {
		out[i] = strings.ToUpper(b)
	}
	return out
}

// regionStrings is the wire shape of the region's display order.
func regionStrings() []string {
	out := make([]string, 0, len(region.AllRegions()))
	for _, r := range region.AllRegions() {
		out = append(out, r.String())
	}
	return out
}

// writeJSON serialises v as JSON and writes it to w with the given status.
// Encoding errors are logged but not surfaced — the response has already
// started.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("pathscope: json encode: %v", err)
	}
}

// writeError returns a JSON error envelope.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// redirectToSlash pushes /pathscope to /pathscope/.
func redirectToSlash(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/pathscope/", http.StatusMovedPermanently)
}

// --- SSE ---

// handleStream serves the Server-Sent Events stream of glance snapshots.
// Each new subscriber immediately gets the current payload synthesised
// via buildGlance, then the loop forwards broadcasts from the hub until
// the client disconnects. A 30 s ": ping" comment heartbeat keeps
// intermediate proxies (and the main binary's ReverseProxy when
// FlushInterval is wired correctly upstream) from killing an idle
// connection.
//
// Headers, no-gzip rationale, and per-client buffer policy are documented
// in hub.go and the plan. The dialect (data: <json>\n\n with no event:
// name, optional ": ping\n\n" comments) matches what the main repo's
// cmd/horstprop/internal/feed/feed.go consumer parses.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("X-Accel-Buffering", "no") // belt-and-suspenders for any nginx in front
	w.WriteHeader(http.StatusOK)

	client := s.hub.Subscribe()
	defer s.hub.Unsubscribe(client)

	// Initial event: synthesise a fresh payload now so the browser has
	// something to render before the next ticker fires (worst case:
	// ticker is `interval` ms away).
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	initial, err := s.buildGlance(ctx)
	cancel()
	if err != nil {
		// Log the full error server-side; tell the client something went
		// wrong without echoing PG error text into the SSE stream.
		log.Printf("pathscope: stream initial build failed: %v", err)
		fmt.Fprintf(w, "event: server_error\ndata: %s\n\n", jsonError(errInternal))
		flusher.Flush()
		return
	}
	if err := writeSSEEvent(w, initial); err != nil {
		log.Printf("pathscope: stream initial write: %v", err)
		return
	}
	flusher.Flush()

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			// SSE comment — ignored by EventSource, but it forces a
			// flush which keeps idle connections from being reaped.
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case g, ok := <-client.send:
			if !ok {
				return // hub closed us
			}
			if err := writeSSEEvent(w, g); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeSSEEvent marshals g and writes it as a single SSE data: event,
// flushing once so the browser receives the bytes immediately. Returns
// the marshal or write error so the caller can decide whether to keep
// the loop running.
func writeSSEEvent(w io.Writer, g GlanceResponse) error {
	b, err := json.Marshal(g)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", b)
	return err
}

// errInternal is the wire-level message used when something goes wrong
// during an SSE initial build — it intentionally does NOT include the
// underlying PG / scoring error so internal error text doesn't leak to
// anonymous browsers. The original error is logged server-side at the
// failure site, not here.
var errInternal = errors.New("scoring pipeline unavailable")

// jsonError returns a JSON string for embedding inside an SSE
// server_error event. It deliberately uses a manual escape (not
// encoding/json) because we only need a single string field and the
// extra allocation from Marshal would dwarf the message itself.
func jsonError(err error) string {
	b, _ := json.Marshal(map[string]string{"error": err.Error()})
	return string(b)
}
