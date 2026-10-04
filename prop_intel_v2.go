package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"horstreporter/internal/region"
)

// prop_intel_v2.go implements /api/prop_intel/v2: the unified multi-source
// propagation-intelligence nowcast. Where v1 (prop_intel.go) is WSPR-only with
// an FT8 flavor cross-reference, v2 evaluates every requested ingest source
// (wspr, pskr, rbn, dxcluster) against its own climatology
// (prop_region_baseline_daily via propBaseline) and rolls them up per
// (band × region) cell with multi-source agreement.
//
// v1 endpoints remain frozen for horstapp compatibility. The remote-end
// resolution is shared with v1 via resolvePropIntelRemoteEnd.

// propIntelV2SourceCell is one source's evidence inside a (band × region)
// cell. SSBOpen/CWOpen are pointers: nil means the source structurally cannot
// prove that mode (e.g. RBN is a CW skimmer — nil, not false).
type propIntelV2SourceCell struct {
	Source    string `json:"source"`
	SpotCount int    `json:"spot_count"`
	// Open is the mode-agnostic "path workable right now" signal.
	Open bool `json:"open"`
	// SSBOpen/CWOpen: nil when not computable for this source.
	SSBOpen *bool `json:"ssb_open,omitempty"`
	CWOpen  *bool `json:"cw_open,omitempty"`
	// OpenBasis documents which model produced the open flags:
	// "budget" (WSPR SNR+power), "snr_floor" (pskr/rbn report floors),
	// "presence" (dxcluster spot count).
	OpenBasis string `json:"open_basis"`
	// UnknownPower: the path budget could not account for TX power (pskr) —
	// the ssb/cw flags are estimates, not measurements.
	UnknownPower bool `json:"unknown_power,omitempty"`
	Rising       bool `json:"rising"`
	// Atypical: non-nil when this source's spot rate z-scores above its own
	// climatology for this (band × region × slot).
	Atypical *propIntelV2Atypical `json:"atypical,omitempty"`
	// SampleDays is the climatology depth behind the atypical call.
	SampleDays int `json:"sample_days"`
}

// propIntelV2Atypical carries v1's z/confidence without the FT8 flavor —
// multi-source agreement replaces the single cross-reference.
type propIntelV2Atypical struct {
	ZScore     float64 `json:"z_score"`
	Confidence float64 `json:"confidence"`
}

// propIntelV2Cell is one (band × region) row: the per-source breakdown plus a
// combined rollup the UI renders directly.
type propIntelV2Cell struct {
	Band   string `json:"band"`
	Region string `json:"region"`
	// FromHere: the operator's QTH is one end of a path in this cell.
	FromHere bool `json:"from_here"`
	// SpotCount sums the selected sources' spots.
	SpotCount int `json:"spot_count"`
	// Open: any selected source reports the path workable.
	Open bool `json:"open"`
	// SSBOpen/CWOpen: any source with a computable non-nil true.
	SSBOpen bool `json:"ssb_open"`
	CWOpen  bool `json:"cw_open"`
	Rising  bool `json:"rising"`
	// Atypical: the highest-confidence atypical across sources.
	Atypical *propIntelV2Atypical `json:"atypical,omitempty"`
	// ActiveSources are the selected sources with spots in this cell,
	// in canonical profile order.
	ActiveSources []string `json:"active_sources"`
	// OpenAgreement is the fraction of active sources reporting Open (0..1).
	// Vacuously 1.0 with a single active source — clients should render the
	// ActiveSources count, not this fraction alone.
	OpenAgreement float64 `json:"open_agreement"`
	// AtypicalAgreement is the fraction of active sources whose own atypical
	// fired. Omitted when no source is atypical.
	AtypicalAgreement float64                 `json:"atypical_agreement,omitempty"`
	PerSource         []propIntelV2SourceCell `json:"sources"`
	// Expected and ExpectedSpots are set in the from-here view only, when
	// PSKReporter is selected and the area has a normal (prop_intel_fromhere.go).
	// Expected is the mean PSKReporter (+ DX-cluster) count for this band and
	// far-end region over the same clock window on recent days, from the
	// operator's area; ExpectedSpots is the live count of those same spots, so
	// ExpectedSpots / Expected is the cell against its normal.
	Expected      *float64 `json:"expected,omitempty"`
	ExpectedSpots *int     `json:"expected_spots,omitempty"`
}

