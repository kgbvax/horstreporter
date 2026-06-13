package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	dxPulseModeQuality                 = "quality"
	dxPulseModeAnomaly                 = "anomaly"
	dxPulseDefaultWindowMinutes        = 15
	dxPulseMaxWindowMinutes            = 60
	dxPulseDefaultBaselineLookbackDays = 45
	dxPulseMaxBaselineLookbackDays     = 90
	dxPulseBaselineExclusionSeconds    = 60 * 60
	dxPulseMinBaselineSupport          = 12
)

type dxPulseRegion string

const (
	dxPulseRegionUnknown dxPulseRegion = "??"
	dxPulseRegionEU      dxPulseRegion = "EU"
	dxPulseRegionNA      dxPulseRegion = "NA"
	dxPulseRegionSA      dxPulseRegion = "SA"
	dxPulseRegionAF      dxPulseRegion = "AF"
	dxPulseRegionAS      dxPulseRegion = "AS"
	dxPulseRegionOC      dxPulseRegion = "OC"
	dxPulseRegionAN      dxPulseRegion = "AN"
	dxPulseRegionJA      dxPulseRegion = "JA"
	dxPulseRegionVK      dxPulseRegion = "VK"
	dxPulseRegionKH6     dxPulseRegion = "KH6"
	dxPulseRegionCAR     dxPulseRegion = "CAR"
)

var dxPulseAllRegions = []dxPulseRegion{
	dxPulseRegionEU,
	dxPulseRegionNA,
	dxPulseRegionSA,
	dxPulseRegionAF,
	dxPulseRegionAS,
	dxPulseRegionJA,
	dxPulseRegionOC,
	dxPulseRegionVK,
	dxPulseRegionKH6,
	dxPulseRegionCAR,
	dxPulseRegionAN,
}

var dxPulseBandOrder = []string{
	"2200m", "630m", "160m", "80m", "60m", "40m", "30m", "20m",
	"17m", "15m", "12m", "10m", "6m", "4m", "2m", "1.25m", "70cm", "33cm", "23cm",
}

var dxPulseDefaultVisibleBands = []string{
	"80m", "60m", "40m", "30m", "20m", "17m", "15m", "12m", "10m", "6m",
}

type dxPulseMatrixResponse struct {
	Target               string                `json:"target"`
	Surroundings         bool                  `json:"surroundings"`
	Mode                 string                `json:"mode"`
	ModeLabel            string                `json:"mode_label"`
	WindowMinutes        int                   `json:"window_minutes"`
	GeneratedAt          int64                 `json:"generated_at"`
	SlotOfDay            int                   `json:"slot_of_day"`
	BaselineLookbackDays int                   `json:"baseline_lookback_days,omitempty"`
	BaselineAvailable    bool                  `json:"baseline_available"`
	Bands                []string              `json:"bands"`
	Regions              []string              `json:"regions"`
	Matrix               [][]dxPulseMatrixCell `json:"matrix"`
}

type dxPulseSummaryResponse struct {
	Target            string                 `json:"target"`
	Surroundings      bool                   `json:"surroundings"`
	Mode              string                 `json:"mode"`
	ModeLabel         string                 `json:"mode_label"`
	WindowMinutes     int                    `json:"window_minutes"`
	GeneratedAt       int64                  `json:"generated_at"`
	BaselineAvailable bool                   `json:"baseline_available"`
	BestBands         []dxPulseSummaryBand   `json:"best_bands"`
	TopRegions        []dxPulseSummaryRegion `json:"top_regions"`
	HotCells          []dxPulseSummaryCell   `json:"hot_cells"`
}

type dxPulseSummaryBand struct {
	Band             string  `json:"band"`
	State            string  `json:"state"`
	Label            string  `json:"label"`
	CurrentSpotCount int     `json:"current_spot_count"`
	Confidence       float64 `json:"confidence"`
}

type dxPulseSummaryRegion struct {
	Region           string  `json:"region"`
	TotalSpotCount   int     `json:"total_spot_count"`
	ActiveBands      int     `json:"active_bands"`
	BestBand         string  `json:"best_band,omitempty"`
	BestBandState    string  `json:"best_band_state,omitempty"`
	BestBandLabel    string  `json:"best_band_label,omitempty"`
	BestBandStrength float64 `json:"best_band_strength"`
}

