package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type aggClock struct{ t time.Time }

func (c *aggClock) now() time.Time          { return c.t }
func (c *aggClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// newTestAlmanacService wires a service to a fake store, a locator-only
// resolver (callsign "NOCALL1" is unresolvable) and a settable watermark.
func newTestAlmanacService(f *fakeAggStore) (*almanacService, *aggClock, *int64) {
	clk := &aggClock{t: aggTestNow}
	wm := f.wm
	s := newAlmanacService(f,
		func(qth string) (almanacArea, error) {
			if strings.EqualFold(qth, "NOCALL1") {
				return almanacArea{}, errAlmanacUnresolved
			}
			return resolveAlmanacAreaWith(qth, nil, nil)
		},
		func() int64 { return wm })
	s.now = clk.now
	s.queryTimeout = 20 * time.Millisecond
	return s, clk, &wm
}

func almanacGet(s *almanacService, qth string) *httptest.ResponseRecorder {
	u := "/api/almanac"
	if qth != "" {
		u += "?qth=" + qth
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, u, nil))
	return rec
}

func populatedAggWorld() *fakeAggStore {
	f := newAggWorld()
	win := almanacWindowFor(aggTestNow.Unix())
	f.activeAllBands("JO32", win.Start, win.Today, allSlots())
	for d := win.Start; d < win.Start+24; d++ {
		for s := 26; s < 36; s++ {
			f.add("JO32", "20m", "NA", d, s, 3)
		}
	}
	return f
}

