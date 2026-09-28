package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// U4 seasonal drill-down tests. They reuse the U2 in-memory read transaction
// (fakeAggStore in almanac_test.go): seasonal rows per layer, the PSKR daily
// tail, ingest-slot totals per layer and lost days.
//
// Clock: aggTestNow = 2026-10-15 12:30 UTC; the watermark sits at today−3
// (2026-10-12), so Oct 1–12 are folded and Oct 13–14 live in the daily tail.

func seasonDay(y int, m time.Month, d int) int64 {
	return utcDayIndex(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix())
}

// setLayerIngest marks every slot of [from, to] alive for layer.
func (f *fakeAggStore) setLayerIngest(layer string, from, to int64, total int64) {
	for d := from; d <= to; d++ {
		for s := 0; s < almanacSeasonSlotsPerDay; s++ {
			f.ingest[aggIngestKey{d, s, layer}] = total
		}
	}
}

// pskrDays puts count spots JO32 → region on band in slot on each day of
// [from, to] (folded or tail depending on the watermark) and makes those
// days alive in the PSKR ingest totals.
func (f *fakeAggStore) pskrDays(band, region string, from, to int64, slot int, count int64) {
	for d := from; d <= to; d++ {
		f.add("JO32", band, region, d, slot, count)
	}
	f.setLayerIngest(almanacSeasonLayerPSKR, from, to, 1000)
}

// wsprDays is pskrDays for the backfilled WSPR layer (seasonal record only).
func (f *fakeAggStore) wsprDays(grid, band, region string, from, to int64, slot int, count int64) {
	for d := from; d <= to; d++ {
		f.addSeason(grid, band, region, almanacSeasonLayerWSPR, d, slot, count)
	}
	f.setLayerIngest(almanacSeasonLayerWSPR, from, to, 500)
}

func readSeason(t *testing.T, f *fakeAggStore, radius int, band, region string) *almanacSeason {
	t.Helper()
	s, err := readAlmanacSeason(context.Background(), f, "JO32", radius, band, region, aggTestNow,
		almanacWSPRCoverage([]string{"JO32"}), -1)
	if err != nil {
		t.Fatalf("readAlmanacSeason: %v", err)
	}
	return s
}

func seasonMonthOf(t *testing.T, s *almanacSeason, m time.Month) almanacSeasonMonth {
	t.Helper()
	if len(s.Months) != 12 {
		t.Fatalf("months = %d, want 12", len(s.Months))
	}
	row := s.Months[int(m)-1]
	if row.Month != int(m) {
		t.Fatalf("row %d has month %d", int(m)-1, row.Month)
	}
	return row
}

func TestAlmanacSeasonPSKRBeatsOlderWSPR(t *testing.T) {
	f := newFakeAggStore(aggToday() - 3)
	f.pskrDays("20m", "NA", seasonDay(2026, 10, 1), seasonDay(2026, 10, 12), 26, 3)
	f.wsprDays("JO32", "20m", "NA", seasonDay(2025, 10, 1), seasonDay(2025, 10, 31), 26, 1)

	oct := seasonMonthOf(t, readSeason(t, f, 0, "20m", "NA"), time.October)
	if oct.Year != 2026 || oct.Layer != almanacSeasonLayerPSKR || oct.Days != 12 || oct.K != almanacOpenMinSpotsPSKR {
		t.Fatalf("oct = year %d layer %q days %d k %d", oct.Year, oct.Layer, oct.Days, oct.K)
	}
	if oct.N[26] != 12 || oct.M[26] != 12 {
		t.Fatalf("slot 26 = %d/%d, want 12/12", oct.N[26], oct.M[26])
	}
	// Slot 25 is active through its successor slot (activity rule) but closed.
	if oct.N[25] != 0 || oct.M[25] != 12 {
		t.Fatalf("slot 25 = %d/%d, want 0/12", oct.N[25], oct.M[25])
	}
	if oct.M[10] != 0 {
		t.Fatalf("quiet slot 10 m = %d, want 0 (unknown, not closed)", oct.M[10])
	}
}

