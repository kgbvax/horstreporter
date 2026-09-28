package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Almanac SNR share (plan KTD13): ingest counters, schema, fold histograms,
// the min_snr floor in the 30-day view and the drill-down, and the WSPR
// backfill bins.

// ---------------------------------------------------------------------------
// Ingest
// ---------------------------------------------------------------------------

func TestRegionDeltaTierBoundaries(t *testing.T) {
	var d regionDelta
	for _, snr := range []int{-21, -20, -16, -15, -11, -10, -6, -5, -1, 0, 5} {
		d.observeSpot(snr, true)
	}
	d.observeSpot(0, false) // DX cluster: RP 0 is not a real SNR
	want := regionDelta{Count: 12, SNR: 11, GE: [almanacSNRTiers]int64{10, 8, 6, 4, 2}}
	if d != want {
		t.Fatalf("delta = %+v, want %+v (tiers inclusive at -20/-15/-10/-5/0)", d, want)
	}
}

func TestObserveSNRCountersExcludeDXCluster(t *testing.T) {
	now := time.Now().Unix()
	spot := func(rp int, md string) MQTTMessage {
		return MQTTMessage{T: now - 60, RP: rp, SC: "K1ABC", SL: "FN31", RC: "DL1ABC", RL: "JO32", B: "20m", MD: md}
	}
	s := newObserveTestStore()
	for _, rp := range []int{-15, -16, 0, -1} {
		_ = s.observe(spot(rp, "FT8"), "20m", 0, 0, 0)
	}
	_ = s.observe(spot(0, "DXCLUSTER"), "20m", 0, 0, 0)
	if len(s.pendingRegion) != 2 {
		t.Fatalf("region keys = %d, want 2", len(s.pendingRegion))
	}
	want := regionDelta{Count: 5, SNR: 4, GE: [almanacSNRTiers]int64{4, 3, 2, 2, 1}}
	for k, d := range s.pendingRegion {
		if d != want {
			t.Fatalf("key %+v: delta %+v, want %+v (DX cluster counts toward spot_count only)", k, d, want)
		}
	}
}

