package main

import (
	"fmt"
	"slices"
	"sort"
)

// almanac.go is the pure core of the 30-day Almanac (plan U2, KTD2/KTD4/
// KTD11): it accumulates the per-slot counts streamed by the read
// transaction (almanac_store.go), derives the ingest-alive and per-slot area
// activity masks, and turns them into "opened N of M days" cells per
// (band, region, slot) plus the agenda windows. No SQL here.
//
// Cell rule (plan "Computing one cell"):
//
//	alive(d,s)  = ingest total of (d,s) > 0 and ≥ 10% of slot s's 30-day median
//	              (lost days are never alive)
//	active(d,s) = the ring had ≥1 spot on the band, to any region incl. its
//	              own, in slot s or s+1 of day d (s+1 of slot 47 is slot 0 of d+1)
//	M           = |{d : alive(d,s) ∧ active(d,s)}|
//	N           = |{d ∈ M-days : Σ_ring count(d,s) ≥ k}|
//	unknown     = M < M_min

const almanacSlotsPerDay = almanacSeasonSlotsPerDay

// almanacMinutesPerDay / almanacSlotMinutes: the slot length (30 min).
const (
	almanacMinutesPerDay = 1440
	almanacSlotMinutes   = almanacMinutesPerDay / almanacSlotsPerDay
)

// Agenda entry statuses.
const (
	almanacStatusOngoing  = "ongoing"
	almanacStatusUpcoming = "upcoming"
)

// almanacBandIndex maps each in-scope band to its almanacInScopeBands index.
var almanacBandIndex = func() map[string]int {
	m := make(map[string]int, len(almanacInScopeBands))
	for i, b := range almanacInScopeBands {
		m[b] = i
	}
	return m
}()

// almanacBandInScope reports whether band is an Almanac band (160 m … 10 m).
func almanacBandInScope(band string) bool {
	_, ok := almanacBandIndex[band]
	return ok
}

// almanacLevels is the number of ring levels (0 … almanacMaxWidenRadius).
const almanacLevels = almanacMaxWidenRadius + 1

// almanacAccumDays: the 30 window days plus today (index almanacWindowDays).
const almanacAccumDays = almanacWindowDays + 1

// almanacWindow is the 30-day typical window [Start, End] (UTC day indexes,
// End = yesterday) plus Today, which feeds only the today overlay.
type almanacWindow struct {
	Start, End, Today int64
}

func almanacWindowFor(now int64) almanacWindow {
	today := utcDayIndex(now)
	return almanacWindow{Start: today - almanacWindowDays, End: today - 1, Today: today}
}

// almanacNormalizeCentre uppercases and truncates a locator to grid4.
func almanacNormalizeCentre(centre string) string {
	c := normalizeQTHToken(centre)
	if len(c) >= 4 && isLocator(c) {
		c = c[:4]
	}
	return c
}

// almanacRingSquares lists the squares within almanacMaxWidenRadius of the
// centre (edge-clipped, getSquaresWithinRings order) with each square's ring
// level: the smallest r whose getSquaresWithinRings(centre, r) contains it.
func almanacRingSquares(centre string) ([]string, []int32) {
	c := almanacNormalizeCentre(centre)
	level := map[string]int32{}
	for r := almanacMaxWidenRadius; r >= 0; r-- {
		for _, sq := range getSquaresWithinRings(c, r) {
			level[sq] = int32(r)
		}
	}
	squares := getSquaresWithinRings(c, almanacMaxWidenRadius)
	rings := make([]int32, len(squares))
	for i, sq := range squares {
		rings[i] = level[sq]
	}
	return squares, rings
}

// ---------------------------------------------------------------------------
// Accumulator
// ---------------------------------------------------------------------------

// almanacAccum holds everything one read transaction returns, reduced to
// fixed-size arrays (≈1 MB at r=2, independent of how busy the ring is):
//
//	counts[level][band][region][day][slot]  uint16, saturating
//	act[level][band][day][slot]             uint16 spot sum to ANY region
//	dayMasks[grid][band]                    active-day bits (window days)
//	ingest[day][slot], lost[day]
//
// Seasonal rows contribute only days ≤ W and tail rows only days > W, where
// W is the watermark read first in the same transaction — so a day crossing
// the watermark is counted exactly once whatever the store returns.
type almanacAccum struct {
	centre  string
	win     almanacWindow
	wm      int64
	hasWM   bool
	squares []string
	rings   []int32
	gridIdx map[string]int
	regIdx  map[string]int
	regions []string
	dayYM   [almanacAccumDays]int
	dayDOM  [almanacAccumDays]int

	counts   []uint16
	act      []uint16
	dayMasks []uint64
	ingest   [almanacAccumDays][almanacSlotsPerDay]int64
	lost     [almanacAccumDays]bool
}

