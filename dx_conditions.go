package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"unsafe"

	"horstreporter/internal/cty"
	"horstreporter/internal/region"
)

const (
	defaultDxWindowMinutes       = 20
	defaultDxCwViableMinDb       = -15
	maxDxWindowMinutes           = 180
	defaultDxBaselineMaxEvents   = 1000000
	dxSparklineBins              = 12 // # of time bins in the per-band activity series / Sparkline
	dxLongHaulThresholdKm        = 3000.0
	dxDedupWindowSeconds         = 30
	dxMinBaselineQuantileSupport = 200
)

var dxBaselineMaxEvents = defaultDxBaselineMaxEvents

// historyScratchPool backs snapshotHubHistoryWindow: pooled scratch buffers
// for the per-request hub.history window copy. dx_conditions, hot_bands, and
// prop_intel share the element type, so they share the pool (ce-optimize
// prop-latency: the old per-request make() churned a ~40MB window slice and
// its GC pressure on every /api/dx_conditions and /api/hot_bands hit).
var historyScratchPool = sync.Pool{
	New: func() interface{} {
		b := make([]MQTTMessage, 0, propIntelHistoryPoolCap)
		return &b
	},
}

// snapshotHubHistoryWindow copies the hub.history slice covering
// [now-minutes*60, now] into a pooled scratch buffer and returns it together
// with a release func. Callers must release() as soon as Evaluate/HotBands
// has returned — the engine reads the slice but never retains it (same
// contract as the prop_intel pool this extends to the DX endpoints).
func snapshotHubHistoryWindow(now int64, minutes int) ([]MQTTMessage, func()) {
	cutoff := now - int64(minutes)*60
	hub.RLock()
	idx := sort.Search(len(hub.history), func(i int) bool {
		return hub.history[i].T >= cutoff
	})
	n := len(hub.history) - idx
	bufp := historyScratchPool.Get().(*[]MQTTMessage)
	if cap(*bufp) < n {
		*bufp = make([]MQTTMessage, n)
	} else {
		*bufp = (*bufp)[:n]
	}
	hist := (*bufp)[:n]
	copy(hist, hub.history[idx:])
	hub.RUnlock()
	return hist, func() { historyScratchPool.Put(bufp) }
}

// SlotsOfDay is the number of 30-minute slots in one UTC day: 48.
// Buckets are aggregated across the week into this 24h × 30min grid;
// day-of-week is intentionally collapsed because propagation patterns
// repeat daily, not weekly.
const SlotsOfDay = 48

type baselineBucket struct {
	Band         string `json:"band"`
	SlotOfDay    int    `json:"slot_of_day"`
	DistanceTier int    `json:"distance_tier"`
	SnrTier      int    `json:"snr_tier"`
	Count        int64  `json:"count"`
}

// UnmarshalJSON accepts both the v4 "slot_of_day" field and the legacy v3
// "hour_of_week" field, collapsing the latter to a 30-min slot index via
// slot = (hour_of_week % 24) * 2 (loses any within-hour or weekday detail
// the legacy scheme never actually carried). The v5 `source4` field is
// silently ignored — v6 drops the dimension entirely and the load path
// re-aggregates legacy keys via remapBucketsForLoad.
func (b *baselineBucket) UnmarshalJSON(data []byte) error {
	type alias baselineBucket
	aux := &struct {
		HourOfWeek *int    `json:"hour_of_week,omitempty"`
		SlotOfDay  *int    `json:"slot_of_day,omitempty"`
		Source4    *string `json:"source4,omitempty"` // accepted for legacy, ignored
		*alias
	}{alias: (*alias)(b)}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	switch {
	case aux.SlotOfDay != nil:
		b.SlotOfDay = *aux.SlotOfDay
	case aux.HourOfWeek != nil:
		b.SlotOfDay = (*aux.HourOfWeek % 24) * 2
	}
	return nil
}

type dxObservedEvent struct {
	T  int64  `json:"t"`
	B  string `json:"b"`
	SC string `json:"sc"`
	RC string `json:"rc"`
	SL string `json:"sl"`
	RL string `json:"rl"`
	RP int    `json:"rp"`
}

type baselineSnapshot struct {
	Version        int                        `json:"version"`
	SavedAt        int64                      `json:"saved_at"`
	FirstEventAt   int64                      `json:"first_event_at,omitempty"`
	LastEventAt    int64                      `json:"last_event_at,omitempty"`
	Buckets        map[string]*baselineBucket `json:"buckets"`
	ClusterBuckets map[string]*baselineBucket `json:"cluster_buckets,omitempty"`
	Events         []dxObservedEvent          `json:"events,omitempty"`
}

type DxBaselineEngine struct {
	mu             sync.RWMutex
	path           string
	store          *dxPostgresStore
	buckets        map[string]*baselineBucket
	clusterBuckets map[string]*baselineBucket
	firstEventAt   int64
	lastEventAt    int64
	eventsStart    int
	events         []dxObservedEvent

	// callsignResolver enables QRZ locator lookup for callsign QTH so the
	// cluster baseline can be derived (cluster anchor from the resolved locator).
	// Optional — when nil, callsign QTH falls back to ctyResolver, then global.
	callsignResolver CallsignLocatorResolver
	// ctyResolver provides DXCC entity centroids (lat/lon) as a last-resort
	// cluster derivation when QRZ has no locator for a callsign QTH.
	ctyResolver *cty.Resolver
}

type dxBandCondition struct {
	Band                      string         `json:"band"`
	Score                     float64        `json:"score"`
	Confidence                float64        `json:"confidence"`
	Status                    string         `json:"status"`
	Condition                 string         `json:"condition"`
	Mode                      string         `json:"mode"`
	Recommendation            string         `json:"recommendation"`
	CurrentLinks              int            `json:"current_links"`
	UniqueLinks               int            `json:"unique_links"`
	RepeatRatio               float64        `json:"repeat_ratio"`
	SpotsPerMinute            float64        `json:"spots_per_minute"`
	UniqueTxStations          int            `json:"unique_tx_stations"`
	UniqueRxStations          int            `json:"unique_rx_stations"`
	UniqueRemoteGrids         int            `json:"unique_remote_grids"`
	AvgDistanceKm             float64        `json:"avg_distance_km"`
	MaxDistanceKm             float64        `json:"max_distance_km"`
	MedianDistanceKm          float64        `json:"median_distance_km"`
	P90DistanceKm             float64        `json:"p90_distance_km"`
	LongHaulRatio             float64        `json:"long_haul_ratio"`
	DxRatio                   float64        `json:"dx_ratio"`
	AvgSnr                    float64        `json:"avg_snr"`
	PeakSnr                   int            `json:"peak_snr"`
	MedianSnr                 float64        `json:"median_snr"`
	P90Snr                    float64        `json:"p90_snr"`
	BaselineActivity          float64        `json:"baseline_activity"`
	ClusterBaselineUsed       bool           `json:"cluster_baseline_used"`
	BaselineActivityBySlot    []float64      `json:"baseline_activity_by_slot,omitempty"`
	BaselineSlotUsedByCluster []bool         `json:"baseline_slot_used_by_cluster,omitempty"`
	DominantDirection         string         `json:"dominant_direction"`
	AzimuthSectors            map[string]int `json:"azimuth_sectors,omitempty"`
	RegionCounts              map[string]int `json:"region_counts,omitempty"`
	Trend                     string         `json:"trend"`
	TrendDelta                float64        `json:"trend_delta"`
	Sparkline                 []float64      `json:"sparkline"`
	// ActivityByBin is the raw spots/min per time bin over the selected window
	// (length 12, i=0 oldest), backed by Postgres dx_raw_spots via recentEvents
	// when a store is configured. The Band Stats "Reports over time" chart uses
	// this for its bars so they cover the full window (incl. 120 min) instead
	// of the ≤60-min in-memory live stream. Absent on backends/depots that
	// can't supply it; the frontend falls back to live-spot counts.
	ActivityByBin []float64 `json:"activity_by_bin,omitempty"`
}

type dxConditionsResponse struct {
	QTH              string            `json:"qth"`
	Surroundings     bool              `json:"surroundings"`
	WindowMinutes    int               `json:"window_minutes"`
	CwMinDb          int               `json:"cw_min_db"`
	CurrentSlotOfDay int               `json:"current_slot_of_day"`
	GeneratedAt      int64             `json:"generated_at"`
	OperatorCluster  string            `json:"qth_cluster,omitempty"`
	BaselineBuckets  int               `json:"baseline_buckets"`
	BaselineEventCnt int               `json:"baseline_event_count"`
	BaselineHistoryM int               `json:"baseline_history_minutes"`
	OverallScore     float64           `json:"overall_score"`
	Confidence       float64           `json:"confidence"`
	Status           string            `json:"status"`
	Condition        string            `json:"condition"`
	BestBands        []string          `json:"best_bands"`
	RecommendedBands []string          `json:"recommended_bands"`
	WorstBands       []string          `json:"worst_bands"`
	AvoidBands       []string          `json:"avoid_bands"`
	Trend            string            `json:"trend"`
	TrendDelta       float64           `json:"trend_delta"`
	Bands            []dxBandCondition `json:"bands"`
}

type bandAccumulator struct {
	total         int
	uniqueLinks   map[string]struct{}
	uniqueTx      map[string]struct{}
	uniqueRx      map[string]struct{}
	uniqueRemote  map[string]struct{}
	totalDistance float64
	distancesKm   []float64
	longHaulCount int
	sumSnr        int
	snrs          []int
	peakSnr       int
	ssbCount      int
	cwCount       int
	directionBins map[string]int
	regionBins    map[string]int
}

