package main

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"horstreporter/internal/region"

	"github.com/jackc/pgx/v5"
)

// prop_baseline.go is the unified per-source band × region climatology engine
// backing /api/prop_intel/v2. It generalizes WsprClimatologyEngine to all four
// ingest sources: buckets are keyed "band|source|slot|region", and the region
// axis is the receiver/reporter side per the source profile's ReceiverSide
// (pskr reports land on RC/RL; wspr/rbn/dxcluster on SC/SL).
//
// IMPORTANT: WsprClimatologyEngine (wspr_climatology.go) remains live and
// untouched — /api/prop_intel v1 (horstapp widgets) reads it. The two engines
// write different tables (wspr_region_baseline_daily vs
// prop_region_baseline_daily); do NOT merge the Observe paths or WSPR counts
// would double.

// propBaselineBucket is one accumulated count keyed by (band, source, slot, region).
type propBaselineBucket struct {
	Band       string          `json:"band"`
	Source     string          `json:"source"` // public profile name
	SlotOfDay  int             `json:"slot_of_day"`
	Region     string          `json:"region"`
	Count      int64           `json:"count"`
	SampleDays int             `json:"sample_days"`
	DayCounts  map[int64]int64 `json:"day_counts,omitempty"`
}

// propRegionCalendarStatRow is one (band, source, region, slot) stats row.
type propRegionCalendarStatRow struct {
	Band       string
	Source     string
	Region     string
	SlotOfDay  int
	Mean       float64
	StdDev     float64
	Today      int64
	SampleDays int
}

// propBaselineSnapshot is the JSONL save/load shape for the in-memory
// fallback (no Postgres). Version 2 adds the Source field on buckets.
type propBaselineSnapshot struct {
	Version      int                            `json:"version"`
	SavedAt      int64                          `json:"saved_at"`
	FirstEventAt int64                          `json:"first_event_at"`
	LastEventAt  int64                          `json:"last_event_at"`
	Buckets      map[string]*propBaselineBucket `json:"buckets"`
}

// propBaselineKey is the Postgres flush key for prop_region_baseline_daily.
type propBaselineKey struct {
	Band      string
	Source    string
	SlotOfDay int
	Region    string
	DayIndex  int64
}

// propBaselineEngine accumulates a climatology keyed by
// (band × source × 11-region × slot-of-day). Mirrors WsprClimatologyEngine:
// in-memory buckets + optional Postgres store + JSONL fallback.
type propBaselineEngine struct {
	mu           sync.RWMutex
	path         string
	legacyPath   string // v1 wspr_climatology.json import source
	store        *dxPostgresStore
	buckets      map[string]*propBaselineBucket
	firstEventAt int64
	lastEventAt  int64

	pending      map[propBaselineKey]int64
	pendingCount int

	statsCache      []propRegionCalendarStatRow
	statsCacheAt    int64
	statsCacheTTL   int64
	statsCacheErrAt int64
}

const (
	propBaselineVersion         = 2
	propBaselineStatsCacheTTL   = 120 // seconds
	propBaselineDefaultDaysBack = 30
	// propBaselineMaxPending caps the pending flush queue during a Postgres
	// outage (oldest day indexes dropped; data loss preferred over OOM).
	propBaselineMaxPending = 100000
)

func newPropBaselineEngine(path, legacyPath string) *propBaselineEngine {
	return &propBaselineEngine{
		path:          strings.TrimSpace(path),
		legacyPath:    strings.TrimSpace(legacyPath),
		buckets:       make(map[string]*propBaselineBucket),
		pending:       make(map[propBaselineKey]int64, 4096),
		statsCacheTTL: propBaselineStatsCacheTTL,
	}
}

// SetStore wires the shared Postgres store (same pool as DxBaselineEngine).
func (e *propBaselineEngine) SetStore(st *dxPostgresStore) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.store = st
	e.mu.Unlock()
}

// propBaselineBucketKey formats the in-memory key: "band|source|slot|region".
func propBaselineBucketKey(band, source string, slot int, reg string) string {
	return band + "|" + source + "|" + itoa(slot) + "|" + reg
}

