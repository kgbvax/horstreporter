package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test doubles: a fake clock (Sleep advances time instantly), a RoundTripper
// stub standing in for wspr.live (no local port binding needed), and an
// in-memory model of the month-commit transaction.
// ---------------------------------------------------------------------------

type wsprFakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *wsprFakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *wsprFakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
	return nil
}

// wsprStubRow is one ring-aggregate row the stub serves (rx4/tx4 are the
// already-truncated grid4s, as the real query returns them).
type wsprStubRow struct {
	Slot int
	Band int
	Rx4  string
	Tx4  string
	C    int64
}

type wsprStubRequest struct {
	URL    string
	Query  string
	From   int64
	To     int64
	Global bool
	At     time.Time
}

var wsprStubWindowRE = regexp.MustCompile(`time >= toDateTime\((\d+)\) AND time < toDateTime\((\d+)\)`)

// wsprStub answers ring and global queries. rowsFor returns ring rows for a
// UTC hour window [from, to); the stub filters by slot so hourly splits
// return exactly the day's rows for that hour. fail, when set, may override
// any response.
type wsprStub struct {
	mu       sync.Mutex
	clock    *wsprFakeClock
	requests []wsprStubRequest
	rowsFor  func(from, to int64) []wsprStubRow
	globalC  int64
	fail     func(r wsprStubRequest, n int) (status int, body string, failed bool)
}

func (s *wsprStub) RoundTrip(req *http.Request) (*http.Response, error) {
	q := req.URL.Query().Get("query")
	m := wsprStubWindowRE.FindStringSubmatch(q)
	var from, to int64
	if m != nil {
		from, _ = strconv.ParseInt(m[1], 10, 64)
		to, _ = strconv.ParseInt(m[2], 10, 64)
	}
	r := wsprStubRequest{
		URL:    req.URL.String(),
		Query:  q,
		From:   from,
		To:     to,
		Global: !strings.Contains(q, "tx_loc),1,4) AS tx4"),
		At:     s.clock.Now(),
	}
	s.mu.Lock()
	s.requests = append(s.requests, r)
	n := len(s.requests)
	s.mu.Unlock()

	resp := func(status int, body string) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	}
	if s.fail != nil {
		if status, body, failed := s.fail(r, n); failed {
			return resp(status, body)
		}
	}
	var b bytes.Buffer
	if r.Global {
		for slot := 0; slot < 48; slot++ {
			st := (from/86400)*86400 + int64(slot)*1800
			if st >= from && st < to {
				fmt.Fprintf(&b, `{"slot":%d,"c":%d}`+"\n", slot, s.globalC)
			}
		}
		return resp(http.StatusOK, b.String())
	}
	if s.rowsFor != nil {
		for _, row := range s.rowsFor(from, to) {
			st := (from/86400)*86400 + int64(row.Slot)*1800
			if st < from || st >= to {
				continue
			}
			fmt.Fprintf(&b, `{"slot":%d,"band":%d,"rx4":%q,"tx4":%q,"c":%d}`+"\n", row.Slot, row.Band, row.Rx4, row.Tx4, row.C)
		}
	}
	return resp(http.StatusOK, b.String())
}

func (s *wsprStub) reqs() []wsprStubRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]wsprStubRequest(nil), s.requests...)
}

type wsprFakeSeasonKey struct {
	Grid4, Band, Region string
	YearMonth           int
	Layer               string
}

type wsprFakeActivityKey struct {
	Grid4, Band string
	YearMonth   int
	Layer       string
}

type wsprFakeIngestKey struct {
	Day   int64
	Slot  int
	Layer string
}

// wsprFakeStore models pgAlmanacWSPRBackfillStore.commitMonth: DELETE the
// WSPR layer for (ring grid4s, month), INSERT the month's rows, replace the
// month's WSPR ingest totals when fetched, and set the done keys — atomically.
type wsprFakeStore struct {
	mu       sync.Mutex
	counts   map[wsprFakeSeasonKey][]byte
	activity map[wsprFakeActivityKey]uint32
	ingest   map[wsprFakeIngestKey]int64
	meta     map[string]string
	commits  int
}

