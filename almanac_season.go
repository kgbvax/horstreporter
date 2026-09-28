package main

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// almanac_season.go serves GET /api/almanac/season (plan U4, R9/R10/R13,
// KTD7): the month × hour drill-down for one (band, region) of the operator's
// area. For each calendar month it shows the most recent year with at least
// M_min (almanacMinActiveDaysSeasonal) active days, PSKR/cluster first and
// the backfilled WSPR layer as fallback, labelled with year and layer.
//
// One read-only REPEATABLE READ transaction (almanacReadStore, U2) reads the
// fold watermark W, the PSKR seasonal rows (days ≤ W), the PSKR daily tail
// (days > W, up to yesterday), the WSPR seasonal rows, both layers' ingest
// totals and the lost days — so a fold committing meanwhile can't make a day
// count twice or go missing.
//
// Cell rule per layer (same as the 30-day view, per calendar month):
//
//	alive(d,s)  = layer ingest total of (d,s) > 0 and ≥ 10% of slot s's median
//	              over the month's days (PSKR lost days are never alive);
//	              WSPR uses its own ingest totals (layer "wspr")
//	active(d,s) = ≥1 spot on the band to any region in slot s or s+1
//	M[s]        = |{d : alive ∧ active}|,  N[s] = those days with count ≥ k
//	days        = |{d : ∃s alive(d,s) ∧ active(d,s)}|  — the M_min test
//	k           = 2 (PSKR), 1 (WSPR)
//
// Radius: the drill-down uses the landing view's radius. getSeason first
// obtains the /api/almanac typical part for the same centre grid4 (cached,
// KTD3 chooseAlmanacRadius over the 30-day PSKR active-day masks) and reads
// the squares at ring level ≤ that radius; a season cache entry is dropped
// when the landing radius changes, so lanes and drill-down always agree.
//
// The sparse seasonal rows (almanac_sparse.go) are fetched for all regions of
// the band (activity is "to any region") and decoded in Go into fixed
// per-month arrays, visiting only non-zero cells; at most
// squares(≤25) × 11 regions × 60 months × 2 layers rows are streamed.

var almanacMonthNames = [12]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

// almanacSeasonMonth is one calendar-month row. Year == 0 means no layer has
// M_min days for this month ("not collected yet").
type almanacSeasonMonth struct {
	Month int // 1–12
	Year  int
	Layer string
	Days  int
	K     int
	N, M  [almanacSlotsPerDay]uint8
}

// almanacSeason is one drill-down result (the cached unit).
type almanacSeason struct {
	Centre    string
	Band      string
	Region    string
	Radius    int
	Squares   []string
	Watermark int64 // as read in the transaction; -1 when none
	Today     int64 // UTC day index the result was computed for
	Months    []almanacSeasonMonth
}

// ---------------------------------------------------------------------------
// Year-month helpers
// ---------------------------------------------------------------------------

func almanacYMAdd(ym, delta int) int {
	idx := (ym/100)*12 + (ym%100 - 1) + delta
	return (idx/12)*100 + idx%12 + 1
}

func almanacYMFirstDay(ym int) int64 {
	return utcDayIndex(time.Date(ym/100, time.Month(ym%100), 1, 0, 0, 0, 0, time.UTC).Unix())
}