type dxPulseSummaryCell struct {
	Band             string  `json:"band"`
	Region           string  `json:"region"`
	State            string  `json:"state"`
	Label            string  `json:"label"`
	CurrentSpotCount int     `json:"current_spot_count"`
	Confidence       float64 `json:"confidence"`
	Strength         float64 `json:"strength"`
}

type dxPulseMatrixCell struct {
	Band                  string  `json:"band"`
	Region                string  `json:"region"`
	State                 string  `json:"state"`
	Label                 string  `json:"label"`
	ColorBucket           string  `json:"color_bucket"`
	CurrentSpotCount      int     `json:"current_spot_count"`
	CurrentUniquePaths    int     `json:"current_unique_paths"`
	CurrentUniqueGrids    int     `json:"current_unique_remote_grids"`
	AvgSNR                float64 `json:"avg_snr,omitempty"`
	MedianSNR             float64 `json:"median_snr,omitempty"`
	LastSeenAgeSeconds    int64   `json:"last_seen_age_seconds,omitempty"`
	BaselineExpectedSpots float64 `json:"baseline_expected_spot_count,omitempty"`
	BaselineRatio         float64 `json:"baseline_ratio,omitempty"`
	BaselineSupport       int     `json:"baseline_support,omitempty"`
	BaselineSupportDays   int     `json:"baseline_support_days,omitempty"`
	Confidence            float64 `json:"confidence"`
}

type dxPulseCellAccumulator struct {
	spotCount         int
	uniquePaths       map[string]struct{}
	uniqueRemoteGrids map[string]struct{}
	sumSNR            int
	snrs              []int
	lastSeen          int64
}

type dxPulseBaselineAccumulator struct {
	totalCount     int
	activeDays     map[int64]struct{}
	activeDayCount int
	uniquePaths    map[string]struct{}
}

type dxPulseObservation struct {
	band      string
	region    dxPulseRegion
	pathKey   string
	remote4   string
	snr       int
	timestamp int64
}

func dxPulseMatrixHandler(w http.ResponseWriter, r *http.Request) {
	target, surroundings, mode, minutes, lookbackDays, historyCopy, baselineAggs, baselineSpots, baselineAvailable, now, ok := loadDxPulseRequestData(r)
	if !ok {
		http.Error(w, "locator target required", http.StatusBadRequest)
		return
	}
	resp := buildDxPulseMatrixFromAggregates(target, surroundings, mode, minutes, lookbackDays, historyCopy, baselineAggs, baselineAvailable, now)
	if mode == dxPulseModeAnomaly && len(baselineAggs) == 0 && len(baselineSpots) > 0 {
		resp = buildDxPulseMatrix(target, surroundings, mode, minutes, lookbackDays, historyCopy, baselineSpots, baselineAvailable, now)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func dxPulseSummaryHandler(w http.ResponseWriter, r *http.Request) {
	target, surroundings, mode, minutes, lookbackDays, historyCopy, baselineAggs, baselineSpots, baselineAvailable, now, ok := loadDxPulseRequestData(r)
	if !ok {
		http.Error(w, "locator target required", http.StatusBadRequest)
		return
	}
	resp := buildDxPulseMatrixFromAggregates(target, surroundings, mode, minutes, lookbackDays, historyCopy, baselineAggs, baselineAvailable, now)
	if mode == dxPulseModeAnomaly && len(baselineAggs) == 0 && len(baselineSpots) > 0 {
		resp = buildDxPulseMatrix(target, surroundings, mode, minutes, lookbackDays, historyCopy, baselineSpots, baselineAvailable, now)
	}

	summary := buildDxPulseSummary(resp)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(summary)
}

func loadDxPulseRequestData(r *http.Request) (string, bool, string, int, int, []MQTTMessage, map[string]*dxPulseBaselineAccumulator, []MQTTMessage, bool, int64, bool) {
	target := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("target")))
	if target == "" {
		target = strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("locator")))
	}
	if !isLocator(target) {
		return "", false, "", 0, 0, nil, nil, nil, false, 0, false
	}
	target = strings.ToUpper(strings.TrimSpace(target[:4]))
	mode := normalizeDxPulseMode(r.URL.Query().Get("mode"))
	surroundings := r.URL.Query().Get("surroundings") == "true"
	minutes := parseIntDefault(r.URL.Query().Get("minutes"), dxPulseDefaultWindowMinutes)
	if minutes <= 0 {
		minutes = dxPulseDefaultWindowMinutes
	}
	if minutes > dxPulseMaxWindowMinutes {
		minutes = dxPulseMaxWindowMinutes
	}
	lookbackDays := parseIntDefault(r.URL.Query().Get("lookback_days"), dxPulseDefaultBaselineLookbackDays)
	if lookbackDays <= 0 {
		lookbackDays = dxPulseDefaultBaselineLookbackDays
	}
	if lookbackDays > dxPulseMaxBaselineLookbackDays {
		lookbackDays = dxPulseMaxBaselineLookbackDays
	}
	now := time.Now().Unix()

	hub.RLock()
	historyCopy := make([]MQTTMessage, len(hub.history))
	copy(historyCopy, hub.history)
	hub.RUnlock()

	targets := expandDxPulseTargets(target, surroundings)
	baselineAggs := map[string]*dxPulseBaselineAccumulator{}
	baselineSpots := []MQTTMessage(nil)
	baselineAvailable := false
	if mode == dxPulseModeAnomaly && dxBaseline != nil {
		if aggs, available, err := dxBaseline.LoadDxPulseBaseline(targets, lookbackDays, minutes, now); err == nil && available {
			baselineAggs = aggs
			baselineAvailable = true
		} else {
			baselineEnd := now - dxPulseBaselineExclusionSeconds
			baselineStart := now - int64(lookbackDays*24*60*60)
			if baselineEnd > baselineStart {
				if spots, err := dxBaseline.LoadSpotsBetween(baselineStart, baselineEnd); err == nil {
					baselineSpots = spots
					baselineAvailable = true
				}
			}
		}
	}
	return target, surroundings, mode, minutes, lookbackDays, historyCopy, baselineAggs, baselineSpots, baselineAvailable, now, true
}