func newWSPRFakeStore() *wsprFakeStore {
	return &wsprFakeStore{
		counts:   map[wsprFakeSeasonKey][]byte{},
		activity: map[wsprFakeActivityKey]uint32{},
		ingest:   map[wsprFakeIngestKey]int64{},
		meta:     map[string]string{},
	}
}

func (s *wsprFakeStore) metaExists(ctx context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.meta[key]
	return ok, nil
}

func (s *wsprFakeStore) setMeta(ctx context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.meta[key] = value
	return nil
}

func (s *wsprFakeStore) commitMonth(ctx context.Context, m *almanacWSPRMonth) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ring := map[string]bool{}
	for _, g := range m.Ring {
		ring[g] = true
	}
	for k := range s.counts {
		if k.Layer == almanacSeasonLayerWSPR && k.YearMonth == m.YearMonth && ring[k.Grid4] {
			delete(s.counts, k)
		}
	}
	for k := range s.activity {
		if k.Layer == almanacSeasonLayerWSPR && k.YearMonth == m.YearMonth && ring[k.Grid4] {
			delete(s.activity, k)
		}
	}
	for k, v := range m.Counts {
		s.counts[wsprFakeSeasonKey{k.Grid4, k.Band, k.Region, m.YearMonth, almanacSeasonLayerWSPR}] = append([]byte(nil), v[:]...)
	}
	for k, v := range m.Activity {
		s.activity[wsprFakeActivityKey{k.Grid, k.Band, m.YearMonth, almanacSeasonLayerWSPR}] = v
	}
	if m.IngestTotals != nil {
		for k := range s.ingest {
			if k.Layer == almanacSeasonLayerWSPR && k.Day >= m.FirstDay && k.Day <= m.LastDay {
				delete(s.ingest, k)
			}
		}
		for k, v := range m.IngestTotals {
			s.ingest[wsprFakeIngestKey{k.Day, k.Slot, almanacSeasonLayerWSPR}] = v
		}
		s.meta[m.IngestDoneKey] = "1"
	}
	s.meta[m.DoneKey] = "1"
	s.commits++
	return nil
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// wsprTestNow is inside September 2026, so August 2026 is the newest complete
// month.
var wsprTestNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

const wsprTestMonth = 202608

func wsprDay(y int, m time.Month, d int) int64 {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400
}

type wsprHarness struct {
	clock *wsprFakeClock
	stub  *wsprStub
	store *wsprFakeStore
	bf    *almanacWSPRBackfill
}

func newWSPRHarness(t *testing.T, areas []string, years int, disk func() (float64, error)) *wsprHarness {
	t.Helper()
	clock := &wsprFakeClock{now: wsprTestNow}
	stub := &wsprStub{clock: clock, globalC: 7}
	store := newWSPRFakeStore()
	if disk == nil {
		disk = func() (float64, error) { return 0.30, nil }
	}
	bf := newAlmanacWSPRBackfill(almanacWSPRBackfillConfig{
		Endpoint:  "https://wspr.example",
		Areas:     areas,
		Years:     years,
		Client:    &http.Client{Transport: stub},
		Store:     store,
		Clock:     clock,
		DiskProbe: disk,
	})
	return &wsprHarness{clock: clock, stub: stub, store: store, bf: bf}
}

// markDoneExcept pre-marks every backfill month for area as done except keep,
// so a test drives exactly one month.
func (h *wsprHarness) markDoneExcept(area string, keep int) {
	for _, ym := range almanacWSPRBackfillMonths(wsprTestNow, h.bf.years) {
		if ym != keep {
			h.store.meta[almanacWSPRMonthDoneKey(area, ym)] = "1"
			h.store.meta[almanacWSPRIngestDoneKey(ym)] = "1"
		}
	}
}

func wsprSeg(t *testing.T, store *wsprFakeStore, grid4, band, region string, ym int) []byte {
	t.Helper()
	return store.counts[wsprFakeSeasonKey{grid4, band, region, ym, almanacSeasonLayerWSPR}]
}

