package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// dx_proplab_store.go contains the Postgres persistence for the Propagation
// Lab. It is intentionally separate from the engine logic so the Ladder/Fusion
// engines can be unit-tested against an in-memory fake of this interface.
//
// Tables created by this file:
//   - proplab_cell_buckets    : midpoint-cell × band × 15-min buckets
//   - proplab_sw_series      : space-weather index time series
//   - proplab_drap_snapshots : D-RAP Highest-Affected-Frequency grids
//   - proplab_events         : contest/DXpedition/POTA active windows

// proplabCellRow is a closed 15-minute bucket ready for upsert.
type proplabCellRow struct {
	BucketStart   int64
	Band          string
	Cell4         string
	Region        string
	Lane          string // "ft8", "rbn", "dcx"
	SpotCount     int
	LinkCount     int
	ReporterCount int
	SnrMedian     int
	SnrP10        int
	DistMedianKm  int
	DistMaxKm     int
}

// proplabSWRow is one observation of a space-weather index.
type proplabSWRow struct {
	Series  string
	ObsTime int64
	Value   float64
}

// proplabDRAPRow is a snapshot of the global D-RAP HAF grid. The grid is stored
// as a compact byte array: 36 latitude rows × 24 longitude columns, HAF in
// deci-MHz (0..255, where 255 means "no HAF / above 25.5 MHz or no data").
const proplabDRAPGridSize = 36 * 24

type proplabDRAPRow struct {
	ObsTime int64
	Grid    []byte // length == proplabDRAPGridSize
}

// proplabEventRow is a scheduled or live operating event that can explain
// demand-side spot spikes (contests, DXpeditions, POTA activations).
type proplabEventRow struct {
	Source   string // "wa7bnm", "ng3k", "pota"
	EventID  string
	Title    string
	BandMask string // e.g. "80m-10m CW/SSB" or empty
	StartUTC int64
	EndUTC   int64
	Locator4 string // 4-char square when known (DXpedition/POTA site)
}

// proplabSchemaStmts returns the DDL for the proplab tables and indexes. It is
// appended to initSchema's statement list so fresh installs and existing
// deployments get the same shape automatically.
func proplabSchemaStmts() []string {
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
		`CREATE INDEX IF NOT EXISTS idx_proplab_cell_band_bucket
			ON proplab_cell_buckets (band, bucket_start);`,
		`CREATE INDEX IF NOT EXISTS idx_proplab_cell_region_band_bucket
			ON proplab_cell_buckets (region, band, bucket_start);`,
		`CREATE INDEX IF NOT EXISTS idx_proplab_cell_bucket_time
			ON proplab_cell_buckets (bucket_start);`,

		`CREATE TABLE IF NOT EXISTS proplab_sw_series (
			series   TEXT NOT NULL,
			obs_time BIGINT NOT NULL,
			value    DOUBLE PRECISION NOT NULL,
			PRIMARY KEY (series, obs_time)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_proplab_sw_series_time
			ON proplab_sw_series (series, obs_time DESC);`,

		`CREATE TABLE IF NOT EXISTS proplab_drap_snapshots (
			obs_time BIGINT PRIMARY KEY,
			haf_grid BYTEA NOT NULL
		);`,

		`CREATE TABLE IF NOT EXISTS proplab_events (
			source    TEXT NOT NULL,
			event_id  TEXT NOT NULL,
			title     TEXT NOT NULL,
			band_mask TEXT NOT NULL DEFAULT '',
			start_utc BIGINT NOT NULL,
			end_utc   BIGINT NOT NULL,
			locator4  TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (source, event_id)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_proplab_events_window
			ON proplab_events (end_utc, start_utc);`,
	}
}

