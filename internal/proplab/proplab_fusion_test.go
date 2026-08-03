package proplab

import (
	"testing"
	"time"
)

func TestWeightedQuantile(t *testing.T) {
	vals := []float64{1, 2, 3, 4, 5}
	w := []float64{1, 1, 1, 1, 1}
	if q := weightedQuantile(vals, w, 0.5); q != 3 {
		t.Fatalf("median=%.1f want 3", q)
	}
	if q := weightedQuantile(vals, w, 0.25); q != 2 {
		t.Fatalf("p25=%.1f want 2", q)
	}
	if q := weightedQuantile(vals, w, 0.75); q != 4 {
		t.Fatalf("p75=%.1f want 4", q)
	}
}

func TestWeightedQuantileWeighted(t *testing.T) {
	vals := []float64{10, 20, 30}
	w := []float64{1, 10, 1}
	if q := weightedQuantile(vals, w, 0.5); q != 20 {
		t.Fatalf("weighted median=%.1f want 20", q)
	}
}

func TestXrayMagnitude(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"A1.0", 1},
		{"B1.0", 10},
		{"C1.0", 100},
		{"M1.0", 1000},
		{"X1.0", 10000},
		{"M5.2", 5200},
		{"garbage", 0},
	}
	for _, c := range cases {
		got := xrayMagnitude(c.in)
		if got != c.want {
			t.Fatalf("xrayMagnitude(%q)=%.0f want %.0f", c.in, got, c.want)
		}
	}
}

func TestEventBandMatches(t *testing.T) {
	if !eventBandMatches("80m-10m CW/SSB", "20m") {
		t.Fatal("20m should match 80m-10m range")
	}
	if !eventBandMatches("20m", "20m") {
		t.Fatal("exact band should match")
	}
	if eventBandMatches("80m-10m", "6m") {
		t.Fatal("6m should not match 80m-10m range")
	}
}

func TestFusionOpenConfirmed(t *testing.T) {
	eng := NewFusionEngine()
	now := time.Now().Unix()
	slot := UTCSlotOfDay(now)
	live := []FusionBandCount{{
		Band: "20m", Region: "NA", SpotCount: 120, LinkCount: 40, ReporterCount: 30,
	}}
	// Baseline: 45 days of ~10 links/day.
	rows := make([]BaselineDayRow, 0, 45)
	for i := 0; i < 45; i++ {
		rows = append(rows, BaselineDayRow{Band: "20m", Region: "NA", Slot: slot, DayIndex: int64(i), LinkCount: 10, SpotCount: 20})
	}
	sw := FusionSWSnapshot{Available: true, Kp: 2, SFI: 120}
	v := eng.Verdict(live, rows, nil, sw, nil, DefaultFusionParams(), now)
	if len(v.Bands) != 1 {
		t.Fatalf("expected 1 band verdict, got %d", len(v.Bands))
	}
	b := v.Bands[0]
	if b.State != StateOpenConfirmed {
		t.Fatalf("expected open_confirmed, got %s (%s)", b.State, b.Reason)
	}
}

func TestFusionGuardbandDownweightsOutlier(t *testing.T) {
	eng := NewFusionEngine()
	now := time.Now().Unix()
	slot := UTCSlotOfDay(now)
	live := []FusionBandCount{{Band: "20m", Region: "NA", SpotCount: 30, LinkCount: 10, ReporterCount: 8}}
	rows := make([]BaselineDayRow, 0, 45)
	for i := 0; i < 44; i++ {
		rows = append(rows, BaselineDayRow{Band: "20m", Region: "NA", Slot: slot, DayIndex: int64(i), LinkCount: 5})
	}
	// One contest weekend outlier.
	rows = append(rows, BaselineDayRow{Band: "20m", Region: "NA", Slot: slot, DayIndex: 44, LinkCount: 500})
	params := DefaultFusionParams()
	v := eng.Verdict(live, rows, nil, FusionSWSnapshot{Available: true, Kp: 2, SFI: 120}, nil, params, now)
	b := v.Bands[0]
	// Without guardband p50 would be 5; with outlier downweighted p50 stays ~5,
	// so 10 links should be ~2x baseline, open_confirmed.
	if b.State != StateOpenConfirmed {
		t.Fatalf("expected open_confirmed with guardband, got %s (p50=%.1f)", b.State, b.BaselineP50)
	}
}

