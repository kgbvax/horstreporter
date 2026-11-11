package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"horstreporter/internal/qrzlookup"
)

// QRZ lookup worker: resolves the callsigns that no feed showed with a grid
// (RBN DX stations that never run FT8, skimmers missing from the RBN list).
// Lookups run on one goroutine at a bounded rate from a bounded, de-duplicated
// queue; the RBN ingest only enqueues and never waits. Answers go into the
// in-memory cache (callLocators) and, when Postgres is configured, into
// callsign_locators so a restart does not ask QRZ again.

type qrzLookuper interface {
	LookupInfo(call string) (qrzlookup.Info, error)
}

// callLocatorStore persists QRZ answers (dxPostgresStore; nil without one).
type callLocatorStore interface {
	loadCallsignLocators(ctx context.Context, positiveSince, negativeSince int64) ([]callLocatorRow, error)
	upsertCallsignLocator(ctx context.Context, call, loc string, fetchedAt int64) error
}

// callLocatorRow is one persisted answer; Locator "" is a negative answer.
type callLocatorRow struct {
	Call      string
	Locator   string
	FetchedAt int64
}

const (
	qrzQueueSize       = 5000
	qrzLookupInterval  = 500 * time.Millisecond // at most 2 lookups a second
	qrzBackoffMin      = 30 * time.Second
	qrzBackoffMax      = 10 * time.Minute
	qrzPersistTimeout  = 3 * time.Second
	qrzLoadTimeout     = 15 * time.Second
	qrzPendingCapacity = qrzQueueSize
)

type qrzWorkerStats struct {
	lookups    atomic.Int64
	found      atomic.Int64 // record with a grid
	noGrid     atomic.Int64 // not found, or a record without a grid
	errors     atomic.Int64
	dropped    atomic.Int64 // queue full
	authFailed atomic.Bool
	loaded     atomic.Int64 // rows loaded from Postgres at startup
}

type qrzWorker struct {
	client qrzLookuper
	cache  *callLocatorCache
	store  callLocatorStore

	queue   chan string
	mu      sync.Mutex
	pending map[string]struct{}
	stopped atomic.Bool

	interval   time.Duration
	backoffMin time.Duration
	backoffMax time.Duration
	now        func() time.Time
	sleep      func(time.Duration)

	stats qrzWorkerStats
}

// qrzLookups is the process's worker; nil when QRZ is not configured.
var qrzLookups *qrzWorker

func newQRZWorker(client qrzLookuper, cache *callLocatorCache, store callLocatorStore) *qrzWorker {
	return &qrzWorker{
		client:     client,
		cache:      cache,
		store:      store,
		queue:      make(chan string, qrzQueueSize),
		pending:    make(map[string]struct{}, 256),
		interval:   qrzLookupInterval,
		backoffMin: qrzBackoffMin,
		backoffMax: qrzBackoffMax,
		now:        time.Now,
		sleep:      time.Sleep,
	}
}

// enqueue asks for a lookup of an upper-case call. It never blocks: a call
// already queued is ignored, and a full queue drops the call (the next spot
// of the same station asks again).
func (w *qrzWorker) enqueue(call string) {
	if w == nil || call == "" || w.stopped.Load() {
		return
	}
	w.mu.Lock()
	if _, ok := w.pending[call]; ok {
		w.mu.Unlock()
		return
	}
	if len(w.pending) >= qrzPendingCapacity {
		w.mu.Unlock()
		w.stats.dropped.Add(1)
		return
	}
	select {
	case w.queue <- call:
		w.pending[call] = struct{}{}
	default:
		w.stats.dropped.Add(1)
	}
	w.mu.Unlock()
}

func (w *qrzWorker) queued() int {
	if w == nil {
		return 0
	}
	return len(w.queue)
}

// loadPersisted fills the cache from Postgres (answers younger than their TTL)
// and returns how many rows it loaded.
func (w *qrzWorker) loadPersisted() int {
	if w == nil || w.store == nil {
		return 0
	}
	now := w.now().Unix()
	ctx, cancel := context.WithTimeout(context.Background(), qrzLoadTimeout)
	defer cancel()
	rows, err := w.store.loadCallsignLocators(ctx, now-callLocatorQRZTTL, now-callLocatorNegativeTTL)
	if err != nil {
		logInfo("QRZ: loading cached callsign locators failed: %v", err)
		return 0
	}
	for _, r := range rows {
		w.cache.storeQRZ(r.Call, r.Locator, r.FetchedAt)
	}
	w.stats.loaded.Store(int64(len(rows)))
	return len(rows)
}

// run serves the queue until an authentication failure stops it.
func (w *qrzWorker) run() {
	backoff := time.Duration(0)
	for call := range w.queue {
		if backoff > 0 {
			w.sleep(backoff)
		}
		next, stop := w.lookupOne(call, backoff)
		w.mu.Lock()
		delete(w.pending, call)
		w.mu.Unlock()
		if stop {
			return
		}
		backoff = next
		w.sleep(w.interval)
	}
}

// lookupOne resolves one call and returns the backoff for the next lookup
// (0 after a success) and whether the worker must stop.
func (w *qrzWorker) lookupOne(call string, backoff time.Duration) (time.Duration, bool) {
	// The feed may have shown the call while it waited in the queue.
	if _, st := w.cache.resolve(call, w.now().Unix()); st != resolveMiss {
		return backoff, false
	}
	w.stats.lookups.Add(1)
	info, err := w.client.LookupInfo(call)
	var authErr *qrzlookup.AuthError
	switch {
	case err == nil || errors.Is(err, qrzlookup.ErrNotFound):
		w.record(call, info.Locator)
		return 0, false
	case errors.As(err, &authErr):
		w.stats.errors.Add(1)
		w.stats.authFailed.Store(true)
		w.stopped.Store(true)
		logInfo("QRZ lookups stopped: %v (check QRZ_USERNAME / QRZ_PASSWORD and the XML subscription)", err)
		return 0, true
	default:
		w.stats.errors.Add(1)
		next := backoff * 2
		if next < w.backoffMin {
			next = w.backoffMin
		}
		if next > w.backoffMax {
			next = w.backoffMax
		}
		if backoff == 0 {
			logInfo("QRZ lookup of %s failed: %v; backing off %s", call, err, next)
		}
		return next, false
	}
}

func (w *qrzWorker) record(call, loc string) {
	now := w.now().Unix()
	if loc == "" {
		w.stats.noGrid.Add(1)
	} else {
		w.stats.found.Add(1)
	}
	w.cache.storeQRZ(call, loc, now)
	if w.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), qrzPersistTimeout)
	defer cancel()
	if err := w.store.upsertCallsignLocator(ctx, call, loc, now); err != nil {
		logDebug("QRZ: persisting %s failed: %v", call, err)
	}
}

// callLocatorStore returns the engine's Postgres store for QRZ answers, or nil.
func (e *DxBaselineEngine) callLocatorStore() callLocatorStore {
	if e == nil {
		return nil
	}
	e.mu.RLock()
	st := e.store
	e.mu.RUnlock()
	if st == nil {
		return nil
	}
	return st
}

// firstNonEmpty returns the first argument that is not blank, trimmed.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
