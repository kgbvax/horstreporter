package main

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"horstreporter/internal/region"
)

// prop_intel_fromhere_detail.go: optional detail for the from-here view of
// /api/prop_intel/v2, used by the experimental matrix looks (dot strip, day
// strip, sparkline). All of it is opt-in per query parameter, so the default
// response is unchanged:
//
//   - silent=1: cells with no live spots whose normal for this hour is at
//     least fromHereSilentMinExpected ("usually open at this hour, nothing
//     now").
//   - normal_day=1: per cell, the area normal over the whole UTC day, one
//     mean per 30-minute slot (normal_day).
//   - trend=1: per cell, the last hour of the spots the normal is built from,
//     in five-minute bins (trend).

const (
	// fromHereSilentMinExpected is the smallest normal worth a silent cell:
	// P(0 | mean 5) is under 1%.
	fromHereSilentMinExpected = 5.0
	// fromHereTrendBins × fromHereTrendBinSec is the trend span: one hour.
	fromHereTrendBins   = 12
	fromHereTrendBinSec = 300
	// fromHereDayQueryTimeout bounds the day-curve query: it reads every slot
	// of the reference period (a few seconds warm, up to ~20 s cold on prod).
	fromHereDayQueryTimeout = 60 * time.Second
	// fromHereDayWait is how long a request waits for the day curves after the
	// window normals; a slower fetch fills the cache for the next poll.
	fromHereDayWait = 300 * time.Millisecond
)

// fromHereSlotSum is one (band, region, slot) count summed over the area's
// squares and the reference days with PSKReporter ingest in that slot.
type fromHereSlotSum struct {
	Band   string
	Region string
	Slot   int
	Sum    int64
}

// fromHereDaySums is one day-curve fetch: the sums and, per slot, how many
// reference days had ingest coverage.
type fromHereDaySums struct {
	sums    []fromHereSlotSum
	covered map[int]int
}

// fromHereDayCurves maps a cell to its 48 slot means.
type fromHereDayCurves struct {
	ByCell map[propIntelCellKey][]float64
}

// computeFromHereDayCurves turns slot sums into per-slot means (one decimal).
// A slot without a covered reference day reads 0.
func computeFromHereDayCurves(sums []fromHereSlotSum, covered map[int]int) *fromHereDayCurves {
	out := &fromHereDayCurves{ByCell: map[propIntelCellKey][]float64{}}
	for _, s := range sums {
		if s.Slot < 0 || s.Slot >= almanacSeasonSlotsPerDay || !bandInScope(s.Band) {
			continue
		}
		n := covered[s.Slot]
		if n <= 0 {
			continue
		}
		key := propIntelCellKey{band: s.Band, region: s.Region}
		c := out.ByCell[key]
		if c == nil {
			c = make([]float64, almanacSeasonSlotsPerDay)
			out.ByCell[key] = c
		}
		c[s.Slot] = round1(float64(s.Sum) / float64(n))
	}
	return out
}

const fromHereDaySumsSQL = `
	SELECT b.band, b.region, b.slot_of_day, SUM(b.spot_count)::bigint
	FROM dx_region_baseline_daily b
	JOIN almanac_ingest_slots c
	  ON c.day_index = b.day_index AND c.slot_of_day = b.slot_of_day
	 AND c.layer = $1 AND c.spot_total > 0
	WHERE b.target_grid4 = ANY($2::text[])
	  AND b.band = ANY($3::text[])
	  AND b.day_index BETWEEN $4 AND $5
	GROUP BY b.band, b.region, b.slot_of_day`

const fromHereDayCoverageSQL = `
	SELECT slot_of_day, COUNT(*)::int
	FROM almanac_ingest_slots
	WHERE layer = $1
	  AND day_index BETWEEN $2 AND $3
	  AND spot_total > 0
	GROUP BY slot_of_day`

