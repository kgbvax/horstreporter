package proplab

import "testing"

// TestCellBucketDedupAndStats pins the accumulator semantics that feed
// proplab_cell_buckets: pair dedup, reporter counting, SNR median/p10 and
// distance aggregation. (Ported from the removed ProplabService tests.)
func TestCellBucketDedupAndStats(t *testing.T) {
	eng := NewCellBucketEngine()
	mk := func(snr int) Spot {
		return Spot{T: 1700000000, B: "20m", SC: "DL1A", SL: "JO62QM", RC: "DL1B", RL: "JO42QM", RP: snr, Source: "mqtt"}
	}
	for _, snr := range []int{-15, -12, -9} {
		eng.Observe(mk(snr))
	}
	// Duplicate link must not increase link_count.
	eng.Observe(mk(-20))

	base := AlignBucketStart(1700000000)
	rows := eng.CloseBuckets(base)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if len(eng.CloseBuckets(base)) != 0 {
		t.Fatal("second close must be empty")
	}
	r := rows[0]
	if r.SpotCount != 4 || r.LinkCount != 1 || r.ReporterCount != 2 {
		t.Errorf("counts = %d/%d/%d, want 4/1/2", r.SpotCount, r.LinkCount, r.ReporterCount)
	}
	if r.SnrMedian != -13 {
		t.Errorf("snr_median = %d, want -13", r.SnrMedian)
	}
	if r.SnrP10 != -20 {
		t.Errorf("snr_p10 = %d, want -20", r.SnrP10)
	}
	if r.DistMaxKm == 0 || r.DistMedianKm == 0 {
		t.Errorf("dist aggregates zero: med=%d max=%d", r.DistMedianKm, r.DistMaxKm)
	}
	if r.Cell4 == "" || r.Region == "" {
		t.Errorf("cell/region empty: %q/%q", r.Cell4, r.Region)
	}

	// Different lanes must not share a bucket.
	e2 := NewCellBucketEngine()
	e2.Observe(Spot{T: 1700000000, B: "20m", SC: "A", SL: "JO62QM", RC: "B", RL: "FN31AB", RP: -10, Source: "mqtt"})
	e2.Observe(Spot{T: 1700000000, B: "20m", SC: "C", SL: "JO62QM", RC: "D", RL: "FN31AB", RP: 20, Source: "rbn"})
	if got := len(e2.CloseBuckets(base)); got != 2 {
		t.Errorf("lane-split rows = %d, want 2", got)
	}
}
