package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"horstreporter/internal/region"
)

// prop_intel.go implements the WSPR Propagation Intelligence engine: a per
// (band × region) nowcast of SSB/CW openness, rising slope, and atypical
// surge detection against a WSPR climatology. It supersedes the old FT8
// nowcast. The engine is stateless beyond the package-level dxBaseline
// singleton (for the FT8 cross-reference), wsprClimatology singleton (for
// the WSPR climatology z-score), and the hub.history ring.

// Tunable thresholds for the WSPR propagation-intelligence engine.
const (
	// propIntelNowcastWindowMin is the default nowcast window in minutes.
	propIntelNowcastWindowMin = 15
	// propIntelAtypicalZThreshold is the default z-score above which a cell
	// is flagged atypical. Operator-configurable via the surge_threshold
	// query parameter on /api/prop_intel.
	propIntelAtypicalZThreshold = 2.0
	// propIntelRisingSlopeThreshold is the ratio of second-half to first-half
	// rate above which a cell is flagged rising (e.g. 1.5 = 50% increase).
	propIntelRisingSlopeThreshold = 1.5
	// propIntelMinSampleDays is the minimum WSPR climatology sample-days
	// for the atypical z-score to be computed. Below this, no atypical flag.
	propIntelMinSampleDays = 3
	// propIntelMatureSampleDays is the sample-days at which confidence
	// reaches 1.0. Below this, confidence is discounted proportionally.
	propIntelMatureSampleDays = 30
	// propIntelRegionBaselineDaysBack is the lookback for regionCalendarStats.
	propIntelRegionBaselineDaysBack = 30
	// propIntelRegionBaselineCacheTTL is how long the climatology is cached.
	propIntelRegionBaselineCacheTTL int64 = 120
	// propIntelRegionBaselineNegCacheTTL bounds how long a failed query is
	// remembered before retrying Postgres.
	propIntelRegionBaselineNegCacheTTL int64 = 30
	// propIntelRegionBaselineQueryTimeout is the per-query deadline.
	propIntelRegionBaselineQueryTimeout = 1500 * time.Millisecond
	// propIntelHistoryPoolCap is the initial capacity of pooled scratch slices.
	propIntelHistoryPoolCap = 1 << 14
	// SSB/CW budget model constants (KTD8).
	// propIntelSSBFloorDb: SSB requires roughly +10 dB SNR/2500 Hz at 100W
	// reference power to be comfortably copied.
	propIntelSSBFloorDb = 10.0
	// propIntelCWFloorDb: CW can be copied below noise with a narrow filter
	// and a skilled ear; roughly -5 dB SNR/2500 Hz at 100W reference.
	propIntelCWFloorDb = -5.0
	// propIntelReferencePowerW is the assumed SSB/CW transmitter power for
	// the budget model. WSPR beacons run at much lower power (0.1–100W),
	// so the effective SNR for a 100W signal is:
	//   effective_snr = wspr_snr + (wspr_power_dbm - reference_power_dbm)
	// where reference_power_dbm = 10*log10(100W/1mW) = 50 dBm.
	propIntelReferencePowerW = 100.0
	propIntelReferencePowerDbm = 50.0 // 10*log10(100W / 1mW)
)

// propIntelResponse is the JSON envelope returned by /api/prop_intel.
type propIntelResponse struct {
	QTH      string          `json:"qth"`
	Minutes  int             `json:"minutes"`
	Now      int64           `json:"now"`
	FromHere bool            `json:"from_here"`
	Bands    []string        `json:"bands"`
	Regions  []string        `json:"regions"`
	Cells    []propIntelCell `json:"cells"`
}