type matchedBandEvent struct {
	band         string
	remote4      string
	qualityKey   string
	dedupKey     string
	snr          int
	distanceKm   float64
	direction    string
	tx           string
	rx           string
	timestampSec int64
}

func newDxBaselineEngine(path string) *DxBaselineEngine {
	initialCap := dxBaselineMaxEvents
	if initialCap < 0 {
		initialCap = 0
	}
	if initialCap > 4096 {
		initialCap = 4096
	}
	return &DxBaselineEngine{
		path:           strings.TrimSpace(path),
		buckets:        make(map[string]*baselineBucket),
		clusterBuckets: make(map[string]*baselineBucket),
		events:         make([]dxObservedEvent, 0, initialCap),
	}
}

// SetResolvers wires the optional callsign→locator (QRZ) and DXCC-entity
// (cty.dat) resolvers used by the regional baseline to derive the operator's
// region for callsign targets. Locator targets derive their region directly;
// callsign targets try QRZ first, then fall back to the DXCC entity centroid.
// A nil argument leaves the existing resolver unchanged (so QRZ can be wired
// later without clobbering the cty resolver set at startup).
func (e *DxBaselineEngine) SetResolvers(callsign CallsignLocatorResolver, ctyRes *cty.Resolver) {
	if e == nil {
		return
	}
	e.mu.Lock()
	if callsign != nil {
		e.callsignResolver = callsign
	}
	if ctyRes != nil {
		e.ctyResolver = ctyRes
	}
	e.mu.Unlock()
}

func (e *DxBaselineEngine) EnablePostgres(dsn string) error {
	st, err := newDxPostgresStore(context.Background(), dsn)
	if err != nil {
		return err
	}
	if err := st.ensureDxPulseRegionBaseline(context.Background()); err != nil {
		st.Close()
		return err
	}
	// Backfill the grid-cluster baseline (dx_baseline_cluster) from dx_raw_spots
	// so the two-tier fallback (cluster → global) has data immediately after the
	// table is created, rather than accumulating over the first day.
	// No-op once the table has rows; async so it doesn't delay the HTTP listener.
	go func() {
		if err := st.ensureDxBaselineCluster(context.Background()); err != nil {
			logInfo("DX regional baseline backfill failed: %v", err)
		}
	}()
	// Best-effort: record when baseline accumulation began so the per-minute
	// normaliser has a real span. Non-fatal if it can't be determined yet.
	if err := st.seedBaselineFirstObservedIfMissing(context.Background()); err != nil {
		logInfo("baseline first-observed seed skipped: %v", err)
	}
	e.mu.Lock()
	e.store = st
	e.mu.Unlock()
	return nil
}

// Store returns the Postgres store if configured, or nil. Used by the WSPR
// climatology engine to share the same store pool (for wspr_region_baseline_daily).
func (e *DxBaselineEngine) Store() *dxPostgresStore {
	if e == nil {
		return nil
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.store
}

func (e *DxBaselineEngine) LoadRecentSpotCache(minutes int, now int64, includeDXCluster bool) ([]MQTTMessage, error) {
	e.mu.RLock()
	st := e.store
	e.mu.RUnlock()
	if st == nil {
		return nil, nil
	}
	return st.loadRecentSpotCache(minutes, now, includeDXCluster)
}

func (e *DxBaselineEngine) LoadSpotsBetween(start, end int64) ([]MQTTMessage, error) {
	e.mu.RLock()
	st := e.store
	e.mu.RUnlock()
	if st == nil {
		return nil, nil
	}
	return st.loadSpotsBetween(start, end)
}

// LoadSpotsBetweenFiltered is LoadSpotsBetween with the source filter exposed
// (same includeDXCluster semantics as LoadRecentSpotCache). The startup
// backfill walks the window in chunks through this so peak memory stays at
// one chunk instead of the whole window.
func (e *DxBaselineEngine) LoadSpotsBetweenFiltered(start, end int64, includeDXCluster bool) ([]MQTTMessage, error) {
	e.mu.RLock()
	st := e.store
	e.mu.RUnlock()
	if st == nil {
		return nil, nil
	}
	return st.loadSpotsBetweenWithSourceFilter(start, end, includeDXCluster)
}

// CountSpotsBetweenFiltered returns the number of spots in the window under
// the same source filter as LoadSpotsBetweenFiltered, so the caller can
// allocate the destination slice exactly once.
func (e *DxBaselineEngine) CountSpotsBetweenFiltered(start, end int64, includeDXCluster bool) (int64, error) {
	e.mu.RLock()
	st := e.store
	e.mu.RUnlock()
	if st == nil {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var n int64
	err := st.pool.QueryRow(ctx, `
		SELECT count(*) FROM dx_raw_spots
		WHERE spot_time BETWEEN $1 AND $2
		  AND ($3::bool OR LOWER(COALESCE(source_type, 'mqtt')) <> 'dxcluster')
	`, start, end, includeDXCluster).Scan(&n)
	return n, err
}

// PruneRawSpotsOlderThan removes raw spots older than the given Unix
// timestamp from the persistent store. No-op if Postgres isn't configured.
func (e *DxBaselineEngine) PruneRawSpotsOlderThan(cutoff int64) (int64, error) {
	if e == nil {
		return 0, nil
	}
	e.mu.RLock()
	st := e.store
	e.mu.RUnlock()
	if st == nil {
		return 0, nil
	}
	return st.pruneRawSpotsOlderThan(cutoff)
}

// PruneRegionBaselinesOlderThan removes day-indexed climatology rows older than
// cutoffDayIndex from both dx_region_baseline_daily and wspr_region_baseline_daily.
// No-op if Postgres isn't configured. The reader (regionCalendarStats /
// WsprRegionCalendarStats) only ever queries a ~30-day window, so older rows
// are dead weight that grows the table unbounded without this prune.
func (e *DxBaselineEngine) PruneRegionBaselinesOlderThan(cutoffDayIndex int64) (int64, error) {
	if e == nil {
		return 0, nil
	}
	e.mu.RLock()
	st := e.store
	e.mu.RUnlock()
	if st == nil {
		return 0, nil
	}
	return st.pruneRegionBaselinesOlderThan(cutoffDayIndex)
}

func (e *DxBaselineEngine) NumBuckets() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.buckets) + len(e.clusterBuckets)
}

func (e *DxBaselineEngine) Stats(now int64) (bucketCount, eventCount, historyMinutes int) {
	if e == nil {
		return 0, 0, 0
	}

	e.mu.RLock()
	st := e.store
	if st == nil {
		bucketCount = len(e.buckets) + len(e.clusterBuckets)
		events := e.snapshotEventsLocked()
		eventCount = len(events)
		historyMinutes = baselineHistoryMinutes(e.firstEventAt, e.lastEventAt, events, now)
		e.mu.RUnlock()
		return bucketCount, eventCount, historyMinutes
	}
	e.mu.RUnlock()

	bucketCount, eventCount, historyMinutes, err := st.baselineStats(now)
	if err != nil {
		return 0, 0, 0
	}
	return bucketCount, eventCount, historyMinutes
}

func (e *DxBaselineEngine) Observe(m MQTTMessage) {
	band := normalizeBand(m.B)
	if band == "" {
		return
	}
	sl := strings.ToUpper(strings.TrimSpace(m.SL))
	rl := strings.ToUpper(strings.TrimSpace(m.RL))
	sc := strings.ToUpper(strings.TrimSpace(m.SC))
	rc := strings.ToUpper(strings.TrimSpace(m.RC))
	if sl == "" || rl == "" {
		return
	}
	ts := m.T
	if ts <= 0 {
		ts = time.Now().Unix()
	}
	hour := utcSlotOfDay(ts)
	distTier := distanceTierForLocators(sl, rl)
	snrTier := snrTierFromDb(m.RP)

	e.mu.Lock()
	if ts > 0 {
		if e.firstEventAt == 0 || ts < e.firstEventAt {
			e.firstEventAt = ts
		}
		if ts > e.lastEventAt {
			e.lastEventAt = ts
		}
	}

	// Keys are built in a stack buffer for the map lookups; observeBucket
	// clones on insert, so the map never retains the stack-backed strings.
	var kb [96]byte
	buf := appendBaselineKey(kb[:0], band, hour, distTier, snrTier)
	e.observeBucket(e.buckets, stackString(buf), band, hour, distTier, snrTier)

	// Grid-cluster baseline: write a bucket keyed by the observer's grid
	// cluster anchor for both ends of the path. This is the middle tier
	// (cluster → global) that gives operators with thin qth history a baseline
	// scoped to their part of the world (6×6 grid squares, not the whole globe)
	// instead of the global average. Both ends' clusters are written (a spot
	// between cluster JN68 and cluster EM46 increments both) so either
	// operator's Evaluate benefits.
	if anchor, ok := locatorClusterAnchor(sl); ok {
		buf = append(append(kb[:0], anchor...), '|')
		buf = appendBaselineKey(buf, band, hour, distTier, snrTier)
		e.observeBucket(e.clusterBuckets, stackString(buf), band, hour, distTier, snrTier)
	}
	if anchor, ok := locatorClusterAnchor(rl); ok {
		buf = append(append(kb[:0], anchor...), '|')
		buf = appendBaselineKey(buf, band, hour, distTier, snrTier)
		e.observeBucket(e.clusterBuckets, stackString(buf), band, hour, distTier, snrTier)
	}

	maxEvents := dxBaselineMaxEvents
	if maxEvents < 0 {
		maxEvents = 0
	}
	if maxEvents < len(e.events) {
		if maxEvents == 0 {
			e.events = nil
			e.eventsStart = 0
		} else {
			ordered := e.snapshotEventsLocked()
			if len(ordered) > maxEvents {
				ordered = ordered[len(ordered)-maxEvents:]
			}
			rebuilt := make([]dxObservedEvent, len(ordered), maxEvents)
			copy(rebuilt, ordered)
			e.events = rebuilt
			e.eventsStart = 0
		}
	}

	if maxEvents > 0 {
		ev := dxObservedEvent{T: ts, B: band, SC: sc, RC: rc, SL: sl, RL: rl, RP: m.RP}
		if len(e.events) < maxEvents {
			e.events = append(e.events, ev)
		} else {
			e.events[e.eventsStart] = ev
			e.eventsStart++
			if e.eventsStart >= maxEvents {
				e.eventsStart = 0
			}
		}
	}

	st := e.store
	e.mu.Unlock()
	if st != nil {
		_ = st.observe(m, band, hour, distTier, snrTier)
	}
	if cellBucketFeed != nil {
		cellBucketFeed.Observe(m)
	}
}

