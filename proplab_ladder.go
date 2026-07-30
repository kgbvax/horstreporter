package main

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// proplab_ladder.go implements the Propagation Lab "Ladder" variant (B):
// an empirical propagation layer built from within the existing spot stream.
// Each spot votes for the ionosphere at the great-circle midpoint of its path,
// binned into a 4-character Maidenhead square. Per cell the engine tracks
// distinct links, SNR per source lane, and distances. A band is considered
// open when enough distinct links/reporters are seen above an SNR floor. The
// crucial discriminator is the MUF-ladder: adjacent F-layer bands opening in
// order 40m→30m→20m→17m→15m→12m→10m is propagation-coherent; a lone open band
// surrounded by quiet neighbours is more likely an operator-activity spike.

// proplabParamsB holds the tunable parameters for variant B. Defaults are set
// so the engine is usable immediately; the Propagation Lab UI exposes each
// as a slider and sends overrides as query parameters.
type proplabParamsB struct {
	MinLinks             int     `json:"min_links"`
	SnrFloorFT8dB        int     `json:"snr_floor_ft8"`
	SnrFloorRBNdB        int     `json:"snr_floor_rbn"`
	WitnessMin           int     `json:"witness_min"`
	EsMinKm              int     `json:"es_min_km"`
	EsMaxKm              int     `json:"es_max_km"`
	CoherenceMinBands    int     `json:"coherence_min_bands"`
	CusumDrift           float64 `json:"cusum_drift"`
	CusumThreshold       float64 `json:"cusum_threshold"`
	EwmaAlpha            float64 `json:"ewma_alpha"`
	ExpectedLookbackDays int     `json:"expected_lookback_days"`
	TermMinEastDeg       float64 `json:"term_min_east_deg"`
	TermMaxEastDeg       float64 `json:"term_max_east_deg"`
	TermDegPerHour       float64 `json:"term_deg_per_hour"`
}

func defaultProplabParamsB() proplabParamsB {
	return proplabParamsB{
		MinLinks:             2,
		SnrFloorFT8dB:        -18,
		SnrFloorRBNdB:        12,
		WitnessMin:           3,
		EsMinKm:              800,
		EsMaxKm:              2200,
		CoherenceMinBands:    2,
		CusumDrift:           0.5,
		CusumThreshold:       4.0,
		EwmaAlpha:            0.35,
		ExpectedLookbackDays: 21,
		TermMinEastDeg:       15.0,
		TermMaxEastDeg:       30.0,
		TermDegPerHour:       15.0,
	}
}

func (p proplabParamsB) snrFloor(lane string) int {
	switch lane {
	case "rbn":
		return p.SnrFloorRBNdB
	default:
		return p.SnrFloorFT8dB
	}
}

// ladderCellBucket is the in-memory accumulator for one cell×band×lane×bucket.
type ladderCellBucket struct {
	links      map[string]struct{}
	reporters  map[string]struct{}
	snrs       []int
	spotCount  int
	sumDistKm  float64
	maxDistKm  float64
	region     string
	closed     bool
}

// ladderBucketKey indexes the in-memory accumulator map.
type ladderBucketKey struct {
	BucketStart int64
	Band        string
	Cell4       string
	Lane        string
}

// cellBandKey indexes per-cell onset/CUSUM state.
type cellBandKey struct {
	Cell4  string
	Region string
	Band   string
}

// cusumState tracks the one-sided CUSUM statistic and the bucket index of the
// last zero crossing so we can estimate onset age.
type cusumState struct {
	S            float64
	LastZeroIdx  int
	LastAlarmIdx int
}

// ladderEngine is the in-memory working set for variant B. It is not safe for
// concurrent use except via Observe (which locks); callers should hold the lock
// while closing buckets or computing verdicts.
type ladderEngine struct {
	mu      sync.Mutex
	buckets map[ladderBucketKey]*ladderCellBucket
	cusum   map[cellBandKey]*cusumState
	ewma    map[cellBandKey]float64 // expected presence, 0..1
	nextIdx int
}

func newLadderEngine() *ladderEngine {
	return &ladderEngine{
		buckets: make(map[ladderBucketKey]*ladderCellBucket),
		cusum:   make(map[cellBandKey]*cusumState),
		ewma:    make(map[cellBandKey]float64),
	}
}

