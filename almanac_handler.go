package main

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// almanac_handler.go serves GET /api/almanac (plan U2) and owns the two-part
// cache (KTD9), keyed by the centre grid4 plus the SNR tier (KTD13; the
// any-SNR entry is keyed by the bare grid4 — a 6-char locator, and a callsign
// resolving into the same square, share one entry):
//
//   - typical part (n/m lanes + radius): 6 h, or until the fold watermark
//     changes, or the UTC day rolls (the window moves);
//   - today overlay (KTD11): 120 s; refreshed with a tail-only read.
//
// prop_intel guards: 1.5 s per read transaction, 30 s negative cache per
// key, one serialized slow path, LRU of 256 entries.

// errAlmanacUnavailable: the read failed or timed out (or is negative-cached).
var errAlmanacUnavailable = errors.New("almanac: data temporarily unavailable")

type almanacCacheEntry struct {
	typical    *almanacTypical
	typicalAt  time.Time
	errAt      time.Time
	today      *almanacToday
	todayAt    time.Time
	todayErrAt time.Time
}

type almanacService struct {
	store     almanacReadStore
	resolve   func(qth string) (almanacArea, error)
	watermark func() int64 // current fold watermark, -1 unknown; may be nil

	now          func() time.Time
	queryTimeout time.Duration

	mu  sync.Mutex
	lru *almanacLRU[almanacCacheEntry]

	slow sync.Mutex // serializes Postgres reads

	season *almanacSeasonCache // /api/almanac/season entries (almanac_season.go)

	// wsprCoverage is the set of grid4 squares whose WSPR layer the backfill
	// writes: the r=almanacMaxWidenRadius ring of each configured
	// -almanac-wspr-backfill-areas centre. The drill-down reads the WSPR
	// layer only when every chosen square is covered. nil = none.
	wsprCoverage map[string]bool
}

// almanacSvc is the process-wide service (nil without Postgres → 503).
var almanacSvc *almanacService

func newAlmanacService(st almanacReadStore, resolve func(string) (almanacArea, error), watermark func() int64) *almanacService {
	return &almanacService{
		store:        st,
		resolve:      resolve,
		watermark:    watermark,
		now:          time.Now,
		queryTimeout: almanacQueryTimeout,
		lru:          newAlmanacLRU[almanacCacheEntry](almanacCacheMaxEntries),
		season:       newAlmanacSeasonCache(),
	}
}

// startAlmanacService wires the Postgres reader. The watermark is read from
// the fold driver (in memory) so a fold invalidates the typical cache.
// wsprAreas are the parsed -almanac-wspr-backfill-areas (drill-down WSPR gate).
func startAlmanacService(st *dxPostgresStore, resolve func(string) (almanacArea, error), wsprAreas []string) *almanacService {
	s := newAlmanacService(&pgAlmanacReadStore{pool: st.pool, snrColumns: st.snrColumns.Load}, resolve, func() int64 {
		if f := st.AlmanacFolder(); f != nil {
			return f.health().WatermarkDay
		}
		return -1
	})
	s.wsprCoverage = almanacWSPRCoverage(wsprAreas)
	return s
}

// almanacWSPRCoverage is the union of the backfill rings of areas.
func almanacWSPRCoverage(areas []string) map[string]bool {
	if len(areas) == 0 {
		return nil
	}
	cov := map[string]bool{}
	for _, a := range areas {
		for _, sq := range getSquaresWithinRings(almanacNormalizeCentre(a), almanacMaxWidenRadius) {
			cov[sq] = true
		}
	}
	return cov
}

// almanacWSPRCovered: every square lies within some configured backfill ring.
func almanacWSPRCovered(squares []string, cov map[string]bool) bool {
	if len(squares) == 0 || len(cov) == 0 {
		return false
	}
	for _, sq := range squares {
		if !cov[sq] {
			return false
		}
	}
	return true
}

