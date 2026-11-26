package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"horstreporter/internal/maidenhead"
)

const (
	defaultAreaHeardMinutes = 15
	maxAreaHeardMinutes     = 60
	defaultAreaHeardModes   = "FT8,FT4"
)

type areaHeardStation struct {
	Call string `json:"call"`
	Band string `json:"band"`
	Mode string `json:"mode"`
	Grid string `json:"grid"`
}

type areaHeardResponse struct {
	GeneratedAt   int64              `json:"generated_at"`
	QTH           string             `json:"qth"`
	Squares       []string           `json:"squares"`
	WindowMinutes int                `json:"window_minutes"`
	Count         int                `json:"count"`
	Stations      []areaHeardStation `json:"stations"`
}

// areaHeardHandler serves GET /api/area_heard: the stations that PSKReporter
// receivers in the area (the qth square, or the 3x3 block with surroundings)
// heard in the window, one row per sender call, band and mode.
func areaHeardHandler(w http.ResponseWriter, r *http.Request) {
	qth, surroundings := resolveQTHQuery(r)
	if !maidenhead.IsLocator(qth) {
		http.Error(w, "qth must be a Maidenhead locator", http.StatusBadRequest)
		return
	}

	minutes := defaultAreaHeardMinutes
	if m, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("minutes"))); err == nil && m > 0 {
		minutes = m
	}
	if minutes > maxAreaHeardMinutes {
		minutes = maxAreaHeardMinutes
	}

	rawModes := strings.TrimSpace(r.URL.Query().Get("modes"))
	if rawModes == "" {
		rawModes = defaultAreaHeardModes
	}
	modes := make(map[string]struct{})
	for _, m := range strings.Split(rawModes, ",") {
		if m = strings.ToUpper(strings.TrimSpace(m)); m != "" {
			modes[m] = struct{}{}
		}
	}

	squares := maidenhead.QTHSquares(qth, surroundings)
	squareSet := make(map[string]struct{}, len(squares))
	for _, s := range squares {
		squareSet[s] = struct{}{}
	}

	now := time.Now().Unix()
	cutoff := now - int64(minutes)*60
	hist, release := snapshotHubHistoryWindow(now, minutes)

	type rowKey struct{ call, band, mode string }
	type locAt struct {
		grid string
		t    int64
	}
	rows := make(map[rowKey]struct{})
	// A sender's grid is the locator of its latest report in the window.
	grids := make(map[string]locAt)
	for i := range hist {
		m := &hist[i]
		// The window may start a little before cutoff (ordering slack).
		if m.T < cutoff || m.T > now {
			continue
		}
		if sourceTypeForMessage(*m) != "mqtt" {
			continue
		}
		rl := strings.ToUpper(strings.TrimSpace(m.RL))
		if len(rl) < 4 {
			continue
		}
		if _, ok := squareSet[rl[:4]]; !ok {
			continue
		}
		mode := strings.ToUpper(strings.TrimSpace(m.MD))
		if _, ok := modes[mode]; !ok {
			continue
		}
		call := strings.ToUpper(strings.TrimSpace(m.SC))
		band := normalizeBand(m.B)
		if call == "" || band == "" {
			continue
		}
		rows[rowKey{call, band, mode}] = struct{}{}
		if sl := strings.TrimSpace(m.SL); sl != "" {
			if prev, ok := grids[call]; !ok || m.T >= prev.t {
				grids[call] = locAt{grid: sl, t: m.T}
			}
		}
	}
	release()

	stations := make([]areaHeardStation, 0, len(rows))
	for k := range rows {
		stations = append(stations, areaHeardStation{Call: k.call, Band: k.band, Mode: k.mode, Grid: grids[k.call].grid})
	}
	sort.Slice(stations, func(i, j int) bool {
		a, b := stations[i], stations[j]
		if a.Band != b.Band {
			return a.Band < b.Band
		}
		if a.Call != b.Call {
			return a.Call < b.Call
		}
		return a.Mode < b.Mode
	})

	resp := areaHeardResponse{
		GeneratedAt:   now,
		QTH:           qth,
		Squares:       squares,
		WindowMinutes: minutes,
		Count:         len(stations),
		Stations:      stations,
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(resp)
}