// Observe records one spot of any known source into the climatology. Called
// from the hub broadcast funnel so every ingest contributes. The region axis
// is the receiver/reporter end's region (per the source profile), with the
// other end as fallback — global-mesh reference, not from-here.
func (e *propBaselineEngine) Observe(m MQTTMessage) {
	if e == nil {
		return
	}
	src := m.Source
	if src == "" {
		src = "mqtt" // MQTT ingest leaves Source empty (only wspr/rbn/dxc tag theirs)
	}
	prof, ok := propIntelProfilesByTag[src]
	if !ok {
		return
	}
	band := normalizeBand(m.B)
	if band == "" {
		return
	}
	recvLoc, _, otherLoc, _ := prof.receiverEnds(m)
	if recvLoc == "" || otherLoc == "" {
		return
	}
	ts := m.T
	if ts <= 0 {
		ts = time.Now().Unix()
	}
	slot := utcSlotOfDay(ts)

	reg := regionFromLocatorCached(recvLoc)
	if reg == region.Unknown {
		reg = regionFromLocatorCached(otherLoc)
		if reg == region.Unknown {
			return
		}
	}

	key := propBaselineBucketKey(band, prof.PublicName, slot, string(reg))

	e.mu.Lock()
	if e.firstEventAt == 0 || ts < e.firstEventAt {
		e.firstEventAt = ts
	}
	if ts > e.lastEventAt {
		e.lastEventAt = ts
	}

	b := e.buckets[key]
	if b == nil {
		b = &propBaselineBucket{
			Band: band, Source: prof.PublicName, SlotOfDay: slot,
			Region: string(reg), DayCounts: make(map[int64]int64),
		}
		e.buckets[key] = b
	}
	b.Count++
	dayIndex := utcDayIndex(ts)
	b.DayCounts[dayIndex]++

	if e.store != nil {
		e.pending[propBaselineKey{
			Band: band, Source: prof.PublicName,
			SlotOfDay: slot, Region: string(reg), DayIndex: dayIndex,
		}]++
		e.pendingCount++
	}
	e.mu.Unlock()
}

