package main

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"
)

// almanac_handler.go serves GET /api/almanac (plan U2) and owns the two-part
// cache (KTD9), keyed by the centre grid4 (a 6-char locator, and a callsign
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
	key        string
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

	mu    sync.Mutex
	lru   *list.List // front = most recent; values *almanacCacheEntry
	items map[string]*list.Element

	slow sync.Mutex // serializes Postgres reads
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
		lru:          list.New(),
		items:        map[string]*list.Element{},
	}
}

// startAlmanacService wires the Postgres reader. The watermark is read from
// the fold driver (in memory) so a fold invalidates the typical cache.
func startAlmanacService(st *dxPostgresStore, resolve func(string) (almanacArea, error)) *almanacService {
	return newAlmanacService(&pgAlmanacReadStore{pool: st.pool}, resolve, func() int64 {
		if f := st.AlmanacFolder(); f != nil {
			return f.health().WatermarkDay
		}
		return -1
	})
}

func almanacHandler(w http.ResponseWriter, r *http.Request) {
	almanacSvc.ServeHTTP(w, r)
}

// ---------------------------------------------------------------------------
// Cache
// ---------------------------------------------------------------------------

func (s *almanacService) typicalValid(e *almanacCacheEntry, now time.Time, win almanacWindow) bool {
	if e == nil || e.typical == nil || now.Sub(e.typicalAt) >= almanacTypicalCacheTTL || e.typical.Window.Today != win.Today {
		return false
	}
	if s.watermark != nil {
		if cur := s.watermark(); cur >= 0 && cur != e.typical.Watermark {
			return false
		}
	}
	return true
}

// entry returns (creating if asked) the entry for key, marking it recent.
// Caller holds s.mu.
func (s *almanacService) entry(key string, create bool) *almanacCacheEntry {
	if el, ok := s.items[key]; ok {
		s.lru.MoveToFront(el)
		return el.Value.(*almanacCacheEntry)
	}
	if !create {
		return nil
	}
	for s.lru.Len() >= almanacCacheMaxEntries {
		back := s.lru.Back()
		s.lru.Remove(back)
		delete(s.items, back.Value.(*almanacCacheEntry).key)
	}
	e := &almanacCacheEntry{key: key}
	s.items[key] = s.lru.PushFront(e)
	return e
}

// peek returns the entry without touching recency (tests/diagnostics).
func (s *almanacService) peek(key string) *almanacCacheEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if el, ok := s.items[key]; ok {
		return el.Value.(*almanacCacheEntry)
	}
	return nil
}

func (s *almanacService) cacheLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lru.Len()
}

// almanacSnapshot is an immutable view of one cache entry.
type almanacSnapshot struct {
	typical   *almanacTypical
	typicalAt time.Time
	today     *almanacToday
	todayAt   time.Time
}

// fresh returns the snapshot when both parts are servable without a read.
// Caller holds s.mu.
func (s *almanacService) fresh(e *almanacCacheEntry, now time.Time, win almanacWindow) (almanacSnapshot, bool, bool) {
	if !s.typicalValid(e, now, win) {
		return almanacSnapshot{}, false, false
	}
	snap := almanacSnapshot{e.typical, e.typicalAt, e.today, e.todayAt}
	todayOK := now.Sub(e.todayAt) < almanacTodayCacheTTL || now.Sub(e.todayErrAt) < almanacNegCacheTTL
	return snap, true, todayOK
}