func TestBaselineFlushStmtsSNRColumns(t *testing.T) {
	k := dxPulseRegionBaselineDailyKey{TargetGrid4: "JO32", Band: "20m", SlotOfDay: 36, Region: "NA", DayIndex: 100}
	region := map[dxPulseRegionBaselineDailyKey]regionDelta{
		k: {Count: 7, SNR: 5, GE: [almanacSNRTiers]int64{5, 4, 3, 2, 1}},
	}
	for _, withSNR := range []bool{true, false} {
		var sqls []string
		var regionArgs, totalArgs []any
		baselineFlushStmts(nil, region, nil, withSNR, func(sql string, args ...any) {
			sqls = append(sqls, sql)
			switch {
			case strings.Contains(sql, "INSERT INTO dx_region_baseline_daily"):
				regionArgs = args
			case strings.Contains(sql, "INSERT INTO almanac_ingest_slots"):
				totalArgs = args
			}
		})
		if len(sqls) != 2 {
			t.Fatalf("withSNR=%v: %d statements", withSNR, len(sqls))
		}
		if totalArgs[3] != int64(7) {
			t.Fatalf("ingest totals must stay all-spot counts: %v", totalArgs)
		}
		if !withSNR {
			if len(regionArgs) != 6 || regionArgs[5] != int64(7) || strings.Contains(sqls[0], "snr_") {
				t.Fatalf("degraded flush must write spot_count only: %v %q", regionArgs, sqls[0])
			}
			continue
		}
		want := []any{"JO32", "20m", 36, "NA", int64(100), int64(7), int64(5), int64(5), int64(4), int64(3), int64(2), int64(1)}
		if len(regionArgs) != len(want) {
			t.Fatalf("args = %v", regionArgs)
		}
		for i := range want {
			if regionArgs[i] != want[i] {
				t.Fatalf("arg %d = %v, want %v", i, regionArgs[i], want[i])
			}
		}
		for _, c := range regionSNRColumnNames {
			if !strings.Contains(sqls[0], c+" = dx_region_baseline_daily."+c+" + EXCLUDED."+c) {
				t.Fatalf("upsert must add %s: %q", c, sqls[0])
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Schema
// ---------------------------------------------------------------------------

type fakeSNRSchema struct {
	present            int
	checkErr, alterErr error
	alters             int
	seeded             []int64
}

func (f *fakeSNRSchema) columnsPresent(context.Context) (int, error) { return f.present, f.checkErr }
func (f *fakeSNRSchema) alterWithLockTimeout(context.Context) error {
	f.alters++
	if f.alterErr == nil {
		f.present = len(regionSNRColumnNames)
	}
	return f.alterErr
}
func (f *fakeSNRSchema) seedSince(_ context.Context, day int64) error {
	f.seeded = append(f.seeded, day)
	return nil
}

func TestEnsureRegionSNRColumns(t *testing.T) {
	ctx := context.Background()
	t.Run("present: no ALTER", func(t *testing.T) {
		db := &fakeSNRSchema{present: 6}
		var ready atomic.Bool
		if !ensureRegionSNRColumns(ctx, db, &ready, 100) || !ready.Load() || db.alters != 0 {
			t.Fatalf("present columns must skip the ALTER (it takes the table lock): alters=%d", db.alters)
		}
		if len(db.seeded) != 1 || db.seeded[0] != 101 {
			t.Fatalf("SNR start must be seeded as tomorrow: %v", db.seeded)
		}
	})
	t.Run("missing: ALTER", func(t *testing.T) {
		db := &fakeSNRSchema{present: 3}
		var ready atomic.Bool
		if !ensureRegionSNRColumns(ctx, db, &ready, 100) || !ready.Load() || db.alters != 1 {
			t.Fatalf("missing columns must be added once")
		}
	})
	t.Run("ALTER fails: degrade", func(t *testing.T) {
		db := &fakeSNRSchema{alterErr: errors.New("canceling statement due to lock timeout")}
		var ready atomic.Bool
		if ensureRegionSNRColumns(ctx, db, &ready, 100) || ready.Load() || len(db.seeded) != 0 {
			t.Fatalf("a failed ALTER must leave SNR off and record no start")
		}
	})
	t.Run("check fails: degrade", func(t *testing.T) {
		db := &fakeSNRSchema{checkErr: errors.New("down")}
		var ready atomic.Bool
		if ensureRegionSNRColumns(ctx, db, &ready, 100) || ready.Load() || db.alters != 0 {
			t.Fatalf("a failed check must not ALTER")
		}
	})
}

func TestRegionSNRSchemaSQL(t *testing.T) {
	if strings.Count(regionSNRColumnsAlterSQL, "ALTER TABLE") != 1 {
		t.Fatalf("all SNR columns must be added in ONE statement")
	}
	for _, c := range regionSNRColumnNames {
		if !strings.Contains(regionSNRColumnsAlterSQL, "ADD COLUMN IF NOT EXISTS "+c+" INTEGER NOT NULL DEFAULT 0") {
			t.Fatalf("missing %s: %q", c, regionSNRColumnsAlterSQL)
		}
	}
	if !strings.Contains(regionSNRLockTimeoutSQL, "SET LOCAL lock_timeout = '5s'") {
		t.Fatalf("lock timeout: %q", regionSNRLockTimeoutSQL)
	}
	if !strings.Contains(regionSNRColumnsPresentSQL, "$1::text[]") || !strings.Contains(almanacSNRSinceSeedSQL, "DO NOTHING") {
		t.Fatalf("presence check must cast its array; the start is written once")
	}
	if len(regionSNRColumnNames) != 1+almanacSNRTiers {
		t.Fatalf("one snr_ge column per tier")
	}
}

// ---------------------------------------------------------------------------
// Fold
// ---------------------------------------------------------------------------

func snrDailyRow(grid, band, region string, slot int, n int64, snrs ...int) almanacDailyRow {
	var d regionDelta
	for _, s := range snrs {
		d.observeSpot(s, true)
	}
	return almanacDailyRow{Grid4: grid, Band: band, Region: region, Slot: slot, Count: n, SNR: d.SNR, GE: d.GE}
}

func TestAlmanacFoldWritesHistogramsFromSNRStart(t *testing.T) {
	today := almanacTestToday()
	d1, d2, d3 := today-4, today-3, today-2
	ym, dom1 := almanacYearMonthDOM(d1)
	_, dom2 := almanacYearMonthDOM(d2)
	_, dom3 := almanacYearMonthDOM(d3)
	if ym3, _ := almanacYearMonthDOM(d3); ym3 != ym {
		t.Skip("fixture days straddle a month")
	}
	st := newFakeAlmanacFoldStore()
	st.state.watermark, st.state.hasWatermark = d1-1, true
	st.snrSince, st.hasSNRSince = d2, true
	// d1 is the partial deploy day: its counters must not be stored.
	st.daily[d1] = []almanacDailyRow{snrDailyRow("JO32", "20m", "NA", 10, 3, -3, -3)}
	st.daily[d2] = []almanacDailyRow{snrDailyRow("JO32", "20m", "NA", 10, 4, -22, -12, 1)}
	st.daily[d3] = []almanacDailyRow{snrDailyRow("JO32", "20m", "NA", 11, 2, -7)}
	f := newTestAlmanacFolder(st, finalFlush(today))
	if err := f.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	enc := st.state.counts[fakeSeasonKey{"JO32", "20m", "NA", ym}]
	if len(enc) == 0 || enc[0] != almanacSparseVersion2 {
		t.Fatalf("fold must write v2: %x", enc)
	}
	var dense [almanacSeasonCountsLen]byte
	var hist [almanacSeasonCountsLen]almanacSNRHist
	if err := almanacSparseDecodeHist(enc, &dense, &hist); err != nil {
		t.Fatal(err)
	}
	p1, p2, p3 := almanacSegmentOffset(dom1)+10, almanacSegmentOffset(dom2)+10, almanacSegmentOffset(dom3)+11
	if dense[p1] != 3 || hist[p1] != (almanacSNRHist{}) {
		t.Fatalf("day before the SNR start: count %d hist %v, want 3 and no SNR data", dense[p1], hist[p1])
	}
	if dense[p2] != 4 || hist[p2] != (almanacSNRHist{1, 0, 1, 0, 0, 1}) {
		t.Fatalf("d2: count %d hist %v", dense[p2], hist[p2])
	}
	// d3's fold (a later read-modify-write) kept d2's histogram.
	if dense[p3] != 2 || hist[p3] != (almanacSNRHist{0, 0, 0, 1, 0, 0}) {
		t.Fatalf("d3: count %d hist %v", dense[p3], hist[p3])
	}
}

func TestAlmanacFoldStreamSQL(t *testing.T) {
	for _, c := range regionSNRColumnNames {
		if !strings.Contains(almanacFoldStreamSNRSQL, c+"::bigint") {
			t.Fatalf("SNR stream must read %s: %q", c, almanacFoldStreamSNRSQL)
		}
		if strings.Contains(almanacFoldStreamSQL, c) {
			t.Fatalf("the no-SNR stream must not reference %s (columns may be missing)", c)
		}
	}
	if strings.Count(almanacFoldStreamSQL, "0::bigint") != 1+almanacSNRTiers {
		t.Fatalf("no-SNR stream must keep the scan shape: %q", almanacFoldStreamSQL)
	}
	if almanacFoldUsesSNR(10, 11, true) || !almanacFoldUsesSNR(11, 11, true) || almanacFoldUsesSNR(20, 0, false) {
		t.Fatalf("almanacFoldUsesSNR drifted")
	}
}

// ---------------------------------------------------------------------------
// Reader: 30-day view
// ---------------------------------------------------------------------------

func TestAlmanacSNRTierFor(t *testing.T) {
	for db, want := range map[int]int{-40: -20, -23: -20, -18: -20, -17: -15, -12: -10, -8: -10, -7: -5, -3: -5, -2: 0, 0: 0, 30: 0} {
		if got := almanacSNRTierFloors[almanacSNRTierFor(db)]; got != want {
			t.Errorf("tier(%d) = %d, want %d", db, got, want)
		}
	}
}

func TestAlmanacParseMinSNR(t *testing.T) {
	get := func(q string) (*int, int, error) {
		return almanacParseMinSNR(httptest.NewRequest(http.MethodGet, "/api/almanac?qth=JO32"+q, nil))
	}
	if v, tier, err := get(""); v != nil || tier != -1 || err != nil {
		t.Fatalf("absent = any SNR")
	}
	if v, tier, err := get("&min_snr=-12"); err != nil || *v != -12 || tier != 2 {
		t.Fatalf("-12 → tier -10: %v %d %v", v, tier, err)
	}
	for _, bad := range []string{"abc", "-41", "31", "1.5", "-12dB"} {
		if _, _, err := get("&min_snr=" + bad); err == nil {
			t.Errorf("min_snr=%s must be rejected", bad)
		}
	}
}

// snrWorld: JO32 active on every band all window (no SNR), SNR collection
// from win.Start+10; after that, on 20m to NA:
//   - slot 26: 3 spots at −12 dB every day,
//   - slot 28: 3 spots at −3 dB every day,
//   - slot 30: 3 DX-cluster spots (no SNR) every day,
//   - slot 32: even days 2 @ −12 + 2 @ +1, odd days 6 @ −12 + 1 @ +1.
//
// Days before the start carry the same spots (fold would not store their
// SNR; the reader must ignore it anyway).
func snrWorld(withSNR bool) *fakeAggStore {
	f := newAggWorld()
	win := almanacWindowFor(aggTestNow.Unix())
	f.activeAllBands("JO32", win.Start, win.Today, allSlots())
	f.snrSince, f.hasSNRSince = win.Start+10, true
	add := func(d int64, slot int, n int64, snr int) {
		if withSNR {
			f.addSNR("JO32", "20m", "NA", d, slot, n, snr)
		} else {
			f.add("JO32", "20m", "NA", d, slot, n)
		}
	}
	for d := win.Start; d <= win.Today; d++ {
		add(d, 26, 3, -12)
		add(d, 28, 3, -3)
		f.add("JO32", "20m", "NA", d, 30, 3)
		if (d-win.Start)%2 == 0 {
			add(d, 32, 2, -12)
			add(d, 32, 2, 1)
		} else {
			add(d, 32, 6, -12)
			add(d, 32, 1, 1)
		}
	}
	return f
}

func computeAggTier(t *testing.T, f *fakeAggStore, tier int) *almanacTypical {
	t.Helper()
	win := almanacWindowFor(aggTestNow.Unix())
	acc, err := readAlmanacAccum(context.Background(), f, "JO32", win, false, tier)
	if err != nil {
		t.Fatal(err)
	}
	return computeAlmanacTypical(acc)
}

func TestAlmanacNoFloorUnchangedBySNRData(t *testing.T) {
	a := computeAggTier(t, snrWorld(true), -1)
	b := computeAggTier(t, snrWorld(false), -1)
	if len(a.Lanes) != len(b.Lanes) {
		t.Fatalf("lanes %d vs %d", len(a.Lanes), len(b.Lanes))
	}
	for i := range a.Lanes {
		if a.Lanes[i] != b.Lanes[i] {
			t.Fatalf("lane %s/%s differs with SNR data present (any-SNR view must be unchanged)", a.Lanes[i].Band, a.Lanes[i].Region)
		}
		if a.Lanes[i].Share != ([almanacSlotsPerDay]uint16{}) {
			t.Fatalf("no share without a floor")
		}
	}
	l := findLane(t, a, "20m", "NA")
	if l.N[26] != 30 || l.M[26] != 30 || l.N[30] != 30 {
		t.Fatalf("any SNR: slot 26 %d/%d, slot 30 n=%d", l.N[26], l.M[26], l.N[30])
	}
}

func shareOf(t *testing.T, code uint16) (float64, bool) {
	t.Helper()
	return almanacShareValue(code)
}

func TestAlmanacFloorOpenSemantics(t *testing.T) {
	f := snrWorld(true)
	// Tier −15: −12 dB spots pass.
	l := findLane(t, computeAggTier(t, f, 1), "20m", "NA")
	if l.M[26] != 20 || l.N[26] != 20 {
		t.Fatalf("≥−15 slot 26: %d/%d, want 20/20 (days before the SNR start left out)", l.N[26], l.M[26])
	}
	if v, ok := shareOf(t, l.Share[26]); !ok || v != 1 {
		t.Fatalf("≥−15 slot 26 share = %v %v, want 1", v, ok)
	}
	// DX-cluster-only cells: not open, no share.
	if l.N[30] != 0 || l.M[30] != 20 {
		t.Fatalf("cluster-only slot 30: %d/%d, want 0/20", l.N[30], l.M[30])
	}
	if _, ok := shareOf(t, l.Share[30]); ok {
		t.Fatalf("cluster-only slot must have no share")
	}
	// Tier −10: −12 dB spots fail, −3 dB pass.
	l = findLane(t, computeAggTier(t, f, 2), "20m", "NA")
	if l.N[26] != 0 || l.M[26] != 20 || l.N[28] != 20 {
		t.Fatalf("≥−10: slot 26 %d/%d, slot 28 n=%d", l.N[26], l.M[26], l.N[28])
	}
	if v, ok := shareOf(t, l.Share[26]); !ok || v != 0 {
		t.Fatalf("≥−10 slot 26 share = %v, want 0", v)
	}
	// Pooled share: Σ≥floor / Σ SNR spots = (10·2 + 10·1) / (10·4 + 10·7).
	if v, ok := shareOf(t, l.Share[32]); !ok || v != 0.273 {
		t.Fatalf("pooled share = %v, want 0.273 (30/110, not the mean of daily ratios)", v)
	}
	// Even days open (2 ≥ k), odd days not (1 < k).
	if l.N[32] != 10 {
		t.Fatalf("slot 32 n = %d, want 10", l.N[32])
	}
}

func TestAlmanacFloorWithoutSNRDataIsUnknown(t *testing.T) {
	f := snrWorld(true)
	f.hasSNRSince = false
	typ := computeAggTier(t, f, 1)
	l := findLane(t, typ, "20m", "NA")
	for s := 0; s < almanacSlotsPerDay; s++ {
		if l.M[s] != 0 || !l.unknown(s) {
			t.Fatalf("slot %d: m=%d; without SNR data a floor must read unknown, never closed", s, l.M[s])
		}
	}
}

func TestAlmanacHandlerMinSNR(t *testing.T) {
	f := snrWorld(true)
	s, _, _ := newTestAlmanacService(f)
	req := func(q string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/almanac?qth=JO32"+q, nil))
		return rec
	}
	for _, bad := range []string{"&min_snr=x", "&min_snr=-41", "&min_snr=31"} {
		if rec := req(bad); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d, want 400", bad, rec.Code)
		}
	}
	rec := req("&min_snr=-12")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		MinSNR       *int    `json:"min_snr"`
		SNRTier      *int    `json:"snr_tier"`
		SNRSince     *string `json:"snr_since"`
		SNRAvailable bool    `json:"snr_available"`
		Lanes        []struct {
			Band, Region string
			Share        []*float64 `json:"share"`
		} `json:"lanes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	win := almanacWindowFor(aggTestNow.Unix())
	if body.MinSNR == nil || *body.MinSNR != -12 || body.SNRTier == nil || *body.SNRTier != -10 ||
		!body.SNRAvailable || body.SNRSince == nil || *body.SNRSince != almanacDayString(win.Start+10) {
		t.Fatalf("snr fields = %+v", body)
	}
	found := false
	for _, l := range body.Lanes {
		if l.Band == "20m" && l.Region == "NA" {
			found = true
			if len(l.Share) != almanacSlotsPerDay || l.Share[28] == nil || *l.Share[28] != 1 || l.Share[30] != nil {
				t.Fatalf("share array wrong: %v", l.Share)
			}
		}
	}
	if !found {
		t.Fatalf("lane missing")
	}
	// Same tier → same cache entry; another tier → its own read.
	calls := f.txCalls
	if rec := req("&min_snr=-11"); rec.Code != http.StatusOK || f.txCalls != calls {
		t.Fatalf("-11 snaps to the cached -10 tier: tx calls %d → %d", calls, f.txCalls)
	}
	if rec := req("&min_snr=-16"); rec.Code != http.StatusOK || f.txCalls != calls+1 {
		t.Fatalf("-16 (tier -15) must read: tx calls %d → %d", calls, f.txCalls)
	}
	// Any SNR: no share, null floor fields; bare-grid4 key (widget summary).
	rec = req("")
	if strings.Contains(rec.Body.String(), `"share"`) || !strings.Contains(rec.Body.String(), `"min_snr":null`) {
		t.Fatalf("any-SNR body must carry no share and a null min_snr")
	}
	if s.peek("JO32") == nil || s.peek("JO32|snr-10") == nil || s.peek("JO32|snr-15") == nil {
		t.Fatalf("cache keys per tier missing")
	}
}

// ---------------------------------------------------------------------------
// Reader: seasonal drill-down
// ---------------------------------------------------------------------------

func TestAlmanacSeasonFloor(t *testing.T) {
	f := newFakeAggStore(aggToday() - 3)
	oct1, oct14 := seasonDay(2026, 10, 1), seasonDay(2026, 10, 14)
	f.snrSince, f.hasSNRSince = seasonDay(2026, 10, 5), true
	for d := oct1; d <= oct14; d++ {
		f.addSNR("JO32", "20m", "NA", d, 26, 3, -3)
		f.addSNR("JO32", "20m", "NA", d, 27, 1, -3)
		f.addSNR("JO32", "20m", "NA", d, 27, 3, -12)
	}
	f.setLayerIngest(almanacSeasonLayerPSKR, oct1, oct14, 1000)
	f.wsprDays("JO32", "20m", "NA", seasonDay(2025, 12, 1), seasonDay(2025, 12, 31), 26, 2)
	res, err := readAlmanacSeason(context.Background(), f, "JO32", 0, "20m", "NA", aggTestNow,
		almanacWSPRCoverage([]string{"JO32"}), 2)
	if err != nil {
		t.Fatal(err)
	}
	oct := seasonMonthOf(t, res, time.October)
	// Oct 5–14: 10 SNR-covered days.
	if oct.Days != 10 || oct.M[26] != 10 || oct.N[26] != 10 || oct.N[27] != 0 {
		t.Fatalf("oct: days %d slot26 %d/%d slot27 n=%d", oct.Days, oct.N[26], oct.M[26], oct.N[27])
	}
	if v, ok := almanacShareValue(oct.Share[27]); !ok || v != 0.25 {
		t.Fatalf("slot 27 share = %v, want 0.25", v)
	}
	if dec := seasonMonthOf(t, res, time.December); dec.Year != 0 {
		t.Fatalf("with a floor the WSPR fallback is not used: %+v", dec)
	}
	for _, l := range f.seasonLayers {
		if l == almanacSeasonLayerWSPR {
			t.Fatalf("WSPR layer read with a floor")
		}
	}
	// Handler: 400 on a bad floor; share + snr fields on a good one.
	s, _, _ := newTestAlmanacService(f)
	rec := almanacSeasonGet(s, "?qth=JO32&band=20m&region=NA&min_snr=99")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad min_snr: %d", rec.Code)
	}
	rec = almanacSeasonGet(s, "?qth=JO32&band=20m&region=NA&min_snr=-9")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"snr_tier":-10`) ||
		!strings.Contains(rec.Body.String(), `"share":[`) {
		t.Fatalf("season with floor: %d %s", rec.Code, rec.Body)
	}
}

// ---------------------------------------------------------------------------
// WSPR backfill bins
// ---------------------------------------------------------------------------

func TestAlmanacWSPRSNRBins(t *testing.T) {
	sql := almanacWSPRRingSQL("'JO32'", 0, 3600)
	for _, want := range []string{"countIf(snr < -20)", "countIf(snr >= -20 AND snr < -15)", "countIf(snr >= -5 AND snr < 0)", "countIf(snr >= 0)"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("ring SQL lacks %q: %s", want, sql)
		}
	}
	var r almanacWSPRAggRow
	if err := json.Unmarshal([]byte(`{"slot":20,"band":14,"rx4":"JO32","tx4":"FN20","c":6,"h":[1,"2",0,0,0,3]}`), &r); err != nil {
		t.Fatal(err)
	}
	ym := 202608
	m := newAlmanacWSPRMonth("JO32", []string{"JO32"}, ym, false)
	first, _ := almanacWSPRMonthDays(ym)
	m.addRing(first+2, r)
	k := almanacSegKey{Grid4: "JO32", Band: "20m", Region: almanacWSPRRegion("FN20")}
	if m.Hist[k] == nil {
		t.Fatalf("no histogram stored for %+v", k)
	}
	enc := almanacSparseEncode(m.Counts[k], m.Hist[k])
	var dense [almanacSeasonCountsLen]byte
	var hist [almanacSeasonCountsLen]almanacSNRHist
	if err := almanacSparseDecodeHist(enc, &dense, &hist); err != nil {
		t.Fatal(err)
	}
	p := almanacSegmentOffset(3) + 20
	if dense[p] != 6 || hist[p] != (almanacSNRHist{1, 2, 0, 0, 0, 3}) {
		t.Fatalf("WSPR cell: %d %v", dense[p], hist[p])
	}
}
