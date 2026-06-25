// Package api exposes horstprop's scoring HTTP surface: /v1/health and /v1/score.
package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"horstreporter/cmd/horstprop/internal/engine"
	"horstreporter/internal/propcontract"
)

const version = "v1"

// Server holds the scoring API dependencies.
type Server struct {
	eng      *engine.Engine
	started  time.Time
	homeGrid string
	feedOn   bool
	scorer   string // label describing the enabled scoring layers
}

// New builds a Server. scorer is a human label of the active layers (e.g.
// "L1-empirical+L2-mufgate"), surfaced in /v1/health.
func New(eng *engine.Engine, homeGrid string, feedOn bool, scorer string) *Server {
	return &Server{eng: eng, started: time.Now(), homeGrid: homeGrid, feedOn: feedOn, scorer: scorer}
}

// Handler returns the configured HTTP mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", s.handleHealth)
	mux.HandleFunc("/v1/score", s.handleScore)
	mux.HandleFunc("/v1/debug", s.handleDebug)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"service":            "horstprop",
		"version":            version,
		"uptime_sec":         int(time.Since(s.started).Seconds()),
		"scorer":             s.scorer,
		"home_grid":          s.homeGrid,
		"home_resolved":      s.eng.HomeResolved(),
		"feed_enabled":       s.feedOn,
		"reports_stored":     s.eng.StoreLen(),
		"muf_fresh_stations": s.eng.MUFStations(),
	})
}

// handleScore returns a Score (§5.5). POST takes a Spot JSON body (§5.1); GET
// takes query params (dx_call, freq_hz, mode, grid) for easy browser/curl use.
func (s *Server) handleScore(w http.ResponseWriter, r *http.Request) {
	spot, ok := s.spotFromRequest(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.eng.Score(spot))
}

// handleDebug (GET) returns the Score plus the internals that produced it: the
// per-layer breakdown, sampled control-point MUFs, store match counts, geometry.
func (s *Server) handleDebug(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET only", "method_not_allowed")
		return
	}
	spot, ok := s.spotFromRequest(w, r)
	if !ok {
		return
	}
	sc, diag := s.eng.Debug(spot)
	diag.HomeGrid = s.homeGrid
	writeJSON(w, http.StatusOK, map[string]any{"score": sc, "diagnostics": diag})
}

// spotFromRequest parses+validates a Spot from a POST body or GET query and
// writes the error response itself (returning ok=false) on failure.
func (s *Server) spotFromRequest(w http.ResponseWriter, r *http.Request) (propcontract.Spot, bool) {
	var spot propcontract.Spot
	switch r.Method {
	case http.MethodPost:
		if err := json.NewDecoder(r.Body).Decode(&spot); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body", "bad_request")
			return spot, false
		}
	case http.MethodGet:
		q := r.URL.Query()
		spot.DXCall = q.Get("dx_call")
		spot.Mode = q.Get("mode")
		spot.Grid = q.Get("grid")
		spot.FreqHz, _ = strconv.ParseInt(strings.TrimSpace(q.Get("freq_hz")), 10, 64)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "GET or POST", "method_not_allowed")
		return spot, false
	}
	if strings.TrimSpace(spot.DXCall) == "" || spot.FreqHz <= 0 {
		writeErr(w, http.StatusBadRequest, "dx_call and freq_hz are required", "bad_request")
		return spot, false
	}
	return spot, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	// Local operator tool serving only public propagation scores (no secrets, no
	// writes, no auth) — allow browser clients (the Chase Queue UI) to read it.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg, code string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": msg, "code": code})
}