func TestAlmanacSeasonThinPSKRFallsBackToWSPR(t *testing.T) {
	f := newFakeAggStore(aggToday() - 3)
	f.pskrDays("20m", "NA", seasonDay(2026, 9, 1), seasonDay(2026, 9, 5), 26, 3)
	f.wsprDays("JO32", "20m", "NA", seasonDay(2025, 9, 1), seasonDay(2025, 9, 30), 26, 1)

	sep := seasonMonthOf(t, readSeason(t, f, 0, "20m", "NA"), time.September)
	if sep.Year != 2025 || sep.Layer != almanacSeasonLayerWSPR || sep.Days != 30 || sep.K != almanacOpenMinSpotsWSPR {
		t.Fatalf("sep = year %d layer %q days %d k %d", sep.Year, sep.Layer, sep.Days, sep.K)
	}
	// k=1 for WSPR: a single spot per slot is an opening.
	if sep.N[26] != 30 || sep.M[26] != 30 {
		t.Fatalf("slot 26 = %d/%d, want 30/30", sep.N[26], sep.M[26])
	}
}

func TestAlmanacSeasonCurrentMonthJoinsFoldedAndTail(t *testing.T) {
	for _, foldBetween := range []bool{false, true} {
		f := newFakeAggStore(aggToday() - 3)
		// Oct 1–12 folded, Oct 13–14 in the daily tail, Oct 15 = today (partial,
		// excluded).
		f.pskrDays("20m", "NA", seasonDay(2026, 10, 1), seasonDay(2026, 10, 15), 26, 3)
		// One spot (below k=2) in slot 30 every day: a day counted twice
		// would sum to 2 and read as open.
		for d := seasonDay(2026, 10, 1); d <= seasonDay(2026, 10, 15); d++ {
			f.add("JO32", "20m", "NA", d, 30, 1)
		}
		if foldBetween {
			// A fold of Oct 13 commits between the watermark read and the data
			// reads; its daily rows stay until the prune, as in production.
			f.afterWatermark = func(f *fakeAggStore) { f.foldDay(aggToday() - 2) }
		}
		oct := seasonMonthOf(t, readSeason(t, f, 0, "20m", "NA"), time.October)
		if oct.Year != 2026 || oct.Layer != almanacSeasonLayerPSKR || oct.Days != 14 {
			t.Fatalf("fold=%v: oct = year %d layer %q days %d, want 2026 pskr 14", foldBetween, oct.Year, oct.Layer, oct.Days)
		}
		if oct.N[26] != 14 || oct.M[26] != 14 {
			t.Fatalf("fold=%v: slot 26 = %d/%d, want 14/14 (no day twice, today excluded)", foldBetween, oct.N[26], oct.M[26])
		}
		if oct.N[30] != 0 || oct.M[30] != 14 {
			t.Fatalf("fold=%v: slot 30 = %d/%d, want 0/14 (a double-counted day would open)", foldBetween, oct.N[30], oct.M[30])
		}
	}
}

func TestAlmanacSeasonEmptyMonthNotCollected(t *testing.T) {
	f := newFakeAggStore(aggToday() - 3)
	f.pskrDays("20m", "NA", seasonDay(2026, 10, 1), seasonDay(2026, 10, 12), 26, 3)
	s := readSeason(t, f, 0, "20m", "NA")
	nov := seasonMonthOf(t, s, time.November)
	if nov.Year != 0 || nov.Layer != "" || nov.Days != 0 {
		t.Fatalf("nov = %+v, want not collected", nov)
	}
	body, _ := json.Marshal(almanacSeasonMonthToJSON(nov, false))
	var row map[string]any
	_ = json.Unmarshal(body, &row)
	if row["status"] != "not_collected" || row["year"] != nil || row["layer"] != nil || row["n"] != nil || row["m"] != nil {
		t.Fatalf("nov json = %s", body)
	}
}