func buildDxPulseMatrix(target string, surroundings bool, mode string, windowMinutes int, lookbackDays int, currentSpots, baselineSpots []MQTTMessage, baselineAvailable bool, now int64) dxPulseMatrixResponse {
	baselineAggs := map[string]*dxPulseBaselineAccumulator{}
	if mode == dxPulseModeAnomaly && baselineAvailable {
		baselineAggs = aggregateDxPulseBaseline(baselineSpots, expandDxPulseTargets(target, surroundings), windowMinutes, now)
	}
	return buildDxPulseMatrixFromAggregates(target, surroundings, mode, windowMinutes, lookbackDays, currentSpots, baselineAggs, baselineAvailable, now)
}

func buildDxPulseMatrixFromAggregates(target string, surroundings bool, mode string, windowMinutes int, lookbackDays int, currentSpots []MQTTMessage, baselineAggs map[string]*dxPulseBaselineAccumulator, baselineAvailable bool, now int64) dxPulseMatrixResponse {
	mode = normalizeDxPulseMode(mode)
	if windowMinutes <= 0 {
		windowMinutes = dxPulseDefaultWindowMinutes
	}
	if lookbackDays <= 0 {
		lookbackDays = dxPulseDefaultBaselineLookbackDays
	}
	target = strings.ToUpper(strings.TrimSpace(target))
	if len(target) >= 4 {
		target = target[:4]
	}
	targets := expandDxPulseTargets(target, surroundings)

	resp := dxPulseMatrixResponse{
		Target:               target,
		Surroundings:         surroundings,
		Mode:                 mode,
		ModeLabel:            dxPulseModeLabel(mode),
		WindowMinutes:        windowMinutes,
		GeneratedAt:          now,
		SlotOfDay:            utcSlotOfDay(now),
		BaselineLookbackDays: lookbackDays,
		BaselineAvailable:    baselineAvailable,
		Bands:                []string{},
		Regions:              dxPulseRegionStrings(),
		Matrix:               [][]dxPulseMatrixCell{},
	}

	currentAggs := aggregateDxPulseCurrent(currentSpots, targets, windowMinutes, now)
	if baselineAggs == nil {
		baselineAggs = map[string]*dxPulseBaselineAccumulator{}
	}

	bandSet := make(map[string]struct{})
	for _, band := range dxPulseDefaultVisibleBands {
		bandSet[band] = struct{}{}
	}
	for key := range currentAggs {
		bandSet[dxPulseBandFromKey(key)] = struct{}{}
	}
	for key := range baselineAggs {
		bandSet[dxPulseBandFromKey(key)] = struct{}{}
	}
	resp.Bands = dxPulseSortedBands(bandSet)
	if len(resp.Bands) == 0 {
		return resp
	}

	for _, band := range resp.Bands {
		row := make([]dxPulseMatrixCell, 0, len(dxPulseAllRegions))
		for _, region := range dxPulseAllRegions {
			key := dxPulseCellKey(band, string(region))
			current := currentAggs[key]
			baseline := baselineAggs[key]
			if mode == dxPulseModeAnomaly {
				row = append(row, buildDxPulseAnomalyCell(band, string(region), current, baseline, lookbackDays, baselineAvailable, now))
				continue
			}
			row = append(row, buildDxPulseQualityCell(band, string(region), current, now))
		}
		resp.Matrix = append(resp.Matrix, row)
	}

	return resp
}