// propIntelV2FromHere switches the evaluator to the from-here view: only spots
// with an end in the area are counted, and expected / atypical come from the
// area's own normal instead of the global climatology.
type propIntelV2FromHere struct {
	normals *fromHereNormals // nil: no normal available
}

// propIntelV2Response is the JSON envelope for /api/prop_intel/v2.
type propIntelV2Response struct {
	// Area is the live area the cells are scoped to; present only when the
	// request asked for one (rings=auto or rings=N).
	Area     *liveArea `json:"area,omitempty"`
	QTH      string    `json:"qth"`
	Minutes  int       `json:"minutes"`
	Now      int64     `json:"now"`
	FromHere bool      `json:"from_here"`
	// SourcesRequested echoes the resolved source selection (public names,
	// canonical order).
	SourcesRequested []string          `json:"sources_requested"`
	Bands            []string          `json:"bands"`
	BandOrder        []string          `json:"band_order"`
	Regions          []string          `json:"regions"`
	RegionNames      map[string]string `json:"region_names"`
	Cells            []propIntelV2Cell `json:"cells"`
}

// propIntelV2CellKey indexes accumulators by (band × region × source).
type propIntelV2CellKey struct {
	band   string
	region string
	source string // public profile name
}

type propIntelV2Acc struct {
	spotCount       int
	firstHalfSpots  int
	secondHalfSpots int
	// wspr budget model
	bestBudgetSNR float64
	hasPower      bool
	// pskr/rbn report floors
	bestReportSNR int
	hasReport     bool
	// cell-level (source-agnostic): operator QTH on either end of any path
	fromHere bool
}

// resolvePropIntelRemoteEnd resolves the remote end of a spot relative to the
// operator's qthSet, honoring the source's ReceiverSide convention ("sc" or
// "rc"). When an end matches the QTH, the remote is the OTHER end (symmetric,
// works for every source). When neither end matches (global-mesh fallback),
// the receiver/reporter side's locator is the region key — "where the path
// landed". v1 calls this with receiverSide="sc" (WSPR convention), which is
// byte-identical to the inline logic it replaces.
func resolvePropIntelRemoteEnd(m MQTTMessage, qthSet []string, receiverSide string) (remoteLocator, remoteCall string, matchedEnd bool) {
	return resolvePropIntelRemoteEndArea(m, qthSet, nil, receiverSide)
}

// resolvePropIntelRemoteEndArea is resolvePropIntelRemoteEnd with an optional
// wide live area: when set, an end matches if its square lies in the block
// (an O(1) test instead of one prefix test per square; qthSet is ignored).
func resolvePropIntelRemoteEndArea(m MQTTMessage, qthSet []string, area *liveArea, receiverSide string) (remoteLocator, remoteCall string, matchedEnd bool) {
	sl := strings.ToUpper(strings.TrimSpace(m.SL))
	rl := strings.ToUpper(strings.TrimSpace(m.RL))
	sc := strings.ToUpper(strings.TrimSpace(m.SC))
	rc := strings.ToUpper(strings.TrimSpace(m.RC))

	recvLoc, recvCall, otherLoc, otherCall := sl, sc, rl, rc
	if receiverSide == "rc" {
		recvLoc, recvCall, otherLoc, otherCall = rl, rc, sl, sc
	}

	if area != nil {
		if area.contains(recvLoc) {
			return otherLoc, otherCall, true
		}
		if area.contains(otherLoc) {
			return recvLoc, recvCall, true
		}
		return recvLoc, recvCall, false
	}

	for _, t := range qthSet {
		if matchCall(recvCall, t) || (isLocator(t) && recvLoc != "" && strings.HasPrefix(recvLoc, t)) {
			// Operator is the receiver → remote is the other end.
			return otherLoc, otherCall, true
		}
		if matchCall(otherCall, t) || (isLocator(t) && otherLoc != "" && strings.HasPrefix(otherLoc, t)) {
			// Operator is the remote end → remote is the receiver.
			return recvLoc, recvCall, true
		}
	}
	// Global mesh: key by the receiver side's region.
	return recvLoc, recvCall, false
}

