package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Preliminary SNR-floor views: while fewer than M_min days carry SNR data, a
// floored view uses an effective M_min = min(M_min, max(floor, covered SNR
// days)) instead of reading "not enough data" everywhere.

// prelimWorld mirrors prod on 2026-09-28 (relative to aggTestNow): SNR data
// since today−3, fold watermark today−2 (today−3/−2 in the seasonal record,
// today−1 in the daily tail), JO32 active on every band all window, and 3
// spots at −5 dB to NA on 20m slot 20 on every covered day.
func prelimWorld(sinceBack int64) *fakeAggStore {
	today := aggToday()
	f := newFakeAggStore(today - 2)
	f.setIngest(today-31, today, 1000)
	f.snrSince, f.hasSNRSince = today-sinceBack, true
	f.activeAllBands("JO32", today-30, today, allSlots())
	for d := today - sinceBack; d <= today-1; d++ {
		if d < today-30 {
			continue
		}
		f.addSNR("JO32", "20m", "NA", d, 20, 3, -5)
	}
	return f
}

// The since-day, watermark and tail boundaries count every covered day once:
// with 3 covered days (2 folded + 1 tail) M is 3, not 2.
func TestAlmanacFloorCoveredDaysAcrossWatermark(t *testing.T) {
	typ := computeAggTier(t, prelimWorld(3), 1)
	l := findLane(t, typ, "20m", "NA")
	for s := 0; s < almanacSlotsPerDay; s++ {
		if l.M[s] != 3 {
			t.Fatalf("slot %d: m=%d, want 3 (since-day, folded day and tail day all count)", s, l.M[s])
		}
	}
	if l.N[20] != 3 {
		t.Fatalf("slot 20: n=%d, want 3", l.N[20])
	}
}

func TestAlmanacPreliminaryEffectiveMMin(t *testing.T) {
	cases := []struct {
		sinceBack   int64
		wantDays    int
		wantMMin    int
		preliminary bool
	}{
		{0, 0, almanacPreliminaryMinActiveDays, true}, // since = today: nothing covered yet
		{1, 1, almanacPreliminaryMinActiveDays, true}, // floor of 2
		{3, 3, 3, true},
		{9, 9, 9, true},
		{10, 10, almanacMinActiveDays30, false},
		{40, 30, almanacMinActiveDays30, false},
	}
	for _, c := range cases {
		typ := computeAggTier(t, prelimWorld(c.sinceBack), 1)
		if typ.SNRDays != c.wantDays || typ.MMin != c.wantMMin || typ.preliminary() != c.preliminary {
			t.Errorf("since today-%d: snr_days=%d m_min=%d prelim=%v, want %d/%d/%v",
				c.sinceBack, typ.SNRDays, typ.MMin, typ.preliminary(), c.wantDays, c.wantMMin, c.preliminary)
		}
	}
	// Any SNR: the full M_min, never preliminary, whatever the SNR start.
	typ := computeAggTier(t, prelimWorld(3), -1)
	if typ.MMin != almanacMinActiveDays30 || typ.preliminary() {
		t.Fatalf("any SNR: m_min=%d prelim=%v", typ.MMin, typ.preliminary())
	}
	// No SNR collection at all: the floor stays at the full M_min (unknown
	// everywhere, as before), not preliminary.
	f := prelimWorld(3)
	f.hasSNRSince = false
	if typ := computeAggTier(t, f, 1); typ.MMin != almanacMinActiveDays30 || typ.preliminary() || typ.SNRDays != 0 {
		t.Fatalf("no SNR data: m_min=%d prelim=%v days=%d", typ.MMin, typ.preliminary(), typ.SNRDays)
	}
}

func TestAlmanacPreliminaryLostDayNotCovered(t *testing.T) {
	f := prelimWorld(3)
	f.lost[aggToday()-3] = true
	typ := computeAggTier(t, f, 1)
	if typ.SNRDays != 2 || typ.MMin != 2 {
		t.Fatalf("lost covered day: snr_days=%d m_min=%d, want 2/2", typ.SNRDays, typ.MMin)
	}
}

// A covered day without any ingest (an outage: no almanac_ingest_slots rows,
// but not recorded lost) is not alive anywhere, so it can't count toward M
// and must not count toward snr_days either: otherwise M_min_eff = 3 while
// every M ≤ 2 and the preliminary view would stay empty.
func TestAlmanacPreliminaryDeadDayNotCovered(t *testing.T) {
	f := prelimWorld(3)
	dead := aggToday() - 2
	for s := 0; s < almanacSlotsPerDay; s++ {
		delete(f.ingest, aggIngestKey{dead, s, almanacSeasonLayerPSKR})
	}
	typ := computeAggTier(t, f, 1)
	l := findLane(t, typ, "20m", "NA")
	if typ.SNRDays != 2 || typ.MMin != 2 || l.M[20] != 2 || l.unknown(20) || !l.usual(20) {
		t.Fatalf("dead covered day: snr_days=%d m_min=%d m=%d unknown=%v", typ.SNRDays, typ.MMin, l.M[20], l.unknown(20))
	}
}