func buildDxPulseSummary(matrix dxPulseMatrixResponse) dxPulseSummaryResponse {
	summary := dxPulseSummaryResponse{
		Target:            matrix.Target,
		Surroundings:      matrix.Surroundings,
		Mode:              matrix.Mode,
		ModeLabel:         matrix.ModeLabel,
		WindowMinutes:     matrix.WindowMinutes,
		GeneratedAt:       matrix.GeneratedAt,
		BaselineAvailable: matrix.BaselineAvailable,
		BestBands:         []dxPulseSummaryBand{},
		TopRegions:        []dxPulseSummaryRegion{},
		HotCells:          []dxPulseSummaryCell{},
	}

	type bandAgg struct {
		spots      int
		confidence float64
		best       *dxPulseMatrixCell
	}
	type regionAgg struct {
		spots    int
		bands    int
		best     *dxPulseMatrixCell
		seenBand map[string]struct{}
	}

	bandMap := make(map[string]*bandAgg)
	regionMap := make(map[string]*regionAgg)
	for i := range matrix.Matrix {
		for j := range matrix.Matrix[i] {
			cell := &matrix.Matrix[i][j]
			strength := dxPulseCellStrength(*cell)
			if cell.CurrentSpotCount > 0 {
				summary.HotCells = append(summary.HotCells, dxPulseSummaryCell{
					Band:             cell.Band,
					Region:           cell.Region,
					State:            cell.State,
					Label:            cell.Label,
					CurrentSpotCount: cell.CurrentSpotCount,
					Confidence:       cell.Confidence,
					Strength:         round2(strength),
				})
			}

			ba := bandMap[cell.Band]
			if ba == nil {
				ba = &bandAgg{}
				bandMap[cell.Band] = ba
			}
			ba.spots += cell.CurrentSpotCount
			if strength > ba.confidence || ba.best == nil {
				ba.confidence = strength
				ba.best = cell
			}

			ra := regionMap[cell.Region]
			if ra == nil {
				ra = &regionAgg{seenBand: make(map[string]struct{})}
				regionMap[cell.Region] = ra
			}
			ra.spots += cell.CurrentSpotCount
			if cell.CurrentSpotCount > 0 {
				ra.seenBand[cell.Band] = struct{}{}
			}
			if ra.best == nil || strength > dxPulseCellStrength(*ra.best) {
				ra.best = cell
			}
		}
	}

	for band, agg := range bandMap {
		if agg.best == nil || agg.spots == 0 {
			continue
		}
		summary.BestBands = append(summary.BestBands, dxPulseSummaryBand{
			Band:             band,
			State:            agg.best.State,
			Label:            agg.best.Label,
			CurrentSpotCount: agg.spots,
			Confidence:       round2(agg.best.Confidence),
		})
	}
	sort.Slice(summary.BestBands, func(i, j int) bool {
		if summary.BestBands[i].CurrentSpotCount == summary.BestBands[j].CurrentSpotCount {
			return summary.BestBands[i].Confidence > summary.BestBands[j].Confidence
		}
		return summary.BestBands[i].CurrentSpotCount > summary.BestBands[j].CurrentSpotCount
	})
	if len(summary.BestBands) > 5 {
		summary.BestBands = summary.BestBands[:5]
	}

	for region, agg := range regionMap {
		if agg.best == nil || agg.spots == 0 {
			continue
		}
		summary.TopRegions = append(summary.TopRegions, dxPulseSummaryRegion{
			Region:           region,
			TotalSpotCount:   agg.spots,
			ActiveBands:      len(agg.seenBand),
			BestBand:         agg.best.Band,
			BestBandState:    agg.best.State,
			BestBandLabel:    agg.best.Label,
			BestBandStrength: round2(dxPulseCellStrength(*agg.best)),
		})
	}
	sort.Slice(summary.TopRegions, func(i, j int) bool {
		if summary.TopRegions[i].TotalSpotCount == summary.TopRegions[j].TotalSpotCount {
			return summary.TopRegions[i].BestBandStrength > summary.TopRegions[j].BestBandStrength
		}
		return summary.TopRegions[i].TotalSpotCount > summary.TopRegions[j].TotalSpotCount
	})
	if len(summary.TopRegions) > 5 {
		summary.TopRegions = summary.TopRegions[:5]
	}

	sort.Slice(summary.HotCells, func(i, j int) bool {
		if summary.HotCells[i].Strength == summary.HotCells[j].Strength {
			return summary.HotCells[i].CurrentSpotCount > summary.HotCells[j].CurrentSpotCount
		}
		return summary.HotCells[i].Strength > summary.HotCells[j].Strength
	})
	if len(summary.HotCells) > 8 {
		summary.HotCells = summary.HotCells[:8]
	}

	return summary
}

