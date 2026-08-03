package proplab

import (
	"strconv"
	"testing"
	"time"
)

func newTestSpot(t int64, band, senderCall, receiverCall, senderLoc, receiverLoc string, snr int) Spot {
	return Spot{
		T:  t,
		B:  band,
		SC: senderCall,
		RC: receiverCall,
		SL: senderLoc,
		RL: receiverLoc,
		RP: snr,
	}
}

func TestLadderObserveAndCloseBuckets(t *testing.T) {
	eng := NewLadderEngine()
	now := time.Now().Unix()
	bucket := AlignBucketStart(now)
	eng.Observe(newTestSpot(now, "20m", "DL1A", "W1AW", "JO62QM", "FN31AB", -10))
	eng.Observe(newTestSpot(now, "20m", "DL1B", "W2AW", "JO62QM", "FN31AB", -12))
	eng.Observe(newTestSpot(now, "20m", "DL1C", "W3AW", "JO62QM", "FN31AB", -11))
	rows := eng.CloseBuckets(bucket)
	if len(rows) != 1 {
		t.Fatalf("expected one bucket row, got %d", len(rows))
	}
	r := rows[0]
	if r.SpotCount != 3 || r.LinkCount != 3 || r.ReporterCount != 6 {
		t.Fatalf("row=%+v want 3 spots, 3 links, 6 reporters", r)
	}
	if r.SnrMedian != -11 {
		t.Fatalf("snr_median=%d want -11", r.SnrMedian)
	}
}

func TestLadderVerdictOpenCoherentRun(t *testing.T) {
	eng := NewLadderEngine()
	now := time.Now().Unix()
	// Build a coherent 20m+17m+15m opening toward FN31 from JO62.
	for _, band := range []string{"20m", "17m", "15m"} {
		for i := 0; i < 3; i++ {
			eng.Observe(newTestSpot(now, band, "DL"+band+strconv.Itoa(i), "W"+band+strconv.Itoa(i), "JO62QM", "FN31AB", -10+i))
		}
	}
	// Lone 10m spot should not be coherent.
	for i := 0; i < 3; i++ {
		eng.Observe(newTestSpot(now, "10m", "DL10M"+strconv.Itoa(i), "W10M"+strconv.Itoa(i), "JO62QM", "FN31AB", -5+i))
	}

	expected := map[CellExpectedKey]float64{}
	reachable := map[string]map[string]bool{
		"20m": {"NA": true},
		"17m": {"NA": true},
		"15m": {"NA": true},
		"10m": {"NA": true},
	}
	params := DefaultLadderParams()
	v := eng.Verdict("JO62QM", false, nil, reachable, expected, params, now)

	states := make(map[string]string)
	for _, b := range v.Bands {
		states[b.Band] = b.State
	}
	for _, band := range []string{"20m", "17m", "15m"} {
		if states[band] != "open" {
			t.Fatalf("expected %s open, got %s", band, states[band])
		}
	}
	if states["10m"] != "activity_spike" {
		t.Fatalf("expected 10m activity_spike, got %s", states["10m"])
	}
	if v.EmpiricalMUF < 20 {
		t.Fatalf("expected empirical MUF >= 20 MHz, got %.1f", v.EmpiricalMUF)
	}
}

func TestLadderVerdictEsLane(t *testing.T) {
	eng := NewLadderEngine()
	now := time.Now().Unix()
	// 6m spots at 1500 km (single-hop Es geometry) between EU squares.
	for i := 0; i < 3; i++ {
		eng.Observe(newTestSpot(now, "6m", "DL"+strconv.Itoa(i), "G"+strconv.Itoa(i), "JO62QM", "JO01", 0))
	}
	expected := map[CellExpectedKey]float64{}
	reachable := map[string]map[string]bool{"6m": {"EU": true}}
	params := DefaultLadderParams()
	v := eng.Verdict("JO62QM", false, nil, reachable, expected, params, now)
	var sixm LadderBandVerdict
	for _, b := range v.Bands {
		if b.Band == "6m" {
			sixm = b
		}
	}
	if sixm.State != "open" {
		t.Fatalf("expected 6m open due to Es, got %s", sixm.State)
	}
	if len(sixm.EsCells) == 0 {
		t.Fatal("expected EsCells populated for 6m")
	}
}

func TestLadderVerdictNoTargetFallsBackGlobal(t *testing.T) {
	eng := NewLadderEngine()
	now := time.Now().Unix()
	for i := 0; i < 3; i++ {
		eng.Observe(newTestSpot(now, "20m", "A"+strconv.Itoa(i), "B"+strconv.Itoa(i), "JO62QM", "FN31AB", -10+i))
		eng.Observe(newTestSpot(now, "17m", "C"+strconv.Itoa(i), "D"+strconv.Itoa(i), "JO62QM", "FN31AB", -8+i))
	}
	v := eng.Verdict("", false, nil, nil, nil, DefaultLadderParams(), now)
	found20, found17 := false, false
	for _, b := range v.Bands {
		if b.Band == "20m" {
			found20 = true
			if b.State != "open" {
				t.Fatalf("expected 20m open in global fallback, got %s", b.State)
			}
		}
		if b.Band == "17m" {
			found17 = true
		}
	}
	if !found20 || !found17 {
		t.Fatalf("expected 20m and 17m verdict rows, found20=%v found17=%v", found20, found17)
	}
}

func TestLadderVerdictDataThin(t *testing.T) {
	eng := NewLadderEngine()
	now := time.Now().Unix()
	v := eng.Verdict("JO62QM", false, nil, nil, nil, DefaultLadderParams(), now)
	if !v.DataThin {
		t.Fatal("expected DataThin when no observations")
	}
}