func almanacHandler(w http.ResponseWriter, r *http.Request) {
	almanacSvc.ServeHTTP(w, r)
}

// ---------------------------------------------------------------------------
// Cache
// ---------------------------------------------------------------------------

// watermarkCurrent: a cached part computed at fold watermark wm is still
// current (the live watermark is unknown or unchanged).
func (s *almanacService) watermarkCurrent(wm int64) bool {
	if s.watermark != nil {
		if cur := s.watermark(); cur >= 0 && cur != wm {
			return false
		}
	}
	return true
}

func (s *almanacService) typicalValid(e *almanacCacheEntry, now time.Time, win almanacWindow) bool {
	return e != nil && e.typical != nil && now.Sub(e.typicalAt) < almanacTypicalCacheTTL &&
		e.typical.Window.Today == win.Today && s.watermarkCurrent(e.typical.Watermark)
}

// entry returns (creating if asked) the entry for key, marking it recent.
// Caller holds s.mu.
func (s *almanacService) entry(key string, create bool) *almanacCacheEntry {
	return s.lru.get(key, create)
}

// purgeCaches drops every cached /api/almanac and /api/almanac/season
// entry (the SNR backfill calls it after lowering the SNR start, which the
// watermark-keyed caches would otherwise miss for up to their TTL).
func (s *almanacService) purgeCaches() {
	if s == nil {
		return
	}
	// Wait out an in-flight Postgres read so it can't refill the new cache
	// with a result read under the old SNR start.
	s.slow.Lock()
	defer s.slow.Unlock()
	s.mu.Lock()
	s.lru = newAlmanacLRU[almanacCacheEntry](almanacCacheMaxEntries)
	s.mu.Unlock()
	if s.season != nil {
		s.season.mu.Lock()
		s.season.lru = newAlmanacLRU[almanacSeasonEntry](almanacCacheMaxEntries)
		s.season.mu.Unlock()
	}
}

// peek returns the entry without touching recency (tests/diagnostics).
func (s *almanacService) peek(key string) *almanacCacheEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lru.peek(key)
}

func (s *almanacService) cacheLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lru.len()
}

// almanacSnapshot is an immutable view of one cache entry.
type almanacSnapshot struct {
	typical   *almanacTypical
	typicalAt time.Time
	today     *almanacToday
	todayAt   time.Time
}

// snapshot copies the entry's current parts. Caller holds s.mu.
func (e *almanacCacheEntry) snapshot() almanacSnapshot {
	return almanacSnapshot{e.typical, e.typicalAt, e.today, e.todayAt}
}

// fresh returns the snapshot when both parts are servable without a read.
// Caller holds s.mu.
func (s *almanacService) fresh(e *almanacCacheEntry, now time.Time, win almanacWindow) (almanacSnapshot, bool, bool) {
	if !s.typicalValid(e, now, win) {
		return almanacSnapshot{}, false, false
	}
	snap := e.snapshot()
	todayOK := now.Sub(e.todayAt) < almanacTodayCacheTTL || now.Sub(e.todayErrAt) < almanacNegCacheTTL
	return snap, true, todayOK
}

// almanacCacheKey is the cache key of (grid4, SNR tier): the bare grid4 for
// any SNR (tier -1), so the widget summary (warm) keeps finding it.
func almanacCacheKey(grid4 string, tier int) string {
	if tier < 0 {
		return grid4
	}
	return grid4 + "|snr" + strconv.Itoa(almanacSNRTierFloors[tier])
}

