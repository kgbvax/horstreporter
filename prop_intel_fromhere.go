package main

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// prop_intel_fromhere.go: the per-area "normal for this hour" behind the
// from-here view of /api/prop_intel/v2.
//
// The unified climatology (prop_baseline.go) is a global mesh keyed by the
// receiver's region, so it cannot say what is normal FROM a given QTH. The
// region baseline dx_region_baseline_daily can: every PSKReporter FT8/FT4 (and
// DX-cluster) spot adds one row per end, keyed by that end's 4-char square and
// the OTHER end's region (dxPulseRegionBaselineKeysForSpot). Summing it over
// the squares of the operator's area gives, per (band, far-end region, slot,
// day), the same count the from-here live counter produces for those spots.
//
// The normal for a request window is the mean, over recent complete days with
// PSKReporter ingest coverage, of that day's count over the same clock
// window (slot counts weighted by their overlap with the window). Its sample
// standard deviation drives the from-here surge z-score.

const (
	// fromHereNormalDays is the reference period: the last N complete UTC days.
	fromHereNormalDays = 28
	// fromHereMaxRadius caps the area the normal is computed for (7×7 squares);
	// larger explicit areas get no normal rather than a slow query.
	fromHereMaxRadius = 3
	// fromHereQueryTimeout bounds the background query itself.
	fromHereQueryTimeout = 20 * time.Second
	// fromHereErrorTTL keeps a failed fetch from being retried on every request.
	fromHereErrorTTL = 60 * time.Second
	// fromHereCacheCap bounds the cache (one entry per area × slot set × day).
	fromHereCacheCap = 512
	// fromHereSurgeMinSpots is the fewest live reports a from-here surge needs:
	// on a path that is normally near empty, a stddev under one report would
	// otherwise turn one or two stray reports into a surge (and a push).
	fromHereSurgeMinSpots = 5
)

// fromHereWait is how long a request waits for a cache miss; a slower query
// keeps running and fills the cache for the next poll. A var for tests.
var fromHereWait = 1200 * time.Millisecond

// fromHereCountRow is one (band, region, slot, day) count summed over the
// area's squares.
type fromHereCountRow struct {
	Band   string
	Region string
	Slot   int
	Day    int64
	Count  int64
}

// fromHereDaySlot keys ingest coverage.
type fromHereDaySlot struct {
	Day  int64
	Slot int
}

// fromHereNormal is one cell's normal for the request window.
type fromHereNormal struct {
	Expected   float64
	StdDev     float64
	SampleDays int
}

// fromHereNormals is the per-cell normal for one request. SampleDays is the
// number of reference days with ingest coverage for the whole window: a cell
// absent from ByCell had zero spots on all of them.
type fromHereNormals struct {
	ByCell     map[propIntelCellKey]fromHereNormal
	SampleDays int
}

// windowSegment is the part of a request window inside one 30-minute slot.
type windowSegment struct {
	Slot      int
	DayOffset int64 // the slot's UTC day relative to the window end's day (0 or -1)
	Weight    float64
}

// fromHereWindowSegments splits [now−minutes, now] into slot segments.
func fromHereWindowSegments(now int64, minutes int) []windowSegment {
	if minutes <= 0 {
		return nil
	}
	const slotSec = int64(1800)
	start := now - int64(minutes)*60
	endDay := utcDayIndex(now)
	var segs []windowSegment
	for t := start; t < now; {
		slotStart := t - ((t%slotSec)+slotSec)%slotSec
		next := slotStart + slotSec
		if next > now {
			next = now
		}
		segs = append(segs, windowSegment{
			Slot:      utcSlotOfDay(t),
			DayOffset: utcDayIndex(t) - endDay,
			Weight:    float64(next-t) / float64(slotSec),
		})
		t = next
	}
	return segs
}