// Observe ingests one spot into the current in-memory bucket. It is safe for
// concurrent callers (the MQTT/RBN/DX-cluster ingest paths).
func (e *ladderEngine) Observe(m MQTTMessage) {
	band := normalizeBand(m.B)
	if !bandInScope(band) {
		return
	}
	cell, ok := midpointCell(m.SL, m.RL)
	if !ok {
		return
	}
	lane := proplabLaneForSourceType(sourceTypeForMessage(m))
	bucketStart := alignBucketStart(m.T)

	lat1, lon1 := locatorToLatLng(strings.ToUpper(strings.TrimSpace(m.SL)))
	lat2, lon2 := locatorToLatLng(strings.ToUpper(strings.TrimSpace(m.RL)))
	dist := haversineKm(lat1, lon1, lat2, lon2)

	sc := strings.ToUpper(strings.TrimSpace(m.SC))
	rc := strings.ToUpper(strings.TrimSpace(m.RC))

	e.mu.Lock()
	defer e.mu.Unlock()

	key := ladderBucketKey{BucketStart: bucketStart, Band: band, Cell4: cell, Lane: lane}
	b := e.buckets[key]
	if b == nil {
		b = &ladderCellBucket{
			links:     make(map[string]struct{}),
			reporters: make(map[string]struct{}),
			region:    string(dxPulseRegionForLocator(cell)),
		}
		e.buckets[key] = b
	}
	b.spotCount++
	b.links[sc+"|"+rc] = struct{}{}
	b.reporters[sc] = struct{}{}
	b.reporters[rc] = struct{}{}
	b.sumDistKm += dist
	if dist > b.maxDistKm {
		b.maxDistKm = dist
	}
	if lane != "dcx" {
		b.snrs = append(b.snrs, m.RP)
	}
}

// CloseBuckets finalises buckets whose start is before or at cutoff, returns
// them as proplabCellRow values, and removes them from memory. The engine
// keeps at most the current and previous bucket live so recent-window verdicts
// can still read them.
func (e *ladderEngine) CloseBuckets(cutoff int64) []proplabCellRow {
	e.mu.Lock()
	defer e.mu.Unlock()

	rows := make([]proplabCellRow, 0, len(e.buckets))
	for k, b := range e.buckets {
		if k.BucketStart > cutoff {
			continue
		}
		b.closed = true
		r := proplabCellRow{
			BucketStart:   k.BucketStart,
			Band:          k.Band,
			Cell4:         k.Cell4,
			Region:        b.region,
			Lane:          k.Lane,
			SpotCount:     b.spotCount,
			LinkCount:     len(b.links),
			ReporterCount: len(b.reporters),
			DistMaxKm:     int(b.maxDistKm),
		}
		if len(b.snrs) > 0 {
			sort.Ints(b.snrs)
			r.SnrMedian = intMedian(b.snrs)
			r.SnrP10 = int(percentileInt(b.snrs, 0.10))
		}
		if b.spotCount > 0 {
			r.DistMedianKm = int(b.sumDistKm / float64(b.spotCount))
		}
		rows = append(rows, r)
		delete(e.buckets, k)
	}
	return rows
}

// ladderBandVerdict is one band's classification from variant B.
type ladderBandVerdict struct {
	Band           string
	State          string // "open", "rising", "activity_spike", "closed", "unconfirmed"
	Reason         string
	Confidence     float64
	SpotsPerMinute float64
	LinksPerMinute float64
	MufCells       []string // midpoint cells that declare this band open
	EsCells        []string // cells classified as sporadic-E on Es-lane bands
	OnsetMinAgo    int      // -1 if no onset detected
	ForecastHints  []string // human-readable strings (e.g. terminator ETA)
}

// ladderVerdict is the full variant-B result for a target/personalization.
type ladderVerdict struct {
	GeneratedAt   int64
	Params        proplabParamsB
	Bands         []ladderBandVerdict
	OpenRuns      [][]string
	EmpiricalMUF  float64 // highest open ladder-F band as MHz, 0 if none
	DataThin      bool
}