// propIntelCell is one (band × region) row in the WSPR nowcast. Each cell
// carries independent signals: SSB/CW openness (from SNR+Power budget), a
// rising slope flag, and an atypical z-score against the WSPR climatology
// with a three-flavor FT8 cross-reference label.
type propIntelCell struct {
	Band   string `json:"band"`
	Region string `json:"region"`
	// SSBOpen / CWOpen: whether any WSPR path in this cell has enough
	// budget (SNR + TX power) to be audible on SSB / CW. Computed from the
	// best path in the cell.
	SSBOpen bool `json:"ssb_open"`
	CWOpen  bool `json:"cw_open"`
	// Rising: the WSPR path rate has a positive slope over the recent
	// sub-window (band is opening).
	Rising bool `json:"rising"`
	// Atypical: non-nil when the WSPR path rate z-scores above the WSPR
	// climatology mean for this (band × region × slot). Carries the
	// z-score, a confidence value (discounted during cold-start), and a
	// flavor label from the FT8 cross-reference.
	Atypical *AtypicalInfo `json:"atypical,omitempty"`
	// FromHere: true when the operator's QTH is one end of any path in
	// this cell.
	FromHere bool `json:"from_here"`
	// Sources: which ingest sources contributed to this cell (usually just
	// ["wspr"] in the WSPR-primary engine).
	Sources []string `json:"sources"`
	// SpotCount is the number of WSPR spots in the window for this cell.
	SpotCount int `json:"spot_count"`
}

// AtypicalInfo carries the atypical-surge detection result.
type AtypicalInfo struct {
	ZScore     float64 `json:"z_score"`
	Confidence float64 `json:"confidence"`
	Flavor     string  `json:"flavor"`
	// FT8CrossRef notes whether the FT8 cross-reference was available.
	// "available", "unavailable", or "" (not atypical).
	FT8CrossRef string `json:"ft8_cross_ref,omitempty"`
}

// propIntelEngine is the stateless evaluator. It holds no mutable state of
// its own; the dxBaseline singleton (for the FT8 cross-reference), the
// wsprClimatology singleton (for the WSPR climatology z-score), and hub.history
// are read per-request by the handler.
type propIntelEngine struct {
	// baseline is the DxBaselineEngine used for accessing the Postgres store
	// (FT8 regionCalendarStats for the atypical flavor cross-reference, U4).
	baseline *DxBaselineEngine

	// FT8 climatology cache for the atypical flavor cross-reference (U4).
	// Shared across requests; the map is read-only after swap.
	ft8CacheMu    sync.RWMutex
	ft8Cache      map[regionBaselineKey]regionCalendarStatRow
	ft8CacheAt    int64
	ft8CacheErrAt int64
}

// propIntel is the package-level singleton, mirroring dxBaseline. It is wired
// in main.go alongside dxBaseline so the handler can call it without nil
// checks (the handler still guards for safety).
var propIntel = &propIntelEngine{}

// propIntelHistoryPool reuses scratch slices for the per-request hub.history
// copy (#4): a 1.8M-entry window is ~360MB, and allocating (and GC-ing) that
// on every request amplifies the cost of the evaluation passes. Buffers grow
// to fit and are returned after Evaluate, which does not retain the slice.
var propIntelHistoryPool = sync.Pool{
	New: func() interface{} {
		b := make([]MQTTMessage, 0, propIntelHistoryPoolCap)
		return &b
	},
}

// propIntelCellKey indexes a (band × region) accumulator.
type propIntelCellKey struct {
	band   string
	region string
}

// propIntelCellAcc accumulates per-cell evidence during a window scan.
type propIntelCellAcc struct {
	// uniqueSenders deduplicates across the remote callsigns.
	uniqueSenders map[string]struct{}
	// sources records which ingest tags contributed.
	sources map[string]struct{}
	// bestBudgetSNR tracks the highest effective SNR (adjusted for TX power)
	// seen in this cell, for SSB/CW viability flags.
	bestBudgetSNR float64
	// hasPower tracks whether any spot in the cell had a nonzero TXPower
	// (if none, SSB/CW flags can't be computed from this cell).
	hasPower bool
	// fromHere tracks whether the operator's QTH is one end of any path.
	fromHere bool
	// spotCount is the total number of WSPR spots in this cell.
	spotCount int
	// firstHalfSpots / secondHalfSpots count spots in the first and second
	// halves of the nowcast window, for the rising slope computation.
	firstHalfSpots  int
	secondHalfSpots int
}

