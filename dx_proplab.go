package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// dx_proplab.go wires the Propagation Lab (Ladder + Fusion engines) into the
// live ingest stream and provides the service layer used by the /api/proplab/v1
// endpoints. It is intentionally kept separate from the engine logic so the
// pure classifiers remain easy to unit-test.

var proplabService *ProplabService

// ProplabService orchestrates the two Propagation Lab engines: Ladder (variant B,
// empirical propagation) and Fusion (variant C, conditional-quantile + event/SW
// fusion). It observes every spot that passes through the DxBaselineEngine, closes
// and persists 15-minute midpoint-cell buckets, refreshes space-weather/event
// context, and answers verdict requests for the UI.
type ProplabService struct {
	mu            sync.RWMutex
	ladder        *ladderEngine
	fusion        *fusionEngine
	store         *dxPostgresStore
	disabled      bool
	retentionDays int

	paramsB proplabParamsB
	paramsC proplabParamsC

	sw     fusionSWSnapshot
	events []fusionEvent

	dedup             map[string]int64
	lastPrune         time.Time
	stopCh            chan struct{}
	wg                sync.WaitGroup
	started           bool
	recomputeInterval time.Duration
}

// newProplabService builds the service. Even when disabled it is safe to call
// Observe (it becomes a no-op). The store pointer is taken from the baseline
// engine because the propagation lab shares the same Postgres connection.
func newProplabService(baseline *DxBaselineEngine, disabled bool, retentionDays int) *ProplabService {
	s := &ProplabService{
		ladder:            newLadderEngine(),
		fusion:            newFusionEngine(),
		disabled:          disabled,
		retentionDays:     retentionDays,
		paramsB:           defaultProplabParamsB(),
		paramsC:           defaultProplabParamsC(),
		dedup:             make(map[string]int64),
		stopCh:            make(chan struct{}),
		recomputeInterval: 60 * time.Second,
	}

	if baseline != nil {
		s.store = baseline.store
	}

	if !disabled && s.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := s.store.ensureProplabCellBuckets(ctx); err != nil {
			logInfo("Proplab cell-bucket backfill failed: %v", err)
		}
	}

	return s
}

// Start launches the background recompute / persistence / retention loop.
func (s *ProplabService) Start() {
	if s == nil || s.disabled || s.started {
		return
	}
	s.started = true
	s.wg.Add(1)
	go s.loop()
}

// Stop shuts down background goroutines. Main does not currently call this in
// development, but it is useful for tests.
func (s *ProplabService) Stop() {
	if s == nil || s.stopCh == nil {
		return
	}
	close(s.stopCh)
	s.wg.Wait()
}

// Observe ingests one spot into the Ladder engine. It is safe to call from the
// MQTT, DX-cluster and RBN ingest paths and is deduplicated so a spot that is
// both observed and persisted (e.g. DX-cluster) is only counted once.
func (s *ProplabService) Observe(m MQTTMessage) {
	if s == nil || s.disabled {
		return
	}

	band := normalizeBand(m.B)
	if band == "" || !bandInScope(band) {
		return
	}
	if !isLocator(m.SL) || !isLocator(m.RL) || len(m.SL) < 4 || len(m.RL) < 4 {
		return
	}

	lane := proplabLaneForSourceType(sourceTypeForMessage(m))
	sc := strings.ToUpper(strings.TrimSpace(m.SC))
	rc := strings.ToUpper(strings.TrimSpace(m.RC))
	bucketStart := alignBucketStart(m.T)

	// Dedup within a short window. The same DX-cluster spot is observed both via
	// DxBaselineEngine.Observe and PersistRawSpot; this key prevents double
	// counting without requiring invasive changes to the ingest paths.
	key := fmt.Sprintf("%s|%s|%s|%s|%d", band, lane, sc, rc, bucketStart)
	now := time.Now().Unix()

	s.mu.Lock()
	if last, ok := s.dedup[key]; ok && now-last < 5 {
		s.mu.Unlock()
		return
	}
	s.dedup[key] = now
	s.mu.Unlock()

	s.ladder.Observe(m)
}

// Backfill feeds a batch of recovered spots into the Ladder engine at startup.
// It is intentionally not deduplicated against the live stream; callers should
// only pass spots that are not already being ingested live.
func (s *ProplabService) Backfill(spots []MQTTMessage) {
	if s == nil || s.disabled {
		return
	}
	for _, m := range spots {
		s.ladder.Observe(m)
	}
}

