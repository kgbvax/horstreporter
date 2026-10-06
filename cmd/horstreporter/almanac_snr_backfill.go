package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// almanac_snr_backfill.go is the one-off Almanac SNR backfill (KTD13
// follow-up). The SNR counters of dx_region_baseline_daily (snr_spots,
// snr_ge_m20 … snr_ge_0) are only filled by live observe() since the deploy
// that added them, so almanac_snr_since_day starts the day after it. The raw
// PSKReporter spots of the preceding days are still in dx_raw_spots
// (retention ~4 days): this job replays them through the live gates and key
// emitter, writes the counters, re-folds the already folded days so the
// seasonal record gains their SNR histograms, and finally lowers
// almanac_snr_since_day.
//
//   - Range (planAlmanacSNRBackfill): every UTC day D < since−1 that the raw
//     table covers completely (min(spot_time) ≤ start(D)) — counters SET, so
//     a re-run is idempotent — plus the partial deploy day since−1 for the
//     spots before the column-add time (counters ADDed to the live ones,
//     exactly once: guarded by its own dx_meta key in the same transaction).
//   - Only source_type='mqtt' (PSKReporter) rows carry a real SNR; DX-cluster
//     rows ('dxcluster') count toward spot_count only, which the backfill
//     never touches, and RBN/WSPR rows never reach the region baseline. The
//     rows pass the live gates (pskrModeFeedsBaseline, dxBaselineObserveGate,
//     spotCarriesRealSNR) and key emitter (dxPulseRegionBaselineKeysForSpot)
//     unchanged. The live 24 h late-spot clamp is not applied (no ingest time
//     in the raw table); every counter is capped at the row's spot_count.
//   - Writes are batched (almanacSNRBackfillSetBatch / AddBatch keys per
//     transaction, deterministic key order, keys without SNR skipped): each
//     batch COPYs its keys into a temp table and runs UPDATE … FROM it under
//     short statement/lock timeouts; keys missing in the daily table are
//     skipped and counted. A lock timeout backs off and retries the batch.
//     The deploy-day ADD records its last committed batch in dx_meta in the
//     batch's own transaction, so a retry resumes and never adds twice. The
//     raw read is split into hourly windows, each its own statement with a
//     120 s statement_timeout, so memory stays bounded by the day's keys.
//   - Days ≤ the fold watermark are re-folded (refoldDay, SNR stream); the
//     since-day moves (LEAST) and the done key is set in one transaction
//     holding the watermark row lock, after re-checking that no backfilled
//     day was folded (without SNR) by the ticker in the meantime.
//   - Any failure logs an ERROR and leaves the done key unset and the
//     since-day unchanged; the next start retries.

const (
	almanacSNRBackfillDoneKey        = "almanac_snr_backfill_done"
	almanacSNRBackfillPartialDoneKey = "almanac_snr_backfill_partial_done"
	// almanacSNRBackfillPartialProgressKey is the last committed ADD batch
	// index of the deploy day (written in the batch's own transaction), so a
	// retry resumes after it and never adds a batch twice.
	almanacSNRBackfillPartialProgressKey = "almanac_snr_backfill_partial_progress"

	// almanacSNRBackfillStartDelay lets startup (schema, cluster backfill,
	// first fold tick) settle before the backfill reads the raw table.
	almanacSNRBackfillStartDelay = 3 * time.Minute
	// almanacSNRBackfillDayPause throttles between days so the backfill
	// never starves live ingest of the database.
	almanacSNRBackfillDayPause = 5 * time.Second
	// almanacSNRBackfillWindow is one raw read statement (~1.5 M rows/h on
	// prod at the evening peak).
	almanacSNRBackfillWindow = 3600
	// almanacSNRBackfillTxTimeout bounds one raw window read transaction
	// (the statement itself: almanacSNRBackfillStmtTimeoutSQL, 120 s).
	almanacSNRBackfillTxTimeout = 3 * time.Minute
	// Day writes are batched so no transaction rewrites more than a few
	// thousand rows (prod: one 777k-row UPDATE took 119 s). Full past days
	// use almanacSNRBackfillSetBatch keys per transaction; the deploy day,
	// whose rows the live flush upserts every ~2 s, uses small ADD batches so
	// its row locks are held well under a second.
	almanacSNRBackfillSetBatch = 20000
	almanacSNRBackfillAddBatch = 2000 // prod: 5k keys held today's rows 1.8 s; 2k keeps flush waits <1 s
	// almanacSNRBackfillBatchPause throttles between batches.
	almanacSNRBackfillBatchPause = 200 * time.Millisecond
	// almanacSNRBackfillBatchAttempts / BackoffBase: a batch that hits
	// lock_timeout is retried with exponential backoff (1 s, 2 s, 4 s, …).
	almanacSNRBackfillBatchAttempts    = 6
	almanacSNRBackfillBatchBackoffBase = time.Second
	almanacSNRBackfillBatchTxTimeout   = time.Minute
	// almanacSNRBackfillFinalizeRounds bounds the refold/finalize loop.
	almanacSNRBackfillFinalizeRounds = 4

	almanacSNRBackfillSourceType = "mqtt" // PSKReporter rows (prop_intel_sources.go)
)