// get returns the lanes and today overlay for grid4 at SNR tier (-1 = any
// SNR), reading Postgres only when the cache can't serve it.
func (s *almanacService) get(grid4 string, tier int) (almanacSnapshot, error) {
	if s == nil || s.store == nil {
		return almanacSnapshot{}, errAlmanacUnavailable
	}
	now := s.now()
	win := almanacWindowFor(now.Unix())
	key := almanacCacheKey(grid4, tier)

	s.mu.Lock()
	e := s.entry(key, false)
	snap, typOK, todayOK := s.fresh(e, now, win)
	if typOK && todayOK {
		s.mu.Unlock()
		return snap, nil
	}
	if e != nil && !typOK && now.Sub(e.errAt) < almanacNegCacheTTL {
		s.mu.Unlock()
		return almanacSnapshot{}, errAlmanacUnavailable
	}
	s.mu.Unlock()

	s.slow.Lock()
	defer s.slow.Unlock()

	// Re-check: a request queued ahead of us may have filled the entry.
	s.mu.Lock()
	e = s.entry(key, true)
	snap, typOK, todayOK = s.fresh(e, now, win)
	if typOK && todayOK {
		s.mu.Unlock()
		return snap, nil
	}
	if !typOK && now.Sub(e.errAt) < almanacNegCacheTTL {
		s.mu.Unlock()
		return almanacSnapshot{}, errAlmanacUnavailable
	}
	radius := 0
	if typOK {
		radius = e.typical.Radius
	}
	s.mu.Unlock()

	// Detached from the request context: a client hang-up must not poison
	// the negative cache.
	ctx, cancel := context.WithTimeout(context.Background(), s.queryTimeout)
	defer cancel()
	acc, err := readAlmanacAccum(ctx, s.store, grid4, win, typOK, tier)

	s.mu.Lock()
	defer s.mu.Unlock()
	e = s.entry(key, true)
	if typOK {
		// Overlay refresh only; on failure keep serving the old overlay.
		if err != nil {
			logInfo("almanac %s: today overlay read failed: %v", key, err)
			e.todayErrAt = now
		} else {
			e.today = acc.today(radius)
			e.todayAt = now
		}
		return e.snapshot(), nil
	}
	if err != nil {
		logInfo("almanac %s: read failed: %v", key, err)
		e.errAt = now
		return almanacSnapshot{}, errAlmanacUnavailable
	}
	typ := computeAlmanacTypical(acc)
	e.typical, e.typicalAt, e.errAt = typ, now, time.Time{}
	e.today, e.todayAt, e.todayErrAt = acc.today(typ.Radius), now, time.Time{}
	return e.snapshot(), nil
}

// typicalRadius returns the landing view's radius for grid4. A valid cached
// typical part is served as is (marking the entry recent, like get) without
// forcing a today-overlay refresh; only otherwise does it take get()'s full
// read path.
func (s *almanacService) typicalRadius(grid4 string) (int, error) {
	now := s.now()
	win := almanacWindowFor(now.Unix())
	s.mu.Lock()
	if e := s.entry(grid4, false); s.typicalValid(e, now, win) {
		radius := e.typical.Radius
		s.mu.Unlock()
		return radius, nil
	}
	s.mu.Unlock()
	snap, err := s.get(grid4, -1)
	if err != nil {
		return 0, err
	}
	return snap.typical.Radius, nil
}

// warm returns the response for area only when the typical part is cached
// and valid; it never reads Postgres (widget summary field, KTD10/U6). The
// today overlay may be older than 120 s (see today_as_of).
func (s *almanacService) warm(area almanacArea) (*almanacResponse, bool) {
	if s == nil {
		return nil, false
	}
	now := s.now()
	s.mu.Lock()
	e := s.lru.peek(area.Grid4)
	if !s.typicalValid(e, now, almanacWindowFor(now.Unix())) {
		s.mu.Unlock()
		return nil, false
	}
	snap := e.snapshot()
	s.mu.Unlock()
	return buildAlmanacResponse(area.Grid4, area, snap, now, nil), true
}

// ---------------------------------------------------------------------------
// Response
// ---------------------------------------------------------------------------

type almanacAreaInfo struct {
	Grid4       string   `json:"grid4"`
	Source      string   `json:"source"`
	Approximate bool     `json:"approximate"`
	Radius      int      `json:"radius"`
	Squares     []string `json:"squares"`
}