// computeFromHereNormals turns area-summed slot counts and ingest coverage
// into per-cell normals for the window ending at now. Reference days are the
// fromHereNormalDays complete days before now's day; a day counts only when
// every slot segment of its window had PSKReporter ingest.
func computeFromHereNormals(rows []fromHereCountRow, coverage map[fromHereDaySlot]bool, now int64, minutes int) fromHereNormals {
	out := fromHereNormals{ByCell: map[propIntelCellKey]fromHereNormal{}}
	segs := fromHereWindowSegments(now, minutes)
	if len(segs) == 0 {
		return out
	}
	today := utcDayIndex(now)
	var days []int64
	for d := today - fromHereNormalDays; d < today; d++ {
		ok := true
		for _, s := range segs {
			if !coverage[fromHereDaySlot{Day: d + s.DayOffset, Slot: s.Slot}] {
				ok = false
				break
			}
		}
		if ok {
			days = append(days, d)
		}
	}
	out.SampleDays = len(days)
	if len(days) == 0 {
		return out
	}
	dayPos := make(map[int64]int, len(days))
	for i, d := range days {
		dayPos[d] = i
	}

	// Per cell, the window total of each reference day.
	totals := map[propIntelCellKey][]float64{}
	for _, r := range rows {
		for _, s := range segs {
			if r.Slot != s.Slot {
				continue
			}
			i, ok := dayPos[r.Day-s.DayOffset]
			if !ok {
				continue
			}
			key := propIntelCellKey{band: r.Band, region: r.Region}
			t := totals[key]
			if t == nil {
				t = make([]float64, len(days))
				totals[key] = t
			}
			t[i] += float64(r.Count) * s.Weight
		}
	}
	n := float64(len(days))
	for key, t := range totals {
		var sum float64
		for _, v := range t {
			sum += v
		}
		mean := sum / n
		var sd float64
		if len(t) > 1 {
			var ss float64
			for _, v := range t {
				ss += (v - mean) * (v - mean)
			}
			sd = math.Sqrt(ss / (n - 1))
		}
		out.ByCell[key] = fromHereNormal{Expected: mean, StdDev: sd, SampleDays: len(days)}
	}
	return out
}

// fromHereAreaGrids lists the 4-char squares the from-here view counts for:
// the QTH square, its 3×3 block with surroundings, or the live area's block.
// nil for a callsign QTH or an area wider than fromHereMaxRadius.
func fromHereAreaGrids(qth string, surroundings bool, area *liveArea) []string {
	qth = normalizeQTHToken(qth)
	if !isLocator(qth) {
		return nil
	}
	if area != nil && area.Radius > 1 {
		if area.Radius > fromHereMaxRadius {
			return nil
		}
		return getSquaresWithinRings(area.Centre, area.Radius)
	}
	if area != nil {
		surroundings = area.Radius == 1
	}
	return qthSquares(qth, surroundings)
}

// --- Store ------------------------------------------------------------------

const fromHereCountsSQL = `
	SELECT band, region, slot_of_day, day_index, SUM(spot_count)::bigint
	FROM dx_region_baseline_daily
	WHERE target_grid4 = ANY($1::text[])
	  AND slot_of_day = ANY($2::int[])
	  AND day_index BETWEEN $3 AND $4
	GROUP BY band, region, slot_of_day, day_index`

const fromHereCoverageSQL = `
	SELECT day_index, slot_of_day
	FROM almanac_ingest_slots
	WHERE layer = $1
	  AND slot_of_day = ANY($2::int[])
	  AND day_index BETWEEN $3 AND $4
	  AND spot_total > 0`

