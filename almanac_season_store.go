package main

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"maps"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// almanac_season_store.go owns the Almanac seasonal record (plan U3, KTD5):
// three LOGGED tables that preserve every final day of
// dx_region_baseline_daily before the retention prune removes it, plus the
// fold transaction SQL. The counts encoding lives in almanac_sparse.go. The fold driver and its pure decision logic live in
// almanac_fold.go.
//
// NEVER make these tables UNLOGGED: they are never pruned and cannot be
// rebuilt once the daily rows are gone (see the Sep 7 dx_raw_spots loss).

const (
	// almanacSeasonLayerPSKR is the PSKReporter/DX-cluster layer (live ingest).
	almanacSeasonLayerPSKR = "pskr"

	almanacSeasonSlotsPerDay  = 48
	almanacSeasonDaysPerMonth = 31
	// almanacSeasonCountsLen is the logical month grid: 31 days × 48 slots,
	// one uint8 per cell (capped at 255), day-major (pos = (dom−1)*48 +
	// slot). It is stored sparse (almanac_sparse.go), not as a dense bytea.
	almanacSeasonCountsLen = almanacSeasonDaysPerMonth * almanacSeasonSlotsPerDay

	almanacFoldWatermarkKey = "almanac_fold_watermark_day"
	// almanacFoldFetchBatch is the cursor batch size of the fold's
	// read-modify-write (must match almanacFoldFetchSQL).
	almanacFoldFetchBatch = 4096

	almanacLostReasonInitial     = "initial_partial"
	almanacLostReasonForcedPrune = "forced_prune"
)

// almanacSeasonSchemaStmts is the always-applied Almanac DDL, appended to
// initSchemaStmts. No index is added to dx_region_baseline_daily here: if a
// reader needs one it ships as a scripts/migrate_*.sql CONCURRENTLY script.
func almanacSeasonSchemaStmts() []string {
	return []string{
		// Seasonal record: one row per (grid4, band, far-end region, month,
		// layer). counts is the sparse encoding of the 31×48 day-major uint8
		// month grid (almanac_sparse.go: version byte + uvarint gap / count
		// pairs, ≈2 B per non-zero cell). Rows stay well under the ~2 KB TOAST
		// threshold, so Postgres never compresses them: the encoding itself
		// is the compression. fillfactor leaves room for the daily fold
		// rewrites to stay HOT.
		// LOGGED on purpose — never UNLOGGED.
		`CREATE TABLE IF NOT EXISTS almanac_season_counts (
			grid4 TEXT NOT NULL,
			band TEXT NOT NULL,
			region TEXT NOT NULL,
			year_month INTEGER NOT NULL,
			layer TEXT NOT NULL,
			counts BYTEA NOT NULL,
			PRIMARY KEY (grid4, band, region, year_month, layer)
		) WITH (fillfactor = 70);`,
		// Area activity: bit (day_of_month-1) set when grid4 had any spot on
		// band that day (to any region, its own included). LOGGED — never
		// UNLOGGED.
		`CREATE TABLE IF NOT EXISTS almanac_area_activity (
			grid4 TEXT NOT NULL,
			band TEXT NOT NULL,
			year_month INTEGER NOT NULL,
			layer TEXT NOT NULL,
			day_mask INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (grid4, band, year_month, layer)
		);`,
		// Ingest totals per (day, 30-min slot, layer), in the region-key unit
		// (sum of spot_count). Maintained by the baseline flush; seeded by the
		// fold for days that predate it. LOGGED — never UNLOGGED.
		`CREATE TABLE IF NOT EXISTS almanac_ingest_slots (
			day_index BIGINT NOT NULL,
			slot_of_day INTEGER NOT NULL,
			layer TEXT NOT NULL,
			spot_total BIGINT NOT NULL,
			PRIMARY KEY (day_index, slot_of_day, layer)
		);`,
		// Days whose daily rows were (or may have been) pruned before they
		// were folded. They read as "unknown", never "closed". LOGGED.
		`CREATE TABLE IF NOT EXISTS almanac_lost_days (
			day_index BIGINT PRIMARY KEY,
			reason TEXT NOT NULL,
			recorded_at BIGINT NOT NULL
		);`,
	}
}