func TestAlmanacHandlerStatusCodes(t *testing.T) {
	s, _, _ := newTestAlmanacService(populatedAggWorld())
	if rec := almanacGet(s, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing qth: %d", rec.Code)
	}
	if rec := almanacGet(s, "XX99"); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid locator: %d", rec.Code)
	}
	if rec := almanacGet(s, "NOCALL1"); rec.Code != http.StatusNotFound {
		t.Fatalf("unresolvable callsign: %d", rec.Code)
	}
	if rec := almanacGet(s, "JO32"); rec.Code != http.StatusOK {
		t.Fatalf("JO32: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAlmanacHandlerResponseShape(t *testing.T) {
	s, _, _ := newTestAlmanacService(populatedAggWorld())
	rec := almanacGet(s, "jo32ab")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		QTH  string `json:"qth"`
		Area struct {
			Grid4       string   `json:"grid4"`
			Source      string   `json:"source"`
			Approximate bool     `json:"approximate"`
			Radius      int      `json:"radius"`
			Squares     []string `json:"squares"`
		} `json:"area"`
		Window struct {
			StartDay string `json:"start_day"`
			EndDay   string `json:"end_day"`
			Days     int    `json:"days"`
		} `json:"window"`
		MMin    int `json:"m_min"`
		K       int `json:"k"`
		NowSlot int `json:"now_slot"`
		Lanes   []struct {
			Band      string `json:"band"`
			Region    string `json:"region"`
			N         []int  `json:"n"`
			M         []int  `json:"m"`
			OpenToday bool   `json:"open_today"`
		} `json:"lanes"`
		Agenda []struct {
			Band        string `json:"band"`
			Region      string `json:"region"`
			Start       string `json:"start"`
			End         string `json:"end"`
			Status      string `json:"status"`
			StartsInMin int    `json:"starts_in_min"`
			PeakN       int    `json:"peak_n"`
			PeakM       int    `json:"peak_m"`
			OpenToday   bool   `json:"open_today"`
		} `json:"agenda"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	if body.Area.Grid4 != "JO32" || body.Area.Source != "locator" || body.Area.Approximate || body.Area.Radius != 0 {
		t.Fatalf("area = %+v", body.Area)
	}
	if body.Window.Days != 30 || body.Window.StartDay != "2026-09-15" || body.Window.EndDay != "2026-10-14" {
		t.Fatalf("window = %+v", body.Window)
	}
	if body.MMin != almanacMinActiveDays30 || body.K != almanacOpenMinSpotsPSKR || body.NowSlot != 25 {
		t.Fatalf("m_min=%d k=%d now_slot=%d", body.MMin, body.K, body.NowSlot)
	}
	var found bool
	for _, l := range body.Lanes {
		if len(l.N) != 48 || len(l.M) != 48 {
			t.Fatalf("lane %s/%s arrays %d/%d", l.Band, l.Region, len(l.N), len(l.M))
		}
		if l.Band == "20m" && l.Region == "NA" {
			found = true
			if l.N[26] != 24 || l.M[26] != 30 {
				t.Fatalf("20m/NA slot 26 = %d/%d", l.N[26], l.M[26])
			}
		}
	}
	if !found {
		t.Fatalf("20m/NA lane missing")
	}
	if len(body.Agenda) == 0 || body.Agenda[0].Band != "20m" || body.Agenda[0].Region != "NA" ||
		body.Agenda[0].Status != "upcoming" || body.Agenda[0].Start != "13:00" || body.Agenda[0].PeakN != 24 {
		t.Fatalf("agenda = %+v", body.Agenda)
	}
}

func TestAlmanacCacheHitAndWatermarkInvalidation(t *testing.T) {
	f := populatedAggWorld()
	s, clk, wm := newTestAlmanacService(f)
	almanacGet(s, "JO32")
	clk.advance(60 * time.Second)
	almanacGet(s, "JO32")
	if f.txCalls != 1 {
		t.Fatalf("store tx calls = %d, want 1 (typical + today cached)", f.txCalls)
	}
	*wm = *wm + 1
	almanacGet(s, "JO32")
	if f.txCalls != 2 {
		t.Fatalf("store tx calls = %d, want 2 after watermark change", f.txCalls)
	}
}

func TestAlmanacCacheTypicalTTL(t *testing.T) {
	f := populatedAggWorld()
	s, clk, _ := newTestAlmanacService(f)
	almanacGet(s, "JO32")
	// Past the 120 s overlay TTL: only the today overlay is re-read.
	clk.advance(almanacTodayCacheTTL + time.Second)
	tailBefore := f.tailCalls
	almanacGet(s, "JO32")
	if f.txCalls != 2 || f.tailCalls != tailBefore+1 {
		t.Fatalf("overlay refresh: tx=%d tail=%d", f.txCalls, f.tailCalls-tailBefore)
	}
	if e := s.peek("JO32"); e == nil || e.typicalAt != aggTestNow {
		t.Fatalf("typical part should not be recomputed by the overlay refresh")
	}
	// Same day, past 6 h (12:30 → 18:32): full recompute.
	clk.advance(almanacTypicalCacheTTL)
	almanacGet(s, "JO32")
	if e := s.peek("JO32"); e == nil || !e.typicalAt.Equal(clk.t) {
		t.Fatalf("typical not recomputed after 6 h")
	}
}

func TestAlmanacCacheSixCharSharesKey(t *testing.T) {
	f := populatedAggWorld()
	s, _, _ := newTestAlmanacService(f)
	almanacGet(s, "JO32AB")
	almanacGet(s, "jo32")
	almanacGet(s, "JO32XX")
	if f.txCalls != 1 {
		t.Fatalf("tx calls = %d, want 1 (one grid4 key)", f.txCalls)
	}
}

func TestAlmanacTimeoutNegativeCache(t *testing.T) {
	f := populatedAggWorld()
	f.block = true
	s, clk, _ := newTestAlmanacService(f)
	if rec := almanacGet(s, "JO32"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("timeout: %d", rec.Code)
	}
	f.block = false
	clk.advance(10 * time.Second)
	if rec := almanacGet(s, "JO32"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("within negative cache: %d", rec.Code)
	}
	if f.txCalls != 1 {
		t.Fatalf("tx calls = %d, want 1 (negative-cached)", f.txCalls)
	}
	clk.advance(almanacNegCacheTTL)
	if rec := almanacGet(s, "JO32"); rec.Code != http.StatusOK {
		t.Fatalf("after negative TTL: %d", rec.Code)
	}
	if f.txCalls != 2 {
		t.Fatalf("tx calls = %d, want 2", f.txCalls)
	}
}

func TestAlmanacNoStore503(t *testing.T) {
	s := newAlmanacService(nil, func(q string) (almanacArea, error) { return resolveAlmanacAreaWith(q, nil, nil) }, nil)
	if rec := almanacGet(s, "JO32"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no store: %d", rec.Code)
	}
	var nilSvc *almanacService
	rec := httptest.NewRecorder()
	nilSvc.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/almanac?qth=JO32", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil service: %d", rec.Code)
	}
}

func TestAlmanacCacheLRU(t *testing.T) {
	f := populatedAggWorld()
	s, _, _ := newTestAlmanacService(f)
	for i := 0; i < almanacCacheMaxEntries+1; i++ {
		g := fmt.Sprintf("%c%c%d%d", 'A'+(i/100)%18, 'A'+(i/10)%10, (i/10)%10, i%10)
		if rec := almanacGet(s, g); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", g, rec.Code)
		}
	}
	if n := s.cacheLen(); n != almanacCacheMaxEntries {
		t.Fatalf("cache len = %d, want %d", n, almanacCacheMaxEntries)
	}
	if s.peek("AA00") != nil {
		t.Fatalf("oldest entry not evicted")
	}
}

// The warm accessor (for the widget summary, U6) never queries the store.
func TestAlmanacWarmOnly(t *testing.T) {
	f := populatedAggWorld()
	s, clk, _ := newTestAlmanacService(f)
	area := almanacArea{Grid4: "JO32", Source: qthSourceLocator}
	if _, ok := s.warm(area); ok {
		t.Fatalf("cold cache reported warm")
	}
	if f.txCalls != 0 {
		t.Fatalf("warm() queried the store")
	}
	almanacGet(s, "JO32")
	resp, ok := s.warm(area)
	if !ok || resp.Area.Grid4 != "JO32" || len(resp.Agenda) == 0 {
		t.Fatalf("warm = %v %+v", ok, resp)
	}
	clk.advance(almanacTypicalCacheTTL + time.Minute)
	if _, ok := s.warm(area); ok {
		t.Fatalf("expired typical reported warm")
	}
	if f.txCalls != 1 {
		t.Fatalf("warm() queried the store")
	}
}
