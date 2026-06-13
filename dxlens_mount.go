package main

import (
	"sync"
	"time"

	"dxlens"
)

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
}

func newDxlensProvider(engine *DxBaselineEngine, ttl time.Duration) *dxlensProvider {
	if ttl <= 0 {
		ttl = 5 * time.Second
	}
	return &dxlensProvider{engine: engine, ttl: ttl}
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
	defer e.mu.RUnlock()

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
	target := make(map[string]*dxlens.Bucket, len(e.targetBuckets))
	for k, v := range e.targetBuckets {
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
	return &dxlens.Snapshot{
		Version:       4,
		SavedAt:       time.Now().Unix(),
		FirstEventAt:  e.firstEventAt,
		LastEventAt:   e.lastEventAt,
		Buckets:       buckets,
		TargetBuckets: target,
		Events:        conv,
	}
}
