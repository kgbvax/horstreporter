package main

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"horstreporter/internal/region"

	"github.com/jackc/pgx/v5"
)

// prop_baseline_backfill.go seeds prop_region_baseline_daily for the three
// non-WSPR sources (pskr, rbn, dxcluster) — and fills any missing WSPR days —
// from dx_raw_spots. The 11-region taxonomy lives in Go (region.FromLocator)
// and cannot be expressed in SQL, so this is a Go background job, not a
// one-shot SQL statement.
//
// Safety properties (prod dx_raw_spots grows ~1.9G/day):
//   - bounded to the last propBaselineBackfillDays (45) days — never full-table;
//   - per (source × day) chunks, pre-aggregated by Postgres on the
//     (source_type, spot_time) index — never raw-row streaming;
//   - 30s statement timeout per chunk + inter-chunk sleep;
//   - resumable via dx_meta cursor; INSERT ... ON CONFLICT DO NOTHING so a
//     restart is idempotent and the ensure-time WSPR seed always wins;
//   - aborts after 2 consecutive chunk failures, leaving the cursor for the
//     next restart.

const (
	propBaselineBackfillDays          = 45
	propBaselineMetaCursorKey         = "prop_baseline_backfill_cursor_day"
	propBaselineMetaDoneKey           = "prop_baseline_backfill_done_at"
	propBaselineChunkStatementTimeout = 30 * time.Second
	propBaselineChunkSleep            = 300 * time.Millisecond
	propBaselineMaxChunkFailures      = 2
)

// startPropBaselineBackfill spawns the backfill goroutine if Postgres is
// configured and the job isn't already done. Safe to call unconditionally.
func (s *dxPostgresStore) startPropBaselineBackfill() {
	if s == nil {
		return
	}
	go func() {
		ctx := context.Background()
		if err := s.runPropBaselineBackfill(ctx); err != nil {
			logInfo("prop baseline backfill stopped: %v", err)
		}
	}()
}

// runPropBaselineBackfill is the resumable main loop. It walks (source × day)
// pairs from the cursor day forward to today, aggregating each chunk and
// upserting before advancing the cursor.
func (s *dxPostgresStore) runPropBaselineBackfill(ctx context.Context) error {
	now := time.Now().Unix()
	today := utcDayIndex(now)

	// Already finished?
	var doneAt string
	err := s.pool.QueryRow(ctx, `SELECT v FROM dx_meta WHERE k = $1`, propBaselineMetaDoneKey).Scan(&doneAt)
	if err == nil {
		return nil
	}

	// Resume day: cursor+1, or the bounded start when no cursor exists.
	startDay := today - propBaselineBackfillDays + 1
	var cursorStr string
	err = s.pool.QueryRow(ctx, `SELECT v FROM dx_meta WHERE k = $1`, propBaselineMetaCursorKey).Scan(&cursorStr)
	if err == nil {
		if cursorDay, perr := strconv.ParseInt(cursorStr, 10, 64); perr == nil && cursorDay+1 > startDay {
			startDay = cursorDay + 1
		}
	}
	if startDay > today {
		return s.markPropBaselineBackfillDone(ctx)
	}

	sources := []propIntelSourceProfile{}
	for _, p := range propIntelSourceProfiles {
		sources = append(sources, p)
	}

	consecutiveFailures := 0
	for day := startDay; day <= today; day++ {
		dayStart := day * 86400
		dayEnd := dayStart + 86400
		for _, prof := range sources {
			if err := s.backfillPropBaselineChunk(ctx, prof, dayStart, dayEnd); err != nil {
				consecutiveFailures++
				logInfo("prop baseline backfill chunk failed (source=%s day=%d, failures=%d): %v",
					prof.PublicName, day, consecutiveFailures, err)
				if consecutiveFailures >= propBaselineMaxChunkFailures {
					return fmt.Errorf("aborting after %d consecutive chunk failures (cursor day retained)", consecutiveFailures)
				}
			} else {
				consecutiveFailures = 0
			}
			time.Sleep(propBaselineChunkSleep)
		}
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO dx_meta (k, v) VALUES ($1, $2)
			ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v
		`, propBaselineMetaCursorKey, strconv.FormatInt(day, 10)); err != nil {
			return fmt.Errorf("cursor update: %w", err)
		}
		logDebug("prop baseline backfill completed day %d", day)
	}
	return s.markPropBaselineBackfillDone(ctx)
}

func (s *dxPostgresStore) markPropBaselineBackfillDone(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO dx_meta (k, v) VALUES ($1, $2)
		ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v
	`, propBaselineMetaDoneKey, strconv.FormatInt(time.Now().Unix(), 10))
	if err == nil {
		logInfo("prop baseline backfill finished")
	}
	return err
}

