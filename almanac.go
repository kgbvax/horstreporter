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
//
// With an SNR floor (KTD13, tier t of almanacSNRTierFloors):
//
//	M           counts only days ≥ the SNR collection start (earlier days
//	            have no SNR data: unknown, never closed)
//	N           = |{d ∈ M-days : Σ_ring spots with SNR ≥ floor(d,s) ≥ k}|
//	share(s)    = Σ_{M-days} spots ≥ floor / Σ_{M-days} SNR-carrying spots
//	            (null when the denominator is 0)
//	unknown     = M < M_min_eff, M_min_eff = min(M_min, max(2, covered)),
//	            covered = window days on/after the SNR start, not lost
//	            and ingest-alive in at least one slot
//	            ("preliminary" while M_min_eff < M_min; the widening radius
//	            is still chosen on all-SNR activity at the full M_min)

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

	counts []uint16
	act    []uint16
	ingest [almanacAccumDays][almanacSlotsPerDay]int64
	lost   [almanacAccumDays]bool

	// SNR floor (KTD13): tier is the almanacSNRTierFloors index, -1 = any
	// SNR. With a tier, ge holds the spots with SNR ≥ floor and snrN the
	// SNR-carrying spots, both indexed like counts. snrSince is the SNR
	// collection start (hasSNRSince false: never collected).
	tier        int
	ge          []uint16
	snrN        []uint16
	snrSince    int64
	hasSNRSince bool
}

// newAlmanacAccum prepares the accumulator for centre over win; tier is the
// SNR floor index (-1 = any SNR).
func newAlmanacAccum(centre string, win almanacWindow, tier int) *almanacAccum {
	if tier >= almanacSNRTiers {
		tier = almanacSNRTiers - 1
	}
	a := &almanacAccum{centre: almanacNormalizeCentre(centre), win: win, wm: win.Start - 1, tier: tier}
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
	if tier >= 0 {
		a.ge = make([]uint16, len(a.counts))
		a.snrN = make([]uint16, len(a.counts))
	}
	return a
}

// setSNRSince records the SNR collection start read in the transaction.
func (a *almanacAccum) setSNRSince(day int64, ok bool) {
	a.snrSince, a.hasSNRSince = day, ok
}

// snrKnown: window day di has SNR data (it is on or after the SNR start).
func (a *almanacAccum) snrKnown(di int) bool {
	return a.hasSNRSince && a.win.Start+int64(di) >= a.snrSince
}

// snrCoveredDays counts the window days that carry SNR data: on or after
// the SNR start, not lost, and ingest-alive in at least half of the day's
// slots. A lost or wholly dead day (no ingest totals, e.g. an outage) can
// never count toward M, so counting it would hold the preliminary M_min above
// every cell's M. The same holds for a day that is only partly covered (the
// SNR start day, or a long outage): it adds to M only in the slots it covers,
// so counting it would leave every other slot one day short of M_min and read
// "not enough data" exactly there.
func (a *almanacAccum) snrCoveredDays() int {
	alive := almanacAliveMask(&a.ingest, &a.lost)
	n := 0
	for di := 0; di < almanacWindowDays; di++ {
		if !a.snrKnown(di) {
			continue
		}
		slots := 0
		for _, ok := range alive[di] {
			if ok {
				slots++
			}
		}
		if slots >= almanacSlotsPerDay/2 {
			n++
		}
	}
	return n
}

// mMin is the M_min of this read: the full 30-day M_min, or with an SNR
// floor and an SNR start the preliminary almanacEffectiveMMin. Without an
// SNR start a floored view keeps the full M_min (every cell reads unknown).
func (a *almanacAccum) mMin() int {
	if a.tier < 0 || !a.hasSNRSince {
		return almanacMinActiveDays30
	}
	return almanacEffectiveMMin(almanacMinActiveDays30, a.snrCoveredDays())
}

// openCounts is the per-cell count the open test (≥ k) applies to: all
// spots, or with an SNR floor the spots at or above it.
func (a *almanacAccum) openCounts() []uint16 {
	if a.tier >= 0 {
		return a.ge
	}
	return a.counts
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

// addSeasonRow decodes one sparse almanac_season_counts row (almanac_sparse.go)
// for the window days ≤ W in its month, visiting only its non-zero cells. A
// malformed row is treated as absent. counts is only read during the call.
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
	// domDi[dom−1] = window day index + 1 for the days ≤ W of this month
	// (0 = not in the window or already in the daily tail).
	var domDi [almanacSeasonDaysPerMonth]int8
	inWindow := false
	for di := 0; di < almanacWindowDays; di++ {
		if a.win.Start+int64(di) > a.wm {
			break // days ascend: everything later belongs to the tail
		}
		if a.dayYM[di] == ym {
			domDi[a.dayDOM[di]-1] = int8(di + 1)
			inWindow = true
		}
	}
	if !inWindow {
		return
	}
	err := almanacSparseEachCell(counts, func(pos, v int, h *almanacSNRHist) {
		di := int(domDi[pos/almanacSlotsPerDay]) - 1
		if di < 0 {
			return
		}
		s := pos % almanacSlotsPerDay
		satAdd16(&a.act[a.actIdx(level, bi, di, s)], int64(v))
		if hasRegion {
			ci := a.countIdx(level, bi, ri, di, s)
			satAdd16(&a.counts[ci], int64(v))
			if a.tier >= 0 {
				satAdd16(&a.ge[ci], int64(h.atLeast(a.tier)))
				satAdd16(&a.snrN[ci], int64(h.total()))
			}
		}
	})
	if err != nil {
		almanacSparseMalformed("almanac read", grid, band, reg, ym, err)
	}
}

