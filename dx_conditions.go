package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultDxWindowMinutes       = 20
	defaultDxCwViableMinDb       = -15
	maxDxWindowMinutes           = 180
	defaultDxBaselineMaxEvents   = 1000000
	dxSparklineBins              = 12
	dxSparklineBinSeconds        = 10 * 60
	dxLongHaulThresholdKm        = 3000.0
	dxDedupWindowSeconds         = 30
	dxMinBaselineQuantileSupport = 200
)

var dxBaselineMaxEvents = defaultDxBaselineMaxEvents

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
	Version       int                        `json:"version"`
	SavedAt       int64                      `json:"saved_at"`
	FirstEventAt  int64                      `json:"first_event_at,omitempty"`
	LastEventAt   int64                      `json:"last_event_at,omitempty"`
	Buckets       map[string]*baselineBucket `json:"buckets"`
	TargetBuckets map[string]*baselineBucket `json:"target_buckets,omitempty"`
	Events        []dxObservedEvent          `json:"events,omitempty"`
}

type DxBaselineEngine struct {
	mu            sync.RWMutex
	path          string
	store         *dxPostgresStore
	buckets       map[string]*baselineBucket
	targetBuckets map[string]*baselineBucket
	firstEventAt  int64
	lastEventAt   int64
	eventsStart   int
	events        []dxObservedEvent
}

type dxBandCondition struct {
	Band               string         `json:"band"`
	Score              float64        `json:"score"`
	Confidence         float64        `json:"confidence"`
	Status             string         `json:"status"`
	Condition          string         `json:"condition"`
	Mode               string         `json:"mode"`
	Recommendation     string         `json:"recommendation"`
	CurrentLinks       int            `json:"current_links"`
	UniqueLinks        int            `json:"unique_links"`
	RepeatRatio        float64        `json:"repeat_ratio"`
	SpotsPerMinute     float64        `json:"spots_per_minute"`
	UniqueTxStations   int            `json:"unique_tx_stations"`
	UniqueRxStations   int            `json:"unique_rx_stations"`
	UniqueRemoteGrids  int            `json:"unique_remote_grids"`
	AvgDistanceKm      float64        `json:"avg_distance_km"`
	MaxDistanceKm      float64        `json:"max_distance_km"`
	MedianDistanceKm   float64        `json:"median_distance_km"`
	P90DistanceKm      float64        `json:"p90_distance_km"`
	LongHaulRatio      float64        `json:"long_haul_ratio"`
	DxRatio            float64        `json:"dx_ratio"`
	AvgSnr             float64        `json:"avg_snr"`
	PeakSnr            int            `json:"peak_snr"`
	MedianSnr          float64        `json:"median_snr"`
	P90Snr             float64        `json:"p90_snr"`
	BaselineActivity         float64    `json:"baseline_activity"`
	TargetBaselineUsed       bool       `json:"target_baseline_used"`
	BaselineActivityBySlot   []float64  `json:"baseline_activity_by_slot,omitempty"`
	BaselineSlotUsedByTarget []bool     `json:"baseline_slot_used_by_target,omitempty"`
	DominantDirection  string         `json:"dominant_direction"`
	AzimuthSectors     map[string]int `json:"azimuth_sectors,omitempty"`
	Trend              string         `json:"trend"`
	TrendDelta         float64        `json:"trend_delta"`
	Sparkline          []float64      `json:"sparkline"`
}

type dxConditionsResponse struct {
	Target           string            `json:"target"`
	Surroundings     bool              `json:"surroundings"`
	WindowMinutes    int               `json:"window_minutes"`
	CwMinDb          int               `json:"cw_min_db"`
	CurrentSlotOfDay int               `json:"current_slot_of_day"`
	GeneratedAt      int64             `json:"generated_at"`
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
		path:          strings.TrimSpace(path),
		buckets:       make(map[string]*baselineBucket),
		targetBuckets: make(map[string]*baselineBucket),
		events:        make([]dxObservedEvent, 0, initialCap),
	}
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
	e.mu.Lock()
	e.store = st
	e.mu.Unlock()
	return nil
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