// FromHereCounts reads the area-summed region-baseline counts and the
// PSKReporter ingest coverage for the given slots and day range.
func (s *dxPostgresStore) FromHereCounts(ctx context.Context, grids []string, slots []int, dayFrom, dayTo int64) ([]fromHereCountRow, map[fromHereDaySlot]bool, error) {
	if s == nil || s.pool == nil {
		return nil, nil, errNoFromHereStore
	}
	rows, err := s.pool.Query(ctx, fromHereCountsSQL, grids, slots, dayFrom, dayTo)
	if err != nil {
		return nil, nil, err
	}
	var out []fromHereCountRow
	for rows.Next() {
		var r fromHereCountRow
		if err := rows.Scan(&r.Band, &r.Region, &r.Slot, &r.Day, &r.Count); err != nil {
			rows.Close()
			return nil, nil, err
		}
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	cov := map[fromHereDaySlot]bool{}
	crows, err := s.pool.Query(ctx, fromHereCoverageSQL, almanacSeasonLayerPSKR, slots, dayFrom, dayTo)
	if err != nil {
		return nil, nil, err
	}
	defer crows.Close()
	for crows.Next() {
		var k fromHereDaySlot
		if err := crows.Scan(&k.Day, &k.Slot); err != nil {
			return nil, nil, err
		}
		cov[k] = true
	}
	return out, cov, crows.Err()
}

type fromHereStoreError string

func (e fromHereStoreError) Error() string { return string(e) }

const errNoFromHereStore = fromHereStoreError("no Postgres store for from-here normals")

// fromHereCountsSource is what the cache reads from (the Postgres store in
// production, a fake in tests).
type fromHereCountsSource interface {
	FromHereCounts(ctx context.Context, grids []string, slots []int, dayFrom, dayTo int64) ([]fromHereCountRow, map[fromHereDaySlot]bool, error)
}

// fromHereSource returns the production counts source, nil without Postgres.
var fromHereSource = func() fromHereCountsSource {
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

// --- Cache --------------------------------------------------------------------

// fromHereCounts is one fetch of area-summed slot counts with their coverage.
type fromHereCounts struct {
	rows     []fromHereCountRow
	coverage map[fromHereDaySlot]bool
}

// asyncEntry is one cached background fetch; done closes when val/err are set.
type asyncEntry[T any] struct {
	done      chan struct{}
	val       T
	err       error
	fetchedAt time.Time
}

// wait reports whether the fetch finished successfully within d.
func (e *asyncEntry[T]) wait(d time.Duration) bool {
	if d <= 0 {
		select {
		case <-e.done:
		default:
			return false
		}
	} else {
		select {
		case <-e.done:
		case <-time.After(d):
			return false
		}
	}
	return e.err == nil
}

// asyncCache shares one background fetch per key between concurrent callers,
// keeps results until evicted and failures for fromHereErrorTTL.
type asyncCache[T any] struct {
	mu      sync.Mutex
	entries map[string]*asyncEntry[T]
	cap     int
	timeout time.Duration
	what    string
}

func newAsyncCache[T any](capacity int, timeout time.Duration, what string) *asyncCache[T] {
	return &asyncCache[T]{entries: map[string]*asyncEntry[T]{}, cap: capacity, timeout: timeout, what: what}
}

var fromHereCache = newAsyncCache[fromHereCounts](fromHereCacheCap, fromHereQueryTimeout, "from-here normals")

// fromHereNormalsFor returns the per-cell normals for the area and window, or
// nil when there is no store, no area, or the data is not ready within
// fromHereWait (the fetch then completes in the background).
func fromHereNormalsFor(grids []string, now int64, minutes int) *fromHereNormals {
	src := fromHereSource()
	if src == nil || len(grids) == 0 {
		return nil
	}
	segs := fromHereWindowSegments(now, minutes)
	if len(segs) == 0 {
		return nil
	}
	slotSet := map[int]bool{}
	for _, s := range segs {
		slotSet[s.Slot] = true
	}
	slots := make([]int, 0, len(slotSet))
	for s := range slotSet {
		slots = append(slots, s)
	}
	sort.Ints(slots)
	sortedGrids := append([]string(nil), grids...)
	sort.Strings(sortedGrids)
	today := utcDayIndex(now)
	// One day before the reference period covers a window that crosses midnight.
	dayFrom, dayTo := today-fromHereNormalDays-1, today-1

	var kb strings.Builder
	kb.WriteString(strings.Join(sortedGrids, ","))
	kb.WriteByte('|')
	for _, s := range slots {
		kb.WriteString(strconv.Itoa(s))
		kb.WriteByte(',')
	}
	kb.WriteByte('|')
	kb.WriteString(strconv.FormatInt(today, 10))
	key := kb.String()

	e := fromHereCache.get(key, func(ctx context.Context) (fromHereCounts, error) {
		rows, cov, err := src.FromHereCounts(ctx, sortedGrids, slots, dayFrom, dayTo)
		return fromHereCounts{rows: rows, coverage: cov}, err
	})
	if !e.wait(fromHereWait) {
		return nil
	}
	n := computeFromHereNormals(e.val.rows, e.val.coverage, now, minutes)
	return &n
}

// get returns the entry for key, starting one fetch when it is missing or a
// failed fetch has aged out. Concurrent callers share the fetch.
func (c *asyncCache[T]) get(key string, fetch func(context.Context) (T, error)) *asyncEntry[T] {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok {
		select {
		case <-e.done:
			if e.err == nil || time.Since(e.fetchedAt) < fromHereErrorTTL {
				c.mu.Unlock()
				return e
			}
		default:
			c.mu.Unlock()
			return e
		}
	}
	if len(c.entries) >= c.cap {
		c.evictOldestLocked()
	}
	e := &asyncEntry[T]{done: make(chan struct{})}
	c.entries[key] = e
	c.mu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				e.err = fromHereStoreError(c.what + " fetch panicked")
				logInfo("%s fetch panic: %v", c.what, r)
			}
			e.fetchedAt = time.Now()
			close(e.done)
		}()
		ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
		defer cancel()
		e.val, e.err = fetch(ctx)
		if e.err != nil {
			logDebug("%s fetch failed: %v", c.what, e.err)
		}
	}()
	return e
}