// propIntelRemote is resolvePropIntelRemoteEndArea for the window scans: same
// matching, but the remote locator comes back as the trimmed feed string
// instead of an upper-cased copy (callers map it to a region through
// rawRegionMemo, which upper-cases once per distinct locator), and the remote
// callsign is only built on request (remoteCall). Locators are compared case-
// insensitively in place, so the common case allocates nothing.
//
// remoteIsRecv says whether the remote end is the receiver side (receiverSide
// "sc": SC/SL, "rc": RC/RL) or the other one.
func propIntelRemote(m *MQTTMessage, qthSet []string, area *liveArea, receiverSide string) (remoteLoc string, remoteIsRecv, matchedEnd bool) {
	sl := strings.TrimSpace(m.SL)
	rl := strings.TrimSpace(m.RL)
	recvLoc, otherLoc := sl, rl
	if receiverSide == "rc" {
		recvLoc, otherLoc = rl, sl
	}

	if area != nil {
		if area.contains(recvLoc) {
			return otherLoc, false, true
		}
		if area.contains(otherLoc) {
			return recvLoc, true, true
		}
		return recvLoc, true, false
	}

	var recvCall, otherCall string
	if len(qthSet) > 0 {
		recvCall = strings.ToUpper(strings.TrimSpace(m.SC))
		otherCall = strings.ToUpper(strings.TrimSpace(m.RC))
		if receiverSide == "rc" {
			recvCall, otherCall = otherCall, recvCall
		}
	}
	for _, t := range qthSet {
		if matchCall(recvCall, t) || (isLocator(t) && hasPrefixFold(recvLoc, t)) {
			return otherLoc, false, true
		}
		if matchCall(otherCall, t) || (isLocator(t) && hasPrefixFold(otherLoc, t)) {
			return recvLoc, true, true
		}
	}
	return recvLoc, true, false
}

// propRemoteMatcher is propIntelRemote with the per-scan work hoisted out of
// the per-message loop. For a locator qth the token set is a block of 4-char
// squares (own square, or 3x3 with surroundings); testing a message against
// 9 string tokens meant 18 matchCall plus 18 prefix comparisons, which made the
// scan ~250 ms on prod whenever the live area sat at radius 1 (the qthSet
// path). Here the squares are integer coordinates, compared in the original
// token order (when both ends of a spot are inside the block, the first token
// either end matches decides which end is the remote one), behind a bounding-
// box test that rejects spots with no end near the block.
//
// A callsign can never equal a grid square, so the callsign comparisons the
// token loop also made for locator tokens are dropped. qth sets that are not
// a plain list of 4-char squares (a callsign qth, longer locators, a mix) keep
// the original loop.
type propRemoteMatcher struct {
	area                   *liveArea
	qthSet                 []string
	sq                     [][2]int
	minX, maxX, minY, maxY int
	exact                  bool
}

func newPropRemoteMatcher(qthSet []string, area *liveArea) propRemoteMatcher {
	pm := propRemoteMatcher{area: area, qthSet: qthSet}
	if area != nil || len(qthSet) == 0 {
		return pm
	}
	sq := make([][2]int, 0, len(qthSet))
	for _, t := range qthSet {
		if len(t) != 4 || !isLocator(t) {
			return pm
		}
		x, y, _ := locatorSquareXY(t)
		sq = append(sq, [2]int{x, y})
	}
	pm.sq, pm.exact = sq, true
	pm.minX, pm.maxX, pm.minY, pm.maxY = sq[0][0], sq[0][0], sq[0][1], sq[0][1]
	for _, p := range sq[1:] {
		pm.minX, pm.maxX = min(pm.minX, p[0]), max(pm.maxX, p[0])
		pm.minY, pm.maxY = min(pm.minY, p[1]), max(pm.maxY, p[1])
	}
	return pm
}

func (pm *propRemoteMatcher) inBox(x, y int) bool {
	return x >= pm.minX && x <= pm.maxX && y >= pm.minY && y <= pm.maxY
}