// LadderVerdict returns the variant-B result for the supplied target and
// surroundings. A nil params pointer uses the service default.
func (s *ProplabService) LadderVerdict(target string, surroundings bool, params *proplabParamsB) ladderVerdict {
	p := s.paramsB
	if params != nil {
		p = *params
	}
	now := time.Now().Unix()
	history := s.recentHistoryCopy()
	return s.ladder.Verdict(target, surroundings, history, nil, nil, p, now)
}

// FusionVerdict returns the variant-C result. A nil params pointer uses the
// service default. Live counts are taken from the Ladder engine's in-memory
// window; historical baseline and event/SW context are refreshed from Postgres.
func (s *ProplabService) FusionVerdict(params *proplabParamsC) fusionVerdict {
	p := s.paramsC
	if params != nil {
		p = *params
	}
	now := time.Now().Unix()

	s.mu.RLock()
	sw := s.sw
	events := s.events
	s.mu.RUnlock()

	windowStart := alignBucketStart(now - 20*60)
	live := s.ladder.RegionCounts(windowStart)

	var baselineRows []proplabBaselineDayRow
	if len(live) > 0 && s.store != nil {
		bands, regions := liveBandsRegions(live)
		slot := utcSlotOfDay(now)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		var err error
		baselineRows, err = s.store.loadProplabCellBaseline(ctx, bands, regions, slot, p.LookbackDays, now)
		cancel()
		if err != nil {
			logInfo("Proplab fusion baseline load failed: %v", err)
		}
	}

	return s.fusion.Verdict(live, baselineRows, events, sw, nil, p, now)
}

// SetParamsB replaces the default Ladder parameters.
func (s *ProplabService) SetParamsB(p proplabParamsB) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.paramsB = p
	s.mu.Unlock()
}

// SetParamsC replaces the default Fusion parameters.
func (s *ProplabService) SetParamsC(p proplabParamsC) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.paramsC = p
	s.mu.Unlock()
}

func (s *ProplabService) loop() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.recomputeInterval)
	defer ticker.Stop()

	// Run once immediately so the first UI load has data.
	s.recompute()

	for {
		select {
		case <-ticker.C:
			s.recompute()
		case <-s.stopCh:
			return
		}
	}
}

func (s *ProplabService) recompute() {
	now := time.Now().Unix()
	cutoff := alignBucketStart(now - proplabBucketSeconds)
	rows := s.ladder.CloseBuckets(cutoff)
	if len(rows) > 0 && s.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := s.store.upsertProplabCellBuckets(ctx, rows)
		cancel()
		if err != nil {
			logInfo("Proplab bucket persistence failed (rows=%d): %v", len(rows), err)
		}
	}

	s.loadContext()
	s.cleanupDedup()

	if s.retentionDays > 0 && time.Since(s.lastPrune) > 1*time.Hour {
		cutoff := now - int64(s.retentionDays*24*60*60)
		if s.store != nil {
			n, err := s.store.pruneProplabOlderThan(cutoff)
			if err != nil {
				logInfo("Proplab retention prune failed: %v", err)
			} else if n > 0 {
				logInfo("Proplab retention pruned %d rows older than %d days", n, s.retentionDays)
			}
		}
		s.lastPrune = time.Now()
	}
}

