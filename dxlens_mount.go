package main

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"dxlens"
)

// dxlensRegionStatsLookbackDays is the rolling window used to compute typical
// region-calendar percentiles. Picked to match the operator-facing "typical"
// expectation (a month of recent conditions) and stay cheap in SQL.
const dxlensRegionStatsLookbackDays = 30

// dxlensTargetCacheTTL is how long per-target PG lookups stay cached. Much
// longer than the global snapshot TTL because per-target queries are slow
// (cold cache: ~minute) and the underlying data — long-term baseline — only
// shifts perceptibly over days. A 5-minute window lets users click around
// the same target without re-paying the cold load.
const dxlensTargetCacheTTL = 5 * time.Minute

// dxlensTargetCacheEntry holds one cached per-target bucket map plus its
// expiry time so each token ages independently.
type dxlensTargetCacheEntry struct {
	buckets   map[string]*dxlens.Bucket
	expiresAt time.Time
}

// dxlensProvider adapts the HorstReporter in-memory DxBaselineEngine to the
// dxlens.SnapshotProvider interface. It builds an *dxlens.Snapshot on demand
// and caches it for a short window to amortize the per-call clone cost.
type dxlensProvider struct {
	engine *DxBaselineEngine
	ttl    time.Duration

	mu       sync.Mutex
	snap     *dxlens.Snapshot
	madeAt   time.Time
	loadedAt time.Time

	// targetCache memoizes per-target PG lookups. Each entry has its own
	// expiry (dxlensTargetCacheTTL from build time) so tokens age out
	// independently — and independently of the snapshot rebuild cadence.
	targetCache map[string]dxlensTargetCacheEntry
}

func newDxlensProvider(engine *DxBaselineEngine, ttl time.Duration) *dxlensProvider {
	if ttl <= 0 {
		ttl = 5 * time.Second
	}
	return &dxlensProvider{engine: engine, ttl: ttl}
}

// Recent24hForTokens implements dxlens api.Recent24hForTokensLookup. Reads
// the last 24h of spots from dx_raw_spots filtered by token match — used
// by the rose's target+recent_24h and target+compare paths.
func (p *dxlensProvider) Recent24hForTokens(tokens []string, now int64) []*dxlens.Recent24hCell {
	if p == nil || p.engine == nil || len(tokens) == 0 {
		return nil
	}
	p.mu.Lock()
	store := p.engine.store
	p.mu.Unlock()
	if store == nil {
		return nil
	}
	rows, err := store.recent24hBandSlotCountsForTokens(tokens, now)
	if err != nil {
		logInfo("DXLens recent-24h-for-tokens query failed (tokens=%v): %v", tokens, err)
		return nil
	}
	if len(rows) == 0 {
		return nil
	}
	out := make([]*dxlens.Recent24hCell, 0, len(rows))
	for _, r := range rows {
		band := normalizeBand(r.Band)
		if band == "" {
			continue
		}
		out = append(out, &dxlens.Recent24hCell{
			Band:      band,
			SlotOfDay: r.SlotOfDay,
			Count:     r.Count,
		})
	}
	return out
}