// wsprAugRows is a fixed per-day data set for August 2026 used by several
// tests (JO32 is the configured area; JO31 and JO22 lie in its r=2 ring).
func wsprAugRows(from, to int64) []wsprStubRow {
	day := from / 86400
	if day == wsprDay(2026, 8, 3) {
		return []wsprStubRow{
			{Slot: 20, Band: 14, Rx4: "JO32", Tx4: "FN20", C: 3},   // rx in ring → (JO32, NA)
			{Slot: 21, Band: 14, Rx4: "JO32", Tx4: "JO31", C: 2},   // both ends in ring → two rows
			{Slot: 22, Band: 7, Rx4: "PM95", Tx4: "JO22", C: 4},    // tx in ring → (JO22, JA)
			{Slot: 23, Band: 2400, Rx4: "JO32", Tx4: "FN20", C: 9}, // out of scope → dropped
			{Slot: 23, Band: 13, Rx4: "JO32", Tx4: "FN20", C: 9},   // unknown code → dropped
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

func TestAlmanacWSPRBackfillMonths(t *testing.T) {
	got := almanacWSPRBackfillMonths(wsprTestNow, 1)
	if len(got) != 12 {
		t.Fatalf("1 year = %d months, want 12: %v", len(got), got)
	}
	if got[0] != 202608 || got[11] != 202509 {
		t.Fatalf("newest-first complete months: got %d … %d, want 202608 … 202509", got[0], got[11])
	}
	for _, ym := range got {
		if ym == 202609 {
			t.Fatalf("current month 202609 must never be backfilled")
		}
	}
	// Floor: never before 2008-03 (the wspr.live archive start).
	early := almanacWSPRBackfillMonths(time.Date(2009, 1, 15, 0, 0, 0, 0, time.UTC), 3)
	if want := []int{200812, 200811, 200810, 200809, 200808, 200807, 200806, 200805, 200804, 200803}; !reflect.DeepEqual(early, want) {
		t.Fatalf("floored months = %v, want %v", early, want)
	}
}

func TestParseAlmanacWSPRBackfillAreas(t *testing.T) {
	got, err := parseAlmanacWSPRBackfillAreas(" jo32, JO62 ,,JO32")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"JO32", "JO62"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("areas = %v, want %v", got, want)
	}
	if got, err := parseAlmanacWSPRBackfillAreas(""); err != nil || len(got) != 0 {
		t.Fatalf("empty flag = %v, %v; want off", got, err)
	}
	for _, bad := range []string{"JO3", "DL1ABC", "ZZ99", "JO32AB"} {
		if _, err := parseAlmanacWSPRBackfillAreas(bad); err == nil {
			t.Errorf("area %q accepted, want error (grid4 only)", bad)
		}
	}
}

func TestAlmanacWSPRRegionNarrowBoxes(t *testing.T) {
	cases := map[string]string{
		"BL11": "KH6", // Hawaii, not NA/OC
		"PM95": "JA",  // Japan, not AS
		"FK68": "CAR", // Puerto Rico, not NA/SA
		"FN20": "NA",
		"JO31": "EU",
		"QF56": "VK",
	}
	for g, want := range cases {
		if got := almanacWSPRRegion(g); got != want {
			t.Errorf("region(%s) = %q, want %q", g, got, want)
		}
	}
	if got := almanacWSPRRegion("ZZ99"); got != "" {
		t.Errorf("invalid grid → %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// Backfill scenarios
// ---------------------------------------------------------------------------

func TestAlmanacWSPRBackfillBothOrientationsAndBands(t *testing.T) {
	h := newWSPRHarness(t, []string{"JO32"}, 1, nil)
	h.markDoneExcept("JO32", wsprTestMonth)
	h.stub.rowsFor = wsprAugRows
	if err := h.bf.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	dom3 := almanacSegmentOffset(3)

	// rx in ring, tx outside → only (JO32, NA).
	if seg := wsprSeg(t, h.store, "JO32", "20m", "NA", wsprTestMonth); seg == nil || seg[dom3+20] != 3 {
		t.Fatalf("(JO32,20m,NA) slot 20 = %v, want 3", seg)
	}
	if seg := wsprSeg(t, h.store, "FN20", "20m", "EU", wsprTestMonth); seg != nil {
		t.Fatalf("far end outside the ring must not get a row: FN20 %v", seg)
	}
	// Both ends in ring → one row per orientation, like dx_region_baseline_daily.
	a := wsprSeg(t, h.store, "JO32", "20m", "EU", wsprTestMonth)
	b := wsprSeg(t, h.store, "JO31", "20m", "EU", wsprTestMonth)
	if a == nil || b == nil || a[dom3+21] != 2 || b[dom3+21] != 2 {
		t.Fatalf("both-ends-in-ring: (JO32,EU)=%v (JO31,EU)=%v, want 2 at slot 21 in both", a, b)
	}
	// tx in ring, rx far end in the narrow JA box → (JO22, JA) on 40m.
	if seg := wsprSeg(t, h.store, "JO22", "40m", "JA", wsprTestMonth); seg == nil || seg[dom3+22] != 4 {
		t.Fatalf("(JO22,40m,JA) slot 22 = %v, want 4", seg)
	}
	// Band 14 maps to 20m; 2400 and 13 are dropped (no slot-23 counts anywhere).
	for k, v := range h.store.counts {
		if k.Band != "20m" && k.Band != "40m" {
			t.Fatalf("unexpected band row %+v", k)
		}
		if v[dom3+23] != 0 {
			t.Fatalf("dropped bands leaked into %+v slot 23 = %d", k, v[dom3+23])
		}
	}
	if len(h.store.counts) != 4 {
		t.Fatalf("got %d seasonal rows, want 4: %v", len(h.store.counts), h.store.counts)
	}
	// Activity masks: day 3 bit for each emitting (grid4, band).
	for _, k := range []wsprFakeActivityKey{{"JO32", "20m", wsprTestMonth, "wspr"}, {"JO31", "20m", wsprTestMonth, "wspr"}, {"JO22", "40m", wsprTestMonth, "wspr"}} {
		if h.store.activity[k] != 1<<2 {
			t.Fatalf("activity %+v = %b, want day-3 bit", k, h.store.activity[k])
		}
	}
	// Ingest-alive totals: one per slot per day of August, WSPR layer, in the
	// region-key unit (two keys per spot with both locators).
	if got := h.store.ingest[wsprFakeIngestKey{wsprDay(2026, 8, 10), 5, "wspr"}]; got != 14 {
		t.Fatalf("ingest total = %d, want 14 (2 × 7)", got)
	}
	if len(h.store.ingest) != 31*48 {
		t.Fatalf("ingest rows = %d, want %d", len(h.store.ingest), 31*48)
	}
	if h.store.meta[almanacWSPRMonthDoneKey("JO32", wsprTestMonth)] == "" {
		t.Fatalf("month-done key not set")
	}
}

func TestAlmanacWSPRBackfillSameGridPairCountsOnce(t *testing.T) {
	h := newWSPRHarness(t, []string{"JO32"}, 1, nil)
	h.markDoneExcept("JO32", wsprTestMonth)
	h.stub.rowsFor = func(from, to int64) []wsprStubRow {
		if from/86400 == wsprDay(2026, 8, 1) {
			return []wsprStubRow{{Slot: 0, Band: 14, Rx4: "JO32", Tx4: "JO32", C: 5}}
		}
		return nil
	}
	if err := h.bf.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The daily table de-duplicates identical keys per spot; so does the backfill.
	if seg := wsprSeg(t, h.store, "JO32", "20m", "EU", wsprTestMonth); seg == nil || seg[0] != 5 {
		t.Fatalf("same-grid pair = %v, want 5 (not 10)", seg)
	}
}

func TestAlmanacWSPRBackfillNeverRequestsCurrentMonth(t *testing.T) {
	h := newWSPRHarness(t, []string{"JO32"}, 1, nil)
	if err := h.bf.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := h.stub.reqs()
	if len(reqs) == 0 {
		t.Fatal("no requests issued")
	}
	curStart := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Unix()
	oldest := time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC).Unix()
	for _, r := range reqs {
		if r.To > curStart {
			t.Fatalf("request reaches into the current month: %d..%d", r.From, r.To)
		}
		if r.From < oldest {
			t.Fatalf("request older than 1 year: %d", r.From)
		}
	}
	// Newest-first: the first request is for the newest complete month.
	if first := time.Unix(reqs[0].From, 0).UTC(); first.Year() != 2026 || first.Month() != time.August {
		t.Fatalf("first request for %v, want August 2026", first)
	}
	if got := h.store.commits; got != 12 {
		t.Fatalf("committed %d months, want 12", got)
	}
}

func TestAlmanacWSPRBackfillCompletedMonthIssuesNoRequest(t *testing.T) {
	h := newWSPRHarness(t, []string{"JO32"}, 1, nil)
	h.markDoneExcept("JO32", 0) // every month done
	if err := h.bf.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(h.stub.reqs()); n != 0 {
		t.Fatalf("completed months issued %d requests, want 0", n)
	}
}

func TestAlmanacWSPRBackfillGlobalTotalsFetchedOncePerMonth(t *testing.T) {
	h := newWSPRHarness(t, []string{"JO32", "JO62"}, 1, nil)
	for _, a := range []string{"JO32", "JO62"} {
		h.markDoneExcept(a, wsprTestMonth)
	}
	if err := h.bf.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	global := 0
	for _, r := range h.stub.reqs() {
		if r.Global {
			global++
		}
	}
	if global != 31 {
		t.Fatalf("global count requests = %d, want 31 (one per day, shared by both areas)", global)
	}
}

func TestAlmanacWSPRBackfillRerunAfterAbortIsByteIdentical(t *testing.T) {
	// Reference: a clean run.
	ref := newWSPRHarness(t, []string{"JO32"}, 1, nil)
	ref.markDoneExcept("JO32", wsprTestMonth)
	ref.stub.rowsFor = wsprAugRows
	if err := ref.bf.run(context.Background()); err != nil {
		t.Fatal(err)
	}

	h := newWSPRHarness(t, []string{"JO32"}, 1, nil)
	h.markDoneExcept("JO32", wsprTestMonth)
	h.stub.rowsFor = wsprAugRows
	// A row from an earlier ring configuration / earlier data: same ring grid,
	// same month, a region the new data does not produce.
	stale := wsprFakeSeasonKey{"JO33", "80m", "SA", wsprTestMonth, almanacSeasonLayerWSPR}
	h.store.counts[stale] = bytes.Repeat([]byte{9}, almanacSeasonCountsLen)
	// The PSKR layer is never touched by the WSPR backfill.
	pskr := wsprFakeSeasonKey{"JO32", "20m", "NA", wsprTestMonth, almanacSeasonLayerPSKR}
	h.store.counts[pskr] = bytes.Repeat([]byte{1}, almanacSeasonCountsLen)

	// First attempt: the service dies mid-month (from request 20 on).
	h.stub.fail = func(r wsprStubRequest, n int) (int, string, bool) {
		return http.StatusInternalServerError, "boom", n >= 20
	}
	if err := h.bf.run(context.Background()); err == nil {
		t.Fatal("expected abort")
	}
	if h.store.commits != 0 {
		t.Fatalf("aborted month committed %d times", h.store.commits)
	}
	// Re-run with the service healthy.
	h.stub.fail = nil
	if err := h.bf.run(context.Background()); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if _, ok := h.store.counts[stale]; ok {
		t.Fatalf("stale ring row survived the month replace")
	}
	if _, ok := h.store.counts[pskr]; !ok {
		t.Fatalf("PSKR layer row deleted by the WSPR backfill")
	}
	delete(h.store.counts, pskr)
	if !reflect.DeepEqual(h.store.counts, ref.store.counts) {
		t.Fatalf("rerun seasonal rows differ from a clean run")
	}
	if !reflect.DeepEqual(h.store.activity, ref.store.activity) || !reflect.DeepEqual(h.store.ingest, ref.store.ingest) {
		t.Fatalf("rerun activity/ingest differ from a clean run")
	}
	// Running a third time replaces the month again: still byte-identical.
	h.store.meta = map[string]string{}
	h.markDoneExcept("JO32", wsprTestMonth)
	if err := h.bf.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.store.counts, ref.store.counts) {
		t.Fatalf("third run is not byte-identical (double counting?)")
	}
}

func TestAlmanacWSPRBackfillAbortsAfterThreeFailures(t *testing.T) {
	h := newWSPRHarness(t, []string{"JO32"}, 1, nil)
	h.markDoneExcept("JO32", wsprTestMonth)
	h.stub.fail = func(r wsprStubRequest, n int) (int, string, bool) {
		return http.StatusInternalServerError, "internal error", true
	}
	err := h.bf.run(context.Background())
	if !errors.Is(err, errAlmanacWSPRBackfillAborted) {
		t.Fatalf("err = %v, want errAlmanacWSPRBackfillAborted", err)
	}
	if n := len(h.stub.reqs()); n != 3 {
		t.Fatalf("requests = %d, want 3", n)
	}
	if msg := h.store.meta[almanacWSPRLastErrorKey]; !strings.Contains(msg, "500") {
		t.Fatalf("recorded error = %q, want the HTTP 500", msg)
	}
	if h.store.commits != 0 {
		t.Fatal("aborted month must not commit")
	}
	// Exponential backoff between the attempts.
	reqs := h.stub.reqs()
	g1, g2 := reqs[1].At.Sub(reqs[0].At), reqs[2].At.Sub(reqs[1].At)
	if g2 < 2*g1-time.Second || g1 < almanacWSPRBackoffBase {
		t.Fatalf("backoff gaps %v, %v: want exponential from %v", g1, g2, almanacWSPRBackoffBase)
	}
}

func TestAlmanacWSPRBackfillTimeoutSplitsIntoHours(t *testing.T) {
	h := newWSPRHarness(t, []string{"JO32"}, 1, nil)
	h.markDoneExcept("JO32", wsprTestMonth)
	h.stub.rowsFor = wsprAugRows
	day3 := wsprDay(2026, 8, 3) * 86400
	h.stub.fail = func(r wsprStubRequest, n int) (int, string, bool) {
		if !r.Global && r.From == day3 && r.To == day3+86400 {
			return http.StatusInternalServerError,
				"Code: 159. DB::Exception: Timeout exceeded: elapsed 30.1 seconds, maximum: 30. (TIMEOUT_EXCEEDED)", true
		}
		return 0, "", false
	}
	if err := h.bf.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	seen := map[string]int{}
	hourly := 0
	for _, r := range h.stub.reqs() {
		seen[r.Query]++
		if !r.Global && r.From >= day3 && r.To <= day3+86400 && r.To-r.From == 3600 {
			hourly++
		}
	}
	for q, n := range seen {
		if n > 1 {
			t.Fatalf("query re-run %d times unchanged: %s", n, q)
		}
	}
	if hourly != 24 {
		t.Fatalf("hourly ring requests for the timed-out day = %d, want 24", hourly)
	}
	// The split still yields the day's data.
	if seg := wsprSeg(t, h.store, "JO32", "20m", "NA", wsprTestMonth); seg == nil || seg[almanacSegmentOffset(3)+20] != 3 {
		t.Fatalf("split day data missing: %v", seg)
	}
}

func TestAlmanacWSPRBackfillOversizedResponseSplits(t *testing.T) {
	h := newWSPRHarness(t, []string{"JO32"}, 1, nil)
	h.markDoneExcept("JO32", wsprTestMonth)
	h.bf.maxRows = 2
	day5 := wsprDay(2026, 8, 5)
	h.stub.rowsFor = func(from, to int64) []wsprStubRow {
		if from/86400 != day5 {
			return nil
		}
		return []wsprStubRow{
			{Slot: 0, Band: 14, Rx4: "JO32", Tx4: "FN20", C: 1},
			{Slot: 10, Band: 14, Rx4: "JO32", Tx4: "FN20", C: 1},
			{Slot: 20, Band: 14, Rx4: "JO32", Tx4: "FN20", C: 1},
		}
	}
	if err := h.bf.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	seg := wsprSeg(t, h.store, "JO32", "20m", "NA", wsprTestMonth)
	off := almanacSegmentOffset(5)
	if seg == nil || seg[off] != 1 || seg[off+10] != 1 || seg[off+20] != 1 {
		t.Fatalf("oversized day not recovered via hourly split: %v", seg)
	}
}

func TestAlmanacWSPRBackfillPacing(t *testing.T) {
	h := newWSPRHarness(t, []string{"JO32"}, 1, nil)
	h.markDoneExcept("JO32", wsprTestMonth)
	if err := h.bf.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := h.stub.reqs()
	if len(reqs) < 40 {
		t.Fatalf("burst too small to test pacing: %d", len(reqs))
	}
	for i := 0; i+20 < len(reqs); i++ {
		if span := reqs[i+20].At.Sub(reqs[i].At); span < time.Minute {
			t.Fatalf("21 requests within %v (≤ 20/min violated at %d)", span, i)
		}
	}
}

func TestAlmanacWSPRBackfillRefusesWhenDiskFull(t *testing.T) {
	for name, probe := range map[string]func() (float64, error){
		"over 80%":    func() (float64, error) { return 0.85, nil },
		"probe error": func() (float64, error) { return 0, errors.New("statfs failed") },
	} {
		t.Run(name, func(t *testing.T) {
			h := newWSPRHarness(t, []string{"JO32"}, 1, probe)
			err := h.bf.run(context.Background())
			if !errors.Is(err, errAlmanacWSPRDiskFull) {
				t.Fatalf("err = %v, want errAlmanacWSPRDiskFull", err)
			}
			if n := len(h.stub.reqs()); n != 0 {
				t.Fatalf("issued %d requests despite a full disk", n)
			}
		})
	}
}

func TestAlmanacWSPRBackfillRequestURL(t *testing.T) {
	h := newWSPRHarness(t, []string{"JO32"}, 1, nil)
	h.markDoneExcept("JO32", wsprTestMonth)
	if err := h.bf.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, r := range h.stub.reqs() {
		if len(r.URL) >= 8192 {
			t.Fatalf("URL is %d bytes, want < 8 KB", len(r.URL))
		}
		req, _ := http.NewRequest(http.MethodGet, r.URL, nil)
		params := req.URL.Query()
		keys := make([]string, 0, len(params))
		for k := range params {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if !reflect.DeepEqual(keys, []string{"query"}) {
			t.Fatalf("URL params = %v, want only query=", keys)
		}
		q := params.Get("query")
		if !strings.Contains(q, "band IN (1,3,5,7,10,14,18,21,24,28)") {
			t.Fatalf("query lacks the band whitelist: %s", q)
		}
		if !r.Global && !strings.Contains(q, "'JO32'") {
			t.Fatalf("ring query lacks the ring: %s", q)
		}
	}
}

func TestAlmanacWSPRBackfillMidStreamTimeoutSplits(t *testing.T) {
	// ClickHouse reports an exception raised after streaming began as trailing
	// text in a 200 body; no partial row from it may be counted.
	h := newWSPRHarness(t, []string{"JO32"}, 1, nil)
	h.markDoneExcept("JO32", wsprTestMonth)
	h.stub.rowsFor = wsprAugRows
	day3 := wsprDay(2026, 8, 3) * 86400
	h.stub.fail = func(r wsprStubRequest, n int) (int, string, bool) {
		if !r.Global && r.From == day3 && r.To == day3+86400 {
			return http.StatusOK, `{"slot":20,"band":14,"rx4":"JO32","tx4":"FN20","c":3}` + "\n" +
				"Code: 159. DB::Exception: Timeout exceeded. (TIMEOUT_EXCEEDED)\n", true
		}
		return 0, "", false
	}
	if err := h.bf.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if seg := wsprSeg(t, h.store, "JO32", "20m", "NA", wsprTestMonth); seg == nil || seg[almanacSegmentOffset(3)+20] != 3 {
		t.Fatalf("slot 20 = %v, want exactly 3 (hourly split, partial response discarded)", seg)
	}
}