// The SNR start day is usually only partly covered (backfill from raw spots
// starts mid-day). It adds to M only in the slots it covers, so it must not
// count toward the effective M_min either: otherwise every slot before the
// start time sits one day short (M=2 < M_min_eff=3) and reads "not enough
// data" around the current time of day.
func TestAlmanacPreliminaryPartialStartDayNotCovered(t *testing.T) {
	f := prelimWorld(3)
	start := aggToday() - 3
	for s := 0; s < 28; s++ {
		delete(f.ingest, aggIngestKey{start, s, almanacSeasonLayerPSKR})
	}
	typ := computeAggTier(t, f, 1)
	l := findLane(t, typ, "20m", "NA")
	if typ.SNRDays != 2 || typ.MMin != 2 || l.M[20] != 2 || l.unknown(20) || !l.usual(20) {
		t.Fatalf("partial start day: snr_days=%d m_min=%d m=%d unknown=%v", typ.SNRDays, typ.MMin, l.M[20], l.unknown(20))
	}
	// A slot the start day does cover has M=3 and stays known.
	if l.M[30] != 3 || l.unknown(30) {
		t.Fatalf("covered slot: m=%d unknown=%v", l.M[30], l.unknown(30))
	}
}

func TestAlmanacPreliminaryCellsAndAgenda(t *testing.T) {
	typ := computeAggTier(t, prelimWorld(3), 1)
	l := findLane(t, typ, "20m", "NA")
	if l.unknown(20) || !l.usual(20) {
		t.Fatalf("slot 20 (3/3 with effective M_min 3) must be known and usual")
	}
	if l.unknown(21) || l.usual(21) {
		t.Fatalf("slot 21 (0/3) must be known (closed)")
	}
	ag := almanacAgenda(typ, nil, 9*60) // 09:00 UTC: slot 20 (10:00) starts in 60 min
	found := false
	for _, e := range ag {
		if e.Band == "20m" && e.Region == "NA" && e.StartSlot == 20 && e.PeakN == 3 && e.PeakM == 3 {
			found = true
		}
	}
	if !found {
		t.Fatalf("preliminary agenda lacks 20m/NA at 10:00: %+v", ag)
	}
	// Same data at the full M_min: unknown, no agenda (the old behaviour).
	full := *typ
	full.Lanes = append([]almanacLane(nil), typ.Lanes...)
	for i := range full.Lanes {
		full.Lanes[i].MMin = almanacMinActiveDays30
	}
	if len(almanacAgenda(&full, nil, 9*60)) != 0 {
		t.Fatalf("full M_min must leave the agenda empty")
	}
}

// The widening radius is chosen on the all-SNR activity: a floored view uses
// the same radius as the any-SNR view.
func TestAlmanacPreliminaryRadiusUnchanged(t *testing.T) {
	today := aggToday()
	f := newFakeAggStore(today - 2)
	f.setIngest(today-31, today, 1000)
	f.snrSince, f.hasSNRSince = today-3, true
	// Centre JO32 sparse (5 days); the ring-1 neighbours fill the window.
	f.activeAllBands("JO32", today-5, today-1, allSlots())
	for _, sq := range getSquaresWithinRings("JO32", 1) {
		if sq != "JO32" {
			f.activeAllBands(sq, today-30, today-1, allSlots())
		}
	}
	anySNR := computeAggTier(t, f, -1)
	floored := computeAggTier(t, f, 1)
	if anySNR.Radius != 1 || floored.Radius != anySNR.Radius {
		t.Fatalf("radius any=%d floored=%d, want 1/1", anySNR.Radius, floored.Radius)
	}
}

func TestAlmanacHandlerPreliminaryFields(t *testing.T) {
	s, _, _ := newTestAlmanacService(prelimWorld(3))
	get := func(q string) map[string]json.RawMessage {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/almanac?qth=JO32"+q, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d %s", q, rec.Code, rec.Body)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	body := get("&min_snr=-15")
	if string(body["m_min"]) != "3" || string(body["snr_days"]) != "3" || string(body["preliminary"]) != "true" {
		t.Fatalf("floored: m_min=%s snr_days=%s preliminary=%s", body["m_min"], body["snr_days"], body["preliminary"])
	}
	body = get("")
	if string(body["m_min"]) != "10" {
		t.Fatalf("any SNR m_min = %s", body["m_min"])
	}
	if _, ok := body["snr_days"]; ok {
		t.Fatalf("any SNR must not carry snr_days")
	}
	if _, ok := body["preliminary"]; ok {
		t.Fatalf("any SNR must not carry preliminary")
	}
	// A full floored view: snr_days reported, no preliminary flag.
	s, _, _ = newTestAlmanacService(prelimWorld(12))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/almanac?qth=JO32&min_snr=-15", nil))
	if !strings.Contains(rec.Body.String(), `"snr_days":12`) || strings.Contains(rec.Body.String(), `"preliminary"`) ||
		!strings.Contains(rec.Body.String(), `"m_min":10`) {
		t.Fatalf("full floored body: %s", rec.Body)
	}
}

