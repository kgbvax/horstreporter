package main

import (
	"context"
	"sort"
	"strings"
	"time"

	"horstreporter/internal/proplab"
)

// dx_proplab_reach.go assembles the reachability product verdict per request:
// destination live rates (persisted closed buckets + the still-open in-memory
// bucket), the slot-of-day quantile baseline, persistence counts, the cached
// 48-slot profile, SW/event context and a fresh Ladder verdict. Everything is
// computed on demand (5s-HTTP-cache keyed by QTH+surroundings) because the
// scope is per-operator; the only ticker-level work is the shared slot-profile
// cache refresh. Surge state (tick-over-tick diff + 4h TTL) lives here keyed by
// scope so every QTH gets its own "what's new" stream.

// destProfileEntry caches one scope's 48-slot baseline profile; refreshed at
// most every 6h because baselines change daily, not minutely.
type destProfileEntry struct {
	rows []proplab.BaselineDayRow
	at   time.Time
}

const destProfileMaxAge = 6 * time.Hour

// destBands returns the 13 canonical bands in low-to-high order.
func destBands() []string {
	order := []string{"160m", "80m", "60m", "40m", "30m", "20m", "17m", "15m", "12m", "10m", "6m", "4m", "2m"}
	out := order[:0:len(order)]
	return append([]string(nil), out...)
}

// destScopeKey normalizes a scope set for map keys and the HTTP cache.
func destScopeKey(scopes []string) string {
	return strings.Join(scopes, ",")
}

// ReachVerdict composes the product verdict for one QTH scope. An empty or
// invalid QTH yields the global view (no scope filter).
func (s *ProplabService) ReachVerdict(qth string, surroundings bool) proplab.ReachVerdict {
	now := time.Now().Unix()
	qth = strings.ToUpper(strings.TrimSpace(qth))

	bands := destBands()
	scopeMap := destScopesForQTH(qth, surroundings)
	var scopes []string
	if scopeMap != nil {
		for sc := range scopeMap {
			scopes = append(scopes, sc)
		}
		sort.Strings(scopes)
	}
	scopeKey := destScopeKey(scopes)

	// Live window: 30 min of closed persisted buckets plus the still-open
	// in-memory bucket (merged additively; during the first minutes of a bucket
	// rates run slightly under — acceptable freshness-vs-fuzziness trade).
	liveWindowMin := 30
	merged := make(map[[2]string]*proplab.DestRow)
	addRows := func(rows []proplab.DestRow) {
		for i := range rows {
			r := rows[i]
			key := [2]string{r.Band, r.Region}
			m := merged[key]
			if m == nil {
				cp := r
				merged[key] = &cp
				continue
			}
			m.SpotCount += r.SpotCount
			m.LinkCount += r.LinkCount
			m.ReporterCount += r.ReporterCount
			if r.DistMaxKm > m.DistMaxKm {
				m.DistMaxKm = r.DistMaxKm
			}
		}
	}
	if s.store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		persisted, err := s.store.loadDestLive(ctx, scopes, bands, nil, liveWindowMin, now)
		cancel()
		if err != nil {
			logInfo("Proplab dest live load failed: %v", err)
		} else {
			addRows(persisted)
		}
	}
	addRows(s.destLiveSnapshot(scopeMap, proplab.AlignBucketStart(now)))

	live := make([]proplab.DestRow, 0, len(merged))
	for _, m := range merged {
		live = append(live, *m)
	}
	liveB, liveR := destLiveBandsRegions(live)

	var baseline []proplab.BaselineDayRow
	var persistence map[[2]string][2]int
	if s.store != nil && len(live) > 0 {
		slot := utcSlotOfDay(now)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		var err error
		baseline, err = s.store.loadDestBaseline(ctx, scopes, liveB, liveR, slot, 45, now)
		if err != nil {
			logInfo("Proplab dest baseline load failed: %v", err)
		}
		persistence, err = s.store.loadDestPersistence(ctx, scopes, liveB, liveR, 120, 2, now)
		cancel()
		if err != nil {
			logInfo("Proplab dest persistence load failed: %v", err)
		}
	}
	if persistence == nil {
		persistence = map[[2]string][2]int{}
	}

	profile := s.destSlotProfile(scopeKey, scopes, bands, now)

	s.mu.RLock()
	sw := s.sw
	events := s.events
	s.mu.RUnlock()

	ladder := s.LadderVerdict(qth, surroundings, nil)

	v := proplab.ComposeReach(proplab.ReachInputs{
		Now:          now,
		QTH:          qth,
		Surroundings: surroundings,
		Live:         live,
		LiveMinutes:  liveWindowMin,
		Baseline:     baseline,
		SlotProfile:  profile,
		Persistence:  persistence,
		SW:           sw,
		Events:       events,
		Ladder:       ladder,
	})

	// Surge state: diff against the previous snapshot for this scope, merge new
	// detections into the TTL-carried set, and swap in the current snapshot.
	s.mu.Lock()
	if s.reachPrev == nil {
		s.reachPrev = make(map[string]*proplab.ReachVerdict)
	}
	if s.reachSurges == nil {
		s.reachSurges = make(map[string]map[string]proplab.ReachSurge)
	}
	prev := s.reachPrev[scopeKey]
	active := s.reachSurges[scopeKey]
	if active == nil {
		active = make(map[string]proplab.ReachSurge)
		s.reachSurges[scopeKey] = active
	}
	for _, sg := range proplab.DetectSurges(prev, v, profile, now) {
		active[sg.Kind+"|"+sg.Band+"|"+sg.Region] = sg
	}
	for key, sg := range active {
		if now-sg.FirstSeen > proplab.SurgeTTLMinutes*60 {
			delete(active, key)
		}
	}
	cur := v
	s.reachPrev[scopeKey] = &cur
	s.mu.Unlock()

	activeList := make([]proplab.ReachSurge, 0, len(active))
	for _, sg := range active {
		activeList = append(activeList, sg)
	}
	sort.Slice(activeList, func(i, j int) bool { return activeList[i].FirstSeen > activeList[j].FirstSeen })
	v.Surges = append(v.Surges, activeList...)
	return v
}

// destSlotProfile returns the cached 48-slot baseline profile for a scope,
// loading (or refreshing) it when older than destProfileMaxAge. Returns nil on
// load failure — the verdict degrades to schedule_unavailable, never an error.
func (s *ProplabService) destSlotProfile(scopeKey string, scopes, bands []string, now int64) []proplab.BaselineDayRow {
	if s.store == nil {
		return nil
	}
	s.mu.RLock()
	entry := s.destProfiles[scopeKey]
	s.mu.RUnlock()
	if entry != nil && time.Since(entry.at) < destProfileMaxAge {
		return entry.rows
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	rows, err := s.store.loadDestSlotProfile(ctx, scopes, bands, nil, 45, now)
	cancel()
	if err != nil {
		logInfo("Proplab dest slot profile load failed: %v", err)
		if entry != nil {
			return entry.rows // serve stale rather than nothing
		}
		return nil
	}

	s.mu.Lock()
	if s.destProfiles == nil {
		s.destProfiles = make(map[string]*destProfileEntry)
	}
	s.destProfiles[scopeKey] = &destProfileEntry{rows: rows, at: time.Now()}
	s.mu.Unlock()
	return rows
}

func destLiveBandsRegions(live []proplab.DestRow) (bands, regions []string) {
	bandSet := make(map[string]struct{})
	regionSet := make(map[string]struct{})
	for _, r := range live {
		bandSet[r.Band] = struct{}{}
		regionSet[r.Region] = struct{}{}
	}
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
