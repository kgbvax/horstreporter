package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// wspr_heard.go serves the WSPR reverse-beacon view: "who heard me". The
// operator's own WSPR transmitter runs continuously and other stations report
// its signal to wspr.live; those reception reports are the most direct measure
// of what is reachable from the operator's location. This endpoint aggregates
// the already-persisted dx_raw_spots rows (source_type='wspr', receiver = the
// operator) into per-(band, hearing station) reports plus a per-(band, region)
// above/below-average baseline. Raw SNR + count are returned; the client applies
// the mode translation and power offset (single source of truth in JS).

// wsprHeardReport is one aggregated "who heard me" report in the API response.
type wsprHeardReport struct {
	Band           string  `json:"band"`
	HearingCall    string  `json:"hearing_callsign"`
	HearingLocator string  `json:"hearing_locator"`
	DistanceKm     float64 `json:"distance_km"`
	SnrMedian      float64 `json:"snr_median"`
	SnrMax         int     `json:"snr_max"`
	SnrMin         int     `json:"snr_min"`
	Count          int64   `json:"count"`
	LastHeardAt    int64   `json:"last_heard_at"`
}

// wsprHeardRegionBaseline is one (band, region) cell of the above/below-average
// view.
type wsprHeardRegionBaseline struct {
	Band          string  `json:"band"`
	Region        string  `json:"region"`
	CurrentCount  int64   `json:"current_count"`
	BaselineCount float64 `json:"baseline_count"`
	BaselineDays  int     `json:"baseline_days"`
	Ratio         float64 `json:"ratio"`
}

// wsprHeardResponse is the full /api/wspr_heard payload.
type wsprHeardResponse struct {
	QTH            string                    `json:"qth"`
	Hours          int                       `json:"hours"`
	MatchedBy      string                    `json:"matched_by"`
	GeneratedAt    int64                     `json:"generated_at"`
	Reports        []wsprHeardReport         `json:"reports"`
	RegionBaseline []wsprHeardRegionBaseline `json:"region_baseline"`
}

// wsprHeardHandler serves GET /api/wspr_heard. Params: qth (required, callsign
// or locator), hours (default 24, max 168), baseline_days (default 14),
// current_window (seconds, default 3600).
func wsprHeardHandler(w http.ResponseWriter, r *http.Request) {
	qth, _ := resolveQTHQuery(r)
	if qth == "" {
		http.Error(w, "qth required", http.StatusBadRequest)
		return
	}

	hours := 24
	if raw := strings.TrimSpace(r.URL.Query().Get("hours")); raw != "" {
		if h, err := strconv.Atoi(raw); err == nil && h > 0 {
			hours = h
		}
	}
	if hours > 168 {
		hours = 168
	}

	baselineDays := 14
	if raw := strings.TrimSpace(r.URL.Query().Get("baseline_days")); raw != "" {
		if d, err := strconv.Atoi(raw); err == nil && d > 0 {
			baselineDays = d
		}
	}
	currentWindowSec := 3600
	if raw := strings.TrimSpace(r.URL.Query().Get("current_window")); raw != "" {
		if s, err := strconv.Atoi(raw); err == nil && s > 0 {
			currentWindowSec = s
		}
	}

	matchedBy := "callsign"
	if isLocator(qth) {
		matchedBy = "locator"
	}

	resp := wsprHeardResponse{
		QTH:            qth,
		Hours:          hours,
		MatchedBy:      matchedBy,
		GeneratedAt:    time.Now().Unix(),
		Reports:        []wsprHeardReport{},
		RegionBaseline: []wsprHeardRegionBaseline{},
	}

	st := wsprHeardStore()
	if st == nil {
		// No Postgres store configured: degrade to an empty (but well-formed)
		// response rather than erroring, so the client can still render the
		// panel shell.
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := st.wsprHeardReports(ctx, qth, hours)
	if err != nil {
		logError("wspr_heard reports query failed: %v", err)
		http.Error(w, "wspr_heard query failed", http.StatusInternalServerError)
		return
	}

	// Resolve the operator's own locator for distance. A locator QTH is used
	// directly; a callsign QTH falls back to the receiver_locator stored on the
	// operator's own WSPR rows (their transmitter reports its grid).
	opLocator := ""
	if isLocator(qth) {
		opLocator = qth
	} else if loc, err := st.wsprOperatorLocator(ctx, qth, hours); err == nil {
		opLocator = loc
	}

	for _, row := range rows {
		dist := 0.0
		if opLocator != "" && row.HearingLocator != "" {
			dist = distanceKmForLocators(opLocator, row.HearingLocator)
		}
		resp.Reports = append(resp.Reports, wsprHeardReport{
			Band:           row.Band,
			HearingCall:    row.HearingCall,
			HearingLocator: row.HearingLocator,
			DistanceKm:     dist,
			SnrMedian:      row.SnrMedian,
			SnrMax:         row.SnrMax,
			SnrMin:         row.SnrMin,
			Count:          row.Count,
			LastHeardAt:    row.LastHeardAt,
		})
	}

	baseline, err := st.wsprHeardRegionBaseline(ctx, qth, baselineDays, currentWindowSec)
	if err != nil {
		// Baseline is a secondary surface; a failure here should not fail the
		// whole request. Serve the reports and an empty baseline.
		logError("wspr_heard region baseline query failed: %v", err)
		baseline = nil
	}
	for _, b := range baseline {
		resp.RegionBaseline = append(resp.RegionBaseline, wsprHeardRegionBaseline{
			Band:          b.Band,
			Region:        b.Region,
			CurrentCount:  b.CurrentCount,
			BaselineCount: b.BaselineCount,
			BaselineDays:  b.BaselineDays,
			Ratio:         b.Ratio,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// wsprHeardStore returns the Postgres store backing the WSPR reverse-beacon
// queries, or nil when Postgres isn't configured.
func wsprHeardStore() *dxPostgresStore {
	if dxBaseline == nil {
		return nil
	}
	dxBaseline.mu.RLock()
	st := dxBaseline.store
	dxBaseline.mu.RUnlock()
	return st
}

// wsprOperatorLocator returns the operator's own transmitter locator (the
// receiver_locator on their WSPR rows), used to compute distance to hearing
// stations. Returns "" when unknown.
func (s *dxPostgresStore) wsprOperatorLocator(ctx context.Context, qth string, hours int) (string, error) {
	if s == nil {
		return "", nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if hours <= 0 {
		hours = 24
	}
	if hours > 168 {
		hours = 168
	}
	qth = strings.ToUpper(strings.TrimSpace(qth))
	if qth == "" {
		return "", nil
	}
	now := time.Now().Unix()
	start := now - int64(hours)*3600
	where, args := wsprReceiverMatch(qth, start)
	query := fmt.Sprintf(`
		SELECT receiver_locator
		FROM dx_raw_spots
		WHERE source_type = 'wspr'
		  AND spot_time > $1
		  AND receiver_locator <> ''
		  AND %s
		ORDER BY spot_time DESC
		LIMIT 1
	`, where)
	var loc string
	if err := s.pool.QueryRow(ctx, query, args...).Scan(&loc); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return loc, nil
}