var errAlmanacSNRCutoffUnknown = errors.New("almanac SNR backfill: the deploy-day cutoff is unknown (dx_meta " +
	almanacSNRColumnsAddedKey + " missing): set -almanac-snr-backfill-cutoff-unix")

// ---------------------------------------------------------------------------
// Row replay (parity with live ingest)
// ---------------------------------------------------------------------------

// almanacSNRRawRow is one dx_raw_spots row as the backfill reads it.
type almanacSNRRawRow struct {
	SpotTime    int64
	Band        string
	SenderLoc   string
	ReceiverLoc string
	Mode        string
	SNR         int
}

// almanacSNRBackfillObserve replays one PSKReporter raw row through the live
// ingest gates (ingestPSKRMessage → DxBaselineEngine.Observe → observe) and
// adds its region-key deltas to agg, exactly as observeSpot does live.
// Returns whether the row emitted any key.
func almanacSNRBackfillObserve(agg map[dxPulseRegionBaselineDailyKey]regionDelta, r almanacSNRRawRow) bool {
	m := MQTTMessage{T: r.SpotTime, B: r.Band, SL: r.SenderLoc, RL: r.ReceiverLoc, MD: r.Mode, RP: r.SNR}
	if !pskrModeFeedsBaseline(strings.ToUpper(strings.TrimSpace(m.MD))) {
		return false
	}
	band, ok := dxBaselineObserveGate(m)
	if !ok {
		return false
	}
	keys := dxPulseRegionBaselineKeysForSpot(m.T, band, m.SL, m.RL)
	if len(keys) == 0 {
		return false
	}
	hasSNR := spotCarriesRealSNR(m)
	for _, k := range keys {
		d := agg[k]
		d.observeSpot(m.RP, hasSNR)
		agg[k] = d
	}
	return true
}

// almanacSNRBackfillMerge models the day UPDATE for one key: SET (full past
// day) or ADD (partial deploy day) the SNR counters, each capped at the
// row's spot_count (Count is never changed).
func almanacSNRBackfillMerge(existing, fresh regionDelta, add bool) regionDelta {
	out := existing
	capAt := func(v int64) int64 { return min(v, existing.Count) }
	if add {
		out.SNR = capAt(existing.SNR + fresh.SNR)
		for i := range out.GE {
			out.GE[i] = capAt(existing.GE[i] + fresh.GE[i])
		}
		return out
	}
	out.SNR = capAt(fresh.SNR)
	for i := range out.GE {
		out.GE[i] = capAt(fresh.GE[i])
	}
	return out
}

// ---------------------------------------------------------------------------
// Range
// ---------------------------------------------------------------------------

// almanacSNRBackfillPlan is what one run backfills.
type almanacSNRBackfillPlan struct {
	// FullDays are SET from all of the day's raw rows, oldest first.
	FullDays []int64
	// PartialDay (since−1) is ADDed from its raw rows before Cutoff, unless
	// PartialDone (a previous run already added it).
	PartialDay  int64
	HasPartial  bool
	PartialDone bool
	Cutoff      int64
	// NewSince is the SNR collection start after success (== the old one
	// when nothing can be backfilled).
	NewSince int64
}

// Days lists every backfilled day (full days and the partial day), oldest
// first: the re-fold candidates.
func (p almanacSNRBackfillPlan) Days() []int64 {
	out := append([]int64(nil), p.FullDays...)
	if p.HasPartial {
		out = append(out, p.PartialDay)
	}
	return out
}

// planAlmanacSNRBackfill determines the range. rawMin is min(spot_time) of
// the PSKReporter raw rows (hasRaw false: none), since the current
// almanac_snr_since_day, cutoff the column-add time (0 = unknown).
//
// The SNR start can only move down over a contiguous run of complete days
// ending at since−1, so the partial deploy day since−1 must itself be
// covered (min(spot_time) ≤ its start); otherwise nothing is backfilled.
// Full days are those with min(spot_time) ≤ start(D), D < since−1.
func planAlmanacSNRBackfill(rawMin int64, hasRaw bool, since, cutoff int64, partialDone bool) (almanacSNRBackfillPlan, error) {
	plan := almanacSNRBackfillPlan{NewSince: since}
	partial := since - 1
	if !hasRaw || rawMin > partial*86400 {
		return plan, nil // the deploy day is not covered: nothing contiguous to add
	}
	if cutoff <= 0 {
		return plan, errAlmanacSNRCutoffUnknown
	}
	if cutoff <= partial*86400 || cutoff > (partial+1)*86400 {
		return plan, fmt.Errorf("almanac SNR backfill: cutoff %d (%s) is not inside the deploy day %s (since-day %d − 1)",
			cutoff, time.Unix(cutoff, 0).UTC().Format(time.RFC3339), almanacDayString(partial), since)
	}
	first := rawMin / 86400
	if rawMin > first*86400 {
		first++ // the first day starting at or after rawMin
	}
	for d := first; d < partial; d++ {
		plan.FullDays = append(plan.FullDays, d)
	}
	plan.PartialDay, plan.HasPartial = partial, true
	plan.PartialDone = partialDone
	plan.Cutoff = cutoff
	plan.NewSince = partial
	if len(plan.FullDays) > 0 {
		plan.NewSince = plan.FullDays[0]
	}
	return plan, nil
}