func TestEmpiricalMUFMHz(t *testing.T) {
	cases := []struct {
		open map[string]bool
		want float64
	}{
		{nil, 0},
		{map[string]bool{"20m": true}, 14.0},
		{map[string]bool{"20m": true, "15m": true, "10m": true}, 28.0},
		{map[string]bool{"6m": true}, 0},
	}
	for _, c := range cases {
		got := empiricalMUFMHz(c.open)
		if got != c.want {
			t.Fatalf("empiricalMUFMHz(%v)=%.1f want %.1f", c.open, got, c.want)
		}
	}
}

func TestAlignBucketStart(t *testing.T) {
	if AlignBucketStart(0) != 0 {
		t.Fatalf("AlignBucketStart(0)=%d want 0", AlignBucketStart(0))
	}
	if AlignBucketStart(900) != 900 {
		t.Fatalf("AlignBucketStart(900)=%d want 900", AlignBucketStart(900))
	}
	if AlignBucketStart(901) != 900 {
		t.Fatalf("AlignBucketStart(901)=%d want 900", AlignBucketStart(901))
	}
}

// TestCusumOnsetLatchAndGate pins the 2026-08-03 calibration fixes: sparse
// singleton-link buckets drain the CUSUM statistic instead of raising it, an
// alarm stops being reported once its threshold crossing is stale (the latch),
// and an onset needs >=2 distinct cells with fresh crossings (single-cell
// noise produced 507 false alarms vs 10 confirmed in round-2 calibration).
func TestCusumOnsetLatchAndGate(t *testing.T) {
	params := DefaultLadderParams()
	cbk1 := cellBandKey{Cell4: "JO62", Region: "EU", Band: "20m"}
	cbk2 := cellBandKey{Cell4: "JN58", Region: "EU", Band: "20m"}
	cells := []cellBandKey{cbk1, cbk2}
	baseBucket := int64(600000)

	// Expected baseline μ = 0.5 links/bucket at the current slot: the build-up
	// to 6 links/bucket is a genuine 12× onset. Constant activity at the
	// baseline level must never alarm (that IS the normal state).
	step := func(e *LadderEngine, k int64, links int) (int, string) {
		now := (baseBucket+k)*BucketSeconds + 100
		slot := UTCSlotOfDay(now)
		expected := map[CellExpectedKey]float64{
			{Band: "20m", Cell4: cbk1.Cell4, Region: "EU", Slot: slot}: 0.5,
			{Band: "20m", Cell4: cbk2.Cell4, Region: "EU", Slot: slot}: 0.5,
		}
		live := map[cellBandKey]*ladderBandAggregate{
			cbk1: {linkCount: links},
			cbk2: {linkCount: links},
		}
		return e.minOnsetAge(cells, live, expected, params, now)
	}

	// Gate: ten consecutive 1-link buckets must never alarm.
	e := NewLadderEngine()
	for k := int64(0); k < 10; k++ {
		if age, _ := step(e, k, 1); age >= 0 {
			t.Fatalf("sparse bucket %d alarmed (age %d), want none", k, age)
		}
	}

	// Normal activity: constant links at the baseline must not alarm — the
	// drift (1.5×μ) exceeds the input, so S drains for the normal state.
	e = NewLadderEngine()
	for k := int64(0); k < 10; k++ {
		if age, _ := step(e, k, 1); age >= 0 { // 1 link < gate anyway; see build-up for normal vs onset
			t.Fatalf("baseline bucket %d alarmed (age %d), want none", k, age)
		}
	}

	// Coherence: a single alarmed cell must never suffice.
	e = NewLadderEngine()
	for k := int64(0); k < 6; k++ {
		now := (baseBucket+k)*BucketSeconds + 100
		slot := UTCSlotOfDay(now)
		expected := map[CellExpectedKey]float64{
			{Band: "20m", Cell4: cbk1.Cell4, Region: "EU", Slot: slot}: 0.5,
			{Band: "20m", Cell4: cbk2.Cell4, Region: "EU", Slot: slot}: 0.5,
		}
		live := map[cellBandKey]*ladderBandAggregate{
			cbk1: {linkCount: 6},
			cbk2: {linkCount: 1}, // cbk2 stays gated-quiet
		}
		if age, _ := e.minOnsetAge(cells, live, expected, params, now); age >= 0 {
			t.Fatalf("single-cell build-up alarmed at bucket %d (age %d), want none", k, age)
		}
	}

	// Build-up: 6-link buckets trip the alarm from the second bucket on.
	e = NewLadderEngine()
	if age, _ := step(e, 0, 6); age >= 0 {
		t.Fatalf("first build-up bucket alarmed (age %d), want none", age)
	}
	age, region := step(e, 1, 6)
	if age < 0 {
		t.Fatalf("second build-up bucket: no onset detected")
	}
	if region != "EU" {
		t.Errorf("onset region = %q, want EU", region)
	}
	if age != 15 {
		t.Errorf("onset age = %d min, want 15 (one bucket since zero crossing)", age)
	}

	// Latch: keep the same busy pattern past the freshness window; the alarm
	// must stop reporting even though S remains above threshold.
	seen := false
	for k := int64(2); k < 30; k++ {
		if a, _ := step(e, k, 6); a >= 0 {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("fresh onsets never reported during build-up")
	}
	if age, _ := step(e, 40, 6); age >= 0 {
		t.Errorf("stale alarm still reported (age %d), want none past freshness window", age)
	}
}