// resolve is propIntelRemote(m, qthSet, area, receiverSide).
func (pm *propRemoteMatcher) resolve(m *MQTTMessage, receiverSide string) (remoteLoc string, remoteIsRecv, matchedEnd bool) {
	if !pm.exact {
		return propIntelRemote(m, pm.qthSet, pm.area, receiverSide)
	}
	sl := strings.TrimSpace(m.SL)
	rl := strings.TrimSpace(m.RL)
	recvLoc, otherLoc := sl, rl
	if receiverSide == "rc" {
		recvLoc, otherLoc = rl, sl
	}
	rx, ry, rok := locatorSquareXYFold(recvLoc)
	ox, oy, ook := locatorSquareXYFold(otherLoc)
	rok = rok && pm.inBox(rx, ry)
	ook = ook && pm.inBox(ox, oy)
	if !rok && !ook {
		return recvLoc, true, false
	}
	for _, t := range pm.sq {
		if rok && rx == t[0] && ry == t[1] {
			return otherLoc, false, true
		}
		if ook && ox == t[0] && oy == t[1] {
			return recvLoc, true, true
		}
	}
	return recvLoc, true, false
}

// propIntelRemoteCall is the upper-cased callsign of the end propIntelRemote
// picked as the remote one.
func propIntelRemoteCall(m *MQTTMessage, receiverSide string, remoteIsRecv bool) string {
	recvCall, otherCall := m.SC, m.RC
	if receiverSide == "rc" {
		recvCall, otherCall = otherCall, recvCall
	}
	if remoteIsRecv {
		return strings.ToUpper(strings.TrimSpace(recvCall))
	}
	return strings.ToUpper(strings.TrimSpace(otherCall))
}

// scanPropIntelV2Window walks the history window once and accumulates the
// per-(band, region, source) cells, the from-here flags and the bands seen.
func scanPropIntelV2Window(history []MQTTMessage, profiles []propIntelSourceProfile, qthSet []string, matchArea *liveArea, now, cutoff, midpoint int64) (map[propIntelV2CellKey]*propIntelV2Acc, map[propIntelCellKey]bool, map[string]struct{}) {
	accs, fromHere, bands, _ := scanPropIntelV2WindowMode(history, profiles, qthSet, matchArea, now, cutoff, midpoint, false)
	return accs, fromHere, bands
}