// ---------------------------------------------------------------------------
// Status (/api/stats postgres.almanac_snr_backfill)
// ---------------------------------------------------------------------------

type almanacSNRBackfillStatus struct {
	mu             sync.Mutex
	enabled        bool
	running        bool
	done           bool
	daysBackfilled int
	sinceDay       int64
	lastError      string
	lastErrorUnix  int64
}

// almanacSNRBackfillState is the process-wide status (nil: never started).
var almanacSNRBackfillState *almanacSNRBackfillStatus

func (s *almanacSNRBackfillStatus) update(f func(*almanacSNRBackfillStatus)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	f(s)
	s.mu.Unlock()
}

// almanacSNRBackfillStatsBlock is postgres.almanac_snr_backfill in /api/stats.
type almanacSNRBackfillStatsBlock struct {
	Enabled        bool   `json:"enabled"`
	Running        bool   `json:"running"`
	Done           bool   `json:"done"`
	DaysBackfilled int    `json:"days_backfilled"`
	SinceDay       int64  `json:"since_day,omitempty"`
	LastError      string `json:"last_error,omitempty"`
	LastErrorUnix  int64  `json:"last_error_unix,omitempty"`
}

func (s *almanacSNRBackfillStatus) stats() *almanacSNRBackfillStatsBlock {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return &almanacSNRBackfillStatsBlock{
		Enabled:        s.enabled,
		Running:        s.running,
		Done:           s.done,
		DaysBackfilled: s.daysBackfilled,
		SinceDay:       s.sinceDay,
		LastError:      s.lastError,
		LastErrorUnix:  s.lastErrorUnix,
	}
}

// ---------------------------------------------------------------------------
// Runner
// ---------------------------------------------------------------------------

// almanacSNRBackfillMeta is the dx_meta state the backfill reads.
type almanacSNRBackfillMeta struct {
	Done        bool
	Since       int64
	HasSince    bool
	AddedUnix   int64 // 0 = not recorded
	PartialDone bool
	// PartialProgress is the last committed deploy-day ADD batch (-1 none).
	PartialProgress int
}

// almanacSNRDayScan / almanacSNRDayWrite are per-day counts for the log.
type almanacSNRDayScan struct {
	Rows, Accepted int64
}

type almanacSNRDayWrite struct {
	Keys, Written, Updated, Missing int64
	Batches, Resumed                int
}

// almanacSNRKeyDelta is one daily key's backfilled counters.
type almanacSNRKeyDelta struct {
	Key   dxPulseRegionBaselineDailyKey
	Delta regionDelta
}

// almanacSNRBackfillEntries lists the day's keys that carry SNR (a key
// with snr_spots = 0 changes nothing: SET writes 0 over 0 on past days, ADD
// adds 0), in a deterministic order so the deploy day's ADD batches are
// identical across retries.
func almanacSNRBackfillEntries(agg map[dxPulseRegionBaselineDailyKey]regionDelta, day int64) []almanacSNRKeyDelta {
	out := make([]almanacSNRKeyDelta, 0, len(agg))
	for k, d := range agg {
		if k.DayIndex != day || d.SNR <= 0 {
			continue
		}
		out = append(out, almanacSNRKeyDelta{k, d})
	}
	slices.SortFunc(out, func(a, b almanacSNRKeyDelta) int {
		return cmp.Or(
			cmp.Compare(a.Key.TargetGrid4, b.Key.TargetGrid4),
			cmp.Compare(a.Key.Band, b.Key.Band),
			cmp.Compare(a.Key.SlotOfDay, b.Key.SlotOfDay),
			cmp.Compare(a.Key.Region, b.Key.Region),
		)
	})
	return out
}

// almanacSNRBackfillBatches splits n entries into [from, to) ranges of at
// most size. n = 0 yields no batch.
func almanacSNRBackfillBatches(n, size int) [][2]int {
	var out [][2]int
	for from := 0; from < n; from += size {
		out = append(out, [2]int{from, min(from+size, n)})
	}
	return out
}

// almanacSNRBatchSpec describes one write transaction.
type almanacSNRBatchSpec struct {
	Add    bool  // deploy-day ADD (else SET)
	Index  int   // ADD batch index (progress key)
	Last   bool  // last ADD batch: also sets the partial-done key
	Cutoff int64 // recorded in the partial-done key
}

// almanacSNRRetryable: the batch hit lock_timeout (SQLSTATE 55P03) and is
// retried after a backoff.
func almanacSNRRetryable(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "55P03"
}