func (e *DxBaselineEngine) PersistRawSpot(m MQTTMessage, sourceType, spotter string, frequencyKHz *float64, comment string) {
	e.mu.RLock()
	st := e.store
	e.mu.RUnlock()
	if st == nil {
		return
	}

	band := normalizeBand(m.B)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := st.insertRawSpot(ctx, m, band, sourceType, spotter, frequencyKHz, comment); err != nil {
		logDebug("DX raw spot persistence failed (source=%s): %v", sourceType, err)
	}
	if cellBucketFeed != nil {
		cellBucketFeed.Observe(m)
	}
}

// observeBucket looks up a bucket by key and increments it. key may be a
// stack-backed lookup string (stackString); on insert it is cloned to a heap
// string so the map never retains the caller's buffer.
func (e *DxBaselineEngine) observeBucket(store map[string]*baselineBucket, key, band string, hour, distTier, snrTier int) {
	b := store[key]
	if b == nil {
		b = &baselineBucket{Band: band, SlotOfDay: hour, DistanceTier: distTier, SnrTier: snrTier}
		store[strings.Clone(key)] = b
	}
	b.Count++
}

func (e *DxBaselineEngine) Load() error {
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
	var snap baselineSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return err
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	// v8 snapshot: per-call and 11-region baseline tiers were removed; only
	// the global and grid-cluster buckets persist. Snapshots before v8 carry
	// target_buckets/regional_buckets which are discarded (the cluster tier
	// is rebuilt from dx_raw_spots on startup, so no data is lost).
	legacy := snap.Version < 8
	e.buckets = remapBucketsForLoad(snap.Buckets, legacy)
	if snap.ClusterBuckets != nil {
		e.clusterBuckets = remapBucketsForLoad(snap.ClusterBuckets, legacy)
	}

	e.firstEventAt = snap.FirstEventAt
	e.lastEventAt = snap.LastEventAt

	maxEvents := dxBaselineMaxEvents
	if maxEvents < 0 {
		maxEvents = 0
	}
	if maxEvents == 0 {
		e.events = nil
		e.eventsStart = 0
		if e.firstEventAt == 0 || e.lastEventAt == 0 {
			e.refreshEventSpanLocked()
		}
		return nil
	}
	if len(snap.Events) > maxEvents {
		snap.Events = snap.Events[len(snap.Events)-maxEvents:]
	}
	e.events = append(make([]dxObservedEvent, 0, maxEvents), snap.Events...)
	e.eventsStart = 0
	if e.firstEventAt == 0 || e.lastEventAt == 0 {
		e.refreshEventSpanLocked()
	}
	return nil
}

func (e *DxBaselineEngine) Save() error {
	if e == nil || e.path == "" {
		return nil
	}
	e.mu.RLock()
	snap := baselineSnapshot{
		Version:        8,
		SavedAt:        time.Now().Unix(),
		FirstEventAt:   e.firstEventAt,
		LastEventAt:    e.lastEventAt,
		Buckets:        cloneBuckets(e.buckets),
		ClusterBuckets: cloneBuckets(e.clusterBuckets),
		Events:         e.snapshotEventsLocked(),
	}
	e.mu.RUnlock()

	return writeJSONAtomic(e.path, snap)
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return nil
}

func cloneBuckets(src map[string]*baselineBucket) map[string]*baselineBucket {
	out := make(map[string]*baselineBucket, len(src))
	for k, v := range src {
		if v == nil {
			continue
		}
		cp := *v
		out[k] = &cp
	}
	return out
}

// remapBucketsForLoad rebuilds the in-memory bucket map from a freshly
// unmarshalled snapshot using the current key shape. When legacy is true
// (snapshot version < 6) the existing keys may embed a source4 segment
// and the bucket's Source4 field may be set — both are discarded; entries
// that collide on the new (band, slot, distTier, snrTier) tuple sum.
func remapBucketsForLoad(src map[string]*baselineBucket, legacy bool) map[string]*baselineBucket {
	out := make(map[string]*baselineBucket, len(src))
	for _, v := range src {
		if v == nil {
			continue
		}
		newKey := baselineKey(v.Band, v.SlotOfDay, v.DistanceTier, v.SnrTier)
		if existing, ok := out[newKey]; ok {
			existing.Count += v.Count
			continue
		}
		cp := *v
		out[newKey] = &cp
	}
	_ = legacy // both branches collapse to the same shape now
	return out
}

// deriveOperatorCluster determines the grid-cluster anchor locator for the
// operator's QTH, used to key the cluster baseline (the middle tier of the
// cluster → global fallback). Resolution order:
//  1. Locator QTH → locatorClusterAnchor directly.
//  2. Callsign QTH → QRZ locator → locatorClusterAnchor.
//  3. Callsign QTH, QRZ fails → DXCC entity centroid (cty.dat) →
//     centroidLatLng → locatorClusterAnchor.
//  4. All fail → "" (cluster tier skipped; falls back to global).
func (e *DxBaselineEngine) deriveOperatorCluster(qth string) string {
	if qth == "" {
		return ""
	}
	// Locator QTH: derive directly.
	if isLocator(qth) {
		if anchor, ok := locatorClusterAnchor(qth); ok {
			return anchor
		}
		return ""
	}
	// Callsign QTH: try QRZ, then cty.dat centroid.
	e.mu.RLock()
	qrz := e.callsignResolver
	ctyRes := e.ctyResolver
	e.mu.RUnlock()
	if qrz != nil {
		if info, err := qrz.LookupInfo(qth); err == nil && info.Locator != "" {
			if anchor, ok := locatorClusterAnchor(info.Locator); ok {
				return anchor
			}
		}
	}
	if ctyRes != nil {
		if ent, _, ok := ctyRes.Resolve(qth); ok && ent.Lat != 0 && ent.Lon != 0 {
			if anchor, ok := locatorClusterAnchor(latLngToLocator(ent.Lat, ent.Lon, 4)); ok {
				return anchor
			}
		}
	}
	return ""
}