// almanacSeasonMaintenanceStmts are best-effort (logged and skipped on error)
// storage settings, run with initSchema's optional maintenance statements.
func almanacSeasonMaintenanceStmts() [][2]string {
	return [][2]string{
		{
			"almanac_season_counts fillfactor",
			`ALTER TABLE almanac_season_counts SET (fillfactor = 70);`,
		},
		{
			// Drops the toast_tuple_target = 128 of the earlier dense format
			// from tables created before the sparse encoding (sparse rows
			// are stored inline; the setting is meaningless). No-op otherwise.
			"almanac_season_counts reset toast target",
			`ALTER TABLE almanac_season_counts RESET (toast_tuple_target);`,
		},
		{
			// Every fold rewrites the day's rows (a new tuple version per
			// affected key): vacuum the heap aggressively.
			"tune autovacuum almanac_season_counts",
			`ALTER TABLE almanac_season_counts SET (
				autovacuum_vacuum_scale_factor = 0.02,
				autovacuum_vacuum_threshold = 5000,
				autovacuum_analyze_scale_factor = 0.02
			);`,
		},
	}
}

// almanacIngestSlotFlushSQL adds one flush batch's per-slot totals. Runs in
// the baseline flush transaction, so a failed flush writes no totals and the
// merged-back region deltas re-derive them on the next flush.
const almanacIngestSlotFlushSQL = `
	INSERT INTO almanac_ingest_slots (day_index, slot_of_day, layer, spot_total)
	VALUES ($1,$2,$3,$4)
	ON CONFLICT (day_index, slot_of_day, layer)
	DO UPDATE SET spot_total = almanac_ingest_slots.spot_total + EXCLUDED.spot_total
`

// Fold transaction SQL (one final day per transaction). The Go model of these
// statements is fakeAlmanacFoldStore in almanac_fold_test.go.
const (
	almanacFoldLockWatermarkSQL = `SELECT v FROM dx_meta WHERE k = $1 FOR UPDATE`

	// Streams one day via idx_dx_region_baseline_daily_day_index.
	almanacFoldStreamSQL = `
		SELECT target_grid4, band, region, slot_of_day, spot_count
		FROM dx_region_baseline_daily
		WHERE day_index = $1`

	// The day's affected (grid4, band, region) keys. Drives the read of the
	// existing rows and the activity upsert.
	almanacFoldTempTableSQL = `
		CREATE TEMP TABLE IF NOT EXISTS almanac_fold_seg (
			grid4 TEXT NOT NULL,
			band TEXT NOT NULL,
			region TEXT NOT NULL
		) ON COMMIT DROP`

	// The re-encoded rows (merged existing + fresh) for the final upsert.
	almanacFoldNewTableSQL = `
		CREATE TEMP TABLE IF NOT EXISTS almanac_fold_new (
			grid4 TEXT NOT NULL,
			band TEXT NOT NULL,
			region TEXT NOT NULL,
			counts BYTEA NOT NULL
		) ON COMMIT DROP`

	// $1 year_month, $2 layer. Cursor over the existing seasonal rows of the
	// day's keys, fetched in almanacFoldFetchBatch batches so memory stays
	// bounded (a day is ~100k–125k keys). Run in the simple protocol (pgx
	// interpolates the parameters client-side): DECLARE/FETCH are utility
	// statements.
	almanacFoldDeclareSQL = `
		DECLARE almanac_fold_cur NO SCROLL CURSOR FOR
		SELECT c.grid4, c.band, c.region, c.counts
		FROM almanac_season_counts c
		JOIN almanac_fold_seg s ON s.grid4 = c.grid4 AND s.band = c.band AND s.region = c.region
		WHERE c.year_month = $1::integer AND c.layer = $2::text`
	almanacFoldFetchSQL = `FETCH 4096 FROM almanac_fold_cur`
	almanacFoldCloseSQL = `CLOSE almanac_fold_cur`

	// $1 year_month, $2 layer. The Go side already replaced the day's
	// segment (SET semantics, almanacSparseReplaceDay), so the upsert simply
	// stores the new encoding; re-folding a day is byte-identical and an
	// unchanged row is not rewritten.
	almanacFoldUpsertSQL = `
		INSERT INTO almanac_season_counts AS c (grid4, band, region, year_month, layer, counts)
		SELECT n.grid4, n.band, n.region, $1::integer, $2::text, n.counts
		FROM almanac_fold_new n
		ON CONFLICT (grid4, band, region, year_month, layer)
		DO UPDATE SET counts = EXCLUDED.counts
		WHERE c.counts IS DISTINCT FROM EXCLUDED.counts`

	// $1 year_month, $2 layer, $3 the day's mask bit. OR is idempotent.
	almanacFoldActivitySQL = `
		INSERT INTO almanac_area_activity AS a (grid4, band, year_month, layer, day_mask)
		SELECT DISTINCT s.grid4, s.band, $1::integer, $2::text, $3::integer
		FROM almanac_fold_seg s
		ON CONFLICT (grid4, band, year_month, layer)
		DO UPDATE SET day_mask = a.day_mask | EXCLUDED.day_mask`

	// $1 day_index, $2 layer, $3 slots, $4 totals. Seeds days with no flush
	// totals (pre-deploy backlog, raw-spot rebuilds) and raises the partial
	// deploy day to the daily table's sum; GREATEST keeps it idempotent and
	// never lowers a flush-maintained total.
	almanacFoldIngestSeedSQL = `
		INSERT INTO almanac_ingest_slots AS i (day_index, slot_of_day, layer, spot_total)
		SELECT $1::bigint, u.slot, $2::text, u.total
		FROM unnest($3::integer[], $4::bigint[]) AS u(slot, total)
		ON CONFLICT (day_index, slot_of_day, layer)
		DO UPDATE SET spot_total = GREATEST(i.spot_total, EXCLUDED.spot_total)`

	// Monotonic: the watermark never moves backwards.
	almanacSetWatermarkSQL = `
		INSERT INTO dx_meta (k, v) VALUES ($1, $2)
		ON CONFLICT (k) DO UPDATE SET v = GREATEST(dx_meta.v::bigint, EXCLUDED.v::bigint)::text`

	almanacRecordLostSQL = `
		INSERT INTO almanac_lost_days (day_index, reason, recorded_at)
		SELECT d, $2::text, $3::bigint FROM unnest($1::bigint[]) AS d
		ON CONFLICT (day_index) DO NOTHING`
)