func newAlmanacAccum(centre string, win almanacWindow) *almanacAccum {
	a := &almanacAccum{centre: almanacNormalizeCentre(centre), win: win, wm: win.Start - 1}
	a.squares, a.rings = almanacRingSquares(a.centre)
	a.gridIdx = make(map[string]int, len(a.squares))
	for i, sq := range a.squares {
		a.gridIdx[sq] = i
	}
	a.regions = allRegionStrings()
	a.regIdx = make(map[string]int, len(a.regions))
	for i, r := range a.regions {
		a.regIdx[r] = i
	}
	for di := 0; di < almanacAccumDays; di++ {
		a.dayYM[di], a.dayDOM[di] = almanacYearMonthDOM(win.Start + int64(di))
	}
	nb, nr := len(almanacInScopeBands), len(a.regions)
	a.counts = make([]uint16, almanacLevels*nb*nr*almanacAccumDays*almanacSlotsPerDay)
	a.act = make([]uint16, almanacLevels*nb*almanacAccumDays*almanacSlotsPerDay)
	a.dayMasks = make([]uint64, len(a.squares)*nb)
	return a
}

// setWatermark records the fold watermark read at the start of the
// transaction. Without one (fold never ran) wm keeps newAlmanacAccum's
// win.Start − 1, so every day comes from the tail.
func (a *almanacAccum) setWatermark(w int64, ok bool) {
	a.hasWM = ok
	if ok {
		a.wm = w
	}
}

// tailAfter is the exclusive lower day bound for daily-table reads.
func (a *almanacAccum) tailAfter() int64 { return a.wm }

// seasonMonths lists the year_months holding window days ≤ W.
func (a *almanacAccum) seasonMonths() []int {
	var out []int
	for di := 0; di < almanacWindowDays; di++ {
		if a.win.Start+int64(di) > a.wm {
			break
		}
		if len(out) == 0 || out[len(out)-1] != a.dayYM[di] {
			out = append(out, a.dayYM[di])
		}
	}
	return out
}

func satAdd16(p *uint16, v int64) {
	if v <= 0 {
		return
	}
	if s := int64(*p) + v; s >= 0xFFFF {
		*p = 0xFFFF
	} else {
		*p = uint16(s)
	}
}

// satAdd8 adds v > 0 to *p, saturating at 255 (never wraps, also on int64
// overflow of the sum).
func satAdd8(p *uint8, v int64) {
	if v <= 0 {
		return
	}
	if s := int64(*p) + v; s >= 0xFF || s < 0 {
		*p = 0xFF
	} else {
		*p = uint8(s)
	}
}

func (a *almanacAccum) countIdx(level, b, r, di, s int) int {
	nb, nr := len(almanacInScopeBands), len(a.regions)
	return ((((level*nb+b)*nr+r)*almanacAccumDays)+di)*almanacSlotsPerDay + s
}

func (a *almanacAccum) actIdx(level, b, di, s int) int {
	nb := len(almanacInScopeBands)
	return (((level*nb+b)*almanacAccumDays)+di)*almanacSlotsPerDay + s
}