// scanPropIntelV2WindowMode is scanPropIntelV2Window with the from-here view:
// with fromHereOnly a spot counts once for each of its ends inside the area,
// in the cell of the OTHER end's region (fromHereRemoteEnds), and spots that
// do not touch the area are skipped. normalSpots then counts, per (band,
// region), the spots the area normal is built from (PSKReporter FT8/FT4 and
// DX cluster); nil otherwise.
func scanPropIntelV2WindowMode(history []MQTTMessage, profiles []propIntelSourceProfile, qthSet []string, matchArea *liveArea, now, cutoff, midpoint int64, fromHereOnly bool) (map[propIntelV2CellKey]*propIntelV2Acc, map[propIntelCellKey]bool, map[string]struct{}, map[propIntelCellKey]int) {
	// Per-message accumulation is indexed by (band, region, source) position
	// in flat arrays instead of string-keyed maps (three string hashes per
	// message); the maps the rest of the function reads are rebuilt below.
	// cellFromHere is per (band, region) because from-here is source-agnostic
	// at the rollup level.
	nBands, nRegions, nSources := len(inScopeBandNames), len(region.AllRegions()), len(profiles)
	accs := make([]*propIntelV2Acc, nBands*nRegions*nSources)
	var bandSeen [len(inScopeBandNames)]bool
	fromHere := make([]bool, nBands*nRegions)
	var normal []int
	var fhm fromHereMatcher
	if fromHereOnly {
		normal = make([]int, nBands*nRegions)
		fhm = newFromHereMatcher(qthSet, matchArea)
	}
	regions := newRawRegionMemo()
	pm := newPropRemoteMatcher(qthSet, matchArea)

	for i := range history {
		m := &history[i]
		if m.T > now || m.T < cutoff {
			continue
		}
		src := m.Source
		if src == "" {
			src = "mqtt" // MQTT ingest leaves Source empty (only wspr/rbn/dxc tag theirs)
		}
		si := -1
		for j := range profiles {
			if profiles[j].InternalTag == src {
				si = j
				break
			}
		}
		if si < 0 {
			continue
		}
		prof := &profiles[si]
		bi := feedBandIndex(m.B)
		if bi < 0 {
			continue
		}

		var ends [2]string
		nEnds := 0
		matchedEnd := true
		if fromHereOnly {
			a, b := fhm.ends(m, prof.ReceiverSide)
			if a != "" {
				ends[nEnds] = a
				nEnds++
			}
			if b != "" {
				ends[nEnds] = b
				nEnds++
			}
		} else {
			ends[0], _, matchedEnd = pm.resolve(m, prof.ReceiverSide)
			nEnds = 1
		}
		for k := 0; k < nEnds; k++ {
			ri := regions.get(ends[k])
			if ri < 0 {
				continue
			}

			ai := (bi*nRegions+ri)*nSources + si
			cell := accs[ai]
			if cell == nil {
				cell = &propIntelV2Acc{}
				accs[ai] = cell
			}
			cell.spotCount++
			bandSeen[bi] = true

			if prof.HasTXPower && m.TXPower > 0 {
				eff := float64(m.RP) + propIntelReferencePowerDbm - float64(m.TXPower)
				if !cell.hasPower || eff > cell.bestBudgetSNR {
					cell.bestBudgetSNR = eff
				}
				cell.hasPower = true
			}
			if prof.InternalTag == "mqtt" || prof.InternalTag == "rbn" {
				if !cell.hasReport || m.RP > cell.bestReportSNR {
					cell.bestReportSNR = m.RP
				}
				cell.hasReport = true
			}

			if matchedEnd {
				fromHere[bi*nRegions+ri] = true
			}
			if fromHereOnly && (src == "mqtt" || src == "dxcluster") {
				normal[bi*nRegions+ri]++
			}

			if m.T < midpoint {
				cell.firstHalfSpots++
			} else {
				cell.secondHalfSpots++
			}
		}
	}

	// cellAccs[cellKey] is per (band, region, source).
	cellAccs := make(map[propIntelV2CellKey]*propIntelV2Acc)
	cellFromHere := make(map[propIntelCellKey]bool)
	bandsSeen := make(map[string]struct{})
	var normalSpots map[propIntelCellKey]int
	if fromHereOnly {
		normalSpots = make(map[propIntelCellKey]int)
	}
	allRegions := region.AllRegions()
	for bi := 0; bi < nBands; bi++ {
		if bandSeen[bi] {
			bandsSeen[inScopeBandNames[bi]] = struct{}{}
		}
		for ri := 0; ri < nRegions; ri++ {
			if fromHere[bi*nRegions+ri] {
				cellFromHere[propIntelCellKey{band: inScopeBandNames[bi], region: string(allRegions[ri])}] = true
			}
			if fromHereOnly && normal[bi*nRegions+ri] > 0 {
				normalSpots[propIntelCellKey{band: inScopeBandNames[bi], region: string(allRegions[ri])}] = normal[bi*nRegions+ri]
			}
			for si := 0; si < nSources; si++ {
				if acc := accs[(bi*nRegions+ri)*nSources+si]; acc != nil {
					cellAccs[propIntelV2CellKey{band: inScopeBandNames[bi], region: string(allRegions[ri]), source: profiles[si].PublicName}] = acc
				}
			}
		}
	}
	return cellAccs, cellFromHere, bandsSeen, normalSpots
}

// propIntelV2Engine is stateless like v1's; the propBaseline singleton holds
// the climatology. Kept as a struct for symmetry and test fixtures.
type propIntelV2Engine struct{}

var propIntelV2 = &propIntelV2Engine{}

// EvaluateV2 computes the unified per-(band × region) nowcast for the given
// QTH, history window, and requested sources. Mirrors v1 Evaluate's
// windowing/rising/atypical math, generalized per source profile.
func (e *propIntelV2Engine) EvaluateV2(qth string, surroundings bool, minutes int, profiles []propIntelSourceProfile, ssbOverride, cwOverride *int, history []MQTTMessage, now int64, atypicalThreshold float64) propIntelV2Response {
	return e.EvaluateV2Area(qth, surroundings, minutes, profiles, ssbOverride, cwOverride, history, now, atypicalThreshold, nil)
}

// EvaluateV2Area is EvaluateV2 scoped to a live area (nil: the own square, or
// the 3×3 block with surroundings). As in EvaluateArea an area of radius ≤ 1 is
// that legacy block and only labels the response; radius ≥ 2 matches by
// square distance. Surge detection (atypical) is skipped for a widened area:
// the climatology it compares against is not scaled to a widened block, so its
// larger live counts would read as surges.
func (e *propIntelV2Engine) EvaluateV2Area(qth string, surroundings bool, minutes int, profiles []propIntelSourceProfile, ssbOverride, cwOverride *int, history []MQTTMessage, now int64, atypicalThreshold float64, area *liveArea) propIntelV2Response {
	return e.evaluateV2(qth, surroundings, minutes, profiles, ssbOverride, cwOverride, history, now, atypicalThreshold, area, nil)
}

