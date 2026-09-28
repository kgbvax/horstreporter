package main

import (
	"context"
	"sync/atomic"
	"time"
)

// almanac_fold.go drives the Almanac fold (plan U3, KTD6): every final day of
// dx_region_baseline_daily is folded, one day per transaction, into the
// sparse seasonal record (almanac_season_store.go, encoding in
// almanac_sparse.go), and the fold watermark
// gates the daily table's retention prune so no day is pruned unfolded
// unless the grace period has run out or the disk is full.

const (
	// almanacFoldInterval is the fold ticker period. Independent of
	// -dx-region-baseline-retention-days (the fold runs even with retention 0).
	almanacFoldInterval   = 15 * time.Minute
	almanacFoldFirstDelay = 90 * time.Second
	// almanacFoldDayTimeout bounds one day's fold transaction.
	almanacFoldDayTimeout = 60 * time.Second
	// almanacFoldMaxDaysPerRun bounds one tick (the initial backlog is ~35
	// days; a longer stall catches up over several ticks).
	almanacFoldMaxDaysPerRun = 64
	// almanacFoldGraceDays: unfolded days may outlive the retention cutoff by
	// this many days before a forced prune drops them (skipped when the disk
	// is over almanacDiskFullThreshold).
	almanacFoldGraceDays = 7
	// almanacFoldFlushLag: day d is final only once a baseline flush has
	// succeeded after end(d) + 1 h.
	almanacFoldFlushLag = 3600

	// Live observe() region-key window (KTD6 late spots). Matches the
	// today−2 finality rule: no accepted spot can land on a folded day.
	almanacLateSpotMaxAgeSeconds = 24 * 3600
	almanacFutureSpotSkewSeconds = 10 * 60
)

// almanacRegionTimestampAccepted is the live-ingest clamp for region-baseline
// keys: [now − 24 h, now + 10 min]. Applied in observe() only — never in the
// shared key emitter, so rebuilding the baseline from raw spots still works.
func almanacRegionTimestampAccepted(ts, now int64) bool {
	return ts >= now-almanacLateSpotMaxAgeSeconds && ts <= now+almanacFutureSpotSkewSeconds
}