type almanacWindowInfo struct {
	StartDay      string `json:"start_day"`
	EndDay        string `json:"end_day"`
	Days          int    `json:"days"`
	StartDayIndex int64  `json:"start_day_index"`
	EndDayIndex   int64  `json:"end_day_index"`
}

type almanacLaneJSON struct {
	Band      string                    `json:"band"`
	Region    string                    `json:"region"`
	N         [almanacSlotsPerDay]uint8 `json:"n"`
	M         [almanacSlotsPerDay]uint8 `json:"m"`
	OpenToday bool                      `json:"open_today"`
	// Share (SNR floor only, KTD13): per slot the pooled share of spots at
	// or above the floor, null where the slot has no SNR data.
	Share []*float64 `json:"share,omitempty"`
}

// almanacShareJSON decodes a lane's coded shares (nil array when none).
func almanacShareJSON(codes *[almanacSlotsPerDay]uint16) []*float64 {
	out := make([]*float64, almanacSlotsPerDay)
	for i, c := range codes {
		if v, ok := almanacShareValue(c); ok {
			out[i] = &v
		}
	}
	return out
}

// almanacSNRInfo is the SNR part of both Almanac responses (KTD13).
type almanacSNRInfo struct {
	// MinSNR is the requested floor (dB), SNRTier the applied tier floor;
	// both null for any SNR.
	MinSNR  *int `json:"min_snr"`
	SNRTier *int `json:"snr_tier"`
	// SNRAvailable: SNR data is being collected (the columns exist and the
	// collection start is recorded); SNRSince is that start (UTC day).
	SNRAvailable bool    `json:"snr_available"`
	SNRSince     *string `json:"snr_since"`
}

func newAlmanacSNRInfo(minSNR *int, tier int, since int64, hasSince bool) almanacSNRInfo {
	info := almanacSNRInfo{SNRAvailable: hasSince}
	if minSNR != nil && tier >= 0 {
		v, t := *minSNR, almanacSNRTierFloors[tier]
		info.MinSNR, info.SNRTier = &v, &t
	}
	if hasSince {
		d := almanacDayString(since)
		info.SNRSince = &d
	}
	return info
}

// almanacResponse is the /api/almanac body (documented in docs/api.md).
type almanacResponse struct {
	QTH         string            `json:"qth"`
	Area        almanacAreaInfo   `json:"area"`
	Window      almanacWindowInfo `json:"window"`
	SlotMinutes int               `json:"slot_minutes"`
	// MMin is the effective M_min the lanes and agenda were judged against:
	// with an SNR floor and fewer than 10 SNR days, the preliminary
	// min(10, max(2, snr_days)); Preliminary is then true.
	MMin int `json:"m_min"`
	// SNRDays: window days carrying SNR data (SNR-floored views only).
	SNRDays      *int    `json:"snr_days,omitempty"`
	Preliminary  bool    `json:"preliminary,omitempty"`
	K            int     `json:"k"`
	UsuallyShare float64 `json:"usually_share"`
	WatermarkDay int64   `json:"watermark_day"`
	NowSlot      int     `json:"now_slot"`
	GeneratedAt  int64   `json:"generated_at"`
	TodayAsOf    int64   `json:"today_as_of"`
	almanacSNRInfo
	Lanes  []almanacLaneJSON    `json:"lanes"`
	Agenda []almanacAgendaEntry `json:"agenda"`
}

func almanacDayString(day int64) string {
	return time.Unix(day*86400, 0).UTC().Format("2006-01-02")
}

