package main

import (
	"sync"
	"time"
)

// TTL cache for the all-bands baseline indexes the Postgres store serves to
// every dx_conditions / hot_bands request.
//
// allBandBaselinePairs ran two GROUP BY queries per request (about 25k rows
// pulled and parsed each time) and was ~45% of those endpoints' CPU on prod.
// The tables behind it only change when the baseline flusher writes (and drift
// on the scale of days), so a minute-old copy is indistinguishable. The global
// index is shared by every caller; cluster indexes are kept per operator
// cluster. Callers treat the maps as read-only, which is what makes sharing
// them safe (pairsForBandSlot, quantilesFromPairs and reachBaselineP90 only
// read).
const (
	baselinePairsTTL = 60 * time.Second
	// A failed refresh serves the previous copy for this long rather than
	// dropping the baseline to zero (which the engine reads as "no baseline").
	baselinePairsStaleOK = 10 * time.Minute
	// After a failed load, retry no sooner than this: waiters share the entry
	// lock, so without it a struggling Postgres would be asked (and waited on,
	// up to the query timeout) once per queued request.
	baselinePairsRetryAfter = 5 * time.Second
	// Distinct cluster indexes held (a cluster index is on the order of 1 MB);
	// the least recently loaded is evicted.
	baselinePairsMaxClusters = 24
)

type baselinePairsIndex = map[bandSlotKey][]baselinePair

type baselinePairsEntry struct {
	mu      sync.Mutex // held across a load, so concurrent requests share one query
	idx     baselinePairsIndex
	at      time.Time
	failAt  time.Time // when the last load failed (zero: it did not)
	failErr error
}

type baselinePairsCache struct {
	mu      sync.Mutex
	entries map[string]*baselinePairsEntry
}

// get returns the cached index for key, loading it when missing or older than
// the TTL. Concurrent callers for one key wait for a single load.
func (c *baselinePairsCache) get(key string, now time.Time, load func() (baselinePairsIndex, error)) (baselinePairsIndex, error) {
	e := c.entry(key, now)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.idx != nil && now.Sub(e.at) < baselinePairsTTL {
		return e.idx, nil
	}
	stale := e.idx != nil && now.Sub(e.at) < baselinePairsStaleOK
	if !e.failAt.IsZero() && now.Sub(e.failAt) < baselinePairsRetryAfter {
		if stale {
			return e.idx, nil
		}
		return nil, e.failErr
	}
	idx, err := load()
	if err != nil {
		e.failAt, e.failErr = now, err
		if stale {
			return e.idx, nil
		}
		return nil, err
	}
	e.idx, e.at, e.failAt, e.failErr = idx, now, time.Time{}, nil
	return idx, nil
}

func (c *baselinePairsCache) entry(key string, now time.Time) *baselinePairsEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]*baselinePairsEntry)
	}
	if e := c.entries[key]; e != nil {
		return e
	}
	// The global index (key "") is never evicted; clusters are bounded.
	if len(c.entries) > baselinePairsMaxClusters {
		var oldestKey string
		var oldest time.Time
		first := true
		for k, e := range c.entries {
			if k == "" {
				continue
			}
			if first || e.at.Before(oldest) {
				oldestKey, oldest, first = k, e.at, false
			}
		}
		if !first {
			delete(c.entries, oldestKey)
		}
	}
	e := &baselinePairsEntry{}
	c.entries[key] = e
	return e
}
