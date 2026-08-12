package main

import (
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// prop_intel.go implements the Propagation Intelligence engine: a per
// (band × region) nowcast + 1-hour forecast of P(open), expected spot count,
// and confidence. It reuses the existing DxBaseline.Evaluate sparkline for the
// forecast slope, the dxPulse 11-region classifier for the region axis, and
// the regionCalendarStats Postgres baseline when a store is configured.
//
// The engine is intentionally stateless beyond the package-level dxBaseline
// singleton and the hub.history ring: every request re-derives its cells from
// a snapshotted history window, so it stays consistent with dxConditionsHandler
// and hotBandsHandler.

// Tunable thresholds for the prop_intel confidence model. Confidence is a
// function of (a) unique-sender support — how many distinct stations backed
// the cell, and (b) source diversity — how many of the rbn/pskreporter/wspr
// feeds contributed. A single-source cell is discounted; a multi-source cell
// with thin support still caps below 1. These constants are calibrated so the
// sparse-WSPRnet case (2 senders, 1 source) lands below 0.3 and the dense
// multi-source case reaches the high band — see TestPropIntelConfidence.
const (
	// propIntelNowcastWindowMin is the default nowcast window in minutes.
	propIntelNowcastWindowMin = 15
	// propIntelNowcastPOpenMin is the 15-minute slot used in the Poisson
	// P(open) = 1 − e^(−λ) formula: λ = rate_per_hour × (slot/60).
	propIntelNowcastSlotMin = 15.0
	// propIntelForecastHorizonH is the forecast horizon in hours (1h ahead).
	propIntelForecastHorizonH = 1.0
	// propIntelMinSendersHighConf is the unique-sender count at/above which a
	// multi-source cell reaches the high confidence band.
	propIntelMinSendersHighConf = 10
	// propIntelMinSendersModConf is the unique-sender count at/above which a
	// single-source cell reaches moderate confidence.
	propIntelMinSendersModConf = 8
	// propIntelSingleSourceDiscount is multiplied into the confidence of a
	// cell backed by only one ingest source.
	propIntelSingleSourceDiscount = 0.6
	// propIntelForecastVolatilityDiscount is the per-unit-of-variance
	// discount applied to forecast confidence relative to nowcast confidence.
	propIntelForecastVolatilityDiscount = 0.02
	// propIntelRegionBaselineDaysBack is the lookback for regionCalendarStats.
	propIntelRegionBaselineDaysBack = 30

	// Surge-detection thresholds. Mirrors the hot_bands.go threshold-constant
	// pattern (lines 12–24). The surge detector runs after the nowcast cells are
	// built and computes a z-score per (band × region) cell against the
	// region-level baseline. See KTD3 and U2 in the plan.
	//
	// propIntelSurgeZThreshold is the default z-score above which a cell is
	// flagged as a surge. Operator-configurable via the `surge_threshold`
	// query parameter on /api/prop_intel. Per-band/region overrides are
	// deferred to v2.
	propIntelSurgeZThreshold = 2.0
	// propIntelSurgeMinSamples is the minimum baseline sample count for the
	// z-score to be statistically meaningful. Below this, the stddev is too
	// noisy (rare band/region combos) and surge detection is suppressed for
	// the cell. Applied to the PG `regionCalendarStats.SampleDays`. See U2.
	propIntelSurgeMinSamples = 30
	// propIntelSurgeMinSamplesMem is the memory-fallback analogue: the
	// minimum number of 15-min sub-windows in the trailing 6h baseline for
	// the z-score to be trustworthy. Lower than the PG guard because each
	// sub-window carries less evidence than a full day, and a 6h window at
	// 15-min granularity yields only ~23 sub-windows — the guard must be
	// achievable within the fallback window or the fallback is dead code.
	propIntelSurgeMinSamplesMem = 10
	// propIntelSurgeBaselineWindowMin is the trailing window used to derive a
	// memory-fallback baseline rate/stddev when no PG store is configured.
	// Must be wider than the nowcast window so the live signal is excluded.
	propIntelSurgeBaselineWindowMin = 6 * 60
)

// propIntelResponse is the JSON envelope returned by /api/prop_intel.
type propIntelResponse struct {
	QTH                 string           `json:"qth"`
	Minutes             int              `json:"minutes"`
	Now                 int64            `json:"now"`
	ForecastHorizonHours float64         `json:"forecast_horizon_hours"`
	Bands               []string         `json:"bands"`
	Regions             []string         `json:"regions"`
	Cells               []propIntelCell  `json:"cells"`
}

// propIntelCell is one (band × region) row. Sparse cells (no spots in the
// window) are still emitted with a low-confidence estimate so the frontend
// can render the full 11-region grid; the deduplication and source attribution
// only apply to cells with ≥1 spot.
type propIntelCell struct {
	Band          string             `json:"band"`
	Region        string             `json:"region"`
	POpen         float64            `json:"p_open"`
	ExpectedCount float64            `json:"expected_count"`
	Confidence    float64            `json:"confidence"`
	Sources       []string           `json:"sources"`
	Nowcast       propIntelEstimate  `json:"nowcast"`
	Forecast      propIntelEstimate  `json:"forecast"`
	// Surge is non-nil when surge detection flagged this cell. nil means no
	// surge (either below the z-threshold, suppressed by the minimum-sample
	// guard, or the stddev was zero). See U2.
	Surge         *SurgeInfo         `json:"surge,omitempty"`
}

// SurgeInfo carries the surge-detection result for a flagged cell. Mirrors
// the AE2 acceptance example: z-score against the region-level baseline, plus
// a human-readable label ("tune to 10m, surge to Caribbean").
type SurgeInfo struct {
	ZScore float64 `json:"z_score"`
	Label  string  `json:"label"`
}

// propIntelEstimate is the nowcast or forecast sub-object.
type propIntelEstimate struct {
	POpen         float64 `json:"p_open"`
	ExpectedCount float64 `json:"expected_count"`
	Confidence    float64 `json:"confidence"`
}

// propIntelEngine is the stateless evaluator. It holds no mutable state of its
// own; the dxBaseline singleton and hub.history are read per-request by the
// handler. The struct exists so tests can construct an engine with a chosen
// dxBaseline (nil for the no-PG path) and call Evaluate directly.
type propIntelEngine struct {
	// baseline is the DxBaselineEngine used for the per-band sparkline
	// (forecast slope) and for accessing the Postgres store
	// (regionCalendarStats). May be nil — the engine degrades to a
	// history-only nowcast with a flat (zero-slope) forecast.
	baseline *DxBaselineEngine
}

// propIntel is the package-level singleton, mirroring dxBaseline. It is wired
// in main.go alongside dxBaseline so the handler can call it without nil
// checks (the handler still guards for safety).
var propIntel = &propIntelEngine{}

// propIntelCellKey indexes a (band × region) accumulator.
type propIntelCellKey struct {
	band   string
	region string
}

// propIntelCellAcc accumulates per-cell evidence during a window scan.
type propIntelCellAcc struct {
	// uniqueSenders deduplicates across sources: the same callsign appearing
	// in both an RBN spot and a PSKReporter spot counts once. The key is the
	// remote callsign (the end not matching QTH).
	uniqueSenders map[string]struct{}
	// sources records which ingest tags contributed, lower-cased and mapped to
	// the canonical rbn/pskreporter/wspr labels.
	sources map[string]struct{}
}

// Evaluate computes the per-(band × region) nowcast and 1-hour forecast for the
// given QTH and history window. It mirrors the dxConditionsHandler history
// access pattern: the caller passes a pre-copied history slice scoped to the
// window, so no lock is taken here.
//
// The engine:
//  1. Resolves the QTH to a qthSet (locator → surroundings expansion, callsign
//     → QRZ/cty.dat fallback via dxBaseline.deriveOperatorCluster's resolver).
//  2. Scans the window, resolving each spot's remote end (the end not matching
//     QTH) to a region via dxPulseRegionForLocator, grouping by (band × region)
//     and deduplicating senders across sources.
//  3. Computes the nowcast rate per cell = unique_senders / window_hours, then
//     P(open) = 1 − e^(−λ) with λ = rate × (slot/60).
//  4. Computes the 1-hour forecast by calling dxBaseline.Evaluate for the
//     per-band sparkline, taking slope = mean(last4) − mean(first4) bins,
//     normalised to rate/hour, and applying it to the nowcast rate.
//  5. Confidence = f(unique_senders, source_diversity), discounted for the
//     forecast by sparkline volatility.
func (e *propIntelEngine) Evaluate(qth string, surroundings bool, minutes int, cwMinDb int, history []MQTTMessage, now int64, surgeThreshold float64) propIntelResponse {
	qth = normalizeQTHToken(qth)
	if minutes <= 0 {
		minutes = propIntelNowcastWindowMin
	}
	if minutes > maxDxWindowMinutes {
		minutes = maxDxWindowMinutes
	}
	if cwMinDb < -40 || cwMinDb > 20 {
		cwMinDb = defaultDxCwViableMinDb
	}

	resp := propIntelResponse{
		QTH:                  qth,
		Minutes:              minutes,
		Now:                  now,
		ForecastHorizonHours: propIntelForecastHorizonH,
		Bands:                []string{},
		Regions:              allRegionStrings(),
		Cells:                []propIntelCell{},
	}

	if qth == "" {
		return resp
	}

	// Build the QTH match set. For a locator QTH with surroundings, expand to
	// the 3×3 block (same as dxConditionsHandler). For a callsign QTH, rely on
	// the existing matchCall prefix/suffix logic — no locator expansion.
	qthSet := []string{qth}
	if surroundings && isLocator(qth) {
		qthSet = getSurroundingSquares(qth)
	}

	cutoff := now - int64(minutes)*60
	windowHours := float64(minutes) / 60.0
	if windowHours <= 0 {
		windowHours = float64(propIntelNowcastWindowMin) / 60.0
	}

	// Scan the window into per-(band × region) accumulators.
	acc := make(map[propIntelCellKey]*propIntelCellAcc)
	bandsSeen := make(map[string]struct{})
	for _, m := range history {
		if m.T < cutoff || m.T > now {
			continue
		}
		band := normalizeBand(m.B)
		if band == "" || !bandInScope(band) {
			continue
		}
		// Resolve the remote end: the end not matching QTH. Reuse the same
		// match logic as extractMatchedBandEvent so sender/receiver roles are
		// consistent with the rest of the codebase.
		remoteLocator, remoteCall, ok := resolveRemoteEnd(m, qthSet)
		if !ok {
			continue
		}
		region := dxPulseRegionForLocator(remoteLocator)
		if region == dxPulseRegionUnknown {
			continue
		}

		key := propIntelCellKey{band: band, region: string(region)}
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
		cell.sources[canonicalSource(m)] = struct{}{}
		bandsSeen[band] = struct{}{}
	}

	// In-scope bands list: the bandsInScope order is a map (unordered), so
	// emit the canonical HF→VHF order for a stable response.
	resp.Bands = inScopeBandsOrdered(bandsSeen)

	// Per-band sparkline for forecast slope. Only call Evaluate if a baseline
	// is configured; otherwise the slope is zero (flat forecast).
	bandSparkline := make(map[string][]float64)
	if e.baseline != nil {
		cond := e.baseline.Evaluate(qth, surroundings, minutes, cwMinDb, history, now)
		for _, b := range cond.Bands {
			if len(b.Sparkline) > 0 {
				bandSparkline[b.Band] = b.Sparkline
			}
		}
	}

	// Region baseline rates from Postgres (regionCalendarStats), keyed by
	// (band, region, slot). Used as the prior for sparse cells and as a
	// sanity clamp on the nowcast rate. Absent when no store is configured.
	regionBaseline := e.loadRegionBaselines(now)

	slot := utcSlotOfDay(now)
	cells := make([]propIntelCell, 0, len(acc))

	// Emit cells for every (band × region) with data, plus sparse cells for
	// in-scope bands with a region baseline but no live spots.
	emitted := make(map[propIntelCellKey]bool)
	for key, cell := range acc {
		uniqueSenders := len(cell.uniqueSenders)
		nowcastRate := float64(uniqueSenders) / windowHours
		// Clamp the nowcast rate against the region baseline mean when the
		// baseline exists and is much higher than the live rate — this is a
		// mild regulariser, not a cap: a live burst above baseline is allowed.
		if base, ok := regionBaseline[regionBaselineKey{key.band, key.region, slot}]; ok && base.Mean > nowcastRate {
			// Blend: 70% live, 30% baseline prior when live is thin.
			if uniqueSenders < propIntelMinSendersHighConf {
				nowcastRate = 0.7*nowcastRate + 0.3*base.Mean
			}
		}

		nowcastPOpen := poissonPOpen(nowcastRate, propIntelNowcastSlotMin)
		nowcastConf := cellConfidence(uniqueSenders, len(cell.sources))

		spark := bandSparkline[key.band]
		slope := sparklineSlopePerHour(spark, minutes)
		forecastRate := nowcastRate + slope*propIntelForecastHorizonH
		if forecastRate < 0 {
			forecastRate = 0
		}
		forecastPOpen := poissonPOpen(forecastRate, propIntelNowcastSlotMin)
		volatility := sparklineVolatility(spark)
		forecastConf := nowcastConf * (1.0 - volatility*propIntelForecastVolatilityDiscount)
		if forecastConf < 0 {
			forecastConf = 0
		}

		cells = append(cells, propIntelCell{
			Band:          key.band,
			Region:        key.region,
			POpen:         round3(nowcastPOpen),
			ExpectedCount: round3(nowcastRate),
			Confidence:    round3(nowcastConf),
			Sources:       sortedSources(cell.sources),
			Nowcast: propIntelEstimate{
				POpen:         round3(nowcastPOpen),
				ExpectedCount: round3(nowcastRate),
				Confidence:    round3(nowcastConf),
			},
			Forecast: propIntelEstimate{
				POpen:         round3(forecastPOpen),
				ExpectedCount: round3(forecastRate),
				Confidence:    round3(forecastConf),
			},
		})
		emitted[key] = true
	}

	// Sparse cells: in-scope bands seen in the window with a region baseline
	// but no live spots. These get a low-confidence estimate from the baseline
	// prior so the frontend can render the full region grid.
	for band := range bandsSeen {
		for _, region := range dxPulseAllRegions {
			key := propIntelCellKey{band: band, region: string(region)}
			if emitted[key] {
				continue
			}
			base, ok := regionBaseline[regionBaselineKey{band, string(region), slot}]
			if !ok || base.Mean <= 0 {
				continue
			}
			rate := base.Mean
			pOpen := poissonPOpen(rate, propIntelNowcastSlotMin)
			conf := 0.15 // sparse prior: low confidence, no source attribution
			cells = append(cells, propIntelCell{
				Band:          band,
				Region:        string(region),
				POpen:         round3(pOpen),
				ExpectedCount: round3(rate),
				Confidence:    round3(conf),
				Sources:       []string{},
				Nowcast: propIntelEstimate{
					POpen:         round3(pOpen),
					ExpectedCount: round3(rate),
					Confidence:    round3(conf),
				},
				Forecast: propIntelEstimate{
					POpen:         round3(pOpen),
					ExpectedCount: round3(rate),
					Confidence:    round3(conf),
				},
			})
			emitted[key] = true
		}
	}

	sort.Slice(cells, func(i, j int) bool {
		if cells[i].Band != cells[j].Band {
			return cells[i].Band < cells[j].Band
		}
		return cells[i].Region < cells[j].Region
	})

	// Surge detection runs after the nowcast cells are built. It mutates
	// cells in place, attaching *SurgeInfo to any cell whose live rate z-scores
	// above the region-level baseline. See U2.
	threshold := surgeThreshold
	if threshold <= 0 {
		threshold = propIntelSurgeZThreshold
	}
	detectSurges(cells, qth, surroundings, cwMinDb, history, now, threshold, regionBaseline)

	resp.Cells = cells

	// U5: Web Push. After detectSurges has flagged cells, fan out push
	// notifications to subscriptions whose preferences match a surged
	// (band × region). Runs in a goroutine so a slow push endpoint
	// cannot block the /api/prop_intel HTTP response (the plan's
	// async-push requirement). The store is nil-safe and a no-op when
	// push is not configured. cells is a copy owned by this response,
	// so the goroutine can read it after the handler returns.
	if hasSurge := surgePresent(cells); hasSurge {
		go pushStore.NotifySurges(cells, qth)
	}

	return resp
}

// surgePresent reports whether any cell in the slice has a Surge.
// O(cells); used to skip the goroutine launch when there is nothing to push.
func surgePresent(cells []propIntelCell) bool {
	for i := range cells {
		if cells[i].Surge != nil {
			return true
		}
	}
	return false
}

// resolveRemoteEnd returns the remote locator and callsign (the end of the spot
// not matching the QTH set), mirroring extractMatchedBandEvent's role logic.
// ok=false if neither end matches or the remote locator is empty/non-locator.
func resolveRemoteEnd(m MQTTMessage, qthSet []string) (locator, callsign string, ok bool) {
	sc := strings.ToUpper(strings.TrimSpace(m.SC))
	rc := strings.ToUpper(strings.TrimSpace(m.RC))
	sl := strings.ToUpper(strings.TrimSpace(m.SL))
	rl := strings.ToUpper(strings.TrimSpace(m.RL))
	if sl == "" && rl == "" {
		return "", "", false
	}

	isSender := false
	isReceiver := false
	for _, t := range qthSet {
		if matchCall(sc, t) || (isLocator(t) && sl != "" && strings.HasPrefix(sl, t)) {
			isSender = true
		}
		if matchCall(rc, t) || (isLocator(t) && rl != "" && strings.HasPrefix(rl, t)) {
			isReceiver = true
		}
	}
	if !isSender && !isReceiver {
		return "", "", false
	}

	// If the operator is the sender, the remote is the receiver end and vice
	// versa. If both ends match (e.g. surrounding squares overlap), prefer the
	// receiver locator as the remote — matching matchAndCreateSpot's bias.
	remoteLocator := rl
	remoteCall := rc
	if isSender && !isReceiver {
		remoteLocator = rl
		remoteCall = rc
	} else if isReceiver && !isSender {
		remoteLocator = sl
		remoteCall = sc
	}
	if remoteLocator == "" || !isLocator(remoteLocator) {
		return "", "", false
	}
	return remoteLocator, remoteCall, true
}

// canonicalSource maps an MQTTMessage.Source / mode to the canonical
// rbn/pskreporter/wspr label used in the response. dxcluster spots are folded
// into "pskreporter" (they share the FT8/SSB SNR scale); only RBN and WSPR get
// their own label.
func canonicalSource(m MQTTMessage) string {
	src := strings.ToLower(strings.TrimSpace(m.Source))
	switch src {
	case "rbn":
		return "rbn"
	case "wspr":
		return "wspr"
	case "dxcluster":
		return "pskreporter"
	default:
		// Legacy mqtt ingest is PSKReporter.
		return "pskreporter"
	}
}

// sortedSources returns the source set as a sorted slice for stable JSON.
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
	out := make([]string, 0, len(dxPulseAllRegions))
	for _, r := range dxPulseAllRegions {
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

// poissonPOpen returns P(≥1 event) = 1 − e^(−λ) where λ = ratePerHour ×
// (slotMinutes/60). A zero rate yields P(open)=0.
func poissonPOpen(ratePerHour, slotMinutes float64) float64 {
	if ratePerHour <= 0 {
		return 0
	}
	lambda := ratePerHour * (slotMinutes / 60.0)
	return 1.0 - math.Exp(-lambda)
}

// cellConfidence computes the nowcast confidence from unique-sender support
// and source diversity. Returns a 0–1 value.
//
//	uniqueSenders ≥ 10 and ≥2 sources  → high (0.7–1.0)
//	uniqueSenders ≥ 8  and 1 source     → moderate (0.4–0.6)
//	uniqueSenders < 3                   → low (<0.3)
//
// Single-source cells are discounted by propIntelSingleSourceDiscount, but a
// dense single-source cell (≥ propIntelMinSendersModConf senders) is lifted
// back to at least moderate confidence.
func cellConfidence(uniqueSenders, sourceCount int) float64 {
	if uniqueSenders <= 0 {
		return 0
	}
	// Support factor: saturates at propIntelMinSendersHighConf senders.
	support := clamp01(float64(uniqueSenders) / float64(propIntelMinSendersHighConf))
	// Diversity factor: 1 source → 0.5, 2 → 0.85, 3+ → 1.0.
	var diversity float64
	switch {
	case sourceCount >= 3:
		diversity = 1.0
	case sourceCount == 2:
		diversity = 0.85
	default:
		diversity = 0.5
	}
	conf := 0.5*support + 0.5*diversity
	if sourceCount <= 1 {
		conf *= propIntelSingleSourceDiscount
		// A dense single-source cell should still reach moderate confidence.
		if uniqueSenders >= propIntelMinSendersModConf {
			conf = math.Max(conf, 0.45)
		}
	}
	return clamp01(conf)
}

// sparklineSlopePerHour returns the slope of the sparkline (mean of last 4
// bins − mean of first 4 bins), converted to a rate-per-hour delta. The
// sparkline is a 0–100 normalised series over `minutes`; the conversion assumes
// the series is proportional to spots/min, so slope_per_hour = slope × (60 /
// minutes) — i.e. the bin delta expressed as an hourly rate change. A nil/short
// sparkline yields 0 (flat forecast).
func sparklineSlopePerHour(sparkline []float64, minutes int) float64 {
	if len(sparkline) < 8 || minutes <= 0 {
		return 0
	}
	first4 := mean(sparkline[:4])
	last4 := mean(sparkline[len(sparkline)-4:])
	delta := last4 - first4
	// The sparkline spans `minutes` across 12 bins; each bin = minutes/12 min.
	// Convert the 0–100 delta to a per-hour rate delta: scale by 60/minutes so
	// a full-window rise maps to an hourly rate.
	return delta * (60.0 / float64(minutes))
}

// sparklineVolatility returns the variance of bin-to-bin deltas, a measure of
// how jittery the band has been. Used to discount forecast confidence.
func sparklineVolatility(sparkline []float64) float64 {
	if len(sparkline) < 2 {
		return 0
	}
	deltas := make([]float64, 0, len(sparkline)-1)
	for i := 1; i < len(sparkline); i++ {
		deltas = append(deltas, sparkline[i]-sparkline[i-1])
	}
	m := mean(deltas)
	var sumSq float64
	for _, d := range deltas {
		sumSq += (d - m) * (d - m)
	}
	return sumSq / float64(len(deltas))
}

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

// loadRegionBaselines fetches the per-(band × region × slot) baseline from
// Postgres when a store is configured. Returns an empty map in the no-PG path
// (the engine then uses the live history rate only).
func (e *propIntelEngine) loadRegionBaselines(now int64) map[regionBaselineKey]regionCalendarStatRow {
	out := make(map[regionBaselineKey]regionCalendarStatRow)
	if e == nil || e.baseline == nil {
		return out
	}
	e.baseline.mu.RLock()
	st := e.baseline.store
	e.baseline.mu.RUnlock()
	if st == nil {
		return out
	}
	rows, err := st.regionCalendarStats(propIntelRegionBaselineDaysBack, now)
	if err != nil {
		return out
	}
	for _, r := range rows {
		out[regionBaselineKey{r.Band, r.Region, r.SlotOfDay}] = r
	}
	return out
}

// propIntelRegionDisplayNames maps the 11-region codes to the display names
// used in surge labels (e.g., "tune to 10m, surge to Caribbean"). The codes
// follow dxPulseAllRegions; the names follow the operator-facing convention
// used in the existing WSPR matrix panel.
var propIntelRegionDisplayNames = map[string]string{
	"EU":   "Europe",
	"NA":   "North America",
	"SA":   "South America",
	"AF":   "Africa",
	"AS":   "Asia",
	"OC":   "Oceania",
	"AN":   "Antarctica",
	"JA":   "Japan",
	"VK":   "Australia",
	"KH6":  "Hawaii",
	"CAR":  "Caribbean",
}

// regionDisplayName returns the display name for a region code, falling back
// to the code itself if unknown.
func regionDisplayName(code string) string {
	if name, ok := propIntelRegionDisplayNames[code]; ok {
		return name
	}
	return code
}

// detectSurges flags per-(band × region) cells whose live nowcast rate
// z-scores above the region-level baseline. Mutates `cells` in place by
// setting cell.Surge for flagged cells.
//
// The baseline rate and stddev come from two sources, in order:
//  1. PG-backed: `regionBaseline` (regionCalendarStats) for the current slot.
//     Suppressed when the cell has fewer than propIntelSurgeMinSamples samples
//     — a stddev from very few samples is statistically meaningless for rare
//     band/region combinations (U2 minimum-sample guard).
//  2. Memory fallback: a trailing propIntelSurgeBaselineWindowMin-minute rate
//     computed from the history window, excluding the live 15-minute nowcast
//     window so the surge signal does not contaminate the baseline. The
//     baseline stddev is derived from per-15-minute sub-window rates across
//     the trailing baseline window.
//
// z = (live_rate − baseline_rate) / baseline_stddev. A zero stddev yields no
// surge (guard against division by zero; the baseline has no variance to
// compare against). A cell is flagged when z ≥ threshold.
func detectSurges(
	cells []propIntelCell,
	qth string,
	surroundings bool,
	cwMinDb int,
	history []MQTTMessage,
	now int64,
	threshold float64,
	regionBaseline map[regionBaselineKey]regionCalendarStatRow,
) {
	if threshold <= 0 {
		threshold = propIntelSurgeZThreshold
	}
	slot := utcSlotOfDay(now)
	nowcastWindowMin := propIntelNowcastWindowMin

	// Pre-compute the memory-fallback baseline per (band × region) from the
	// trailing 6-hour window excluding the live 15-minute window. Each
	// 15-minute sub-window contributes one rate sample; the baseline rate is
	// the mean and the baseline stddev is the sample standard deviation of
	// those sub-window rates. The live window (now-15m..now) is excluded so
	// the surge signal does not contaminate the baseline (U2 requirement).
	memBaselines := memorySurgeBaselines(history, qth, surroundings, now, nowcastWindowMin)

	for i := range cells {
		c := &cells[i]
		// Only cells with a nonzero nowcast rate can surge — a zero-rate
		// cell has nothing to surge above.
		if c.ExpectedCount <= 0 {
			continue
		}
		liveRate := c.ExpectedCount

		var baseRate, baseStd float64
		if row, ok := regionBaseline[regionBaselineKey{c.Band, c.Region, slot}]; ok && row.SampleDays >= propIntelSurgeMinSamples {
			baseRate = row.Mean
			baseStd = row.StdDev
		} else if mb, ok := memBaselines[propIntelCellKey{c.Band, c.Region}]; ok && mb.n >= propIntelSurgeMinSamplesMem {
			baseRate = mb.mean
			baseStd = mb.stddev
		}

		// Guard against stddev=0: the baseline has no variance to compare
		// against, so a z-score is undefined. Treat as no surge (U2).
		if baseStd <= 0 {
			continue
		}

		z := (liveRate - baseRate) / baseStd
		if z < threshold {
			continue
		}

		c.Surge = &SurgeInfo{
			ZScore: round3(z),
			Label:  "tune to " + c.Band + ", surge to " + regionDisplayName(c.Region),
		}
	}
}

// memorySurgeBaseline is the memory-fallback baseline (rate + stddev) for a
// (band × region) cell, derived from per-15-minute sub-window rates across a
// trailing 6-hour window that excludes the live nowcast window.
type memorySurgeBaseline struct {
	mean   float64
	stddev float64
	n      int
}

// memorySurgeBaselines computes the memory-fallback surge baseline for every
// (band × region) cell present in the trailing history. The window spans
// [now - baselineWindowMin, now), and the live nowcast window
// [now - nowcastWindowMin, now) is excluded so the surge signal does not
// contaminate the baseline (U2 requirement). The trailing window is divided
// into 15-minute sub-windows; each sub-window contributes one rate sample
// (unique senders per hour). The baseline rate is the mean of those samples
// and the baseline stddev is the sample standard deviation. Cells with
// fewer than propIntelSurgeMinSamples sub-windows are returned but flagged
// via n (the caller's minimum-sample guard suppresses them).
func memorySurgeBaselines(
	history []MQTTMessage,
	qth string,
	surroundings bool,
	now int64,
	nowcastWindowMin int,
) map[propIntelCellKey]memorySurgeBaseline {
	if nowcastWindowMin <= 0 {
		nowcastWindowMin = propIntelNowcastWindowMin
	}
	baselineWindowMin := propIntelSurgeBaselineWindowMin
	if baselineWindowMin <= nowcastWindowMin {
		baselineWindowMin = nowcastWindowMin * 2
	}

	// Build the QTH match set (mirrors Evaluate's resolver).
	qth = normalizeQTHToken(qth)
	if qth == "" {
		return nil
	}
	qthSet := []string{qth}
	if surroundings && isLocator(qth) {
		qthSet = getSurroundingSquares(qth)
	}

	nowcastCutoff := now - int64(nowcastWindowMin)*60
	baselineStart := now - int64(baselineWindowMin)*60
	// Trailing baseline window: [baselineStart, nowcastCutoff) — excludes the
	// live nowcast window so the surge signal is not in the baseline.
	if baselineStart >= nowcastCutoff {
		return nil
	}
	// Sub-window granularity matches the nowcast slot (15 min). A 6h
	// baseline yields ~23 sub-window samples. The PG baseline counts days
	// (propIntelSurgeMinSamples=30 days); the memory fallback counts
	// 15-min slots, which carry far less evidence each, so the guard uses
	// propIntelSurgeMinSamplesMem (lower) — see detectSurges for the gate.
	subWindowSec := int64(propIntelNowcastSlotMin * 60)
	if subWindowSec <= 0 {
		subWindowSec = 900
	}

	// Per-(band × region × sub-window) unique-sender accumulators.
	type subAcc struct {
		uniqueSenders map[string]struct{}
	}
	type cellSubs struct {
		subs map[int64]*subAcc
	}
	acc := make(map[propIntelCellKey]*cellSubs)

	for _, m := range history {
		if m.T < baselineStart || m.T >= nowcastCutoff {
			continue
		}
		band := normalizeBand(m.B)
		if band == "" || !bandInScope(band) {
			continue
		}
		remoteLocator, remoteCall, ok := resolveRemoteEnd(m, qthSet)
		if !ok {
			continue
		}
		region := dxPulseRegionForLocator(remoteLocator)
		if region == dxPulseRegionUnknown {
			continue
		}
		// Sub-window index relative to baselineStart: 0, 1, 2, ...
		subIdx := (m.T - baselineStart) / subWindowSec
		if subIdx < 0 {
			continue
		}
		key := propIntelCellKey{band: band, region: string(region)}
		cell := acc[key]
		if cell == nil {
			cell = &cellSubs{subs: make(map[int64]*subAcc)}
			acc[key] = cell
		}
		sa := cell.subs[subIdx]
		if sa == nil {
			sa = &subAcc{uniqueSenders: make(map[string]struct{})}
			cell.subs[subIdx] = sa
		}
		if remoteCall != "" {
			sa.uniqueSenders[remoteCall] = struct{}{}
		}
	}

	out := make(map[propIntelCellKey]memorySurgeBaseline, len(acc))
	subHours := float64(subWindowSec) / 3600.0
	if subHours <= 0 {
		subHours = 0.25
	}
	// Total number of sub-windows in the baseline window. Each sub-window
	// (including zero-activity ones) counts as one sample — a flat baseline
	// with half its sub-windows empty still has nonzero variance when the
	// other half is active, and the minimum-sample guard must see the empty
	// sub-windows too (otherwise a sparse baseline inflates n with only the
	// active sub-windows and misrepresents its statistical weight).
	totalSubs := int((nowcastCutoff - baselineStart) / subWindowSec)
	if totalSubs < 1 {
		totalSubs = 1
	}
	for key, cell := range acc {
		rates := make([]float64, 0, totalSubs)
		// Iterate sub-window indices 0..totalSubs-1; missing entries are
		// zero-activity sub-windows and contribute rate 0.
		for idx := int64(0); idx < int64(totalSubs); idx++ {
			sa, ok := cell.subs[idx]
			if !ok {
				rates = append(rates, 0)
				continue
			}
			rate := float64(len(sa.uniqueSenders)) / subHours
			rates = append(rates, rate)
		}
		if len(rates) == 0 {
			continue
		}
		mean := mean(rates)
		var stddev float64
		if len(rates) >= 2 {
			var sumSqDiff float64
			for _, r := range rates {
				d := r - mean
				sumSqDiff += d * d
			}
			// Sample standard deviation (n−1 denominator).
			stddev = math.Sqrt(sumSqDiff / float64(len(rates)-1))
		}
		out[key] = memorySurgeBaseline{mean: mean, stddev: stddev, n: len(rates)}
	}
	return out
}

// propIntelHandler is the HTTP handler for /api/prop_intel. It follows the
// dxConditionsHandler pattern: resolve QTH, parse minutes/cw_min_db/
// surroundings, snapshot hub.history via binary-search + copy under RLock,
// call the engine, JSON-encode. The `surge_threshold` query parameter
// overrides the default z-score threshold for surge detection (U2).
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

	// surge_threshold overrides the default z-score threshold for this
	// request. Per-band/region overrides are deferred to v2 (KTD3).
	surgeThreshold := propIntelSurgeZThreshold
	if raw := strings.TrimSpace(r.URL.Query().Get("surge_threshold")); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v > 0 {
			surgeThreshold = v
		}
	}

	now := time.Now().Unix()
	cutoff := now - int64(minutes)*60
	// Copy a wider window than the nowcast when surge detection is active so
	// the memory-fallback baseline (trailing 6h) is available even when the
	// requested `minutes` is short. The engine ignores out-of-window spots
	// for the nowcast; detectSurges uses the extra history for its baseline.
	baselineCutoff := now - int64(propIntelSurgeBaselineWindowMin)*60
	if baselineCutoff < cutoff {
		cutoff = baselineCutoff
	}
	hub.RLock()
	idx := sort.Search(len(hub.history), func(i int) bool {
		return hub.history[i].T >= cutoff
	})
	historyCopy := make([]MQTTMessage, len(hub.history)-idx)
	copy(historyCopy, hub.history[idx:])
	hub.RUnlock()

	engine := propIntel
	resp := engine.Evaluate(qth, surroundings, minutes, cwMinDb, historyCopy, now, surgeThreshold)

	// Count surges flagged by detectSurges so /api/stats can report the
	// surge-detection rate (U6). A single request may flag more than one
	// (band × region) cell; each flagged cell increments the counter.
	for i := range resp.Cells {
		if resp.Cells[i].Surge != nil {
			propIntelAccounting.surgesDetected.Add(1)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}