// Load reads the JSONL fallback snapshot. Version-2 files load directly;
// a legacy version-1 WSPR snapshot (wspr_climatology.json) is imported from
// legacyPath by tagging every bucket with Source "wspr". No-op if paths are
// empty or files don't exist.
func (e *propBaselineEngine) Load() error {
	if e == nil {
		return nil
	}
	loaded := false
	if e.path != "" {
		raw, err := os.ReadFile(e.path)
		if err == nil {
			var snap propBaselineSnapshot
			if err := json.Unmarshal(raw, &snap); err != nil {
				return err
			}
			e.mu.Lock()
			e.buckets = make(map[string]*propBaselineBucket, len(snap.Buckets))
			for k, v := range snap.Buckets {
				if v == nil {
					continue
				}
				cp := *v
				if cp.Source == "" {
					cp.Source = "wspr" // pre-versioning safety net
				}
				e.buckets[k] = &cp
			}
			e.firstEventAt = snap.FirstEventAt
			e.lastEventAt = snap.LastEventAt
			e.mu.Unlock()
			loaded = true
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if !loaded && e.legacyPath != "" {
		raw, err := os.ReadFile(e.legacyPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		var snap wsprClimatologySnapshot
		if err := json.Unmarshal(raw, &snap); err != nil {
			return err
		}
		e.mu.Lock()
		e.buckets = make(map[string]*propBaselineBucket, len(snap.Buckets))
		for k, v := range snap.Buckets {
			if v == nil {
				continue
			}
			e.buckets[propBaselineBucketKey(v.Band, "wspr", v.SlotOfDay, v.Region)] = &propBaselineBucket{
				Band: v.Band, Source: "wspr", SlotOfDay: v.SlotOfDay,
				Region: v.Region, Count: v.Count, SampleDays: v.SampleDays,
				DayCounts: v.DayCounts,
			}
			_ = k
		}
		e.firstEventAt = snap.FirstEventAt
		e.lastEventAt = snap.LastEventAt
		e.mu.Unlock()
	}
	return nil
}

// Save writes the in-memory buckets to the JSONL fallback file atomically.
func (e *propBaselineEngine) Save() error {
	if e == nil || e.path == "" {
		return nil
	}
	e.mu.RLock()
	snap := propBaselineSnapshot{
		Version:      propBaselineVersion,
		SavedAt:      time.Now().Unix(),
		FirstEventAt: e.firstEventAt,
		LastEventAt:  e.lastEventAt,
		Buckets:      make(map[string]*propBaselineBucket, len(e.buckets)),
	}
	for k, v := range e.buckets {
		if v == nil {
			continue
		}
		cp := *v
		snap.Buckets[k] = &cp
	}
	e.mu.RUnlock()
	return writeJSONAtomic(e.path, snap)
}

// Stats returns bucket count and history span for /api/stats surfacing.
func (e *propBaselineEngine) Stats(now int64) (bucketCount int, historyMinutes int) {
	if e == nil {
		return 0, 0
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	bucketCount = len(e.buckets)
	if e.firstEventAt > 0 && e.lastEventAt > 0 {
		historyMinutes = int((e.lastEventAt - e.firstEventAt) / 60)
	}
	return bucketCount, historyMinutes
}

// FlushPendingPropBaseline batches the pending counts into the Postgres
// prop_region_baseline_daily table.
func (s *dxPostgresStore) FlushPendingPropBaseline(ctx context.Context, pending map[propBaselineKey]int64) error {
	if s == nil || len(pending) == 0 {
		return nil
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	batch := &pgx.Batch{}
	queued := 0
	for k, v := range pending {
		batch.Queue(`
			INSERT INTO prop_region_baseline_daily (band, source, slot_of_day, region, day_index, spot_count)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (band, source, slot_of_day, region, day_index)
			DO UPDATE SET spot_count = prop_region_baseline_daily.spot_count + EXCLUDED.spot_count
		`, k.Band, k.Source, k.SlotOfDay, k.Region, k.DayIndex, v)
		queued++
	}
	if queued > 0 {
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
	}
	return tx.Commit(ctx)
}

// FlushPending drains the pending queue to Postgres. Returns flushed keys.
func (e *propBaselineEngine) FlushPending(ctx context.Context) (int, error) {
	if e == nil {
		return 0, nil
	}
	e.mu.Lock()
	st := e.store
	pending := e.pending
	count := e.pendingCount
	e.pending = make(map[propBaselineKey]int64, 4096)
	e.pendingCount = 0
	e.mu.Unlock()

	if st == nil || len(pending) == 0 {
		return 0, nil
	}
	err := st.FlushPendingPropBaseline(ctx, pending)
	if err != nil {
		// Re-enqueue on failure, capped to prevent OOM during a long outage.
		e.mu.Lock()
		for k, v := range pending {
			e.pending[k] += v
		}
		e.pendingCount += count
		if e.pendingCount > propBaselineMaxPending {
			e.pending, e.pendingCount = capPendingPropBaseline(e.pending, propBaselineMaxPending)
		}
		e.mu.Unlock()
		return 0, err
	}
	return len(pending), nil
}

// ensurePropRegionBaseline creates the prop_region_baseline_daily table and
// seeds it once from the existing WSPR climatology table (idempotent).
// The day_index-leading index mirrors idx_wspr_region_baseline_daily_day_index —
// both the stats reader (WHERE day_index BETWEEN …) and the prune
// (WHERE day_index < cutoff) need it; the PK's suffix position is unusable.
func (s *dxPostgresStore) ensurePropRegionBaseline(ctx context.Context) error {
	if s == nil {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS prop_region_baseline_daily (
			band TEXT NOT NULL,
			source TEXT NOT NULL,
			slot_of_day INTEGER NOT NULL,
			region TEXT NOT NULL,
			day_index BIGINT NOT NULL,
			spot_count BIGINT NOT NULL,
			PRIMARY KEY (band, source, slot_of_day, region, day_index)
		);
		CREATE INDEX IF NOT EXISTS idx_prop_region_baseline_daily_day_index
			ON prop_region_baseline_daily (day_index);
		INSERT INTO prop_region_baseline_daily (band, source, slot_of_day, region, day_index, spot_count)
		SELECT band, 'wspr', slot_of_day, region, day_index, spot_count
		FROM wspr_region_baseline_daily
		ON CONFLICT DO NOTHING;
	`)
	return err
}

// PropRegionCalendarStats returns per (band, source, region, slot) statistics
// across the last daysBack day_indexes. sources filters by public profile
// name; nil/empty means all sources.
func (s *dxPostgresStore) PropRegionCalendarStats(ctx context.Context, sources []string, daysBack int, now int64) ([]propRegionCalendarStatRow, error) {
	if s == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if daysBack <= 0 {
		daysBack = propBaselineDefaultDaysBack
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	today := utcDayIndex(now)
	dayStart := today - int64(daysBack-1)

	rows, err := s.pool.Query(ctx, `
		WITH daily AS (
			SELECT band, source, region, slot_of_day, day_index,
			       SUM(spot_count)::bigint AS c
			FROM prop_region_baseline_daily
			WHERE day_index BETWEEN $1 AND $2
			  AND region <> ''
			  AND region <> '??'
			  AND ($4::text[] IS NULL OR source = ANY($4))
			GROUP BY band, source, region, slot_of_day, day_index
		)
		SELECT band, source, region, slot_of_day,
		       AVG(c)::double precision AS mean,
		       COALESCE(stddev_samp(c), 0)::double precision AS stddev,
		       COUNT(DISTINCT day_index)::int AS sample_days,
		       COALESCE(SUM(c) FILTER (WHERE day_index = $3), 0)::bigint AS today
		FROM daily
		GROUP BY band, source, region, slot_of_day
	`, dayStart, today, today, sources)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]propRegionCalendarStatRow, 0, 8192)
	for rows.Next() {
		var r propRegionCalendarStatRow
		if err := rows.Scan(&r.Band, &r.Source, &r.Region, &r.SlotOfDay, &r.Mean, &r.StdDev, &r.SampleDays, &r.Today); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RegionCalendarStats returns the unified climatology stats, cached for
// statsCacheTTL seconds. Falls back to in-memory buckets when Postgres is
// absent or errors. Single-flight + negative cache, mirroring the WSPR engine.
func (e *propBaselineEngine) RegionCalendarStats(ctx context.Context, daysBack int, now int64) []propRegionCalendarStatRow {
	if e == nil {
		return nil
	}
	if daysBack <= 0 {
		daysBack = propBaselineDefaultDaysBack
	}
	if now <= 0 {
		now = time.Now().Unix()
	}

	e.mu.RLock()
	cacheAge := now - e.statsCacheAt
	cached := e.statsCache
	ttl := e.statsCacheTTL
	errAge := now - e.statsCacheErrAt
	e.mu.RUnlock()
	if ttl > 0 && cacheAge >= 0 && cacheAge < ttl && cached != nil {
		return cached
	}
	if cached == nil && e.statsCacheErrAt != 0 && errAge >= 0 && errAge < propIntelRegionBaselineNegCacheTTL {
		return nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if ttl > 0 && (now-e.statsCacheAt) < ttl && e.statsCache != nil {
		return e.statsCache
	}
	if e.statsCache == nil && e.statsCacheErrAt != 0 && (now-e.statsCacheErrAt) < propIntelRegionBaselineNegCacheTTL {
		return nil
	}

	var rows []propRegionCalendarStatRow
	if st := e.store; st != nil {
		var err error
		rows, err = st.PropRegionCalendarStats(ctx, nil, daysBack, now)
		if err != nil {
			logDebug("prop baseline stats query failed: %v", err)
			e.statsCacheErrAt = now
			if e.statsCache != nil {
				return e.statsCache
			}
			rows = e.statsFromMemoryLocked(now)
		}
	} else {
		rows = e.statsFromMemoryLocked(now)
	}

	e.statsCache = rows
	e.statsCacheAt = now
	e.statsCacheErrAt = 0
	return rows
}

// statsFromMemoryLocked computes climatology stats from in-memory buckets.
// Caller must hold e.mu.
func (e *propBaselineEngine) statsFromMemoryLocked(now int64) []propRegionCalendarStatRow {
	if len(e.buckets) == 0 {
		return nil
	}
	today := utcDayIndex(now)
	out := make([]propRegionCalendarStatRow, 0, len(e.buckets))
	for _, b := range e.buckets {
		if b == nil || len(b.DayCounts) == 0 {
			continue
		}
		counts := make([]float64, 0, len(b.DayCounts))
		var todayCount int64
		for dayIdx, c := range b.DayCounts {
			counts = append(counts, float64(c))
			if dayIdx == today {
				todayCount = c
			}
		}
		meanVal := mean(counts)
		var stddev float64
		if len(counts) >= 2 {
			var sumSqDiff float64
			for _, c := range counts {
				d := c - meanVal
				sumSqDiff += d * d
			}
			stddev = math.Sqrt(sumSqDiff / float64(len(counts)-1))
		}
		out = append(out, propRegionCalendarStatRow{
			Band: b.Band, Source: b.Source, Region: b.Region, SlotOfDay: b.SlotOfDay,
			Mean: meanVal, StdDev: stddev, Today: todayCount, SampleDays: len(counts),
		})
	}
	return out
}

// capPendingPropBaseline drops the oldest day-index entries until the pending
// map fits within maxKeys (data loss preferred over OOM).
func capPendingPropBaseline(pending map[propBaselineKey]int64, maxKeys int) (map[propBaselineKey]int64, int) {
	if len(pending) <= maxKeys {
		return pending, len(pending)
	}
	var minDay, maxDay int64
	for k := range pending {
		if minDay == 0 || k.DayIndex < minDay {
			minDay = k.DayIndex
		}
		if k.DayIndex > maxDay {
			maxDay = k.DayIndex
		}
	}
	cutoff := minDay + (maxDay-minDay)/2
	out := make(map[propBaselineKey]int64, maxKeys)
	for k, v := range pending {
		if k.DayIndex < cutoff {
			continue
		}
		out[k] = v
	}
	return out, len(out)
}

// FlushPendingAsync periodically flushes the pending queue to Postgres.
func (e *propBaselineEngine) FlushPendingAsync(interval time.Duration, stop <-chan struct{}) {
	if e == nil || interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			n, err := e.FlushPending(ctx)
			cancel()
			if err != nil {
				logDebug("prop baseline flush failed: %v", err)
			} else if n > 0 {
				logDebug("prop baseline flushed %d region keys", n)
			}
		}
	}
}

// SaveLoop periodically saves the in-memory buckets to the JSONL fallback.
func (e *propBaselineEngine) SaveLoop(interval time.Duration, stop <-chan struct{}) {
	if e == nil || e.path == "" || interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if err := e.Save(); err != nil {
				logDebug("prop baseline JSONL save failed: %v", err)
			}
		}
	}
}
