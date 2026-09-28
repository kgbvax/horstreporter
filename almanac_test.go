package main

import (
	"context"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// In-memory model of the U2 read transaction. fakeAggStore mirrors the SQL in
// almanac_store.go: seasonal rows are packed 31×48 uint8 per (grid4, band,
// region, year_month, layer) exactly like almanac_season_counts; daily rows
// are dx_region_baseline_daily (no layer column: PSKR/cluster only); the tail
// aggregate sums across ring grids grouped by ring level like the SQL JOIN on
// unnest(grids, rings). afterWatermark lets a test commit a "fold" between
// the watermark read and the data reads (worst case: no snapshot isolation).
// ---------------------------------------------------------------------------

type aggSeasonKey struct {
	Grid, Band, Region string
	YM                 int
	Layer              string
}

type aggDailyKey struct {
	Grid, Band, Region string
	Day                int64
	Slot               int
}

type aggIngestKey struct {
	Day   int64
	Slot  int
	Layer string
}

type fakeAggStore struct {
	mu        sync.Mutex
	wm        int64
	hasWM     bool
	season    map[aggSeasonKey][]byte
	daily     map[aggDailyKey]int64
	ingest    map[aggIngestKey]int64
	lost      map[int64]bool
	err       error
	block     bool // wait for ctx.Done (timeout simulation)
	txCalls   int
	tailCalls int
	// seasonLayers records every layer the reader asked for.
	seasonLayers   []string
	afterWatermark func(f *fakeAggStore)
}

func newFakeAggStore(wm int64) *fakeAggStore {
	return &fakeAggStore{
		wm:     wm,
		hasWM:  true,
		season: map[aggSeasonKey][]byte{},
		daily:  map[aggDailyKey]int64{},
		ingest: map[aggIngestKey]int64{},
		lost:   map[int64]bool{},
	}
}

// addSeason sets one packed slot byte (saturating), like the fold would.
func (f *fakeAggStore) addSeason(grid, band, region, layer string, day int64, slot int, n int64) {
	ym, dom := almanacYearMonthDOM(day)
	k := aggSeasonKey{grid, band, region, ym, layer}
	c := f.season[k]
	if c == nil {
		c = make([]byte, almanacSeasonCountsLen)
		f.season[k] = c
	}
	i := almanacSegmentOffset(dom) + slot
	v := int64(c[i]) + n
	if v > 255 {
		v = 255
	}
	c[i] = byte(v)
}

// add puts PSKR spots where they live: folded days in the seasonal record,
// later days in the daily table.
func (f *fakeAggStore) add(grid, band, region string, day int64, slot int, n int64) {
	if f.hasWM && day <= f.wm {
		f.addSeason(grid, band, region, almanacSeasonLayerPSKR, day, slot, n)
		return
	}
	f.daily[aggDailyKey{grid, band, region, day, slot}] += n
}

// foldDay copies the daily rows of day into the seasonal record and advances
// the watermark (the daily rows stay until the prune, as in production).
func (f *fakeAggStore) foldDay(day int64) {
	for k, v := range f.daily {
		if k.Day == day {
			f.addSeason(k.Grid, k.Band, k.Region, almanacSeasonLayerPSKR, k.Day, k.Slot, v)
		}
	}
	f.wm = day
}

func (f *fakeAggStore) setIngest(from, to int64, total int64) {
	for d := from; d <= to; d++ {
		for s := 0; s < almanacSeasonSlotsPerDay; s++ {
			f.ingest[aggIngestKey{d, s, almanacSeasonLayerPSKR}] = total
		}
	}
}

func (f *fakeAggStore) withReadTx(ctx context.Context, fn func(almanacReadTx) error) error {
	f.mu.Lock()
	f.txCalls++
	block, err := f.block, f.err
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	return fn(&fakeAggTx{f: f})
}

type fakeAggTx struct{ f *fakeAggStore }

func (t *fakeAggTx) watermark(context.Context) (int64, bool, error) {
	w, ok := t.f.wm, t.f.hasWM
	if t.f.afterWatermark != nil {
		hook := t.f.afterWatermark
		t.f.afterWatermark = nil
		hook(t.f)
	}
	return w, ok, nil
}

func aggContains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func (t *fakeAggTx) seasonCounts(_ context.Context, grids, bands []string, months []int, layer string,
	fn func(grid, band, region string, ym int, counts []byte)) error {
	t.f.seasonLayers = append(t.f.seasonLayers, layer)
	for k, c := range t.f.season {
		if k.Layer != layer || !aggContains(grids, k.Grid) || !aggContains(bands, k.Band) {
			continue
		}
		okMonth := false
		for _, m := range months {
			okMonth = okMonth || m == k.YM
		}
		if okMonth {
			fn(k.Grid, k.Band, k.Region, k.YM, c)
		}
	}
	return nil
}

func (t *fakeAggTx) tailCounts(_ context.Context, grids []string, rings []int32, bands []string, afterDay, fromDay, toDay int64,
	fn func(ring int, band, region string, day int64, slot int, count int64)) error {
	t.f.tailCalls++
	type k struct {
		ring         int
		band, region string
		day          int64
		slot         int
	}
	sum := map[k]int64{}
	for dk, v := range t.f.daily {
		if dk.Day <= afterDay || dk.Day < fromDay || dk.Day > toDay || !aggContains(bands, dk.Band) {
			continue
		}
		for i, g := range grids {
			if g == dk.Grid {
				sum[k{int(rings[i]), dk.Band, dk.Region, dk.Day, dk.Slot}] += v
			}
		}
	}
	for kk, v := range sum {
		fn(kk.ring, kk.band, kk.region, kk.day, kk.slot, v)
	}
	return nil
}

func (t *fakeAggTx) tailActiveDays(_ context.Context, grids, bands []string, afterDay, fromDay, toDay int64,
	fn func(grid, band string, day int64)) error {
	type k struct {
		grid, band string
		day        int64
	}
	seen := map[k]bool{}
	for dk, v := range t.f.daily {
		if v <= 0 || dk.Day <= afterDay || dk.Day < fromDay || dk.Day > toDay ||
			!aggContains(bands, dk.Band) || !aggContains(grids, dk.Grid) {
			continue
		}
		seen[k{dk.Grid, dk.Band, dk.Day}] = true
	}
	for kk := range seen {
		fn(kk.grid, kk.band, kk.day)
	}
	return nil
}

func (t *fakeAggTx) ingestSlots(_ context.Context, layer string, fromDay, toDay int64, fn func(day int64, slot int, total int64)) error {
	for k, v := range t.f.ingest {
		if k.Layer == layer && k.Day >= fromDay && k.Day <= toDay {
			fn(k.Day, k.Slot, v)
		}
	}
	return nil
}

func (t *fakeAggTx) lostDays(_ context.Context, fromDay, toDay int64, fn func(day int64)) error {
	for d := range t.f.lost {
		if d >= fromDay && d <= toDay {
			fn(d)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// aggTestNow is 2026-10-15 12:30 UTC (slot 25).
var aggTestNow = time.Date(2026, 10, 15, 12, 30, 0, 0, time.UTC)

func aggToday() int64 { return utcDayIndex(aggTestNow.Unix()) }

// newAggWorld: watermark at today−3, ingest alive on every slot of the
// window, and JO32 active on every in-scope band in every slot (1 spot to EU:
// below k, so it only makes the area "active").
func newAggWorld() *fakeAggStore {
	today := aggToday()
	f := newFakeAggStore(today - 3)
	f.setIngest(today-31, today, 1000)
	return f
}

func (f *fakeAggStore) activeAllBands(grid string, fromDay, toDay int64, slots []int) {
	for d := fromDay; d <= toDay; d++ {
		for _, b := range almanacInScopeBands {
			for _, s := range slots {
				f.add(grid, b, "EU", d, s, 1)
			}
		}
	}
}

func allSlots() []int {
	s := make([]int, almanacSeasonSlotsPerDay)
	for i := range s {
		s[i] = i
	}
	return s
}

func slotRange(from, to int) []int {
	var s []int
	for i := from; i <= to; i++ {
		s = append(s, i)
	}
	return s
}

func computeAgg(t testing.TB, f *fakeAggStore, centre string) *almanacTypical {
	t.Helper()
	win := almanacWindowFor(aggTestNow.Unix())
	acc, err := readAlmanacAccum(context.Background(), f, centre, win, false)
	if err != nil {
		t.Fatalf("readAlmanacAccum: %v", err)
	}
	return computeAlmanacTypical(acc)
}

func findLane(t testing.TB, typ *almanacTypical, band, region string) almanacLane {
	t.Helper()
	for _, l := range typ.Lanes {
		if l.Band == band && l.Region == region {
			return l
		}
	}
	t.Fatalf("lane %s/%s missing", band, region)
	return almanacLane{}
}

// ---------------------------------------------------------------------------
// Cells
// ---------------------------------------------------------------------------

func TestAlmanacWindow(t *testing.T) {
	win := almanacWindowFor(aggTestNow.Unix())
	today := aggToday()
	if win.Today != today || win.End != today-1 || win.Start != today-30 {
		t.Fatalf("window = %+v, today %d", win, today)
	}
}

// AE1 shape + R2: 24 of 30 alive, active days open → n=24, m=30; the six
// zero-spot days count as closed, never skipped.
func TestAlmanacCellOpenNofM(t *testing.T) {
	f := newAggWorld()
	win := almanacWindowFor(aggTestNow.Unix())
	f.activeAllBands("JO32", win.Start, win.End, allSlots())
	for i := int64(0); i < 24; i++ {
		for s := 26; s < 36; s++ {
			f.add("JO32", "20m", "NA", win.Start+i, s, 3)
		}
	}
	typ := computeAgg(t, f, "JO32")
	if typ.Radius != 0 {
		t.Fatalf("radius = %d, want 0", typ.Radius)
	}
	l := findLane(t, typ, "20m", "NA")
	if l.N[26] != 24 || l.M[26] != 30 {
		t.Fatalf("20m/NA slot 26: n=%d m=%d, want 24/30", l.N[26], l.M[26])
	}
	if l.N[20] != 0 || l.M[20] != 30 || l.unknown(20) {
		t.Fatalf("20m/NA slot 20 (never open, active): n=%d m=%d unknown=%v, want closed 0/30", l.N[20], l.M[20], l.unknown(20))
	}
	// A region never reached on an active band is closed, not absent (R2).
	sa := findLane(t, typ, "20m", "SA")
	if sa.M[26] != 30 || sa.N[26] != 0 || sa.unknown(26) {
		t.Fatalf("20m/SA: n=%d m=%d, want 0/30 closed", sa.N[26], sa.M[26])
	}
}

// AE2: an area active on only 6 days of a band reads "not enough data".
func TestAlmanacCellUnknownBelowMmin(t *testing.T) {
	f := newAggWorld()
	win := almanacWindowFor(aggTestNow.Unix())
	// Other bands active all month (radius 0), 10 m on 6 days only.
	for d := win.Start; d <= win.End; d++ {
		for _, b := range almanacInScopeBands {
			if b == "10m" && d >= win.Start+6 {
				continue
			}
			for _, s := range allSlots() {
				f.add("JO32", b, "EU", d, s, 1)
			}
		}
	}
	f.add("JO32", "10m", "KH6", win.Start+1, 30, 5)
	typ := computeAgg(t, f, "JO32")
	l := findLane(t, typ, "10m", "KH6")
	if l.M[30] != 6 || l.N[30] != 1 {
		t.Fatalf("10m/KH6 slot 30: n=%d m=%d, want 1/6", l.N[30], l.M[30])
	}
	if !l.unknown(30) {
		t.Fatalf("10m/KH6 with 6 active days must be unknown")
	}
}

// A slot whose ingest total is below 10% of its 30-day median on day d drops
// d from both N and M for that slot only.
func TestAlmanacDeadSlotExcluded(t *testing.T) {
	f := newAggWorld()
	win := almanacWindowFor(aggTestNow.Unix())
	f.activeAllBands("JO32", win.Start, win.End, allSlots())
	for d := win.Start; d <= win.End; d++ {
		f.add("JO32", "20m", "NA", d, 26, 3)
	}
	dead := win.Start + 5
	f.ingest[aggIngestKey{dead, 26, almanacSeasonLayerPSKR}] = 50 // 5% of 1000
	typ := computeAgg(t, f, "JO32")
	l := findLane(t, typ, "20m", "NA")
	if l.N[26] != 29 || l.M[26] != 29 {
		t.Fatalf("slot 26: n=%d m=%d, want 29/29 (dead day dropped)", l.N[26], l.M[26])
	}
	if l.M[27] != 30 {
		t.Fatalf("slot 27 m=%d, want 30 (other slots unaffected)", l.M[27])
	}
}

// MQTT down: the slot carries a single cluster spot → dead, not alive, even
// though that spot "opens" the cell.
func TestAlmanacMQTTDownSlotIsDead(t *testing.T) {
	f := newAggWorld()
	win := almanacWindowFor(aggTestNow.Unix())
	f.activeAllBands("JO32", win.Start, win.End, allSlots())
	down := win.Start + 10
	f.add("JO32", "20m", "NA", down, 26, 2) // cluster spots, count reaches k
	f.ingest[aggIngestKey{down, 26, almanacSeasonLayerPSKR}] = 1
	typ := computeAgg(t, f, "JO32")
	l := findLane(t, typ, "20m", "NA")
	if l.N[26] != 0 || l.M[26] != 29 {
		t.Fatalf("slot 26: n=%d m=%d, want 0/29", l.N[26], l.M[26])
	}
	// Missing ingest rows (no totals at all) are dead too.
	delete(f.ingest, aggIngestKey{down, 27, almanacSeasonLayerPSKR})
	typ = computeAgg(t, f, "JO32")
	if m := findLane(t, typ, "20m", "NA").M[27]; m != 29 {
		t.Fatalf("slot 27 without ingest row: m=%d, want 29", m)
	}
}

// A daytime-only area: night slots where the ring had no spots on the band
// read unknown, not closed (per-slot activity, s or s+1).
func TestAlmanacDaytimeOnlyAreaNightUnknown(t *testing.T) {
	f := newAggWorld()
	win := almanacWindowFor(aggTestNow.Unix())
	f.activeAllBands("JO32", win.Start, win.End, slotRange(16, 35))
	typ := computeAgg(t, f, "JO32")
	l := findLane(t, typ, "20m", "NA")
	if l.M[2] != 0 || !l.unknown(2) {
		t.Fatalf("night slot 2: m=%d unknown=%v, want 0/unknown", l.M[2], l.unknown(2))
	}
	if l.M[20] != 30 || l.unknown(20) {
		t.Fatalf("day slot 20: m=%d, want 30 known", l.M[20])
	}
	// Slot 15 counts as active because slot 16 (s+1) had spots.
	if l.M[15] != 30 {
		t.Fatalf("slot 15 (s+1 active): m=%d, want 30", l.M[15])
	}
	if l.M[36] != 0 {
		t.Fatalf("slot 36: m=%d, want 0", l.M[36])
	}
}

// Slot 47 looks at slot 0 of the next day for its s+1 activity.
func TestAlmanacSlot47WrapsToNextDay(t *testing.T) {
	f := newAggWorld()
	win := almanacWindowFor(aggTestNow.Unix())
	// Active only in slot 0 of each day, window days and today.
	f.activeAllBands("JO32", win.Start, win.Today, []int{0})
	typ := computeAgg(t, f, "JO32")
	l := findLane(t, typ, "20m", "NA")
	if l.M[47] != 30 {
		t.Fatalf("slot 47 m=%d, want 30 (next-day slot 0 active)", l.M[47])
	}
}

// WSPR-layer rows in the window never change n or m (R6).
func TestAlmanacIgnoresWSPRLayer(t *testing.T) {
	f := newAggWorld()
	win := almanacWindowFor(aggTestNow.Unix())
	f.activeAllBands("JO32", win.Start, win.End, slotRange(16, 35))
	for d := win.Start; d <= win.End-3; d++ {
		f.add("JO32", "20m", "NA", d, 26, 3)
	}
	base := findLane(t, computeAgg(t, f, "JO32"), "20m", "NA")
	for d := win.Start; d <= f.wm; d++ {
		for s := 0; s < 48; s++ {
			f.addSeason("JO32", "20m", "NA", "wspr", d, s, 9)
			f.addSeason("JO32", "20m", "EU", "wspr", d, s, 9)
			f.ingest[aggIngestKey{d, s, "wspr"}] = 5
		}
	}
	got := findLane(t, computeAgg(t, f, "JO32"), "20m", "NA")
	if got.N != base.N || got.M != base.M {
		t.Fatalf("WSPR rows changed n/m:\n n %v vs %v\n m %v vs %v", got.N, base.N, got.M, base.M)
	}
	for _, l := range f.seasonLayers {
		if l != almanacSeasonLayerPSKR {
			t.Fatalf("reader asked for layer %q", l)
		}
	}
}

// Two ring grids with 1 spot each on the same day sum to k=2: open.
func TestAlmanacRingGridsSumBeforeThreshold(t *testing.T) {
	f := newAggWorld()
	win := almanacWindowFor(aggTestNow.Unix())
	// Centre silent; its ring-1 neighbours active → radius 1.
	f.activeAllBands("JO31", win.Start, win.End, allSlots())
	f.activeAllBands("JO33", win.Start, win.End, allSlots())
	day := win.Start + 3
	f.add("JO31", "20m", "NA", day, 26, 1)
	f.add("JO33", "20m", "NA", day, 26, 1)
	tail := win.End // unfolded day too
	f.add("JO31", "20m", "NA", tail, 26, 1)
	f.add("JO33", "20m", "NA", tail, 26, 1)
	typ := computeAgg(t, f, "JO32")
	if typ.Radius != 1 {
		t.Fatalf("radius = %d, want 1", typ.Radius)
	}
	l := findLane(t, typ, "20m", "NA")
	if l.N[26] != 2 || l.M[26] != 30 {
		t.Fatalf("slot 26: n=%d m=%d, want 2/30", l.N[26], l.M[26])
	}
}

// A day that crosses the watermark (a fold commits between the watermark read
// and the data reads) is counted exactly once.
func TestAlmanacWatermarkCrossingCountedOnce(t *testing.T) {
	f := newAggWorld()
	win := almanacWindowFor(aggTestNow.Unix())
	f.activeAllBands("JO32", win.Start, win.End, allSlots())
	crossing := f.wm + 1                        // today−2, still in the daily table
	f.add("JO32", "20m", "NA", crossing, 26, 1) // 1 < k: double-count would open it
	f.add("JO32", "20m", "NA", crossing, 27, 2) // must stay counted (not missed)
	f.afterWatermark = func(s *fakeAggStore) { s.foldDay(crossing) }
	typ := computeAgg(t, f, "JO32")
	l := findLane(t, typ, "20m", "NA")
	if l.N[26] != 0 {
		t.Fatalf("slot 26 n=%d: crossing day double-counted", l.N[26])
	}
	if l.N[27] != 1 {
		t.Fatalf("slot 27 n=%d: crossing day missed", l.N[27])
	}
	if typ.Watermark != crossing-1 {
		t.Fatalf("typical watermark = %d, want the value read first (%d)", typ.Watermark, crossing-1)
	}
	// Activity must not double either: the day is active once (m 30, not 31).
	if l.M[26] != 30 {
		t.Fatalf("m=%d, want 30", l.M[26])
	}
}

// Lost days read unknown, never closed.
func TestAlmanacLostDaysExcluded(t *testing.T) {
	f := newAggWorld()
	win := almanacWindowFor(aggTestNow.Unix())
	f.activeAllBands("JO32", win.Start, win.End, allSlots())
	f.lost[win.Start+2] = true
	typ := computeAgg(t, f, "JO32")
	if m := findLane(t, typ, "20m", "NA").M[10]; m != 29 {
		t.Fatalf("m=%d, want 29 (lost day excluded)", m)
	}
}

// Per-day active masks feed U1's widening: a sparse centre widens to 2.
func TestAlmanacMasksDriveWidening(t *testing.T) {
	f := newAggWorld()
	win := almanacWindowFor(aggTestNow.Unix())
	f.activeAllBands("JO30", win.Start, win.End, []int{20}) // ring 2 of JO32
	typ := computeAgg(t, f, "JO32")
	if typ.Radius != 2 || len(typ.Squares) != 25 {
		t.Fatalf("radius=%d squares=%d, want 2/25", typ.Radius, len(typ.Squares))
	}
	if m := findLane(t, typ, "20m", "NA").M[20]; m != 30 {
		t.Fatalf("m=%d, want 30 at radius 2", m)
	}
}

// ---------------------------------------------------------------------------
// Agenda
// ---------------------------------------------------------------------------

func usualLane(slots []int, n, m uint8) almanacLane {
	l := almanacLane{Band: "20m", Region: "NA"}
	for s := 0; s < 48; s++ {
		l.M[s] = m
	}
	for _, s := range slots {
		l.N[s] = n
	}
	return l
}

func TestAlmanacWindowsConsecutive(t *testing.T) {
	l := usualLane(slotRange(10, 35), 20, 30) // 26 slots
	w := almanacUsualWindows(l)
	if len(w) != 1 || w[0].Start != 10 || w[0].Len != 26 {
		t.Fatalf("windows = %+v, want one 10+26", w)
	}
}

func TestAlmanacWindowsBridgeOneSlotGap(t *testing.T) {
	l := usualLane(slotRange(10, 35), 20, 30)
	l.N[20] = 10 // 33% inside the run
	w := almanacUsualWindows(l)
	if len(w) != 1 || w[0].Start != 10 || w[0].Len != 26 {
		t.Fatalf("windows = %+v, want gap bridged", w)
	}
	l.N[21] = 10 // two-slot gap splits
	w = almanacUsualWindows(l)
	if len(w) != 2 {
		t.Fatalf("windows = %+v, want 2 (2-slot gap not bridged)", w)
	}
}

func TestAlmanacWindowsCrossMidnight(t *testing.T) {
	l := usualLane(append(slotRange(40, 47), slotRange(0, 15)...), 20, 30) // 20z–08z
	w := almanacUsualWindows(l)
	if len(w) != 1 || w[0].Start != 40 || w[0].Len != 24 {
		t.Fatalf("windows = %+v, want one 40+24", w)
	}
	e := almanacAgendaEntryFor(l, w[0], nil, 21*60)
	if e.Start != "20:00" || e.End != "08:00" || !e.CrossesMidnight {
		t.Fatalf("entry = %+v", e)
	}
}

func TestAlmanacWindowsUnknownNotUsual(t *testing.T) {
	l := usualLane(slotRange(10, 20), 5, 6) // 83% but m < M_min
	if w := almanacUsualWindows(l); len(w) != 0 {
		t.Fatalf("unknown cells formed windows: %+v", w)
	}
}

// AE6: at 12:30 UTC an opening that usually starts 13:00 is upcoming; a slot
// today at ≥ k marks it already open.
func TestAlmanacAgendaUpcomingAndOpenToday(t *testing.T) {
	typ := &almanacTypical{Lanes: []almanacLane{usualLane(slotRange(26, 35), 24, 30)}}
	nowMin := 12*60 + 30
	ag := almanacAgenda(typ, nil, nowMin)
	if len(ag) != 1 {
		t.Fatalf("agenda = %+v", ag)
	}
	e := ag[0]
	if e.Status != "upcoming" || e.StartsInMin != 30 || e.Start != "13:00" || e.End != "18:00" || e.OpenToday {
		t.Fatalf("entry = %+v", e)
	}
	if e.PeakN != 24 || e.PeakM != 30 {
		t.Fatalf("peak = %d/%d", e.PeakN, e.PeakM)
	}
	today := &almanacToday{}
	today.set(typ.Lanes[0].Band, typ.Lanes[0].Region, 25, 3) // current slot
	ag = almanacAgenda(typ, today, nowMin)
	if !ag[0].OpenToday {
		t.Fatalf("today slot 25 ≥ k should mark open today")
	}
	// Ongoing at 14:00.
	ag = almanacAgenda(typ, nil, 14*60)
	if ag[0].Status != "ongoing" || ag[0].StartsInMin != 0 {
		t.Fatalf("at 14:00: %+v", ag[0])
	}
	// Starting more than 12 h ahead is not listed: at 00:30 the window starts in 12.5 h.
	if ag = almanacAgenda(typ, nil, 30); len(ag) != 0 {
		t.Fatalf("12.5 h ahead listed: %+v", ag)
	}
}

func TestAlmanacTodayPreviousSlotAcrossMidnight(t *testing.T) {
	today := &almanacToday{}
	today.set("20m", "NA", -1, 2) // yesterday slot 47
	if !today.openNow("20m", "NA", 0) {
		t.Fatalf("previous slot (yesterday 47) at ≥ k should count at slot 0")
	}
	if today.openNow("20m", "NA", 1) {
		t.Fatalf("slot 1 should not see yesterday 47")
	}
}

// ---------------------------------------------------------------------------
// Memory: one request at r=2 with every (grid, band, region) row populated.
// ---------------------------------------------------------------------------

func denseAggStore() *fakeAggStore {
	today := aggToday()
	f := newFakeAggStore(today - 3)
	f.setIngest(today-31, today, 1000)
	grids := getSquaresWithinRings("JO32", 2)
	regions := allRegionStrings()
	win := almanacWindowFor(aggTestNow.Unix())
	for _, g := range grids {
		for _, b := range almanacInScopeBands {
			for _, r := range regions {
				for d := win.Start - 5; d <= win.Today; d++ {
					if d <= f.wm {
						// Fill whole packed rows cheaply.
						ym, _ := almanacYearMonthDOM(d)
						k := aggSeasonKey{g, b, r, ym, almanacSeasonLayerPSKR}
						if f.season[k] == nil {
							c := make([]byte, almanacSeasonCountsLen)
							for i := range c {
								c[i] = byte(1 + i%5)
							}
							f.season[k] = c
						}
						continue
					}
					for s := 0; s < 48; s++ {
						f.daily[aggDailyKey{g, b, r, d, s}] = int64(1 + s%4)
					}
				}
			}
		}
	}
	return f
}

func TestAlmanacHeapUnder20MB(t *testing.T) {
	f := denseAggStore()
	win := almanacWindowFor(aggTestNow.Unix())
	// The fake's tail aggregation (maps) is precomputed outside the
	// measurement, so only the production reader/compute allocations count.
	st := newPrecomputedAggStore(f, "JO32", win)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	acc, err := readAlmanacAccum(context.Background(), st, "JO32", win, false)
	if err != nil {
		t.Fatal(err)
	}
	typ := computeAlmanacTypical(acc)
	runtime.ReadMemStats(&after)
	alloc := after.TotalAlloc - before.TotalAlloc
	t.Logf("r=2 dense: %d tail rows, %d lanes, radius %d, %.2f MB allocated",
		len(st.tail), len(typ.Lanes), typ.Radius, float64(alloc)/(1<<20))
	if alloc > 20<<20 {
		t.Fatalf("allocated %.1f MB, want < 20 MB", float64(alloc)/(1<<20))
	}
}

type preTailRow struct {
	ring         int
	band, region string
	day          int64
	slot         int
	count        int64
}

type precomputedAggStore struct {
	f    *fakeAggStore
	tail []preTailRow
}

func newPrecomputedAggStore(f *fakeAggStore, centre string, win almanacWindow) *precomputedAggStore {
	p := &precomputedAggStore{f: f}
	grids, rings := almanacRingSquares(centre)
	(&fakeAggTx{f: f}).tailCounts(context.Background(), grids, rings, almanacInScopeBands, f.wm, win.Start, win.Today,
		func(ring int, band, region string, day int64, slot int, count int64) {
			p.tail = append(p.tail, preTailRow{ring, band, region, day, slot, count})
		})
	sort.Slice(p.tail, func(i, j int) bool { return p.tail[i].day < p.tail[j].day })
	return p
}

func (p *precomputedAggStore) withReadTx(ctx context.Context, fn func(almanacReadTx) error) error {
	return fn(&precomputedAggTx{fakeAggTx: fakeAggTx{f: p.f}, p: p})
}

type precomputedAggTx struct {
	fakeAggTx
	p *precomputedAggStore
}

func (t *precomputedAggTx) tailCounts(_ context.Context, _ []string, _ []int32, _ []string, afterDay, fromDay, toDay int64,
	fn func(ring int, band, region string, day int64, slot int, count int64)) error {
	for _, r := range t.p.tail {
		if r.day > afterDay && r.day >= fromDay && r.day <= toDay {
			fn(r.ring, r.band, r.region, r.day, r.slot, r.count)
		}
	}
	return nil
}

func BenchmarkAlmanacComputeR2(b *testing.B) {
	f := denseAggStore()
	win := almanacWindowFor(aggTestNow.Unix())
	st := newPrecomputedAggStore(f, "JO32", win)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		acc, err := readAlmanacAccum(context.Background(), st, "JO32", win, false)
		if err != nil {
			b.Fatal(err)
		}
		_ = computeAlmanacTypical(acc)
	}
}