// Evaluate computes the per-(band × region) WSPR nowcast for the given QTH and
// history window. The engine:
//  1. Resolves the QTH to a qthSet.
//  2. Scans WSPR spots only, resolving each spot's remote end to a region.
//  3. Computes SSB/CW openness from the best path budget (SNR + TX power).
//  4. Computes the rising slope from the first/second half spot counts.
//  5. Computes the atypical z-score against the WSPR climatology.
//  6. Tags from-here cells where the operator's QTH is one end.
func (e *propIntelEngine) Evaluate(qth string, surroundings bool, minutes int, cwMinDb int, history []MQTTMessage, now int64, atypicalThreshold float64) propIntelResponse {
	qth = normalizeQTHToken(qth)
	if minutes <= 0 {
		minutes = propIntelNowcastWindowMin
	}
	if minutes > maxDxWindowMinutes {
		minutes = maxDxWindowMinutes
	}

	resp := propIntelResponse{
		QTH:     qth,
		Minutes: minutes,
		Now:     now,
		Bands:   []string{},
		Regions: allRegionStrings(),
		Cells:   []propIntelCell{},
	}

	if qth == "" {
		return resp
	}

	qthSet := []string{qth}
	if surroundings && isLocator(qth) {
		qthSet = getSurroundingSquares(qth)
	}

	cutoff := now - int64(minutes)*60
	midpoint := cutoff + (now-cutoff)/2

	acc := make(map[propIntelCellKey]*propIntelCellAcc)
	bandsSeen := make(map[string]struct{})

	for _, m := range history {
		if m.T > now || m.T < cutoff {
			continue
		}
		// WSPR spots only — the old FT8 nowcast is retired (R12).
		if m.Source != "wspr" {
			continue
		}
		band := normalizeBand(m.B)
		if band == "" || !bandInScope(band) {
			continue
		}
		// Resolve the remote end for WSPR: SC/SL = receiver, RC/RL =
		// transmitter (opposite of FT8 convention). If the operator's QTH
		// matches one end, the remote is the other end. If neither end
		// matches (global mesh view), use the receiver locator (SL) as the
		// region key — "where the path landed".
		sl := strings.ToUpper(strings.TrimSpace(m.SL))
		rl := strings.ToUpper(strings.TrimSpace(m.RL))
		sc := strings.ToUpper(strings.TrimSpace(m.SC))
		rc := strings.ToUpper(strings.TrimSpace(m.RC))

		var remoteLocator, remoteCall string
		matchedEnd := false
		for _, t := range qthSet {
			if matchCall(sc, t) || (isLocator(t) && sl != "" && strings.HasPrefix(sl, t)) {
				// Operator is the receiver → remote is the transmitter.
				remoteLocator = rl
				remoteCall = rc
				matchedEnd = true
				break
			}
			if matchCall(rc, t) || (isLocator(t) && rl != "" && strings.HasPrefix(rl, t)) {
				// Operator is the transmitter → remote is the receiver.
				remoteLocator = sl
				remoteCall = sc
				matchedEnd = true
				break
			}
		}
		if !matchedEnd {
			// Global mesh view: neither end matches QTH. Use the receiver
			// locator as the region key.
			remoteLocator = sl
			remoteCall = sc
		}
		if remoteLocator == "" || !isLocator(remoteLocator) {
			continue
		}
		reg := region.FromLocator(remoteLocator)
		if reg == region.Unknown {
			continue
		}
		key := propIntelCellKey{band: band, region: string(reg)}

		cell := acc[key]
		if cell == nil {
			cell = &propIntelCellAcc{
				uniqueSenders: make(map[string]struct{}),
				sources:       make(map[string]struct{}),
			}
			acc[key] = cell
		}
		if remoteCall != "" {
			cell.uniqueSenders[remoteCall] = struct{}{}
		}
		cell.sources["wspr"] = struct{}{}
		cell.spotCount++
		bandsSeen[band] = struct{}{}

		// SSB/CW budget: effective_snr = wspr_snr + (ref_power_dbm - tx_power_dbm)
		// TXPower is in dBm (wspr.live convention). The reference is 100W = 50 dBm.
		// A 20W beacon (43 dBm) at SNR +5 dB has effective SNR = 5 + (50-43) = +12 dB
		// (the path would carry a 100W signal 7 dB stronger than the beacon).
		if m.TXPower > 0 {
			cell.hasPower = true
			effectiveSNR := float64(m.RP) + propIntelReferencePowerDbm - float64(m.TXPower)
			if effectiveSNR > cell.bestBudgetSNR {
				cell.bestBudgetSNR = effectiveSNR
			}
		}

		// From-here: check if the operator's QTH is one end of this path.
		if !cell.fromHere && matchedEnd {
			cell.fromHere = true
		}

		// Rising slope: first vs second half of the window.
		if m.T < midpoint {
			cell.firstHalfSpots++
		} else {
			cell.secondHalfSpots++
		}
	}

	resp.Bands = inScopeBandsOrdered(bandsSeen)
	slot := utcSlotOfDay(now)

	// Load the WSPR climatology for the atypical z-score.
	var wsprClim map[regionBaselineKey]wsprRegionCalendarStatRow
	if wsprClimatology != nil {
		ctx, cancel := context.WithTimeout(context.Background(), propIntelRegionBaselineQueryTimeout)
		rows := wsprClimatology.RegionCalendarStats(ctx, propIntelRegionBaselineDaysBack, now)
		cancel()
		wsprClim = make(map[regionBaselineKey]wsprRegionCalendarStatRow, len(rows))
		for _, r := range rows {
			wsprClim[regionBaselineKey{r.Band, r.Region, r.SlotOfDay}] = r
		}
	}

	// Load the FT8 climatology for the atypical flavor cross-reference (U4).
	ft8Clim := e.loadFT8Baselines(now)

	cells := make([]propIntelCell, 0, len(acc))
	// Use cwMinDb from the query param when valid; else use the default.
	cwFloor := float64(propIntelCWFloorDb)
	if cwMinDb >= -40 && cwMinDb <= 20 {
		cwFloor = float64(cwMinDb)
	}
	for key, cell := range acc {
		// SSB/CW flags from the best path budget.
		ssbOpen := cell.hasPower && cell.bestBudgetSNR >= propIntelSSBFloorDb
		cwOpen := cell.hasPower && cell.bestBudgetSNR >= cwFloor

		// Rising slope: second half rate > first half rate * threshold.
		rising := false
		if cell.firstHalfSpots > 0 {
			ratio := float64(cell.secondHalfSpots) / float64(cell.firstHalfSpots)
			rising = ratio >= propIntelRisingSlopeThreshold
		} else if cell.secondHalfSpots > 0 {
			// No first-half spots but second-half spots → definitely rising.
			rising = true
		}

		// Atypical z-score against the WSPR climatology.
		// The climatology Mean is the average per-day count for this (band,
		// region, slot). Scale the live count to a per-slot extrapolation
		// (spotCount * 30/minutes) so the z-score compares like-for-like.
		var atypical *AtypicalInfo
		if base, ok := wsprClim[regionBaselineKey{key.band, key.region, slot}]; ok {
			if base.StdDev > 0 && base.SampleDays >= propIntelMinSampleDays {
				liveRate := float64(cell.spotCount) * (30.0 / float64(minutes))
				z := (liveRate - base.Mean) / base.StdDev
				if z >= atypicalThreshold {
					conf := atypicalConfidence(base.SampleDays)
					flavor, ft8Ref := assignFlavor(key.band, key.region, slot, ft8Clim)
					atypical = &AtypicalInfo{
						ZScore:      round3(z),
						Confidence:  round3(conf),
						Flavor:      flavor,
						FT8CrossRef: ft8Ref,
					}
				}
			}
		}

		cells = append(cells, propIntelCell{
			Band:     key.band,
			Region:   key.region,
			SSBOpen:  ssbOpen,
			CWOpen:   cwOpen,
			Rising:   rising,
			Atypical: atypical,
			FromHere: cell.fromHere,
			Sources:  sortedSources(cell.sources),
			SpotCount: cell.spotCount,
		})
	}

	sort.Slice(cells, func(i, j int) bool {
		if cells[i].Band != cells[j].Band {
			return cells[i].Band < cells[j].Band
		}
		return cells[i].Region < cells[j].Region
	})

	resp.Cells = cells
	return resp
}

