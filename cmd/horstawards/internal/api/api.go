// Package api exposes horstawards's HTTP surface: /v1/health, /v1/wanted, and
// /v1/refresh. The wanted query is a pure in-memory index lookup (no disk, no
// upstream), so it is fast and safe to call per spot batch.
package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"horstreporter/cmd/horstawards/internal/award"
	"horstreporter/internal/awardcontract"
)

const version = "v1"

// wantedMaxSpots caps a single /v1/wanted batch (mirrors the agent's enrich cap).
const wantedMaxSpots = 60

// Service is what the API needs from the app: the current index + degraded state
// (returned together for a consistent read), a health snapshot, and a
// manual-refresh trigger.
type Service interface {
	Snapshot() (*award.Index, bool)
	Degraded() bool
	Health() map[string]any
	TriggerRefresh()
}

// Server holds the API dependencies.
type Server struct {
	svc     Service
	started time.Time
}

// New builds a Server.
func New(svc Service) *Server {
	return &Server{svc: svc, started: time.Now()}
}

// Handler returns the configured HTTP mux wrapped in permissive CORS (this is a
// local, read-only operator tool — no secrets in responses).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", s.handleHealth)
	mux.HandleFunc("/v1/wanted", s.handleWanted)
	mux.HandleFunc("/v1/refresh", s.handleRefresh)
	return withCORS(mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	h := s.svc.Health()
	if h == nil {
		h = map[string]any{}
	}
	h["service"] = "horstawards"
	h["version"] = version
	h["uptime_sec"] = int(time.Since(s.started).Seconds())
	h["degraded"] = s.svc.Degraded()
	writeJSON(w, http.StatusOK, h)
}

// handleWanted evaluates a batch of resolved spots against the progress index.
func (s *Server) handleWanted(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if !requireJSON(w, r) {
		return
	}
	var req awardcontract.WantedRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 512*1024))
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !req.PermitLookup {
		writeErr(w, http.StatusForbidden, "permit_lookup must be true")
		return
	}
	if len(req.Spots) > wantedMaxSpots {
		req.Spots = req.Spots[:wantedMaxSpots]
	}

	ix, degraded := s.svc.Snapshot()
	results := make([]awardcontract.WantedResult, 0, len(req.Spots))
	for _, sp := range req.Spots {
		results = append(results, ix.Evaluate(sp))
	}
	writeJSON(w, http.StatusOK, awardcontract.WantedResponse{
		OK:       true,
		Degraded: degraded,
		Results:  results,
	})
}

// handleRefresh triggers an immediate async refresh of all sources. Gated on
// permit_refresh=true in a JSON body only (not a query param): requiring an
// application/json content type forces a CORS preflight for cross-origin callers,
// which the server-to-server CORS policy then rejects — closing the CSRF vector
// that a query-param + simple-request path would open.
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if !requireJSON(w, r) {
		return
	}
	var body struct {
		PermitRefresh bool `json:"permit_refresh"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	if !body.PermitRefresh {
		writeErr(w, http.StatusForbidden, "permit_refresh must be true")
		return
	}
	s.svc.TriggerRefresh()
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "refresh triggered"})
}

// requireJSON enforces a JSON content type on mutating POSTs. Beyond hygiene this
// forces a CORS preflight for cross-origin browsers (application/json is not a
// CORS-safelisted content type), which the CORS policy below then blocks.
func requireJSON(w http.ResponseWriter, r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	if !strings.EqualFold(strings.TrimSpace(ct), "application/json") {
		writeErr(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}
	return true
}

// withCORS is deliberately restrictive: horstawards is a server-to-server,
// operator-local service (the Go agent is its only client and sends no Origin).
// We allow CORS only for loopback origins (a local browser tool hitting /v1/health
// is fine) and never reflect an arbitrary Origin. A cross-origin preflight thus
// receives no Allow-Origin and is rejected by the browser — which, combined with
// the application/json requirement on mutating POSTs, closes the CSRF vector.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if isLoopbackOrigin(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isLoopbackOrigin reports whether an Origin header is a localhost/127.0.0.1/[::1]
// origin (any port). Empty origin (non-browser clients like the agent) is not a
// CORS request and returns false.
func isLoopbackOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")
	host := rest
	if i := strings.LastIndexByte(rest, ':'); i >= 0 && !strings.HasSuffix(rest, "]") {
		host = rest[:i]
	}
	host = strings.Trim(host, "[]")
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": msg})
}