// almanacSNRBackfillStore is the persistence side (pgAlmanacSNRBackfillStore
// in production, an in-memory model in tests).
type almanacSNRBackfillStore interface {
	loadMeta(ctx context.Context) (almanacSNRBackfillMeta, error)
	// rawCoverageStart is min(spot_time) of the PSKReporter raw rows.
	rawCoverageStart(ctx context.Context) (int64, bool, error)
	// aggregate replays the PSKReporter raw rows with from ≤ spot_time < to.
	aggregate(ctx context.Context, from, to int64) (map[dxPulseRegionBaselineDailyKey]regionDelta, almanacSNRDayScan, error)
	// writeBatch SETs or ADDs one batch of the day's counters in its own
	// transaction and returns the rows updated. An ADD batch advances
	// almanacSNRBackfillPartialProgressKey from Index−1 to Index in the same
	// transaction; skipped reports that it was already committed (nothing
	// written). The last ADD batch also sets almanacSNRBackfillPartialDoneKey.
	writeBatch(ctx context.Context, day int64, batch []almanacSNRKeyDelta, spec almanacSNRBatchSpec) (updated int64, skipped bool, err error)
	foldWatermark(ctx context.Context) (int64, bool, error)
	refoldDay(ctx context.Context, day int64, withSNR bool) error
	// finalize, under the watermark lock: returns the backfilled days that
	// are folded but not yet re-folded (then nothing is written); otherwise
	// lowers the since-day to newSince (LEAST) and sets the done key.
	finalize(ctx context.Context, newSince int64, days []int64, refolded map[int64]bool) ([]int64, error)
}

type almanacSNRBackfillRunner struct {
	store      almanacSNRBackfillStore
	cutoffFlag int64 // -almanac-snr-backfill-cutoff-unix (0 = dx_meta)
	status     *almanacSNRBackfillStatus
	pause      func(ctx context.Context, d time.Duration) error
	setBatch   int
	addBatch   int
}

func almanacSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// run performs the backfill once. A nil error means the done key is set
// (or was already set).
func (r *almanacSNRBackfillRunner) run(ctx context.Context) (err error) {
	r.status.update(func(s *almanacSNRBackfillStatus) { s.running = true })
	defer func() {
		r.status.update(func(s *almanacSNRBackfillStatus) {
			s.running = false
			if err != nil {
				s.lastError = err.Error()
				s.lastErrorUnix = time.Now().Unix()
			}
		})
		if err != nil {
			logError("almanac SNR backfill failed (done key left unset, since-day unchanged; retried at the next start): %v", err)
		}
	}()

	meta, err := r.store.loadMeta(ctx)
	if err != nil {
		return fmt.Errorf("reading dx_meta: %w", err)
	}
	if meta.Done {
		r.status.update(func(s *almanacSNRBackfillStatus) { s.done, s.sinceDay = true, meta.Since })
		logInfo("almanac SNR backfill: already done")
		return nil
	}
	if !meta.HasSince {
		return errors.New("almanac SNR backfill: " + almanacSNRSinceKey + " is not recorded (SNR columns missing?)")
	}
	rawMin, hasRaw, err := r.store.rawCoverageStart(ctx)
	if err != nil {
		return fmt.Errorf("raw coverage: %w", err)
	}
	cutoff := meta.AddedUnix
	if r.cutoffFlag > 0 {
		cutoff = r.cutoffFlag
	}
	plan, err := planAlmanacSNRBackfill(rawMin, hasRaw, meta.Since, cutoff, meta.PartialDone)
	if err != nil {
		return err
	}
	logInfo("almanac SNR backfill: raw PSKReporter rows from %s, since-day %s; full days %d, partial day %v (cutoff %d, already added %v) → since-day %s",
		time.Unix(rawMin, 0).UTC().Format(time.RFC3339), almanacDayString(meta.Since), len(plan.FullDays),
		plan.HasPartial, plan.Cutoff, plan.PartialDone, almanacDayString(plan.NewSince))

	backfilled := 0
	for i, day := range plan.FullDays {
		if i > 0 {
			if err := r.pause(ctx, almanacSNRBackfillDayPause); err != nil {
				return err
			}
		}
		if err := r.backfillDay(ctx, day, day*86400, (day+1)*86400, false, 0, -1); err != nil {
			return fmt.Errorf("day %s: %w", almanacDayString(day), err)
		}
		backfilled++
		r.status.update(func(s *almanacSNRBackfillStatus) { s.daysBackfilled = backfilled })
	}
	if plan.HasPartial {
		if !plan.PartialDone {
			if len(plan.FullDays) > 0 {
				if err := r.pause(ctx, almanacSNRBackfillDayPause); err != nil {
					return err
				}
			}
			d := plan.PartialDay
			if err := r.backfillDay(ctx, d, d*86400, plan.Cutoff, true, plan.Cutoff, meta.PartialProgress); err != nil {
				return fmt.Errorf("partial day %s: %w", almanacDayString(d), err)
			}
		}
		backfilled++
		r.status.update(func(s *almanacSNRBackfillStatus) { s.daysBackfilled = backfilled })
	}

	if err := r.refoldAndFinalize(ctx, plan); err != nil {
		return err
	}
	r.status.update(func(s *almanacSNRBackfillStatus) { s.done, s.sinceDay = true, plan.NewSince })
	logInfo("almanac SNR backfill done: %d day(s) backfilled, since-day %s", backfilled, almanacDayString(plan.NewSince))
	return nil
}