func dxPulseCellStrength(cell dxPulseMatrixCell) float64 {
	strength := float64(cell.CurrentSpotCount)*0.6 + float64(cell.CurrentUniquePaths)*0.4
	if cell.BaselineRatio > 0 {
		strength += cell.BaselineRatio * 4
	}
	strength += cell.Confidence * 10
	return strength
}

func expandDxPulseTargets(target string, surroundings bool) []string {
	target = strings.ToUpper(strings.TrimSpace(target))
	if len(target) >= 4 {
		target = target[:4]
	}
	if !surroundings {
		return []string{target}
	}
	return getSurroundingSquares(target)
}

func normalizeDxPulseMode(raw string) string {
	mode := strings.ToLower(strings.TrimSpace(raw))
	switch mode {
	case "", dxPulseModeQuality, "absolute":
		return dxPulseModeQuality
	case dxPulseModeAnomaly, "relative":
		return dxPulseModeAnomaly
	default:
		return dxPulseModeQuality
	}
}

func dxPulseModeLabel(mode string) string {
	if normalizeDxPulseMode(mode) == dxPulseModeAnomaly {
		return "Anomaly"
	}
	return "Quality"
}

func dxPulseRegionStrings() []string {
	out := make([]string, len(dxPulseAllRegions))
	for i, r := range dxPulseAllRegions {
		out[i] = string(r)
	}
	return out
}

func aggregateDxPulseCurrent(spots []MQTTMessage, targets []string, windowMinutes int, now int64) map[string]*dxPulseCellAccumulator {
	cutoff := now - int64(windowMinutes*60)
	out := make(map[string]*dxPulseCellAccumulator)
	for _, m := range spots {
		if m.T < cutoff || m.T > now {
			continue
		}
		obs, ok := dxPulseObserve(m, targets)
		if !ok {
			continue
		}
		key := dxPulseCellKey(obs.band, string(obs.region))
		acc := out[key]
		if acc == nil {
			acc = &dxPulseCellAccumulator{
				uniquePaths:       make(map[string]struct{}),
				uniqueRemoteGrids: make(map[string]struct{}),
				snrs:              make([]int, 0, 8),
			}
			out[key] = acc
		}
		acc.spotCount++
		acc.uniquePaths[obs.pathKey] = struct{}{}
		if obs.remote4 != "" {
			acc.uniqueRemoteGrids[obs.remote4] = struct{}{}
		}
		acc.sumSNR += obs.snr
		acc.snrs = append(acc.snrs, obs.snr)
		if obs.timestamp > acc.lastSeen {
			acc.lastSeen = obs.timestamp
		}
	}
	return out
}