func (s *dxPostgresStore) upsertProplabCellBuckets(ctx context.Context, rows []proplabCellRow) error {
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

// proplabCellQueryResult is one row returned by the cell-bucket queries.
type proplabCellQueryResult struct {
	BucketStart   int64
	Band          string
	Cell4         string
	Region        string
	Lane          string
	SpotCount     int
	LinkCount     int
	ReporterCount int
	SnrMedian     int
	SnrP10        int
	DistMedianKm  int
	DistMaxKm     int
}

func scanProplabCellRow(rows pgx.Rows) (proplabCellQueryResult, error) {
	var r proplabCellQueryResult
	err := rows.Scan(&r.BucketStart, &r.Band, &r.Cell4, &r.Region, &r.Lane,
		&r.SpotCount, &r.LinkCount, &r.ReporterCount,
		&r.SnrMedian, &r.SnrP10, &r.DistMedianKm, &r.DistMaxKm)
	return r, err
}

// queryProplabCellBuckets returns cell buckets within a time window, optionally
// filtered by band and/or lane. Cells are returned newest-first within a bucket.
func (s *dxPostgresStore) queryProplabCellBuckets(ctx context.Context, start, end int64, band, lane string, limit int) ([]proplabCellQueryResult, error) {
	if s == nil {
		return nil, nil
	}
	args := []any{start, end}
	conds := []string{"bucket_start >= $1", "bucket_start <= $2"}
	if band != "" {
		args = append(args, band)
		conds = append(conds, fmt.Sprintf("band = $%d", len(args)))
	}
	if lane != "" {
		args = append(args, lane)
		conds = append(conds, fmt.Sprintf("lane = $%d", len(args)))
	}
	q := fmt.Sprintf(`
		SELECT bucket_start, band, cell4, region, lane, spot_count, link_count, reporter_count,
		       snr_median, snr_p10, dist_median_km, dist_max_km
		FROM proplab_cell_buckets
		WHERE %s
		ORDER BY bucket_start DESC, link_count DESC
		LIMIT %d
	`, strings.Join(conds, " AND "), limit)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []proplabCellQueryResult
	for rows.Next() {
		r, err := scanProplabCellRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// loadProplabCellBaseline returns daily counts per (band, region, slot_of_day)
// over the requested lookback window. This is the raw material for Fusion's
// conditional-quantile baseline with guardband. The region column makes the
// aggregation a direct GROUP BY.
func (s *dxPostgresStore) loadProplabCellBaseline(ctx context.Context, bands []string, regions []string, slot int, lookbackDays int, now int64) ([]proplabBaselineDayRow, error) {
	if s == nil || len(bands) == 0 {
		return nil, nil
	}
	if len(regions) == 0 {
		regions = nil
	}
	if lookbackDays <= 0 {
		lookbackDays = 45
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	start := now - int64(lookbackDays*24*60*60)
	rows, err := s.pool.Query(ctx, `
		SELECT band, region, slot_of_day,
		       (bucket_start / 86400) AS day_index,
		       SUM(link_count)::bigint AS link_count,
		       SUM(spot_count)::bigint AS spot_count,
		       MAX(dist_max_km)::int AS dist_max_km
		FROM proplab_cell_buckets
		WHERE bucket_start >= $1
		  AND band = ANY($2)
		  AND ($3::text[] IS NULL OR region = ANY($3))
		  AND slot_of_day = $4
		GROUP BY band, region, slot_of_day, (bucket_start / 86400)
	`, start, bands, regions, slot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []proplabBaselineDayRow
	for rows.Next() {
		var r proplabBaselineDayRow
		if err := rows.Scan(&r.Band, &r.Region, &r.Slot, &r.DayIndex, &r.LinkCount, &r.SpotCount, &r.DistMaxKm); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type proplabBaselineDayRow struct {
	Band      string
	Region    string
	Slot      int
	DayIndex  int64
	LinkCount int64
	SpotCount int64
	DistMaxKm int
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

// latestProplabSW returns the most recent value for each known series.
func (s *dxPostgresStore) latestProplabSW(ctx context.Context) (map[string]proplabSWRow, error) {
	if s == nil {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (series) series, obs_time, value
		FROM proplab_sw_series
		ORDER BY series, obs_time DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]proplabSWRow)
	for rows.Next() {
		var r proplabSWRow
		if err := rows.Scan(&r.Series, &r.ObsTime, &r.Value); err != nil {
			return nil, err
		}
		out[r.Series] = r
	}
	return out, rows.Err()
}

// upsertProplabDRAP stores a D-RAP HAF grid snapshot.
func (s *dxPostgresStore) upsertProplabDRAP(ctx context.Context, r proplabDRAPRow) error {
	if s == nil || len(r.Grid) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO proplab_drap_snapshots (obs_time, haf_grid)
		VALUES ($1,$2)
		ON CONFLICT (obs_time) DO UPDATE SET haf_grid = EXCLUDED.haf_grid
	`, r.ObsTime, r.Grid)
	return err
}

// latestProplabDRAP returns the most recent D-RAP grid, or nil if none.
func (s *dxPostgresStore) latestProplabDRAP(ctx context.Context) (*proplabDRAPRow, error) {
	if s == nil {
		return nil, nil
	}
	var r proplabDRAPRow
	var grid []byte
	err := s.pool.QueryRow(ctx, `
		SELECT obs_time, haf_grid
		FROM proplab_drap_snapshots
		ORDER BY obs_time DESC
		LIMIT 1
	`).Scan(&r.ObsTime, &grid)
	if err != nil {
		return nil, err
	}
	r.Grid = grid
	return &r, nil
}

// upsertProplabEvents merges event rows. Existing rows are replaced so title or
// window updates from upstream calendars take effect.
func (s *dxPostgresStore) upsertProplabEvents(ctx context.Context, rows []proplabEventRow) error {
	if s == nil || len(rows) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(`
			INSERT INTO proplab_events (source, event_id, title, band_mask, start_utc, end_utc, locator4)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (source, event_id) DO UPDATE SET
				title     = EXCLUDED.title,
				band_mask = EXCLUDED.band_mask,
				start_utc = EXCLUDED.start_utc,
				end_utc   = EXCLUDED.end_utc,
				locator4  = EXCLUDED.locator4
		`, r.Source, r.EventID, r.Title, r.BandMask, r.StartUTC, r.EndUTC, r.Locator4)
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

// activeProplabEvents returns events overlapping the window ending at now.
func (s *dxPostgresStore) activeProplabEvents(ctx context.Context, now int64, windowSeconds int) ([]proplabEventRow, error) {
	if s == nil {
		return nil, nil
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	start := now - int64(windowSeconds)
	rows, err := s.pool.Query(ctx, `
		SELECT source, event_id, title, band_mask, start_utc, end_utc, locator4
		FROM proplab_events
		WHERE end_utc >= $1 AND start_utc <= $2
		ORDER BY end_utc DESC
	`, start, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []proplabEventRow
	for rows.Next() {
		var r proplabEventRow
		if err := rows.Scan(&r.Source, &r.EventID, &r.Title, &r.BandMask, &r.StartUTC, &r.EndUTC, &r.Locator4); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// pruneProplabOlderThan removes proplab data older than cutoff. Each table is
// pruned separately; partial success returns the last error but total counts
// are still reported.
func (s *dxPostgresStore) pruneProplabOlderThan(cutoff int64) (int64, error) {
	if s == nil {
		return 0, nil
	}
	tables := []string{
		"proplab_cell_buckets",
		"proplab_sw_series",
		"proplab_drap_snapshots",
		"proplab_events",
	}
	var total int64
	var lastErr error
	for _, table := range tables {
		var col string
		switch table {
		case "proplab_cell_buckets", "proplab_sw_series", "proplab_drap_snapshots":
			col = "bucket_start"
			if table == "proplab_sw_series" {
				col = "obs_time"
			}
			if table == "proplab_drap_snapshots" {
				col = "obs_time"
			}
		case "proplab_events":
			col = "end_utc"
		}
		n, err := s.pruneProplabTable(table, col, cutoff)
		if err != nil {
			lastErr = err
		}
		total += n
	}
	return total, lastErr
}

func (s *dxPostgresStore) pruneProplabTable(table, timeCol string, cutoff int64) (int64, error) {
	var total int64
	for b := 0; b < pruneRawSpotsMaxBatches; b++ {
		ctx, cancel := context.WithTimeout(context.Background(), pruneRawSpotsBatchTimeout)
		tag, err := s.pool.Exec(ctx, fmt.Sprintf(`
			DELETE FROM %s
			WHERE %s < $1
			  AND ctid IN (SELECT ctid FROM %s WHERE %s < $1 ORDER BY %s LIMIT $2)
		`, table, timeCol, table, timeCol, timeCol), cutoff, pruneRawSpotsBatchSize)
		cancel()
		if err != nil {
			return total, err
		}
		n := tag.RowsAffected()
		total += n
		if n < pruneRawSpotsBatchSize {
			break
		}
	}
	return total, nil
}

// ensureProplabCellBuckets rebuilds the last 48 hours of cell buckets from
// dx_raw_spots when the table is empty but raw spots exist. This keeps a
// freshly-upgraded backend from being blind for the first 15 minutes. It is
// guarded by a dx_meta marker so it only runs once per deployment.
//
// To avoid OOM on busy deployments, rows are processed in time-ordered batches
// (proplabBackfillBatchSeconds) and each batch is flushed to Postgres before
// the next batch is read.
func (s *dxPostgresStore) ensureProplabCellBuckets(ctx context.Context) error {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM proplab_cell_buckets LIMIT 1)`).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	var rawExists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM dx_raw_spots LIMIT 1)`).Scan(&rawExists); err != nil {
		return err
	}
	if !rawExists {
		return nil
	}
	logInfo("Proplab cell-bucket backfill starting from existing raw spots")
	now := time.Now().Unix()
	cutoff := now - 48*60*60

	processed := int64(0)
	bucketsFlushed := 0
	for batchStart := cutoff; batchStart < now; batchStart += proplabBackfillBatchSeconds {
		batchEnd := batchStart + proplabBackfillBatchSeconds
		if batchEnd > now {
			batchEnd = now
		}
		batchRows, err := s.loadRawSpotsBatch(ctx, batchStart, batchEnd)
		if err != nil {
			return err
		}
		if len(batchRows) == 0 {
			continue
		}
		accum := newProplabInMemoryAccumulator()
		for _, m := range batchRows {
			accum.observe(m)
			processed++
		}
		cellRows := accum.closeBuckets(0)
		if len(cellRows) > 0 {
			if err := s.upsertProplabCellBuckets(ctx, cellRows); err != nil {
				return err
			}
			bucketsFlushed += len(cellRows)
		}
		logInfo("Proplab backfill batch %d-%d: %d spots, %d buckets flushed", batchStart, batchEnd, len(batchRows), len(cellRows))
	}

	if _, err := s.pool.Exec(ctx, `
		INSERT INTO dx_meta (k,v) VALUES ('proplab_cell_buckets_built_at',$1)
		ON CONFLICT (k) DO UPDATE SET v = EXCLUDED.v
	`, fmt.Sprintf("%d", time.Now().Unix())); err != nil {
		return err
	}
	logInfo("Proplab cell-bucket backfill finished (%d raw spots processed, %d buckets flushed)", processed, bucketsFlushed)
	return nil
}

func (s *dxPostgresStore) loadRawSpotsBatch(ctx context.Context, start, end int64) ([]proplabBackfillSpot, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT spot_time, band, sender_locator, receiver_locator, sender_callsign, receiver_callsign,
		       signal_report_db, source_type
		FROM dx_raw_spots
		WHERE spot_time >= $1 AND spot_time < $2
		ORDER BY spot_time ASC
	`, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []proplabBackfillSpot
	for rows.Next() {
		var m proplabBackfillSpot
		if err := rows.Scan(&m.SpotTime, &m.Band, &m.SenderLoc, &m.ReceiverLoc,
			&m.SenderCall, &m.ReceiverCall, &m.SNR, &m.SourceType); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

const proplabBackfillBatchSeconds = 2 * 60 * 60 // 2-hour batches

type proplabBackfillSpot struct {
	SpotTime     int64
	Band         string
	SenderLoc    string
	ReceiverLoc  string
	SenderCall   string
	ReceiverCall string
	SNR          int
	SourceType   string
}

// proplabBucketAccumulator is a simple in-memory aggregator used by the backfill
// path. The full Ladder engine in proplab_ladder.go uses a richer variant with
// CUSUM/onset state; this type is intentionally minimal and kept here so the
// store backfill does not depend on engine internals.
type proplabBucketAccumulator struct {
	mu      sync.Mutex
	buckets map[proplabBucketKey]*proplabCellBucket
}

type proplabBucketKey struct {
	BucketStart int64
	Band        string
	Cell4       string
	Lane        string
}

type proplabCellBucket struct {
	links     map[string]struct{}
	reporters map[string]struct{}
	snrs      []int
	spotCount int
	sumDistKm float64
	maxDistKm float64
	region    string
}

func newProplabInMemoryAccumulator() *proplabBucketAccumulator {
	return &proplabBucketAccumulator{
		buckets: make(map[proplabBucketKey]*proplabCellBucket),
	}
}

const proplabBucketSeconds = 15 * 60

func (a *proplabBucketAccumulator) observe(m proplabBackfillSpot) {
	band := normalizeBand(m.Band)
	if band == "" || !bandInScope(band) {
		return
	}
	cell, ok := midpointCell(m.SenderLoc, m.ReceiverLoc)
	if !ok {
		return
	}
	lane := proplabLaneForSourceType(m.SourceType)
	bucketStart := (m.SpotTime / proplabBucketSeconds) * proplabBucketSeconds

	lat1, lon1 := locatorToLatLng(m.SenderLoc)
	lat2, lon2 := locatorToLatLng(m.ReceiverLoc)
	dist := haversineKm(lat1, lon1, lat2, lon2)

	key := proplabBucketKey{BucketStart: bucketStart, Band: band, Cell4: cell, Lane: lane}

	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.buckets[key]
	if b == nil {
		b = &proplabCellBucket{
			links:     make(map[string]struct{}),
			reporters: make(map[string]struct{}),
			region:    string(dxPulseRegionForLocator(cell)),
		}
		a.buckets[key] = b
	}
	b.spotCount++
	b.links[m.SenderCall+"|"+m.ReceiverCall] = struct{}{}
	b.reporters[m.ReceiverCall] = struct{}{}
	b.reporters[m.SenderCall] = struct{}{}
	b.sumDistKm += dist
	if dist > b.maxDistKm {
		b.maxDistKm = dist
	}
	if lane != "dcx" {
		b.snrs = append(b.snrs, m.SNR)
	}
}

func intMedian(in []int) int {
	sort.Ints(in)
	n := len(in)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return in[n/2]
	}
	return (in[n/2-1] + in[n/2]) / 2
}

func proplabLaneForSourceType(t string) string {
	switch t {
	case "rbn":
		return "rbn"
	case "dxcluster":
		return "dcx"
	default:
		return "ft8"
	}
}

func (a *proplabBucketAccumulator) closeBuckets(_ int64) []proplabCellRow {
	a.mu.Lock()
	defer a.mu.Unlock()
	rows := make([]proplabCellRow, 0, len(a.buckets))
	for k, b := range a.buckets {
		if b.spotCount == 0 {
			continue
		}
		r := proplabCellRow{
			BucketStart:   k.BucketStart,
			Band:          k.Band,
			Cell4:         k.Cell4,
			Region:        b.region,
			Lane:          k.Lane,
			SpotCount:     b.spotCount,
			LinkCount:     len(b.links),
			ReporterCount: len(b.reporters),
			DistMaxKm:     int(b.maxDistKm),
		}
		if len(b.snrs) > 0 {
			sort.Ints(b.snrs)
			r.SnrMedian = intMedian(b.snrs)
			r.SnrP10 = int(percentileInt(b.snrs, 0.10))
		}
		if b.spotCount > 0 {
			r.DistMedianKm = int(b.sumDistKm / float64(b.spotCount))
		}
		rows = append(rows, r)
	}
	return rows
}