// TargetBucketsMulti returns the long-term per-target bucket aggregate for a
// set of target tokens in a single PG query. Implements dxlens
// api.TargetBucketsMultiLookup; preferred over per-token TargetBuckets when
// the caller has multiple tokens (target + surroundings), because a single
// bitmap scan over the target_token-leading PK index is many times cheaper
// than N independent scans, especially on cold cache.
//
// Returns a flat map keyed "<TOKEN>|<band>|<slot>|<distTier>|<snrTier>" —
// the same shape as snap.TargetBuckets. Per-token results are also stored
// in the per-token cache so subsequent single-token queries don't re-fetch.
func (p *dxlensProvider) TargetBucketsMulti(tokens []string) map[string]*dxlens.Bucket {
	if p == nil || p.engine == nil || len(tokens) == 0 {
		return nil
	}
	// Normalise each token through the same path observe()/initSchema use,
	// so 4-char locators collapse to their 2×2 block anchor (e.g. JO32→JO22)
	// and PG's storage shape (block-anchored tokens only) actually matches
	// the queried token set. Without this, "JO32 + surroundings" would query
	// nine 4-char locators of which only two happen to be block anchors.
	norm := make([]string, 0, len(tokens))
	seen := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		u := normalizeQTHTokenUpper(strings.ToUpper(strings.TrimSpace(t)))
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
		return nil
	}

	// Check the per-token cache: collect cached results and the missing tokens.
	now := time.Now()
	p.mu.Lock()
	store := p.engine.store
	if p.targetCache == nil {
		p.targetCache = make(map[string]dxlensTargetCacheEntry, len(norm))
	}
	result := make(map[string]*dxlens.Bucket, 4096)
	missing := norm[:0:0]
	for _, t := range norm {
		if entry, ok := p.targetCache[t]; ok && now.Before(entry.expiresAt) {
			for k, v := range entry.buckets {
				result[k] = v
			}
			continue
		}
		missing = append(missing, t)
	}
	p.mu.Unlock()

	if len(missing) == 0 || store == nil {
		if len(result) == 0 {
			return nil
		}
		return result
	}

	rows, err := store.typicalTargetBucketsMulti(missing)
	if err != nil {
		logInfo("DXLens target-buckets-multi query failed (tokens=%v): %v", missing, err)
		// Fall through with whatever the cache had so the UI isn't blank.
		if len(result) == 0 {
			return nil
		}
		return result
	}

	// Re-bucket per-token so we can populate the per-token cache too.
	perToken := make(map[string]map[string]*dxlens.Bucket, len(missing))
	for _, t := range missing {
		perToken[t] = make(map[string]*dxlens.Bucket, 64)
	}
	for _, r := range rows {
		band := normalizeBand(r.Band)
		if band == "" {
			continue
		}
		key := baselineQthKeyFromBase(r.TargetToken, baselineKey(band, r.SlotOfDay, r.DistanceTier, r.SnrTier))
		b := &dxlens.Bucket{
			Band:         band,
			SlotOfDay:    r.SlotOfDay,
			DistanceTier: r.DistanceTier,
			SnrTier:      r.SnrTier,
			Count:        r.Count,
		}
		result[key] = b
		if m, ok := perToken[r.TargetToken]; ok {
			m[key] = b
		}
	}

	expiresAt := time.Now().Add(dxlensTargetCacheTTL)
	p.mu.Lock()
	for t, m := range perToken {
		p.targetCache[t] = dxlensTargetCacheEntry{buckets: m, expiresAt: expiresAt}
	}
	p.mu.Unlock()
	return result
}

// TargetBuckets returns the long-term per-target bucket aggregate for a single
// target token. Implements dxlens api.TargetBucketsLookup so the heatmap
// handler can show the full historical pattern when a target is set, rather
// than just the in-memory accumulation since the last restart.
func (p *dxlensProvider) TargetBuckets(token string) map[string]*dxlens.Bucket {
	if p == nil || p.engine == nil {
		return nil
	}
	token = normalizeQTHTokenUpper(strings.ToUpper(strings.TrimSpace(token)))
	if token == "" {
		return nil
	}

	now := time.Now()
	p.mu.Lock()
	store := p.engine.store
	if p.targetCache == nil {
		p.targetCache = make(map[string]dxlensTargetCacheEntry, 4)
	}
	if entry, ok := p.targetCache[token]; ok && now.Before(entry.expiresAt) {
		p.mu.Unlock()
		return entry.buckets
	}
	p.mu.Unlock()

	if store == nil {
		return nil
	}
	rows, err := store.typicalTargetBuckets(token)
	if err != nil {
		logInfo("DXLens target-buckets query failed (token=%s): %v", token, err)
		return nil
	}
	out := make(map[string]*dxlens.Bucket, len(rows))
	for _, r := range rows {
		band := normalizeBand(r.Band)
		if band == "" {
			continue
		}
		key := baselineQthKeyFromBase(token, baselineKey(band, r.SlotOfDay, r.DistanceTier, r.SnrTier))
		out[key] = &dxlens.Bucket{
			Band:         band,
			SlotOfDay:    r.SlotOfDay,
			DistanceTier: r.DistanceTier,
			SnrTier:      r.SnrTier,
			Count:        r.Count,
		}
	}
	p.mu.Lock()
	p.targetCache[token] = dxlensTargetCacheEntry{buckets: out, expiresAt: time.Now().Add(dxlensTargetCacheTTL)}
	p.mu.Unlock()
	return out
}

// Snapshot returns the latest cached snapshot, rebuilding it if the TTL expired.
func (p *dxlensProvider) Snapshot() *dxlens.Snapshot {
	if p == nil || p.engine == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.snap != nil && time.Since(p.madeAt) < p.ttl {
		return p.snap
	}
	p.snap = buildDxlensSnapshot(p.engine)
	p.madeAt = time.Now()
	p.loadedAt = p.madeAt
	return p.snap
}

