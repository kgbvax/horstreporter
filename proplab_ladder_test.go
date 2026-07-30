package main

import (
	"testing"
	"time"
)

func newTestSpot(t int64, band, senderCall, receiverCall, senderLoc, receiverLoc string, snr int) MQTTMessage {
	return MQTTMessage{
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
	eng := newLadderEngine()
	now := time.Now().Unix()
	bucket := alignBucketStart(now)
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
	eng := newLadderEngine()
	now := time.Now().Unix()
	// Build a coherent 20m+17m+15m opening toward FN31 from JO62.
	for _, band := range []string{"20m", "17m", "15m"} {
		for i := 0; i < 3; i++ {
			eng.Observe(newTestSpot(now, band, "DL"+band+itoa(i), "W"+band+itoa(i), "JO62QM", "FN31AB", -10+i))
		}
	}
	// Lone 10m spot should not be coherent.
	for i := 0; i < 3; i++ {
		eng.Observe(newTestSpot(now, "10m", "DL10M"+itoa(i), "W10M"+itoa(i), "JO62QM", "FN31AB", -5+i))
	}

	expected := map[cellBandKey]float64{}
	reachable := map[string]map[string]bool{
		"20m": {"NA": true},
		"17m": {"NA": true},
		"15m": {"NA": true},
		"10m": {"NA": true},
	}
	params := defaultProplabParamsB()
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
	eng := newLadderEngine()
	now := time.Now().Unix()
	// 6m spots at 1500 km (single-hop Es geometry) between EU squares.
	for i := 0; i < 3; i++ {
		eng.Observe(newTestSpot(now, "6m", "DL"+itoa(i), "G"+itoa(i), "JO62QM", "JO01", 0))
	}
	expected := map[cellBandKey]float64{}
	reachable := map[string]map[string]bool{"6m": {"EU": true}}
	params := defaultProplabParamsB()
	v := eng.Verdict("JO62QM", false, nil, reachable, expected, params, now)
	var sixm ladderBandVerdict
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
	eng := newLadderEngine()
	now := time.Now().Unix()
	for i := 0; i < 3; i++ {
		eng.Observe(newTestSpot(now, "20m", "A"+itoa(i), "B"+itoa(i), "JO62QM", "FN31AB", -10+i))
		eng.Observe(newTestSpot(now, "17m", "C"+itoa(i), "D"+itoa(i), "JO62QM", "FN31AB", -8+i))
	}
	v := eng.Verdict("", false, nil, nil, nil, defaultProplabParamsB(), now)
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
	eng := newLadderEngine()
	now := time.Now().Unix()
	v := eng.Verdict("JO62QM", false, nil, nil, nil, defaultProplabParamsB(), now)
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
	if alignBucketStart(0) != 0 {
		t.Fatalf("alignBucketStart(0)=%d want 0", alignBucketStart(0))
	}
	if alignBucketStart(900) != 900 {
		t.Fatalf("alignBucketStart(900)=%d want 900", alignBucketStart(900))
	}
	if alignBucketStart(901) != 900 {
		t.Fatalf("alignBucketStart(901)=%d want 900", alignBucketStart(901))
	}
}