// Verdict evaluates the current window and returns per-band recommendations.
// expected is a map of (cell,band) -> expected fractional presence in a bucket
// (0..1) used by the CUSUM/change-point detector. reachable is a map of
// band -> set of regions the target historically reaches, used to scope the
// verdict to bands/regions relevant to the operator. If reachable is empty the
// engine falls back to all observed cells.
func (e *ladderEngine) Verdict(target string, surroundings bool, history []MQTTMessage, reachable map[string]map[string]bool, expected map[cellBandKey]float64, params proplabParamsB, now int64) ladderVerdict {
	resp := ladderVerdict{
		GeneratedAt: now,
		Params:      params,
		Bands:       []ladderBandVerdict{},
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	// Build a working copy of all cell-band-lane observations from both closed
	// (persisted) memory and the live ingest window. We only keep buckets in
	// the last two 15-minute windows for MUF/onset evaluation.
	windowStart := alignBucketStart(now - 30*60)
	live := e.collectLiveBuckets(windowStart)

	// Seed expected-presence EWMA if absent.
	for cbk, exp := range expected {
		if _, ok := e.ewma[cbk]; !ok {
			e.ewma[cbk] = exp
		}
	}

	// Determine which cells matter for this target.
	relevantCells := e.relevantCells(target, surroundings, history, live, reachable)
	if len(relevantCells) == 0 {
		resp.DataThin = true
		return resp
	}

	// Per cell-band, decide open/closed and whether Es.
	openBands := make(map[string]bool)
	cellBandOpen := make(map[cellBandKey]bool)
	cellBandDetails := make(map[cellBandKey]*ladderBandAggregate)

	for _, cbk := range relevantCells {
		agg := live[cbk]
		if agg == nil {
			continue
		}
		cellBandDetails[cbk] = agg
		floor := params.snrFloor(agg.lane)
		if agg.linkCount >= params.MinLinks && agg.reporterCount >= params.WitnessMin && agg.snrMedian >= floor {
			openBands[cbk.Band] = true
			cellBandOpen[cbk] = true
		}
		// Sporadic-E classification for Es-lane bands, regardless of SNR floor.
		if isLadderEsBand(cbk.Band) {
			if _, hops := esSkipClassify(cbk.Band, agg.distMedian); hops > 0 {
				// Mark Es separately; does not change openBands.
			}
		}
	}

	runs, maxRun := ladderOpenBands(openBands)
	resp.OpenRuns = runs
	resp.EmpiricalMUF = empiricalMUFMHz(openBands)

	// Evaluate each in-scope band.
	for _, band := range sortedBands(bandsInScope) {
		v := ladderBandVerdict{Band: band}
		bandCells := bandCells(relevantCells, band, live)
		if len(bandCells) == 0 {
			v.State = "closed"
			v.Reason = "no recent midpoint observations"
			v.Confidence = 0.0
			resp.Bands = append(resp.Bands, v)
			continue
		}

		// Aggregate rates over the live window.
		var totalSpots, totalLinks int
		for _, cbk := range bandCells {
			if agg := live[cbk]; agg != nil {
				totalSpots += agg.spotCount
				totalLinks += agg.linkCount
				if cellBandOpen[cbk] {
					v.MufCells = append(v.MufCells, cbk.Cell4)
				}
				if isLadderEsBand(band) {
					if _, hops := esSkipClassify(band, agg.distMedian); hops > 0 {
						v.EsCells = append(v.EsCells, cbk.Cell4)
					}
				}
			}
		}
		v.SpotsPerMinute = float64(totalSpots) / 30.0
		v.LinksPerMinute = float64(totalLinks) / 30.0

		if openBands[band] {
			if isLadderFBand(band) && !bandInOpenRun(band, openBands, params.CoherenceMinBands) {
				v.State = "activity_spike"
				v.Reason = "single-band spike (activity artifact likely)"
				v.Confidence = 0.35
			} else {
				v.State = "open"
				if isLadderFBand(band) {
					v.Reason = "propagation-coherent MUF opening"
				} else if isLadderEsBand(band) {
					v.Reason = "sporadic-E skip geometry"
				} else {
					v.Reason = "low-band opening"
				}
				v.Confidence = math.Min(1.0, 0.5+0.15*float64(maxRun))
			}
		} else {
			v.State = "closed"
			v.Reason = "below link/witness/SNR floor"
			v.Confidence = 0.5
		}

		// Onset detection and forecast hints.
		onsetAge := e.minOnsetAge(bandCells, live, expected, params, now)
		if onsetAge >= 0 {
			v.OnsetMinAgo = onsetAge
			v.Reason += "; opening detected ~" + itoa(onsetAge) + " min ago"
		}
		if isLadderLowBand(band) {
			hints := e.terminatorHints(bandCells, relevantCells, params, now)
			v.ForecastHints = append(v.ForecastHints, hints...)
		}

		resp.Bands = append(resp.Bands, v)
	}

	resp.DataThin = len(resp.Bands) > 0 && resp.EmpiricalMUF == 0 && maxRun == 0
	return resp
}

// ladderBandAggregate is a lane-merged aggregate for one cell-band over the
// live window (up to two 15-minute buckets). For MUF/witness decisions we merge
// all lanes but keep the strongest median SNR and the sum of distinct links.
type ladderBandAggregate struct {
	lane        string
	spotCount   int
	linkCount   int
	reporterCount int
	snrMedian   int
	snrP10      int
	distMedian  float64
	distMax     float64
}

// collectLiveBuckets merges the in-memory buckets for the requested window
// into per-cell-band aggregates. Lane merging is done by taking the max of
// link/reporter counts and the best (highest) median SNR among lanes that meet
// the source-type SNR floor. Distance median is the unweighted median across
// all lanes.
func (e *ladderEngine) collectLiveBuckets(windowStart int64) map[cellBandKey]*ladderBandAggregate {
	out := make(map[cellBandKey]*ladderBandAggregate)
	for k, b := range e.buckets {
		if k.BucketStart < windowStart {
			continue
		}
		cbk := cellBandKey{Cell4: k.Cell4, Region: b.region, Band: k.Band}
		agg := out[cbk]
		if agg == nil {
			agg = &ladderBandAggregate{lane: k.Lane}
			out[cbk] = agg
		}
		agg.spotCount += b.spotCount
		agg.linkCount += len(b.links)
		agg.reporterCount += len(b.reporters)
		if b.maxDistKm > agg.distMax {
			agg.distMax = b.maxDistKm
		}
		// Use median SNR of the lane with the most links if it has data.
		if len(b.snrs) > 0 {
			med := intMedian(b.snrs)
			if med > agg.snrMedian {
				agg.snrMedian = med
			}
		}
	}
	// Second pass: compute distance median from raw spot counts would require
	// keeping distances per spot; instead use maxDist as a proxy in this v1.
	// The Postgres buckets store the real median for historical analysis.
	for _, agg := range out {
		agg.distMedian = agg.distMax
	}
	return out
}

// relevantCells decides which midpoint cells should contribute to the target's
// verdict. It is the union of:
//   - cells crossed by the live-window paths involving the target's locator;
//   - cells in regions the target historically reaches for each band.
func (e *ladderEngine) relevantCells(target string, surroundings bool, history []MQTTMessage, live map[cellBandKey]*ladderBandAggregate, reachable map[string]map[string]bool) []cellBandKey {
	target = strings.ToUpper(strings.TrimSpace(target))
	var targets []string
	if isLocator(target) {
		targets = []string{target[:4]}
		if surroundings {
			targets = append(targets, getSurroundingSquares(target[:4])...)
		}
	}

	seen := make(map[cellBandKey]bool)
	var out []cellBandKey
	add := func(cbk cellBandKey) {
		if seen[cbk] {
			return
		}
		seen[cbk] = true
		out = append(out, cbk)
	}

	// Live paths involving the target.
	if len(targets) > 0 {
		for _, m := range history {
			band := normalizeBand(m.B)
			if !bandInScope(band) {
				continue
			}
			if !locatorMatchesTargets(m.SL, targets) && !locatorMatchesTargets(m.RL, targets) {
				continue
			}
			cell, ok := midpointCell(m.SL, m.RL)
			if !ok {
				continue
			}
			region := string(dxPulseRegionForLocator(cell))
			add(cellBandKey{Cell4: cell, Region: region, Band: band})
		}
	}

	// Historical reachability.
	for band, regions := range reachable {
		if !bandInScope(band) {
			continue
		}
		for region := range regions {
			for cbk, agg := range live {
				if cbk.Band == band && cbk.Region == region {
					// only cells in the same region, no need to know exact target crossing
					_ = agg
					add(cbk)
				}
			}
		}
	}

	// If we still have no scope (no target, no reachability), fall back to all
	// observed cells so the user at least sees global MUF/coherence.
	if len(out) == 0 {
		for cbk := range live {
			add(cbk)
		}
	}
	return out
}

func locatorMatchesTargets(locator string, targets []string) bool {
	if len(locator) < 4 {
		return false
	}
	prefix := strings.ToUpper(locator[:4])
	for _, t := range targets {
		if prefix == strings.ToUpper(t) {
			return true
		}
	}
	return false
}

func bandCells(cells []cellBandKey, band string, live map[cellBandKey]*ladderBandAggregate) []cellBandKey {
	var out []cellBandKey
	for _, cbk := range cells {
		if cbk.Band == band && live[cbk] != nil {
			out = append(out, cbk)
		}
	}
	return out
}

// minOnsetAge scans the requested cell-band keys, runs the CUSUM update for the
// current bucket, and returns the youngest onset age in minutes, or -1 if none.
func (e *ladderEngine) minOnsetAge(cells []cellBandKey, live map[cellBandKey]*ladderBandAggregate, expected map[cellBandKey]float64, params proplabParamsB, now int64) int {
	bucketIdx := int(now / proplabBucketSeconds)
	minAge := -1
	for _, cbk := range cells {
		agg := live[cbk]
		if agg == nil {
			continue
		}
		exp := expected[cbk]
		if exp <= 0 {
			exp = e.ewma[cbk]
			if exp <= 0 {
				exp = 0.05 // tiny background so a first spot can raise
			}
		}
		// Update EWMA with observed fractional presence for next time.
		obs := math.Min(1.0, float64(agg.linkCount)/math.Max(1.0, exp*float64(params.WitnessMin)))
		old := e.ewma[cbk]
		e.ewma[cbk] = params.EwmaAlpha*obs + (1-params.EwmaAlpha)*old

		st := e.cusum[cbk]
		if st == nil {
			st = &cusumState{}
			e.cusum[cbk] = st
		}
		drift := params.CusumDrift * exp
		st.S += obs - drift
		if st.S < 0 {
			st.S = 0
			st.LastZeroIdx = bucketIdx
		}
		if st.S >= params.CusumThreshold*exp && st.LastZeroIdx < bucketIdx {
			ageBuckets := bucketIdx - st.LastZeroIdx
			ageMin := ageBuckets * proplabBucketSeconds / 60
			if minAge == -1 || ageMin < minAge {
				minAge = ageMin
			}
		}
	}
	return minAge
}

// terminatorHints scans for low-band onsets in cells east of any relevant cell
// and returns human-readable ETA strings.
func (e *ladderEngine) terminatorHints(relevantCells, allCells []cellBandKey, params proplabParamsB, now int64) []string {
	if len(relevantCells) == 0 {
		return nil
	}
	// Longitude of relevant cells (midpoints of the squares).
	var targetLons []float64
	for _, cbk := range relevantCells {
		lat, lon := locatorToLatLng(cbk.Cell4)
		_ = lat
		targetLons = append(targetLons, lon)
	}
	var hints []string
	for _, cbk := range allCells {
		if !isLadderLowBand(cbk.Band) {
			continue
		}
		st := e.cusum[cbk]
		if st == nil || st.S < params.CusumThreshold*0.5 {
			continue
		}
		lat, lon := locatorToLatLng(cbk.Cell4)
		_ = lat
		for _, tl := range targetLons {
			delta := normalizeLon(lon - tl)
			if delta >= params.TermMinEastDeg && delta <= params.TermMaxEastDeg {
				eta := delta / params.TermDegPerHour
				hints = append(hints, "terminator opening may reach this cell in ~"+formatFloat1(eta)+" h")
			}
		}
	}
	if len(hints) > 2 {
		return hints[:2]
	}
	return hints
}

func alignBucketStart(t int64) int64 {
	return (t / proplabBucketSeconds) * proplabBucketSeconds
}

func empiricalMUFMHz(open map[string]bool) float64 {
	// Highest F-ladder band open. Frequencies in MHz.
	mhz := map[string]float64{
		"40m": 7.0, "30m": 10.1, "20m": 14.0, "17m": 18.1,
		"15m": 21.0, "12m": 24.9, "10m": 28.0,
	}
	max := 0.0
	for band := range open {
		if f, ok := mhz[band]; ok && f > max {
			max = f
		}
	}
	return max
}

func sortedBands(set map[string]struct{}) []string {
	order := []string{"160m", "80m", "60m", "40m", "30m", "20m", "17m", "15m", "12m", "10m", "6m", "4m", "2m"}
	var out []string
	for _, b := range order {
		if _, ok := set[b]; ok {
			out = append(out, b)
		}
	}
	return out
}

func formatFloat1(v float64) string {
	return strconv.FormatFloat(v, 'f', 1, 64)
}
