package main

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func pairsIdx(count int64) baselinePairsIndex {
	return baselinePairsIndex{{Band: "20m", Slot: 1}: {{DistanceTier: 1, SnrTier: 1, Count: count}}}
}

func TestBaselinePairsCacheServesWithinTTLAndReloadsAfter(t *testing.T) {
	var c baselinePairsCache
	var loads atomic.Int64
	load := func() (baselinePairsIndex, error) { return pairsIdx(loads.Add(1)), nil }
	t0 := time.Now()
	a, _ := c.get("c:JO30", t0, load)
	b, _ := c.get("c:JO30", t0.Add(baselinePairsTTL-time.Second), load)
	if loads.Load() != 1 || a[bandSlotKey{Band: "20m", Slot: 1}][0].Count != b[bandSlotKey{Band: "20m", Slot: 1}][0].Count {
		t.Fatalf("loads=%d: second call inside the TTL must hit the cache", loads.Load())
	}
	d, _ := c.get("c:JO30", t0.Add(baselinePairsTTL+time.Second), load)
	if loads.Load() != 2 || d[bandSlotKey{Band: "20m", Slot: 1}][0].Count != 2 {
		t.Fatalf("loads=%d: an expired entry must reload", loads.Load())
	}
	// A different key loads separately.
	c.get("", t0, load)
	if loads.Load() != 3 {
		t.Fatalf("loads=%d, want a separate load for the global key", loads.Load())
	}
}

func TestBaselinePairsCacheCoalescesConcurrentLoads(t *testing.T) {
	var c baselinePairsCache
	var loads atomic.Int64
	release := make(chan struct{})
	load := func() (baselinePairsIndex, error) {
		loads.Add(1)
		<-release
		return pairsIdx(1), nil
	}
	now := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if idx, err := c.get("", now, load); err != nil || idx == nil {
				t.Errorf("get: %v", err)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if loads.Load() != 1 {
		t.Fatalf("%d loads for 8 concurrent callers, want 1", loads.Load())
	}
}

func TestBaselinePairsCacheKeepsStaleCopyWhenRefreshFails(t *testing.T) {
	var c baselinePairsCache
	t0 := time.Now()
	c.get("", t0, func() (baselinePairsIndex, error) { return pairsIdx(7), nil })
	failing := func() (baselinePairsIndex, error) { return nil, errors.New("pg down") }

	got, err := c.get("", t0.Add(baselinePairsTTL+time.Second), failing)
	if err != nil || got[bandSlotKey{Band: "20m", Slot: 1}][0].Count != 7 {
		t.Fatalf("stale copy not served on refresh failure: %v %v", got, err)
	}
	if _, err := c.get("", t0.Add(baselinePairsStaleOK+time.Second), failing); err == nil {
		t.Fatal("a copy older than the stale limit must surface the error")
	}
	// Within the retry window the failing load is not attempted again.
	var calls atomic.Int64
	counting := func() (baselinePairsIndex, error) { calls.Add(1); return nil, errors.New("pg down") }
	var c2 baselinePairsCache
	c2.get("", t0, counting)
	c2.get("", t0.Add(time.Second), counting)
	if calls.Load() != 1 {
		t.Fatalf("%d load attempts inside the retry window, want 1", calls.Load())
	}
	c2.get("", t0.Add(baselinePairsRetryAfter+time.Second), counting)
	if calls.Load() != 2 {
		t.Fatalf("%d load attempts after the retry window, want 2", calls.Load())
	}
	// An error with nothing cached is returned as before.
	var empty baselinePairsCache
	if _, err := empty.get("c:X", t0, failing); err == nil {
		t.Fatal("expected the load error")
	}
}

func TestBaselinePairsCacheBoundsClusterEntriesButKeepsGlobal(t *testing.T) {
	var c baselinePairsCache
	t0 := time.Now()
	load := func() (baselinePairsIndex, error) { return pairsIdx(1), nil }
	c.get("", t0, load)
	for i := 0; i < baselinePairsMaxClusters+10; i++ {
		c.get("c:"+string(rune('A'+i%26))+string(rune('A'+i/26)), t0.Add(time.Duration(i)*time.Second), load)
	}
	if n := len(c.entries); n > baselinePairsMaxClusters+2 {
		t.Fatalf("%d entries, bound is %d", n, baselinePairsMaxClusters)
	}
	if _, ok := c.entries[""]; !ok {
		t.Fatal("the global entry was evicted")
	}
}