// addSeasonRow unpacks one almanac_season_counts row (31×48 day-major uint8)
// for the window days ≤ W in its month. counts is only read during the call.
func (a *almanacAccum) addSeasonRow(grid, band, reg string, ym int, counts []byte) {
	gi, ok := a.gridIdx[grid]
	if !ok {
		return
	}
	bi, ok := almanacBandIndex[band]
	if !ok {
		return
	}
	ri, hasRegion := a.regIdx[reg]
	level := int(a.rings[gi])
	for di := 0; di < almanacWindowDays; di++ {
		if a.win.Start+int64(di) > a.wm {
			break // days ascend: everything later belongs to the tail
		}
		if a.dayYM[di] != ym {
			continue
		}
		off := almanacSegmentOffset(a.dayDOM[di])
		if off+almanacSlotsPerDay > len(counts) {
			continue
		}
		seg := counts[off : off+almanacSlotsPerDay]
		any := false
		for s, v := range seg {
			if v == 0 {
				continue
			}
			any = true
			satAdd16(&a.act[a.actIdx(level, bi, di, s)], int64(v))
			if hasRegion {
				satAdd16(&a.counts[a.countIdx(level, bi, ri, di, s)], int64(v))
			}
		}
		if any {
			a.dayMasks[gi*len(almanacInScopeBands)+bi] |= 1 << uint(di)
		}
	}
}

// addTailRow adds one ring-level aggregate row from the daily table (days > W).
func (a *almanacAccum) addTailRow(level int, band, reg string, day int64, slot int, count int64) {
	if day <= a.wm || day < a.win.Start || day > a.win.Today || slot < 0 || slot >= almanacSlotsPerDay ||
		level < 0 || level >= almanacLevels {
		return
	}
	bi, ok := almanacBandIndex[band]
	if !ok {
		return
	}
	di := int(day - a.win.Start)
	satAdd16(&a.act[a.actIdx(level, bi, di, slot)], count)
	if ri, ok := a.regIdx[reg]; ok {
		satAdd16(&a.counts[a.countIdx(level, bi, ri, di, slot)], count)
	}
}

// addTailActive marks (grid, band) active on a tail day (widening masks).
func (a *almanacAccum) addTailActive(grid, band string, day int64) {
	if day <= a.wm || day < a.win.Start || day > a.win.End {
		return
	}
	gi, ok := a.gridIdx[grid]
	if !ok {
		return
	}
	bi, ok := almanacBandIndex[band]
	if !ok {
		return
	}
	a.dayMasks[gi*len(almanacInScopeBands)+bi] |= 1 << uint(day-a.win.Start)
}

func (a *almanacAccum) addIngest(day int64, slot int, total int64) {
	if day < a.win.Start || day > a.win.Today || slot < 0 || slot >= almanacSlotsPerDay {
		return
	}
	a.ingest[day-a.win.Start][slot] += total
}

func (a *almanacAccum) addLost(day int64) {
	if day >= a.win.Start && day <= a.win.Today {
		a.lost[day-a.win.Start] = true
	}
}

// masks returns the per-(grid4, band) active-day masks for U1's widening.
func (a *almanacAccum) masks() map[almanacGridBand]uint64 {
	nb := len(almanacInScopeBands)
	out := make(map[almanacGridBand]uint64, len(a.squares)*nb)
	for gi, sq := range a.squares {
		for bi, b := range almanacInScopeBands {
			if m := a.dayMasks[gi*nb+bi]; m != 0 {
				out[almanacGridBand{Grid: sq, Band: b}] = m
			}
		}
	}
	return out
}

// almanacAliveMedian sorts vals (non-empty) in place and returns their
// median: the average of the two middle values (the middle one when odd).
func almanacAliveMedian(vals []int64) float64 {
	slices.Sort(vals)
	n := len(vals)
	return float64(vals[(n-1)/2]+vals[n/2]) / 2
}

// almanacIsAlive is the ingest-alive rule for one (d, s): total t > 0 and
// ≥ almanacAliveFraction × the slot's median; lost days are never alive.
func almanacIsAlive(t int64, lost bool, median float64) bool {
	return !lost && t > 0 && float64(t) >= almanacAliveFraction*median
}

// almanacActiveOrNext: activity in slot s of day, or in slot s+1 (slot 0 of
// next, which may be nil, after the day's last slot).
func almanacActiveOrNext(day, next *[almanacSlotsPerDay]bool, s int) bool {
	if day[s] {
		return true
	}
	if s+1 < almanacSlotsPerDay {
		return day[s+1]
	}
	return next != nil && next[0]
}