// almanacDayFinal is the finality rule: day d may be folded only when
//   - d ≤ today(UTC) − 2,
//   - the last successful baseline flush happened after end(d) + 1 h, and
//   - no pending (or in-flight) region delta is for a day ≤ d.
func almanacDayFinal(d, now, lastFlushOK, pendingMinDay int64, hasPending bool) bool {
	if d > utcDayIndex(now)-2 {
		return false
	}
	if lastFlushOK <= (d+1)*86400+almanacFoldFlushLag {
		return false
	}
	if hasPending && pendingMinDay <= d {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// Day fold builder
// ---------------------------------------------------------------------------

// almanacDailyRow is one dx_region_baseline_daily row of the day being folded.
type almanacDailyRow struct {
	Grid4, Band, Region string
	Slot                int
	Count               int64
}

type almanacSegKey struct {
	Grid4, Band, Region string
}

// almanacDayFold accumulates one day: per-(grid, band, region) 48-byte
// segments (uint8 per slot, saturating at 255) and the uncapped per-slot spot
// totals (ingest-slot seed). The (grid, band) pairs active that day are the
// Segments keys' (Grid4, Band) (almanacFoldActivitySQL's SELECT DISTINCT).
type almanacDayFold struct {
	Day        int64
	YearMonth  int
	DayOfMonth int
	Segments   map[almanacSegKey]*[almanacSeasonSlotsPerDay]byte
	SlotTotals [almanacSeasonSlotsPerDay]int64
}

func newAlmanacDayFold(day int64) *almanacDayFold {
	ym, dom := almanacYearMonthDOM(day)
	return &almanacDayFold{
		Day:        day,
		YearMonth:  ym,
		DayOfMonth: dom,
		Segments:   make(map[almanacSegKey]*[almanacSeasonSlotsPerDay]byte, 1024),
	}
}

func (f *almanacDayFold) add(r almanacDailyRow) {
	if r.Slot < 0 || r.Slot >= almanacSeasonSlotsPerDay || r.Count <= 0 {
		return
	}
	k := almanacSegKey{r.Grid4, r.Band, r.Region}
	seg := f.Segments[k]
	if seg == nil {
		seg = new([almanacSeasonSlotsPerDay]byte)
		f.Segments[k] = seg
	}
	satAdd8(&seg[r.Slot], r.Count) // saturate; never wrap
	f.SlotTotals[r.Slot] += r.Count
}

// dayMaskBit is the area-activity bit for this day: 1 << (day_of_month − 1).
func (f *almanacDayFold) dayMaskBit() uint32 { return 1 << uint(f.DayOfMonth-1) }

// nonZeroSlotTotals returns the slots with spots and their totals, for the
// ingest-slot seed (empty slots stay absent = no ingest).
func (f *almanacDayFold) nonZeroSlotTotals() ([]int32, []int64) {
	var slots []int32
	var totals []int64
	for s, t := range f.SlotTotals {
		if t > 0 {
			slots = append(slots, int32(s))
			totals = append(totals, t)
		}
	}
	return slots, totals
}

// almanacYearMonthDOM maps a UTC day index to (yyyymm, day-of-month).
func almanacYearMonthDOM(day int64) (int, int) {
	t := time.Unix(day*86400, 0).UTC()
	return t.Year()*100 + int(t.Month()), t.Day()
}

// almanacSegmentOffset is the 0-based position of day-of-month dom's first
// slot in the logical 31×48 month grid: [(dom−1)*48, dom*48).
func almanacSegmentOffset(dom int) int { return (dom - 1) * almanacSeasonSlotsPerDay }

// almanacInitialWatermark: on first deploy the watermark starts at the oldest
// daily day (which may be partly pruned, so it is recorded lost); folding
// begins at the next day. An empty daily table starts at today − 1.
func almanacInitialWatermark(minDay int64, hasMin bool, today int64) (w, lost int64, hasLost bool) {
	if !hasMin {
		return today - 1, 0, false
	}
	return minDay, minDay, true
}

// ---------------------------------------------------------------------------
// Prune gating
// ---------------------------------------------------------------------------

type almanacPruneDecision struct {
	// DeleteBelow is the effective prune cutoff: delete day_index < DeleteBelow.
	DeleteBelow int64
	// Force: the watermark must first advance to NewWatermark, recording the
	// skipped days as lost.
	Force        bool
	NewWatermark int64
}

// almanacPruneDecide gates the retention cutoff by the fold watermark:
// normally delete only day_index < min(cutoff, watermark+1). Unfolded days
// may outlive the cutoff by graceDays; past that — or at once when the disk
// is over the threshold — a forced prune advances the watermark and drops them.
func almanacPruneDecide(cutoff, watermark int64, graceDays int, diskOver bool) almanacPruneDecision {
	if watermark+1 >= cutoff {
		return almanacPruneDecision{DeleteBelow: cutoff}
	}
	forced := cutoff - int64(graceDays)
	if diskOver {
		forced = cutoff
	}
	if watermark+1 < forced {
		return almanacPruneDecision{DeleteBelow: forced, Force: true, NewWatermark: forced - 1}
	}
	return almanacPruneDecision{DeleteBelow: watermark + 1}
}

// almanacLostDayRange lists the days (from, to] skipped by a forced advance.
func almanacLostDayRange(from, to int64) []int64 {
	if to <= from {
		return nil
	}
	days := make([]int64, 0, to-from)
	for d := from + 1; d <= to; d++ {
		days = append(days, d)
	}
	return days
}

// ---------------------------------------------------------------------------
// Fold driver
// ---------------------------------------------------------------------------

// almanacFoldStore is the persistence side of the fold (pgAlmanacFoldStore in
// production, an in-memory model in tests).
type almanacFoldStore interface {
	ensureWatermark(ctx context.Context, today int64) (int64, error)
	foldDay(ctx context.Context, day int64) (int64, error)
	forceAdvance(ctx context.Context, newWatermark int64, reason string) (int64, error)
	lostDayCount(ctx context.Context) (int64, error)
}

// almanacFlushState is what the finality rule needs from the ingest side
// (implemented by *dxPostgresStore).
type almanacFlushState interface {
	baselineFlushLastOK() int64
	pendingRegionMinDay() (int64, bool)
}

type almanacFolder struct {
	store     almanacFoldStore
	flush     almanacFlushState
	diskPath  string
	diskProbe func(string) (float64, error)
	now       func() time.Time

	watermark  atomic.Int64 // -1 until known
	failStreak atomic.Int64
	lastOKUnix atomic.Int64
	lostDays   atomic.Int64

	// afterFold, when set, runs in the fold goroutine after each successful
	// runOnce (production: pre-warm the configured Almanac areas, U6/KTD10).
	afterFold func()
}

func newAlmanacFolder(store almanacFoldStore, flush almanacFlushState, diskPath string) *almanacFolder {
	f := &almanacFolder{
		store:     store,
		flush:     flush,
		diskPath:  diskPath,
		diskProbe: almanacDiskUsedFraction,
		now:       time.Now,
	}
	f.watermark.Store(-1)
	return f
}

// almanacFoldHealth is the /api/stats view of the fold.
type almanacFoldHealth struct {
	WatermarkDay int64
	FailStreak   int64
	LastOKUnix   int64
	LostDays     int64
}

func (f *almanacFolder) health() almanacFoldHealth {
	return almanacFoldHealth{
		WatermarkDay: f.watermark.Load(),
		FailStreak:   f.failStreak.Load(),
		LastOKUnix:   f.lastOKUnix.Load(),
		LostDays:     f.lostDays.Load(),
	}
}

func (f *almanacFolder) recordFailure(stage string, err error) error {
	n := f.failStreak.Add(1)
	logInfo("almanac fold %s failed (streak=%d): %v", stage, n, err)
	if n == 4 || (n > 4 && n%96 == 0) { // ~1 h, then daily at the 15-min tick
		logError("almanac fold has failed %d times consecutively; seasonal history is not being preserved", n)
	}
	return err
}

func (f *almanacFolder) refreshLostDays(ctx context.Context) {
	if n, err := f.store.lostDayCount(ctx); err == nil {
		f.lostDays.Store(n)
	}
}

// runOnce folds every final day after the watermark (bounded per run).
func (f *almanacFolder) runOnce(ctx context.Context) error {
	now := f.now().Unix()
	today := utcDayIndex(now)
	w, err := f.store.ensureWatermark(ctx, today)
	if err != nil {
		return f.recordFailure("watermark", err)
	}
	f.watermark.Store(w)
	for n := 0; n < almanacFoldMaxDaysPerRun; n++ {
		d := w + 1
		pendingMin, hasPending := f.flush.pendingRegionMinDay()
		if !almanacDayFinal(d, now, f.flush.baselineFlushLastOK(), pendingMin, hasPending) {
			break
		}
		dayCtx, cancel := context.WithTimeout(ctx, almanacFoldDayTimeout)
		started := time.Now()
		nw, err := f.store.foldDay(dayCtx, d)
		cancel()
		if err != nil {
			return f.recordFailure("day "+almanacDayString(d), err)
		}
		logInfo("almanac fold: day %s folded in %s (watermark %d)",
			almanacDayString(d), time.Since(started).Round(time.Millisecond), nw)
		w = nw
		f.watermark.Store(w)
	}
	f.failStreak.Store(0)
	f.lastOKUnix.Store(now)
	f.refreshLostDays(ctx)
	return nil
}

// pruneCutoff returns the effective dx_region_baseline_daily prune cutoff for
// the requested retention cutoff, forcing the watermark forward (and
// recording lost days) when the grace period is exhausted or the disk is full.
func (f *almanacFolder) pruneCutoff(ctx context.Context, cutoff int64) (int64, error) {
	today := utcDayIndex(f.now().Unix())
	w, err := f.store.ensureWatermark(ctx, today)
	if err != nil {
		return 0, err
	}
	frac, perr := f.diskProbe(f.diskPath)
	diskOver := almanacDiskOverThreshold(frac, perr)
	dec := almanacPruneDecide(cutoff, w, almanacFoldGraceDays, diskOver)
	if !dec.Force {
		return dec.DeleteBelow, nil
	}
	n, err := f.store.forceAdvance(ctx, dec.NewWatermark, almanacLostReasonForcedPrune)
	if err != nil {
		return 0, err
	}
	if dec.NewWatermark > f.watermark.Load() {
		f.watermark.Store(dec.NewWatermark)
	}
	if n > 0 {
		logError("almanac: forced prune of dx_region_baseline_daily below day %d (disk_over=%v, probe_err=%v, disk_used=%.2f); %d unfolded day(s) recorded in almanac_lost_days",
			dec.DeleteBelow, diskOver, perr, frac, n)
	}
	f.refreshLostDays(ctx)
	return dec.DeleteBelow, nil
}

// startAlmanacFold wires the Postgres fold driver into the store (which gates
// the daily prune from then on) and starts its ticker. prewarmAreas (the
// parsed -almanac-wspr-backfill-areas) are pre-warmed after each fold.
func startAlmanacFold(st *dxPostgresStore, diskPath string, prewarmAreas []string) *almanacFolder {
	f := newAlmanacFolder(&pgAlmanacFoldStore{pool: st.pool}, st, diskPath)
	st.setAlmanacFolder(f)
	f.afterFold = almanacPrewarmHook(prewarmAreas)
	if frac, err := almanacDiskUsedFraction(diskPath); err != nil {
		logInfo("almanac fold enabled; disk probe unavailable (%v): prune grace period disabled (fail-safe)", err)
	} else {
		logInfo("almanac fold enabled (disk %s at %.0f%% used)", diskPath, frac*100)
	}
	f.start(st.stopCh)
	return f
}

// start runs the fold ticker until stop closes.
func (f *almanacFolder) start(stop <-chan struct{}) {
	go func() {
		t := time.NewTimer(almanacFoldFirstDelay)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
			}
			f.tick(context.Background())
			t.Reset(almanacFoldInterval)
		}
	}()
}

// tick is one fold run followed, on success, by the afterFold hook.
func (f *almanacFolder) tick(ctx context.Context) {
	if err := f.runOnce(ctx); err != nil || f.afterFold == nil {
		return
	}
	f.afterFold()
}

// almanacPrewarmHook returns the afterFold hook that fills the Almanac cache
// for the configured areas after a fold (the fold moves the watermark, which
// invalidates their typical part), so the widget summary field (KTD10) finds
// them warm. almanacSvc is read per call. Errors are logged, never fatal.
func almanacPrewarmHook(areas []string) func() {
	return func() { almanacPrewarmAreas(almanacSvc, areas) }
}

// almanacPrewarmAreas runs the full read path for each grid4 area.
func almanacPrewarmAreas(svc *almanacService, areas []string) {
	if svc == nil {
		return
	}
	for _, a := range areas {
		if _, err := svc.get(a); err != nil {
			logInfo("almanac pre-warm %s failed: %v", a, err)
		}
	}
}
