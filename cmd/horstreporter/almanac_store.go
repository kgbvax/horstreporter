package main

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// almanac_store.go is the thin SQL shim behind the 30-day Almanac (plan U2,
// KTD2): one read-only REPEATABLE READ transaction reads the fold watermark
// W, the seasonal record for days ≤ W, the daily-table tail for days > W,
// the ingest-slot totals and the lost days — PSKR/cluster layer only (R6).
// All logic (the ≤ W / > W split included) lives in almanacAccum
// (almanac.go); the in-memory model is fakeAggStore in almanac_test.go.
//
// Aggregation choice (KTD2 allows it): the sparse seasonal rows
// (almanac_sparse.go: version byte + uvarint gap/count pairs of the non-zero
// cells of the 31×48 month grid) are fetched and decoded in Go rather than
// in SQL, which has no cheap way to walk the varint stream. The fetch is at
// most 25 grids × 10 bands × 11 regions × 3 months ≈ 8k rows (≈2 B per
// non-zero cell), streamed row by row into fixed-size accumulators via
// RawValues (no per-row copy); decoding visits only non-zero cells.
// The daily-table tail is still aggregated in SQL (summed per ring level).
// Heap per request stays ≈3 MB at r=2 (TestAlmanacHeapUnder20MB).
//
// All day bounds are int64 parameters computed in Go (no extract()); array
// parameters carry explicit casts. The exact statements are also recorded,
// with their EXPLAIN commands, in docs/runbooks/almanac-fold-rollout.md.

// almanacReadTx is one consistent read of the Almanac inputs. Callbacks run
// synchronously per row; byte slices are only valid during the call. Season
// counts are passed through in their stored sparse encoding.
type almanacReadTx interface {
	watermark(ctx context.Context) (int64, bool, error)
	// snrSince is the SNR collection start (KTD13); ok false = not
	// collected (columns missing).
	snrSince(ctx context.Context) (int64, bool, error)
	seasonCounts(ctx context.Context, grids, bands []string, months []int, layer string,
		fn func(grid, band, region string, ym int, counts []byte)) error
	// tailCounts: with tier ≥ 0 the rows also carry the SNR-carrying spots
	// and the spots with SNR ≥ almanacSNRTierFloors[tier] (0 otherwise).
	tailCounts(ctx context.Context, grids []string, rings []int32, bands []string, afterDay, fromDay, toDay int64, tier int,
		fn func(ring int, band, region string, day int64, slot int, count, snr, ge int64)) error
	ingestSlots(ctx context.Context, layer string, fromDay, toDay int64, fn func(day int64, slot int, total int64)) error
	lostDays(ctx context.Context, fromDay, toDay int64, fn func(day int64)) error
}

// almanacReadStore opens the read transaction.
type almanacReadStore interface {
	withReadTx(ctx context.Context, fn func(almanacReadTx) error) error
}

// readAlmanacAccum runs the reads for centre over win in one transaction.
// todayOnly reads just the tail of yesterday and today (the 120 s today
// overlay refresh, KTD11); the typical read covers the whole window. tier is
// the SNR floor index (-1 = any SNR, KTD13).
func readAlmanacAccum(ctx context.Context, st almanacReadStore, centre string, win almanacWindow, todayOnly bool, tier int) (*almanacAccum, error) {
	acc := newAlmanacAccum(centre, win, tier)
	err := st.withReadTx(ctx, func(tx almanacReadTx) error {
		w, ok, err := tx.watermark(ctx)
		if err != nil {
			return err
		}
		acc.setWatermark(w, ok)
		since, sok, err := tx.snrSince(ctx)
		if err != nil {
			return err
		}
		acc.setSNRSince(since, sok)
		bands := almanacInScopeBands
		if todayOnly {
			return tx.tailCounts(ctx, acc.squares, acc.rings, bands, acc.tailAfter(), win.Today-1, win.Today, acc.tier, acc.addTailRow)
		}
		if months := acc.seasonMonths(); len(months) > 0 {
			if err := tx.seasonCounts(ctx, acc.squares, bands, months, almanacSeasonLayerPSKR, acc.addSeasonRow); err != nil {
				return err
			}
		}
		if err := tx.tailCounts(ctx, acc.squares, acc.rings, bands, acc.tailAfter(), win.Start, win.Today, acc.tier, acc.addTailRow); err != nil {
			return err
		}
		if err := tx.ingestSlots(ctx, almanacSeasonLayerPSKR, win.Start, win.Today, acc.addIngest); err != nil {
			return err
		}
		return tx.lostDays(ctx, win.Start, win.End, acc.addLost)
	})
	if err != nil {
		return nil, err
	}
	return acc, nil
}

// ---------------------------------------------------------------------------
// Postgres
// ---------------------------------------------------------------------------

const (
	// $1 grids, $2 bands, $3 year_months, $4 layer. PK-prefix lookups.
	almanacSeasonCountsSQL = `
		SELECT grid4, band, region, year_month, counts
		FROM almanac_season_counts
		WHERE grid4 = ANY($1::text[])
		  AND band = ANY($2::text[])
		  AND year_month = ANY($3::int[])
		  AND layer = $4`

	// $1 grids, $2 ring levels (parallel to $1), $3 bands, $4 watermark
	// (exclusive), $5/$6 day bounds. Summed across the ring per level, so
	// the result is bounded by levels × bands × regions × days × slots.
	// The SNR sums are 0 here; almanacTailCountsSQLFor substitutes the
	// counter columns for a tier (KTD13).
	almanacTailCountsSQL = `
		SELECT r.ring, d.band, d.region, d.day_index, d.slot_of_day, SUM(d.spot_count)::bigint,
			0::bigint, 0::bigint
		FROM dx_region_baseline_daily d
		JOIN unnest($1::text[], $2::int[]) AS r(grid4, ring) ON d.target_grid4 = r.grid4
		WHERE d.band = ANY($3::text[])
		  AND d.day_index > $4
		  AND d.day_index BETWEEN $5 AND $6
		GROUP BY r.ring, d.band, d.region, d.day_index, d.slot_of_day`

	almanacIngestSlotsReadSQL = `
		SELECT day_index, slot_of_day, spot_total
		FROM almanac_ingest_slots
		WHERE layer = $1 AND day_index BETWEEN $2 AND $3`

	almanacLostDaysReadSQL = `
		SELECT day_index FROM almanac_lost_days WHERE day_index BETWEEN $1 AND $2`
)

