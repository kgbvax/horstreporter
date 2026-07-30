package main

import (
	"strings"
	"testing"
	"time"
)

func TestProplabLaneForSourceType(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"mqtt", "ft8"},
		{"dxcluster", "dcx"},
		{"rbn", "rbn"},
		{"", "ft8"},
		{"MQTT", "ft8"}, // source_type is usually lower-case; accept default
	}
	for _, c := range cases {
		got := proplabLaneForSourceType(c.in)
		if got != c.want {
			t.Fatalf("proplabLaneForSourceType(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestProplabBucketAccumulatorDedupAndStats(t *testing.T) {
	acc := newProplabInMemoryAccumulator()
	base := time.Now().Unix()
	// Three spots, two unique links, on the same band/cell/lane.
	for i := 0; i < 3; i++ {
		acc.observe(proplabBackfillSpot{
			SpotTime:     base,
			Band:         "20m",
			SenderLoc:    "JO62QM",
			ReceiverLoc:  "FN31AB",
			SenderCall:   "DL1A",
			ReceiverCall: "W1AW",
			SNR:          -15 + i*3, // -15, -12, -9
			SourceType:   "mqtt",
		})
	}
	// Duplicate link should not increase link_count.
	acc.observe(proplabBackfillSpot{
		SpotTime:     base,
		Band:         "20m",
		SenderLoc:    "JO62QM",
		ReceiverLoc:  "FN31AB",
		SenderCall:   "DL1A",
		ReceiverCall: "W1AW",
		SNR:          -20,
		SourceType:   "mqtt",
	})
	rows := acc.closeBuckets(0)
	if len(rows) != 1 {
		t.Fatalf("expected one bucket row, got %d", len(rows))
	}
	r := rows[0]
	if r.SpotCount != 4 {
		t.Fatalf("spot_count=%d want 4", r.SpotCount)
	}
	if r.LinkCount != 1 {
		t.Fatalf("link_count=%d want 1 (dedup)", r.LinkCount)
	}
	if r.ReporterCount != 2 {
		t.Fatalf("reporter_count=%d want 2", r.ReporterCount)
	}
	if r.SnrMedian != -12 {
		t.Fatalf("snr_median=%d want -12", r.SnrMedian)
	}
	if r.SnrP10 != -20 {
		t.Fatalf("snr_p10=%d want -20", r.SnrP10)
	}
	if r.DistMaxKm == 0 {
		t.Fatal("expected non-zero dist_max_km")
	}
	if r.Cell4 == "" || r.Region == "" {
		t.Fatalf("expected cell and region, got cell=%q region=%q", r.Cell4, r.Region)
	}
}

func TestProplabBucketAccumulatorSeparateLanes(t *testing.T) {
	acc := newProplabInMemoryAccumulator()
	base := time.Now().Unix()
	acc.observe(proplabBackfillSpot{SpotTime: base, Band: "20m", SenderLoc: "JO62QM", ReceiverLoc: "FN31AB", SenderCall: "A", ReceiverCall: "B", SNR: -10, SourceType: "mqtt"})
	acc.observe(proplabBackfillSpot{SpotTime: base, Band: "20m", SenderLoc: "JO62QM", ReceiverLoc: "FN31AB", SenderCall: "C", ReceiverCall: "D", SNR: 20, SourceType: "rbn"})
	acc.observe(proplabBackfillSpot{SpotTime: base, Band: "20m", SenderLoc: "JO62QM", ReceiverLoc: "FN31AB", SenderCall: "E", ReceiverCall: "F", SNR: 0, SourceType: "dxcluster"})
	rows := acc.closeBuckets(0)
	if len(rows) != 3 {
		t.Fatalf("expected 3 lane rows, got %d", len(rows))
	}
	lanes := make(map[string]int)
	for _, r := range rows {
		lanes[r.Lane]++
	}
	if lanes["ft8"] != 1 || lanes["rbn"] != 1 || lanes["dcx"] != 1 {
		t.Fatalf("unexpected lane distribution: %v", lanes)
	}
}

func TestProplabSchemaStmts(t *testing.T) {
	stmts := proplabSchemaStmts()
	if len(stmts) == 0 {
		t.Fatal("expected schema statements")
	}
	joined := strings.Join(stmts, "\n")
	for _, name := range []string{"proplab_cell_buckets", "proplab_sw_series", "proplab_drap_snapshots", "proplab_events"} {
		if !strings.Contains(joined, name) {
			t.Fatalf("schema missing table %s", name)
		}
	}
}