func (s *ProplabService) loadContext() {
	if s.store == nil {
		s.mu.Lock()
		s.sw = fusionSWSnapshot{}
		s.events = nil
		s.mu.Unlock()
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	latestSW, err := s.store.latestProplabSW(ctx)
	if err != nil {
		logInfo("Proplab SW load failed: %v", err)
	}

	activeRows, err := s.store.activeProplabEvents(ctx, time.Now().Unix(), 24*60*60)
	if err != nil {
		logInfo("Proplab events load failed: %v", err)
	}

	s.mu.Lock()
	s.sw = buildFusionSW(latestSW)
	s.events = make([]fusionEvent, 0, len(activeRows))
	for _, r := range activeRows {
		endsIn := int((r.EndUTC - time.Now().Unix()) / 60)
		if endsIn < 0 {
			endsIn = 0
		}
		s.events = append(s.events, fusionEvent{
			Source:    r.Source,
			Title:     r.Title,
			BandMask:  r.BandMask,
			Locator4:  r.Locator4,
			Region:    string(dxPulseRegionForLocator(r.Locator4)),
			EndsInMin: endsIn,
		})
	}
	s.mu.Unlock()
}

func buildFusionSW(latest map[string]proplabSWRow) fusionSWSnapshot {
	if len(latest) == 0 {
		return fusionSWSnapshot{}
	}
	sw := fusionSWSnapshot{Available: true}
	for series, r := range latest {
		switch series {
		case "kp":
			sw.Kp = r.Value
		case "sfi":
			sw.SFI = r.Value
		case "xray":
			// Stored as a numeric proxy; real parsing deferred to ingest layer.
		case "aurora":
			sw.AuroraGW = r.Value
		}
		if r.ObsTime > sw.FetchedAt {
			sw.FetchedAt = r.ObsTime
		}
	}
	return sw
}

func (s *ProplabService) cleanupDedup() {
	now := time.Now().Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, last := range s.dedup {
		if now-last > 60 {
			delete(s.dedup, k)
		}
	}
}

func (s *ProplabService) recentHistoryCopy() []MQTTMessage {
	hub.Lock()
	defer hub.Unlock()
	out := make([]MQTTMessage, len(hub.history))
	copy(out, hub.history)
	return out
}

func liveBandsRegions(live []fusionBandCount) (bands, regions []string) {
	bandSet := make(map[string]struct{})
	regionSet := make(map[string]struct{})
	for _, c := range live {
		bandSet[c.Band] = struct{}{}
		regionSet[c.Region] = struct{}{}
	}
	bands = make([]string, 0, len(bandSet))
	regions = make([]string, 0, len(regionSet))
	for b := range bandSet {
		bands = append(bands, b)
	}
	for r := range regionSet {
		regions = append(regions, r)
	}
	sort.Strings(bands)
	sort.Strings(regions)
	return bands, regions
}

// RegionCounts returns per-(band,region) aggregates from the in-memory buckets
// whose start is at or after windowStart. It is used by the Fusion engine to
// turn the Ladder's cell-lane buckets into the band-region live counts the
// quantile baseline expects.
func (e *ladderEngine) RegionCounts(windowStart int64) []fusionBandCount {
	e.mu.Lock()
	defer e.mu.Unlock()

	type agg struct {
		spotCount int
		links     map[string]struct{}
		reporters map[string]struct{}
		distMax   int
	}

	counts := make(map[string]*agg)
	for k, b := range e.buckets {
		if k.BucketStart < windowStart {
			continue
		}
		key := k.Band + "|" + b.region
		a := counts[key]
		if a == nil {
			a = &agg{
				links:     make(map[string]struct{}),
				reporters: make(map[string]struct{}),
			}
			counts[key] = a
		}
		a.spotCount += b.spotCount
		for link := range b.links {
			a.links[link] = struct{}{}
		}
		for rep := range b.reporters {
			a.reporters[rep] = struct{}{}
		}
		d := int(b.maxDistKm)
		if d > a.distMax {
			a.distMax = d
		}
	}

	out := make([]fusionBandCount, 0, len(counts))
	for key, a := range counts {
		parts := strings.SplitN(key, "|", 2)
		out = append(out, fusionBandCount{
			Band:          parts[0],
			Region:        parts[1],
			SpotCount:     a.spotCount,
			LinkCount:     len(a.links),
			ReporterCount: len(a.reporters),
			DistMaxKm:     a.distMax,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Band != out[j].Band {
			return bandOrder(out[i].Band) < bandOrder(out[j].Band)
		}
		return out[i].Region < out[j].Region
	})
	return out
}

func bandOrder(band string) int {
	order := []string{"160m", "80m", "60m", "40m", "30m", "20m", "17m", "15m", "12m", "10m", "6m", "4m", "2m"}
	for i, b := range order {
		if b == band {
			return i
		}
	}
	return 999
}

// startProplabSWIngest is a placeholder for the NOAA SWPC / D-RAP / OVATION
// ingest scheduled as a follow-up task. Logging it keeps the startup path
// explicit and grep-friendly.
func startProplabSWIngest() {
	logInfo("Proplab space-weather ingest not yet implemented")
}

// startProplabEventIngest is a placeholder for the contest/DXpedition/POTA
// calendar ingest scheduled as a follow-up task.
func startProplabEventIngest() {
	logInfo("Proplab event-calendar ingest not yet implemented")
}