func (e *DxBaselineEngine) Evaluate(qth string, surroundings bool, minutes int, cwMinDb int, history []MQTTMessage, now int64) dxConditionsResponse {
	qth = normalizeQTHToken(qth)
	if minutes <= 0 {
		minutes = defaultDxWindowMinutes
	}
	if minutes > maxDxWindowMinutes {
		minutes = maxDxWindowMinutes
	}
	if cwMinDb < -40 || cwMinDb > 20 {
		cwMinDb = defaultDxCwViableMinDb
	}

	resp := dxConditionsResponse{
		QTH:              qth,
		Surroundings:     surroundings,
		WindowMinutes:    minutes,
		CwMinDb:          cwMinDb,
		CurrentSlotOfDay: utcSlotOfDay(now),
		GeneratedAt:      now,
		Status:           "grey",
		Condition:        "Poor",
		BestBands:        []string{},
		RecommendedBands: []string{},
		WorstBands:       []string{},
		AvoidBands:       []string{},
		Bands:            []dxBandCondition{},
		Trend:            "stable",
	}

	if qth == "" {
		return resp
	}

	// Derive the operator's grid-cluster anchor for the cluster baseline tier.
	operatorCluster := e.deriveOperatorCluster(qth)
	resp.OperatorCluster = operatorCluster

	qthSet := []string{qth}
	if surroundings && isLocator(qth) {
		qthSet = getSurroundingSquares(qth)
	}

	e.mu.RLock()
	st := e.store
	// In-memory (dev/no-store) path: clone the baseline bucket maps and snapshot
	// the event ring for the per-band baseline calls + activity fallback below.
	// The store path skips these — the bucket clones are unused there, and the
	// event snapshot is only needed if the Postgres aggregate fails (deferred to
	// that case) — avoiding a full bucket-map + ~1M-event ring copy per request
	// and not holding the RLock through the copy (which blocks the 20k/min
	// ingest Observe path).
	var globalBuckets, clusterBuckets map[string]*baselineBucket
	var events []dxObservedEvent
	var firstEventAt, lastEventAt int64
	if st == nil {
		globalBuckets = cloneBuckets(e.buckets)
		clusterBuckets = cloneBuckets(e.clusterBuckets)
		events = e.snapshotEventsLocked()
		// Capture the event span under the RLock — reading these unlocked
		// (as the old code did) races with Observe's writes, skewing the
		// history-minutes denominator and the baseline normaliser.
		firstEventAt = e.firstEventAt
		lastEventAt = e.lastEventAt
	}
	e.mu.RUnlock()

	var aggGlobalBuckets map[string]*baselineBucket
	var aggClusterBuckets map[string]*baselineBucket
	if st == nil {
		// v8: keys already exclude source4, so the maps are usable directly.
		aggGlobalBuckets = globalBuckets
		aggClusterBuckets = clusterBuckets
		resp.BaselineBuckets = len(globalBuckets) + len(clusterBuckets)
		resp.BaselineEventCnt = len(events)
		resp.BaselineHistoryM = baselineHistoryMinutes(firstEventAt, lastEventAt, events, now)
	} else {
		if b, ev, hm, err := st.baselineStats(now); err == nil {
			resp.BaselineBuckets = b
			resp.BaselineEventCnt = ev
			resp.BaselineHistoryM = hm
		} else {
			// Don't swallow: a failure here leaves BaselineHistoryM at 0, which
			// previously caused the baseline normaliser to over-inflate.
			logDebug("dx baselineStats failed (baseline_history_minutes defaults to 0): %v", err)
		}
	}

	// activityByBinMap is the Postgres-backed, server-side-aggregated spots/min
	// time series per band for the chart bars AND the trend/sparkline. Built
	// once for all bands via one bounded GROUP BY query so a high-volume
	// dx_raw_spots table can't truncate it (the old recentEvents row-
	// materialisation path did). nil when there's no store, the query fails,
	// or zero rows matched; the per-band loop then lazily snapshots the
	// in-memory event ring and falls back to in-memory binning (see the loop).
	var activityByBinMap map[string][]float64
	if st != nil {
		if m, err := st.activityByBinForTargets(qthSet, cwMinDb, minutes, now); err == nil {
			activityByBinMap = m
		} else {
			logDebug("dx activityByBinForTargets failed (falling back to in-memory binning): %v", err)
		}
	}

	// baselinePairs{Cluster,Global} are the all-bands/all-slots/all-tiers
	// baseline breakdown fetched ONCE (2 PG round-trips) so the per-band loop
	// below can derive activity/support/quantiles/all-slots in memory instead of
	// issuing ~100 per-band round-trips. nil on no-store or query error; the
	// per-band derivation then yields zero/unused, matching the old per-band
	// err == nil guards.
	var baselinePairsCluster, baselinePairsGlobal map[bandSlotKey][]baselinePair
	if st != nil {
		if tc, tg, err := st.allBandBaselinePairs(operatorCluster); err == nil {
			baselinePairsCluster, baselinePairsGlobal = tc, tg
		} else {
			logDebug("dx allBandBaselinePairs failed (per-band baseline falls back to zero): %v", err)
		}
	}
	// The per-band loop below snapshots the event ring lazily (only when the
	// Postgres aggregate lacks a band) instead of eagerly here: an eager copy
	// would fire on every request whose targets matched zero raw rows —
	// including the common "genuinely quiet grid" case that returns before the
	// loop and would never consume the ~ring-size copy.

	cutoff := now - int64(minutes*60)
	bandAcc := make(map[string]*bandAccumulator)
	dedupSeen := make(map[string]struct{})

	for _, m := range history {
		if m.T < cutoff || m.T > now {
			continue
		}
		ev, matched := extractMatchedBandEvent(m, qthSet)
		if !matched {
			continue
		}
		// Gate to the analysed HF/low-VHF band set (160m–2m). Microwave and
		// other out-of-scope bands that occasionally arrive on the feeds are
		// passed through for display but excluded from conditions, matching
		// the gate already applied in hot_bands.go and dx_cellfeed.go.
		if !bandInScope(ev.band) {
			continue
		}
		// Keep the FT8-calibrated conditions accumulator (spots_per_min, classifyMode,
		// avgSnr, distances) pure from RBN CW/RTTY, whose dB is on a different SNR scale
		// than PSKReporter FT8 SNR (docs/horstprop.md). FT8/FT4 (PSKReporter), "" (legacy)
		// and "DXCLUSTER" (a source marker, not a mode — RP=0, already counted today) stay
		// in; real non-FT8 modes (CW, RTTY, PSK*, SSB, … only produced by RBN) are skipped.
		// RBN spots still reach the live stream (broadcastMsg) and the activity chart.
		if isNonConditionsMode(m.MD) {
			continue
		}
		if ev.snr < cwMinDb {
			continue
		}
		if _, exists := dedupSeen[ev.dedupKey]; exists {
			continue
		}
		dedupSeen[ev.dedupKey] = struct{}{}

		acc := bandAcc[ev.band]
		if acc == nil {
			acc = &bandAccumulator{
				uniqueLinks:   make(map[string]struct{}),
				uniqueTx:      make(map[string]struct{}),
				uniqueRx:      make(map[string]struct{}),
				uniqueRemote:  make(map[string]struct{}),
				directionBins: make(map[string]int),
				regionBins:    make(map[string]int),
				peakSnr:       -999,
			}
			bandAcc[ev.band] = acc
		}

		acc.total++
		acc.uniqueLinks[ev.qualityKey] = struct{}{}
		acc.uniqueTx[ev.tx] = struct{}{}
		acc.uniqueRx[ev.rx] = struct{}{}
		if ev.remote4 != "" {
			acc.uniqueRemote[ev.remote4] = struct{}{}
		}
		acc.totalDistance += ev.distanceKm
		acc.distancesKm = append(acc.distancesKm, ev.distanceKm)
		if ev.distanceKm >= dxLongHaulThresholdKm {
			acc.longHaulCount++
		}
		acc.sumSnr += ev.snr
		acc.snrs = append(acc.snrs, ev.snr)
		if ev.snr > acc.peakSnr {
			acc.peakSnr = ev.snr
		}
		if ev.snr >= 0 {
			acc.ssbCount++
		}
		if ev.snr >= -12 {
			acc.cwCount++
		}
		if ev.direction != "" {
			acc.directionBins[ev.direction]++
		}
		if r := region.FromLocator(ev.remote4); r != "" && r != region.Unknown {
			acc.regionBins[string(r)]++
		}
	}

	if len(bandAcc) == 0 {
		return resp
	}

	bands := make([]dxBandCondition, 0, len(bandAcc))
	totalScoreWeighted := 0.0
	totalConfWeighted := 0.0
	totalWeight := 0.0
	trendAccumulator := 0.0

	for band, acc := range bandAcc {
		if acc.total == 0 {
			continue
		}

		baselineActivity := 0.0
		clusterBaselineUsed := false
		baselineSupport := int64(0)
		q25, q75, quantileOK := 0.0, 0.0, false
		var baselineActivityBySlot []float64
		var baselineSlotUsedByCluster []bool
		if st == nil {
			baselineActivity, _ = baselineActivityForBand(aggGlobalBuckets, aggClusterBuckets, operatorCluster, band, resp.CurrentSlotOfDay, resp.BaselineHistoryM)
			baselineSupport = baselineSupportForBand(aggGlobalBuckets, aggClusterBuckets, operatorCluster, band, resp.CurrentSlotOfDay)
			q25, q75, clusterBaselineUsed, quantileOK = baselineScoreQuantilesForBand(aggGlobalBuckets, aggClusterBuckets, operatorCluster, band, resp.CurrentSlotOfDay)
			baselineActivityBySlot, baselineSlotUsedByCluster = baselineActivityForBandAllSlots(aggGlobalBuckets, aggClusterBuckets, operatorCluster, band, resp.BaselineHistoryM)
		} else {
			// Derive all four baseline values from the single all-bands/all-slots
			// index fetched above (2 PG round-trips total) instead of ~100
			// per-band round-trips. On fetch failure all indexes are nil and
			// every value stays zero/unused, matching the old per-band err guards.
			pairs, usedCluster := pairsForBandSlot(baselinePairsCluster, baselinePairsGlobal, band, resp.CurrentSlotOfDay)
			baselineActivity = normalizeBaselineToSpotsPerMinute(float64(sumPairs(pairs)), resp.BaselineHistoryM)
			clusterBaselineUsed = usedCluster
			baselineSupport = sumPairs(pairs)
			q25, q75, quantileOK = quantilesFromPairs(pairs)
			// Length-48 per-slot series, built only when the fetch succeeded
			// (nil global index ⇒ fetch failed ⇒ leave nil, as the old call did
			// on its own error).
			if baselinePairsGlobal != nil {
				rates := make([]float64, SlotsOfDay)
				usedSlot := make([]bool, SlotsOfDay)
				for slot := 0; slot < SlotsOfDay; slot++ {
					ps, uc := pairsForBandSlot(baselinePairsCluster, baselinePairsGlobal, band, slot)
					rates[slot] = normalizeBaselineToSpotsPerMinute(float64(sumPairs(ps)), resp.BaselineHistoryM)
					usedSlot[slot] = uc
				}
				baselineActivityBySlot = rates
				baselineSlotUsedByCluster = usedSlot
			}
		}

		// Prefer the Postgres aggregate; fall back to in-memory binning from
		// `events` when the map has no series for this band (no store, query
		// failed/returned nothing, or the band's only spots are newer than the
		// last raw-spot flush). The ring snapshot is taken lazily here (once)
		// so the common all-bands-present path never pays the ~ring-size copy.
		activityByBin := activityByBinMap[band]
		if activityByBin == nil {
			if st != nil && events == nil {
				e.mu.RLock()
				events = e.snapshotEventsLocked()
				e.mu.RUnlock()
			}
			activityByBin = buildBandActivityByBin(events, qthSet, band, cwMinDb, minutes, now)
		}
		// Sparkline = the activity series normalised to 0..100 (max-scaling),
		// so hot_bands' sustainedRecentBins floor (30.0) and computeTrend stay
		// meaningful. Backed by the full-window activity series now, not the old
		// recentEvents path that only ever populated the newest bins.
		historicalBandSeries := normalizeSeriesTo100(activityByBin)
		trend, trendDelta := computeTrend(historicalBandSeries)

		uniqueCount := len(acc.uniqueLinks)
		repeatRatio := 0.0
		if acc.total > 0 {
			repeatRatio = clamp01(float64(acc.total-uniqueCount) / float64(acc.total))
		}
		spotsPerMin := float64(acc.total) / math.Max(1, float64(minutes))
		avgDistance := 0.0
		if acc.total > 0 {
			avgDistance = acc.totalDistance / float64(acc.total)
		}
		maxDistance := maxFloat(acc.distancesKm)
		medianDistance := percentileFloat(acc.distancesKm, 0.5)
		p90Distance := percentileFloat(acc.distancesKm, 0.9)
		longHaulRatio := clamp01(float64(acc.longHaulCount) / float64(acc.total))
		dxRatio := longHaulRatio

		avgSnr := float64(acc.sumSnr) / math.Max(1, float64(acc.total))
		medianSnr := percentileInt(acc.snrs, 0.5)
		p90Snr := percentileInt(acc.snrs, 0.9)

		activityNorm := activityScoreNorm(spotsPerMin, baselineActivity)
		distanceNorm := clamp01((p90Distance/7000.0)*0.7 + dxRatio*0.3)
		snrNorm := clamp01((p90Snr + 20.0) / 30.0)
		rawScore := (0.45*distanceNorm + 0.35*activityNorm + 0.20*snrNorm) * 100.0

		spotSupport := clamp01(float64(acc.total) / 25.0)
		baselineSupportNorm := clamp01(float64(baselineSupport) / 2000.0)
		confidenceFactor := spotSupport * (0.35 + 0.65*baselineSupportNorm)
		if !quantileOK {
			confidenceFactor *= 0.75
		}
		bandScore := clamp(rawScore*confidenceFactor, 0, 100)
		bandConfidence := clamp(confidenceFactor*100.0, 0, 99)
		status := classifyBandStatus(bandScore, q25, q75, quantileOK, acc.total)
		condition := legacyConditionFromStatus(status, bandScore)
		mode := classifyMode(acc.ssbCount, acc.cwCount, acc.total)
		direction := dominantDirection(acc.directionBins)
		recommendation := classifyRecommendation(status, mode, dxRatio, spotsPerMin, bandConfidence)

		bands = append(bands, dxBandCondition{
			Band:                      band,
			Score:                     round1(bandScore),
			Confidence:                round1(bandConfidence),
			Status:                    status,
			Condition:                 condition,
			Mode:                      mode,
			Recommendation:            recommendation,
			CurrentLinks:              acc.total,
			UniqueLinks:               uniqueCount,
			RepeatRatio:               round2(repeatRatio),
			SpotsPerMinute:            round2(spotsPerMin),
			UniqueTxStations:          len(acc.uniqueTx),
			UniqueRxStations:          len(acc.uniqueRx),
			UniqueRemoteGrids:         len(acc.uniqueRemote),
			AvgDistanceKm:             round1(avgDistance),
			MaxDistanceKm:             round1(maxDistance),
			MedianDistanceKm:          round1(medianDistance),
			P90DistanceKm:             round1(p90Distance),
			LongHaulRatio:             round2(longHaulRatio),
			DxRatio:                   round2(dxRatio),
			AvgSnr:                    round1(avgSnr),
			PeakSnr:                   acc.peakSnr,
			MedianSnr:                 round1(medianSnr),
			P90Snr:                    round1(p90Snr),
			BaselineActivity:          round2(baselineActivity),
			ClusterBaselineUsed:       clusterBaselineUsed,
			BaselineActivityBySlot:    roundFloats2(baselineActivityBySlot),
			BaselineSlotUsedByCluster: baselineSlotUsedByCluster,
			DominantDirection:         direction,
			AzimuthSectors:            acc.directionBins,
			RegionCounts:              acc.regionBins,
			Trend:                     trend,
			TrendDelta:                trendDelta,
			Sparkline:                 historicalBandSeries,
			ActivityByBin:             roundFloats2(activityByBin),
		})

		weight := float64(acc.total)
		totalScoreWeighted += bandScore * weight
		totalConfWeighted += bandConfidence * weight
		totalWeight += weight
		trendAccumulator += trendDelta * weight
	}

	sort.Slice(bands, func(i, j int) bool {
		if bands[i].Score == bands[j].Score {
			return bands[i].Confidence > bands[j].Confidence
		}
		return bands[i].Score > bands[j].Score
	})

	resp.Bands = bands
	if totalWeight > 0 {
		resp.OverallScore = round1(totalScoreWeighted / totalWeight)
		resp.Confidence = round1(totalConfWeighted / totalWeight)
		resp.TrendDelta = round2(trendAccumulator / totalWeight)
	}
	resp.Status = classifyOverallStatus(resp.OverallScore, resp.Confidence, len(resp.Bands))
	resp.Condition = legacyConditionFromStatus(resp.Status, resp.OverallScore)
	if resp.TrendDelta > 0.2 {
		resp.Trend = "rising"
	} else if resp.TrendDelta < -0.2 {
		resp.Trend = "falling"
	} else {
		resp.Trend = "stable"
	}

	for i := 0; i < len(bands) && i < 3; i++ {
		resp.BestBands = append(resp.BestBands, bands[i].Band)
		if bands[i].Status == "green" || bands[i].Status == "yellow" {
			resp.RecommendedBands = append(resp.RecommendedBands, bands[i].Band)
		}
	}

	sortedWorst := append([]dxBandCondition(nil), bands...)
	sort.Slice(sortedWorst, func(i, j int) bool {
		if sortedWorst[i].Score == sortedWorst[j].Score {
			return sortedWorst[i].Confidence > sortedWorst[j].Confidence
		}
		return sortedWorst[i].Score < sortedWorst[j].Score
	})
	for _, b := range sortedWorst {
		if len(resp.WorstBands) >= 3 {
			break
		}
		resp.WorstBands = append(resp.WorstBands, b.Band)
		if b.Confidence >= 30 && (b.Status == "red" || b.Status == "grey") {
			resp.AvoidBands = append(resp.AvoidBands, b.Band)
		}
	}

	return resp
}