// backfillDay aggregates [from, to) and writes the day in batches,
// re-checking the raw coverage after the read so a concurrent raw retention
// prune can't leave a truncated day SET. For the deploy day (add) the
// batches up to progress were committed by an earlier run and are skipped.
func (r *almanacSNRBackfillRunner) backfillDay(ctx context.Context, day, from, to int64, add bool, cutoff int64, progress int) error {
	started := time.Now()
	agg, scan, err := r.store.aggregate(ctx, from, to)
	if err != nil {
		return fmt.Errorf("raw read: %w", err)
	}
	rawMin, hasRaw, err := r.store.rawCoverageStart(ctx)
	if err != nil {
		return fmt.Errorf("raw coverage: %w", err)
	}
	if !hasRaw || rawMin > day*86400 {
		return fmt.Errorf("raw rows of the day were pruned during the read (min spot_time %d)", rawMin)
	}
	readDur := time.Since(started)

	entries := almanacSNRBackfillEntries(agg, day)
	size := r.setBatch
	if add {
		size = r.addBatch
	}
	batches := almanacSNRBackfillBatches(len(entries), size)
	if add && len(batches) == 0 {
		batches = [][2]int{{0, 0}} // still records the partial-done key
	}
	w := almanacSNRDayWrite{Keys: int64(len(agg)), Written: int64(len(entries)), Batches: len(batches)}
	for i, b := range batches {
		if add && i <= progress {
			w.Resumed++
			continue
		}
		if i > 0 {
			if err := r.pause(ctx, almanacSNRBackfillBatchPause); err != nil {
				return err
			}
		}
		spec := almanacSNRBatchSpec{Add: add, Index: i, Last: i == len(batches)-1, Cutoff: cutoff}
		batch := entries[b[0]:b[1]]
		upd, skipped, err := r.writeBatchRetry(ctx, day, batch, spec)
		if err != nil {
			return fmt.Errorf("write batch %d/%d: %w", i+1, len(batches), err)
		}
		if skipped {
			w.Resumed++
			continue
		}
		w.Updated += upd
		w.Missing += int64(len(batch)) - upd
	}
	mode := "SET"
	if add {
		mode = "ADD"
	}
	logInfo("almanac SNR backfill: day %s %s: %d raw rows (%d accepted), %d keys (%d with SNR) in %d batch(es) (%d already committed), %d rows updated, %d keys missing in dx_region_baseline_daily (skipped); read %s, total %s",
		almanacDayString(day), mode, scan.Rows, scan.Accepted, w.Keys, w.Written, w.Batches, w.Resumed, w.Updated, w.Missing,
		readDur.Round(time.Millisecond), time.Since(started).Round(time.Millisecond))
	return nil
}

// writeBatchRetry writes one batch, backing off and retrying when it hits
// lock_timeout (live flush holding the rows).
func (r *almanacSNRBackfillRunner) writeBatchRetry(ctx context.Context, day int64, batch []almanacSNRKeyDelta, spec almanacSNRBatchSpec) (int64, bool, error) {
	backoff := almanacSNRBackfillBatchBackoffBase
	for attempt := 1; ; attempt++ {
		upd, skipped, err := r.store.writeBatch(ctx, day, batch, spec)
		if err == nil || !almanacSNRRetryable(err) || attempt >= almanacSNRBackfillBatchAttempts {
			return upd, skipped, err
		}
		logInfo("almanac SNR backfill: day %s batch %d hit lock_timeout (attempt %d), retrying in %s", almanacDayString(day), spec.Index, attempt, backoff)
		if err := r.pause(ctx, backoff); err != nil {
			return 0, false, err
		}
		backoff *= 2
	}
}

// refoldAndFinalize re-folds every backfilled day the fold has already
// passed, then (under the watermark lock) moves the since-day and sets the
// done key — unless the fold ticker folded another backfilled day meanwhile,
// in which case that day is re-folded first.
func (r *almanacSNRBackfillRunner) refoldAndFinalize(ctx context.Context, plan almanacSNRBackfillPlan) error {
	days := plan.Days()
	refolded := map[int64]bool{}
	for round := 0; round < almanacSNRBackfillFinalizeRounds; round++ {
		w, ok, err := r.store.foldWatermark(ctx)
		if err != nil {
			return fmt.Errorf("fold watermark: %w", err)
		}
		for _, d := range days {
			if !ok || d > w || refolded[d] {
				continue
			}
			started := time.Now()
			dctx, cancel := context.WithTimeout(ctx, almanacFoldDayTimeout)
			err := r.store.refoldDay(dctx, d, true)
			cancel()
			if err != nil {
				return fmt.Errorf("re-fold %s: %w", almanacDayString(d), err)
			}
			refolded[d] = true
			logInfo("almanac SNR backfill: day %s re-folded with SNR in %s", almanacDayString(d), time.Since(started).Round(time.Millisecond))
		}
		pending, err := r.store.finalize(ctx, plan.NewSince, days, refolded)
		if err != nil {
			return fmt.Errorf("finalize: %w", err)
		}
		if len(pending) == 0 {
			return nil
		}
		logInfo("almanac SNR backfill: %d backfilled day(s) folded meanwhile, re-folding", len(pending))
	}
	return errors.New("finalize: the fold kept advancing over backfilled days")
}