// almanacAliveMask applies the ingest-alive rule: (d, s) is alive when its
// total is > 0 and ≥ almanacAliveFraction × the slot's 30-day median (days
// without a row count as 0 in the median). Lost days are never alive.
func almanacAliveMask(ingest *[almanacAccumDays][almanacSlotsPerDay]int64, lost *[almanacAccumDays]bool) [almanacWindowDays][almanacSlotsPerDay]bool {
	var alive [almanacWindowDays][almanacSlotsPerDay]bool
	var sorted [almanacWindowDays]int64
	for s := 0; s < almanacSlotsPerDay; s++ {
		for di := 0; di < almanacWindowDays; di++ {
			sorted[di] = ingest[di][s]
		}
		median := almanacAliveMedian(sorted[:])
		for di := 0; di < almanacWindowDays; di++ {
			alive[di][s] = almanacIsAlive(ingest[di][s], lost[di], median)
		}
	}
	return alive
}

// ---------------------------------------------------------------------------
// Cells
// ---------------------------------------------------------------------------

// almanacLane is one (band, region) lane: per-slot N (open days) and M
// (alive, active days). Compact uint8 so a cached result stays ~10 KB.
type almanacLane struct {
	Band   string
	Region string
	N      [almanacSlotsPerDay]uint8
	M      [almanacSlotsPerDay]uint8
}

// unknown: fewer than M_min active days → "not enough data" (R3).
func (l almanacLane) unknown(s int) bool { return int(l.M[s]) < almanacMinActiveDays30 }

// usual: a known cell open on at least almanacUsuallyShare of its days.
func (l almanacLane) usual(s int) bool {
	return !l.unknown(s) && l.M[s] > 0 && float64(l.N[s]) >= almanacUsuallyShare*float64(l.M[s])
}

// almanacTypical is the cached "typical" part (KTD9): lanes + radius.
type almanacTypical struct {
	Centre    string
	Window    almanacWindow
	Watermark int64 // as read in the transaction; -1 when none
	Radius    int
	Squares   []string
	Lanes     []almanacLane
}

// computeAlmanacTypical picks the radius from the active-day masks (U1) and
// computes the lanes at that radius.
func computeAlmanacTypical(a *almanacAccum) *almanacTypical {
	radius, squares := chooseAlmanacRadius(a.centre, a.masks(), almanacMinActiveDays30)
	wm := int64(-1)
	if a.hasWM {
		wm = a.wm
	}
	return &almanacTypical{
		Centre:    a.centre,
		Window:    a.win,
		Watermark: wm,
		Radius:    radius,
		Squares:   squares,
		Lanes:     a.cells(radius),
	}
}

// cells computes N/M per (band, region, slot) at radius (levels 0…radius).
// Bands with no active slot at all are omitted; every region of an active
// band gets a lane, so never-reached regions read "closed" (R2).
func (a *almanacAccum) cells(radius int) []almanacLane {
	if radius >= almanacLevels {
		radius = almanacLevels - 1
	}
	alive := almanacAliveMask(&a.ingest, &a.lost)
	var lanes []almanacLane
	var actSum [almanacAccumDays][almanacSlotsPerDay]bool
	var ok [almanacWindowDays][almanacSlotsPerDay]bool
	for bi, band := range almanacInScopeBands {
		for di := 0; di < almanacAccumDays; di++ {
			for s := 0; s < almanacSlotsPerDay; s++ {
				var sum int
				for lv := 0; lv <= radius; lv++ {
					sum += int(a.act[a.actIdx(lv, bi, di, s)])
				}
				actSum[di][s] = sum > 0
			}
		}
		var m [almanacSlotsPerDay]uint8
		anyM := false
		for di := 0; di < almanacWindowDays; di++ {
			for s := 0; s < almanacSlotsPerDay; s++ {
				ok[di][s] = alive[di][s] && almanacActiveOrNext(&actSum[di], &actSum[di+1], s)
				if ok[di][s] {
					m[s]++
					anyM = true
				}
			}
		}
		if !anyM {
			continue
		}
		for ri, reg := range a.regions {
			l := almanacLane{Band: band, Region: reg, M: m}
			for di := 0; di < almanacWindowDays; di++ {
				for s := 0; s < almanacSlotsPerDay; s++ {
					if !ok[di][s] {
						continue
					}
					var sum int
					for lv := 0; lv <= radius; lv++ {
						sum += int(a.counts[a.countIdx(lv, bi, ri, di, s)])
					}
					if sum >= almanacOpenMinSpotsPSKR {
						l.N[s]++
					}
				}
			}
			lanes = append(lanes, l)
		}
	}
	return lanes
}