// Seasonal drill-down: a month with fewer than M_min SNR-covered days uses
// min(M_min, max(floor, covered days in that month)).
func TestAlmanacSeasonPreliminaryMonth(t *testing.T) {
	f := newFakeAggStore(aggToday() - 3)
	oct1, oct14 := seasonDay(2026, 10, 1), seasonDay(2026, 10, 14)
	f.snrSince, f.hasSNRSince = seasonDay(2026, 10, 11), true // Oct 11–14: 4 covered days
	for d := oct1; d <= oct14; d++ {
		f.addSNR("JO32", "20m", "NA", d, 26, 3, -3)
	}
	f.setLayerIngest(almanacSeasonLayerPSKR, oct1, oct14, 1000)
	res, err := readAlmanacSeason(context.Background(), f, "JO32", 0, "20m", "NA", aggTestNow, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	oct := seasonMonthOf(t, res, time.October)
	if oct.Year != 2026 || oct.Days != 4 || oct.MMin != 4 || oct.SNRDays != 4 || oct.M[26] != 4 || oct.N[26] != 4 {
		t.Fatalf("oct: year %d days %d m_min %d snr_days %d slot26 %d/%d", oct.Year, oct.Days, oct.MMin, oct.SNRDays, oct.N[26], oct.M[26])
	}
	// Any SNR: the full seasonal M_min (14 days qualify).
	res, err = readAlmanacSeason(context.Background(), f, "JO32", 0, "20m", "NA", aggTestNow, nil, -1)
	if err != nil {
		t.Fatal(err)
	}
	if oct := seasonMonthOf(t, res, time.October); oct.MMin != almanacMinActiveDaysSeasonal || oct.Days != 14 {
		t.Fatalf("any SNR oct: m_min %d days %d", oct.MMin, oct.Days)
	}
	// A dead covered day (no ingest) is not covered: Oct 11–14 minus Oct 12.
	for s := 0; s < almanacSlotsPerDay; s++ {
		delete(f.ingest, aggIngestKey{seasonDay(2026, 10, 12), s, almanacSeasonLayerPSKR})
	}
	res, err = readAlmanacSeason(context.Background(), f, "JO32", 0, "20m", "NA", aggTestNow, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if oct := seasonMonthOf(t, res, time.October); oct.SNRDays != 3 || oct.MMin != 3 || oct.Days != 3 || oct.Year != 2026 {
		t.Fatalf("dead day oct: snr_days %d m_min %d days %d", oct.SNRDays, oct.MMin, oct.Days)
	}
	f.setLayerIngest(almanacSeasonLayerPSKR, oct1, oct14, 1000)
	// One covered day: the floor of 2 keeps the month out.
	f.snrSince = oct14
	res, err = readAlmanacSeason(context.Background(), f, "JO32", 0, "20m", "NA", aggTestNow, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if oct := seasonMonthOf(t, res, time.October); oct.Year != 0 {
		t.Fatalf("1 covered day must stay not_collected: %+v", oct)
	}

	// Handler: per-month m_min / snr_days / preliminary, top-level preliminary.
	f.snrSince = seasonDay(2026, 10, 11)
	s, _, _ := newTestAlmanacService(f)
	rec := almanacSeasonGet(s, "?qth=JO32&band=20m&region=NA&min_snr=-10")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	var body struct {
		MMin        int  `json:"m_min"`
		Preliminary bool `json:"preliminary"`
		Months      []struct {
			Month       int `json:"month"`
			Status      string
			MMin        *int `json:"m_min"`
			SNRDays     *int `json:"snr_days"`
			Preliminary bool `json:"preliminary"`
		} `json:"months"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	o := body.Months[9]
	if body.MMin != almanacMinActiveDaysSeasonal || !body.Preliminary || o.Status != "ok" || o.MMin == nil || *o.MMin != 4 ||
		o.SNRDays == nil || *o.SNRDays != 4 || !o.Preliminary {
		t.Fatalf("season body: %s", rec.Body)
	}
	if body.Months[0].MMin != nil || body.Months[0].Preliminary {
		t.Fatalf("not-collected month must carry no m_min / preliminary")
	}
	rec = almanacSeasonGet(s, "?qth=JO32&band=20m&region=NA")
	if strings.Contains(rec.Body.String(), `"preliminary"`) || strings.Contains(rec.Body.String(), `"snr_days"`) {
		t.Fatalf("any-SNR season must carry no preliminary fields: %s", rec.Body)
	}
}

func TestAlmanacEffectiveMMin(t *testing.T) {
	for _, c := range []struct{ full, covered, want int }{
		{10, 0, 2}, {10, 1, 2}, {10, 2, 2}, {10, 7, 7}, {10, 10, 10}, {10, 30, 10}, {8, 5, 5}, {8, 9, 8},
	} {
		if got := almanacEffectiveMMin(c.full, c.covered); got != c.want {
			t.Errorf("effective(%d, %d) = %d, want %d", c.full, c.covered, got, c.want)
		}
	}
}
