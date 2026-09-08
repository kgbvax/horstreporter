package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const defaultDxPostgresDSN = "postgres://dxuser@localhost:5432/dxdata?sslmode=disable"

const (
	dxBaselineFlushInterval   = 2000 * time.Millisecond
	dxBaselineFlushMaxPending = 2048
)

type dxPostgresStore struct {
	pool *pgxpool.Pool

	mu              sync.Mutex
	pendingGlobal   map[baselineGlobalKey]baselineDelta
	pendingRegion   map[dxPulseRegionBaselineDailyKey]int64
	pendingCluster  map[clusterBaselineKey]baselineDelta
	pendingCount    int
	pendingRawSpots []rawSpotRow

	flushCh   chan struct{}
	stopCh    chan struct{}
	closeOnce *sync.Once
	wg        sync.WaitGroup

	// Flush health instrumentation. Flush failures were logged one INFO line
	// at a time for 11 days (Aug 28 – Sep 7 2026) without anyone noticing,
	// which is how a multi-week persistence outage went unseen until a crash
	// wiped the (then-unlogged) raw table. Streak counters escalate the log
	// line and feed /api/stats.
	rawFlushLastOKUnix      atomic.Int64
	rawFlushFailStreak      atomic.Int64
	baselineFlushLastOKUnix atomic.Int64
	baselineFlushFailStreak atomic.Int64

	// baselineStatsCache memoizes the display-only baseline stats (bucket/event
	// planner estimates + the baseline_first_observed_at history span). These
	// change on the scale of minutes but baselineStats ran 2–3 PG round-trips on
	// every /api/dx_conditions poll (~15s per active browser). 60s TTL.
	baselineStatsMu   sync.Mutex
	baselineStatsAt   int64
	baselineStatsBkt  int
	baselineStatsEv   int
	baselineStatsHist int
}

type baselineDelta struct {
	Count int64
}

type baselineGlobalKey struct {
	Band         string
	SlotOfDay    int
	DistanceTier int
	SnrTier      int
}

type baselinePair struct {
	DistanceTier int
	SnrTier      int
	Count        int64
}

// clusterBaselineKey keys the dx_baseline_cluster table: the 6×6 grid-cluster
// anchor locator + the standard 4-dimension bucket key. Replaces both the old
// per-callsign dx_baseline_target and the 11-region dx_baseline_region tables.
type clusterBaselineKey struct {
	ClusterAnchor string
	Band          string
	SlotOfDay     int
	DistanceTier  int
	SnrTier       int
}

// bandSlotKey indexes the all-bands/all-slots baseline breakdown by (band, slot).
type bandSlotKey struct {
	Band string
	Slot int
}

// rawSpotRow is one spot enqueued for the next flushRawSpots batch. The
// derived columns (source4, lat/lon, upper-trimmed callsigns/locators/mode) are
// precomputed in observe BEFORE the store lock so flushRawSpots can build the
// multi-VALUES INSERT without recomputing per row.
type rawSpotRow struct {
	m                  MQTTMessage
	band               string
	source4            string
	lat, lon           float64
	sc, rc, sl, rl, md string
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
	st.pendingGlobal = make(map[baselineGlobalKey]baselineDelta, 2048)
	st.pendingRegion = make(map[dxPulseRegionBaselineDailyKey]int64, 2048)
	st.pendingCluster = make(map[clusterBaselineKey]baselineDelta, 2048)
	st.pendingRawSpots = make([]rawSpotRow, 0, 512)
	st.flushCh = make(chan struct{}, 1)
	st.stopCh = make(chan struct{})
	st.closeOnce = &sync.Once{}
	if err := st.initSchema(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	st.startBatchFlusher()
	return st, nil
}

func (s *dxPostgresStore) Close() {
	if s == nil {
		return
	}
	// Guard against double-close: close(s.stopCh) panics if called twice.
	s.closeOnce.Do(func() { close(s.stopCh) })
	s.wg.Wait()
	if s.pool != nil {
		s.pool.Close()
	}
}

func (s *dxPostgresStore) startBatchFlusher() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(dxBaselineFlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.flushPendingWithTimeout(4 * time.Second)
			case <-s.flushCh:
				s.flushPendingWithTimeout(4 * time.Second)
			case <-s.stopCh:
				s.flushPendingWithTimeout(5 * time.Second)
				return
			}
		}
	}()
}

func (s *dxPostgresStore) signalFlush() {
	select {
	case s.flushCh <- struct{}{}:
	default:
	}
}

func (s *dxPostgresStore) flushPendingWithTimeout(timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := s.flushPending(ctx); err != nil {
		s.reportFlushFailure("baseline", err, &s.baselineFlushFailStreak, &s.baselineFlushLastOKUnix)
	} else {
		recordFlushSuccess(&s.baselineFlushFailStreak, &s.baselineFlushLastOKUnix)
	}
	if err := s.flushRawSpots(ctx); err != nil {
		s.reportFlushFailure("raw spot", err, &s.rawFlushFailStreak, &s.rawFlushLastOKUnix)
	} else {
		recordFlushSuccess(&s.rawFlushFailStreak, &s.rawFlushLastOKUnix)
	}
}

func recordFlushSuccess(streak, lastOK *atomic.Int64) {
	lastOK.Store(time.Now().Unix())
	streak.Store(0)
}

// reportFlushFailure keeps the per-tick INFO line and escalates to an error
// line once failures form a streak. Tick is dxBaselineFlushInterval (2s):
// 15 ≈ 30s of failures, 450 ≈ 15min, then one escalation per hour.
func (s *dxPostgresStore) reportFlushFailure(kind string, err error, streak, lastOK *atomic.Int64) {
	n := streak.Add(1)
	logInfo("DX postgres %s batch flush failed: %v", kind, err)
	if n == 15 || n == 450 || (n > 450 && n%1800 == 0) {
		ago := "never in this process"
		if last := lastOK.Load(); last > 0 {
			ago = time.Since(time.Unix(last, 0)).Round(time.Minute).String()
		}
		logError("DX postgres %s flush has failed %d times consecutively (last success: %s); %s history persistence is DOWN",
			kind, n, ago, kind)
	}
}

// FlushHealth exposes the persistence counters for /api/stats: unix time of
// the last successful flush and the current consecutive-failure streak, for
// the raw-spot and baseline flushes separately.
func (s *dxPostgresStore) FlushHealth() (rawLastOK, rawStreak, baselineLastOK, baselineStreak int64) {
	return s.rawFlushLastOKUnix.Load(), s.rawFlushFailStreak.Load(),
		s.baselineFlushLastOKUnix.Load(), s.baselineFlushFailStreak.Load()
}

func (s *dxPostgresStore) flushPending(ctx context.Context) error {
	s.mu.Lock()
	if len(s.pendingGlobal) == 0 && len(s.pendingCluster) == 0 && len(s.pendingRegion) == 0 {
		s.mu.Unlock()
		return nil
	}

	global := s.pendingGlobal
	region := s.pendingRegion
	cluster := s.pendingCluster
	globalCount := len(global)
	regionCount := len(region)
	clusterCount := len(cluster)
	s.pendingGlobal = make(map[baselineGlobalKey]baselineDelta, len(global)/2+16)
	s.pendingRegion = make(map[dxPulseRegionBaselineDailyKey]int64, len(region)/2+16)
	s.pendingCluster = make(map[clusterBaselineKey]baselineDelta, len(cluster)/2+16)
	s.pendingCount = 0
	s.mu.Unlock()

	started := time.Now()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		s.mergePendingBack(global, region, cluster)
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	batch := &pgx.Batch{}
	queued := 0

	for k, d := range global {
		batch.Queue(`
			INSERT INTO dx_baseline_global (band, slot_of_day, distance_tier, snr_tier, count)
			VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (band, slot_of_day, distance_tier, snr_tier)
			DO UPDATE SET
				count = dx_baseline_global.count + EXCLUDED.count
		`, k.Band, k.SlotOfDay, k.DistanceTier, k.SnrTier, d.Count)
		queued++
	}

	for k, v := range region {
		batch.Queue(`
			INSERT INTO dx_region_baseline_daily (target_grid4, band, slot_of_day, region, day_index, spot_count)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (target_grid4, band, slot_of_day, region, day_index)
			DO UPDATE SET spot_count = dx_region_baseline_daily.spot_count + EXCLUDED.spot_count
		`, k.TargetGrid4, k.Band, k.SlotOfDay, k.Region, k.DayIndex, v)
		queued++
	}

	for k, d := range cluster {
		batch.Queue(`
			INSERT INTO dx_baseline_cluster (cluster_anchor, band, slot_of_day, distance_tier, snr_tier, count)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (cluster_anchor, band, slot_of_day, distance_tier, snr_tier)
			DO UPDATE SET
				count = dx_baseline_cluster.count + EXCLUDED.count
		`, k.ClusterAnchor, k.Band, k.SlotOfDay, k.DistanceTier, k.SnrTier, d.Count)
		queued++
	}

	if queued > 0 {
		br := tx.SendBatch(ctx, batch)
		for i := 0; i < queued; i++ {
			if _, err := br.Exec(); err != nil {
				_ = br.Close()
				s.mergePendingBack(global, region, cluster)
				return err
			}
		}
		if err := br.Close(); err != nil {
			s.mergePendingBack(global, region, cluster)
			return err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		s.mergePendingBack(global, region, cluster)
		return err
	}
	logDebug("DX postgres baseline batch flushed: global=%d region=%d cluster=%d total=%d in %s", globalCount, regionCount, clusterCount, globalCount+regionCount+clusterCount, time.Since(started).Round(time.Millisecond))
	return nil
}

func (s *dxPostgresStore) mergePendingBack(global map[baselineGlobalKey]baselineDelta, region map[dxPulseRegionBaselineDailyKey]int64, cluster map[clusterBaselineKey]baselineDelta) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for k, d := range global {
		e := s.pendingGlobal[k]
		e.Count += d.Count
		s.pendingGlobal[k] = e
		s.pendingCount++
	}
	for k, d := range cluster {
		e := s.pendingCluster[k]
		e.Count += d.Count
		s.pendingCluster[k] = e
		s.pendingCount++
	}
	for k, v := range region {
		s.pendingRegion[k] += v
		s.pendingCount++
	}
}

