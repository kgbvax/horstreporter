package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// propSpot builds an MQTTMessage for baseline tests. For wspr/rbn/dxcluster
// SC/SL is the receiver side; for pskr ("mqtt") RC/RL is the receiver side.
func propSpot(ts int64, source, band, sc, sl, rc, rl string) MQTTMessage {
	return MQTTMessage{Source: source, B: band, T: ts, SC: sc, SL: sl, RC: rc, RL: rl}
}

func TestPropBaselineObserveKeysBySource(t *testing.T) {
	e := newPropBaselineEngine("", "")
	now := time.Now().Unix()

	// Same band/region/time, four sources → four distinct buckets.
	e.Observe(propSpot(now, "wspr", "20m", "RX", "JO62qm", "TX", "FN31ab"))   // recv=SL JO62 (EU)
	e.Observe(propSpot(now, "mqtt", "20m", "TX", "JO62qm", "RX", "FN31ab"))   // recv=RL FN31 (NA)
	e.Observe(propSpot(now, "rbn", "20m", "SKIM", "JO62qm", "DX", "FN31ab"))  // recv=SL JO62 (EU)
	e.Observe(propSpot(now, "dxcluster", "20m", "SPOT", "JO62qm", "DX", "FN31ab"))

	if len(e.buckets) != 4 {
		t.Fatalf("expected 4 source-distinct buckets, got %d", len(e.buckets))
	}
	// pskr keys by the RC/RL (reporter) region: FN31 = NA.
	if b := e.buckets[propBaselineBucketKey("20m", "pskr", utcSlotOfDay(now), "NA")]; b == nil {
		t.Fatalf("pskr bucket must key by RC/RL region (NA); buckets: %v", keysOf(e.buckets))
	}
	// wspr keys by SC/SL (receiver) region: JO62 = EU.
	if b := e.buckets[propBaselineBucketKey("20m", "wspr", utcSlotOfDay(now), "EU")]; b == nil {
		t.Fatalf("wspr bucket must key by SC/SL region (EU); buckets: %v", keysOf(e.buckets))
	}
}

func TestPropBaselineRegionFallback(t *testing.T) {
	e := newPropBaselineEngine("", "")
	now := time.Now().Unix()

	// Receiver locator garbage → fall back to the other end's region.
	e.Observe(propSpot(now, "wspr", "20m", "RX", "XX99zz", "TX", "FN31ab"))
	if len(e.buckets) != 1 {
		t.Fatalf("expected fallback bucket, got %d", len(e.buckets))
	}
	if b := e.buckets[propBaselineBucketKey("20m", "wspr", utcSlotOfDay(now), "NA")]; b == nil {
		t.Fatalf("expected NA fallback region, buckets: %v", keysOf(e.buckets))
	}

	// Both ends garbage → no bucket.
	e.Observe(propSpot(now, "wspr", "20m", "RX", "XX99zz", "TX", "YY88ww"))
	if len(e.buckets) != 1 {
		t.Fatalf("both-invalid must not observe, got %d buckets", len(e.buckets))
	}

	// Unknown source tag → skipped entirely.
	e.Observe(propSpot(now, "pskunknown", "20m", "RX", "JO62qm", "TX", "FN31ab"))
	if len(e.buckets) != 1 {
		t.Fatalf("unknown source must be skipped, got %d buckets", len(e.buckets))
	}
}

func TestPropBaselineInMemStatsParityWithWSPR(t *testing.T) {
	unified := newPropBaselineEngine("", "")
	legacy := newWsprClimatologyEngine("")
	now := time.Now().Unix()

	// Feed identical WSPR spots to both engines across two synthetic days.
	day0 := now - 2*86400
	for i := 0; i < 4; i++ {
		m := propSpot(day0+int64(i)*300, "wspr", "20m", "RX", "JO62qm", "TX", "FN31ab")
		unified.Observe(m)
		legacy.Observe(m)
	}
	for i := 0; i < 2; i++ {
		m := propSpot(day0+86400+int64(i)*300, "wspr", "20m", "RX", "JO62qm", "TX", "FN31ab")
		unified.Observe(m)
		legacy.Observe(m)
	}

	uRows := unified.statsFromMemoryLocked(now)
	lRows := legacy.regionCalendarStatsFromMemoryLocked(now)

	slot := utcSlotOfDay(day0)
	find := func(rows []propRegionCalendarStatRow) *propRegionCalendarStatRow {
		for i := range rows {
			if rows[i].SlotOfDay == slot && rows[i].Source == "wspr" {
				return &rows[i]
			}
		}
		return nil
	}
	var u *propRegionCalendarStatRow
	if u = find(uRows); u == nil {
		t.Fatalf("unified row not found for slot %d: %+v", slot, uRows)
	}
	var l *wsprRegionCalendarStatRow
	for i := range lRows {
		if lRows[i].SlotOfDay == slot {
			l = &lRows[i]
		}
	}
	if l == nil {
		t.Fatalf("legacy row not found for slot %d: %+v", slot, lRows)
	}
	if u.Mean != l.Mean || u.StdDev != l.StdDev || u.SampleDays != l.SampleDays {
		t.Fatalf("parity mismatch: unified mean=%v std=%v days=%d vs legacy mean=%v std=%v days=%d",
			u.Mean, u.StdDev, u.SampleDays, l.Mean, l.StdDev, l.SampleDays)
	}
}