// startAlmanacSNRBackfill runs the backfill once in the background after
// almanacSNRBackfillStartDelay. cutoffFlag overrides the dx_meta cutoff.
func startAlmanacSNRBackfill(st *dxPostgresStore, cutoffFlag int64) {
	status := &almanacSNRBackfillStatus{enabled: true}
	almanacSNRBackfillState = status
	if !st.snrColumns.Load() {
		status.update(func(s *almanacSNRBackfillStatus) { s.lastError = "SNR columns unavailable" })
		logInfo("almanac SNR backfill skipped: the SNR columns are unavailable in this run")
		return
	}
	r := &almanacSNRBackfillRunner{
		store:      &pgAlmanacSNRBackfillStore{pool: st.pool, fold: &pgAlmanacFoldStore{pool: st.pool}},
		cutoffFlag: cutoffFlag,
		status:     status,
		pause:      almanacSleep,
		setBatch:   almanacSNRBackfillSetBatch,
		addBatch:   almanacSNRBackfillAddBatch,
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-st.stopCh
		cancel()
	}()
	go func() {
		if almanacSleep(ctx, almanacSNRBackfillStartDelay) != nil {
			return
		}
		if r.run(ctx) == nil {
			almanacSvc.purgeCaches() // views pick up the SNR days at once
		}
	}()
}

// ---------------------------------------------------------------------------
// Postgres store
// ---------------------------------------------------------------------------

const (
	almanacSNRBackfillMetaSQL = `SELECT k, v FROM dx_meta WHERE k = ANY($1::text[])`

	// Served by idx_dx_raw_spots_source_type_spot_time (min() → index scan
	// LIMIT 1 on the equality prefix).
	almanacSNRBackfillRawMinSQL = `SELECT min(spot_time) FROM dx_raw_spots WHERE source_type = $1::text`

	almanacSNRBackfillStmtTimeoutSQL = `SET LOCAL statement_timeout = '120s'`

	// One hourly window of PSKReporter rows via the (source_type, spot_time)
	// index; all gates run in Go (almanacSNRBackfillObserve).
	almanacSNRBackfillStreamSQL = `
		SELECT spot_time, band, sender_locator, receiver_locator, mode, signal_report_db
		FROM dx_raw_spots
		WHERE source_type = $1::text AND spot_time >= $2::bigint AND spot_time < $3::bigint`

	almanacSNRBackfillTempSQL = `
		CREATE TEMP TABLE IF NOT EXISTS almanac_snr_backfill_keys (
			target_grid4 TEXT NOT NULL,
			band TEXT NOT NULL,
			slot_of_day INTEGER NOT NULL,
			region TEXT NOT NULL,
			snr_spots INTEGER NOT NULL,
			snr_ge_m20 INTEGER NOT NULL,
			snr_ge_m15 INTEGER NOT NULL,
			snr_ge_m10 INTEGER NOT NULL,
			snr_ge_m5 INTEGER NOT NULL,
			snr_ge_0 INTEGER NOT NULL
		) ON COMMIT DROP`

	// Per-batch write limits. SET batches (past days): 30 s / lock 5 s. ADD
	// batches (deploy day, rows the live flush upserts): 10 s / lock 2 s.
	almanacSNRBackfillSetStmtTimeoutSQL = `SET LOCAL statement_timeout = '30s'`
	almanacSNRBackfillSetLockTimeoutSQL = `SET LOCAL lock_timeout = '5s'`
	almanacSNRBackfillAddStmtTimeoutSQL = `SET LOCAL statement_timeout = '10s'`
	almanacSNRBackfillAddLockTimeoutSQL = `SET LOCAL lock_timeout = '2s'`

	// Exactly-once guard of each deploy-day ADD batch (same transaction):
	// advances the progress key from $3 (= batch − 1; −1 = no row yet) to
	// $2. 0 rows affected = the batch was already committed.
	almanacSNRBackfillProgressSQL = `
		INSERT INTO dx_meta (k, v) VALUES ($1::text, $2::text)
		ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v
		WHERE dx_meta.v::bigint = $3::bigint`

	// Set with the last ADD batch.
	almanacSNRBackfillPartialGuardSQL = `INSERT INTO dx_meta (k, v) VALUES ($1::text, $2::text) ON CONFLICT (k) DO NOTHING`

	almanacSNRBackfillWatermarkAdvisorySQL = `SELECT pg_advisory_xact_lock(hashtext($1::text))`

	// Only ever lowers the SNR start.
	almanacSNRBackfillSinceSQL = `
		UPDATE dx_meta SET v = LEAST(v::bigint, $2::bigint)::text
		WHERE k = $1::text`

	almanacSNRBackfillDoneSQL = `
		INSERT INTO dx_meta (k, v) VALUES ($1::text, $2::text)
		ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v`
)