// get returns the lanes and today overlay for grid4, reading Postgres only
// when the cache can't serve it.
func (s *almanacService) get(grid4 string) (almanacSnapshot, error) {
	if s == nil || s.store == nil {
		return almanacSnapshot{}, errAlmanacUnavailable
	}
	now := s.now()
	win := almanacWindowFor(now.Unix())

	s.mu.Lock()
	e := s.entry(grid4, false)
	if snap, ok, todayOK := s.fresh(e, now, win); ok && todayOK {
		s.mu.Unlock()
		return snap, nil
	}
	if e != nil && !s.typicalValid(e, now, win) && now.Sub(e.errAt) < almanacNegCacheTTL {
		s.mu.Unlock()
		return almanacSnapshot{}, errAlmanacUnavailable
	}
	s.mu.Unlock()

	s.slow.Lock()
	defer s.slow.Unlock()

	// Re-check: a request queued ahead of us may have filled the entry.
	s.mu.Lock()
	e = s.entry(grid4, true)
	snap, typOK, todayOK := s.fresh(e, now, win)
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
	acc, err := readAlmanacAccum(ctx, s.store, grid4, win, typOK)

	s.mu.Lock()
	defer s.mu.Unlock()
	e = s.entry(grid4, true)
	if typOK {
		// Overlay refresh only; on failure keep serving the old overlay.
		if err != nil {
			logInfo("almanac %s: today overlay read failed: %v", grid4, err)
			e.todayErrAt = now
		} else {
			e.today = acc.today(radius)
			e.todayAt = now
		}
		return almanacSnapshot{e.typical, e.typicalAt, e.today, e.todayAt}, nil
	}
	if err != nil {
		logInfo("almanac %s: read failed: %v", grid4, err)
		e.errAt = now
		return almanacSnapshot{}, errAlmanacUnavailable
	}
	typ := computeAlmanacTypical(acc)
	e.typical, e.typicalAt, e.errAt = typ, now, time.Time{}
	e.today, e.todayAt, e.todayErrAt = acc.today(typ.Radius), now, time.Time{}
	return almanacSnapshot{e.typical, e.typicalAt, e.today, e.todayAt}, nil
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
	el, ok := s.items[area.Grid4]
	if !ok {
		s.mu.Unlock()
		return nil, false
	}
	e := el.Value.(*almanacCacheEntry)
	if !s.typicalValid(e, now, almanacWindowFor(now.Unix())) {
		s.mu.Unlock()
		return nil, false
	}
	snap := almanacSnapshot{e.typical, e.typicalAt, e.today, e.todayAt}
	s.mu.Unlock()
	return buildAlmanacResponse(area.Grid4, area, snap, now), true
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
}

// almanacResponse is the /api/almanac body (documented in docs/api.md).
type almanacResponse struct {
	QTH          string               `json:"qth"`
	Area         almanacAreaInfo      `json:"area"`
	Window       almanacWindowInfo    `json:"window"`
	SlotMinutes  int                  `json:"slot_minutes"`
	MMin         int                  `json:"m_min"`
	K            int                  `json:"k"`
	UsuallyShare float64              `json:"usually_share"`
	WatermarkDay int64                `json:"watermark_day"`
	NowSlot      int                  `json:"now_slot"`
	GeneratedAt  int64                `json:"generated_at"`
	TodayAsOf    int64                `json:"today_as_of"`
	Lanes        []almanacLaneJSON    `json:"lanes"`
	Agenda       []almanacAgendaEntry `json:"agenda"`
}

func almanacDayString(day int64) string {
	return time.Unix(day*86400, 0).UTC().Format("2006-01-02")
}

func buildAlmanacResponse(qth string, area almanacArea, snap almanacSnapshot, now time.Time) *almanacResponse {
	typ := snap.typical
	u := now.UTC()
	nowMin := u.Hour()*60 + u.Minute()
	nowSlot := nowMin / 30
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
		SlotMinutes:  30,
		MMin:         almanacMinActiveDays30,
		K:            almanacOpenMinSpotsPSKR,
		UsuallyShare: almanacUsuallyShare,
		WatermarkDay: typ.Watermark,
		NowSlot:      nowSlot,
		GeneratedAt:  snap.typicalAt.Unix(),
		Lanes:        make([]almanacLaneJSON, 0, len(typ.Lanes)),
		Agenda:       almanacAgenda(typ, snap.today, nowMin),
	}
	if !snap.todayAt.IsZero() {
		resp.TodayAsOf = snap.todayAt.Unix()
	}
	for _, l := range typ.Lanes {
		resp.Lanes = append(resp.Lanes, almanacLaneJSON{
			Band: l.Band, Region: l.Region, N: l.N, M: l.M,
			OpenToday: snap.today.openNow(l.Band, l.Region, nowSlot),
		})
	}
	return resp
}

// ServeHTTP: GET /api/almanac?qth=<locator|callsign>.
// 400 missing/invalid qth, 404 unresolvable callsign, 503 when the data is
// unavailable (no Postgres, timeout, negative cache).
func (s *almanacService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	qth, _ := resolveQTHQuery(r)
	if qth == "" {
		http.Error(w, "qth required", http.StatusBadRequest)
		return
	}
	if s == nil || s.store == nil {
		http.Error(w, "almanac unavailable (no Postgres)", http.StatusServiceUnavailable)
		return
	}
	area, err := s.resolve(qth)
	switch {
	case errors.Is(err, errAlmanacInvalidQTH):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case errors.Is(err, errAlmanacUnresolved):
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case err != nil:
		http.Error(w, "qth resolution failed", http.StatusInternalServerError)
		return
	}
	snap, err := s.get(area.Grid4)
	if err != nil {
		w.Header().Set("Retry-After", "30")
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	body, err := json.Marshal(buildAlmanacResponse(qth, area, snap, s.now()))
	if err != nil {
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "max-age=60")
	_, _ = w.Write(body)
}