func TestPropBaselinePendingCap(t *testing.T) {
	pending := make(map[propBaselineKey]int64)
	// Two day groups, 60 keys each (over a tiny cap of 60).
	for i := 0; i < 60; i++ {
		pending[propBaselineKey{Band: "20m", Source: "wspr", SlotOfDay: i, Region: "EU", DayIndex: 100}] = 1
	}
	for i := 0; i < 60; i++ {
		pending[propBaselineKey{Band: "20m", Source: "wspr", SlotOfDay: i, Region: "NA", DayIndex: 200}] = 1
	}
	out, n := capPendingPropBaseline(pending, 60)
	if n > 60 || len(out) > 60 {
		t.Fatalf("cap not enforced: n=%d len=%d", n, len(out))
	}
	// Oldest day (100) must be dropped entirely.
	for k := range out {
		if k.DayIndex == 100 {
			t.Fatalf("oldest day index not dropped: %+v", k)
		}
	}
}

func TestPropBaselineJSONLRoundTripAndV1Import(t *testing.T) {
	dir := t.TempDir()

	// V1 import: write a legacy wsprClimatologySnapshot, import it.
	legacyPath := filepath.Join(dir, "wspr_climatology.json")
	legacy := wsprClimatologySnapshot{
		Version:      wsprClimatologyVersion,
		SavedAt:      123,
		FirstEventAt: 100,
		LastEventAt:  200,
		Buckets: map[string]*wsprClimatologyBucket{
			"20m|20|EU": {Band: "20m", SlotOfDay: 20, Region: "EU", Count: 7, DayCounts: map[int64]int64{50: 7}},
		},
	}
	if err := writeJSONAtomic(legacyPath, legacy); err != nil {
		t.Fatalf("write legacy snapshot: %v", err)
	}
	e := newPropBaselineEngine("", legacyPath)
	if err := e.Load(); err != nil {
		t.Fatalf("v1 import load: %v", err)
	}
	b := e.buckets[propBaselineBucketKey("20m", "wspr", 20, "EU")]
	if b == nil || b.Source != "wspr" || b.Count != 7 {
		t.Fatalf("v1 import mismatch: %+v", b)
	}

	// V2 round-trip.
	v2Path := filepath.Join(dir, "prop_baseline.json")
	e2 := newPropBaselineEngine(v2Path, legacyPath)
	e2.Observe(propSpot(time.Now().Unix(), "rbn", "40m", "SKIM", "JO62qm", "DX", "FN31ab"))
	if err := e2.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	e3 := newPropBaselineEngine(v2Path, legacyPath)
	if err := e3.Load(); err != nil {
		t.Fatalf("round-trip load: %v", err)
	}
	var rbnBucket *propBaselineBucket
	for _, b := range e3.buckets {
		if b.Source == "rbn" {
			rbnBucket = b
		}
	}
	if rbnBucket == nil || rbnBucket.Count != 1 {
		t.Fatalf("round-trip lost rbn bucket: %+v", e3.buckets)
	}
	// Loading v2 must NOT re-import the legacy file on top.
	if e3.firstEventAt == 100 {
		t.Fatalf("legacy import must not run when v2 file exists")
	}

	// Missing files are fine.
	e4 := newPropBaselineEngine(filepath.Join(dir, "nope.json"), filepath.Join(dir, "nope2.json"))
	if err := e4.Load(); err != nil {
		t.Fatalf("missing files must be a no-op: %v", err)
	}
	os.Remove(legacyPath) // silence unused warning if assertions reordered
}

func keysOf(m map[string]*propBaselineBucket) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