// almanacTailCountsSQLFor is almanacTailCountsSQL summing snr_spots and the
// tier's snr_ge_* column (a fixed whitelist, never user text) when tier ≥ 0
// and the SNR columns exist; otherwise the SNR sums stay 0.
func almanacTailCountsSQLFor(tier int, snrColumns bool) string {
	if tier < 0 || tier >= almanacSNRTiers || !snrColumns {
		return almanacTailCountsSQL
	}
	return strings.Replace(almanacTailCountsSQL, "0::bigint, 0::bigint",
		"SUM(d.snr_spots)::bigint, SUM(d."+regionSNRColumnNames[1+tier]+")::bigint", 1)
}

// pgAlmanacReadStore is the Postgres almanacReadStore. snrColumns reports
// whether the KTD13 SNR counter columns exist (nil = no).
type pgAlmanacReadStore struct {
	pool       *pgxpool.Pool
	snrColumns func() bool
}

func (p *pgAlmanacReadStore) hasSNRColumns() bool {
	return p.snrColumns != nil && p.snrColumns()
}

func (p *pgAlmanacReadStore) withReadTx(ctx context.Context, fn func(almanacReadTx) error) error {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(&pgAlmanacReadTx{tx: tx, snrColumns: p.hasSNRColumns()}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type pgAlmanacReadTx struct {
	tx         pgx.Tx
	snrColumns bool
}

func (t *pgAlmanacReadTx) watermark(ctx context.Context) (int64, bool, error) {
	return readAlmanacWatermark(ctx, t.tx, false)
}

// snrSince reads the SNR collection start; without the SNR columns the SNR
// data counts as never collected.
func (t *pgAlmanacReadTx) snrSince(ctx context.Context) (int64, bool, error) {
	if !t.snrColumns {
		return 0, false, nil
	}
	return readAlmanacSNRSince(ctx, t.tx)
}

func (t *pgAlmanacReadTx) seasonCounts(ctx context.Context, grids, bands []string, months []int, layer string,
	fn func(grid, band, region string, ym int, counts []byte)) error {
	ms := make([]int32, len(months))
	for i, m := range months {
		ms[i] = int32(m)
	}
	rows, err := t.tx.Query(ctx, almanacSeasonCountsSQL, grids, bands, ms, layer)
	if err != nil {
		return err
	}
	defer rows.Close()
	var (
		grid, band, reg string
		ym              int32
		buf             []byte
	)
	for rows.Next() {
		// Binary bytea arrives as raw bytes: read it in place (no per-row
		// copy). Text format (unexpected) falls back to a decoded scan.
		if rows.FieldDescriptions()[4].Format == pgx.BinaryFormatCode {
			if err := rows.Scan(&grid, &band, &reg, &ym, nil); err != nil {
				return err
			}
			fn(grid, band, reg, int(ym), rows.RawValues()[4])
			continue
		}
		if err := rows.Scan(&grid, &band, &reg, &ym, &buf); err != nil {
			return err
		}
		fn(grid, band, reg, int(ym), buf)
	}
	return rows.Err()
}

func (t *pgAlmanacReadTx) tailCounts(ctx context.Context, grids []string, rings []int32, bands []string, afterDay, fromDay, toDay int64, tier int,
	fn func(ring int, band, region string, day int64, slot int, count, snr, ge int64)) error {
	rows, err := t.tx.Query(ctx, almanacTailCountsSQLFor(tier, t.snrColumns), grids, rings, bands, afterDay, fromDay, toDay)
	if err != nil {
		return err
	}
	defer rows.Close()
	var (
		ring, slot       int32
		band, reg        string
		day, count, n, g int64
	)
	for rows.Next() {
		if err := rows.Scan(&ring, &band, &reg, &day, &slot, &count, &n, &g); err != nil {
			return err
		}
		fn(int(ring), band, reg, day, int(slot), count, n, g)
	}
	return rows.Err()
}

func (t *pgAlmanacReadTx) ingestSlots(ctx context.Context, layer string, fromDay, toDay int64, fn func(day int64, slot int, total int64)) error {
	rows, err := t.tx.Query(ctx, almanacIngestSlotsReadSQL, layer, fromDay, toDay)
	if err != nil {
		return err
	}
	defer rows.Close()
	var (
		day, total int64
		slot       int32
	)
	for rows.Next() {
		if err := rows.Scan(&day, &slot, &total); err != nil {
			return err
		}
		fn(day, int(slot), total)
	}
	return rows.Err()
}

func (t *pgAlmanacReadTx) lostDays(ctx context.Context, fromDay, toDay int64, fn func(day int64)) error {
	rows, err := t.tx.Query(ctx, almanacLostDaysReadSQL, fromDay, toDay)
	if err != nil {
		return err
	}
	defer rows.Close()
	var day int64
	for rows.Next() {
		if err := rows.Scan(&day); err != nil {
			return err
		}
		fn(day)
	}
	return rows.Err()
}