func aggregateDxPulseBaseline(spots []MQTTMessage, targets []string, windowMinutes int, now int64) map[string]*dxPulseBaselineAccumulator {
	out := make(map[string]*dxPulseBaselineAccumulator)
	for _, m := range spots {
		if !dxPulseMatchesWindowOfDay(m.T, now, windowMinutes) {
			continue
		}
		obs, ok := dxPulseObserve(m, targets)
		if !ok {
			continue
		}
		key := dxPulseCellKey(obs.band, string(obs.region))
		acc := out[key]
		if acc == nil {
			acc = &dxPulseBaselineAccumulator{
				activeDays:  make(map[int64]struct{}),
				uniquePaths: make(map[string]struct{}),
			}
			out[key] = acc
		}
		acc.totalCount++
		acc.uniquePaths[obs.pathKey] = struct{}{}
		acc.activeDays[utcDayIndex(obs.timestamp)] = struct{}{}
		acc.activeDayCount = len(acc.activeDays)
	}
	return out
}

func dxPulseObserve(m MQTTMessage, targets []string) (dxPulseObservation, bool) {
	band := normalizeBand(m.B)
	if band == "" {
		return dxPulseObservation{}, false
	}
	sc := strings.ToUpper(strings.TrimSpace(m.SC))
	rc := strings.ToUpper(strings.TrimSpace(m.RC))
	sl := strings.ToUpper(strings.TrimSpace(m.SL))
	rl := strings.ToUpper(strings.TrimSpace(m.RL))
	if sl == "" || rl == "" {
		return dxPulseObservation{}, false
	}

	senderMatch := dxPulseLocatorMatchesTargets(sl, targets)
	receiverMatch := dxPulseLocatorMatchesTargets(rl, targets)
	if !senderMatch && !receiverMatch {
		return dxPulseObservation{}, false
	}

	remote := rl
	if !senderMatch && receiverMatch {
		remote = sl
	}
	region := dxPulseRegionForLocator(remote)
	if region == dxPulseRegionUnknown {
		return dxPulseObservation{}, false
	}
	remote4 := normalizeSource4(remote)
	if remote4 == unknownSource4 {
		remote4 = ""
	}

	return dxPulseObservation{
		band:      band,
		region:    region,
		pathKey:   sc + "|" + rc + "|" + remote4,
		remote4:   remote4,
		snr:       m.RP,
		timestamp: m.T,
	}, true
}