// evictOldestLocked drops the oldest finished entry (in-flight ones stay).
func (c *asyncCache[T]) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	for k, e := range c.entries {
		select {
		case <-e.done:
		default:
			continue
		}
		if oldestKey == "" || e.fetchedAt.Before(oldest) {
			oldestKey, oldest = k, e.fetchedAt
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

// reset empties the cache (tests).
func (c *asyncCache[T]) reset() {
	c.mu.Lock()
	c.entries = map[string]*asyncEntry[T]{}
	c.mu.Unlock()
}

// fromHereMatcher decides which ends of a spot lie in the operator's area,
// with the per-request work (which QTH tokens are squares, which callsigns)
// done once instead of per spot.
type fromHereMatcher struct {
	area    *liveArea // radius ≥ 2: square distance
	squares []string  // 4-char squares of a locator QTH (own square or 3×3)
	calls   []string  // a callsign QTH
}

func newFromHereMatcher(qthSet []string, area *liveArea) fromHereMatcher {
	f := fromHereMatcher{area: area}
	if area != nil {
		return f
	}
	for _, t := range qthSet {
		if isLocator(t) {
			f.squares = append(f.squares, t)
		} else {
			f.calls = append(f.calls, t)
		}
	}
	return f
}

func (f *fromHereMatcher) inArea(loc, call string) bool {
	if f.area != nil {
		return f.area.contains(loc)
	}
	for _, sq := range f.squares {
		if hasPrefixFold(loc, sq) {
			return true
		}
	}
	for _, c := range f.calls {
		if matchCall(call, c) {
			return true
		}
	}
	return false
}

// ends returns the far end of a spot once for each of its ends inside the
// area (a: the receiver is inside, b: the other end is inside), mirroring
// dxPulseRegionBaselineKeysForSpot restricted to the area: a spot with both
// ends inside counts twice, once per end, unless both ends share a 4-char
// square (the baseline dedupes that identical key). Membership is the test
// propIntelRemote uses; locators come back as trimmed feed strings, so the
// common case allocates nothing.
func (f *fromHereMatcher) ends(m *MQTTMessage, receiverSide string) (a, b string) {
	recvLoc, otherLoc := strings.TrimSpace(m.SL), strings.TrimSpace(m.RL)
	var recvCall, otherCall string
	if len(f.calls) > 0 {
		recvCall = strings.ToUpper(strings.TrimSpace(m.SC))
		otherCall = strings.ToUpper(strings.TrimSpace(m.RC))
	}
	if receiverSide == "rc" {
		recvLoc, otherLoc = otherLoc, recvLoc
		recvCall, otherCall = otherCall, recvCall
	}
	recvIn, otherIn := f.inArea(recvLoc, recvCall), f.inArea(otherLoc, otherCall)
	if recvIn {
		a = otherLoc
	}
	if otherIn {
		b = recvLoc
	}
	if recvIn && otherIn && len(recvLoc) >= 4 && len(otherLoc) >= 4 && strings.EqualFold(recvLoc[:4], otherLoc[:4]) {
		b = ""
	}
	return a, b
}

// fromHereRemoteEnds is fromHereMatcher.ends for a one-off spot.
func fromHereRemoteEnds(m *MQTTMessage, qthSet []string, area *liveArea, receiverSide string) (a, b string) {
	f := newFromHereMatcher(qthSet, area)
	return f.ends(m, receiverSide)
}

// applyFromHereNormal sets a from-here cell's expected / expected_spots from
// the area normal and, when the live count clears the surge threshold against
// it, the atypical (also on the pskr source, the only one the normal covers).
// A cell the reference days never saw gets expected 0. No-op without a normal
// of at least propIntelMinSampleDays.
func applyFromHereNormal(cell *propIntelV2Cell, normals *fromHereNormals, live int, threshold float64) {
	if normals == nil || normals.SampleDays < propIntelMinSampleDays {
		return
	}
	nrm := normals.ByCell[propIntelCellKey{band: cell.Band, region: cell.Region}]
	exp := round1(nrm.Expected)
	spots := live
	cell.Expected = &exp
	cell.ExpectedSpots = &spots
	for i := range cell.PerSource {
		if cell.PerSource[i].Source == "pskr" {
			cell.PerSource[i].SampleDays = normals.SampleDays
		}
	}
	if nrm.StdDev <= 0 || live < fromHereSurgeMinSpots {
		return
	}
	z := (float64(live) - nrm.Expected) / nrm.StdDev
	if z < threshold {
		return
	}
	at := propIntelV2Atypical{ZScore: round3(z), Confidence: round3(atypicalConfidence(normals.SampleDays))}
	cell.Atypical = &at
	marked := 0
	for i := range cell.PerSource {
		if cell.PerSource[i].Source == "pskr" {
			cp := at
			cell.PerSource[i].Atypical = &cp
			marked++
		}
	}
	if n := len(cell.PerSource); n > 0 && marked > 0 {
		cell.AtypicalAgreement = round2(float64(marked) / float64(n))
	}
}