// roundFloats2 returns a copy of v with each element rounded to 2 decimals;
// nil stays nil so omitempty fields drop cleanly.
func roundFloats2(v []float64) []float64 {
	if v == nil {
		return nil
	}
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = round2(x)
	}
	return out
}

// maxPlausibleBaselineSpotsPerMin bounds an honest baseline activity rate (the
// cluster/global network aggregate per band+slot; observed prod values run
// 100–1000/min). Rates far above this are corrupt baselines (see
// maxPlausibleBaselineCount in dx_postgres.go — the historic mergePendingBack
// double-merge left prod counts around ±1e15) and are treated as missing so
// they can't pin activityNorm to 0 (positive corruption) or inflate it via
// the no-baseline fallback (negative corruption).
const maxPlausibleBaselineSpotsPerMin = 1e6

func activityScoreNorm(spotsPerMin, baselineActivity float64) float64 {
	// Missing OR implausible baseline → absolute-rate normalisation.
	if baselineActivity <= 0 || baselineActivity > maxPlausibleBaselineSpotsPerMin {
		return clamp01(spotsPerMin / 2.0)
	}
	ratio := spotsPerMin / math.Max(0.05, baselineActivity)
	return clamp01(ratio / 2.0)
}

func classifyBandStatus(score, q25, q75 float64, quantileOK bool, spots int) string {
	if spots < 2 || !quantileOK {
		return "grey"
	}
	if score > q75 {
		return "green"
	}
	if score < q25 {
		return "red"
	}
	return "yellow"
}

func classifyOverallStatus(score, confidence float64, bands int) string {
	if bands == 0 || confidence < 15 {
		return "grey"
	}
	if score >= 65 {
		return "green"
	}
	if score >= 35 {
		return "yellow"
	}
	return "red"
}

func legacyConditionFromStatus(status string, score float64) string {
	switch status {
	case "green":
		if score >= 78 {
			return "Excellent"
		}
		return "Good"
	case "yellow":
		return "Fair"
	case "red":
		return "Poor"
	default:
		return "Poor"
	}
}

// isNonConditionsMode reports whether a spot's mode is a real over-the-air mode that the
// FT8-calibrated conditions accumulator must exclude. RBN CW/RTTY dB is on a different SNR
// scale than PSKReporter FT8 SNR (docs/horstprop.md), so feeding it to classifyMode / the
// per-band snr and distance stats would corrupt them (strong CW would mislabel a band
// "ssb"; +20 dB would create phantom high snr_tier rows). WSPR is likewise excluded: its SNR
// is on the same 2500 Hz scale as FT8 but WSPR stations transmit at wildly varying power
// (0.1–100 W+), so raw SNR conflates station capability with propagation. FT8/FT4
// (PSKReporter), "" (legacy / unset), and "DXCLUSTER" (a source marker, not a real mode —
// RP=0, already counted in the baseline today) stay in; any other real mode (CW, RTTY, PSK*,
// SSB, AM, FM, DIGI, WSPR, …) is excluded. Only RBN and WSPR produce those real non-FT8
// modes today.
func isNonConditionsMode(md string) bool {
	switch strings.ToUpper(strings.TrimSpace(md)) {
	case "", "FT8", "FT4", "DXCLUSTER":
		return false
	}
	return true
}