func dxPulseLocatorMatchesTargets(locator string, targets []string) bool {
	if !isLocator(locator) {
		return false
	}
	prefix := strings.ToUpper(strings.TrimSpace(locator[:4]))
	for _, target := range targets {
		if prefix == strings.ToUpper(strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

func buildDxPulseQualityCell(band, region string, current *dxPulseCellAccumulator, now int64) dxPulseMatrixCell {
	cell := dxPulseMatrixCell{
		Band:        band,
		Region:      region,
		State:       "none",
		Label:       "No propagation",
		ColorBucket: "none",
		Confidence:  0,
	}
	if current == nil || current.spotCount == 0 {
		return cell
	}

	count := current.spotCount
	uniquePaths := len(current.uniquePaths)
	uniqueGrids := len(current.uniqueRemoteGrids)
	avgSnr := float64(current.sumSNR) / float64(count)
	medianSnr := medianInts(current.snrs)
	state, label := dxPulseQualityState(count, uniquePaths, uniqueGrids, medianSnr)

	cell.State = state
	cell.Label = label
	cell.ColorBucket = state
	cell.CurrentSpotCount = count
	cell.CurrentUniquePaths = uniquePaths
	cell.CurrentUniqueGrids = uniqueGrids
	cell.AvgSNR = round1(avgSnr)
	cell.MedianSNR = round1(medianSnr)
	cell.LastSeenAgeSeconds = maxInt64(0, now-current.lastSeen)
	cell.Confidence = round2(dxPulseQualityConfidence(count, uniquePaths, uniqueGrids))
	return cell
}

func buildDxPulseAnomalyCell(band, region string, current *dxPulseCellAccumulator, baseline *dxPulseBaselineAccumulator, lookbackDays int, baselineAvailable bool, now int64) dxPulseMatrixCell {
	cell := dxPulseMatrixCell{
		Band:        band,
		Region:      region,
		State:       "none",
		Label:       "No propagation",
		ColorBucket: "black",
	}
	if current != nil {
		cell.CurrentSpotCount = current.spotCount
		cell.CurrentUniquePaths = len(current.uniquePaths)
		cell.CurrentUniqueGrids = len(current.uniqueRemoteGrids)
		if current.spotCount > 0 {
			cell.AvgSNR = round1(float64(current.sumSNR) / float64(current.spotCount))
			cell.MedianSNR = round1(medianInts(current.snrs))
			cell.LastSeenAgeSeconds = maxInt64(0, now-current.lastSeen)
		}
	}

	currentCount := 0
	if current != nil {
		currentCount = current.spotCount
	}
	if currentCount == 0 {
		cell.Confidence = 0
		return cell
	}
	if !baselineAvailable || baseline == nil {
		cell.State = "insufficient_baseline"
		cell.Label = "Baseline sparse"
		cell.ColorBucket = "grey"
		cell.Confidence = round2(dxPulseQualityConfidence(currentCount, cell.CurrentUniquePaths, cell.CurrentUniqueGrids) * 0.4)
		return cell
	}

	cell.BaselineSupport = baseline.totalCount
	cell.BaselineSupportDays = dxPulseBaselineDayCount(baseline)
	expected := 0.0
	if lookbackDays > 0 {
		expected = float64(baseline.totalCount) / float64(lookbackDays)
	}
	cell.BaselineExpectedSpots = round2(expected)
	if baseline.totalCount < dxPulseMinBaselineSupport || expected <= 0 {
		cell.State = "insufficient_baseline"
		cell.Label = "Baseline sparse"
		cell.ColorBucket = "grey"
		cell.Confidence = round2(dxPulseAnomalyConfidence(currentCount, baseline.totalCount, dxPulseBaselineDayCount(baseline)) * 0.5)
		return cell
	}

	smoothedExpected := expected
	if smoothedExpected < 0.5 {
		smoothedExpected = 0.5
	}
	ratio := float64(currentCount) / smoothedExpected
	cell.BaselineRatio = round2(ratio)
	cell.Confidence = round2(dxPulseAnomalyConfidence(currentCount, baseline.totalCount, dxPulseBaselineDayCount(baseline)))

	switch {
	case ratio < 0.5:
		cell.State = "far_below"
		cell.Label = "Far below normal"
		cell.ColorBucket = "dark_blue"
	case ratio < 0.8:
		cell.State = "below"
		cell.Label = "Below normal"
		cell.ColorBucket = "light_blue"
	case ratio <= 1.25:
		cell.State = "normal"
		cell.Label = "Near normal"
		cell.ColorBucket = "green"
	case ratio <= 2.0:
		cell.State = "above"
		cell.Label = "Above normal"
		cell.ColorBucket = "orange"
	default:
		cell.State = "far_above"
		cell.Label = "Far above normal"
		cell.ColorBucket = "red"
	}
	return cell
}

func dxPulseQualityState(count, uniquePaths, uniqueGrids int, medianSnr float64) (string, string) {
	switch {
	case count == 0:
		return "none", "No propagation"
	case count >= 12 && uniquePaths >= 5 && uniqueGrids >= 4 && medianSnr >= -12:
		return "excellent", "Excellent"
	case count >= 6 && uniquePaths >= 3 && uniqueGrids >= 2 && medianSnr >= -18:
		return "good", "Good"
	case count >= 3 && uniquePaths >= 2:
		return "fair", "Fair"
	default:
		return "poor", "Poor"
	}
}

func dxPulseQualityConfidence(count, uniquePaths, uniqueGrids int) float64 {
	conf := 0.0
	conf += clamp01(float64(count)/12.0) * 0.4
	conf += clamp01(float64(uniquePaths)/5.0) * 0.35
	conf += clamp01(float64(uniqueGrids)/4.0) * 0.25
	return clamp01(conf)
}

func dxPulseAnomalyConfidence(currentCount, baselineTotal, baselineDays int) float64 {
	conf := 0.0
	conf += clamp01(float64(currentCount)/8.0) * 0.3
	conf += clamp01(float64(baselineTotal)/40.0) * 0.4
	conf += clamp01(float64(baselineDays)/10.0) * 0.3
	return clamp01(conf)
}

func dxPulseMatchesWindowOfDay(ts, now int64, windowMinutes int) bool {
	if windowMinutes <= 0 {
		return false
	}
	const secPerDay = int64(24 * 60 * 60)
	nowSOD := ((now % secPerDay) + secPerDay) % secPerDay
	startSOD := nowSOD - int64(windowMinutes*60)
	tsSOD := ((ts % secPerDay) + secPerDay) % secPerDay
	if startSOD >= 0 {
		return tsSOD >= startSOD && tsSOD <= nowSOD
	}
	return tsSOD >= secPerDay+startSOD || tsSOD <= nowSOD
}

func dxPulseWindowSlots(now int64, windowMinutes int) []int32 {
	if windowMinutes <= 0 {
		return []int32{int32(utcSlotOfDay(now))}
	}
	const slotSeconds = int64(30 * 60)
	const daySeconds = int64(24 * 60 * 60)
	start := now - int64(windowMinutes*60)
	end := now
	seen := make(map[int32]struct{}, 4)
	out := make([]int32, 0, 4)
	for ts := start; ts <= end; {
		mod := ((ts % daySeconds) + daySeconds) % daySeconds
		slot := int32(mod / slotSeconds)
		if _, ok := seen[slot]; !ok {
			seen[slot] = struct{}{}
			out = append(out, slot)
		}
		nextBoundary := ts - mod + (int64(slot)+1)*slotSeconds
		if nextBoundary <= ts {
			nextBoundary = ts + 1
		}
		ts = nextBoundary
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func dxPulseCellKey(band, region string) string {
	return band + "|" + region
}

func dxPulseBandFromKey(key string) string {
	if i := strings.IndexByte(key, '|'); i > 0 {
		return key[:i]
	}
	return key
}

func dxPulseSortedBands(bands map[string]struct{}) []string {
	out := make([]string, 0, len(bands))
	for band := range bands {
		if band == "" {
			continue
		}
		out = append(out, band)
	}
	sort.Slice(out, func(i, j int) bool {
		ri := dxPulseBandRank(out[i])
		rj := dxPulseBandRank(out[j])
		if ri == rj {
			return out[i] < out[j]
		}
		return ri < rj
	})
	return out
}

func dxPulseBandRank(band string) int {
	for i, known := range dxPulseBandOrder {
		if known == band {
			return i
		}
	}
	return len(dxPulseBandOrder) + 100
}

func dxPulseRegionForLocator(loc string) dxPulseRegion {
	if !isLocator(loc) {
		return dxPulseRegionUnknown
	}
	lat, lng := locatorToLatLng(loc)
	return dxPulseRegionForLatLng(lat, lng)
}

func dxPulseRegionForLatLng(lat, lng float64) dxPulseRegion {
	if lat <= -60 {
		return dxPulseRegionAN
	}
	if lat >= 30 && lat <= 46 && lng >= 128 && lng <= 146 {
		return dxPulseRegionJA
	}
	if lat >= 18 && lat <= 29 && lng >= -161 && lng <= -154 {
		return dxPulseRegionKH6
	}
	if lat >= 10 && lat <= 25 && lng >= -85 && lng <= -60 {
		return dxPulseRegionCAR
	}
	if lat >= -50 && lat <= -10 && lng >= 110 && lng <= 180 {
		return dxPulseRegionVK
	}
	if lat >= 35 && lat <= 72 && lng >= -15 && lng <= 45 {
		return dxPulseRegionEU
	}
	if lat >= -40 && lat <= 37 && lng >= -20 && lng <= 55 {
		return dxPulseRegionAF
	}
	if lat >= 15 && lat <= 84 && lng >= -170 && lng <= -50 {
		return dxPulseRegionNA
	}
	if lat >= -60 && lat < 15 && lng >= -90 && lng <= -30 {
		return dxPulseRegionSA
	}
	if lat >= 0 && lat <= 78 && lng >= 40 && lng <= 180 {
		return dxPulseRegionAS
	}
	if lat >= -50 && lat <= 30 && (lng >= 130 || lng <= -130) {
		return dxPulseRegionOC
	}
	return dxPulseRegionUnknown
}

func medianInts(values []int) float64 {
	if len(values) == 0 {
		return 0
	}
	cp := append([]int(nil), values...)
	sort.Ints(cp)
	mid := len(cp) / 2
	if len(cp)%2 == 1 {
		return float64(cp[mid])
	}
	return float64(cp[mid-1]+cp[mid]) / 2.0
}

func utcDayIndex(ts int64) int64 {
	const secPerDay = int64(24 * 60 * 60)
	if ts >= 0 {
		return ts / secPerDay
	}
	return (ts - (secPerDay - 1)) / secPerDay
}

func dxPulseBaselineDayCount(b *dxPulseBaselineAccumulator) int {
	if b == nil {
		return 0
	}
	if b.activeDayCount > 0 {
		return b.activeDayCount
	}
	return len(b.activeDays)
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