// EvaluateV2FromHere is the from-here view (from_here=true): a spot counts once
// for each of its ends inside the area, in the cell of the OTHER end's region,
// exactly as dx_region_baseline_daily keys it (one count when both ends share a
// square). Spots that do not touch the area are ignored, so counts, open flags
// and rising describe paths from the operator's area only. With normals, cells
// carry expected / expected_spots and the surge z-score compares the live
// PSKReporter count with the area's normal; without, there is no atypical (the
// global climatology does not describe paths from one area).
func (e *propIntelV2Engine) EvaluateV2FromHere(qth string, surroundings bool, minutes int, profiles []propIntelSourceProfile, ssbOverride, cwOverride *int, history []MQTTMessage, now int64, atypicalThreshold float64, area *liveArea, normals *fromHereNormals) propIntelV2Response {
	resp := e.evaluateV2(qth, surroundings, minutes, profiles, ssbOverride, cwOverride, history, now, atypicalThreshold, area,
		&propIntelV2FromHere{normals: normals})
	resp.FromHere = true
	return resp
}

func (e *propIntelV2Engine) evaluateV2(qth string, surroundings bool, minutes int, profiles []propIntelSourceProfile, ssbOverride, cwOverride *int, history []MQTTMessage, now int64, atypicalThreshold float64, area *liveArea, fh *propIntelV2FromHere) propIntelV2Response {
	qth = normalizeQTHToken(qth)
	if len(profiles) == 0 {
		profiles = propIntelSourceProfiles
	}

	resp := propIntelV2Response{
		Area:        area,
		QTH:         qth,
		Minutes:     minutes,
		Now:         now,
		Bands:       []string{},
		BandOrder:   append([]string(nil), propIntelBandOrder...),
		Regions:     allRegionStrings(),
		RegionNames: regionDisplayNames(),
		Cells:       []propIntelV2Cell{},
	}
	for _, p := range profiles {
		resp.SourcesRequested = append(resp.SourcesRequested, p.PublicName)
	}

	if qth == "" {
		return resp
	}

	var matchArea *liveArea
	if area != nil && area.Radius > 1 {
		matchArea = area
	}
	if area != nil && matchArea == nil {
		surroundings = area.Radius == 1
	}
	qthSet := qthSquares(qth, surroundings)

	cutoff := now - int64(minutes)*60
	midpoint := cutoff + (now-cutoff)/2

	cellAccs, cellFromHere, bandsSeen, normalSpots := scanPropIntelV2WindowMode(history, profiles, qthSet, matchArea, now, cutoff, midpoint, fh != nil)

	resp.Bands = inScopeBandsOrdered(bandsSeen)
	slot := utcSlotOfDay(now)

	// Load the unified climatology once; key by (band × source × region × slot).
	type climKey struct {
		band, source, region string
		slot                 int
	}
	clim := map[climKey]propRegionCalendarStatRow{}
	if propBaseline != nil && fh == nil {
		ctx, cancel := context.WithTimeout(context.Background(), propIntelRegionBaselineQueryTimeout)
		rows := propBaseline.RegionCalendarStats(ctx, propIntelRegionBaselineDaysBack, now)
		cancel()
		clim = make(map[climKey]propRegionCalendarStatRow, len(rows))
		for _, r := range rows {
			clim[climKey{r.Band, r.Source, r.Region, r.SlotOfDay}] = r
		}
	}

	// Group source accumulators into cells.
	byCell := make(map[propIntelCellKey][]propIntelV2SourceCell)
	pskrSelected := false
	for _, p := range profiles {
		if p.InternalTag == "mqtt" {
			pskrSelected = true
		}
	}

	for ck, acc := range cellAccs {
		prof := propIntelProfilesByName[ck.source]
		sc := propIntelV2SourceCell{
			Source:    ck.source,
			SpotCount: acc.spotCount,
			OpenBasis: openBasisForProfile(prof),
		}

		// Global Min SNR overrides (ssb_min_db/cw_min_db query params) swap the
		// per-source profile floor for the operator's own threshold — one
		// control gates every SNR-floored source. Presence-only sources
		// (dxcluster) have no SNR to filter.
		ssbFloor, cwFloor := prof.SSBFloorDb, prof.CWFloorDb
		if ssbOverride != nil && ssbFloor != nil {
			v := float64(*ssbOverride)
			ssbFloor = &v
		}
		if cwOverride != nil && cwFloor != nil {
			v := float64(*cwOverride)
			cwFloor = &v
		}

		// Open flags per profile basis (see prop_intel_sources.go).
		switch {
		case prof.HasTXPower:
			// wspr: budget model, identical math to v1.
			ssb := acc.hasPower && acc.bestBudgetSNR >= *ssbFloor
			cw := acc.hasPower && acc.bestBudgetSNR >= *cwFloor
			sc.SSBOpen = &ssb
			sc.CWOpen = &cw
			sc.Open = ssb || cw
		case prof.PresenceMinSpots > 0:
			// dxcluster: presence-only.
			sc.Open = acc.spotCount >= prof.PresenceMinSpots
		default:
			// pskr/rbn: SNR floors. Unknown power → honesty flag.
			if ssbFloor != nil {
				ssb := acc.hasReport && float64(acc.bestReportSNR) >= *ssbFloor
				sc.SSBOpen = &ssb
			}
			if cwFloor != nil {
				cw := acc.hasReport && float64(acc.bestReportSNR) >= *cwFloor
				sc.CWOpen = &cw
			}
			digital := prof.DigitalFloorDb != nil && acc.hasReport && float64(acc.bestReportSNR) >= *prof.DigitalFloorDb
			sc.Open = (sc.SSBOpen != nil && *sc.SSBOpen) || (sc.CWOpen != nil && *sc.CWOpen) || digital
			if !prof.HasTXPower {
				sc.UnknownPower = true
			}
		}

		// Rising: identical to v1.
		if acc.firstHalfSpots > 0 {
			sc.Rising = float64(acc.secondHalfSpots)/float64(acc.firstHalfSpots) >= propIntelRisingSlopeThreshold
		} else if acc.secondHalfSpots > 0 {
			sc.Rising = true
		}

		// Atypical z-score against this source's climatology (not for a
		// widened area, see EvaluateV2Area).
		if base, ok := clim[climKey{ck.band, ck.source, ck.region, slot}]; ok && (area == nil || !area.Widened) {
			sc.SampleDays = base.SampleDays
			if base.StdDev > 0 && base.SampleDays >= propIntelMinSampleDays {
				liveRate := float64(acc.spotCount) * (30.0 / float64(minutes))
				z := (liveRate - base.Mean) / base.StdDev
				if z >= atypicalThreshold {
					sc.Atypical = &propIntelV2Atypical{
						ZScore:     round3(z),
						Confidence: round3(atypicalConfidence(base.SampleDays)),
					}
				}
			}
		}

		cellKey := propIntelCellKey{band: ck.band, region: ck.region}
		byCell[cellKey] = append(byCell[cellKey], sc)
	}

	cells := make([]propIntelV2Cell, 0, len(byCell))
	for cellKey, sources := range byCell {
		// Canonical source order within the cell.
		sort.Slice(sources, func(i, j int) bool {
			return sourceOrderIndex(sources[i].Source) < sourceOrderIndex(sources[j].Source)
		})

		cell := propIntelV2Cell{
			Band:          cellKey.band,
			Region:        cellKey.region,
			FromHere:      cellFromHere[cellKey],
			PerSource:     sources,
			ActiveSources: []string{},
		}
		var openCount, atypicalCount int
		for _, sc := range sources {
			cell.SpotCount += sc.SpotCount
			cell.ActiveSources = append(cell.ActiveSources, sc.Source)
			if sc.Open {
				cell.Open = true
				openCount++
			}
			if sc.SSBOpen != nil && *sc.SSBOpen {
				cell.SSBOpen = true
			}
			if sc.CWOpen != nil && *sc.CWOpen {
				cell.CWOpen = true
			}
			if sc.Rising {
				cell.Rising = true
			}
			if sc.Atypical != nil {
				atypicalCount++
				if cell.Atypical == nil ||
					sc.Atypical.Confidence > cell.Atypical.Confidence ||
					(sc.Atypical.Confidence == cell.Atypical.Confidence && sc.Atypical.ZScore > cell.Atypical.ZScore) {
					cp := *sc.Atypical
					cell.Atypical = &cp
				}
			}
		}
		if len(sources) > 0 {
			cell.OpenAgreement = round2(float64(openCount) / float64(len(sources)))
			if atypicalCount > 0 {
				cell.AtypicalAgreement = round2(float64(atypicalCount) / float64(len(sources)))
			}
		}
		if fh != nil && pskrSelected {
			applyFromHereNormal(&cell, fh.normals, normalSpots[cellKey], atypicalThreshold)
		}
		cells = append(cells, cell)
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

// sourceOrderIndex returns the canonical position of a source's public name.
func sourceOrderIndex(name string) int {
	for i, p := range propIntelSourceProfiles {
		if p.PublicName == name {
			return i
		}
	}
	return len(propIntelSourceProfiles)
}

// openBasisForProfile documents the open-flag model in the response.
func openBasisForProfile(p propIntelSourceProfile) string {
	switch {
	case p.HasTXPower:
		return "budget"
	case p.PresenceMinSpots > 0:
		return "presence"
	default:
		return "snr_floor"
	}
}

// applyFromHere stamps from_here and filters to from-here cells (v2 twin of
// the v1 method; v1 stays frozen).
func (resp propIntelV2Response) applyFromHere(fromHere bool) propIntelV2Response {
	resp.FromHere = fromHere
	if !fromHere {
		return resp
	}
	filtered := make([]propIntelV2Cell, 0, len(resp.Cells))
	for _, c := range resp.Cells {
		if c.FromHere {
			filtered = append(filtered, c)
		}
	}
	resp.Cells = filtered
	return resp
}

// propIntelV2Handler serves /api/prop_intel/v2.
func propIntelV2Handler(w http.ResponseWriter, r *http.Request) {
	propIntelAccounting.requests.Add(1)
	p, ok := parsePropIntelParams(r)
	if !ok {
		propIntelAccounting.errors.Add(1)
		http.Error(w, "qth required", http.StatusBadRequest)
		return
	}
	profiles := parseSourcesParam(r)

	now := time.Now().Unix()
	area := liveAreaForRequest(r, p.qth, p.surroundings, now)
	historyCopy, release := snapshotPropIntelHistory(now, p.minutes)
	defer release()

	var resp propIntelV2Response
	if p.fromHere {
		// From-here view: only spots touching the area, compared with the
		// area's own normal (prop_intel_fromhere.go).
		normals := fromHereNormalsFor(fromHereAreaGrids(p.qth, p.surroundings, area), now, p.minutes)
		resp = propIntelV2.EvaluateV2FromHere(p.qth, p.surroundings, p.minutes, profiles, p.ssbOverride, p.cwOverride, historyCopy, now, p.atypicalThreshold, area, normals)
	} else {
		resp = propIntelV2.EvaluateV2Area(p.qth, p.surroundings, p.minutes, profiles, p.ssbOverride, p.cwOverride, historyCopy, now, p.atypicalThreshold, area)
	}
	resp = resp.applyFromHere(p.fromHere)

	// Push fan-out: adapt v2 cells to the v1 push payload (push.go consumes
	// []propIntelCell; the flavor string is unused by the payload).
	hasAtypical := false
	var pushCells []propIntelCell
	for _, c := range resp.Cells {
		if c.Atypical == nil {
			continue
		}
		hasAtypical = true
		flavor := "atypical-wspr-only"
		if c.AtypicalAgreement >= 0.5 {
			flavor = "atypical-multi"
		}
		pushCells = append(pushCells, propIntelCell{
			Band:     c.Band,
			Region:   c.Region,
			SSBOpen:  c.SSBOpen,
			CWOpen:   c.CWOpen,
			FromHere: c.FromHere,
			Atypical: &AtypicalInfo{
				ZScore:     c.Atypical.ZScore,
				Confidence: c.Atypical.Confidence,
				Flavor:     flavor,
			},
		})
	}
	if hasAtypical {
		propIntelAccounting.surgesDetected.Add(1)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logInfo("push goroutine panic: %v", r)
				}
			}()
			pushStore.NotifySurges(pushCells, p.qth)
		}()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