// FromHereDaySums reads the area-summed counts of every slot over the
// reference days with PSKReporter ingest, and the covered days per slot.
func (s *dxPostgresStore) FromHereDaySums(ctx context.Context, grids, bands []string, dayFrom, dayTo int64) ([]fromHereSlotSum, map[int]int, error) {
	if s == nil || s.pool == nil {
		return nil, nil, errNoFromHereStore
	}
	rows, err := s.pool.Query(ctx, fromHereDaySumsSQL, almanacSeasonLayerPSKR, grids, bands, dayFrom, dayTo)
	if err != nil {
		return nil, nil, err
	}
	var out []fromHereSlotSum
	for rows.Next() {
		var r fromHereSlotSum
		if err := rows.Scan(&r.Band, &r.Region, &r.Slot, &r.Sum); err != nil {
			rows.Close()
			return nil, nil, err
		}
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	covered := map[int]int{}
	crows, err := s.pool.Query(ctx, fromHereDayCoverageSQL, almanacSeasonLayerPSKR, dayFrom, dayTo)
	if err != nil {
		return nil, nil, err
	}
	defer crows.Close()
	for crows.Next() {
		var slot, n int
		if err := crows.Scan(&slot, &n); err != nil {
			return nil, nil, err
		}
		covered[slot] = n
	}
	return out, covered, crows.Err()
}

// fromHereDaySumsSource is what the day-curve cache reads from.
type fromHereDaySumsSource interface {
	FromHereDaySums(ctx context.Context, grids, bands []string, dayFrom, dayTo int64) ([]fromHereSlotSum, map[int]int, error)
}

// fromHereDaySource returns the production source, nil without Postgres.
var fromHereDaySource = func() fromHereDaySumsSource {
	if dxBaseline == nil {
		return nil
	}
	dxBaseline.mu.RLock()
	st := dxBaseline.store
	dxBaseline.mu.RUnlock()
	if st == nil {
		return nil
	}
	return st
}

var fromHereDayCache = newAsyncCache[fromHereDaySums](fromHereCacheCap, fromHereDayQueryTimeout, "from-here day curves")

// fromHereDayCurvesFor returns the area's day curves, or nil when there is no
// store or area, or the fetch is not done within wait (it then completes in
// the background; one fetch per area and UTC day).
func fromHereDayCurvesFor(grids []string, now int64, wait time.Duration) *fromHereDayCurves {
	src := fromHereDaySource()
	if src == nil || len(grids) == 0 {
		return nil
	}
	sortedGrids := append([]string(nil), grids...)
	sort.Strings(sortedGrids)
	today := utcDayIndex(now)
	key := strings.Join(sortedGrids, ",") + "|" + strconv.FormatInt(today, 10)
	bands := append([]string(nil), inScopeBandNames[:]...)
	e := fromHereDayCache.get(key, func(ctx context.Context) (fromHereDaySums, error) {
		sums, covered, err := src.FromHereDaySums(ctx, sortedGrids, bands, today-fromHereNormalDays, today-1)
		return fromHereDaySums{sums: sums, covered: covered}, err
	})
	if !e.wait(wait) {
		return nil
	}
	return computeFromHereDayCurves(e.val.sums, e.val.covered)
}

// fromHereTrend counts, per (band, far-end region), the spots the area normal
// is built from (selected PSKReporter FT8/FT4 and DX-cluster spots, once per
// end inside the area, as scanPropIntelV2WindowMode counts ExpectedSpots) in
// fromHereTrendBins five-minute bins ending at now, oldest first. The last
// three bins are the 15-minute window.
func fromHereTrend(history []MQTTMessage, profiles []propIntelSourceProfile, qthSet []string, area *liveArea, now int64) map[propIntelCellKey][]int {
	var mqttSide, dxcSide string
	var wantMQTT, wantDXC bool
	for _, p := range profiles {
		switch p.InternalTag {
		case "mqtt":
			wantMQTT, mqttSide = true, p.ReceiverSide
		case "dxcluster":
			wantDXC, dxcSide = true, p.ReceiverSide
		}
	}
	out := map[propIntelCellKey][]int{}
	if !wantMQTT && !wantDXC {
		return out
	}
	start := now - fromHereTrendBins*fromHereTrendBinSec
	nRegions := len(region.AllRegions())
	counts := make([]int, len(inScopeBandNames)*nRegions*fromHereTrendBins)
	fhm := newFromHereMatcher(qthSet, area)
	regions := newRawRegionMemo()
	for i := range history {
		m := &history[i]
		if m.T > now || m.T < start {
			continue
		}
		var side string
		switch m.Source {
		case "", "mqtt":
			if !wantMQTT {
				continue
			}
			side = mqttSide
		case "dxcluster":
			if !wantDXC {
				continue
			}
			side = dxcSide
		default:
			continue
		}
		bi := feedBandIndex(m.B)
		if bi < 0 {
			continue
		}
		bin := int((m.T - start) / fromHereTrendBinSec)
		if bin >= fromHereTrendBins {
			bin = fromHereTrendBins - 1
		}
		a, b := fhm.ends(m, side)
		for _, end := range [2]string{a, b} {
			if end == "" {
				continue
			}
			if ri := regions.get(end); ri >= 0 {
				counts[(bi*nRegions+ri)*fromHereTrendBins+bin]++
			}
		}
	}
	allRegions := region.AllRegions()
	for bi, band := range inScopeBandNames {
		for ri := 0; ri < nRegions; ri++ {
			c := counts[(bi*nRegions+ri)*fromHereTrendBins : (bi*nRegions+ri+1)*fromHereTrendBins]
			for _, v := range c {
				if v > 0 {
					out[propIntelCellKey{band: band, region: string(allRegions[ri])}] = append([]int(nil), c...)
					break
				}
			}
		}
	}
	return out
}

// fromHereSilentCells builds the silent cells: band × far-end regions the area
// normally reaches at this hour (normal ≥ fromHereSilentMinExpected) that have
// no live from-here spots at all.
func fromHereSilentCells(normals *fromHereNormals, live map[propIntelCellKey][]propIntelV2SourceCell) []propIntelV2Cell {
	if normals == nil || normals.SampleDays < propIntelMinSampleDays {
		return nil
	}
	validRegion := make(map[string]bool)
	for _, r := range allRegionStrings() {
		validRegion[r] = true
	}
	var out []propIntelV2Cell
	for key, nrm := range normals.ByCell {
		if _, has := live[key]; has || nrm.Expected < fromHereSilentMinExpected {
			continue
		}
		if !bandInScope(key.band) || !validRegion[key.region] {
			continue
		}
		exp := round1(nrm.Expected)
		zero := 0
		out = append(out, propIntelV2Cell{
			Band:          key.band,
			Region:        key.region,
			FromHere:      true,
			ActiveSources: []string{},
			PerSource:     []propIntelV2SourceCell{},
			Expected:      &exp,
			ExpectedSpots: &zero,
			Silent:        true,
		})
	}
	return out
}

// applyFromHereDetail attaches the requested day curve and trend to a
// from-here cell. A requested trend is always 12 bins (zeros when quiet).
func applyFromHereDetail(cell *propIntelV2Cell, fh *propIntelV2FromHere, trend map[propIntelCellKey][]int) {
	key := propIntelCellKey{band: cell.Band, region: cell.Region}
	if fh.dayCurves != nil {
		cell.NormalDay = fh.dayCurves.ByCell[key]
	}
	if fh.trend {
		if t, ok := trend[key]; ok {
			cell.Trend = t
		} else {
			cell.Trend = make([]int, fromHereTrendBins)
		}
	}
}