func classifyMode(ssbCount, cwCount, total int) string {
	if total <= 0 {
		return "none"
	}
	ssbRatio := float64(ssbCount) / float64(total)
	cwRatio := float64(cwCount) / float64(total)
	if ssbRatio >= 0.55 {
		return "ssb"
	}
	if cwRatio >= 0.55 {
		return "cw"
	}
	if total >= 3 {
		return "digital"
	}
	return "none"
}

func classifyRecommendation(status, mode string, dxRatio, spotsPerMin, confidence float64) string {
	if confidence < 20 || status == "grey" {
		return "Insufficient data"
	}
	if status == "green" && dxRatio >= 0.4 {
		if mode == "ssb" {
			return "Try DX on SSB"
		}
		if mode == "cw" {
			return "Try DX on CW"
		}
		return "Try DX"
	}
	if status == "green" || status == "yellow" {
		if spotsPerMin > 0.8 {
			return "Band usable now"
		}
		return "Monitor for openings"
	}
	return "Avoid for now"
}

func (e *DxBaselineEngine) refreshEventSpanLocked() {
	if e == nil {
		return
	}
	e.firstEventAt = 0
	e.lastEventAt = 0
	for _, ev := range e.events {
		if ev.T <= 0 {
			continue
		}
		if e.firstEventAt == 0 || ev.T < e.firstEventAt {
			e.firstEventAt = ev.T
		}
		if ev.T > e.lastEventAt {
			e.lastEventAt = ev.T
		}
	}
}

func (e *DxBaselineEngine) snapshotEventsLocked() []dxObservedEvent {
	if len(e.events) == 0 {
		return nil
	}
	if e.eventsStart <= 0 || e.eventsStart >= len(e.events) {
		out := make([]dxObservedEvent, len(e.events))
		copy(out, e.events)
		return out
	}
	out := make([]dxObservedEvent, 0, len(e.events))
	out = append(out, e.events[e.eventsStart:]...)
	out = append(out, e.events[:e.eventsStart]...)
	return out
}

func baselineHistoryMinutes(firstEventAt, lastEventAt int64, events []dxObservedEvent, now int64) int {
	if firstEventAt > 0 && lastEventAt >= firstEventAt {
		return int((lastEventAt - firstEventAt) / 60)
	}
	return baselineHistoryMinutesFromEvents(events, now)
}

func baselineHistoryMinutesFromEvents(events []dxObservedEvent, now int64) int {
	if len(events) == 0 {
		return 0
	}
	earliest := now
	latest := int64(0)
	for _, e := range events {
		if e.T <= 0 {
			continue
		}
		if e.T < earliest {
			earliest = e.T
		}
		if e.T > latest {
			latest = e.T
		}
	}
	if latest == 0 {
		return 0
	}
	if latest < earliest {
		return 0
	}
	return int((latest - earliest) / 60)
}

// compassOrder is the deterministic tiebreak order for dominantDirection. Go
// map iteration is randomised, so without an explicit tiebreak two directions
// with equal counts would win non-deterministically across calls, making the
// bearing indicator jitter. N first matches the natural compass reading.
var compassOrder = map[string]int{
	"N": 0, "NE": 1, "E": 2, "SE": 3, "S": 4, "SW": 5, "W": 6, "NW": 7,
}

func dominantDirection(bins map[string]int) string {
	best := ""
	bestN := 0
	for dir, n := range bins {
		if n > bestN || (n == bestN && n > 0 && compassOrder[dir] < compassOrder[best]) {
			best = dir
			bestN = n
		}
	}
	if best == "" {
		return "-"
	}
	return best
}

// baselineScoreQuantilesForBand returns the 25th/75th percentile score
// proxy for a band at the given slot, using a two-tier fallback:
// grid-cluster → global. Returns (q25, q75, clusterUsed, ok).
func baselineScoreQuantilesForBand(global, clusterBuckets map[string]*baselineBucket, operatorCluster string, band string, hour int) (float64, float64, bool, bool) {
	type pair struct {
		score  float64
		weight int64
	}
	pairs := make([]pair, 0, 24)
	maxCount := int64(0)

	appendBucket := func(b *baselineBucket) {
		if b == nil || b.Count <= 0 {
			return
		}
		if b.Count > maxCount {
			maxCount = b.Count
		}
	}

	collectBucket := func(b *baselineBucket) {
		if b == nil || b.Count <= 0 {
			return
		}
		distanceNorm := clamp01(float64(b.DistanceTier) / 4.0)
		snrNorm := clamp01(float64(b.SnrTier) / 3.0)
		activityNorm := 0.5
		if maxCount > 0 {
			activityNorm = clamp01(float64(b.Count) / float64(maxCount))
		}
		proxy := (0.45*distanceNorm + 0.35*activityNorm + 0.20*snrNorm) * 100.0
		pairs = append(pairs, pair{score: proxy, weight: b.Count})
	}

	// Tier 1: grid-cluster.
	clusterUsed := false
	if operatorCluster != "" {
		for d := 0; d <= 4; d++ {
			for s := 0; s <= 3; s++ {
				appendBucket(clusterBuckets[baselineClusterKey(operatorCluster, band, hour, d, s)])
			}
		}
		clusterUsed = maxCount > 0
	}

	// Tier 2: global (only if cluster had no support).
	if maxCount == 0 {
		for d := 0; d <= 4; d++ {
			for s := 0; s <= 3; s++ {
				appendBucket(global[baselineKey(band, hour, d, s)])
			}
		}
	}

	// Collect pairs from whichever tier was selected.
	if clusterUsed && operatorCluster != "" {
		for d := 0; d <= 4; d++ {
			for s := 0; s <= 3; s++ {
				collectBucket(clusterBuckets[baselineClusterKey(operatorCluster, band, hour, d, s)])
			}
		}
	} else {
		for d := 0; d <= 4; d++ {
			for s := 0; s <= 3; s++ {
				collectBucket(global[baselineKey(band, hour, d, s)])
			}
		}
	}

	if len(pairs) == 0 {
		return 0, 0, clusterUsed, false
	}

	sort.Slice(pairs, func(i, j int) bool { return pairs[i].score < pairs[j].score })
	totalWeight := int64(0)
	for _, p := range pairs {
		totalWeight += p.weight
	}
	if totalWeight < dxMinBaselineQuantileSupport {
		return 0, 0, clusterUsed, false
	}
	q25Target := float64(totalWeight) * 0.25
	q75Target := float64(totalWeight) * 0.75

	cum := int64(0)
	q25 := pairs[0].score
	q75 := pairs[len(pairs)-1].score
	for _, p := range pairs {
		cum += p.weight
		if float64(cum) >= q25Target {
			q25 = p.score
			break
		}
	}
	cum = 0
	for _, p := range pairs {
		cum += p.weight
		if float64(cum) >= q75Target {
			q75 = p.score
			break
		}
	}
	return q25, q75, clusterUsed, true
}

func extractMatchedBandEvent(m MQTTMessage, qthSet []string) (matchedBandEvent, bool) {
	band := normalizeBand(m.B)
	if band == "" {
		return matchedBandEvent{}, false
	}
	sc := strings.ToUpper(strings.TrimSpace(m.SC))
	rc := strings.ToUpper(strings.TrimSpace(m.RC))
	sl := strings.ToUpper(strings.TrimSpace(m.SL))
	rl := strings.ToUpper(strings.TrimSpace(m.RL))
	if sl == "" || rl == "" {
		return matchedBandEvent{}, false
	}

	isSender := false
	isReceiver := false
	for _, t := range qthSet {
		if matchCall(sc, t) || (isLocator(t) && strings.HasPrefix(sl, t)) {
			isSender = true
		}
		if matchCall(rc, t) || (isLocator(t) && strings.HasPrefix(rl, t)) {
			isReceiver = true
		}
	}
	if !isSender && !isReceiver {
		return matchedBandEvent{}, false
	}

	localLocator := sl
	remoteLocator := rl
	if !isSender && isReceiver {
		localLocator = rl
		remoteLocator = sl
	}
	if localLocator == "" || remoteLocator == "" {
		return matchedBandEvent{}, false
	}

	distanceKm := distanceKmForLocators(localLocator, remoteLocator)
	lat1, lon1 := locatorToLatLng(localLocator)
	lat2, lon2 := locatorToLatLng(remoteLocator)
	direction := ""
	if !(lat1 == 0 && lon1 == 0) && !(lat2 == 0 && lon2 == 0) {
		direction = bearingDirection(lat1, lon1, lat2, lon2)
	}

	dedupBucket := m.T / dxDedupWindowSeconds
	dedupKey := fmt.Sprintf("%d|%s|%s|%s|%s|%s", dedupBucket, band, sc, rc, sl, rl)
	qualityKey := band + "|" + sc + "|" + rc + "|" + remoteLocator
	remote4 := ""
	if isLocator(remoteLocator) {
		remote4 = remoteLocator[:4]
	}

	return matchedBandEvent{
		band:         band,
		remote4:      remote4,
		qualityKey:   qualityKey,
		dedupKey:     dedupKey,
		snr:          m.RP,
		distanceKm:   distanceKm,
		direction:    direction,
		tx:           sc,
		rx:           rc,
		timestampSec: m.T,
	}, true
}