// addTailRow adds one ring-level aggregate row from the daily table (days >
// W): all spots, SNR-carrying spots and spots at or above the floor.
func (a *almanacAccum) addTailRow(level int, band, reg string, day int64, slot int, count, snr, ge int64) {
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
		ci := a.countIdx(level, bi, ri, di, slot)
		satAdd16(&a.counts[ci], count)
		if a.tier >= 0 {
			satAdd16(&a.ge[ci], ge)
			satAdd16(&a.snrN[ci], snr)
		}
	}
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
// (alive, active days). Compact uint8 so a cached result stays ~10 KB. Share
// is the pooled SNR share coded by almanacShareCode (KTD13; 0 = none / no
// SNR floor). MMin is the lane's (effective) M_min; 0 means the full
// almanacMinActiveDays30.
type almanacLane struct {
	Band   string
	Region string
	N      [almanacSlotsPerDay]uint8
	M      [almanacSlotsPerDay]uint8
	Share  [almanacSlotsPerDay]uint16
	MMin   uint8
}

// almanacShareCode codes num/den as 1 + the share in per mille (1..1001);
// 0 = no share (den 0). The zero value of a lane therefore has no share.
func almanacShareCode(num, den int64) uint16 {
	if den <= 0 {
		return 0
	}
	if num > den {
		num = den
	}
	if num < 0 {
		num = 0
	}
	return uint16((num*1000+den/2)/den) + 1
}

// almanacShareValue decodes almanacShareCode: the share (0..1) and ok.
func almanacShareValue(code uint16) (float64, bool) {
	if code == 0 {
		return 0, false
	}
	return float64(code-1) / 1000, true
}

// mMin is the lane's M_min (the full 30-day one unless set).
func (l almanacLane) mMin() int {
	if l.MMin == 0 {
		return almanacMinActiveDays30
	}
	return int(l.MMin)
}

// unknown: fewer than M_min active days → "not enough data" (R3).
func (l almanacLane) unknown(s int) bool { return int(l.M[s]) < l.mMin() }

// usual: a known cell open on at least almanacUsuallyShare of its days.
func (l almanacLane) usual(s int) bool {
	return !l.unknown(s) && l.M[s] > 0 && float64(l.N[s]) >= almanacUsuallyShare*float64(l.M[s])
}

// almanacTypical is the cached "typical" part (KTD9): lanes + radius, for one
// SNR tier (-1 = any SNR) and the SNR collection start read with it.
type almanacTypical struct {
	Centre      string
	Window      almanacWindow
	Watermark   int64 // as read in the transaction; -1 when none
	Radius      int
	Squares     []string
	Lanes       []almanacLane
	Tier        int
	SNRSince    int64
	HasSNRSince bool
	// MMin is the M_min the lanes were judged against (the preliminary
	// effective one for a floored view with few SNR days); SNRDays the
	// window days carrying SNR data (floored views only, else 0).
	MMin    int
	SNRDays int
}

// preliminary: a floored view judged against less than the full M_min.
func (t *almanacTypical) preliminary() bool {
	return t.Tier >= 0 && t.mMin() < almanacMinActiveDays30
}

// mMin is MMin, or the full 30-day M_min when unset.
func (t *almanacTypical) mMin() int {
	if t.MMin <= 0 {
		return almanacMinActiveDays30
	}
	return t.MMin
}

// computeAlmanacTypical picks the radius from per-slot knownness (U1) and
// computes the lanes at that radius. The radius always uses the all-SNR
// activity at the full M_min, so floored and any-SNR views share it.
func computeAlmanacTypical(a *almanacAccum) *almanacTypical {
	radius, squares := chooseAlmanacRadius(a.centre, a.knownSlotCounts(almanacMinActiveDays30), almanacWidenMinKnownSlots)
	mMin := a.mMin()
	snrDays := 0
	if a.tier >= 0 {
		snrDays = a.snrCoveredDays()
	}
	wm := int64(-1)
	if a.hasWM {
		wm = a.wm
	}
	return &almanacTypical{
		Centre:      a.centre,
		Window:      a.win,
		Watermark:   wm,
		Radius:      radius,
		Squares:     squares,
		Lanes:       a.cells(radius, mMin),
		Tier:        a.tier,
		SNRSince:    a.snrSince,
		HasSNRSince: a.hasSNRSince,
		MMin:        mMin,
		SNRDays:     snrDays,
	}
}