// ---------------------------------------------------------------------------
// Today overlay (KTD11)
// ---------------------------------------------------------------------------

// almanacToday holds the ring's per-lane spot sums for yesterday's last slot
// (index 0) and today's 48 slots (index 1+s), at the typical radius.
type almanacToday struct {
	lanes map[almanacLaneKey]*[almanacSlotsPerDay + 1]uint16
}

type almanacLaneKey struct {
	band, region string
}

// set stores a count; slot -1 is yesterday's slot 47.
func (t *almanacToday) set(band, reg string, slot int, n int) {
	if slot < -1 || slot >= almanacSlotsPerDay {
		return
	}
	if t.lanes == nil {
		t.lanes = map[almanacLaneKey]*[almanacSlotsPerDay + 1]uint16{}
	}
	k := almanacLaneKey{band, reg}
	arr := t.lanes[k]
	if arr == nil {
		arr = new([almanacSlotsPerDay + 1]uint16)
		t.lanes[k] = arr
	}
	if n > 0xFFFF {
		n = 0xFFFF
	}
	arr[slot+1] = uint16(n)
}

// openNow: the current or previous slot today reached k in the ring.
func (t *almanacToday) openNow(band, reg string, nowSlot int) bool {
	if t == nil || nowSlot < 0 || nowSlot >= almanacSlotsPerDay {
		return false
	}
	arr := t.lanes[almanacLaneKey{band, reg}]
	if arr == nil {
		return false
	}
	return int(arr[nowSlot+1]) >= almanacOpenMinSpotsPSKR || int(arr[nowSlot]) >= almanacOpenMinSpotsPSKR
}

// today builds the overlay from the tail rows of yesterday and today.
func (a *almanacAccum) today(radius int) *almanacToday {
	if radius >= almanacLevels {
		radius = almanacLevels - 1
	}
	t := &almanacToday{}
	yd := almanacWindowDays - 1
	td := almanacWindowDays
	for bi, band := range almanacInScopeBands {
		for ri, reg := range a.regions {
			sum := func(di, s int) int {
				var v int
				for lv := 0; lv <= radius; lv++ {
					v += int(a.counts[a.countIdx(lv, bi, ri, di, s)])
				}
				return v
			}
			if v := sum(yd, almanacSlotsPerDay-1); v > 0 {
				t.set(band, reg, -1, v)
			}
			for s := 0; s < almanacSlotsPerDay; s++ {
				if v := sum(td, s); v > 0 {
					t.set(band, reg, s, v)
				}
			}
		}
	}
	return t
}

// ---------------------------------------------------------------------------
// Agenda (R14, KTD4)
// ---------------------------------------------------------------------------

// almanacSlotWindow is a circular run of usual slots: Start 0–47, Len 1–48.
type almanacSlotWindow struct {
	Start, Len int
}

func (w almanacSlotWindow) contains(s int) bool {
	return ((s-w.Start)%almanacSlotsPerDay+almanacSlotsPerDay)%almanacSlotsPerDay < w.Len
}