// buildAlmanacResponse renders snap; minSNR is the requested SNR floor (nil
// = any SNR; the applied tier is the snapshot's).
func buildAlmanacResponse(qth string, area almanacArea, snap almanacSnapshot, now time.Time, minSNR *int) *almanacResponse {
	typ := snap.typical
	u := now.UTC()
	nowMin := u.Hour()*60 + u.Minute()
	nowSlot := nowMin / almanacSlotMinutes
	resp := &almanacResponse{
		QTH: qth,
		Area: almanacAreaInfo{
			Grid4:       typ.Centre,
			Source:      string(area.Source),
			Approximate: area.Approximate(),
			Radius:      typ.Radius,
			Squares:     typ.Squares,
		},
		Window: almanacWindowInfo{
			StartDay:      almanacDayString(typ.Window.Start),
			EndDay:        almanacDayString(typ.Window.End),
			Days:          almanacWindowDays,
			StartDayIndex: typ.Window.Start,
			EndDayIndex:   typ.Window.End,
		},
		SlotMinutes:    almanacSlotMinutes,
		MMin:           typ.mMin(),
		Preliminary:    typ.preliminary(),
		K:              almanacOpenMinSpotsPSKR,
		UsuallyShare:   almanacUsuallyShare,
		WatermarkDay:   typ.Watermark,
		NowSlot:        nowSlot,
		GeneratedAt:    snap.typicalAt.Unix(),
		almanacSNRInfo: newAlmanacSNRInfo(minSNR, typ.Tier, typ.SNRSince, typ.HasSNRSince),
		Lanes:          make([]almanacLaneJSON, 0, len(typ.Lanes)),
		Agenda:         almanacAgenda(typ, snap.today, nowMin),
	}
	if !snap.todayAt.IsZero() {
		resp.TodayAsOf = snap.todayAt.Unix()
	}
	if typ.Tier >= 0 {
		d := typ.SNRDays
		resp.SNRDays = &d
	}
	for _, l := range typ.Lanes {
		lj := almanacLaneJSON{
			Band: l.Band, Region: l.Region, N: l.N, M: l.M,
			OpenToday: snap.today.openNow(l.Band, l.Region, nowSlot),
		}
		if typ.Tier >= 0 {
			lj.Share = almanacShareJSON(&l.Share)
		}
		resp.Lanes = append(resp.Lanes, lj)
	}
	return resp
}

// ServeHTTP: GET /api/almanac?qth=<locator|callsign>[&min_snr=<dB>].
// 400 missing/invalid qth or min_snr, 404 unresolvable callsign, 503 when the
// data is unavailable (no Postgres, timeout, negative cache).
func (s *almanacService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var minSNR *int
	tier := -1
	qth, area, ok := s.resolveRequest(w, r, func() (err error) {
		minSNR, tier, err = almanacParseMinSNR(r)
		return err
	})
	if !ok {
		return
	}
	snap, err := s.get(area.Grid4, tier)
	if err != nil {
		writeAlmanacUnavailable(w, err)
		return
	}
	writeAlmanacJSON(w, buildAlmanacResponse(qth, area, snap, s.now(), minSNR))
}

// SNR floor parameter (KTD13).
const (
	almanacMinSNRLowest  = -40
	almanacMinSNRHighest = 30
)

// almanacSNRTierFor snaps a floor (dB) to the nearest almanacSNRTierFloors
// index; a tie goes to the stricter (higher) tier.
func almanacSNRTierFor(db int) int {
	best := 0
	for i, f := range almanacSNRTierFloors {
		d, bd := db-f, db-almanacSNRTierFloors[best]
		if d < 0 {
			d = -d
		}
		if bd < 0 {
			bd = -bd
		}
		if d <= bd {
			best = i
		}
	}
	return best
}

// almanacParseMinSNR reads the optional min_snr parameter: absent/empty =
// any SNR (nil, -1); otherwise an integer in [−40, +30] dB snapped to its
// tier. Anything else is an error (400).
func almanacParseMinSNR(r *http.Request) (*int, int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("min_snr"))
	if raw == "" {
		return nil, -1, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < almanacMinSNRLowest || v > almanacMinSNRHighest {
		return nil, -1, fmt.Errorf("min_snr must be an integer from %d to %d (dB)", almanacMinSNRLowest, almanacMinSNRHighest)
	}
	return &v, almanacSNRTierFor(v), nil
}