func (s *dxPostgresStore) initSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE EXTENSION IF NOT EXISTS postgis;`,
		// v6 schema note: existing deploys with the old source4 shape must run
		// scripts/migrate_drop_source4.sql first — created-tables cover fresh
		// installs only. (The target and 11-region tables were removed in v8.)
		`CREATE TABLE IF NOT EXISTS dx_baseline_global (
			band TEXT NOT NULL,
			slot_of_day INTEGER NOT NULL,
			distance_tier INTEGER NOT NULL,
			snr_tier INTEGER NOT NULL,
			count BIGINT NOT NULL,
			PRIMARY KEY (band, slot_of_day, distance_tier, snr_tier)
		);`,
		// Grid-cluster baseline: the middle tier (cluster → global). Keyed by
		// the 6×6 cluster anchor locator + the standard 4-dimension bucket key.
		// Replaces both dx_baseline_target (per-callsign) and dx_baseline_region
		// (11 DXPulse regions) — one table, one granularity.
		`CREATE TABLE IF NOT EXISTS dx_baseline_cluster (
			cluster_anchor TEXT NOT NULL,
			band TEXT NOT NULL,
			slot_of_day INTEGER NOT NULL,
			distance_tier INTEGER NOT NULL,
			snr_tier INTEGER NOT NULL,
			count BIGINT NOT NULL,
			PRIMARY KEY (cluster_anchor, band, slot_of_day, distance_tier, snr_tier)
		);`,
		`ALTER TABLE dx_baseline_global DROP COLUMN IF EXISTS sum_distance;`,
		`ALTER TABLE dx_baseline_global DROP COLUMN IF EXISTS sum_snr;`,
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
			spot_geom geometry(Point, 4326),
			source_type TEXT NOT NULL DEFAULT 'mqtt',
			spotter_callsign TEXT NOT NULL DEFAULT '',
			frequency_khz DOUBLE PRECISION,
			comment TEXT NOT NULL DEFAULT ''
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
		`ALTER TABLE dx_raw_spots ADD COLUMN IF NOT EXISTS source_type TEXT NOT NULL DEFAULT 'mqtt';`,
		`ALTER TABLE dx_raw_spots ADD COLUMN IF NOT EXISTS spotter_callsign TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE dx_raw_spots ADD COLUMN IF NOT EXISTS frequency_khz DOUBLE PRECISION;`,
		`ALTER TABLE dx_raw_spots ADD COLUMN IF NOT EXISTS comment TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE dx_raw_spots ADD COLUMN IF NOT EXISTS spot_geom geometry(Point, 4326);`,
		`CREATE INDEX IF NOT EXISTS idx_dx_raw_spots_spot_time ON dx_raw_spots (spot_time);`,
		`CREATE INDEX IF NOT EXISTS idx_dx_raw_spots_source_type_spot_time ON dx_raw_spots (source_type, spot_time);`,
		// Locator-prefix indexes for activityByBinForTargets /
		// recent24hBandSlotCountsForTokens: their ~>=~/~<~ prefix-range arms
		// are members of the text_pattern_ops opfamily (collation-
		// independent), so the planner BitmapOr's them into index scans
		// instead of full-window filter scans (which blew the 6s ctx timeout
		// at ≥90-min windows on the prod box). On a large existing table run
		// scripts/migrate_add_locator_prefix_indexes.sql (CONCURRENTLY, no
		// ingest lock) BEFORE deploying a binary that includes this — the IF
		// NOT EXISTS here then no-ops; otherwise startup builds them serially,
		// blocking raw-spot inserts for the build's duration.
		`CREATE INDEX IF NOT EXISTS idx_dx_raw_spots_sender_loc_time ON dx_raw_spots (sender_locator text_pattern_ops, spot_time);`,
		`CREATE INDEX IF NOT EXISTS idx_dx_raw_spots_receiver_loc_time ON dx_raw_spots (receiver_locator text_pattern_ops, spot_time);`,
		// No GIST index on spot_geom: nothing in this codebase queries it
		// (verified by grep), it costs ~5GB on prod, and the prod disk ran
		// full. The column stays; the optional maintenance list below drops
		// any leftover index from earlier deploys.
		`CREATE TABLE IF NOT EXISTS dx_region_baseline_daily (
			target_grid4 TEXT NOT NULL,
			band TEXT NOT NULL,
			slot_of_day INTEGER NOT NULL,
			region TEXT NOT NULL,
			day_index BIGINT NOT NULL,
			spot_count BIGINT NOT NULL,
			PRIMARY KEY (target_grid4, band, slot_of_day, region, day_index)
		);`,
		// day_index is the last PK column, so neither the reader
		// (regionCalendarStats: WHERE day_index BETWEEN ...) nor the prune
		// (pruneDayIndexedBaselineOlderThan: WHERE day_index < cutoff) can use
		// the PK — both would seq-scan the whole table. This index makes them
		// sargable. On a large existing table build it CONCURRENTLY via
		// scripts/migrate_add_region_day_index.sql BEFORE deploying a binary
		// that includes this; the IF NOT EXISTS here then no-ops.
		`CREATE INDEX IF NOT EXISTS idx_dx_region_baseline_daily_day_index ON dx_region_baseline_daily (day_index);`,
		`CREATE TABLE IF NOT EXISTS dx_meta (
			k TEXT PRIMARY KEY,
			v TEXT NOT NULL
		);`,
	}

	stmts = append(stmts, cellfeedSchemaStmts()...)

	for _, q := range stmts {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			return err
		}
	}

	type optionalMaintenanceStmt struct {
		name string
		sql  string
	}

	optional := []optionalMaintenanceStmt{
		{
			// Dead since the Propagation Lab removal (2026-08-04): empty on
			// prod, zero readers in this codebase, only legacy schema upkeep.
			name: "drop empty legacy table proplab_dest_buckets",
			sql:  `DROP TABLE IF EXISTS proplab_dest_buckets;`,
		},
		{
			// Dormant since the Propagation Lab removal: documented dormant in
			// docs/api.md, zero writers/readers in this codebase.
			name: "drop dormant legacy table proplab_drap_snapshots",
			sql:  `DROP TABLE IF EXISTS proplab_drap_snapshots;`,
		},
		{
			name: "drop dormant legacy table proplab_events",
			sql:  `DROP TABLE IF EXISTS proplab_events;`,
		},
		{
			// 1.4GB on prod with 2 lifetime scans; nothing queries (band,
			// spot_time) — the spot_time and (source_type, spot_time) indexes
			// cover the real access paths.
			name: "drop unused idx_dx_raw_spots_band_spot_time",
			sql:  `DROP INDEX CONCURRENTLY IF EXISTS idx_dx_raw_spots_band_spot_time;`,
		},
		{
			// ~2.6GB on prod, 0 lifetime scans: redundant with the primary
			// key, which leads with bucket_start and already serves the
			// retention prune and pathscope's baseline lookups.
			name: "drop redundant idx_proplab_cell_band_bucket",
			sql:  `DROP INDEX CONCURRENTLY IF EXISTS idx_proplab_cell_band_bucket;`,
		},
		{
			name: "drop redundant idx_proplab_cell_region_band_bucket",
			sql:  `DROP INDEX CONCURRENTLY IF EXISTS idx_proplab_cell_region_band_bucket;`,
		},
		{
			name: "drop redundant idx_proplab_cell_bucket_time",
			sql:  `DROP INDEX CONCURRENTLY IF EXISTS idx_proplab_cell_bucket_time;`,
		},
		{
			name: "drop legacy idx_dx_raw_spots_t",
			sql:  `DROP INDEX CONCURRENTLY IF EXISTS idx_dx_raw_spots_t;`,
		},
		{
			name: "drop legacy idx_dx_raw_spots_band_t",
			sql:  `DROP INDEX CONCURRENTLY IF EXISTS idx_dx_raw_spots_band_t;`,
		},
		{
			name: "drop legacy idx_dx_raw_spots_geom",
			sql:  `DROP INDEX CONCURRENTLY IF EXISTS idx_dx_raw_spots_geom;`,
		},
		{
			// ~5GB on prod, zero query users in this codebase (spot_geom the
			// column is kept; only the index goes). Created by earlier deploys.
			name: "drop unused idx_dx_raw_spots_spot_geom",
			sql:  `DROP INDEX CONCURRENTLY IF EXISTS idx_dx_raw_spots_spot_geom;`,
		},
		{
			name: "drop redundant idx_dx_baseline_global_band_slot",
			sql:  `DROP INDEX CONCURRENTLY IF EXISTS idx_dx_baseline_global_band_slot;`,
		},
		{
			name: "drop redundant idx_dx_region_baseline_lookup",
			sql:  `DROP INDEX CONCURRENTLY IF EXISTS idx_dx_region_baseline_lookup;`,
		},
		{
			name: "tune autovacuum dx_baseline_global",
			sql: `ALTER TABLE dx_baseline_global SET (
				autovacuum_vacuum_scale_factor = 0.02,
				autovacuum_vacuum_threshold = 20000,
				autovacuum_analyze_scale_factor = 0.01,
				autovacuum_analyze_threshold = 20000
			);`,
		},
		{
			name: "tune autovacuum dx_region_baseline_daily",
			sql: `ALTER TABLE dx_region_baseline_daily SET (
				autovacuum_vacuum_scale_factor = 0.01,
				autovacuum_vacuum_threshold = 50000,
				autovacuum_analyze_scale_factor = 0.005,
				autovacuum_analyze_threshold = 50000
			);`,
		},
		{
			name: "tune autovacuum dx_raw_spots",
			sql: `ALTER TABLE dx_raw_spots SET (
				autovacuum_vacuum_scale_factor = 0.05,
				autovacuum_analyze_scale_factor = 0.02
			);`,
		},
	}

	for _, st := range optional {
		if _, err := s.pool.Exec(ctx, st.sql); err != nil {
			logInfo("DX postgres optional schema maintenance skipped (%s): %v", st.name, err)
		}
	}
	return nil
}

func (s *dxPostgresStore) ensureDxPulseRegionBaseline(ctx context.Context) error {
	var baselineExists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM dx_region_baseline_daily LIMIT 1)`).Scan(&baselineExists); err != nil {
		return err
	}
	if baselineExists {
		return nil
	}

	var rawExists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM dx_raw_spots LIMIT 1)`).Scan(&rawExists); err != nil {
		return err
	}
	if !rawExists {
		return nil
	}

	logInfo("DXPulse region baseline backfill starting from existing raw spots")

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

	logInfo("DXPulse region baseline backfill finished (%d raw spots processed)", processed)
	return nil
}

// ensureDxBaselineCluster backfills the dx_baseline_cluster table from
// dx_raw_spots on the first startup after the table is created. Mirrors
// ensureDxPulseRegionBaseline: if the table already has rows, it's a no-op;
// otherwise it scans dx_raw_spots in ascending spot_time order, recomputes
// the (cluster_anchor, band, slot_of_day, distance_tier, snr_tier) bucket key
// for each spot (deriving the cluster anchor, slot, dist tier, snr tier from
// the locators and signal_report_db), and batches upserts.
func (s *dxPostgresStore) ensureDxBaselineCluster(ctx context.Context) error {
	var baselineExists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM dx_baseline_cluster LIMIT 1)`).Scan(&baselineExists); err != nil {
		return err
	}
	if baselineExists {
		return nil
	}

	var rawExists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM dx_raw_spots LIMIT 1)`).Scan(&rawExists); err != nil {
		return err
	}
	if !rawExists {
		return nil
	}

	logInfo("DX grid-cluster baseline backfill starting from existing raw spots")

	rows, err := s.pool.Query(ctx, `
		SELECT spot_time, band, sender_locator, receiver_locator, signal_report_db
		FROM dx_raw_spots
		ORDER BY spot_time ASC
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	agg := make(map[clusterBaselineKey]int64, 8192)
	flush := func() error {
		if len(agg) == 0 {
			return nil
		}
		batch := &pgx.Batch{}
		keys := make([]clusterBaselineKey, 0, len(agg))
		for k, v := range agg {
			keys = append(keys, k)
			batch.Queue(`
				INSERT INTO dx_baseline_cluster (cluster_anchor, band, slot_of_day, distance_tier, snr_tier, count)
				VALUES ($1,$2,$3,$4,$5,$6)
				ON CONFLICT (cluster_anchor, band, slot_of_day, distance_tier, snr_tier)
				DO UPDATE SET count = dx_baseline_cluster.count + EXCLUDED.count
			`, k.ClusterAnchor, k.Band, k.SlotOfDay, k.DistanceTier, k.SnrTier, v)
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
	skipped := int64(0)
	for rows.Next() {
		var ts int64
		var band, senderLoc, receiverLoc string
		var snr int
		if err := rows.Scan(&ts, &band, &senderLoc, &receiverLoc, &snr); err != nil {
			return err
		}
		// Recompute the bucket dimensions the in-memory Observe derives:
		// normalizeBand, utcSlotOfDay, distanceTierForLocators, snrTierFromDb,
		// and the cluster anchor. Both ends' clusters get a bucket (a spot
		// between cluster A and cluster B increments both), matching Observe.
		nb := normalizeBand(band)
		if nb == "" || !bandInScope(nb) {
			skipped++
			continue
		}
		slot := utcSlotOfDay(ts)
		distTier := distanceTierForLocators(senderLoc, receiverLoc)
		snrt := snrTierFromDb(snr)
		for _, loc := range [2]string{senderLoc, receiverLoc} {
			anchor, ok := locatorClusterAnchor(loc)
			if !ok {
				continue
			}
			agg[clusterBaselineKey{
				ClusterAnchor: anchor,
				Band:          nb,
				SlotOfDay:     slot,
				DistanceTier:  distTier,
				SnrTier:       snrt,
			}]++
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

	logInfo("DX grid-cluster baseline backfill finished (%d raw spots processed, %d skipped)", processed, skipped)
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

func (s *dxPostgresStore) observe(m MQTTMessage, band string, slotOfDay, distTier, snrTier int) error {
	// Precompute the derived dx_raw_spots columns BEFORE taking the store lock
	// (these only depend on m) so flushRawSpots can emit a multi-VALUES INSERT
	// without recomputing per row and so the ingest lock critical section isn't
	// extended by the normalization/geo work.
	isDXCluster := strings.EqualFold(strings.TrimSpace(m.MD), "DXCLUSTER")
	var row rawSpotRow
	if !isDXCluster {
		source4 := normalizeSource4(m.SL)
		lat, lon := locatorToLatLng(source4)
		row = rawSpotRow{
			m:       m,
			band:    band,
			source4: source4,
			lat:     lat,
			lon:     lon,
			sc:      strings.ToUpper(strings.TrimSpace(m.SC)),
			rc:      strings.ToUpper(strings.TrimSpace(m.RC)),
			sl:      strings.ToUpper(strings.TrimSpace(m.SL)),
			rl:      strings.ToUpper(strings.TrimSpace(m.RL)),
			md:      strings.ToUpper(strings.TrimSpace(m.MD)),
		}
	}

	s.mu.Lock()
	gk := baselineGlobalKey{
		Band:         band,
		SlotOfDay:    slotOfDay,
		DistanceTier: distTier,
		SnrTier:      snrTier,
	}
	gd := s.pendingGlobal[gk]
	gd.Count += 1
	s.pendingGlobal[gk] = gd
	s.pendingCount++

	for _, key := range dxPulseRegionBaselineKeysForSpot(m.T, band, m.SL, m.RL) {
		s.pendingRegion[key] += 1
		s.pendingCount++
	}

	// Grid-cluster baseline (dx_baseline_cluster): increment for both ends'
	// cluster anchors so either operator's Evaluate benefits. This is the
	// Postgres mirror of the in-memory clusterBuckets write in Observe.
	for _, loc := range [2]string{m.SL, m.RL} {
		anchor, ok := locatorClusterAnchor(loc)
		if !ok {
			continue
		}
		rk := clusterBaselineKey{
			ClusterAnchor: anchor,
			Band:          band,
			SlotOfDay:     slotOfDay,
			DistanceTier:  distTier,
			SnrTier:       snrTier,
		}
		rd := s.pendingCluster[rk]
		rd.Count += 1
		s.pendingCluster[rk] = rd
		s.pendingCount++
	}

	// DX-cluster spots are persisted separately, with full fidelity (frequency,
	// comment, source_type 'dxcluster'), via PersistRawSpot. Don't also enqueue a
	// frequency-less 'mqtt' raw row for them here: that duplicate restores as an
	// unscorable "0 MHz" spot in the Chase Queue and can shadow the real
	// dxcluster row during /api/dxspots dedup. Baseline/region aggregation above
	// still counts the spot.
	if !isDXCluster {
		s.pendingRawSpots = append(s.pendingRawSpots, row)
		s.trimPendingRawSpotsLocked()
	}
	shouldFlush := s.pendingCount >= dxBaselineFlushMaxPending
	s.mu.Unlock()

	if shouldFlush {
		s.signalFlush()
	}
	return nil
}

// rawSpotInsertChunkSize bounds the number of rows per multi-VALUES INSERT so
// the per-statement parameter count (rows×15) stays well under pgx's 65535-param
// limit and each statement stays manageable. 1000 rows ⇒ 15000 params.
const rawSpotInsertChunkSize = 1000

// rawSpotMaxPendingRows caps the raw-spot retry buffer. When Postgres stays
// slow (evening FT8 peak), every flush fails and mergeRawSpotsBack requeues
// the batch while ~370 new spots/s keep appending: the buffer grows without
// bound and OOM-killed the memory-constrained prod box (Sep 4, 22:08). At
// ~370 spots/s the cap holds ~9 minutes of traffic — oldest rows are dropped
// first so the buffer converges instead of exploding; the live SSE stream
// and baselines are unaffected (this only bounds raw-history persistence).
const rawSpotMaxPendingRows = 200000

func (s *dxPostgresStore) flushRawSpots(ctx context.Context) error {
	s.mu.Lock()
	if len(s.pendingRawSpots) == 0 {
		s.mu.Unlock()
		return nil
	}
	rows := s.pendingRawSpots
	s.pendingRawSpots = make([]rawSpotRow, 0, len(rows)/2+16)
	s.mu.Unlock()

	var minSpotTime int64
	for start := 0; start < len(rows); start += rawSpotInsertChunkSize {
		end := start + rawSpotInsertChunkSize
		if end > len(rows) {
			end = len(rows)
		}
		chunk := rows[start:end]
		for _, row := range chunk {
			if row.m.T > 0 && (minSpotTime == 0 || row.m.T < minSpotTime) {
				minSpotTime = row.m.T
			}
		}
		q, args := buildRawSpotInsertSQL(chunk)
		if _, err := s.pool.Exec(ctx, q, args...); err != nil {
			// Re-queue the failed chunk and all remaining unflushed rows so a
			// transient PG error doesn't permanently drop spots from the raw
			// history. Earlier chunks in this loop already committed; the rows
			// from `start` onward are prepended back into pendingRawSpots,
			// mirroring mergePendingBack's requeue pattern for baseline deltas.
			s.mergeRawSpotsBack(rows[start:])
			return err
		}
	}
	s.touchBaselineFirstObserved(ctx, minSpotTime)
	return nil
}

// mergeRawSpotsBack re-enqueues raw spot rows that failed to flush back into
// pendingRawSpots so they survive a transient Postgres error and are retried
// on the next flush tick. Rows are prepended to preserve arrival order.
func (s *dxPostgresStore) mergeRawSpotsBack(rows []rawSpotRow) {
	if len(rows) == 0 {
		return
	}
	s.mu.Lock()
	// Prepend the failed rows ahead of any rows added since the swap so they
	// are retried first (oldest-first), keeping the stored order roughly stable.
	combined := make([]rawSpotRow, 0, len(rows)+len(s.pendingRawSpots))
	combined = append(combined, rows...)
	combined = append(combined, s.pendingRawSpots...)
	s.pendingRawSpots = combined
	s.trimPendingRawSpotsLocked()
	s.mu.Unlock()
}

// trimPendingRawSpotsLocked drops the oldest buffered rows when the retry
// buffer exceeds rawSpotMaxPendingRows. Caller must hold s.mu.
func (s *dxPostgresStore) trimPendingRawSpotsLocked() {
	if excess := len(s.pendingRawSpots) - rawSpotMaxPendingRows; excess > 0 {
		s.pendingRawSpots = s.pendingRawSpots[excess:len(s.pendingRawSpots):len(s.pendingRawSpots)]
		logInfo("Raw spot retry buffer overflowed; dropped %d oldest rows (Postgres persisting slower than ingest)", excess)
	}
}

// buildRawSpotInsertSQL builds one multi-VALUES INSERT for a chunk of precomputed
// rawSpotRows. Each row contributes 15 parameters; the spot_geom column is built
// in SQL via ST_SetSRID(ST_MakePoint(lon, lat), 4326) (NULL when lat=lon=0),
// identical to the old per-row INSERT. Extracted so it can be unit-tested without
// a database.
func buildRawSpotInsertSQL(chunk []rawSpotRow) (string, []any) {
	const cols = 15
	var b strings.Builder
	b.WriteString(`INSERT INTO dx_raw_spots (
		spot_time, band, sender_callsign, receiver_callsign,
		sender_locator, receiver_locator, mode, signal_report_db,
		source_grid4, spot_geom,
		source_type, spotter_callsign, frequency_khz, comment)
	VALUES `)
	args := make([]any, 0, len(chunk)*cols)
	for ri, row := range chunk {
		if ri > 0 {
			b.WriteByte(',')
		}
		base := ri*cols + 1           // 1-indexed placeholder base
		latp, lonp := base+9, base+10 // $lat, $lon
		// spot_time..source_grid4 (9 direct), spot_geom (CASE expr), source_type..comment (4)
		fmt.Fprintf(&b, "($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,"+
			"CASE WHEN $%d::float8 = 0 AND $%d::float8 = 0 THEN NULL "+
			"ELSE ST_SetSRID(ST_MakePoint($%d, $%d), 4326) END,"+
			"$%d,$%d,$%d,$%d)",
			base, base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8,
			latp, lonp, lonp, latp,
			base+11, base+12, base+13, base+14)
		args = append(args,
			row.m.T,
			row.band,
			row.sc,
			row.rc,
			row.sl,
			row.rl,
			row.md,
			row.m.RP,
			row.source4,
			row.lat,
			row.lon,
			"mqtt",
			"",
			(*float64)(nil),
			"",
		)
	}
	return b.String(), args
}

func (s *dxPostgresStore) insertRawSpot(ctx context.Context, m MQTTMessage, band, sourceType, spotter string, frequencyKHz *float64, comment string) error {
	if band == "" {
		band = normalizeBand(m.B)
	}
	if band == "" {
		band = "unknown"
	}

	source4 := normalizeSource4(m.SL)
	lat, lon := locatorToLatLng(source4)

	if strings.TrimSpace(sourceType) == "" {
		sourceType = "mqtt"
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO dx_raw_spots (
			spot_time, band, sender_callsign, receiver_callsign,
			sender_locator, receiver_locator, mode, signal_report_db,
			source_grid4, spot_geom,
			source_type, spotter_callsign, frequency_khz, comment
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,
			CASE WHEN $10::float8 = 0 AND $11::float8 = 0 THEN NULL
			ELSE ST_SetSRID(ST_MakePoint($11, $10), 4326) END,
			$12,$13,$14,$15)
	`,
		m.T,
		band,
		strings.ToUpper(strings.TrimSpace(m.SC)),
		strings.ToUpper(strings.TrimSpace(m.RC)),
		strings.ToUpper(strings.TrimSpace(m.SL)),
		strings.ToUpper(strings.TrimSpace(m.RL)),
		strings.ToUpper(strings.TrimSpace(m.MD)),
		m.RP,
		source4,
		lat,
		lon,
		strings.ToLower(strings.TrimSpace(sourceType)),
		strings.ToUpper(strings.TrimSpace(spotter)),
		frequencyKHz,
		strings.TrimSpace(comment),
	)
	return err
}

func (s *dxPostgresStore) bandPairs(ctx context.Context, table string, _ []string, band string, slot int) ([]baselinePair, error) {
	// Only dx_baseline_global is queried here now (the target table was removed
	// in v8; cluster queries go through clusterBandPairs). The targets arg is
	// kept for signature stability but unused.
	q := `
		SELECT distance_tier, snr_tier, SUM(count)::bigint
		FROM dx_baseline_global
		WHERE band = $1 AND slot_of_day = $2
		GROUP BY distance_tier, snr_tier
	`
	args := []any{band, slot}
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

// allBandBaselinePairs fetches the full baseline bucket breakdown for every band
// and slot in two queries: target-filtered (summed across the given tokens) and
// global. Returns indexes keyed by (band, slot) → per-(distance_tier, snr_tier)
// pairs, from which every per-band baseline value Evaluate needs (activity,
// support, quantiles, all-slots rates) is derived in memory. Replaces ~100
// per-band round-trips (4 methods × ~13 bands × 1-2 RT each) with 2.
//
// targetIdx is nil when targets is empty. Both indexes are nil only on query
// error; callers fall back to zero/unused, matching the old per-call err paths.
// allBandBaselinePairs fetches the two baseline indexes (cluster, global)
// needed by Evaluate's per-band loop in 2 PG round-trips. clusterIdx is nil
// when operatorCluster is "". Both indexes are nil only on query error;
// callers fall back to zero/unused.
func (s *dxPostgresStore) allBandBaselinePairs(operatorCluster string) (map[bandSlotKey][]baselinePair, map[bandSlotKey][]baselinePair, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	var clusterIdx map[bandSlotKey][]baselinePair
	if operatorCluster != "" {
		rows, err := s.pool.Query(ctx, `
			SELECT band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
			FROM dx_baseline_cluster
			WHERE cluster_anchor = $1
			GROUP BY band, slot_of_day, distance_tier, snr_tier
		`, operatorCluster)
		if err != nil {
			return nil, nil, err
		}
		clusterIdx, err = scanBandSlotPairs(rows)
		if err != nil {
			return nil, nil, err
		}
	}

	rows, err := s.pool.Query(ctx, `
		SELECT band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
		FROM dx_baseline_global
		GROUP BY band, slot_of_day, distance_tier, snr_tier
	`)
	if err != nil {
		return nil, nil, err
	}
	globalIdx, err := scanBandSlotPairs(rows)
	if err != nil {
		return nil, nil, err
	}
	return clusterIdx, globalIdx, nil
}

// scanBandSlotPairs drains rows into a map[bandSlotKey][]baselinePair.
func scanBandSlotPairs(rows pgx.Rows) (map[bandSlotKey][]baselinePair, error) {
	defer rows.Close()
	idx := make(map[bandSlotKey][]baselinePair, 1024)
	for rows.Next() {
		var (
			band string
			slot int
			p    baselinePair
		)
		if err := rows.Scan(&band, &slot, &p.DistanceTier, &p.SnrTier, &p.Count); err != nil {
			return nil, err
		}
		k := bandSlotKey{Band: band, Slot: slot}
		idx[k] = append(idx[k], p)
	}
	return idx, rows.Err()
}

// maxPlausibleBaselineCount bounds an honest per-(distance_tier, snr_tier)
// baseline count. Counts are per-spot increments (≤2 per spot, one per end) on
// a 30-min slot, so even a wildly generous 1e6 spots/min feed stays 4+ orders
// below this over a year. The historic mergePendingBack double-merge bug
// inflated dx_baseline_cluster counts to ~2^62 before int64 wrap (some wrapped
// negative); read sites treat anything outside this bound as corrupt data and
// skip it rather than folding it into scores/quantiles. Paired with the
// prod-side canary: rows with count < 0 OR count > maxPlausibleBaselineCount
// should be investigated, not trusted.
const maxPlausibleBaselineCount = int64(1) << 30 // ~1.07e9

// plausibleBaselinePair reports whether one baseline pair's count is sane
// enough to feed scoring. Corrupt pairs (overflowed or wrapped counts) are
// excluded so a single bad row can't own activity/support/quantiles.
func plausibleBaselinePair(p baselinePair) bool {
	return p.Count > 0 && p.Count <= maxPlausibleBaselineCount
}

// sumPairs returns the total count across pairs — the numerator for both
// activity (spots/min after normalisation) and support. Implausible pairs
// (see maxPlausibleBaselineCount) are skipped so corrupt rows can't swamp or
// wrap the sum.
func sumPairs(pairs []baselinePair) int64 {
	var sum int64
	for _, p := range pairs {
		if !plausibleBaselinePair(p) {
			continue
		}
		sum += p.Count
	}
	return sum
}

// pairsForBandSlot returns the pairs for (band, slot) using a two-tier
// fallback: cluster → global, matching the in-memory helpers. Returns
// (pairs, clusterUsed). A nil/empty cluster index yields global + false.
func pairsForBandSlot(clusterIdx, globalIdx map[bandSlotKey][]baselinePair, band string, slot int) ([]baselinePair, bool) {
	if clusterIdx != nil {
		if pairs, ok := clusterIdx[bandSlotKey{Band: band, Slot: slot}]; ok && len(pairs) > 0 {
			return pairs, true
		}
	}
	return globalIdx[bandSlotKey{Band: band, Slot: slot}], false
}

// quantilesFromPairs computes the weighted q25/q75 band-score quantiles from
// per-(distance_tier, snr_tier) pairs. Lifted verbatim from the old
// baselineQuantilesForBand so the collapsed path produces identical values.
// Returns ok=false when there are no pairs or total support is below
// dxMinBaselineQuantileSupport. Implausible pairs (see maxPlausibleBaselineCount)
// are excluded — one corrupt row would otherwise collapse the whole weighted
// distribution onto its own score and own q25/q75.
func quantilesFromPairs(pairs []baselinePair) (q25, q75 float64, ok bool) {
	if len(pairs) == 0 {
		return 0, 0, false
	}
	maxCount := int64(0)
	for _, p := range pairs {
		if plausibleBaselinePair(p) && p.Count > maxCount {
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
		if !plausibleBaselinePair(p) {
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
		return 0, 0, false
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
	q25 = items[0].score
	q75 = items[len(items)-1].score
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
	return q25, q75, true
}

// baselineP90DistanceForBand returns the tier-weighted p90 path length for a
// (band, slot), summed across all SNR tiers. Uses a two-tier fallback:
// cluster → global. Returns (km, clusterUsed, err).
func (s *dxPostgresStore) baselineP90DistanceForBand(operatorCluster, band string, slot int) (float64, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var tiers [5]int64
	usedCluster := false
	if operatorCluster != "" {
		clusterPairs, err := s.clusterBandPairs(ctx, operatorCluster, band, slot)
		if err != nil {
			return 0, false, err
		}
		for _, p := range clusterPairs {
			if p.DistanceTier < 0 || p.DistanceTier > 4 || !plausibleBaselinePair(p) {
				continue
			}
			tiers[p.DistanceTier] += p.Count
			usedCluster = true
		}
	}
	if !usedCluster {
		globalPairs, err := s.bandPairs(ctx, "dx_baseline_global", nil, band, slot)
		if err != nil {
			return 0, false, err
		}
		for _, p := range globalPairs {
			if p.DistanceTier < 0 || p.DistanceTier > 4 || !plausibleBaselinePair(p) {
				continue
			}
			tiers[p.DistanceTier] += p.Count
		}
	}
	return p90FromTierCounts(tiers), usedCluster, nil
}

// clusterBandPairs queries dx_baseline_cluster for a single (cluster_anchor, band, slot).
func (s *dxPostgresStore) clusterBandPairs(ctx context.Context, clusterAnchor, band string, slot int) ([]baselinePair, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT distance_tier, snr_tier, SUM(count)::bigint
		FROM dx_baseline_cluster
		WHERE cluster_anchor = $1 AND band = $2 AND slot_of_day = $3
		GROUP BY distance_tier, snr_tier
	`, clusterAnchor, band, slot)
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

// locatorTargetRE matches a strict Maidenhead locator token (4–10 chars:
// field, square, optional subsquare/extended pairs). Only tokens matching
// this may match the locator columns of dx_raw_spots: the charset excludes
// SQL LIKE metacharacters (%/_/backslash — a raw-qth qth of "%" must not
// become a wildcard), and byte-range bounds derived from it reproduce
// exactly the live stream's isLocator+HasPrefix semantics (a token matches
// locators that extend it). Everything else (callsigns, malformed input)
// is matched only against the callsign columns with plain equality.
var locatorTargetRE = regexp.MustCompile(`^[A-R]{2}[0-9]{2}(?:[A-X]{2}(?:[0-9]{2}(?:[A-X]{2})?)?)?$`)

// splitTargetTokens partitions normalized (uppercased, deduplicated) target
// tokens into strict locator tokens and callsign candidates.
func splitTargetTokens(norm []string) (locators, calls []string) {
	for _, t := range norm {
		if locatorTargetRE.MatchString(t) {
			locators = append(locators, t)
		} else {
			calls = append(calls, t)
		}
	}
	return locators, calls
}

// locatorPrefixRange returns byte-range bounds [lo, hi) under the
// text_pattern_ops comparison order such that `s ~>=~ lo AND s ~<~ hi` holds
// iff s starts with prefix — the sargable equivalent of s LIKE 'prefix%'.
// hi is the prefix with its final byte incremented; the ASCII charset
// guaranteed by locatorTargetRE makes wrap-around impossible (ok=false only
// for the defensive 0xFF edge, in which case the caller emits an unbounded
// ~>=~ lo arm).
//
// Ranges are used instead of LIKE because PostgreSQL only rewrites LIKE into
// index range quals when the pattern is a plan-time constant — `col LIKE
// ANY($param)` from pgx is never index-sargable, while param-bounded
// ~>=~/~<~ comparisons are members of the text_pattern_ops opfamily and use
// the (locator, spot_time) indexes with any bound source.
func locatorPrefixRange(prefix string) (lo, hi string, ok bool) {
	if prefix == "" {
		return "", "", false
	}
	last := prefix[len(prefix)-1]
	if last == 0xFF {
		return prefix, "", false
	}
	return prefix, prefix[:len(prefix)-1] + string(last+1), true
}

// appendTargetArms builds the OR-ed target-match predicate shared by
// activityByBinForTargets and recent24hBandSlotCountsForTokens, appending
// placeholders' args and returning the next free placeholder index. Caller
// seeds args with its own leading parameters and passes that count as idx.
//
// Locator tokens become indexable ~>=~/~<~ prefix-range arms over both
// locator columns (BitmapOr-able); callsign tokens become = ANY arms — kept
// ONLY when non-locator tokens exist, because one non-indexable OR arm would
// otherwise force the planner back to a full-window filter scan for every
// request, even pure-locator ones.
func appendTargetArms(arms []string, args []any, idx int, locators, calls []string) ([]string, []any, int) {
	for _, t := range locators {
		lo, hi, _ := locatorPrefixRange(t)
		loIdx := idx
		args = append(args, lo)
		idx++
		if hi == "" {
			arms = append(arms, fmt.Sprintf("(sender_locator ~>=~ $%[1]d OR receiver_locator ~>=~ $%[1]d)", loIdx))
			continue
		}
		hiIdx := idx
		args = append(args, hi)
		idx++
		arms = append(arms, fmt.Sprintf("((sender_locator ~>=~ $%[1]d AND sender_locator ~<~ $%[2]d) OR (receiver_locator ~>=~ $%[1]d AND receiver_locator ~<~ $%[2]d))", loIdx, hiIdx))
	}
	if len(calls) > 0 {
		args = append(args, calls)
		arms = append(arms, fmt.Sprintf("(sender_callsign = ANY($%[1]d) OR receiver_callsign = ANY($%[1]d))", idx))
		idx++
	}
	return arms, args, idx
}

// activityByBinForTargets returns raw spots/min per (band, time-bin) over the
// selected `minutes` window, target-filtered the same way the live stream
// matches (callsign or locator prefix — a 6-char target like JO62QM matches
// locators extending it, mirroring strings.HasPrefix in extractMatchedBandEvent).
// This is the Postgres-backed source for the Band Stats "Reports over time"
// chart bars.
//
// Unlike recentEvents (which materializes every raw row in the window and
// chokes on a high-volume feed, leaving the in-memory fallback holding only
// ~10 min), this aggregates server-side via GROUP BY band, bin and returns
// ~12 × #bands rows, so it stays fast and bounded even when dx_raw_spots holds
// tens of millions of rows. Predicate mirrors recent24hBandSlotCountsForTokens.
// minutes<=0, no targets, or zero matched rows → nil map (caller falls back to
// in-memory binning; an empty non-nil map would defeat that fallback and
// render all-zero charts next to non-zero live report counts).
func (s *dxPostgresStore) activityByBinForTargets(targets []string, cwMinDb, minutes int, now int64) (map[string][]float64, error) {
	if s == nil || minutes <= 0 || now <= 0 {
		return nil, nil
	}
	norm := make([]string, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for _, t := range targets {
		u := strings.ToUpper(strings.TrimSpace(t))
		if u == "" {
			continue
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		norm = append(norm, u)
	}
	if len(norm) == 0 {
		return nil, nil
	}
	// Locator targets match with prefix semantics via sargable ~>=~/~<~ byte
	// ranges (see appendTargetArms): a 4-char target behaves exactly like the
	// old substring(1 for 4) equality, while longer targets (6-char qth like
	// JO62QM) match locators extending them — the old substring equality
	// could never match those, starving the chart for any operator with a
	// 6-char qth.
	locators, calls := splitTargetTokens(norm)

	const bins = 12
	windowSec := int64(minutes) * 60
	binSec := windowSec / bins
	if binSec <= 0 {
		return nil, nil
	}
	binMinutes := float64(binSec) / 60.0
	windowStart := now - windowSec

	args := []any{windowStart, binSec, now, cwMinDb}
	arms := make([]string, 0, len(locators)+1)
	arms, args, _ = appendTargetArms(arms, args, len(args)+1, locators, calls)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT band,
		       ((spot_time - $1) / $2)::int AS bin,
		       COUNT(*)::bigint
		FROM dx_raw_spots
		WHERE spot_time BETWEEN $1 AND $3
		  AND signal_report_db >= $4
		  AND (%s)
		GROUP BY band, bin
	`, strings.Join(arms, "\n\t\t    OR ")), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string][]float64, 32)
	for rows.Next() {
		var band string
		var bin int32
		var count int64
		if err := rows.Scan(&band, &bin, &count); err != nil {
			return nil, err
		}
		if bin < 0 {
			bin = 0
		}
		if bin >= bins {
			bin = bins - 1
		}
		series := out[band]
		if series == nil {
			series = make([]float64, bins)
			out[band] = series
		}
		series[bin] += float64(count) / binMinutes
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		// Zero matched rows reads as "no data" (nil), not an empty success:
		// Evaluate treats a non-nil map as authoritative and never snapshots
		// the in-memory event ring, so an empty map here would starve the
		// per-band fallback into all-zero chart bins.
		return nil, nil
	}
	return out, nil
}

func (s *dxPostgresStore) baselineStats(now int64) (int, int, int, error) {
	const ttl int64 = 60
	s.baselineStatsMu.Lock()
	if s.baselineStatsAt != 0 && now-s.baselineStatsAt < ttl {
		b, ev, hm := s.baselineStatsBkt, s.baselineStatsEv, s.baselineStatsHist
		s.baselineStatsMu.Unlock()
		return b, ev, hm, nil
	}
	s.baselineStatsMu.Unlock()

	b, ev, hm, err := s.baselineStatsUncached(now)
	if err != nil {
		return 0, 0, 0, err
	}
	s.baselineStatsMu.Lock()
	s.baselineStatsAt = now
	s.baselineStatsBkt = b
	s.baselineStatsEv = ev
	s.baselineStatsHist = hm
	s.baselineStatsMu.Unlock()
	return b, ev, hm, nil
}

func (s *dxPostgresStore) baselineStatsUncached(now int64) (int, int, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// History span comes from the tracked baseline-accumulation start in dx_meta
	// (a fast PK lookup), NOT from COUNT/MIN over the large tables. This is the
	// denominator that turns cumulative bucket counts into a spots/min rate, so
	// it must not depend on a slow COUNT(*) — which previously timed out at 2s,
	// errored, was silently swallowed, and left the span at 0 → baseline inflated.
	historyM := 0
	var firstObserved *int64
	if err := s.pool.QueryRow(ctx,
		`SELECT v::bigint FROM dx_meta WHERE k = 'baseline_first_observed_at'`,
	).Scan(&firstObserved); err != nil && err != pgx.ErrNoRows {
		return 0, 0, 0, err
	}
	if firstObserved != nil && *firstObserved > 0 && now > *firstObserved {
		historyM = int((now - *firstObserved) / 60)
	}

	// Bucket / event counts are display-only (baseline_buckets, event_count). Use
	// planner estimates (pg_class.reltuples) so an exact COUNT(*) over millions of
	// rows can't fail this call or stall the conditions endpoint.
	var bucketCount, eventCount int64
	_ = s.pool.QueryRow(ctx, `
		SELECT
			COALESCE((SELECT SUM(reltuples)::bigint FROM pg_class
				WHERE relname IN ('dx_baseline_global', 'dx_baseline_cluster')), 0),
			COALESCE((SELECT reltuples::bigint FROM pg_class
				WHERE relname = 'dx_raw_spots'), 0)
	`).Scan(&bucketCount, &eventCount)
	if bucketCount < 0 {
		bucketCount = 0
	}
	if eventCount < 0 {
		eventCount = 0
	}

	return int(bucketCount), int(eventCount), historyM, nil
}

// seedBaselineFirstObservedIfMissing records when baseline accumulation began,
// derived from the earliest persisted raw spot (the same observe() stream that
// fills the baseline buckets). The bucket tables carry no timestamp, so this is
// how we know the span to normalise their cumulative counts. Seeded once at
// startup; thereafter flushRawSpots keeps it at the minimum observed spot time.
// If there are no raw spots yet (fresh deployment), it stays unset and the rate
// degrades to "no baseline" until accumulation begins.
func (s *dxPostgresStore) seedBaselineFirstObservedIfMissing(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM dx_meta WHERE k = 'baseline_first_observed_at')`,
	).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	var minT *int64
	if err := s.pool.QueryRow(ctx, `SELECT MIN(spot_time) FROM dx_raw_spots`).Scan(&minT); err != nil {
		return err
	}
	if minT == nil {
		return nil // no raw spots yet; flushRawSpots will set it as spots arrive
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO dx_meta (k, v) VALUES ('baseline_first_observed_at', $1)
		 ON CONFLICT (k) DO NOTHING`,
		fmt.Sprintf("%d", *minT))
	return err
}

// touchBaselineFirstObserved keeps baseline_first_observed_at at the minimum
// spot time ever flushed. Cheap single-row upsert run once per (batched) flush.
func (s *dxPostgresStore) touchBaselineFirstObserved(ctx context.Context, spotTime int64) {
	if spotTime <= 0 {
		return
	}
	_, _ = s.pool.Exec(ctx,
		`INSERT INTO dx_meta (k, v) VALUES ('baseline_first_observed_at', $1)
		 ON CONFLICT (k) DO UPDATE SET v = LEAST(dx_meta.v::bigint, EXCLUDED.v::bigint)::text`,
		fmt.Sprintf("%d", spotTime))
}

func (s *dxPostgresStore) loadSpotsBetween(start, end int64) ([]MQTTMessage, error) {
	return s.loadSpotsBetweenWithSourceFilter(start, end, true)
}

func (s *dxPostgresStore) loadSpotsBetweenWithSourceFilter(start, end int64, includeDXCluster bool) ([]MQTTMessage, error) {
	if end <= 0 {
		end = time.Now().Unix()
	}
	if start > end {
		start = end
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	rows, err := s.pool.Query(ctx, `
		SELECT
			spot_time, sender_callsign, sender_locator,
			receiver_callsign, receiver_locator,
			band, mode, signal_report_db,
			COALESCE(frequency_khz, 0), COALESCE(comment, ''),
			COALESCE(source_type, 'mqtt')
		FROM dx_raw_spots
		WHERE spot_time BETWEEN $1 AND $2
		  AND ($3::bool OR LOWER(COALESCE(source_type, 'mqtt')) <> 'dxcluster')
		ORDER BY spot_time ASC
	`, start, end, includeDXCluster)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]MQTTMessage, 0, 8192)
	for rows.Next() {
		var m MQTTMessage
		// F (frequency_khz) and CM (comment) restore the fields /api/dxspots needs;
		// without them, spots restored after a restart score as freq 0 (unscorable).
		// Source (from source_type) restores the ingest tag so sourceTypeForMessage
		// tags backfilled RBN rows "rbn" (not "mqtt") and the frontend toggle works
		// across a restart.
		if err := rows.Scan(&m.T, &m.SC, &m.SL, &m.RC, &m.RL, &m.B, &m.MD, &m.RP, &m.F, &m.CM, &m.Source); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// pruneRawSpotsOlderThan deletes dx_raw_spots rows whose spot_time is older
// than `cutoff` (a Unix-seconds value). Returns the total number of rows
// deleted across all batches.
//
// Deletion runs in index-backed batches (idx_dx_raw_spots_spot_time feeds the
// LIMIT subquery, the outer DELETE hits the id PK): a fresh short context per
// batch means a slow/timeout batch returns the rows already deleted (committed)
// plus an error, and the next hourly run resumes from the then-current cutoff —
// no more wholesale 60s abort losing all progress. Smaller per-statement work
// also lets autovacuum reclaim pages and refresh pg_class.reltuples (used by
// baselineStats) between batches. pruneMaxBatches is a runaway-loop backstop.
const (
	pruneRawSpotsBatchSize    = 50_000
	pruneRawSpotsBatchTimeout = 15 * time.Second
	pruneRawSpotsMaxBatches   = 1000
)

func (s *dxPostgresStore) pruneRawSpotsOlderThan(cutoff int64) (int64, error) {
	if s == nil {
		return 0, nil
	}
	var total int64
	for b := 0; b < pruneRawSpotsMaxBatches; b++ {
		ctx, cancel := context.WithTimeout(context.Background(), pruneRawSpotsBatchTimeout)
		tag, err := s.pool.Exec(ctx, `
			DELETE FROM dx_raw_spots
			WHERE id IN (SELECT id FROM dx_raw_spots
			             WHERE spot_time < $1
			             ORDER BY spot_time
			             LIMIT $2)`, cutoff, pruneRawSpotsBatchSize)
		cancel()
		if err != nil {
			return total, err
		}
		n := tag.RowsAffected()
		total += n
		if n < pruneRawSpotsBatchSize {
			break // caught up to cutoff
		}
	}
	return total, nil
}

// pruneDayIndexedBaselineOlderThan deletes rows whose day_index is older than
// cutoffDayIndex from a day-indexed baseline table (dx_region_baseline_daily /
// wspr_region_baseline_daily). Both tables are keyed (target_grid4,band,slot,
// region,day_index) with day_index last, so a plain `WHERE day_index < cutoff`
// cannot use the PK; the dedicated day_index index (built by initSchema /
// scripts/migrate_add_region_day_index.sql) makes the ctid-LIMIT subquery
// sargable. Batched with a fresh short context per batch (same rationale as
// pruneRawSpotsOlderThan) so a slow/timeout batch still commits what it deleted.
//
// table is a hardcoded identifier, not user input, so the fmt.Sprintf is safe.
const (
	pruneDayIndexBatchSize    = 50_000
	pruneDayIndexBatchTimeout = 15 * time.Second
	pruneDayIndexMaxBatches   = 200
)

func (s *dxPostgresStore) pruneDayIndexedBaselineOlderThan(table string, cutoffDayIndex int64) (int64, error) {
	if s == nil {
		return 0, nil
	}
	var total int64
	for b := 0; b < pruneDayIndexMaxBatches; b++ {
		ctx, cancel := context.WithTimeout(context.Background(), pruneDayIndexBatchTimeout)
		tag, err := s.pool.Exec(ctx, fmt.Sprintf(`
			DELETE FROM %s
			WHERE ctid IN (SELECT ctid FROM %s
			               WHERE day_index < $1
			               LIMIT $2)`, table, table), cutoffDayIndex, pruneDayIndexBatchSize)
		cancel()
		if err != nil {
			return total, err
		}
		n := tag.RowsAffected()
		total += n
		if n < pruneDayIndexBatchSize {
			break // caught up to cutoff
		}
	}
	return total, nil
}

// pruneRegionBaselinesOlderThan prunes dx_region_baseline_daily,
// wspr_region_baseline_daily, and prop_region_baseline_daily (the day-indexed
// climatology tables). They share the same store pool; only the day_index
// cutoff matters.
func (s *dxPostgresStore) pruneRegionBaselinesOlderThan(cutoffDayIndex int64) (int64, error) {
	n1, err := s.pruneDayIndexedBaselineOlderThan("dx_region_baseline_daily", cutoffDayIndex)
	if err != nil {
		return n1, fmt.Errorf("dx_region_baseline_daily: %w", err)
	}
	n2, err := s.pruneDayIndexedBaselineOlderThan("wspr_region_baseline_daily", cutoffDayIndex)
	if err != nil {
		return n1 + n2, fmt.Errorf("wspr_region_baseline_daily: %w", err)
	}
	n3, err := s.pruneDayIndexedBaselineOlderThan("prop_region_baseline_daily", cutoffDayIndex)
	if err != nil {
		return n1 + n2 + n3, fmt.Errorf("prop_region_baseline_daily: %w", err)
	}
	return n1 + n2 + n3, nil
}

func (s *dxPostgresStore) loadRecentSpotCache(minutes int, now int64, includeDXCluster bool) ([]MQTTMessage, error) {
	if minutes <= 0 {
		minutes = 60
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	windowStart := now - int64(minutes*60)
	return s.loadSpotsBetweenWithSourceFilter(windowStart, now, includeDXCluster)
}

// recent24hBandSlotRow is one (band, slot) cell of the last-24h activity
// histogram. SlotOfDay is computed in SQL from spot_time so the result is
// stable regardless of when it's queried.
type recent24hBandSlotRow struct {
	Band      string
	SlotOfDay int
	Count     int64
}

// recent24hBandSlotCountsForTokens returns spot counts per (band, slot_of_day)
// for the 24h ending at `now`, restricted to spots whose sender/receiver
// callsign or locator prefix matches one of the supplied tokens (a 6-char
// token matches locators extending it, mirroring the live stream's HasPrefix).
// Used to populate the target+recent_24h and target+compare paths so the
// rose has data immediately after a restart (the in-memory event buffer
// takes hours to accumulate 24h of spots at typical rates).
func (s *dxPostgresStore) recent24hBandSlotCountsForTokens(tokens []string, now int64) ([]recent24hBandSlotRow, error) {
	if s == nil || len(tokens) == 0 {
		return nil, nil
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	norm := make([]string, 0, len(tokens))
	seen := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		u := strings.ToUpper(strings.TrimSpace(t))
		if u == "" {
			continue
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		norm = append(norm, u)
	}
	if len(norm) == 0 {
		return nil, nil
	}
	// Same sargable prefix-range matching as activityByBinForTargets (see
	// appendTargetArms): strict-locator tokens match locators extending them,
	// everything else stays on the callsign-equality arm.
	locators, calls := splitTargetTokens(norm)
	args := []any{now - 24*60*60, now}
	arms := make([]string, 0, len(locators)+1)
	arms, args, _ = appendTargetArms(arms, args, len(args)+1, locators, calls)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT band,
		       ((spot_time / 1800) %% 48)::int AS slot_of_day,
		       COUNT(*)::bigint
		FROM dx_raw_spots
		WHERE spot_time BETWEEN $1 AND $2
		  AND (%s)
		GROUP BY band, slot_of_day
	`, strings.Join(arms, "\n\t\t    OR ")), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]recent24hBandSlotRow, 0, 256)
	for rows.Next() {
		var r recent24hBandSlotRow
		if err := rows.Scan(&r.Band, &r.SlotOfDay, &r.Count); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// recent24hBandSlotCounts returns spot counts per (band, slot_of_day) for the
// 24 hours ending at `now`. Slot-of-day is in 30-minute UTC bins (0..47).
//
// On a large dx_raw_spots table (tens of millions of rows) this aggregates
// over the full 24h slice and takes several seconds even with the spot_time
// index, so the context timeout is generous. Callers should cache the
// result rather than reissuing the query per request.
func (s *dxPostgresStore) recent24hBandSlotCounts(now int64) ([]recent24hBandSlotRow, error) {
	if s == nil {
		return nil, nil
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := now - 24*60*60
	rows, err := s.pool.Query(ctx, `
		SELECT band,
		       ((spot_time / 1800) % 48)::int AS slot_of_day,
		       COUNT(*)::bigint
		FROM dx_raw_spots
		WHERE spot_time BETWEEN $1 AND $2
		GROUP BY band, slot_of_day
	`, start, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]recent24hBandSlotRow, 0, 256)
	for rows.Next() {
		var r recent24hBandSlotRow
		if err := rows.Scan(&r.Band, &r.SlotOfDay, &r.Count); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// typicalBucketRow is one (band, slot_of_day, distance_tier, snr_tier) cell
// of the long-term aggregate, summed across source4. Used to populate the
// dxlens snapshot's Buckets map with the persistent all-time view rather
// than just the in-memory accumulation since the last restart.
type typicalBucketRow struct {
	Band         string
	SlotOfDay    int
	DistanceTier int
	SnrTier      int
	Count        int64
}

// typicalBuckets returns the long-term bucket aggregate from dx_baseline_global.
// Source4 is collapsed since dxlens's heatmap doesn't read it. Cardinality
// bound: 13 bands × 48 slots × 5 dist tiers × 4 snr tiers ≈ 12k rows max.
func (s *dxPostgresStore) typicalBuckets() ([]typicalBucketRow, error) {
	if s == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
		FROM dx_baseline_global
		GROUP BY band, slot_of_day, distance_tier, snr_tier
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]typicalBucketRow, 0, 4096)
	for rows.Next() {
		var r typicalBucketRow
		if err := rows.Scan(&r.Band, &r.SlotOfDay, &r.DistanceTier, &r.SnrTier, &r.Count); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// typicalMultiTargetRow extends typicalBucketRow with the matching target
// token so the API layer can rekey results back into the
// "<TOKEN>|<band>|<slot>|<distTier>|<snrTier>" snapshot format.
type typicalMultiTargetRow struct {
	TargetToken string
	typicalBucketRow
}

// typicalTargetBucketsMulti returns the long-term per-target bucket aggregate
// for a SET of target tokens in a single bitmap-index scan. Dramatically
// cheaper than calling typicalTargetBuckets once per token (the rose is
// usually queried with target + 8 surrounding squares).
func (s *dxPostgresStore) typicalTargetBucketsMulti(tokens []string) ([]typicalMultiTargetRow, error) {
	if s == nil || len(tokens) == 0 {
		return nil, nil
	}
	norm := make([]string, 0, len(tokens))
	seen := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		u := strings.ToUpper(strings.TrimSpace(t))
		if u == "" {
			continue
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		norm = append(norm, u)
	}
	if len(norm) == 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL work_mem = '128MB'`); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT cluster_anchor, band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
		FROM dx_baseline_cluster
		WHERE cluster_anchor = ANY($1)
		GROUP BY cluster_anchor, band, slot_of_day, distance_tier, snr_tier
	`, norm)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]typicalMultiTargetRow, 0, 4096)
	for rows.Next() {
		var r typicalMultiTargetRow
		if err := rows.Scan(&r.TargetToken, &r.Band, &r.SlotOfDay, &r.DistanceTier, &r.SnrTier, &r.Count); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// typicalTargetBuckets returns the long-term cluster-anchor-scoped bucket
// aggregate from dx_baseline_cluster for a single cluster anchor. Used to give
// cluster-filtered rose queries the full historical picture instead of just
// the in-memory accumulation since restart.
//
// The bitmap-heap-scan on a 50 GB table takes ~minute on cold cache; the
// generous timeout matches that. Once PG's buffer cache is warm the same
// query is sub-second, and the dxlensProvider memoizes the result for the
// length of one snapshot TTL so the UI doesn't pay per request.
//
// SET LOCAL work_mem keeps the GROUP BY hash in RAM. The hash itself only
// needs a couple of MB, but on a near-full volume the default would be one
// stress point we don't need.
func (s *dxPostgresStore) typicalTargetBuckets(token string) ([]typicalBucketRow, error) {
	if s == nil || strings.TrimSpace(token) == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL work_mem = '128MB'`); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT band, slot_of_day, distance_tier, snr_tier, SUM(count)::bigint
		FROM dx_baseline_cluster
		WHERE cluster_anchor = $1
		GROUP BY band, slot_of_day, distance_tier, snr_tier
	`, strings.ToUpper(strings.TrimSpace(token)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]typicalBucketRow, 0, 1024)
	for rows.Next() {
		var r typicalBucketRow
		if err := rows.Scan(&r.Band, &r.SlotOfDay, &r.DistanceTier, &r.SnrTier, &r.Count); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// regionCalendarStatRow is one (band, region, slot) cell with daily-distribution
// statistics over a lookback window. Percentiles are computed in SQL over the
// distinct day_indexes that observed activity in the cell; days with zero
// activity are not synthesised (so percentiles describe "active days").
type regionCalendarStatRow struct {
	Band       string
	Region     string
	SlotOfDay  int
	P25        float64
	P50        float64
	P75        float64
	Mean       float64
	StdDev     float64
	Today      int64
	SampleDays int
}

// regionCalendarStats returns per (band, region, slot) statistics across the
// last `daysBack` day_indexes (ending at the day_index containing `now`).
// Aggregates across all target_grid4s (i.e. global view across observers).
// `today` is the spot count for the current UTC day at that cell.
func (s *dxPostgresStore) regionCalendarStats(ctx context.Context, daysBack int, now int64) ([]regionCalendarStatRow, error) {
	if s == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if daysBack <= 0 {
		daysBack = 30
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	today := utcDayIndex(now)
	dayStart := today - int64(daysBack-1)

	rows, err := s.pool.Query(ctx, `
		WITH daily AS (
			SELECT band, region, slot_of_day, day_index,
			       SUM(spot_count)::bigint AS c
			FROM dx_region_baseline_daily
			WHERE day_index BETWEEN $1 AND $2
			  AND region <> ''
			  AND region <> '??'
			GROUP BY band, region, slot_of_day, day_index
		)
		SELECT band, region, slot_of_day,
		       percentile_cont(0.25) WITHIN GROUP (ORDER BY c) AS p25,
		       percentile_cont(0.50) WITHIN GROUP (ORDER BY c) AS p50,
		       percentile_cont(0.75) WITHIN GROUP (ORDER BY c) AS p75,
		       AVG(c)::double precision AS mean,
		       COALESCE(stddev_samp(c), 0)::double precision AS stddev,
		       COUNT(DISTINCT day_index)::int AS sample_days,
		       COALESCE(SUM(c) FILTER (WHERE day_index = $3), 0)::bigint AS today
		FROM daily
		GROUP BY band, region, slot_of_day
	`, dayStart, today, today)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]regionCalendarStatRow, 0, 4096)
	for rows.Next() {
		var r regionCalendarStatRow
		if err := rows.Scan(
			&r.Band, &r.Region, &r.SlotOfDay,
			&r.P25, &r.P50, &r.P75,
			&r.Mean, &r.StdDev, &r.SampleDays, &r.Today,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