// atypicalConfidence computes the confidence value from the WSPR climatology
// sample depth. Scales from 0.3 at 1 day to 1.0 at propIntelMatureSampleDays.
func atypicalConfidence(sampleDays int) float64 {
	if sampleDays >= propIntelMatureSampleDays {
		return 1.0
	}
	if sampleDays <= 0 {
		return 0.3
	}
	return 0.3 + 0.7*float64(sampleDays)/float64(propIntelMatureSampleDays)
}

// assignFlavor reads the FT8 climatology for the same (band, region, slot) and
// assigns one of three atypical flavors per R8. When the FT8 climatology is
// unavailable, returns "atypical-wspr-only" with ft8Ref "unavailable" (R9).
func assignFlavor(band, regionCode string, slot int, ft8Clim map[regionBaselineKey]regionCalendarStatRow) (flavor, ft8Ref string) {
	base, ok := ft8Clim[regionBaselineKey{band, regionCode, slot}]
	if !ok || base.SampleDays < propIntelMinSampleDays || base.StdDev <= 0 {
		return "atypical-wspr-only", "unavailable"
	}
	// FT8 is "atypical" if its today count z-scores above the FT8 climatology.
	if base.Today > 0 && base.StdDev > 0 {
		ft8Z := (float64(base.Today) - base.Mean) / base.StdDev
		if ft8Z >= propIntelAtypicalZThreshold {
			return "atypical-both", "available"
		}
	}
	// FT8 effectively absent (no activity today or far below typical).
	if base.Today == 0 || (base.Mean > 0 && float64(base.Today) < base.Mean*0.1) {
		return "atypical-wspr-silent-ft8", "available"
	}
	return "atypical-wspr-only", "available"
}

