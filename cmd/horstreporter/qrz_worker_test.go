package main

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"horstreporter/internal/qrzlookup"
)

type fakeQRZClient struct {
	mu    sync.Mutex
	calls []string
	recs  map[string]string // call -> grid; missing = not found
	err   error
}

func (f *fakeQRZClient) LookupInfo(call string) (qrzlookup.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	if f.err != nil {
		return qrzlookup.Info{}, f.err
	}
	loc, ok := f.recs[call]
	if !ok {
		return qrzlookup.Info{}, qrzlookup.ErrNotFound
	}
	return qrzlookup.Info{Locator: loc}, nil
}

type fakeCallStore struct {
	mu   sync.Mutex
	rows map[string]callLocatorRow
}

func (s *fakeCallStore) loadCallsignLocators(_ context.Context, positiveSince, negativeSince int64) ([]callLocatorRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []callLocatorRow
	for _, r := range s.rows {
		if (r.Locator != "" && r.FetchedAt >= positiveSince) || (r.Locator == "" && r.FetchedAt >= negativeSince) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *fakeCallStore) upsertCallsignLocator(_ context.Context, call, loc string, t int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[call] = callLocatorRow{Call: call, Locator: loc, FetchedAt: t}
	return nil
}

func testQRZWorker(client qrzLookuper, store callLocatorStore, now time.Time) (*qrzWorker, *[]time.Duration) {
	w := newQRZWorker(client, newCallLocatorCache(), store)
	w.now = func() time.Time { return now }
	var slept []time.Duration
	w.sleep = func(d time.Duration) { slept = append(slept, d) }
	return w, &slept
}

func TestQRZWorkerLooksUpOnceAndPersists(t *testing.T) {
	now := time.Unix(1_794_400_000, 0)
	client := &fakeQRZClient{recs: map[string]string{"W1AW": "FN31PR", "DK0XX": ""}}
	store := &fakeCallStore{rows: map[string]callLocatorRow{}}
	w, slept := testQRZWorker(client, store, now)

	for _, call := range []string{"W1AW", "W1AW", "DK0XX", "N0CALL", "W1AW"} {
		w.enqueue(call)
	}
	close(w.queue)
	w.run()

	if len(client.calls) != 3 {
		t.Fatalf("looked up %v, want each call once", client.calls)
	}
	if loc, st := w.cache.resolve("W1AW", now.Unix()); st != resolveQRZ || loc != "FN31PR" {
		t.Fatalf("W1AW: %q %v", loc, st)
	}
	for _, call := range []string{"DK0XX", "N0CALL"} {
		if _, st := w.cache.resolve(call, now.Unix()); st != resolveNegative {
			t.Fatalf("%s should be a negative entry: %v", call, st)
		}
		if r := store.rows[call]; r.Locator != "" || r.FetchedAt != now.Unix() {
			t.Fatalf("%s persisted as %+v", call, r)
		}
	}
	if store.rows["W1AW"].Locator != "FN31PR" {
		t.Fatalf("W1AW persisted as %+v", store.rows["W1AW"])
	}
	for _, d := range *slept {
		if d != qrzLookupInterval {
			t.Fatalf("slept %v between lookups, want the rate interval only", d)
		}
	}
	if w.stats.found.Load() != 1 || w.stats.noGrid.Load() != 2 {
		t.Fatalf("stats found %d no_grid %d", w.stats.found.Load(), w.stats.noGrid.Load())
	}
}

func TestQRZWorkerSkipsCallsTheFeedResolved(t *testing.T) {
	now := time.Unix(1_794_400_000, 0)
	client := &fakeQRZClient{recs: map[string]string{}}
	w, _ := testQRZWorker(client, nil, now)
	w.enqueue("DL1ABC")
	w.cache.observeFeed("DL1ABC", "JO62", now.Unix())
	close(w.queue)
	w.run()
	if len(client.calls) != 0 {
		t.Fatalf("looked up %v although the feed resolved it", client.calls)
	}
}

func TestQRZWorkerBacksOffAndStopsOnAuthError(t *testing.T) {
	now := time.Unix(1_794_400_000, 0)
	client := &fakeQRZClient{err: errors.New("connection refused")}
	w, _ := testQRZWorker(client, nil, now)
	w.enqueue("A1A")
	w.enqueue("B1B")
	w.enqueue("C1C")
	// Network errors back off (doubling, capped); an auth error stops.
	if next, stop := w.lookupOne("A1A", 0); stop || next != qrzBackoffMin {
		t.Fatalf("first error: backoff %v stop %v", next, stop)
	}
	if next, _ := w.lookupOne("B1B", qrzBackoffMax); next != qrzBackoffMax {
		t.Fatalf("backoff not capped: %v", next)
	}
	client.err = &qrzlookup.AuthError{Msg: "Username/password incorrect"}
	if _, stop := w.lookupOne("C1C", 0); !stop || !w.stats.authFailed.Load() {
		t.Fatal("auth error must stop the worker")
	}
	w.enqueue("D1D")
	if w.queued() != 3 {
		t.Fatalf("a stopped worker still queues: %d", w.queued())
	}
}

func TestQRZWorkerQueueIsBounded(t *testing.T) {
	w, _ := testQRZWorker(&fakeQRZClient{}, nil, time.Unix(1, 0))
	for i := 0; i < qrzQueueSize+10; i++ {
		w.enqueue("K" + strconv.Itoa(i))
	}
	if w.queued() != qrzQueueSize || w.stats.dropped.Load() != 10 {
		t.Fatalf("queued %d dropped %d", w.queued(), w.stats.dropped.Load())
	}
}

func TestQRZWorkerLoadsPersistedAnswers(t *testing.T) {
	now := time.Unix(1_794_400_000, 0)
	store := &fakeCallStore{rows: map[string]callLocatorRow{
		"W1AW":   {Call: "W1AW", Locator: "FN31PR", FetchedAt: now.Unix() - 3600},
		"OLD1":   {Call: "OLD1", Locator: "FN31", FetchedAt: now.Unix() - callLocatorQRZTTL - 10},
		"N0CALL": {Call: "N0CALL", FetchedAt: now.Unix() - 3600},
	}}
	w, _ := testQRZWorker(&fakeQRZClient{}, store, now)
	if n := w.loadPersisted(); n != 2 {
		t.Fatalf("loaded %d rows, want the 2 within their TTL", n)
	}
	if _, st := w.cache.resolve("W1AW", now.Unix()); st != resolveQRZ {
		t.Fatalf("W1AW: %v", st)
	}
	if _, st := w.cache.resolve("N0CALL", now.Unix()); st != resolveNegative {
		t.Fatalf("N0CALL: %v", st)
	}
}