// backfillPropBaselineChunk aggregates one (source × UTC day) window of
// dx_raw_spots into (band, slot, region) counts and merges them into
// prop_region_baseline_daily. Postgres pre-aggregates to grid4-level rows;
// Go maps grid4 → region.
//
// dx_raw_spots locator columns map verbatim from MQTTMessage fields:
// sender_locator = SL, receiver_locator = RL. The receiver/reporter end per
// the profile (ReceiverSide) selects which column keys the region; rows with
// a blank/invalid receiver locator fall back to the other end in a second
// pass (same rule as propBaselineEngine.Observe).
func (s *dxPostgresStore) backfillPropBaselineChunk(ctx context.Context, prof propIntelSourceProfile, dayStart, dayEnd int64) error {
	recvCol, otherCol := "sender_locator", "receiver_locator"
	if prof.ReceiverSide == "rc" {
		recvCol, otherCol = "receiver_locator", "sender_locator"
	}

	acc := make(map[propBaselineKey]int64)
	dayIndex := dayStart / 86400

	// Pass 1 aggregates rows with a usable receiver-side locator; pass 2
	// covers only the rows whose receiver side was blank, keyed by the other
	// end (the Observe fallback rule).
	pass1 := fmt.Sprintf(`
		SELECT band,
		       (mod(spot_time, 86400) / 1800)::int AS slot,
		       left(upper(%[1]s), 4) AS loc4,
		       count(*)::bigint AS c
		FROM dx_raw_spots
		WHERE source_type = $1
		  AND spot_time >= $2 AND spot_time < $3
		  AND length(%[1]s) >= 4
		GROUP BY 1, 2, 3
	`, recvCol)
	pass2 := fmt.Sprintf(`
		SELECT band,
		       (mod(spot_time, 86400) / 1800)::int AS slot,
		       left(upper(%[1]s), 4) AS loc4,
		       count(*)::bigint AS c
		FROM dx_raw_spots
		WHERE source_type = $1
		  AND spot_time >= $2 AND spot_time < $3
		  AND length(%[2]s) < 4
		  AND length(%[1]s) >= 4
		GROUP BY 1, 2, 3
	`, otherCol, recvCol)

	collect := func(query string) error {
		qctx, cancel := context.WithTimeout(ctx, propBaselineChunkStatementTimeout)
		defer cancel()
		rows, err := s.pool.Query(qctx, query, prof.InternalTag, dayStart, dayEnd)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var band, loc4 string
			var slot int
			var c int64
			if err := rows.Scan(&band, &slot, &loc4, &c); err != nil {
				return err
			}
			reg := region.FromLocator(loc4)
			if reg == region.Unknown {
				continue
			}
			nb := normalizeBand(band)
			if nb == "" {
				continue
			}
			acc[propBaselineKey{Band: nb, Source: prof.PublicName, SlotOfDay: slot, Region: string(reg), DayIndex: dayIndex}] += c
		}
		return rows.Err()
	}

	if err := collect(pass1); err != nil {
		return err
	}
	if err := collect(pass2); err != nil {
		return err
	}

	if len(acc) == 0 {
		return nil
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, fmt.Sprintf(`SET LOCAL statement_timeout = %d`, propBaselineChunkStatementTimeout.Milliseconds())); err != nil {
		return err
	}
	batch := &pgx.Batch{}
	queued := 0
	for k, v := range acc {
		// DO NOTHING: the ensure-time WSPR seed and any already-backfilled
		// rows always win — a restarted backfill is idempotent.
		batch.Queue(`
			INSERT INTO prop_region_baseline_daily (band, source, slot_of_day, region, day_index, spot_count)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT DO NOTHING
		`, k.Band, k.Source, k.SlotOfDay, k.Region, k.DayIndex, v)
		queued++
	}
	br := tx.SendBatch(ctx, batch)
	for i := 0; i < queued; i++ {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return err
		}
	}
	if err := br.Close(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
