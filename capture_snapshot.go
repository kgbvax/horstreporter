package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type captureSnapshotResponse struct {
	Target       string       `json:"target"`
	Surroundings bool         `json:"surroundings"`
	SnapshotAt   int64        `json:"snapshot_at"`
	WindowMin    int          `json:"window_minutes"`
	GeneratedAt  int64        `json:"generated_at"`
	Count        int          `json:"count"`
	Spots        []streamSpot `json:"spots"`
}

func captureSnapshotHandler(w http.ResponseWriter, r *http.Request) {
	target, surroundings := resolveTargetQuery(r)
	if target == "" {
		http.Error(w, "target required", http.StatusBadRequest)
		return
	}

	snapshotAt := time.Now().Unix()
	if raw := strings.TrimSpace(r.URL.Query().Get("snapshot_at")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			http.Error(w, "snapshot_at must be a unix timestamp", http.StatusBadRequest)
			return
		}
		snapshotAt = parsed
	}

	windowMinutes := parseIntDefault(r.URL.Query().Get("minutes"), 15)
	if windowMinutes <= 0 {
		windowMinutes = 15
	}
	if windowMinutes > 720 {
		windowMinutes = 720
	}

	minSnrMode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("min_snr_mode")))
	ssbMinDb := parseIntDefault(r.URL.Query().Get("ssb_min_db"), 0)
	cwMinDb := parseIntDefault(r.URL.Query().Get("cw_min_db"), -15)
	selectedBand := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("selected_band")))
	enabledBands := parseEnabledBands(r.URL.Query().Get("enabled_bands"))
	includeDxcluster := true
	if raw := strings.TrimSpace(r.URL.Query().Get("include_dxcluster")); raw != "" {
		includeDxcluster = strings.EqualFold(raw, "true") || raw == "1"
	}
	includeRbn := true
	if raw := strings.TrimSpace(r.URL.Query().Get("include_rbn")); raw != "" {
		includeRbn = strings.EqualFold(raw, "true") || raw == "1"
	}

	targets := []string{target}
	if surroundings && isLocator(target) {
		targets = getSurroundingSquares(target)
	}
	client := &Client{targets: targets}

	cutoff := snapshotAt - int64(windowMinutes*60)

	hub.RLock()
	historyCopy := make([]MQTTMessage, len(hub.history))
	copy(historyCopy, hub.history)
	hub.RUnlock()

	spots := make([]streamSpot, 0)
	for _, msg := range historyCopy {
		if msg.T < cutoff || msg.T > snapshotAt {
			continue
		}

		spot, ok := matchAndCreateSpot(client, msg, snapshotAt)
		if !ok {
			continue
		}

		if !includeDxcluster && strings.EqualFold(spot.SourceType, "dxcluster") {
			continue
		}
		if !includeRbn && strings.EqualFold(spot.SourceType, "rbn") {
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

		spots = append(spots, toStreamSpot(spot))
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

	resp := captureSnapshotResponse{
		Target:       target,
		Surroundings: surroundings,
		SnapshotAt:   snapshotAt,
		WindowMin:    windowMinutes,
		GeneratedAt:  time.Now().Unix(),
		Count:        len(spots),
		Spots:        spots,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