func TestFusionExplainedByEvent(t *testing.T) {
	eng := NewFusionEngine()
	now := time.Now().Unix()
	slot := UTCSlotOfDay(now)
	live := []FusionBandCount{{Band: "20m", Region: "NA", SpotCount: 60, LinkCount: 20, ReporterCount: 15}}
	rows := make([]BaselineDayRow, 0, 45)
	for i := 0; i < 45; i++ {
		rows = append(rows, BaselineDayRow{Band: "20m", Region: "NA", Slot: slot, DayIndex: int64(i), LinkCount: 10})
	}
	events := []FusionEvent{{
		Source: "wa7bnm", Title: "CQ WW CW", BandMask: "80m-10m", Region: "NA", EndsInMin: 120,
	}}
	v := eng.Verdict(live, rows, events, FusionSWSnapshot{Available: true, Kp: 2, SFI: 120}, nil, DefaultFusionParams(), now)
	b := v.Bands[0]
	// Activity is ~2x baseline but explained by contest.
	if b.State != StateClosedButActive {
		t.Fatalf("expected closed_but_active, got %s", b.State)
	}
	if len(b.ExplainedBy) == 0 || b.ExplainedBy[0] != "CQ WW CW" {
		t.Fatalf("expected explained_by CQ WW CW, got %v", b.ExplainedBy)
	}
}

func TestFusionClosedAbsorption(t *testing.T) {
	eng := NewFusionEngine()
	now := time.Now().Unix()
	slot := UTCSlotOfDay(now)
	live := []FusionBandCount{{Band: "20m", Region: "NA", SpotCount: 0, LinkCount: 0, ReporterCount: 0}}
	rows := make([]BaselineDayRow, 0, 45)
	for i := 0; i < 45; i++ {
		rows = append(rows, BaselineDayRow{Band: "20m", Region: "NA", Slot: slot, DayIndex: int64(i), LinkCount: 10})
	}
	sw := FusionSWSnapshot{Available: true, Kp: 6, SFI: 120, HasDrap: true, DrapHAF: map[string]float64{"NA": 15.0}}
	v := eng.Verdict(live, rows, nil, sw, nil, DefaultFusionParams(), now)
	b := v.Bands[0]
	if b.State != StateClosedWithCause {
		t.Fatalf("expected closed_with_cause, got %s", b.State)
	}
	if b.ClosureType != "absorption_limited" {
		t.Fatalf("expected absorption_limited, got %s", b.ClosureType)
	}
}

func TestFusionInsufficientData(t *testing.T) {
	eng := NewFusionEngine()
	now := time.Now().Unix()
	live := []FusionBandCount{{Band: "20m", Region: "NA", SpotCount: 5, LinkCount: 2, ReporterCount: 2}}
	v := eng.Verdict(live, nil, nil, FusionSWSnapshot{}, nil, DefaultFusionParams(), now)
	if v.Bands[0].State != StateInsufficientData {
		t.Fatalf("expected insufficient_data, got %s", v.Bands[0].State)
	}
}

// TestEventExplanationsSkipsRoutinePOTA pins the 2026-08-03 fix: POTA
// activator spots are routine baseline activity, not anomaly events — they
// must not explain (and thereby cap) cells, or every EU cell sits at the
// event cap whenever activators are on the air.
func TestEventExplanationsSkipsRoutinePOTA(t *testing.T) {
	events := []FusionEvent{
		{Source: "pota", Title: "DK5UR @ DE-0837", BandMask: "20m", Region: "EU"},
		{Source: "pota", Title: "DK5UR @ DE-0837", BandMask: "20m", Region: "EU"},
		{Source: "wa7bnm", Title: "CQ WW CW", BandMask: "80m-10m", Region: ""},
		{Source: "ng3k", Title: "5V7A DXpedition", BandMask: "", Region: "AF"},
	}
	got := eventExplanations("20m", "EU", events)
	if len(got) != 1 || got[0] != "CQ WW CW" {
		t.Errorf("20m EU explanations = %v, want only [CQ WW CW]", got)
	}
	if got := eventExplanations("20m", "AF", events); len(got) != 2 {
		t.Errorf("20m AF explanations = %v, want contest + dxpedition", got)
	}
	if got := eventExplanations("20m", "JA", events); len(got) != 1 || got[0] != "CQ WW CW" {
		t.Errorf("20m JA explanations = %v, want only global contest", got)
	}
}