// activeSlotDays marks, for band bi at the given ring radius, the (day, slot)
// pairs that count toward M: the slot is ingest-alive and the area (rings
// 0…radius) had a spot on the band, to any region, in that slot or the next.
// It is tier-agnostic; cells() layers the SNR-collection rule on top. any is
// true when at least one pair is marked.
func (a *almanacAccum) activeSlotDays(bi, radius int, alive *[almanacWindowDays][almanacSlotsPerDay]bool) (ok [almanacWindowDays][almanacSlotsPerDay]bool, any bool) {
	var actSum [almanacAccumDays][almanacSlotsPerDay]bool
	for di := 0; di < almanacAccumDays; di++ {
		for s := 0; s < almanacSlotsPerDay; s++ {
			var sum int
			for lv := 0; lv <= radius; lv++ {
				sum += int(a.act[a.actIdx(lv, bi, di, s)])
			}
			actSum[di][s] = sum > 0
		}
	}
	for di := 0; di < almanacWindowDays; di++ {
		for s := 0; s < almanacSlotsPerDay; s++ {
			ok[di][s] = alive[di][s] && almanacActiveOrNext(&actSum[di], &actSum[di+1], s)
			any = any || ok[di][s]
		}
	}
	return ok, any
}

// knownSlotCounts is the widening input: for each ring radius 0…max and each
// in-scope band, how many of the 48 slots are known, i.e. have at least mMin
// alive, area-active days. That is the exact condition under which a lane
// shows a slot instead of "not enough data", so the radius is picked on what
// the panel can actually display. (The old test counted active days at any
// time of day, which a sparse square passes while every slot stays unknown.)
func (a *almanacAccum) knownSlotCounts(mMin int) [almanacLevels][]int {
	var out [almanacLevels][]int
	alive := almanacAliveMask(&a.ingest, &a.lost)
	for r := 0; r < almanacLevels; r++ {
		out[r] = make([]int, len(almanacInScopeBands))
		for bi := range almanacInScopeBands {
			ok, _ := a.activeSlotDays(bi, r, &alive)
			for s := 0; s < almanacSlotsPerDay; s++ {
				days := 0
				for di := 0; di < almanacWindowDays; di++ {
					if ok[di][s] {
						days++
					}
				}
				if days >= mMin {
					out[r][bi]++
				}
			}
		}
	}
	return out
}

// cells computes N/M per (band, region, slot) at radius (levels 0…radius).
// Bands with no active slot at all are omitted; every region of an active
// band gets a lane, so never-reached regions read "closed" (R2). mMin is
// the M_min each lane is judged against (stored in the lane).
func (a *almanacAccum) cells(radius, mMin int) []almanacLane {
	if radius >= almanacLevels {
		radius = almanacLevels - 1
	}
	alive := almanacAliveMask(&a.ingest, &a.lost)
	open := a.openCounts()
	var lanes []almanacLane
	for bi, band := range almanacInScopeBands {
		active, anyActive := a.activeSlotDays(bi, radius, &alive)
		// anyActive lists the band whatever the SNR floor, so days without
		// SNR data read as "not enough data" instead of dropping the band.
		if !anyActive {
			continue
		}
		ok := active
		var m [almanacSlotsPerDay]uint8
		for di := 0; di < almanacWindowDays; di++ {
			for s := 0; s < almanacSlotsPerDay; s++ {
				ok[di][s] = ok[di][s] && (a.tier < 0 || a.snrKnown(di))
				if ok[di][s] {
					m[s]++
				}
			}
		}
		for ri, reg := range a.regions {
			l := almanacLane{Band: band, Region: reg, M: m, MMin: uint8(mMin)}
			var geSum, snrSum [almanacSlotsPerDay]int64
			for di := 0; di < almanacWindowDays; di++ {
				for s := 0; s < almanacSlotsPerDay; s++ {
					if !ok[di][s] {
						continue
					}
					var sum int
					for lv := 0; lv <= radius; lv++ {
						ci := a.countIdx(lv, bi, ri, di, s)
						sum += int(open[ci])
						if a.tier >= 0 {
							geSum[s] += int64(a.ge[ci])
							snrSum[s] += int64(a.snrN[ci])
						}
					}
					if sum >= almanacOpenMinSpotsPSKR {
						l.N[s]++
					}
				}
			}
			if a.tier >= 0 {
				for s := range l.Share {
					l.Share[s] = almanacShareCode(geSum[s], snrSum[s])
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
	open := a.openCounts()
	for bi, band := range almanacInScopeBands {
		for ri, reg := range a.regions {
			sum := func(di, s int) int {
				var v int
				for lv := 0; lv <= radius; lv++ {
					v += int(open[a.countIdx(lv, bi, ri, di, s)])
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
	// PeakShare is the SNR share at the peak slot (0..1) when an SNR floor
	// is applied and the slot has SNR data (KTD13); omitted otherwise.
	PeakShare *float64 `json:"peak_share,omitempty"`
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
	if e.PeakSlot >= 0 {
		if v, ok := almanacShareValue(l.Share[e.PeakSlot]); ok {
			e.PeakShare = &v
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
