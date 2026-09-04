package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"horstreporter/internal/proplab"
)

// dx_cellfeed.go is the midpoint-cell bucket feed: it accumulates the live
// spot stream into 15-minute proplab_cell_buckets rows (midpoint cell × band ×
// lane) and persists them for the pathscope module. This is the only survivor
// of the Propagation Lab backend — the Ladder/Fusion/Reach engines, dest
// buckets, event calendar, the A/B/C lab endpoints/UI and the backtest
// harness were removed (2026-08-04); the table names are unchanged because
// pathscope reads them.

var cellBucketFeed *CellBucketFeed

// proplabSWRow is one observation of a space-weather index (kp, F10.7, xray,
// ovation) — pathscope's SolarContext reads this series.
type proplabSWRow struct {
	Series  string
	ObsTime int64
	Value   float64
}

// cellfeedSchemaStmts returns the DDL for the tables this feed owns. It is
// appended to initSchema's statement list so fresh installs and existing
// deployments get the same shape automatically.
func cellfeedSchemaStmts() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS proplab_cell_buckets (
			bucket_start   BIGINT NOT NULL,
			band           TEXT   NOT NULL,
			cell4          TEXT   NOT NULL,
			region         TEXT   NOT NULL DEFAULT '',
			lane           TEXT   NOT NULL DEFAULT 'ft8',
			spot_count     INT    NOT NULL DEFAULT 0,
			link_count     INT    NOT NULL DEFAULT 0,
			reporter_count INT    NOT NULL DEFAULT 0,
			snr_median     SMALLINT NOT NULL DEFAULT 0,
			snr_p10        SMALLINT NOT NULL DEFAULT 0,
			dist_median_km INT    NOT NULL DEFAULT 0,
			dist_max_km    INT    NOT NULL DEFAULT 0,
			PRIMARY KEY (bucket_start, band, cell4, lane)
		);`,
		// No secondary indexes here: the primary key leads with bucket_start,
		// which already serves the retention prune and pathscope's
		// bucket_start-baseline lookups. The former (band, bucket_start),
		// (region, band, bucket_start) and (bucket_start) indexes were
		// redundant and dropped (0 lifetime scans on prod).

		`CREATE TABLE IF NOT EXISTS proplab_sw_series (
			series   TEXT NOT NULL,
			obs_time BIGINT NOT NULL,
			value    DOUBLE PRECISION NOT NULL,
			PRIMARY KEY (series, obs_time)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_proplab_sw_series_time
			ON proplab_sw_series (series, obs_time DESC);`,

		// Aggressive autovacuum on the hot bucket table: it accumulates
		// additive upserts (insert + update on conflict) and can grow
		// hundreds of thousands of dead tuples per day.
		`ALTER TABLE proplab_cell_buckets SET (
			autovacuum_vacuum_scale_factor = 0.05,
			autovacuum_vacuum_threshold = 10000,
			autovacuum_analyze_scale_factor = 0.02,
			autovacuum_analyze_threshold = 10000,
			autovacuum_vacuum_cost_limit = 2000
		);`,
	}
}

// upsertProplabCellBuckets persists closed cell buckets additively: counts
// accumulate on conflict (e.g. a late flush), medians are replaced by the
// newest flush, dist_max takes the greatest.
func (s *dxPostgresStore) upsertProplabCellBuckets(ctx context.Context, rows []proplab.CellRow) error {
	if s == nil || len(rows) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(`
			INSERT INTO proplab_cell_buckets
			(bucket_start, band, cell4, region, lane, spot_count, link_count, reporter_count,
			 snr_median, snr_p10, dist_median_km, dist_max_km)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT (bucket_start, band, cell4, lane)
			DO UPDATE SET
				spot_count     = proplab_cell_buckets.spot_count + EXCLUDED.spot_count,
				link_count     = proplab_cell_buckets.link_count + EXCLUDED.link_count,
				reporter_count = proplab_cell_buckets.reporter_count + EXCLUDED.reporter_count,
				snr_median     = EXCLUDED.snr_median,
				snr_p10        = EXCLUDED.snr_p10,
				dist_median_km = EXCLUDED.dist_median_km,
				dist_max_km    = GREATEST(proplab_cell_buckets.dist_max_km, EXCLUDED.dist_max_km)
		`, r.BucketStart, r.Band, r.Cell4, r.Region, r.Lane,
			r.SpotCount, r.LinkCount, r.ReporterCount, r.SnrMedian, r.SnrP10, r.DistMedianKm, r.DistMaxKm)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }()
	for range rows {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return br.Close()
}

// upsertProplabSW inserts or replaces a single space-weather observation.
func (s *dxPostgresStore) upsertProplabSW(ctx context.Context, rows []proplabSWRow) error {
	if s == nil || len(rows) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(`
			INSERT INTO proplab_sw_series (series, obs_time, value)
			VALUES ($1,$2,$3)
			ON CONFLICT (series, obs_time) DO UPDATE SET value = EXCLUDED.value
		`, r.Series, r.ObsTime, r.Value)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }()
	for range rows {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return br.Close()
}

const (
	// Small batches with a realistic timeout: the prod box's Postgres
	// cannot finish a 20k ctid delete in 2s under memory pressure, so the
	// hourly prune timed out every time and the 35d retention made no
	// progress (Sep 4). 10k/10s/100 caps a pass at ~1M rows, worst case
	// ~15 min, still bounded.
	cellfeedPruneBatchSize    = 10000
	cellfeedPruneBatchTimeout = 10 * time.Second
	cellfeedPruneMaxBatches   = 100
)

// pruneCellFeedOlderThan removes cell bucket / SW rows older than cutoff.
func (s *dxPostgresStore) pruneCellFeedOlderThan(cutoff int64) (int64, error) {
	if s == nil {
		return 0, nil
	}
	var total int64
	var lastErr error
	for _, table := range []string{"proplab_cell_buckets", "proplab_sw_series"} {
		col := "bucket_start"
		if table == "proplab_sw_series" {
			col = "obs_time"
		}
		for b := 0; b < cellfeedPruneMaxBatches; b++ {
			ctx, cancel := context.WithTimeout(context.Background(), cellfeedPruneBatchTimeout)
			res, err := s.pool.Exec(ctx, fmt.Sprintf(`
				DELETE FROM %s
				WHERE ctid IN (SELECT ctid FROM %s WHERE %s < $1 LIMIT %d)
			`, table, table, col, cellfeedPruneBatchSize), cutoff)
			cancel()
			if err != nil {
				lastErr = err
				break
			}
			n := res.RowsAffected()
			total += n
			if n < cellfeedPruneBatchSize {
				break
			}
		}
	}
	return total, lastErr
}

// CellBucketFeed accumulates the live stream into cell buckets and persists
// them on a 60-second tick. It replaces the removed ProplabService entirely.
type CellBucketFeed struct {
	mu            sync.RWMutex
	engine        *proplab.CellBucketEngine
	store         *dxPostgresStore
	retentionDays int

	dedup             map[string]int64
	lastPrune         time.Time
	started           bool
	recomputeInterval time.Duration
}

// newCellBucketFeed builds the feed; store comes from the baseline engine.
func newCellBucketFeed(baseline *DxBaselineEngine, retentionDays int) *CellBucketFeed {
	s := &CellBucketFeed{
		engine:            proplab.NewCellBucketEngine(),
		retentionDays:     retentionDays,
		dedup:             make(map[string]int64),
		recomputeInterval: 60 * time.Second,
	}
	if baseline != nil {
		s.store = baseline.store
	}
	return s
}

// Start launches the persistence tick.
func (s *CellBucketFeed) Start() {
	if s == nil || s.started {
		return
	}
	s.started = true
	go s.loop()
}

func (s *CellBucketFeed) loop() {
	t := time.NewTicker(s.recomputeInterval)
	defer t.Stop()
	for range t.C {
		s.tick()
	}
}

func (s *CellBucketFeed) tick() {
	now := time.Now().Unix()
	cutoff := proplab.AlignBucketStart(now - int64(proplab.BucketSeconds))
	rows := s.engine.CloseBuckets(cutoff)
	if len(rows) > 0 && s.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := s.store.upsertProplabCellBuckets(ctx, rows)
		cancel()
		if err != nil {
			logInfo("Cell bucket persistence failed (rows=%d): %v", len(rows), err)
		}
	}
	s.cleanupDedup()

	if s.retentionDays > 0 && time.Since(s.lastPrune) > 1*time.Hour {
		pruneCutoff := now - int64(s.retentionDays*24*60*60)
		if s.store != nil {
			n, err := s.store.pruneCellFeedOlderThan(pruneCutoff)
			if err != nil {
				logInfo("Cell feed retention prune failed: %v", err)
			} else if n > 0 {
				logInfo("Cell feed retention pruned %d rows older than %d days", n, s.retentionDays)
			}
		}
		s.lastPrune = time.Now()
	}
}

// Observe ingests one spot, deduplicated against the two ingest paths that
// both deliver spots (DX-cluster observe + raw persistence).
func (s *CellBucketFeed) Observe(m MQTTMessage) {
	if s == nil {
		return
	}

	band := normalizeBand(m.B)
	if band == "" || !bandInScope(band) {
		return
	}
	if !isLocator(m.SL) || !isLocator(m.RL) || len(m.SL) < 4 || len(m.RL) < 4 {
		return
	}

	spot := toProplabSpot(m)
	lane := proplab.LaneForSourceType(proplab.SourceTypeForMessage(spot))
	sc := strings.ToUpper(strings.TrimSpace(m.SC))
	rc := strings.ToUpper(strings.TrimSpace(m.RC))
	bucketStart := proplab.AlignBucketStart(m.T)

	key := fmt.Sprintf("%s|%s|%s|%s|%d", band, lane, sc, rc, bucketStart)
	now := time.Now().Unix()

	s.mu.Lock()
	if last, ok := s.dedup[key]; ok && now-last < 5 {
		s.mu.Unlock()
		return
	}
	s.dedup[key] = now
	s.mu.Unlock()

	s.engine.Observe(spot)
}

// Backfill feeds a batch of recovered spots at startup (not deduplicated —
// callers pass spots not already ingested live).
func (s *CellBucketFeed) Backfill(spots []MQTTMessage) {
	if s == nil {
		return
	}
	for _, m := range spots {
		s.engine.Observe(toProplabSpot(m))
	}
}

func (s *CellBucketFeed) cleanupDedup() {
	now := time.Now().Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, last := range s.dedup {
		if now-last > 30 {
			delete(s.dedup, k)
		}
	}
}

// toProplabSpot converts an ingest message into the engine's Spot contract.
func toProplabSpot(m MQTTMessage) proplab.Spot {
	return proplab.Spot{
		T:      m.T,
		B:      m.B,
		SC:     m.SC,
		SL:     m.SL,
		RC:     m.RC,
		RL:     m.RL,
		RP:     m.RP,
		Source: strings.ToLower(strings.TrimSpace(sourceTypeForMessage(m))),
	}
}