// AE5: after two weeks of runtime, months before collection started show the
// backfilled WSPR layer, labelled as WSPR.
func TestAlmanacSeasonAE5BackfilledDecemberWSPR(t *testing.T) {
	f := newFakeAggStore(aggToday() - 3)
	f.pskrDays("20m", "OC", seasonDay(2026, 10, 1), seasonDay(2026, 10, 14), 20, 2)
	f.wsprDays("JO32", "20m", "OC", seasonDay(2025, 12, 1), seasonDay(2025, 12, 31), 20, 1)

	s := readSeason(t, f, 0, "20m", "OC")
	dec := seasonMonthOf(t, s, time.December)
	if dec.Year != 2025 || dec.Layer != almanacSeasonLayerWSPR || dec.Days != 31 || dec.N[20] != 31 {
		t.Fatalf("dec = year %d layer %q days %d n20 %d", dec.Year, dec.Layer, dec.Days, dec.N[20])
	}
	oct := seasonMonthOf(t, s, time.October)
	if oct.Year != 2026 || oct.Layer != almanacSeasonLayerPSKR || oct.Days != 14 {
		t.Fatalf("oct = year %d layer %q days %d", oct.Year, oct.Layer, oct.Days)
	}
}

func TestAlmanacSeasonMostRecentQualifyingYear(t *testing.T) {
	f := newFakeAggStore(aggToday() - 3)
	f.wsprDays("JO32", "20m", "NA", seasonDay(2023, 6, 1), seasonDay(2023, 6, 30), 26, 1)
	f.wsprDays("JO32", "20m", "NA", seasonDay(2024, 6, 1), seasonDay(2024, 6, 30), 26, 1)
	f.wsprDays("JO32", "20m", "NA", seasonDay(2025, 6, 1), seasonDay(2025, 6, 4), 26, 1) // too few days
	jun := seasonMonthOf(t, readSeason(t, f, 0, "20m", "NA"), time.June)
	if jun.Year != 2024 || jun.Layer != almanacSeasonLayerWSPR {
		t.Fatalf("jun = year %d layer %q, want 2024 wspr", jun.Year, jun.Layer)
	}
}

// Spots to other regions make the area active (m) but never open (n); the
// WSPR layer is alive only from its own ingest totals; lost PSKR days are
// never alive; squares outside the radius are ignored.
func TestAlmanacSeasonRulesPerLayer(t *testing.T) {
	f := newFakeAggStore(aggToday() - 3)
	f.pskrDays("20m", "EU", seasonDay(2026, 8, 1), seasonDay(2026, 8, 31), 26, 5)
	f.lost[seasonDay(2026, 8, 3)] = true
	// WSPR May 2025 rows but no WSPR ingest totals: never alive.
	for d := seasonDay(2025, 5, 1); d <= seasonDay(2025, 5, 31); d++ {
		f.addSeason("JO32", "20m", "NA", almanacSeasonLayerWSPR, d, 26, 4)
	}
	f.setLayerIngest(almanacSeasonLayerPSKR, seasonDay(2025, 5, 1), seasonDay(2025, 5, 31), 1000)
	// Ring-1 square JO33 only: invisible at radius 0.
	f.wsprDays("JO33", "20m", "NA", seasonDay(2025, 3, 1), seasonDay(2025, 3, 31), 26, 1)

	s := readSeason(t, f, 0, "20m", "NA")
	aug := seasonMonthOf(t, s, time.August)
	if aug.Layer != almanacSeasonLayerPSKR || aug.Days != 30 || aug.M[26] != 30 || aug.N[26] != 0 {
		t.Fatalf("aug = layer %q days %d slot26 %d/%d, want pskr 30 days 0/30", aug.Layer, aug.Days, aug.N[26], aug.M[26])
	}
	if may := seasonMonthOf(t, s, time.May); may.Year != 0 {
		t.Fatalf("may without WSPR ingest = %+v, want not collected", may)
	}
	if mar := seasonMonthOf(t, s, time.March); mar.Year != 0 {
		t.Fatalf("mar at radius 0 = year %d, want not collected", mar.Year)
	}
	s1 := readSeason(t, f, 1, "20m", "NA")
	if mar := seasonMonthOf(t, s1, time.March); mar.Year != 2025 || mar.N[26] != 31 {
		t.Fatalf("mar at radius 1 = year %d n %d", mar.Year, mar.N[26])
	}
	if len(s1.Squares) != 9 || len(s.Squares) != 1 {
		t.Fatalf("squares r0=%v r1=%v", s.Squares, s1.Squares)
	}
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func almanacSeasonGet(s *almanacService, query string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.ServeSeason(rec, httptest.NewRequest(http.MethodGet, "/api/almanac/season"+query, nil))
	return rec
}

func TestAlmanacSeasonHandlerStatusCodes(t *testing.T) {
	s, _, _ := newTestAlmanacService(populatedAggWorld())
	cases := []struct {
		q    string
		want int
	}{
		{"?band=20m&region=NA", http.StatusBadRequest},
		{"?qth=XX99&band=20m&region=NA", http.StatusBadRequest},
		{"?qth=JO32&region=NA", http.StatusBadRequest},
		{"?qth=JO32&band=6m&region=NA", http.StatusBadRequest},
		{"?qth=JO32&band=20m", http.StatusBadRequest},
		{"?qth=JO32&band=20m&region=ZZ", http.StatusBadRequest},
		{"?qth=NOCALL1&band=20m&region=NA", http.StatusNotFound},
		{"?qth=JO32&band=20m&region=NA", http.StatusOK},
		{"?qth=jo32ab&band=20M&region=na", http.StatusOK},
	}
	for _, c := range cases {
		if rec := almanacSeasonGet(s, c.q); rec.Code != c.want {
			t.Fatalf("%s: %d, want %d (%s)", c.q, rec.Code, c.want, rec.Body.String())
		}
	}

	noStore := newAlmanacService(nil, func(q string) (almanacArea, error) { return resolveAlmanacAreaWith(q, nil, nil) }, nil)
	if rec := almanacSeasonGet(noStore, "?qth=JO32&band=20m&region=NA"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no store: %d", rec.Code)
	}
	var nilSvc *almanacService
	if rec := almanacSeasonGet(nilSvc, "?qth=JO32&band=20m&region=NA"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil service: %d", rec.Code)
	}

	f := populatedAggWorld()
	f.block = true
	blocked, _, _ := newTestAlmanacService(f)
	rec := almanacSeasonGet(blocked, "?qth=JO32&band=20m&region=NA")
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("timeout: %d retry-after %q", rec.Code, rec.Header().Get("Retry-After"))
	}
}

