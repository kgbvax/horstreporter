package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// dxSpot is one DX-cluster spot exposed to operator tools (the Chase Queue).
// Unlike /api/stream (locator-only, target-filtered), this surfaces the DX
// callsign, frequency and comment that cluster spots actually carry.
type dxSpot struct {
	DXCall         string  `json:"dx_call"`
	Spotter        string  `json:"spotter"`
	FreqKHz        float64 `json:"freq_khz"`
	Band           string  `json:"band"`
	DXLocator      string  `json:"dx_locator,omitempty"`
	SpotterLocator string  `json:"spotter_locator,omitempty"`
	AgeSeconds     int64   `json:"age_seconds"`
	Comment        string  `json:"comment,omitempty"`
}

// dxSpotsHandler returns recent DX-cluster spots, de-duplicated to the most
// recent per (DX call, band), newest first. Read-only over hub.history.
// Params: minutes (default 15, max 60).
func dxSpotsHandler(w http.ResponseWriter, r *http.Request) {
	minutes, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("minutes")))
	if err != nil || minutes <= 0 {
		minutes = 15
	}
	if minutes > 60 {
		minutes = 60
	}

	now := time.Now().Unix()
	cutoff := now - int64(minutes*60)

	hub.RLock()
	idx := sort.Search(len(hub.history), func(i int) bool { return hub.history[i].T >= cutoff })
	window := make([]MQTTMessage, len(hub.history)-idx)
	copy(window, hub.history[idx:])
	hub.RUnlock()

	// Keep the most recent spot per (DX call, band). history is ascending in T,
	// so a later entry overwrites an earlier one.
	latest := make(map[string]MQTTMessage)
	for _, m := range window {
		if !strings.EqualFold(m.MD, "DXCLUSTER") || m.RC == "" {
			continue
		}
		latest[m.RC+"|"+m.B] = m
	}

	spots := make([]dxSpot, 0, len(latest))
	for _, m := range latest {
		age := now - m.T
		if age < 0 {
			age = 0
		}
		spots = append(spots, dxSpot{
			DXCall:         m.RC,
			Spotter:        m.SC,
			FreqKHz:        m.F,
			Band:           m.B,
			DXLocator:      m.RL,
			SpotterLocator: m.SL,
			AgeSeconds:     age,
			Comment:        m.CM,
		})
	}
	sort.Slice(spots, func(i, j int) bool { return spots[i].AgeSeconds < spots[j].AgeSeconds })

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_ = json.NewEncoder(w).Encode(spots)
}