// almanacSNRBackfillPlanStmts pin each batch UPDATE's plan: a batch is at
// most 20k keys, so the right plan is a nested loop over the temp table with
// a primary-key probe per key. A hash join would scan the whole day
// (~777k rows via the day_index index, ~5 s on prod) once per batch while
// holding the batch's row locks. ANALYZE gives the temp table statistics.
var almanacSNRBackfillPlanStmts = []string{
	`ANALYZE almanac_snr_backfill_keys`,
	`SET LOCAL enable_hashjoin = off`,
	`SET LOCAL enable_mergejoin = off`,
}

// almanacSNRBackfillUpdateSQL is the day UPDATE: SET (add false) or ADD the
// SNR counters, each capped at spot_count. $1 is the day.
func almanacSNRBackfillUpdateSQL(add bool) string {
	var b strings.Builder
	b.WriteString("\n\t\tUPDATE dx_region_baseline_daily AS d SET\n")
	for i, c := range regionSNRColumnNames {
		v := "t." + c
		if add {
			v = "d." + c + " + t." + c
		}
		sep := ","
		if i == len(regionSNRColumnNames)-1 {
			sep = ""
		}
		fmt.Fprintf(&b, "\t\t\t%s = LEAST(%s, d.spot_count)%s\n", c, v, sep)
	}
	b.WriteString(`		FROM almanac_snr_backfill_keys t
		WHERE d.day_index = $1::bigint
		  AND d.target_grid4 = t.target_grid4 AND d.band = t.band
		  AND d.slot_of_day = t.slot_of_day AND d.region = t.region`)
	return b.String()
}

var (
	almanacSNRBackfillSetSQL = almanacSNRBackfillUpdateSQL(false)
	almanacSNRBackfillAddSQL = almanacSNRBackfillUpdateSQL(true)
)

var almanacSNRBackfillTempCols = append([]string{"target_grid4", "band", "slot_of_day", "region"}, regionSNRColumnNames...)

type pgAlmanacSNRBackfillStore struct {
	pool *pgxpool.Pool
	fold *pgAlmanacFoldStore
}

func (p *pgAlmanacSNRBackfillStore) loadMeta(ctx context.Context) (almanacSNRBackfillMeta, error) {
	var m almanacSNRBackfillMeta
	m.PartialProgress = -1
	keys := []string{almanacSNRBackfillDoneKey, almanacSNRSinceKey, almanacSNRColumnsAddedKey,
		almanacSNRBackfillPartialDoneKey, almanacSNRBackfillPartialProgressKey}
	rows, err := p.pool.Query(ctx, almanacSNRBackfillMetaSQL, keys)
	if err != nil {
		return m, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return m, err
		}
		switch k {
		case almanacSNRBackfillDoneKey:
			m.Done = true
		case almanacSNRBackfillPartialDoneKey:
			m.PartialDone = true
		case almanacSNRBackfillPartialProgressKey:
			n, perr := strconv.Atoi(v)
			if perr != nil {
				return m, fmt.Errorf("%s %q: %w", k, v, perr)
			}
			m.PartialProgress = n
		case almanacSNRSinceKey:
			n, perr := strconv.ParseInt(v, 10, 64)
			if perr != nil {
				return m, fmt.Errorf("%s %q: %w", k, v, perr)
			}
			m.Since, m.HasSince = n, true
		case almanacSNRColumnsAddedKey:
			n, perr := strconv.ParseInt(v, 10, 64)
			if perr != nil {
				return m, fmt.Errorf("%s %q: %w", k, v, perr)
			}
			m.AddedUnix = n
		}
	}
	return m, rows.Err()
}

func (p *pgAlmanacSNRBackfillStore) rawCoverageStart(ctx context.Context) (int64, bool, error) {
	var v *int64
	if err := p.pool.QueryRow(ctx, almanacSNRBackfillRawMinSQL, almanacSNRBackfillSourceType).Scan(&v); err != nil {
		return 0, false, err
	}
	if v == nil {
		return 0, false, nil
	}
	return *v, true, nil
}

func (p *pgAlmanacSNRBackfillStore) aggregate(ctx context.Context, from, to int64) (map[dxPulseRegionBaselineDailyKey]regionDelta, almanacSNRDayScan, error) {
	agg := make(map[dxPulseRegionBaselineDailyKey]regionDelta, 1<<16)
	var scan almanacSNRDayScan
	for w := from; w < to; w += almanacSNRBackfillWindow {
		end := min(w+almanacSNRBackfillWindow, to)
		if err := p.aggregateWindow(ctx, w, end, agg, &scan); err != nil {
			return nil, scan, err
		}
	}
	return agg, scan, nil
}

// aggregateWindow reads one window in its own read-only transaction (for
// the SET LOCAL statement_timeout), streaming rows into agg.
func (p *pgAlmanacSNRBackfillStore) aggregateWindow(ctx context.Context, from, to int64, agg map[dxPulseRegionBaselineDailyKey]regionDelta, scan *almanacSNRDayScan) error {
	ctx, cancel := context.WithTimeout(ctx, almanacSNRBackfillTxTimeout)
	defer cancel()
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, almanacSNRBackfillStmtTimeoutSQL); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, almanacSNRBackfillStreamSQL, almanacSNRBackfillSourceType, from, to)
	if err != nil {
		return err
	}
	defer rows.Close()
	var r almanacSNRRawRow
	for rows.Next() {
		if err := rows.Scan(&r.SpotTime, &r.Band, &r.SenderLoc, &r.ReceiverLoc, &r.Mode, &r.SNR); err != nil {
			return err
		}
		scan.Rows++
		if almanacSNRBackfillObserve(agg, r) {
			scan.Accepted++
		}
	}
	return rows.Err()
}