// propIntelRegionDisplayNames maps the 11-region codes to the display names
func sortedSources(s map[string]struct{}) []string {
	if len(s) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// allRegionStrings returns the 11-region list as strings.
func allRegionStrings() []string {
	out := make([]string, 0, len(region.AllRegions()))
	for _, r := range region.AllRegions() {
		out = append(out, string(r))
	}
	return out
}

// inScopeBandsOrdered returns the in-scope bands that appear in `seen`, in the
// canonical HF→VHF order (160m…2m), so the response band list is stable.
func inScopeBandsOrdered(seen map[string]struct{}) []string {
	order := []string{"160m", "80m", "60m", "40m", "30m", "20m", "17m", "15m", "12m", "10m", "6m", "4m", "2m"}
	out := make([]string, 0, len(seen))
	for _, b := range order {
		if _, ok := seen[b]; ok {
			out = append(out, b)
		}
	}
	return out
}

// mean returns the arithmetic mean of a float64 slice.
func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func round3(v float64) float64 {
	return math.Round(v*1000) / 1000
}

// regionBaselineKey indexes regionCalendarStats rows by (band, region, slot).
type regionBaselineKey struct {
	band   string
	region string
	slot   int
}

// loadFT8Baselines fetches the FT8 per-(band × region × slot) climatology
// from Postgres for the atypical flavor cross-reference (U4). Returns an
// empty map when no store is configured.
func (e *propIntelEngine) loadFT8Baselines(now int64) map[regionBaselineKey]regionCalendarStatRow {
	if e == nil || e.baseline == nil {
		return nil
	}
	e.ft8CacheMu.RLock()
	if e.ft8Cache != nil && now-e.ft8CacheAt < propIntelRegionBaselineCacheTTL {
		cached := e.ft8Cache
		e.ft8CacheMu.RUnlock()
		return cached
	}
	if e.ft8Cache == nil && e.ft8CacheErrAt != 0 && now-e.ft8CacheErrAt < propIntelRegionBaselineNegCacheTTL {
		e.ft8CacheMu.RUnlock()
		return nil
	}
	e.ft8CacheMu.RUnlock()

	e.ft8CacheMu.Lock()
	defer e.ft8CacheMu.Unlock()
	if e.ft8Cache != nil && now-e.ft8CacheAt < propIntelRegionBaselineCacheTTL {
		return e.ft8Cache
	}
	if e.ft8Cache == nil && e.ft8CacheErrAt != 0 && now-e.ft8CacheErrAt < propIntelRegionBaselineNegCacheTTL {
		return nil
	}

	e.baseline.mu.RLock()
	st := e.baseline.store
	e.baseline.mu.RUnlock()
	if st == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), propIntelRegionBaselineQueryTimeout)
	defer cancel()
	rows, err := st.regionCalendarStats(ctx, propIntelRegionBaselineDaysBack, now)
	if err != nil {
		e.ft8CacheErrAt = now
		if e.ft8Cache != nil {
			return e.ft8Cache
		}
		return nil
	}
	out := make(map[regionBaselineKey]regionCalendarStatRow, len(rows))
	for _, r := range rows {
		out[regionBaselineKey{r.Band, r.Region, r.SlotOfDay}] = r
	}
	e.ft8Cache = out
	e.ft8CacheAt = now
	e.ft8CacheErrAt = 0
	return out
}