// normalizeSeriesTo100 returns a copy of `series` max-scaled to the 0..100
// range (each value = value/max*100, rounded to 2 dp). A series whose max is
// <=0 is returned as all-zeros. This produces the 0..100 Sparkline shape that
// hot_bands.sustainedRecentBins (floor 30.0) and computeTrend expect, derived
// from the per-band activity series instead of the old per-event scan.
func normalizeSeriesTo100(series []float64) []float64 {
	out := make([]float64, len(series))
	maxV := 0.0
	for _, v := range series {
		if v > maxV {
			maxV = v
		}
	}
	if maxV <= 0 {
		return out
	}
	for i, v := range series {
		out[i] = round2((v / maxV) * 100.0)
	}
	return out
}

// buildBandActivityByBin bins raw observed events for one band into 12 equal
// bins spanning the selected `minutes` window and returns spots/min per bin
// (i=0 oldest). It counts raw events (no quality weighting, no 0-100
// normalization) and returns a real spots/min rate so the chart bars are
// directly comparable to the baseline line. Evaluate prefers the Postgres
// aggregate (activityByBinForTargets) and only falls back to this in-memory
// binning from `events` when there is no store or that query failed.
func buildBandActivityByBin(events []dxObservedEvent, targets []string, band string, cwMinDb int, minutes int, now int64) []float64 {
	const bins = 12
	series := make([]float64, bins)
	if minutes <= 0 {
		return series
	}
	if len(events) == 0 {
		return series
	}
	windowSec := int64(minutes) * 60
	binSec := windowSec / bins
	if binSec <= 0 {
		return series
	}
	binMinutes := float64(binSec) / 60.0
	windowStart := now - windowSec
	for _, e := range events {
		if e.B != band {
			continue
		}
		if e.T < windowStart || e.T > now {
			continue
		}
		if !eventMatchesTargets(e, targets) {
			continue
		}
		if e.RP < cwMinDb {
			continue
		}
		idx := int((e.T - windowStart) / binSec)
		if idx < 0 {
			idx = 0
		}
		if idx >= bins {
			idx = bins - 1
		}
		series[idx]++
	}
	for i := range series {
		series[i] = series[i] / binMinutes
	}
	return series
}

func eventMatchesTargets(e dxObservedEvent, targets []string) bool {
	for _, t := range targets {
		if matchCall(e.SC, t) || matchCall(e.RC, t) {
			return true
		}
		if isLocator(t) && (strings.HasPrefix(e.SL, t) || strings.HasPrefix(e.RL, t)) {
			return true
		}
	}
	return false
}

func computeTrend(series []float64) (string, float64) {
	if len(series) < 6 {
		return "stable", 0
	}
	mid := len(series) / 2
	prev := average(series[:mid])
	curr := average(series[mid:])
	delta := curr - prev
	if delta > 6 {
		return "rising", round2(delta / 10.0)
	}
	if delta < -6 {
		return "falling", round2(delta / 10.0)
	}
	return "stable", round2(delta / 10.0)
}

// baselineActivityForBand returns the expected spots/minute for a given band
// and 30-minute slot, normalised by how many days of history are in the
// baseline so the value stays comparable to the live spotsPerMin rate.
// baselineActivityForBand returns the expected spots/minute for a band at
// the given slot, using a two-tier fallback: grid-cluster → global. The
// returned bool marks whether the cluster baseline was used (false = global).
func baselineActivityForBand(global, clusterBuckets map[string]*baselineBucket, operatorCluster string, band string, hour int, historyMinutes int) (float64, bool) {
	if operatorCluster != "" {
		total := 0.0
		for d := 0; d <= 4; d++ {
			for s := 0; s <= 3; s++ {
				if b := clusterBuckets[baselineClusterKey(operatorCluster, band, hour, d, s)]; b != nil {
					total += float64(b.Count)
				}
			}
		}
		if total > 0 {
			return normalizeBaselineToSpotsPerMinute(total, historyMinutes), true
		}
	}

	total := 0.0
	for d := 0; d <= 4; d++ {
		for s := 0; s <= 3; s++ {
			if b := global[baselineKey(band, hour, d, s)]; b != nil {
				total += float64(b.Count)
			}
		}
	}
	if total == 0 {
		return 0, false
	}
	return normalizeBaselineToSpotsPerMinute(total, historyMinutes), false
}

// baselineActivityForBandAllSlots returns the expected spots/minute for a
// band at every 30-minute UTC slot (length-48 array). The accompanying
// boolean slice marks per-slot whether the grid-cluster baseline was used;
// when false for a slot, the value is from the global fallback or no data.
//
// Per-slot fallback is independent: a slot with a non-zero cluster count
// uses the cluster baseline; a slot with no cluster rows falls back to global
// for that slot only.
func baselineActivityForBandAllSlots(global, clusterBuckets map[string]*baselineBucket, operatorCluster string, band string, historyMinutes int) ([]float64, []bool) {
	rates := make([]float64, SlotsOfDay)
	usedCluster := make([]bool, SlotsOfDay)
	for slot := 0; slot < SlotsOfDay; slot++ {
		if operatorCluster != "" {
			clusterTotal := 0.0
			for d := 0; d <= 4; d++ {
				for s := 0; s <= 3; s++ {
					if b := clusterBuckets[baselineClusterKey(operatorCluster, band, slot, d, s)]; b != nil {
						clusterTotal += float64(b.Count)
					}
				}
			}
			if clusterTotal > 0 {
				rates[slot] = normalizeBaselineToSpotsPerMinute(clusterTotal, historyMinutes)
				usedCluster[slot] = true
				continue
			}
		}
		globalTotal := 0.0
		for d := 0; d <= 4; d++ {
			for s := 0; s <= 3; s++ {
				if b := global[baselineKey(band, slot, d, s)]; b != nil {
					globalTotal += float64(b.Count)
				}
			}
		}
		if globalTotal > 0 {
			rates[slot] = normalizeBaselineToSpotsPerMinute(globalTotal, historyMinutes)
		}
	}
	return rates, usedCluster
}

// normalizeBaselineToSpotsPerMinute converts a raw cumulative bucket count
// into an expected spots/minute rate. Each slot_of_day is a 30-minute window
// that repeats once per UTC day, so the denominator is historyDays × 30 min.
//
// historyMinutes <= 0 means the baseline accumulation span is unknown. In that
// case return 0 so callers treat the baseline as unavailable, rather than
// dividing by a 1-day floor — which, when the true span was actually months,
// inflated the baseline by ~that many days and both emptied the activity chart
// (baseline dwarfs the live bars) and tanked the per-band scores.
func normalizeBaselineToSpotsPerMinute(totalCount float64, historyMinutes int) float64 {
	if historyMinutes <= 0 {
		return 0
	}
	historyDays := float64(historyMinutes) / float64(24*60)
	if historyDays < 1 {
		historyDays = 1
	}
	return totalCount / (historyDays * 30.0)
}

// distanceTierBounds returns the lower/upper km bound for a distance tier.
// Tier 4 is unbounded above; we cap it at 12000 km so p90 interpolation
// produces a usable number (~ slightly past the antipodal half-circle).
func distanceTierBounds(tier int) (lo, hi float64) {
	switch tier {
	case 0:
		return 0, 500
	case 1:
		return 500, 1500
	case 2:
		return 1500, 3000
	case 3:
		return 3000, 7000
	default:
		return 7000, 12000
	}
}

// baselineP90DistanceForBand returns the tier-weighted p90 path length for a
// band+slot, summed across all SNR tiers. Falls back from target buckets to
// global. Used by the hot-bands recommender to detect DX surges: a band whose
// live p90 distance is meaningfully above its historical p90 for the same
// target and slot is open along an unusually long path.
//
// Tier counts are converted to a piecewise-uniform distribution over the
// tier's [lo, hi] bound and the 90th percentile interpolated within the tier
// where the cumulative weight crosses 0.9 of the total.
// baselineP90DistanceForBand returns the p90 distance tier interpolation for a
// band at the given slot, using a two-tier fallback: grid-cluster → global.
// Returns (km, clusterUsed).
func baselineP90DistanceForBand(global, clusterBuckets map[string]*baselineBucket, operatorCluster string, band string, hour int) (float64, bool) {
	tierCounts := [5]int64{}
	collectFromCluster := func() bool {
		any := false
		if operatorCluster == "" {
			return false
		}
		for d := 0; d <= 4; d++ {
			for s := 0; s <= 3; s++ {
				if b := clusterBuckets[baselineClusterKey(operatorCluster, band, hour, d, s)]; b != nil && b.Count > 0 {
					tierCounts[d] += b.Count
					any = true
				}
			}
		}
		return any
	}
	collectFromGlobal := func() {
		for d := 0; d <= 4; d++ {
			for s := 0; s <= 3; s++ {
				if b := global[baselineKey(band, hour, d, s)]; b != nil && b.Count > 0 {
					tierCounts[d] += b.Count
				}
			}
		}
	}

	usedCluster := collectFromCluster()
	if !usedCluster {
		collectFromGlobal()
	}
	return p90FromTierCounts(tierCounts), usedCluster
}