func (p *pgAlmanacSNRBackfillStore) writeBatch(ctx context.Context, day int64, batch []almanacSNRKeyDelta, spec almanacSNRBatchSpec) (int64, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, almanacSNRBackfillBatchTxTimeout)
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, stmt := range almanacSNRBatchTimeoutStmts(spec.Add) {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return 0, false, err
		}
	}
	if spec.Add {
		tag, err := tx.Exec(ctx, almanacSNRBackfillProgressSQL, almanacSNRBackfillPartialProgressKey,
			strconv.Itoa(spec.Index), int64(spec.Index-1))
		if err != nil {
			return 0, false, err
		}
		if tag.RowsAffected() == 0 {
			return 0, true, nil // committed by an earlier run
		}
	}
	var updated int64
	if len(batch) > 0 {
		if _, err := tx.Exec(ctx, almanacSNRBackfillTempSQL); err != nil {
			return 0, false, err
		}
		i := 0
		_, err = tx.CopyFrom(ctx, pgx.Identifier{"almanac_snr_backfill_keys"}, almanacSNRBackfillTempCols,
			pgx.CopyFromFunc(func() ([]any, error) {
				if i >= len(batch) {
					return nil, nil
				}
				k, d := batch[i].Key, batch[i].Delta
				i++
				return []any{k.TargetGrid4, k.Band, int32(k.SlotOfDay), k.Region,
					int32(d.SNR), int32(d.GE[0]), int32(d.GE[1]), int32(d.GE[2]), int32(d.GE[3]), int32(d.GE[4])}, nil
			}))
		if err != nil {
			return 0, false, err
		}
		for _, stmt := range almanacSNRBackfillPlanStmts {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return 0, false, err
			}
		}
		sql := almanacSNRBackfillSetSQL
		if spec.Add {
			sql = almanacSNRBackfillAddSQL
		}
		tag, err := tx.Exec(ctx, sql, day)
		if err != nil {
			return 0, false, err
		}
		updated = tag.RowsAffected()
	}
	if spec.Add && spec.Last {
		if _, err := tx.Exec(ctx, almanacSNRBackfillPartialGuardSQL, almanacSNRBackfillPartialDoneKey,
			strconv.FormatInt(spec.Cutoff, 10)); err != nil {
			return 0, false, err
		}
	}
	return updated, false, tx.Commit(ctx)
}

// almanacSNRBatchTimeoutStmts are a batch transaction's first statements.
func almanacSNRBatchTimeoutStmts(add bool) []string {
	if add {
		return []string{almanacSNRBackfillAddStmtTimeoutSQL, almanacSNRBackfillAddLockTimeoutSQL}
	}
	return []string{almanacSNRBackfillSetStmtTimeoutSQL, almanacSNRBackfillSetLockTimeoutSQL}
}

func (p *pgAlmanacSNRBackfillStore) foldWatermark(ctx context.Context) (int64, bool, error) {
	return readAlmanacWatermark(ctx, p.pool, false)
}

func (p *pgAlmanacSNRBackfillStore) refoldDay(ctx context.Context, day int64, withSNR bool) error {
	return p.fold.refoldDay(ctx, day, withSNR)
}

func (p *pgAlmanacSNRBackfillStore) finalize(ctx context.Context, newSince int64, days []int64, refolded map[int64]bool) ([]int64, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Serialize with the watermark's first-time init (advisory lock) and
	// with foldDay/refoldDay/forceAdvance (row lock): a fold after this
	// commit reads the new since-day.
	if _, err := tx.Exec(ctx, almanacSNRBackfillWatermarkAdvisorySQL, almanacFoldWatermarkKey); err != nil {
		return nil, err
	}
	w, ok, err := readAlmanacWatermark(ctx, tx, true)
	if err != nil {
		return nil, err
	}
	if pending := almanacSNRBackfillPending(days, refolded, w, ok); len(pending) > 0 {
		return pending, nil
	}
	if _, err := tx.Exec(ctx, almanacSNRBackfillSinceSQL, almanacSNRSinceKey, newSince); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, almanacSNRBackfillDoneSQL, almanacSNRBackfillDoneKey, strconv.FormatInt(time.Now().Unix(), 10)); err != nil {
		return nil, err
	}
	return nil, tx.Commit(ctx)
}

// almanacSNRBackfillPending lists the backfilled days the fold has passed
// (≤ watermark w) that were not re-folded with SNR.
func almanacSNRBackfillPending(days []int64, refolded map[int64]bool, w int64, hasW bool) []int64 {
	if !hasW {
		return nil
	}
	var out []int64
	for _, d := range days {
		if d <= w && !refolded[d] {
			out = append(out, d)
		}
	}
	return out
}
