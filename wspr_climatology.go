package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"

	"horstreporter/internal/region"

	"github.com/jackc/pgx/v5"
)

// wsprClimatologyBucket is one accumulated count keyed by (band, slot, region).
// The in-memory fallback uses this shape; the Postgres path uses
// wspr_region_baseline_daily (same key, per-day-index breakdown).
type wsprClimatologyBucket struct {
	Band       string `json:"band"`
	SlotOfDay  int    `json:"slot_of_day"`
	Region     string `json:"region"`
	Count      int64  `json:"count"`
	SampleDays int    `json:"sample_days"`
}

// wsprRegionCalendarStatRow mirrors regionCalendarStatRow but for the WSPR
// climatology. Mean and StdDev are per-(band, region, slot) across the
// lookback window; Today is the current UTC day's count; SampleDays is the
// number of distinct day_indexes with activity.
type wsprRegionCalendarStatRow struct {
	Band       string
	Region     string
	SlotOfDay  int
	Mean       float64
	StdDev     float64
	Today      int64
	SampleDays int
}

// wsprClimatologySnapshot is the JSONL save/load shape for the in-memory
// fallback (no Postgres). Buckets are aggregated (no per-day-index breakdown),
// so SampleDays is approximate (derived from firstEventAt/lastEventAt span).
type wsprClimatologySnapshot struct {
	Version      int                        `json:"version"`
	SavedAt      int64                      `json:"saved_at"`
	FirstEventAt int64                      `json:"first_event_at"`
	LastEventAt  int64                      `json:"last_event_at"`
	Buckets      map[string]*wsprClimatologyBucket `json:"buckets"`
}

// WsprClimatologyEngine accumulates a WSPR climatology keyed by
// (band × 11 DXPulse region × slot-of-day). It mirrors the DxBaselineEngine
// pattern: in-memory buckets + optional Postgres store + JSONL fallback.
type WsprClimatologyEngine struct {
	mu          sync.RWMutex
	path        string
	store       *dxPostgresStore // shared with DxBaselineEngine; nil in dev
	buckets     map[string]*wsprClimatologyBucket
	firstEventAt int64
	lastEventAt  int64

	// pendingWsprRegion is the Postgres flush queue, mirroring pendingRegion.
	pendingWsprRegion map[wsprRegionBaselineKey]int64
	pendingWsprCount   int

	// statsCache mirrors the prop_intel region-baseline cache pattern.
	statsCache     []wsprRegionCalendarStatRow
	statsCacheAt   int64
	statsCacheTTL  int64
}

// wsprRegionBaselineKey is the Postgres flush key for wspr_region_baseline_daily.
type wsprRegionBaselineKey struct {
	Band      string
	SlotOfDay int
	Region    string
	DayIndex  int64
}

const (
	wsprClimatologyVersion     = 1
	wsprClimatologyStatsCacheTTL = 120 // seconds
	wsprClimatologyDefaultDaysBack = 30
)

func newWsprClimatologyEngine(path string) *WsprClimatologyEngine {
	return &WsprClimatologyEngine{
		path:              strings.TrimSpace(path),
		buckets:           make(map[string]*wsprClimatologyBucket),
		pendingWsprRegion: make(map[wsprRegionBaselineKey]int64, 2048),
		statsCacheTTL:     wsprClimatologyStatsCacheTTL,
	}
}

// SetStore wires the shared Postgres store (same pool as DxBaselineEngine).
func (e *WsprClimatologyEngine) SetStore(st *dxPostgresStore) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.store = st
	e.mu.Unlock()
}

// wsprClimatologyKey formats the in-memory bucket key: "band|slot|region".
func wsprClimatologyKey(band string, slot int, region string) string {
	return band + "|" + itoa(slot) + "|" + region
}