func (e *DxBaselineEngine) LoadDxPulseBaseline(targets []string, lookbackDays int, windowMinutes int, now int64) (map[string]*dxPulseBaselineAccumulator, bool, error) {
	e.mu.RLock()
	st := e.store
	e.mu.RUnlock()
	if st == nil {
		return map[string]*dxPulseBaselineAccumulator{}, false, nil
	}
	return st.dxPulseBaselineForTargets(targets, lookbackDays, windowMinutes, now)
}

func (e *DxBaselineEngine) NumBuckets() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.buckets) + len(e.targetBuckets)
}

func (e *DxBaselineEngine) Stats(now int64) (bucketCount, eventCount, historyMinutes int) {
	if e == nil {
		return 0, 0, 0
	}

	e.mu.RLock()
	st := e.store
	if st == nil {
		bucketCount = len(e.buckets) + len(e.targetBuckets)
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

	baseKey := baselineKey(band, hour, distTier, snrTier)
	e.observeBucket(e.buckets, baseKey, band, hour, distTier, snrTier)

	targetTokens := [4]string{
		normalizeTargetTokenUpper(sc),
		normalizeTargetTokenUpper(rc),
		normalizeTargetTokenUpper(sl),
		normalizeTargetTokenUpper(rl),
	}
	for i := range targetTokens {
		t := targetTokens[i]
		if t == "" {
			continue
		}
		duplicate := false
		for j := 0; j < i; j++ {
			if targetTokens[j] == t {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		e.observeBucket(e.targetBuckets, baselineTargetKeyFromBase(t, baseKey), band, hour, distTier, snrTier)
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
		_ = st.observe(m, band, hour, distTier, snrTier, targetTokens)
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
}

func (e *DxBaselineEngine) observeBucket(store map[string]*baselineBucket, key, band string, hour, distTier, snrTier int) {
	b := store[key]
	if b == nil {
		b = &baselineBucket{Band: band, SlotOfDay: hour, DistanceTier: distTier, SnrTier: snrTier}
		store[key] = b
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

	// Any snapshot before v6 had source4 in the key (and used full 4-char
	// locator target tokens). remapBucketsForLoad / remapTargetBucketsForLoad
	// strip source4 and collapse to the v6 key shape; the target variant also
	// runs the locator-block collapse so JO32/JO33 from older saves get
	// summed into JO22.
	legacy := snap.Version < 6
	e.buckets = remapBucketsForLoad(snap.Buckets, legacy)
	e.targetBuckets = remapTargetBucketsForLoad(snap.TargetBuckets, legacy)

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
		Version:       6,
		SavedAt:       time.Now().Unix(),
		FirstEventAt:  e.firstEventAt,
		LastEventAt:   e.lastEventAt,
		Buckets:       cloneBuckets(e.buckets),
		TargetBuckets: cloneBuckets(e.targetBuckets),
		Events:        e.snapshotEventsLocked(),
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

// remapTargetBucketsForLoad mirrors remapBucketsForLoad for target buckets.
// In addition to stripping source4, legacy snapshots may carry full
// 4-character locator tokens (e.g. JO32); these are collapsed to their 2×2
// block (JO22) via normalizeTargetTokenUpper so the on-disk format matches
// the new in-memory and DB shape.
func remapTargetBucketsForLoad(src map[string]*baselineBucket, legacy bool) map[string]*baselineBucket {
	out := make(map[string]*baselineBucket, len(src))
	for k, v := range src {
		if v == nil {
			continue
		}
		// Extract the token prefix (everything before the first '|') from
		// either legacy "<TOKEN>|<band>|<slot>|<dist>|<snr>|<source4>" or
		// v6 "<TOKEN>|<band>|<slot>|<dist>|<snr>" key shapes.
		i := strings.IndexByte(k, '|')
		token := ""
		if i > 0 {
			token = k[:i]
		}
		token = normalizeTargetTokenUpper(token)
		if token == "" {
			continue
		}
		newKey := baselineTargetKeyFromBase(token, baselineKey(v.Band, v.SlotOfDay, v.DistanceTier, v.SnrTier))
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

func (e *DxBaselineEngine) Evaluate(target string, surroundings bool, minutes int, cwMinDb int, history []MQTTMessage, now int64) dxConditionsResponse {
	target = normalizeTargetToken(target)
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
		Target:           target,
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

	if target == "" {
		return resp
	}

	targets := []string{target}
	if surroundings && isLocator(target) {
		targets = getSurroundingSquares(target)
	}

	// baselineTargets is the deduped, block-normalised version of `targets`.
	// Baseline storage (in-memory and Postgres) keys locator tokens by their
	// 2×2 block anchor (JO32→JO22), so a literal lookup with the raw
	// surrounding squares would miss every block whose anchor wasn't itself
	// in the surroundings list. Live-spot matching keeps `targets` because
	// prefix-matching against 6-char locators needs the user-facing squares.
	baselineTargets := normalizeTargetsForBaseline(targets)

	e.mu.RLock()
	st := e.store
	globalBuckets := cloneBuckets(e.buckets)
	targetBuckets := cloneBuckets(e.targetBuckets)
	events := e.snapshotEventsLocked()
	e.mu.RUnlock()

	var aggGlobalBuckets map[string]*baselineBucket
	var aggTargetBuckets map[string]*baselineBucket
	if st == nil {
		// v6: keys already exclude source4, so the maps are usable directly.
		aggGlobalBuckets = globalBuckets
		aggTargetBuckets = targetBuckets
		resp.BaselineBuckets = len(globalBuckets) + len(targetBuckets)
		resp.BaselineEventCnt = len(events)
		resp.BaselineHistoryM = baselineHistoryMinutes(e.firstEventAt, e.lastEventAt, events, now)
	} else {
		if b, ev, hm, err := st.baselineStats(now); err == nil {
			resp.BaselineBuckets = b
			resp.BaselineEventCnt = ev
			resp.BaselineHistoryM = hm
		}
		if recent, err := st.recentEvents(now); err == nil {
			events = recent
		}
	}

	cutoff := now - int64(minutes*60)
	bandAcc := make(map[string]*bandAccumulator)
	dedupSeen := make(map[string]struct{})

	for _, m := range history {
		if m.T < cutoff || m.T > now {
			continue
		}
		ev, matched := extractMatchedBandEvent(m, targets)
		if !matched {
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
		targetBaselineUsed := false
		baselineSupport := int64(0)
		q25, q75, quantileOK := 0.0, 0.0, false
		var baselineActivityBySlot []float64
		var baselineSlotUsedByTarget []bool
		if st == nil {
			baselineActivity, targetBaselineUsed = baselineActivityForBand(aggGlobalBuckets, aggTargetBuckets, baselineTargets, band, resp.CurrentSlotOfDay, resp.BaselineHistoryM)
			baselineSupport = baselineSupportForBand(aggGlobalBuckets, aggTargetBuckets, baselineTargets, band, resp.CurrentSlotOfDay)
			q25, q75, quantileOK = baselineScoreQuantilesForBand(aggGlobalBuckets, aggTargetBuckets, baselineTargets, band, resp.CurrentSlotOfDay)
			baselineActivityBySlot, baselineSlotUsedByTarget = baselineActivityForBandAllSlots(aggGlobalBuckets, aggTargetBuckets, baselineTargets, band, resp.BaselineHistoryM)
		} else {
			if act, used, err := st.baselineActivityForBand(baselineTargets, band, resp.CurrentSlotOfDay, resp.BaselineHistoryM); err == nil {
				baselineActivity = act
				targetBaselineUsed = used
			}
			if support, err := st.baselineSupportForBand(baselineTargets, band, resp.CurrentSlotOfDay); err == nil {
				baselineSupport = support
			}
			if ql, qh, ok, err := st.baselineQuantilesForBand(baselineTargets, band, resp.CurrentSlotOfDay); err == nil {
				q25, q75, quantileOK = ql, qh, ok
			}
			if rates, used, err := st.baselineActivityForBandAllSlots(baselineTargets, band, resp.BaselineHistoryM); err == nil {
				baselineActivityBySlot = rates
				baselineSlotUsedByTarget = used
			}
		}

		historicalBandSeries := buildBandSparkline(events, targets, band, cwMinDb, now)
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
			Band:               band,
			Score:              round1(bandScore),
			Confidence:         round1(bandConfidence),
			Status:             status,
			Condition:          condition,
			Mode:               mode,
			Recommendation:     recommendation,
			CurrentLinks:       acc.total,
			UniqueLinks:        uniqueCount,
			RepeatRatio:        round2(repeatRatio),
			SpotsPerMinute:     round2(spotsPerMin),
			UniqueTxStations:   len(acc.uniqueTx),
			UniqueRxStations:   len(acc.uniqueRx),
			UniqueRemoteGrids:  len(acc.uniqueRemote),
			AvgDistanceKm:      round1(avgDistance),
			MaxDistanceKm:      round1(maxDistance),
			MedianDistanceKm:   round1(medianDistance),
			P90DistanceKm:      round1(p90Distance),
			LongHaulRatio:      round2(longHaulRatio),
			DxRatio:            round2(dxRatio),
			AvgSnr:             round1(avgSnr),
			PeakSnr:            acc.peakSnr,
			MedianSnr:          round1(medianSnr),
			P90Snr:             round1(p90Snr),
			BaselineActivity:         round2(baselineActivity),
			TargetBaselineUsed:       targetBaselineUsed,
			BaselineActivityBySlot:   roundSlotRates(baselineActivityBySlot),
			BaselineSlotUsedByTarget: baselineSlotUsedByTarget,
			DominantDirection:        direction,
			AzimuthSectors:     acc.directionBins,
			Trend:              trend,
			TrendDelta:         trendDelta,
			Sparkline:          historicalBandSeries,
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

func roundSlotRates(rates []float64) []float64 {
	if rates == nil {
		return nil
	}
	out := make([]float64, len(rates))
	for i, v := range rates {
		out[i] = round2(v)
	}
	return out
}

func activityScoreNorm(spotsPerMin, baselineActivity float64) float64 {
	if baselineActivity <= 0 {
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

func dominantDirection(bins map[string]int) string {
	best := ""
	bestN := 0
	for dir, n := range bins {
		if n > bestN {
			best = dir
			bestN = n
		}
	}
	if best == "" {
		return "-"
	}
	return best
}

func baselineScoreQuantilesForBand(global, targetBuckets map[string]*baselineBucket, targets []string, band string, hour int) (float64, float64, bool) {
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

	for _, t := range targets {
		for d := 0; d <= 4; d++ {
			for s := 0; s <= 3; s++ {
				appendBucket(targetBuckets[baselineTargetKey(t, band, hour, d, s)])
			}
		}
	}
	if maxCount == 0 {
		for d := 0; d <= 4; d++ {
			for s := 0; s <= 3; s++ {
				appendBucket(global[baselineKey(band, hour, d, s)])
			}
		}
	}

	for _, t := range targets {
		for d := 0; d <= 4; d++ {
			for s := 0; s <= 3; s++ {
				collectBucket(targetBuckets[baselineTargetKey(t, band, hour, d, s)])
			}
		}
	}
	if len(pairs) == 0 {
		for d := 0; d <= 4; d++ {
			for s := 0; s <= 3; s++ {
				collectBucket(global[baselineKey(band, hour, d, s)])
			}
		}
	}

	if len(pairs) == 0 {
		return 0, 0, false
	}

	sort.Slice(pairs, func(i, j int) bool { return pairs[i].score < pairs[j].score })
	totalWeight := int64(0)
	for _, p := range pairs {
		totalWeight += p.weight
	}
	if totalWeight < dxMinBaselineQuantileSupport {
		return 0, 0, false
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
	return q25, q75, true
}

func extractMatchedBandEvent(m MQTTMessage, targets []string) (matchedBandEvent, bool) {
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
	for _, t := range targets {
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

func buildBandSparkline(events []dxObservedEvent, targets []string, band string, cwMinDb int, now int64) []float64 {
	series := make([]float64, dxSparklineBins)
	if len(events) == 0 {
		return series
	}
	windowStart := now - int64(dxSparklineBins*dxSparklineBinSeconds)
	for _, e := range events {
		if e.T < windowStart || e.T > now {
			continue
		}
		if e.B != band {
			continue
		}
		if !eventMatchesTargets(e, targets) {
			continue
		}
		if e.RP < cwMinDb {
			continue
		}
		idx := int((e.T - windowStart) / dxSparklineBinSeconds)
		if idx < 0 {
			idx = 0
		}
		if idx >= dxSparklineBins {
			idx = dxSparklineBins - 1
		}
		series[idx] += eventQualityWeight(e)
	}

	maxV := 0.0
	for _, v := range series {
		if v > maxV {
			maxV = v
		}
	}
	if maxV > 0 {
		for i := range series {
			series[i] = round2((series[i] / maxV) * 100.0)
		}
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

func eventQualityWeight(e dxObservedEvent) float64 {
	d := distanceKmForLocators(e.SL, e.RL)
	s := float64(snrTierFromDb(e.RP))
	return 1.0 + clamp01(d/6000.0) + (s * 0.25)
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
func baselineActivityForBand(global, targetBuckets map[string]*baselineBucket, targets []string, band string, hour int, historyMinutes int) (float64, bool) {
	total := 0.0
	for _, t := range targets {
		for d := 0; d <= 4; d++ {
			for s := 0; s <= 3; s++ {
				if b := targetBuckets[baselineTargetKey(t, band, hour, d, s)]; b != nil {
					total += float64(b.Count)
				}
			}
		}
	}
	if total > 0 {
		return normalizeBaselineToSpotsPerMinute(total, historyMinutes), true
	}

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

// baselineActivityForBandAllSlots returns the expected spots/minute for a band
// at every 30-minute UTC slot (length-48 array). The accompanying boolean
// slice marks per-slot whether the target-specific baseline was used; when
// false for a slot, the value is from the global fallback or no data exists.
//
// Per-slot fallback is independent: a slot with a non-zero target count uses
// the target baseline; a slot with no target rows falls back to global for
// that slot only. The chart needs to know per slot which kind of baseline
// it's looking at.
func baselineActivityForBandAllSlots(global, targetBuckets map[string]*baselineBucket, targets []string, band string, historyMinutes int) ([]float64, []bool) {
	rates := make([]float64, SlotsOfDay)
	used := make([]bool, SlotsOfDay)
	for slot := 0; slot < SlotsOfDay; slot++ {
		targetTotal := 0.0
		for _, t := range targets {
			for d := 0; d <= 4; d++ {
				for s := 0; s <= 3; s++ {
					if b := targetBuckets[baselineTargetKey(t, band, slot, d, s)]; b != nil {
						targetTotal += float64(b.Count)
					}
				}
			}
		}
		if targetTotal > 0 {
			rates[slot] = normalizeBaselineToSpotsPerMinute(targetTotal, historyMinutes)
			used[slot] = true
			continue
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
	return rates, used
}

// normalizeBaselineToSpotsPerMinute converts a raw cumulative bucket count
// into an expected spots/minute rate. Each slot_of_day is a 30-minute window
// that repeats once per UTC day, so the denominator is historyDays × 30 min.
func normalizeBaselineToSpotsPerMinute(totalCount float64, historyMinutes int) float64 {
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
func baselineP90DistanceForBand(global, targetBuckets map[string]*baselineBucket, targets []string, band string, hour int) (float64, bool) {
	tierCounts := [5]int64{}
	collectFromTarget := func() bool {
		any := false
		for _, t := range targets {
			for d := 0; d <= 4; d++ {
				for s := 0; s <= 3; s++ {
					if b := targetBuckets[baselineTargetKey(t, band, hour, d, s)]; b != nil && b.Count > 0 {
						tierCounts[d] += b.Count
						any = true
					}
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

	used := collectFromTarget()
	if !used {
		collectFromGlobal()
	}
	return p90FromTierCounts(tierCounts), used
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

func baselineSupportForBand(global, targetBuckets map[string]*baselineBucket, targets []string, band string, hour int) int64 {
	var support int64
	for _, t := range targets {
		for d := 0; d <= 4; d++ {
			for s := 0; s <= 3; s++ {
				if b := targetBuckets[baselineTargetKey(t, band, hour, d, s)]; b != nil {
					support += b.Count
				}
			}
		}
	}
	if support > 0 {
		return support
	}
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

func baselineKey(band string, slotOfDay, distanceTier, snrTier int) string {
	return band + "|" + itoa(slotOfDay) + "|" + itoa(distanceTier) + "|" + itoa(snrTier)
}

func baselineTargetKey(target, band string, slotOfDay, distanceTier, snrTier int) string {
	return normalizeTargetToken(target) + "|" + baselineKey(band, slotOfDay, distanceTier, snrTier)
}

func baselineTargetKeyFromBase(target, baseKey string) string {
	return target + "|" + baseKey
}

func normalizeTargetToken(t string) string {
	t = strings.ToUpper(strings.TrimSpace(t))
	return normalizeTargetTokenUpper(t)
}

// normalizeTargetTokenUpper canonicalises a target token (callsign or
// Maidenhead locator). Callsigns pass through unchanged. 4+ char locators
// are collapsed to a 2×2 block by flooring each grid-square digit to its
// nearest even value, so JO32/JO33/JO42/JO43 all map to JO22/JO22/JO42/JO42
// — matching the storage shape that drops source4 + the 2×2 collapse.
// Inputs shorter than 4 chars or with invalid digits pass through.
func normalizeTargetTokenUpper(t string) string {
	if t == "" {
		return ""
	}
	if isLocator(t) && len(t) >= 4 {
		return locatorBlockToken(t)
	}
	return t
}

// normalizeTargetsForBaseline maps a list of target tokens through
// normalizeTargetTokenUpper (which collapses 4-char locators to their 2×2
// block anchor) and dedupes the result. Used to convert a user-facing
// surroundings list like [JO21, JO22, JO23, JO31, JO32, JO33, JO41, JO42,
// JO43] into the block-anchor set [JO20, JO22, JO40, JO42] before looking
// up keys in the block-keyed baseline store.
//
// Callsign tokens pass through unchanged; locator tokens collapse.
// Input is expected to already be upper-cased.
func normalizeTargetsForBaseline(targets []string) []string {
	if len(targets) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(targets))
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		n := normalizeTargetTokenUpper(strings.ToUpper(strings.TrimSpace(t)))
		if n == "" {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}

// locatorBlockToken returns the 2×2-block anchor for a 4-char Maidenhead
// locator: keep the field (chars 0..1), and floor each grid-square digit
// (chars 2..3) to the nearest even number. Examples:
//
//	JO22 -> JO22, JO33 -> JO22, JO43 -> JO42, JO89 -> JO88.
//
// Inputs that don't match the [A-R][A-R][0-9][0-9] pattern in the first
// 4 chars are returned as the upper-cased prefix unchanged.
func locatorBlockToken(loc string) string {
	if len(loc) < 4 {
		return loc
	}
	d3 := loc[2]
	d4 := loc[3]
	if d3 < '0' || d3 > '9' || d4 < '0' || d4 > '9' {
		return loc[:4]
	}
	// Floor each odd digit to the previous even by clearing the low bit.
	return loc[:2] + string([]byte{(d3 - '0') &^ 1 + '0'}) + string([]byte{(d4 - '0') &^ 1 + '0'})
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
