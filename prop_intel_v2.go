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
}

// propIntelV2Response is the JSON envelope for /api/prop_intel/v2.
type propIntelV2Response struct {
	QTH      string `json:"qth"`
	Minutes  int    `json:"minutes"`
	Now      int64  `json:"now"`
	FromHere bool   `json:"from_here"`
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
	sl := strings.ToUpper(strings.TrimSpace(m.SL))
	rl := strings.ToUpper(strings.TrimSpace(m.RL))
	sc := strings.ToUpper(strings.TrimSpace(m.SC))
	rc := strings.ToUpper(strings.TrimSpace(m.RC))

	recvLoc, recvCall, otherLoc, otherCall := sl, sc, rl, rc
	if receiverSide == "rc" {
		recvLoc, recvCall, otherLoc, otherCall = rl, rc, sl, sc
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

// propIntelV2Engine is stateless like v1's; the propBaseline singleton holds
// the climatology. Kept as a struct for symmetry and test fixtures.
type propIntelV2Engine struct{}

var propIntelV2 = &propIntelV2Engine{}

// EvaluateV2 computes the unified per-(band × region) nowcast for the given
// QTH, history window, and requested sources. Mirrors v1 Evaluate's
// windowing/rising/atypical math, generalized per source profile.
func (e *propIntelV2Engine) EvaluateV2(qth string, surroundings bool, minutes int, profiles []propIntelSourceProfile, ssbOverride, cwOverride *int, history []MQTTMessage, now int64, atypicalThreshold float64) propIntelV2Response {
	qth = normalizeQTHToken(qth)
	if len(profiles) == 0 {
		profiles = propIntelSourceProfiles
	}

	resp := propIntelV2Response{
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

	qthSet := []string{qth}
	if surroundings && isLocator(qth) {
		qthSet = getSurroundingSquares(qth)
	}

	cutoff := now - int64(minutes)*60
	midpoint := cutoff + (now-cutoff)/2

	profByTag := make(map[string]propIntelSourceProfile, len(profiles))
	for _, p := range profiles {
		profByTag[p.InternalTag] = p
	}

	// cellAccs[cellKey] is per (band, region, source); cellFromHere is per
	// (band, region) because from-here is source-agnostic at the rollup level.
	cellAccs := make(map[propIntelV2CellKey]*propIntelV2Acc)
	cellFromHere := make(map[propIntelCellKey]bool)
	bandsSeen := make(map[string]struct{})

	for _, m := range history {
		if m.T > now || m.T < cutoff {
			continue
		}
		src := m.Source
		if src == "" {
			src = "mqtt" // MQTT ingest leaves Source empty (only wspr/rbn/dxc tag theirs)
		}
		prof, ok := profByTag[src]
		if !ok {
			continue
		}
		band := normalizeBand(m.B)
		if band == "" || !bandInScope(band) {
			continue
		}

		remoteLocator, _, matchedEnd := resolvePropIntelRemoteEnd(m, qthSet, prof.ReceiverSide)
		if remoteLocator == "" || !isLocator(remoteLocator) {
			continue
		}
		reg := region.FromLocator(remoteLocator)
		if reg == region.Unknown {
			continue
		}

		regionCode := string(reg)
		ck := propIntelV2CellKey{band: band, region: regionCode, source: prof.PublicName}
		cell := cellAccs[ck]
		if cell == nil {
			cell = &propIntelV2Acc{}
			cellAccs[ck] = cell
		}
		cell.spotCount++
		bandsSeen[band] = struct{}{}

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
			cellFromHere[propIntelCellKey{band: band, region: regionCode}] = true
		}

		if m.T < midpoint {
			cell.firstHalfSpots++
		} else {
			cell.secondHalfSpots++
		}
	}

	resp.Bands = inScopeBandsOrdered(bandsSeen)
	slot := utcSlotOfDay(now)

	// Load the unified climatology once; key by (band × source × region × slot).
	type climKey struct {
		band, source, region string
		slot                 int
	}
	clim := map[climKey]propRegionCalendarStatRow{}
	if propBaseline != nil {
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

		// Atypical z-score against this source's climatology.
		if base, ok := clim[climKey{ck.band, ck.source, ck.region, slot}]; ok {
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
	historyCopy, release := snapshotPropIntelHistory(now, p.minutes)
	defer release()

	resp := propIntelV2.EvaluateV2(p.qth, p.surroundings, p.minutes, profiles, p.ssbOverride, p.cwOverride, historyCopy, now, p.atypicalThreshold)
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