func TestAlmanacSeasonHandlerShapeAndRadiusAgreement(t *testing.T) {
	f := populatedAggWorld()
	f.wsprDays("JO32", "20m", "NA", seasonDay(2025, 12, 1), seasonDay(2025, 12, 31), 26, 1)
	s, _, _ := newTestAlmanacService(f)

	landing := almanacGet(s, "JO32")
	var lb struct {
		Area struct {
			Radius int `json:"radius"`
		} `json:"area"`
	}
	_ = json.Unmarshal(landing.Body.Bytes(), &lb)

	rec := almanacSeasonGet(s, "?qth=JO32AB&band=20m&region=NA")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		QTH  string `json:"qth"`
		Area struct {
			Grid4   string   `json:"grid4"`
			Source  string   `json:"source"`
			Radius  int      `json:"radius"`
			Squares []string `json:"squares"`
		} `json:"area"`
		Band         string `json:"band"`
		Region       string `json:"region"`
		SlotMinutes  int    `json:"slot_minutes"`
		MMin         int    `json:"m_min"`
		K            map[string]int
		WatermarkDay int64 `json:"watermark_day"`
		Months       []struct {
			Month  int     `json:"month"`
			Name   string  `json:"name"`
			Status string  `json:"status"`
			Year   *int    `json:"year"`
			Layer  *string `json:"layer"`
			Days   int     `json:"days"`
			K      *int    `json:"k"`
			N      []int   `json:"n"`
			M      []int   `json:"m"`
		} `json:"months"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	if body.Area.Grid4 != "JO32" || body.Band != "20m" || body.Region != "NA" || body.MMin != almanacMinActiveDaysSeasonal ||
		body.SlotMinutes != 30 || body.WatermarkDay != aggToday()-3 {
		t.Fatalf("header = %+v", body)
	}
	if body.Area.Radius != lb.Area.Radius {
		t.Fatalf("season radius %d != landing radius %d", body.Area.Radius, lb.Area.Radius)
	}
	if len(body.Months) != 12 {
		t.Fatalf("months = %d", len(body.Months))
	}
	for i, m := range body.Months {
		if m.Month != i+1 || m.Name == "" {
			t.Fatalf("row %d = %+v", i, m)
		}
	}
	dec := body.Months[11]
	if dec.Status != "ok" || dec.Year == nil || *dec.Year != 2025 || dec.Layer == nil || *dec.Layer != "wspr" ||
		dec.K == nil || *dec.K != 1 || len(dec.N) != 48 || len(dec.M) != 48 || dec.N[26] != 31 {
		t.Fatalf("dec = %+v", dec)
	}
	oct := body.Months[9]
	if oct.Status != "ok" || *oct.Layer != "pskr" || *oct.Year != 2026 || oct.Days != 14 || oct.N[26] != 8 {
		// populatedAggWorld's 20m/NA openings run Sep 15 – Oct 8.
		t.Fatalf("oct = %+v", oct)
	}
	if jan := body.Months[0]; jan.Status != "not_collected" || jan.Year != nil || jan.N != nil {
		t.Fatalf("jan = %+v", jan)
	}
}

func TestAlmanacSeasonCacheAndWatermarkInvalidation(t *testing.T) {
	f := populatedAggWorld()
	s, clk, wm := newTestAlmanacService(f)
	q := "?qth=JO32&band=20m&region=NA"
	almanacSeasonGet(s, q)
	calls := f.txCalls
	clk.advance(60 * time.Second)
	almanacSeasonGet(s, "?qth=JO32XX&band=20m&region=NA")
	if f.txCalls != calls {
		t.Fatalf("tx calls %d → %d, want cached", calls, f.txCalls)
	}
	// Another band is its own entry.
	almanacSeasonGet(s, "?qth=JO32&band=40m&region=NA")
	if f.txCalls != calls+1 {
		t.Fatalf("tx calls = %d, want %d (new band key)", f.txCalls, calls+1)
	}
	*wm = *wm + 1
	before := f.txCalls
	almanacSeasonGet(s, q)
	// The landing typical part and the season entry are both re-read.
	if f.txCalls != before+2 {
		t.Fatalf("tx calls = %d, want %d after watermark change", f.txCalls, before+2)
	}
}

// TestAlmanacSeasonWSPRGatedByConfiguredAreas: the WSPR layer is read only
// when every chosen square lies within a configured backfill area's r=2
// ring. An adjacent unconfigured centre (JO33 at r=2 when JO32 is
// configured) reaches outside the backfilled ring and gets no WSPR months;
// the configured centre still does.
func TestAlmanacSeasonWSPRGatedByConfiguredAreas(t *testing.T) {
	f := newFakeAggStore(aggToday() - 3)
	f.wsprDays("JO33", "20m", "NA", seasonDay(2025, 9, 1), seasonDay(2025, 9, 30), 26, 1)
	cov := almanacWSPRCoverage([]string{"JO32"})
	read := func(centre string, cov map[string]bool) *almanacSeason {
		t.Helper()
		s, err := readAlmanacSeason(context.Background(), f, centre, almanacMaxWidenRadius, "20m", "NA", aggTestNow, cov, -1)
		if err != nil {
			t.Fatalf("readAlmanacSeason(%s): %v", centre, err)
		}
		return s
	}

	if sep := seasonMonthOf(t, read("JO32", cov), time.September); sep.Layer != almanacSeasonLayerWSPR || sep.Year != 2025 {
		t.Fatalf("configured JO32: sep = year %d layer %q, want 2025 wspr", sep.Year, sep.Layer)
	}
	for name, c := range map[string]map[string]bool{"JO33 vs JO32 configured": cov, "no areas": nil} {
		centre := "JO33"
		if name == "no areas" {
			centre = "JO32"
		}
		for _, m := range read(centre, c).Months {
			if m.Layer == almanacSeasonLayerWSPR || m.Year != 0 {
				t.Fatalf("%s: month %d = year %d layer %q, want not collected", name, m.Month, m.Year, m.Layer)
			}
		}
	}
}
