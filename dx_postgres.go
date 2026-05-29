package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const defaultDxPostgresDSN = "postgres://dxuser@localhost:5432/dxdata?sslmode=disable"

type dxPostgresStore struct {
	pool *pgxpool.Pool
}

type baselinePair struct {
	DistanceTier int
	SnrTier      int
	Count        int64
}

type dxPulseRegionBaselineDailyKey struct {
	TargetGrid4 string
	Band        string
	SlotOfDay   int
	Region      string
	DayIndex    int64
}

func newDxPostgresStore(ctx context.Context, dsn string) (*dxPostgresStore, error) {
	if strings.TrimSpace(dsn) == "" {
		dsn = defaultDxPostgresDSN
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	st := &dxPostgresStore{pool: pool}
	if err := st.initSchema(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return st, nil
}

func (s *dxPostgresStore) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

func (s *dxPostgresStore) initSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE EXTENSION IF NOT EXISTS postgis;`,
		`CREATE TABLE IF NOT EXISTS dx_baseline_global (
			band TEXT NOT NULL,
			slot_of_day INTEGER NOT NULL,
			source4 TEXT NOT NULL,
			distance_tier INTEGER NOT NULL,
			snr_tier INTEGER NOT NULL,
			count BIGINT NOT NULL,
			sum_distance DOUBLE PRECISION NOT NULL,
			sum_snr DOUBLE PRECISION NOT NULL,
			PRIMARY KEY (band, slot_of_day, source4, distance_tier, snr_tier)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_dx_baseline_global_band_slot ON dx_baseline_global (band, slot_of_day);`,
		`CREATE TABLE IF NOT EXISTS dx_baseline_target (
			target_token TEXT NOT NULL,
			band TEXT NOT NULL,
			slot_of_day INTEGER NOT NULL,
			source4 TEXT NOT NULL,
			distance_tier INTEGER NOT NULL,
			snr_tier INTEGER NOT NULL,
			count BIGINT NOT NULL,
			sum_distance DOUBLE PRECISION NOT NULL,
			sum_snr DOUBLE PRECISION NOT NULL,
			PRIMARY KEY (target_token, band, slot_of_day, source4, distance_tier, snr_tier)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_dx_baseline_target_token_band_slot ON dx_baseline_target (target_token, band, slot_of_day);`,
		`CREATE TABLE IF NOT EXISTS dx_raw_spots (
			id BIGSERIAL PRIMARY KEY,
			spot_time BIGINT NOT NULL,
			band TEXT NOT NULL,
			sender_callsign TEXT NOT NULL,
			receiver_callsign TEXT NOT NULL,
			sender_locator TEXT NOT NULL,
			receiver_locator TEXT NOT NULL,
			mode TEXT NOT NULL,
			signal_report_db INTEGER NOT NULL,
			source_grid4 TEXT NOT NULL,
			spot_geom geometry(Point, 4326)
		);`,
		`DO $$
		BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'dx_raw_spots' AND column_name = 't'
			) THEN
				ALTER TABLE dx_raw_spots RENAME COLUMN t TO spot_time;
			END IF;
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'dx_raw_spots' AND column_name = 'b'
			) THEN
				ALTER TABLE dx_raw_spots RENAME COLUMN b TO band;
			END IF;
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'dx_raw_spots' AND column_name = 'sc'
			) THEN
				ALTER TABLE dx_raw_spots RENAME COLUMN sc TO sender_callsign;
			END IF;
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'dx_raw_spots' AND column_name = 'rc'
			) THEN
				ALTER TABLE dx_raw_spots RENAME COLUMN rc TO receiver_callsign;
			END IF;
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'dx_raw_spots' AND column_name = 'sl'
			) THEN
				ALTER TABLE dx_raw_spots RENAME COLUMN sl TO sender_locator;
			END IF;
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'dx_raw_spots' AND column_name = 'rl'
			) THEN
				ALTER TABLE dx_raw_spots RENAME COLUMN rl TO receiver_locator;
			END IF;
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'dx_raw_spots' AND column_name = 'md'
			) THEN
				ALTER TABLE dx_raw_spots RENAME COLUMN md TO mode;
			END IF;
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'dx_raw_spots' AND column_name = 'rp'
			) THEN
				ALTER TABLE dx_raw_spots RENAME COLUMN rp TO signal_report_db;
			END IF;
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'dx_raw_spots' AND column_name = 'source4'
			) THEN
				ALTER TABLE dx_raw_spots RENAME COLUMN source4 TO source_grid4;
			END IF;
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'dx_raw_spots' AND column_name = 'geom'
			) THEN
				ALTER TABLE dx_raw_spots RENAME COLUMN geom TO spot_geom;
			END IF;
		END
		$$;`,
		`CREATE INDEX IF NOT EXISTS idx_dx_raw_spots_spot_time ON dx_raw_spots (spot_time);`,
		`CREATE INDEX IF NOT EXISTS idx_dx_raw_spots_band_spot_time ON dx_raw_spots (band, spot_time);`,
		`CREATE INDEX IF NOT EXISTS idx_dx_raw_spots_spot_geom ON dx_raw_spots USING GIST (spot_geom);`,
		`CREATE TABLE IF NOT EXISTS dx_region_baseline_daily (
			target_grid4 TEXT NOT NULL,
			band TEXT NOT NULL,
			slot_of_day INTEGER NOT NULL,
			region TEXT NOT NULL,
			day_index BIGINT NOT NULL,
			spot_count BIGINT NOT NULL,
			PRIMARY KEY (target_grid4, band, slot_of_day, region, day_index)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_dx_region_baseline_lookup ON dx_region_baseline_daily (target_grid4, band, slot_of_day, region, day_index);`,
		`CREATE TABLE IF NOT EXISTS dx_meta (
			k TEXT PRIMARY KEY,
			v TEXT NOT NULL
		);`,
	}
	for _, q := range stmts {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func (s *dxPostgresStore) isEmpty(ctx context.Context) (bool, error) {
	var total int64
	err := s.pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM dx_baseline_global) +
			(SELECT COUNT(*) FROM dx_baseline_target) +
			(SELECT COUNT(*) FROM dx_raw_spots)
	`).Scan(&total)
	if err != nil {
		return false, err
	}
	return total == 0, nil
}