// pgAlmanacFoldStore is the Postgres almanacFoldStore.
type pgAlmanacFoldStore struct {
	pool *pgxpool.Pool
}

func readAlmanacWatermark(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, lock bool) (int64, bool, error) {
	sql := `SELECT v FROM dx_meta WHERE k = $1`
	if lock {
		sql = almanacFoldLockWatermarkSQL
	}
	var v string
	err := q.QueryRow(ctx, sql, almanacFoldWatermarkKey).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	w, perr := strconv.ParseInt(v, 10, 64)
	if perr != nil {
		return 0, false, fmt.Errorf("almanac watermark %q: %w", v, perr)
	}
	return w, true, nil
}

func (p *pgAlmanacFoldStore) ensureWatermark(ctx context.Context, today int64) (int64, error) {
	if w, ok, err := readAlmanacWatermark(ctx, p.pool, false); err != nil || ok {
		return w, err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Serialize concurrent first-time initialisers (fold ticker vs prune gate).
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, almanacFoldWatermarkKey); err != nil {
		return 0, err
	}
	if w, ok, err := readAlmanacWatermark(ctx, tx, false); err != nil || ok {
		return w, err
	}
	var minDay *int64
	if err := tx.QueryRow(ctx, `SELECT min(day_index) FROM dx_region_baseline_daily`).Scan(&minDay); err != nil {
		return 0, err
	}
	var min int64
	if minDay != nil {
		min = *minDay
	}
	w, lost, hasLost := almanacInitialWatermark(min, minDay != nil, today)
	if hasLost {
		if _, err := tx.Exec(ctx, almanacRecordLostSQL, []int64{lost}, almanacLostReasonInitial, time.Now().Unix()); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(ctx, almanacSetWatermarkSQL, almanacFoldWatermarkKey, strconv.FormatInt(w, 10)); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return w, nil
}

// foldDay folds one final day in a single transaction and returns the
// watermark after it. If the watermark is already ≥ day (a forced prune or a
// previous fold got there first) nothing is written.
func (p *pgAlmanacFoldStore) foldDay(ctx context.Context, day int64) (int64, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	w, ok, err := readAlmanacWatermark(ctx, tx, true)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, errors.New("almanac watermark missing")
	}
	if w >= day {
		return w, nil
	}

	fold := newAlmanacDayFold(day)
	rows, err := tx.Query(ctx, almanacFoldStreamSQL, day)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var r almanacDailyRow
		if err := rows.Scan(&r.Grid4, &r.Band, &r.Region, &r.Slot, &r.Count); err != nil {
			rows.Close()
			return 0, err
		}
		fold.add(r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	if len(fold.Segments) > 0 {
		if err := foldSeasonRows(ctx, tx, fold); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, almanacFoldActivitySQL, fold.YearMonth, almanacSeasonLayerPSKR,
			int32(fold.dayMaskBit())); err != nil {
			return 0, err
		}
	}
	if slots, totals := fold.nonZeroSlotTotals(); len(slots) > 0 {
		if _, err := tx.Exec(ctx, almanacFoldIngestSeedSQL, day, almanacSeasonLayerPSKR, slots, totals); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(ctx, almanacSetWatermarkSQL, almanacFoldWatermarkKey, strconv.FormatInt(day, 10)); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return day, nil
}

// forceAdvance moves the watermark to newWatermark, recording every skipped
// day in almanac_lost_days, in one transaction holding the watermark row lock
// (so a concurrent fold either commits first or sees the new watermark).
// foldSeasonRows writes the day's segments into almanac_season_counts inside
// the fold transaction. The sparse encoding can't be patched in SQL, so:
//
//  1. COPY the day's keys into almanac_fold_seg;
//  2. stream the existing rows of those keys through a cursor in batches,
//     replace the day's 48-slot segment in Go (almanacSparseReplaceDay) and
//     COPY each re-encoded batch into almanac_fold_new;
//  3. COPY fresh encodings for the keys that had no row yet;
//  4. upsert almanac_fold_new with SET counts = EXCLUDED.counts.
//
// Merged keys are removed from fold.Segments as they are written, so the
// map shrinks while the cursor drains (activity is derived from
// almanac_fold_seg, the ingest seed from fold.SlotTotals).
func foldSeasonRows(ctx context.Context, tx pgx.Tx, fold *almanacDayFold) error {
	if _, err := tx.Exec(ctx, almanacFoldTempTableSQL); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, almanacFoldNewTableSQL); err != nil {
		return err
	}
	// Stream the keys straight from the map (no [][]any copy).
	next, stop := iter.Pull2(maps.All(fold.Segments))
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"almanac_fold_seg"},
		[]string{"grid4", "band", "region"}, pgx.CopyFromFunc(func() ([]any, error) {
			k, _, ok := next()
			if !ok {
				return nil, nil
			}
			return []any{k.Grid4, k.Band, k.Region}, nil
		}))
	stop()
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, almanacFoldDeclareSQL, pgx.QueryExecModeSimpleProtocol,
		int32(fold.YearMonth), almanacSeasonLayerPSKR); err != nil {
		return err
	}
	newCols := []string{"grid4", "band", "region", "counts"}
	batch := make([][]any, 0, almanacFoldFetchBatch)
	for {
		batch = batch[:0]
		rows, err := tx.Query(ctx, almanacFoldFetchSQL, pgx.QueryExecModeSimpleProtocol)
		if err != nil {
			return err
		}
		for rows.Next() {
			var (
				k   almanacSegKey
				old []byte
			)
			if err := rows.Scan(&k.Grid4, &k.Band, &k.Region, &old); err != nil {
				rows.Close()
				return err
			}
			seg, ok := fold.Segments[k]
			if !ok {
				continue // cannot happen: the cursor joins the day's keys
			}
			enc, derr := almanacSparseReplaceDay(old, fold.DayOfMonth, seg)
			if derr != nil {
				almanacSparseMalformed("almanac fold", k.Grid4, k.Band, k.Region, fold.YearMonth, derr)
			}
			delete(fold.Segments, k)
			batch = append(batch, []any{k.Grid4, k.Band, k.Region, enc})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"almanac_fold_new"}, newCols, pgx.CopyFromRows(batch)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, almanacFoldCloseSQL, pgx.QueryExecModeSimpleProtocol); err != nil {
		return err
	}

	// Keys without an existing row: fresh encodings, streamed.
	next, stop = iter.Pull2(maps.All(fold.Segments))
	_, err = tx.CopyFrom(ctx, pgx.Identifier{"almanac_fold_new"}, newCols,
		pgx.CopyFromFunc(func() ([]any, error) {
			k, seg, ok := next()
			if !ok {
				return nil, nil
			}
			enc, _ := almanacSparseReplaceDay(nil, fold.DayOfMonth, seg)
			return []any{k.Grid4, k.Band, k.Region, enc}, nil
		}))
	stop()
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, almanacFoldUpsertSQL, int32(fold.YearMonth), almanacSeasonLayerPSKR)
	return err
}

func (p *pgAlmanacFoldStore) forceAdvance(ctx context.Context, newWatermark int64, reason string) (int64, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	w, ok, err := readAlmanacWatermark(ctx, tx, true)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, errors.New("almanac watermark missing")
	}
	days := almanacLostDayRange(w, newWatermark)
	if len(days) == 0 {
		return 0, nil
	}
	if _, err := tx.Exec(ctx, almanacRecordLostSQL, days, reason, time.Now().Unix()); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, almanacSetWatermarkSQL, almanacFoldWatermarkKey, strconv.FormatInt(newWatermark, 10)); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int64(len(days)), nil
}

func (p *pgAlmanacFoldStore) lostDayCount(ctx context.Context) (int64, error) {
	var n int64
	err := p.pool.QueryRow(ctx, `SELECT count(*) FROM almanac_lost_days`).Scan(&n)
	return n, err
}