// resolveRequest is the shared /api/almanac* request prelude: GET/HEAD only
// (405), qth required (400), then validate when non-nil (400 with its error;
// before the no-Postgres 503 so parameter errors win), 503 without Postgres,
// and qth resolution (400 invalid, 404 unresolvable, 500 otherwise). ok is
// false when a response has been written.
func (s *almanacService) resolveRequest(w http.ResponseWriter, r *http.Request, validate func() error) (qth string, area almanacArea, ok bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return "", almanacArea{}, false
	}
	qth, _ = resolveQTHQuery(r)
	if qth == "" {
		http.Error(w, "qth required", http.StatusBadRequest)
		return "", almanacArea{}, false
	}
	if validate != nil {
		if err := validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return "", almanacArea{}, false
		}
	}
	if s == nil || s.store == nil {
		http.Error(w, "almanac unavailable (no Postgres)", http.StatusServiceUnavailable)
		return "", almanacArea{}, false
	}
	area, err := s.resolve(qth)
	switch {
	case errors.Is(err, errAlmanacInvalidQTH):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "", almanacArea{}, false
	case errors.Is(err, errAlmanacUnresolved):
		http.Error(w, err.Error(), http.StatusNotFound)
		return "", almanacArea{}, false
	case err != nil:
		http.Error(w, "qth resolution failed", http.StatusInternalServerError)
		return "", almanacArea{}, false
	}
	return qth, area, true
}

// writeAlmanacUnavailable writes the 503 for a failed/negative-cached read.
func writeAlmanacUnavailable(w http.ResponseWriter, err error) {
	w.Header().Set("Retry-After", "30")
	http.Error(w, err.Error(), http.StatusServiceUnavailable)
}

// writeAlmanacJSON writes v as a 60 s cacheable JSON body (500 when it can't
// be encoded).
func writeAlmanacJSON(w http.ResponseWriter, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "max-age=60")
	_, _ = w.Write(body)
}

// ---------------------------------------------------------------------------
// LRU
// ---------------------------------------------------------------------------

// almanacLRU is a string-keyed LRU of *T (front = most recent), holding at
// most cap entries. Not synchronized: callers hold their own lock.
type almanacLRU[T any] struct {
	cap   int
	ll    *list.List // values *almanacLRUItem[T]
	items map[string]*list.Element
}

type almanacLRUItem[T any] struct {
	key string
	val *T
}

func newAlmanacLRU[T any](cap int) *almanacLRU[T] {
	return &almanacLRU[T]{cap: cap, ll: list.New(), items: map[string]*list.Element{}}
}

// get returns key's value marking it recent; when absent it returns nil, or
// with create evicts least-recent entries down to cap−1 and inserts a zero T.
func (c *almanacLRU[T]) get(key string, create bool) *T {
	if el, ok := c.items[key]; ok {
		c.ll.MoveToFront(el)
		return el.Value.(*almanacLRUItem[T]).val
	}
	if !create {
		return nil
	}
	for c.ll.Len() >= c.cap {
		back := c.ll.Back()
		c.ll.Remove(back)
		delete(c.items, back.Value.(*almanacLRUItem[T]).key)
	}
	v := new(T)
	c.items[key] = c.ll.PushFront(&almanacLRUItem[T]{key: key, val: v})
	return v
}

// peek returns key's value (nil when absent) without touching recency.
func (c *almanacLRU[T]) peek(key string) *T {
	if el, ok := c.items[key]; ok {
		return el.Value.(*almanacLRUItem[T]).val
	}
	return nil
}

func (c *almanacLRU[T]) len() int { return c.ll.Len() }