func (s *dxPostgresStore) migrateFromJSONIfNeeded(ctx context.Context, jsonPath string) error {
	empty, err := s.isEmpty(ctx)
	if err != nil {
		return err
	}
	if !empty {
		return nil
	}
	jsonPath = strings.TrimSpace(jsonPath)
	if jsonPath == "" {
		return nil
	}
	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var snap baselineSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, b := range snap.Buckets {
		if b == nil {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO dx_baseline_global (band, slot_of_day, source4, distance_tier, snr_tier, count, sum_distance, sum_snr)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (band, slot_of_day, source4, distance_tier, snr_tier)
			DO UPDATE SET
				count = dx_baseline_global.count + EXCLUDED.count,
				sum_distance = dx_baseline_global.sum_distance + EXCLUDED.sum_distance,
				sum_snr = dx_baseline_global.sum_snr + EXCLUDED.sum_snr
		`, normalizeBand(b.Band), b.SlotOfDay, normalizeSource4(b.Source4), b.DistanceTier, b.SnrTier, b.Count, b.SumDistance, b.SumSNR); err != nil {
			return err
		}
	}
	for k, b := range snap.TargetBuckets {
		if b == nil {
			continue
		}
		target := k
		if i := strings.IndexByte(k, '|'); i > 0 {
			target = k[:i]
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO dx_baseline_target (target_token, band, slot_of_day, source4, distance_tier, snr_tier, count, sum_distance, sum_snr)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			ON CONFLICT (target_token, band, slot_of_day, source4, distance_tier, snr_tier)
			DO UPDATE SET
				count = dx_baseline_target.count + EXCLUDED.count,
				sum_distance = dx_baseline_target.sum_distance + EXCLUDED.sum_distance,
				sum_snr = dx_baseline_target.sum_snr + EXCLUDED.sum_snr
		`, normalizeTargetToken(target), normalizeBand(b.Band), b.SlotOfDay, normalizeSource4(b.Source4), b.DistanceTier, b.SnrTier, b.Count, b.SumDistance, b.SumSNR); err != nil {
			return err
		}
	}
	for _, ev := range snap.Events {
		s4 := normalizeSource4(ev.SL)
		lat, lon := locatorToLatLng(s4)
		_, err := tx.Exec(ctx, `
			INSERT INTO dx_raw_spots (
				spot_time, band, sender_callsign, receiver_callsign,
				sender_locator, receiver_locator, mode, signal_report_db,
				source_grid4, spot_geom
			)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,
				CASE WHEN $10::float8 = 0 AND $11::float8 = 0 THEN NULL
				ELSE ST_SetSRID(ST_MakePoint($11, $10), 4326) END)
		`, ev.T, normalizeBand(ev.B), strings.ToUpper(ev.SC), strings.ToUpper(ev.RC), strings.ToUpper(ev.SL), strings.ToUpper(ev.RL), "", ev.RP, s4, lat, lon)
		if err != nil {
			return err
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO dx_meta (k,v) VALUES ('json_migrated_at',$1)
		ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v
	`, fmt.Sprintf("%d", time.Now().Unix())); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}

func (s *dxPostgresStore) ensureDxPulseRegionBaseline(ctx context.Context) error {
	var baselineRows int64
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM dx_region_baseline_daily`).Scan(&baselineRows); err != nil {
		return err
	}
	if baselineRows > 0 {
		return nil
	}

	var rawRows int64
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM dx_raw_spots`).Scan(&rawRows); err != nil {
		return err
	}
	if rawRows == 0 {
		return nil
	}

	logInfo("DXPulse region baseline backfill starting from %d raw spots", rawRows)

	rows, err := s.pool.Query(ctx, `
		SELECT spot_time, band, sender_locator, receiver_locator
		FROM dx_raw_spots
		ORDER BY spot_time ASC
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	agg := make(map[dxPulseRegionBaselineDailyKey]int64, 8192)
	flush := func() error {
		if len(agg) == 0 {
			return nil
		}
		batch := &pgx.Batch{}
		keys := make([]dxPulseRegionBaselineDailyKey, 0, len(agg))
		for k, v := range agg {
			keys = append(keys, k)
			batch.Queue(`
				INSERT INTO dx_region_baseline_daily (target_grid4, band, slot_of_day, region, day_index, spot_count)
				VALUES ($1,$2,$3,$4,$5,$6)
				ON CONFLICT (target_grid4, band, slot_of_day, region, day_index)
				DO UPDATE SET spot_count = dx_region_baseline_daily.spot_count + EXCLUDED.spot_count
			`, k.TargetGrid4, k.Band, k.SlotOfDay, k.Region, k.DayIndex, v)
		}
		br := s.pool.SendBatch(ctx, batch)
		defer func() { _ = br.Close() }()
		for range keys {
			if _, err := br.Exec(); err != nil {
				return err
			}
		}
		clear(agg)
		return nil
	}

	processed := int64(0)
	for rows.Next() {
		var ts int64
		var band string
		var senderLoc string
		var receiverLoc string
		if err := rows.Scan(&ts, &band, &senderLoc, &receiverLoc); err != nil {
			return err
		}
		for _, key := range dxPulseRegionBaselineKeysForSpot(ts, band, senderLoc, receiverLoc) {
			agg[key]++
		}
		processed++
		if len(agg) >= 5000 {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}

	if _, err := s.pool.Exec(ctx, `
		INSERT INTO dx_meta (k,v) VALUES ('dxpulse_region_baseline_built_at',$1)
		ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v
	`, fmt.Sprintf("%d", time.Now().Unix())); err != nil {
		return err
	}

	logInfo("DXPulse region baseline backfill finished (%d raw spots processed)", processed)
	return nil
}

func dxPulseRegionBaselineKeysForSpot(ts int64, band string, senderLoc string, receiverLoc string) []dxPulseRegionBaselineDailyKey {
	band = normalizeBand(band)
	if band == "" {
		return nil
	}
	sender4 := normalizeSource4(senderLoc)
	receiver4 := normalizeSource4(receiverLoc)
	if sender4 == unknownSource4 && receiver4 == unknownSource4 {
		return nil
	}
	senderRegion := string(dxPulseRegionForLocator(senderLoc))
	receiverRegion := string(dxPulseRegionForLocator(receiverLoc))
	slot := utcSlotOfDay(ts)
	dayIndex := utcDayIndex(ts)

	keys := make([]dxPulseRegionBaselineDailyKey, 0, 2)
	seen := make(map[dxPulseRegionBaselineDailyKey]struct{}, 2)
	appendKey := func(target4 string, region string) {
		if target4 == unknownSource4 || region == "" || region == string(dxPulseRegionUnknown) {
			return
		}
		key := dxPulseRegionBaselineDailyKey{
			TargetGrid4: target4,
			Band:        band,
			SlotOfDay:   slot,
			Region:      region,
			DayIndex:    dayIndex,
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}

	appendKey(sender4, receiverRegion)
	appendKey(receiver4, senderRegion)
	return keys
}

func (s *dxPostgresStore) dxPulseBaselineForTargets(targets []string, lookbackDays int, windowMinutes int, now int64) (map[string]*dxPulseBaselineAccumulator, bool, error) {
	if len(targets) == 0 {
		return map[string]*dxPulseBaselineAccumulator{}, false, nil
	}
	if lookbackDays <= 0 {
		lookbackDays = dxPulseDefaultBaselineLookbackDays
	}
	if windowMinutes <= 0 {
		windowMinutes = dxPulseDefaultWindowMinutes
	}
	if now <= 0 {
		now = time.Now().Unix()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var baselineRows int64
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM dx_region_baseline_daily`).Scan(&baselineRows); err != nil {
		return nil, false, err
	}
	if baselineRows == 0 {
		return map[string]*dxPulseBaselineAccumulator{}, false, nil
	}

	dayEnd := utcDayIndex(now - dxPulseBaselineExclusionSeconds)
	dayStart := utcDayIndex(now - int64(lookbackDays*24*60*60))
	if dayEnd < dayStart {
		return map[string]*dxPulseBaselineAccumulator{}, true, nil
	}
	slots := dxPulseWindowSlots(now, windowMinutes)
	rows, err := s.pool.Query(ctx, `
		SELECT band, region, SUM(spot_count)::bigint, COUNT(DISTINCT day_index)::bigint
		FROM dx_region_baseline_daily
		WHERE target_grid4 = ANY($1)
		  AND day_index BETWEEN $2 AND $3
		  AND slot_of_day = ANY($4)
		GROUP BY band, region
	`, targets, dayStart, dayEnd, slots)
	if err != nil {
		return nil, true, err
	}
	defer rows.Close()

	out := make(map[string]*dxPulseBaselineAccumulator)
	for rows.Next() {
		var band string
		var region string
		var totalCount int64
		var activeDays int64
		if err := rows.Scan(&band, &region, &totalCount, &activeDays); err != nil {
			return nil, true, err
		}
		out[dxPulseCellKey(band, region)] = &dxPulseBaselineAccumulator{
			totalCount:     int(totalCount),
			activeDayCount: int(activeDays),
		}
	}
	return out, true, rows.Err()
}

func (s *dxPostgresStore) observe(m MQTTMessage, band string, slotOfDay int, source4 string, distTier, snrTier int, distanceKm float64, targetTokens [4]string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := s.pool.Exec(ctx, `
		INSERT INTO dx_baseline_global (band, slot_of_day, source4, distance_tier, snr_tier, count, sum_distance, sum_snr)
		VALUES ($1,$2,$3,$4,$5,1,$6,$7)
		ON CONFLICT (band, slot_of_day, source4, distance_tier, snr_tier)
		DO UPDATE SET
			count = dx_baseline_global.count + 1,
			sum_distance = dx_baseline_global.sum_distance + EXCLUDED.sum_distance,
			sum_snr = dx_baseline_global.sum_snr + EXCLUDED.sum_snr
	`, band, slotOfDay, source4, distTier, snrTier, distanceKm, float64(m.RP))
	if err != nil {
		return err
	}

	for i := range targetTokens {
		t := normalizeTargetTokenUpper(targetTokens[i])
		if t == "" {
			continue
		}
		duplicate := false
		for j := 0; j < i; j++ {
			if normalizeTargetTokenUpper(targetTokens[j]) == t {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO dx_baseline_target (target_token, band, slot_of_day, source4, distance_tier, snr_tier, count, sum_distance, sum_snr)
			VALUES ($1,$2,$3,$4,$5,$6,1,$7,$8)
			ON CONFLICT (target_token, band, slot_of_day, source4, distance_tier, snr_tier)
			DO UPDATE SET
				count = dx_baseline_target.count + 1,
				sum_distance = dx_baseline_target.sum_distance + EXCLUDED.sum_distance,
				sum_snr = dx_baseline_target.sum_snr + EXCLUDED.sum_snr
		`, t, band, slotOfDay, source4, distTier, snrTier, distanceKm, float64(m.RP)); err != nil {
			return err
		}
	}

	lat, lon := locatorToLatLng(source4)
	_, err = s.pool.Exec(ctx, `
		INSERT INTO dx_raw_spots (
			spot_time, band, sender_callsign, receiver_callsign,
			sender_locator, receiver_locator, mode, signal_report_db,
			source_grid4, spot_geom
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,
			CASE WHEN $10::float8 = 0 AND $11::float8 = 0 THEN NULL
			ELSE ST_SetSRID(ST_MakePoint($11, $10), 4326) END)
	`, m.T, band, m.SC, m.RC, m.SL, m.RL, m.MD, m.RP, source4, lat, lon)
	if err != nil {
		return err
	}

	for _, key := range dxPulseRegionBaselineKeysForSpot(m.T, band, m.SL, m.RL) {
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO dx_region_baseline_daily (target_grid4, band, slot_of_day, region, day_index, spot_count)
			VALUES ($1,$2,$3,$4,$5,1)
			ON CONFLICT (target_grid4, band, slot_of_day, region, day_index)
			DO UPDATE SET spot_count = dx_region_baseline_daily.spot_count + 1
		`, key.TargetGrid4, key.Band, key.SlotOfDay, key.Region, key.DayIndex); err != nil {
			return err
		}
	}
	return nil
}

func (s *dxPostgresStore) bandPairs(ctx context.Context, table string, targets []string, band string, slot int) ([]baselinePair, error) {
	q := ""
	args := []any{band, slot}
	if table == "dx_baseline_target" {
		q = `
			SELECT distance_tier, snr_tier, SUM(count)::bigint
			FROM dx_baseline_target
			WHERE band = $1 AND slot_of_day = $2 AND target_token = ANY($3)
			GROUP BY distance_tier, snr_tier
		`
		args = append(args, targets)
	} else {
		q = `
			SELECT distance_tier, snr_tier, SUM(count)::bigint
			FROM dx_baseline_global
			WHERE band = $1 AND slot_of_day = $2
			GROUP BY distance_tier, snr_tier
		`
	}
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]baselinePair, 0, 24)
	for rows.Next() {
		var p baselinePair
		if err := rows.Scan(&p.DistanceTier, &p.SnrTier, &p.Count); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *dxPostgresStore) baselineActivityForBand(targets []string, band string, slot int) (float64, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	targetPairs, err := s.bandPairs(ctx, "dx_baseline_target", targets, band, slot)
	if err != nil {
		return 0, false, err
	}
	if len(targetPairs) > 0 {
		total := 0.0
		for _, p := range targetPairs {
			total += float64(p.Count)
		}
		return (total / float64(len(targetPairs))) / 60.0, true, nil
	}
	globalPairs, err := s.bandPairs(ctx, "dx_baseline_global", nil, band, slot)
	if err != nil {
		return 0, false, err
	}
	if len(globalPairs) == 0 {
		return 0, false, nil
	}
	total := 0.0
	for _, p := range globalPairs {
		total += float64(p.Count)
	}
	return (total / float64(len(globalPairs))) / 60.0, false, nil
}

func (s *dxPostgresStore) baselineSupportForBand(targets []string, band string, slot int) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	targetPairs, err := s.bandPairs(ctx, "dx_baseline_target", targets, band, slot)
	if err != nil {
		return 0, err
	}
	var support int64
	for _, p := range targetPairs {
		support += p.Count
	}
	if support > 0 {
		return support, nil
	}
	globalPairs, err := s.bandPairs(ctx, "dx_baseline_global", nil, band, slot)
	if err != nil {
		return 0, err
	}
	for _, p := range globalPairs {
		support += p.Count
	}
	return support, nil
}

func (s *dxPostgresStore) baselineQuantilesForBand(targets []string, band string, slot int) (float64, float64, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pairs, err := s.bandPairs(ctx, "dx_baseline_target", targets, band, slot)
	if err != nil {
		return 0, 0, false, err
	}
	if len(pairs) == 0 {
		pairs, err = s.bandPairs(ctx, "dx_baseline_global", nil, band, slot)
		if err != nil {
			return 0, 0, false, err
		}
	}
	if len(pairs) == 0 {
		return 0, 0, false, nil
	}

	maxCount := int64(0)
	for _, p := range pairs {
		if p.Count > maxCount {
			maxCount = p.Count
		}
	}

	type item struct {
		score  float64
		weight int64
	}
	items := make([]item, 0, len(pairs))
	var totalWeight int64
	for _, p := range pairs {
		if p.Count <= 0 {
			continue
		}
		distanceNorm := clamp01(float64(p.DistanceTier) / 4.0)
		snrNorm := clamp01(float64(p.SnrTier) / 3.0)
		activityNorm := 0.5
		if maxCount > 0 {
			activityNorm = clamp01(float64(p.Count) / float64(maxCount))
		}
		score := (0.45*distanceNorm + 0.35*activityNorm + 0.20*snrNorm) * 100.0
		items = append(items, item{score: score, weight: p.Count})
		totalWeight += p.Count
	}
	if len(items) == 0 || totalWeight < dxMinBaselineQuantileSupport {
		return 0, 0, false, nil
	}

	// Tiny list; insertion sort is fine and avoids extra imports.
	for i := 1; i < len(items); i++ {
		j := i
		for j > 0 && items[j-1].score > items[j].score {
			items[j-1], items[j] = items[j], items[j-1]
			j--
		}
	}

	q25Target := float64(totalWeight) * 0.25
	q75Target := float64(totalWeight) * 0.75
	q25 := items[0].score
	q75 := items[len(items)-1].score
	cum := int64(0)
	for _, it := range items {
		cum += it.weight
		if float64(cum) >= q25Target {
			q25 = it.score
			break
		}
	}
	cum = 0
	for _, it := range items {
		cum += it.weight
		if float64(cum) >= q75Target {
			q75 = it.score
			break
		}
	}
	return q25, q75, true, nil
}

func (s *dxPostgresStore) recentEvents(now int64) ([]dxObservedEvent, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	windowStart := now - int64(dxSparklineBins*dxSparklineBinSeconds)
	rows, err := s.pool.Query(ctx, `
		SELECT
			spot_time, band, sender_callsign, receiver_callsign,
			sender_locator, receiver_locator, signal_report_db
		FROM dx_raw_spots
		WHERE spot_time BETWEEN $1 AND $2
		ORDER BY spot_time ASC
	`, windowStart, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]dxObservedEvent, 0, 4096)
	for rows.Next() {
		var ev dxObservedEvent
		if err := rows.Scan(&ev.T, &ev.B, &ev.SC, &ev.RC, &ev.SL, &ev.RL, &ev.RP); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (s *dxPostgresStore) baselineStats(now int64) (int, int, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var bucketCount int64
	var eventCount int64
	var minT, maxT *int64
	err := s.pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM dx_baseline_global) + (SELECT COUNT(*) FROM dx_baseline_target),
			(SELECT COUNT(*) FROM dx_raw_spots),
			(SELECT MIN(spot_time) FROM dx_raw_spots),
			(SELECT MAX(spot_time) FROM dx_raw_spots)
	`).Scan(&bucketCount, &eventCount, &minT, &maxT)
	if err != nil {
		return 0, 0, 0, err
	}
	historyM := 0
	if minT != nil && maxT != nil && *maxT >= *minT {
		historyM = int((*maxT - *minT) / 60)
	}
	return int(bucketCount), int(eventCount), historyM, nil
}

func (s *dxPostgresStore) loadSpotsBetween(start, end int64) ([]MQTTMessage, error) {
	if end <= 0 {
		end = time.Now().Unix()
	}
	if start > end {
		start = end
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rows, err := s.pool.Query(ctx, `
		SELECT
			spot_time, sender_callsign, sender_locator,
			receiver_callsign, receiver_locator,
			band, mode, signal_report_db
		FROM dx_raw_spots
		WHERE spot_time BETWEEN $1 AND $2
		ORDER BY spot_time ASC
	`, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]MQTTMessage, 0, 8192)
	for rows.Next() {
		var m MQTTMessage
		if err := rows.Scan(&m.T, &m.SC, &m.SL, &m.RC, &m.RL, &m.B, &m.MD, &m.RP); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *dxPostgresStore) loadRecentSpotCache(minutes int, now int64) ([]MQTTMessage, error) {
	if minutes <= 0 {
		minutes = 60
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	windowStart := now - int64(minutes*60)
	return s.loadSpotsBetween(windowStart, now)
}