func p90FromTierCounts(tierCounts [5]int64) float64 {
	var total int64
	for _, c := range tierCounts {
		total += c
	}
	if total == 0 {
		return 0
	}
	q90 := 0.9 * float64(total)
	cum := int64(0)
	for d := 0; d < 5; d++ {
		c := tierCounts[d]
		if c <= 0 {
			continue
		}
		if float64(cum+c) >= q90 {
			lo, hi := distanceTierBounds(d)
			frac := (q90 - float64(cum)) / float64(c)
			if frac < 0 {
				frac = 0
			}
			if frac > 1 {
				frac = 1
			}
			return lo + frac*(hi-lo)
		}
		cum += c
	}
	// Numerically: cumulative reached 1.0 without crossing 0.9 — last
	// non-empty tier owns p90. Return its upper bound.
	for d := 4; d >= 0; d-- {
		if tierCounts[d] > 0 {
			_, hi := distanceTierBounds(d)
			return hi
		}
	}
	return 0
}

// baselineSupportForBand returns the total baseline bucket support for a band
// at the given slot, using a two-tier fallback: grid-cluster → global.
func baselineSupportForBand(global, clusterBuckets map[string]*baselineBucket, operatorCluster string, band string, hour int) int64 {
	if operatorCluster != "" {
		var support int64
		for d := 0; d <= 4; d++ {
			for s := 0; s <= 3; s++ {
				if b := clusterBuckets[baselineClusterKey(operatorCluster, band, hour, d, s)]; b != nil {
					support += b.Count
				}
			}
		}
		if support > 0 {
			return support
		}
	}
	var support int64
	for d := 0; d <= 4; d++ {
		for s := 0; s <= 3; s++ {
			if b := global[baselineKey(band, hour, d, s)]; b != nil {
				support += b.Count
			}
		}
	}
	return support
}

func utcSlotOfDay(ts int64) int {
	t := time.Unix(ts, 0).UTC()
	return t.Hour()*2 + t.Minute()/30
}

// utcDayIndex floors a unix timestamp to a day number (negative-safe).
func utcDayIndex(ts int64) int64 {
	const secPerDay = int64(24 * 60 * 60)
	if ts >= 0 {
		return ts / secPerDay
	}
	return (ts - (secPerDay - 1)) / secPerDay
}

// unknownSource4 is the sentinel for a missing/short source locator. Used by
// dx_raw_spots persistence and dx_region_baseline_daily keying — the
// baseline buckets no longer carry source4 since v6.
const unknownSource4 = "----"

// normalizeSource4 returns the first 4 characters of an upper-cased
// Maidenhead-like locator, or unknownSource4 for malformed/short inputs.
// Used by raw-spot ingest and the region baseline; NOT used by the
// baseline_global/target aggregates anymore.
func normalizeSource4(locator string) string {
	l := strings.ToUpper(strings.TrimSpace(locator))
	if len(l) < 4 {
		return unknownSource4
	}
	return l[:4]
}

func normalizeBand(raw string) string {
	b := strings.ToLower(strings.TrimSpace(raw))
	if b == "" {
		return ""
	}
	if strings.HasSuffix(b, "m") {
		return b
	}
	return b + "m"
}

// bandsInScope is the set of amateur bands HorstReporter analyses (160m–2m).
// PSKReporter / DX-cluster feeds occasionally carry microwave spots (13cm, 23cm,
// 70cm, …) whose band strings also end in "m", so normalizeBand passes them
// through. Trend/hot-band analysis must gate on this set explicitly, otherwise
// it surfaces out-of-scope bands like "13cm rising".
var bandsInScope = map[string]struct{}{
	"160m": {}, "80m": {}, "60m": {}, "40m": {}, "30m": {}, "20m": {},
	"17m": {}, "15m": {}, "12m": {}, "10m": {}, "6m": {}, "4m": {}, "2m": {},
}

// bandInScope reports whether a normalized band is one HorstReporter analyses.
func bandInScope(band string) bool {
	_, ok := bandsInScope[band]
	return ok
}

func baselineKey(band string, slotOfDay, distanceTier, snrTier int) string {
	return band + "|" + itoa(slotOfDay) + "|" + itoa(distanceTier) + "|" + itoa(snrTier)
}

// appendBaselineKey appends the same "band|slot|dist|snr" key to buf without
// allocating. The result is for map lookups only — clone before retaining
// (see observeBucket).
func appendBaselineKey(buf []byte, band string, slotOfDay, distanceTier, snrTier int) []byte {
	buf = append(buf, band...)
	buf = append(buf, '|')
	buf = strconv.AppendInt(buf, int64(slotOfDay), 10)
	buf = append(buf, '|')
	buf = strconv.AppendInt(buf, int64(distanceTier), 10)
	buf = append(buf, '|')
	return strconv.AppendInt(buf, int64(snrTier), 10)
}

// stackString reinterprets buf as a string without allocating. The result is
// valid only until buf is reused; callers must clone before retaining it
// (e.g. as a map key). buf must be non-empty.
func stackString(buf []byte) string {
	return unsafe.String(&buf[0], len(buf))
}

// baselineClusterKey keys a grid-cluster baseline bucket: the 6×6 cluster
// anchor locator (e.g. "JN68") prepended to the standard 4-dimension base
// key. This is the middle tier of the cluster → global fallback: when the
// operator's own station history is too thin to score against, the cluster
// tier gives a baseline scoped to their part of the world.
func baselineClusterKey(anchor string, band string, slotOfDay, distanceTier, snrTier int) string {
	return anchor + "|" + baselineKey(band, slotOfDay, distanceTier, snrTier)
}

func baselineClusterKeyFromBase(anchor, baseKey string) string {
	return anchor + "|" + baseKey
}

// normalizeQTHToken uppercases and trims a QTH token without collapsing
// locators to a block. It is the identity normalization for the QTH param
// (callsigns and locators pass through as-is); the cluster derivation
// (locatorClusterAnchor) is a separate step.
func normalizeQTHToken(t string) string {
	return strings.ToUpper(strings.TrimSpace(t))
}

func snrTierFromDb(db int) int {
	switch {
	case db <= -16:
		return 0
	case db <= -9:
		return 1
	case db <= -2:
		return 2
	default:
		return 3
	}
}

func distanceTierForLocators(a, b string) int {
	km := distanceKmForLocators(a, b)
	return distanceTierFromKm(km)
}

func distanceTierFromKm(km float64) int {
	switch {
	case km < 500:
		return 0
	case km < 1500:
		return 1
	case km < 3000:
		return 2
	case km < 7000:
		return 3
	default:
		return 4
	}
}

func distanceKmForLocators(a, b string) float64 {
	la1, lo1 := locatorToLatLng(strings.ToUpper(strings.TrimSpace(a)))
	la2, lo2 := locatorToLatLng(strings.ToUpper(strings.TrimSpace(b)))
	if (la1 == 0 && lo1 == 0) || (la2 == 0 && lo2 == 0) {
		return 0
	}
	return haversineKm(la1, lo1, la2, lo2)
}

func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0
	toRad := math.Pi / 180.0
	dLat := (lat2 - lat1) * toRad
	dLon := (lon2 - lon1) * toRad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*toRad)*math.Cos(lat2*toRad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return r * c
}

func bearingDirection(lat1, lon1, lat2, lon2 float64) string {
	deg := bearingDegrees(lat1, lon1, lat2, lon2)
	if deg < 0 {
		deg += 360
	}
	sectors := []string{"N", "NE", "E", "SE", "S", "SW", "W", "NW"}
	idx := int(math.Round(deg/45.0)) % 8
	return sectors[idx]
}

func bearingDegrees(lat1, lon1, lat2, lon2 float64) float64 {
	toRad := math.Pi / 180.0
	phi1 := lat1 * toRad
	phi2 := lat2 * toRad
	lambda1 := lon1 * toRad
	lambda2 := lon2 * toRad
	y := math.Sin(lambda2-lambda1) * math.Cos(phi2)
	x := math.Cos(phi1)*math.Sin(phi2) - math.Sin(phi1)*math.Cos(phi2)*math.Cos(lambda2-lambda1)
	brng := math.Atan2(y, x) * (180.0 / math.Pi)
	for brng < 0 {
		brng += 360
	}
	for brng >= 360 {
		brng -= 360
	}
	return brng
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round2(v float64) float64 { return math.Round(v*100) / 100 }

func average(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func percentileFloat(in []float64, p float64) float64 {
	if len(in) == 0 {
		return 0
	}
	cp := append([]float64(nil), in...)
	sort.Float64s(cp)
	if p <= 0 {
		return cp[0]
	}
	if p >= 1 {
		return cp[len(cp)-1]
	}
	idx := int(math.Ceil(float64(len(cp))*p)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return cp[idx]
}

func percentileInt(in []int, p float64) float64 {
	if len(in) == 0 {
		return 0
	}
	cp := append([]int(nil), in...)
	sort.Ints(cp)
	if p <= 0 {
		return float64(cp[0])
	}
	if p >= 1 {
		return float64(cp[len(cp)-1])
	}
	idx := int(math.Ceil(float64(len(cp))*p)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return float64(cp[idx])
}

func maxFloat(in []float64) float64 {
	if len(in) == 0 {
		return 0
	}
	m := in[0]
	for _, v := range in[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func itoa(v int) string {
	return strconvItoa(v)
}

func strconvItoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := false
	if v < 0 {
		neg = true
		v = -v
	}
	buf := [20]byte{}
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + (v % 10))
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