// almanacUsualWindows finds the lane's usual windows on the circular day:
// runs of usual slots with non-usual gaps of ≤ almanacAgendaBridgeSlots
// bridged; windows may cross midnight. Sorted by start slot.
func almanacUsualWindows(l almanacLane) []almanacSlotWindow {
	const n = almanacSlotsPerDay
	var u [n]bool
	count := 0
	for s := 0; s < n; s++ {
		u[s] = l.usual(s)
		if u[s] {
			count++
		}
	}
	if count == 0 {
		return nil
	}
	b := u
	// Bridge short gaps: find each maximal circular run of non-usual slots.
	for s := 0; s < n; s++ {
		if u[s] || !u[(s-1+n)%n] {
			continue // not the start of a gap
		}
		gap := 0
		for gap < n && !u[(s+gap)%n] {
			gap++
		}
		if gap <= almanacAgendaBridgeSlots {
			for i := 0; i < gap; i++ {
				b[(s+i)%n] = true
			}
		}
	}
	first := -1
	for s := 0; s < n; s++ {
		if !b[s] {
			first = s
			break
		}
	}
	if first < 0 {
		return []almanacSlotWindow{{Start: 0, Len: n}}
	}
	var out []almanacSlotWindow
	for i := 1; i <= n; i++ {
		s := (first + i) % n
		if b[s] && !b[(s-1+n)%n] {
			length := 0
			for length < n && b[(s+length)%n] {
				length++
			}
			out = append(out, almanacSlotWindow{Start: s, Len: length})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// almanacAgendaEntry is one agenda line, e.g. "20m to NA: usually
// 13:00–18:00 UTC (24/30 days)". Times are UTC (R15); End is exclusive.
type almanacAgendaEntry struct {
	Band            string `json:"band"`
	Region          string `json:"region"`
	StartSlot       int    `json:"start_slot"`
	LenSlots        int    `json:"len_slots"`
	Start           string `json:"start"`
	End             string `json:"end"`
	CrossesMidnight bool   `json:"crosses_midnight"`
	AllDay          bool   `json:"all_day"`
	// Status is "ongoing" (the window contains now) or "upcoming".
	Status      string `json:"status"`
	StartsInMin int    `json:"starts_in_min"`
	// Peak is the window's best usual slot (highest N/M).
	PeakSlot  int  `json:"peak_slot"`
	PeakN     int  `json:"peak_n"`
	PeakM     int  `json:"peak_m"`
	OpenToday bool `json:"open_today"`
}

func almanacHHMM(slot int) string {
	slot = ((slot % almanacSlotsPerDay) + almanacSlotsPerDay) % almanacSlotsPerDay
	m := slot * almanacSlotMinutes
	return fmt.Sprintf("%02d:%02d", m/60, m%60)
}

// almanacAgendaEntryFor describes window w of lane l relative to nowMin
// (minutes since 00:00 UTC).
func almanacAgendaEntryFor(l almanacLane, w almanacSlotWindow, today *almanacToday, nowMin int) almanacAgendaEntry {
	nowSlot := nowMin / almanacSlotMinutes
	e := almanacAgendaEntry{
		Band:            l.Band,
		Region:          l.Region,
		StartSlot:       w.Start,
		LenSlots:        w.Len,
		Start:           almanacHHMM(w.Start),
		End:             almanacHHMM(w.Start + w.Len),
		CrossesMidnight: w.Len < almanacSlotsPerDay && w.Start+w.Len > almanacSlotsPerDay,
		AllDay:          w.Len >= almanacSlotsPerDay,
		PeakSlot:        -1,
		OpenToday:       today.openNow(l.Band, l.Region, nowSlot),
	}
	if w.contains(nowSlot) {
		e.Status = almanacStatusOngoing
	} else {
		e.Status = almanacStatusUpcoming
		e.StartsInMin = ((w.Start*almanacSlotMinutes-nowMin)%almanacMinutesPerDay + almanacMinutesPerDay) % almanacMinutesPerDay
	}
	best := -1.0
	for i := 0; i < w.Len; i++ {
		s := (w.Start + i) % almanacSlotsPerDay
		if !l.usual(s) {
			continue
		}
		r := float64(l.N[s]) / float64(l.M[s])
		if r > best || (r == best && int(l.M[s]) > e.PeakM) {
			best = r
			e.PeakSlot, e.PeakN, e.PeakM = s, int(l.N[s]), int(l.M[s])
		}
	}
	return e
}

// almanacAgenda lists the windows that are open now or usually start within
// the look-ahead, ongoing first, then by start time, then by strength.
func almanacAgenda(t *almanacTypical, today *almanacToday, nowMin int) []almanacAgendaEntry {
	if t == nil {
		return nil
	}
	out := []almanacAgendaEntry{}
	for _, l := range t.Lanes {
		for _, w := range almanacUsualWindows(l) {
			e := almanacAgendaEntryFor(l, w, today, nowMin)
			if e.Status == almanacStatusOngoing || e.StartsInMin <= almanacAgendaLookAheadSlots*almanacSlotMinutes {
				out = append(out, e)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Status == almanacStatusOngoing) != (b.Status == almanacStatusOngoing) {
			return a.Status == almanacStatusOngoing
		}
		if a.StartsInMin != b.StartsInMin {
			return a.StartsInMin < b.StartsInMin
		}
		ra, rb := float64(a.PeakN)/float64(max(a.PeakM, 1)), float64(b.PeakN)/float64(max(b.PeakM, 1))
		return ra > rb
	})
	return out
}