// LoadedAt returns when the cached snapshot was built.
func (p *dxlensProvider) LoadedAt() time.Time {
	if p == nil {
		return time.Time{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.loadedAt
}

// buildDxlensSnapshot converts the engine's internal buckets/events into the
// JSON-compatible *dxlens.Snapshot shape. Holds the engine read lock briefly.
func buildDxlensSnapshot(e *DxBaselineEngine) *dxlens.Snapshot {
	e.mu.RLock()
	store := e.store
	buckets := make(map[string]*dxlens.Bucket, len(e.buckets))
	for k, v := range e.buckets {
		if v == nil {
			continue
		}
		buckets[k] = &dxlens.Bucket{
			Band:         v.Band,
			SlotOfDay:    v.SlotOfDay,
			DistanceTier: v.DistanceTier,
			SnrTier:      v.SnrTier,
			Count:        v.Count,
		}
	}
	target := make(map[string]*dxlens.Bucket, len(e.qthBuckets))
	for k, v := range e.qthBuckets {
		if v == nil {
			continue
		}
		target[k] = &dxlens.Bucket{
			Band:         v.Band,
			SlotOfDay:    v.SlotOfDay,
			DistanceTier: v.DistanceTier,
			SnrTier:      v.SnrTier,
			Count:        v.Count,
		}
	}
	events := e.snapshotEventsLocked()
	conv := make([]dxlens.Event, len(events))
	for i, ev := range events {
		conv[i] = dxlens.Event{
			T: ev.T, B: ev.B,
			SC: ev.SC, RC: ev.RC,
			SL: ev.SL, RL: ev.RL,
			RP: ev.RP,
		}
	}
	firstAt := e.firstEventAt
	lastAt := e.lastEventAt
	e.mu.RUnlock()

	snap := &dxlens.Snapshot{
		Version:       4,
		SavedAt:       time.Now().Unix(),
		FirstEventAt:  firstAt,
		LastEventAt:   lastAt,
		Buckets:       buckets,
		TargetBuckets: target,
		Events:        conv,
	}

	// Historical extensions are only meaningful when a persistent store is
	// available — the in-memory engine alone covers maybe one day of events
	// (and nothing at all when dx_baseline.json is absent at startup).
	if store != nil {
		now := time.Now().Unix()
		populateTypicalBuckets(snap, store)
		populateRecent24h(snap, store, now)
		populateRegionStats(snap, store, now)
	}
	return snap
}

// populateTypicalBuckets replaces the in-memory live-accumulation Buckets map
// with the persistent long-term aggregate from dx_baseline_global. Without
// this, a freshly-restarted backend (or one without dx_baseline.json) shows
// only the slot it's currently in on the "Typical" rose.
func populateTypicalBuckets(snap *dxlens.Snapshot, store *dxPostgresStore) {
	rows, err := store.typicalBuckets()
	if err != nil {
		logInfo("DXLens typical-buckets query failed: %v", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	out := make(map[string]*dxlens.Bucket, len(rows))
	for _, r := range rows {
		band := normalizeBand(r.Band)
		if band == "" {
			continue
		}
		key := baselineKey(band, r.SlotOfDay, r.DistanceTier, r.SnrTier)
		out[key] = &dxlens.Bucket{
			Band:         band,
			SlotOfDay:    r.SlotOfDay,
			DistanceTier: r.DistanceTier,
			SnrTier:      r.SnrTier,
			Count:        r.Count,
		}
	}
	snap.Buckets = out
}

// populateRecent24h fills snap.Recent24hBuckets from the last 24h of raw
// spots. Logs at INFO on failure (so a misconfigured DB or slow query
// shows up without flipping global log level) but leaves the map nil so the
// panel hides gracefully rather than breaking the rest of the snapshot.
func populateRecent24h(snap *dxlens.Snapshot, store *dxPostgresStore, now int64) {
	rows, err := store.recent24hBandSlotCounts(now)
	if err != nil {
		logInfo("DXLens recent-24h query failed: %v", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	out := make(map[string]*dxlens.Recent24hCell, len(rows))
	for _, r := range rows {
		band := normalizeBand(r.Band)
		if band == "" {
			continue
		}
		key := band + "|" + strconv.Itoa(r.SlotOfDay)
		out[key] = &dxlens.Recent24hCell{Band: band, SlotOfDay: r.SlotOfDay, Count: r.Count}
	}
	snap.Recent24hBuckets = out
}

// populateRegionStats fills snap.RegionCalendarStats from
// dx_region_baseline_daily over the configured lookback window.
func populateRegionStats(snap *dxlens.Snapshot, store *dxPostgresStore, now int64) {
	rows, err := store.regionCalendarStats(dxlensRegionStatsLookbackDays, now)
	if err != nil {
		logInfo("DXLens region stats query failed: %v", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	out := make(map[string]*dxlens.RegionCalendarStat, len(rows))
	for _, r := range rows {
		band := normalizeBand(r.Band)
		if band == "" || r.Region == "" {
			continue
		}
		key := band + "|" + r.Region + "|" + strconv.Itoa(r.SlotOfDay)
		out[key] = &dxlens.RegionCalendarStat{
			Band:       band,
			Region:     r.Region,
			SlotOfDay:  r.SlotOfDay,
			P25:        r.P25,
			P50:        r.P50,
			P75:        r.P75,
			Mean:       r.Mean,
			StdDev:     r.StdDev,
			Today:      r.Today,
			SampleDays: r.SampleDays,
		}
	}
	snap.RegionCalendarStats = out
}