// propIntelRegionDisplayNames maps the 11-region codes to the display names
// used in surge labels (e.g., "tune to 10m, surge to Caribbean"). The codes
// follow region.AllRegions(); the names follow the operator-facing convention
// used in the existing WSPR matrix panel.
var propIntelRegionDisplayNames = map[string]string{
	"EU":  "Europe",
	"NA":  "North America",
	"SA":  "South America",
	"AF":  "Africa",
	"AS":  "Asia",
	"OC":  "Oceania",
	"AN":  "Antarctica",
	"JA":  "Japan",
	"VK":  "Australia",
	"KH6": "Hawaii",
	"CAR": "Caribbean",
}

// regionDisplayName returns the display name for a region code, falling back
// to the code itself if unknown.
func regionDisplayName(code string) string {
	if name, ok := propIntelRegionDisplayNames[code]; ok {
		return name
	}
	return code
}

// propIntelHandler is the HTTP handler for /api/prop_intel. It follows the
// dxConditionsHandler pattern: resolve QTH, parse minutes/cw_min_db/
// surroundings, snapshot hub.history, call the engine, JSON-encode.
// The `surge_threshold` query parameter overrides the default atypical
// z-score threshold. The `from_here` query parameter filters to cells where
// the operator's QTH is one end of any path.
func propIntelHandler(w http.ResponseWriter, r *http.Request) {
	propIntelAccounting.requests.Add(1)
	qth, surroundings := resolveQTHQuery(r)
	if qth == "" {
		propIntelAccounting.errors.Add(1)
		http.Error(w, "qth required", http.StatusBadRequest)
		return
	}

	minutes := propIntelNowcastWindowMin
	if raw := strings.TrimSpace(r.URL.Query().Get("minutes")); raw != "" {
		if m, err := strconv.Atoi(raw); err == nil && m > 0 {
			minutes = m
		}
	}
	if minutes > maxDxWindowMinutes {
		minutes = maxDxWindowMinutes
	}

	cwMinDb := defaultDxCwViableMinDb
	if raw := strings.TrimSpace(r.URL.Query().Get("cw_min_db")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil {
			cwMinDb = v
		}
	}

	atypicalThreshold := propIntelAtypicalZThreshold
	if raw := strings.TrimSpace(r.URL.Query().Get("surge_threshold")); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v > 0 {
			atypicalThreshold = v
		}
	}

	fromHere := false
	if raw := strings.TrimSpace(r.URL.Query().Get("from_here")); raw == "true" {
		fromHere = true
	}

	now := time.Now().Unix()
	nowcastCutoff := now - int64(minutes)*60
	hub.RLock()
	idx := sort.Search(len(hub.history), func(i int) bool {
		return hub.history[i].T >= nowcastCutoff
	})
	n := len(hub.history) - idx
	bufp := propIntelHistoryPool.Get().(*[]MQTTMessage)
	if cap(*bufp) < n {
		*bufp = make([]MQTTMessage, n)
	} else {
		*bufp = (*bufp)[:n]
	}
	historyCopy := (*bufp)[:n]
	copy(historyCopy, hub.history[idx:])
	hub.RUnlock()

	engine := propIntel
	resp := engine.Evaluate(qth, surroundings, minutes, cwMinDb, historyCopy, now, atypicalThreshold)
	resp.FromHere = fromHere

	// Filter cells to from-here when the param is set.
	if fromHere {
		filtered := make([]propIntelCell, 0, len(resp.Cells))
		for _, c := range resp.Cells {
			if c.FromHere {
				filtered = append(filtered, c)
			}
		}
		resp.Cells = filtered
	}

	// Count atypical detection events per request.
	hasAtypical := false
	for _, c := range resp.Cells {
		if c.Atypical != nil {
			propIntelAccounting.surgesDetected.Add(1)
			hasAtypical = true
			break
		}
	}

	// Web Push: fan out atypical cells to push subscriptions. Runs in a
	// goroutine so a slow push endpoint cannot block the response. The
	// resp.Cells slice is owned by this response, so the goroutine can
	// read it after the handler returns.
	if hasAtypical {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logInfo("push goroutine panic: %v", r)
				}
			}()
			pushStore.NotifySurges(resp.Cells, qth)
		}()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
	propIntelHistoryPool.Put(bufp)
}