func almanacYMDays(ym int) int {
	return time.Date(ym/100, time.Month(ym%100)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// ---------------------------------------------------------------------------
// Accumulation
// ---------------------------------------------------------------------------

// almanacSeasonMonthAcc holds one (layer, year_month) reduced to the target
// region's per-slot sums, the any-region activity and the ingest totals.
type almanacSeasonMonthAcc struct {
	cnt    [almanacSeasonDaysPerMonth][almanacSlotsPerDay]uint16
	act    [almanacSeasonDaysPerMonth][almanacSlotsPerDay]bool
	ingest [almanacSeasonDaysPerMonth][almanacSlotsPerDay]int64
	lost   [almanacSeasonDaysPerMonth]bool
}

type almanacSeasonAccum struct {
	region    string
	yesterday int64
	layers    map[string]map[int]*almanacSeasonMonthAcc
}

func (a *almanacSeasonAccum) month(layer string, ym int, create bool) *almanacSeasonMonthAcc {
	lm := a.layers[layer]
	if lm == nil {
		if !create {
			return nil
		}
		lm = map[int]*almanacSeasonMonthAcc{}
		a.layers[layer] = lm
	}
	m := lm[ym]
	if m == nil && create {
		m = &almanacSeasonMonthAcc{}
		lm[ym] = m
	}
	return m
}

func (a *almanacSeasonAccum) addSlot(layer string, day int64, slot int, reg string, v int64) {
	if v <= 0 || day > a.yesterday || slot < 0 || slot >= almanacSlotsPerDay {
		return
	}
	ym, dom := almanacYearMonthDOM(day)
	m := a.month(layer, ym, true)
	m.act[dom-1][slot] = true
	if reg == a.region {
		satAdd16(&m.cnt[dom-1][slot], v)
	}
}

// seasonRow returns a seasonCounts callback for layer that decodes the days
// ≤ maxDay (and ≤ yesterday, as addSlot) of each sparse row, visiting only
// its non-zero cells. The row's month accumulator is created on its first
// counted cell, so a month exists only if it has data. A malformed row is
// treated as absent.
func (a *almanacSeasonAccum) seasonRow(layer string, maxDay int64) func(grid, band, reg string, ym int, counts []byte) {
	return func(grid, band, reg string, ym int, counts []byte) {
		first := almanacYMFirstDay(ym)
		days := almanacYMDays(ym)
		monthYM, _ := almanacYearMonthDOM(first) // == ym for a valid ym
		target := reg == a.region
		// Last counted day of month (1-based), clamped to the month length.
		lastDOM := min(int64(days), min(maxDay, a.yesterday)-first+1)
		if lastDOM < 1 {
			return
		}
		limit := int(lastDOM) * almanacSlotsPerDay // pos < limit
		var m *almanacSeasonMonthAcc
		err := almanacSparseEach(counts, func(pos, v int) {
			if pos >= limit {
				return
			}
			if m == nil {
				m = a.month(layer, monthYM, true)
			}
			d, s := pos/almanacSlotsPerDay, pos%almanacSlotsPerDay
			m.act[d][s] = true
			if target {
				satAdd16(&m.cnt[d][s], int64(v))
			}
		})
		if err != nil {
			almanacSparseMalformed("almanac season read", grid, band, reg, ym, err)
		}
	}
}

// firstDay is the first day of the oldest month holding data for layer.
func (a *almanacSeasonAccum) firstDay(layer string) (int64, bool) {
	minYM := 0
	for ym := range a.layers[layer] {
		if minYM == 0 || ym < minYM {
			minYM = ym
		}
	}
	if minYM == 0 {
		return 0, false
	}
	return almanacYMFirstDay(minYM), true
}

func (a *almanacSeasonAccum) ingestRow(layer string) func(day int64, slot int, total int64) {
	return func(day int64, slot int, total int64) {
		if day > a.yesterday || slot < 0 || slot >= almanacSlotsPerDay {
			return
		}
		ym, dom := almanacYearMonthDOM(day)
		if m := a.month(layer, ym, false); m != nil {
			m.ingest[dom-1][slot] += total
		}
	}
}

func (a *almanacSeasonAccum) lostDay(layer string) func(day int64) {
	return func(day int64) {
		ym, dom := almanacYearMonthDOM(day)
		if m := a.month(layer, ym, false); m != nil {
			m.lost[dom-1] = true
		}
	}
}

// compute applies the alive / activity / k rules to one (layer, ym).
func (a *almanacSeasonAccum) compute(layer string, ym int, k int) almanacSeasonMonth {
	m := a.month(layer, ym, false)
	out := almanacSeasonMonth{Month: ym % 100, Year: ym / 100, Layer: layer, K: k}
	if m == nil {
		return out
	}
	lastDom := almanacYMDays(ym)
	if yYM, yDom := almanacYearMonthDOM(a.yesterday); yYM == ym {
		lastDom = yDom
	}
	// nextDay[d] is the day after d (slot 0 feeds "active in s+1" of slot 47):
	// the month's next day, else the next month's first day when present.
	var nextDay [almanacSeasonDaysPerMonth]*[almanacSlotsPerDay]bool
	for d := 0; d+1 < lastDom; d++ {
		nextDay[d] = &m.act[d+1]
	}
	if next := a.month(layer, almanacYMAdd(ym, 1), false); next != nil {
		nextDay[lastDom-1] = &next.act[0]
	}
	var dayOK [almanacSeasonDaysPerMonth]bool
	col := make([]int64, lastDom)
	for s := 0; s < almanacSlotsPerDay; s++ {
		for d := 0; d < lastDom; d++ {
			col[d] = m.ingest[d][s]
		}
		median := almanacAliveMedian(col)
		for d := 0; d < lastDom; d++ {
			if !almanacIsAlive(m.ingest[d][s], m.lost[d], median) || !almanacActiveOrNext(&m.act[d], nextDay[d], s) {
				continue
			}
			dayOK[d] = true
			out.M[s]++
			if int(m.cnt[d][s]) >= k {
				out.N[s]++
			}
		}
	}
	for d := 0; d < lastDom; d++ {
		if dayOK[d] {
			out.Days++
		}
	}
	return out
}

// pick chooses, per calendar month, the most recent qualifying PSKR year,
// else the most recent qualifying WSPR year, else an empty row.
func (a *almanacSeasonAccum) pick() []almanacSeasonMonth {
	months := make([]almanacSeasonMonth, 12)
	for i := range months {
		months[i] = almanacSeasonMonth{Month: i + 1}
	}
	for _, layer := range []struct {
		name string
		k    int
	}{{almanacSeasonLayerPSKR, almanacOpenMinSpotsPSKR}, {almanacSeasonLayerWSPR, almanacOpenMinSpotsWSPR}} {
		yms := make([]int, 0, len(a.layers[layer.name]))
		for ym := range a.layers[layer.name] {
			yms = append(yms, ym)
		}
		sort.Sort(sort.Reverse(sort.IntSlice(yms)))
		for _, ym := range yms {
			i := ym%100 - 1
			if months[i].Year != 0 {
				continue
			}
			if row := a.compute(layer.name, ym, layer.k); row.Days >= almanacMinActiveDaysSeasonal {
				months[i] = row
			}
		}
	}
	return months
}

// readAlmanacSeason runs the drill-down reads for centre at radius in one
// transaction and returns the 12 calendar-month rows. Days run through
// yesterday (today is partial); the lookback is almanacSeasonLookbackMonths.
// wsprCoverage (almanacWSPRCoverage of the configured backfill areas) gates
// the WSPR layer: it is read only when every chosen square lies in a
// backfilled ring. Otherwise a partial ring's n against the global ingest
// totals would pass M_min and mislabel the month, so WSPR months stay
// absent (not_collected).
func readAlmanacSeason(ctx context.Context, st almanacReadStore, centre string, radius int, band, region string, now time.Time, wsprCoverage map[string]bool) (*almanacSeason, error) {
	if radius < 0 {
		radius = 0
	}
	if radius > almanacMaxWidenRadius {
		radius = almanacMaxWidenRadius
	}
	today := utcDayIndex(now.Unix())
	yesterday := today - 1
	lastYM, _ := almanacYearMonthDOM(yesterday)
	months := make([]int, almanacSeasonLookbackMonths)
	for i := range months {
		months[i] = almanacYMAdd(lastYM, i-(almanacSeasonLookbackMonths-1))
	}
	lookbackStart := almanacYMFirstDay(months[0])

	allSquares, allRings := almanacRingSquares(centre)
	var squares []string
	var rings []int32
	for i, sq := range allSquares {
		if int(allRings[i]) <= radius {
			squares = append(squares, sq)
			rings = append(rings, allRings[i])
		}
	}
	res := &almanacSeason{
		Centre: almanacNormalizeCentre(centre), Band: band, Region: region,
		Radius: radius, Squares: squares, Watermark: -1, Today: today,
	}
	wsprOK := almanacWSPRCovered(squares, wsprCoverage)
	acc := &almanacSeasonAccum{region: region, yesterday: yesterday, layers: map[string]map[int]*almanacSeasonMonthAcc{}}
	bands := []string{band}

	err := st.withReadTx(ctx, func(tx almanacReadTx) error {
		w, ok, err := tx.watermark(ctx)
		if err != nil {
			return err
		}
		tailAfter := lookbackStart - 1
		if ok {
			res.Watermark = w
			if w > tailAfter {
				tailAfter = w
			}
			wYM, _ := almanacYearMonthDOM(w)
			var pskrMonths []int
			for _, ym := range months {
				if ym <= wYM {
					pskrMonths = append(pskrMonths, ym)
				}
			}
			if len(pskrMonths) > 0 {
				if err := tx.seasonCounts(ctx, squares, bands, pskrMonths, almanacSeasonLayerPSKR,
					acc.seasonRow(almanacSeasonLayerPSKR, w)); err != nil {
					return err
				}
			}
		}
		if tailAfter < yesterday {
			if err := tx.tailCounts(ctx, squares, rings, bands, tailAfter, tailAfter+1, yesterday,
				func(ring int, _, reg string, day int64, slot int, count int64) {
					if ring <= radius && day > tailAfter {
						acc.addSlot(almanacSeasonLayerPSKR, day, slot, reg, count)
					}
				}); err != nil {
				return err
			}
		}
		layers := []string{almanacSeasonLayerPSKR}
		if wsprOK {
			if err := tx.seasonCounts(ctx, squares, bands, months, almanacSeasonLayerWSPR,
				acc.seasonRow(almanacSeasonLayerWSPR, yesterday)); err != nil {
				return err
			}
			layers = append(layers, almanacSeasonLayerWSPR)
		}
		for _, layer := range layers {
			from, ok := acc.firstDay(layer)
			if !ok {
				continue
			}
			if err := tx.ingestSlots(ctx, layer, from, yesterday, acc.ingestRow(layer)); err != nil {
				return err
			}
			if layer == almanacSeasonLayerPSKR {
				if err := tx.lostDays(ctx, from, yesterday, acc.lostDay(layer)); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	res.Months = acc.pick()
	return res, nil
}

// ---------------------------------------------------------------------------
// Cache (typical-part style, KTD9): key centre grid4 | band | region
// ---------------------------------------------------------------------------

type almanacSeasonEntry struct {
	season *almanacSeason
	at     time.Time
	errAt  time.Time
}

type almanacSeasonCache struct {
	mu  sync.Mutex
	lru *almanacLRU[almanacSeasonEntry]
}

func newAlmanacSeasonCache() *almanacSeasonCache {
	return &almanacSeasonCache{lru: newAlmanacLRU[almanacSeasonEntry](almanacCacheMaxEntries)}
}

// entry returns (creating) the entry for key. Caller holds c.mu.
func (c *almanacSeasonCache) entry(key string) *almanacSeasonEntry {
	return c.lru.get(key, true)
}

func (s *almanacService) seasonValid(e *almanacSeasonEntry, now time.Time, today int64, radius int) bool {
	return e != nil && e.season != nil && now.Sub(e.at) < almanacSeasonCacheTTL &&
		e.season.Today == today && e.season.Radius == radius && s.watermarkCurrent(e.season.Watermark)
}

// getSeason returns the drill-down for (grid4, band, region) at the landing
// view's radius, reading Postgres only when the cache can't serve it.
func (s *almanacService) getSeason(grid4, band, region string) (*almanacSeason, error) {
	if s == nil || s.store == nil || s.season == nil {
		return nil, errAlmanacUnavailable
	}
	radius, err := s.typicalRadius(grid4) // landing typical part → radius (cached)
	if err != nil {
		return nil, err
	}
	now := s.now()
	today := utcDayIndex(now.Unix())
	key := grid4 + "|" + band + "|" + region
	c := s.season

	check := func() (*almanacSeason, bool, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		e := c.entry(key)
		if s.seasonValid(e, now, today, radius) {
			return e.season, true, nil
		}
		if now.Sub(e.errAt) < almanacNegCacheTTL {
			return nil, true, errAlmanacUnavailable
		}
		return nil, false, nil
	}
	if res, done, err := check(); done {
		return res, err
	}
	s.slow.Lock()
	defer s.slow.Unlock()
	if res, done, err := check(); done {
		return res, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.queryTimeout)
	defer cancel()
	res, err := readAlmanacSeason(ctx, s.store, grid4, radius, band, region, now, s.wsprCoverage)

	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entry(key)
	if err != nil {
		logInfo("almanac season %s: read failed: %v", key, err)
		e.errAt = now
		return nil, errAlmanacUnavailable
	}
	e.season, e.at, e.errAt = res, now, time.Time{}
	return res, nil
}

// ---------------------------------------------------------------------------
// Response
// ---------------------------------------------------------------------------

// almanacSeasonMonthJSON: status "ok" carries year/layer/k/n/m; status
// "not_collected" has them null.
type almanacSeasonMonthJSON struct {
	Month  int                        `json:"month"`
	Name   string                     `json:"name"`
	Status string                     `json:"status"`
	Year   *int                       `json:"year"`
	Layer  *string                    `json:"layer"`
	Days   int                        `json:"days"`
	K      *int                       `json:"k"`
	N      *[almanacSlotsPerDay]uint8 `json:"n"`
	M      *[almanacSlotsPerDay]uint8 `json:"m"`
}

func almanacSeasonMonthToJSON(m almanacSeasonMonth) almanacSeasonMonthJSON {
	out := almanacSeasonMonthJSON{Month: m.Month, Status: "not_collected"}
	if m.Month >= 1 && m.Month <= 12 {
		out.Name = almanacMonthNames[m.Month-1]
	}
	if m.Year == 0 {
		return out
	}
	year, layer, k, n, mm := m.Year, m.Layer, m.K, m.N, m.M
	out.Status, out.Year, out.Layer, out.Days, out.K, out.N, out.M = "ok", &year, &layer, m.Days, &k, &n, &mm
	return out
}

// almanacSeasonResponse is the /api/almanac/season body (docs/api.md).
type almanacSeasonResponse struct {
	QTH          string                   `json:"qth"`
	Area         almanacAreaInfo          `json:"area"`
	Band         string                   `json:"band"`
	Region       string                   `json:"region"`
	SlotMinutes  int                      `json:"slot_minutes"`
	MMin         int                      `json:"m_min"`
	K            map[string]int           `json:"k"`
	ThroughDay   string                   `json:"through_day"`
	WatermarkDay int64                    `json:"watermark_day"`
	Months       []almanacSeasonMonthJSON `json:"months"`
}

func buildAlmanacSeasonResponse(qth string, area almanacArea, res *almanacSeason) *almanacSeasonResponse {
	out := &almanacSeasonResponse{
		QTH: qth,
		Area: almanacAreaInfo{
			Grid4: res.Centre, Source: string(area.Source), Approximate: area.Approximate(),
			Radius: res.Radius, Squares: res.Squares,
		},
		Band:         res.Band,
		Region:       res.Region,
		SlotMinutes:  almanacSlotMinutes,
		MMin:         almanacMinActiveDaysSeasonal,
		K:            map[string]int{almanacSeasonLayerPSKR: almanacOpenMinSpotsPSKR, almanacSeasonLayerWSPR: almanacOpenMinSpotsWSPR},
		ThroughDay:   almanacDayString(res.Today - 1),
		WatermarkDay: res.Watermark,
		Months:       make([]almanacSeasonMonthJSON, 0, len(res.Months)),
	}
	for _, m := range res.Months {
		out.Months = append(out.Months, almanacSeasonMonthToJSON(m))
	}
	return out
}

// almanacSeasonParams validates band (in-scope, case-insensitive) and region
// (11-region taxonomy, case-insensitive).
func almanacSeasonParams(r *http.Request) (band, region string, err error) {
	band = strings.ToLower(strings.TrimSpace(r.URL.Query().Get("band")))
	region = strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("region")))
	if !almanacBandInScope(band) {
		return "", "", errors.New("band required: one of 160m…10m")
	}
	regions := allRegionStrings()
	for _, rc := range regions {
		if rc == region {
			return band, region, nil
		}
	}
	return "", "", errors.New("region required: one of " + strings.Join(regions, ","))
}

func almanacSeasonHandler(w http.ResponseWriter, r *http.Request) {
	almanacSvc.ServeSeason(w, r)
}

// ServeSeason: GET /api/almanac/season?qth=&band=&region=.
// 400 missing/invalid qth, band or region; 404 unresolvable callsign; 503
// when the data is unavailable (no Postgres, timeout, negative cache).
func (s *almanacService) ServeSeason(w http.ResponseWriter, r *http.Request) {
	var band, region string
	qth, area, ok := s.resolveRequest(w, r, func() (err error) {
		band, region, err = almanacSeasonParams(r)
		return err
	})
	if !ok {
		return
	}
	res, err := s.getSeason(area.Grid4, band, region)
	if err != nil {
		writeAlmanacUnavailable(w, err)
		return
	}
	writeAlmanacJSON(w, buildAlmanacSeasonResponse(qth, area, res))
}