// Observe records one WSPR spot into the climatology. The remote end's
// DXPulse region is the region axis (one key per spot, not two — the WSPR
// climatology is a global-mesh reference, not from-here).
func (e *WsprClimatologyEngine) Observe(m MQTTMessage) {
	if e == nil {
		return
	}
	band := normalizeBand(m.B)
	if band == "" {
		return
	}
	sl := strings.ToUpper(strings.TrimSpace(m.SL))
	rl := strings.ToUpper(strings.TrimSpace(m.RL))
	if sl == "" || rl == "" {
		return
	}
	ts := m.T
	if ts <= 0 {
		ts = time.Now().Unix()
	}
	slot := utcSlotOfDay(ts)

	// Resolve the remote end's region. For the global-mesh climatology, we
	// key by the receiver locator's region (SL) — the station that heard the
	// beacon. This mirrors how prop_intel keys cells (by the remote end's
	// region) and gives "which region is the path landing in".
	reg := region.FromLocator(sl)
	if reg == region.Unknown {
		// Fall back to transmitter region if receiver region is unknown.
		reg = region.FromLocator(rl)
		if reg == region.Unknown {
			return
		}
	}

	key := wsprClimatologyKey(band, slot, string(reg))

	e.mu.Lock()
	if ts > 0 {
		if e.firstEventAt == 0 || ts < e.firstEventAt {
			e.firstEventAt = ts
		}
		if ts > e.lastEventAt {
			e.lastEventAt = ts
		}
	}

	b := e.buckets[key]
	if b == nil {
		b = &wsprClimatologyBucket{Band: band, SlotOfDay: slot, Region: string(reg)}
		e.buckets[key] = b
	}
	b.Count++

	// Postgres flush queue.
	st := e.store
	if st != nil {
		dayIndex := utcDayIndex(ts)
		pk := wsprRegionBaselineKey{Band: band, SlotOfDay: slot, Region: string(reg), DayIndex: dayIndex}
		e.pendingWsprRegion[pk]++
		e.pendingWsprCount++
	}
	e.mu.Unlock()
}

// Load reads the JSONL fallback snapshot from disk. No-op if path is empty or
// file does not exist.
func (e *WsprClimatologyEngine) Load() error {
	if e == nil || e.path == "" {
		return nil
	}
	raw, err := os.ReadFile(e.path)
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
	defer e.mu.Unlock()
	e.buckets = make(map[string]*wsprClimatologyBucket, len(snap.Buckets))
	for k, v := range snap.Buckets {
		if v == nil {
			continue
		}
		cp := *v
		e.buckets[k] = &cp
	}
	e.firstEventAt = snap.FirstEventAt
	e.lastEventAt = snap.LastEventAt
	return nil
}

// Save writes the in-memory buckets to the JSONL fallback file atomically.
func (e *WsprClimatologyEngine) Save() error {
	if e == nil || e.path == "" {
		return nil
	}
	e.mu.RLock()
	snap := wsprClimatologySnapshot{
		Version:      wsprClimatologyVersion,
		SavedAt:      time.Now().Unix(),
		FirstEventAt: e.firstEventAt,
		LastEventAt:  e.lastEventAt,
		Buckets:      make(map[string]*wsprClimatologyBucket, len(e.buckets)),
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
func (e *WsprClimatologyEngine) Stats(now int64) (bucketCount int, historyMinutes int) {
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

// FlushPendingWsprRegion batches the pending WSPR region counts into the
// Postgres wspr_region_baseline_daily table. Mirrors flushPending's region arm.
func (s *dxPostgresStore) FlushPendingWsprRegion(ctx context.Context, pending map[wsprRegionBaselineKey]int64) error {
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
			INSERT INTO wspr_region_baseline_daily (band, slot_of_day, region, day_index, spot_count)
			VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (band, slot_of_day, region, day_index)
			DO UPDATE SET spot_count = wspr_region_baseline_daily.spot_count + EXCLUDED.spot_count
		`, k.Band, k.SlotOfDay, k.Region, k.DayIndex, v)
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

// FlushPending drains the WSPR region pending queue to Postgres. Called by
// the store's flush loop. Returns the number of keys flushed.
func (e *WsprClimatologyEngine) FlushPending(ctx context.Context) (int, error) {
	if e == nil {
		return 0, nil
	}
	e.mu.Lock()
	st := e.store
	pending := e.pendingWsprRegion
	count := e.pendingWsprCount
	e.pendingWsprRegion = make(map[wsprRegionBaselineKey]int64, 2048)
	e.pendingWsprCount = 0
	e.mu.Unlock()

	if st == nil || len(pending) == 0 {
		return 0, nil
	}
	err := st.FlushPendingWsprRegion(ctx, pending)
	if err != nil {
		// Re-enqueue on failure (mirrors mergePendingBack).
		e.mu.Lock()
		for k, v := range pending {
			e.pendingWsprRegion[k] += v
		}
		e.pendingWsprCount += count
		e.mu.Unlock()
		return 0, err
	}
	return len(pending), nil
}

// ensureWsprRegionBaseline creates the wspr_region_baseline_daily table if it
// does not exist. Called once on startup when Postgres is configured.
func (s *dxPostgresStore) ensureWsprRegionBaseline(ctx context.Context) error {
	if s == nil {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS wspr_region_baseline_daily (
			band TEXT NOT NULL,
			slot_of_day INTEGER NOT NULL,
			region TEXT NOT NULL,
			day_index BIGINT NOT NULL,
			spot_count BIGINT NOT NULL,
			PRIMARY KEY (band, slot_of_day, region, day_index)
		);
	`)
	return err
}

// WsprRegionCalendarStats returns per (band, region, slot) statistics across
// the last daysBack day_indexes from the WSPR climatology. Mirrors
// regionCalendarStats but queries wspr_region_baseline_daily.
func (s *dxPostgresStore) WsprRegionCalendarStats(ctx context.Context, daysBack int, now int64) ([]wsprRegionCalendarStatRow, error) {
	if s == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if daysBack <= 0 {
		daysBack = wsprClimatologyDefaultDaysBack
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
			FROM wspr_region_baseline_daily
			WHERE day_index BETWEEN $1 AND $2
			  AND region <> ''
			  AND region <> '??'
			GROUP BY band, region, slot_of_day, day_index
		)
		SELECT band, region, slot_of_day,
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
	out := make([]wsprRegionCalendarStatRow, 0, 4096)
	for rows.Next() {
		var r wsprRegionCalendarStatRow
		if err := rows.Scan(&r.Band, &r.Region, &r.SlotOfDay, &r.Mean, &r.StdDev, &r.SampleDays, &r.Today); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RegionCalendarStats returns the WSPR climatology stats for the current slot,
// cached for statsCacheTTL seconds. When Postgres is nil, computes from the
// in-memory buckets (aggregated count / span → approximate mean; stddev is 0
// since the in-memory fallback does not track per-day breakdown).
func (e *WsprClimatologyEngine) RegionCalendarStats(ctx context.Context, daysBack int, now int64) []wsprRegionCalendarStatRow {
	if e == nil {
		return nil
	}
	if daysBack <= 0 {
		daysBack = wsprClimatologyDefaultDaysBack
	}
	if now <= 0 {
		now = time.Now().Unix()
	}

	// Cache check.
	e.mu.RLock()
	cacheAge := now - e.statsCacheAt
	cached := e.statsCache
	ttl := e.statsCacheTTL
	e.mu.RUnlock()
	if ttl > 0 && cacheAge >= 0 && cacheAge < ttl && cached != nil {
		return cached
	}

	e.mu.RLock()
	st := e.store
	e.mu.RUnlock()

	var rows []wsprRegionCalendarStatRow
	if st != nil {
		var err error
		rows, err = st.WsprRegionCalendarStats(ctx, daysBack, now)
		if err != nil {
			logDebug("WSPR climatology stats query failed: %v", err)
			rows = e.regionCalendarStatsFromMemory(now)
		}
	} else {
		rows = e.regionCalendarStatsFromMemory(now)
	}

	e.mu.Lock()
	e.statsCache = rows
	e.statsCacheAt = now
	e.mu.Unlock()
	return rows
}

// regionCalendarStatsFromMemory computes approximate climatology stats from
// the in-memory buckets. Without per-day-index tracking, Mean is the
// aggregated count divided by the span in days, StdDev is 0 (can't compute
// sample stddev from a single aggregated count), and SampleDays is the span.
func (e *WsprClimatologyEngine) regionCalendarStatsFromMemory(now int64) []wsprRegionCalendarStatRow {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if len(e.buckets) == 0 {
		return nil
	}
	spanDays := int64(1)
	if e.firstEventAt > 0 && e.lastEventAt > e.firstEventAt {
		spanDays = (e.lastEventAt - e.firstEventAt) / 86400
		if spanDays < 1 {
			spanDays = 1
		}
	}
	out := make([]wsprRegionCalendarStatRow, 0, len(e.buckets))
	for _, b := range e.buckets {
		if b == nil || b.Count == 0 {
			continue
		}
		mean := float64(b.Count) / float64(spanDays)
		out = append(out, wsprRegionCalendarStatRow{
			Band:       b.Band,
			Region:     b.Region,
			SlotOfDay:  b.SlotOfDay,
			Mean:       mean,
			StdDev:     0,
			Today:      0,
			SampleDays: int(spanDays),
		})
	}
	return out
}

// FlushPendingAsync is the goroutine loop that periodically flushes the
// WSPR region pending queue to Postgres. Mirrors the DX baseline flush loop.
func (e *WsprClimatologyEngine) FlushPendingAsync(interval time.Duration, stop <-chan struct{}) {
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
				logDebug("WSPR climatology flush failed: %v", err)
			} else if n > 0 {
				logDebug("WSPR climatology flushed %d region keys", n)
			}
		}
	}
}

// SaveLoop periodically saves the in-memory buckets to the JSONL fallback.
func (e *WsprClimatologyEngine) SaveLoop(interval time.Duration, stop <-chan struct{}) {
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
				logDebug("WSPR climatology JSONL save failed: %v", err)
			}
		}
	}
}