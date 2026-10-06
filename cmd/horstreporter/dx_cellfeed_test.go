package main

import (
	"fmt"
	"strings"
	"testing"

	"horstreporter/internal/proplab"
)

// dx_cellfeed_test.go covers the cell-bucket feed's pure seams without a live
// Postgres: the toProplabSpot mapping, the upsert/prune SQL arms, and the
// dedup key. Pool-dependent persistence paths are out of scope here.

func TestToProplabSpot(t *testing.T) {
	t.Run("maps a real-shaped PSKReporter message field by field", func(t *testing.T) {
		m := MQTTMessage{
			T:  1731300000, RP: -13,
			SC: "DL1ABC", SL: "JO62qm",
			RC: "K1ABC", RL: "FN31pr",
			B:  "20m", MD: "FT8",
			Source: "mqtt",
		}
		got := toProplabSpot(m)
		if got.T != 1731300000 || got.RP != -13 {
			t.Fatalf("T/RP = %d/%d, want 1731300000/-13", got.T, got.RP)
		}
		if got.SC != "DL1ABC" || got.SL != "JO62qm" || got.RC != "K1ABC" || got.RL != "FN31pr" {
			t.Fatalf("callsign/locator mapping drifted: %+v", got)
		}
		if got.B != "20m" {
			t.Fatalf("band mapping drifted: %+v", got)
		}
		// Characterization: MD is intentionally NOT mapped — the ingest tag
		// already resolved into Source (SourceTypeForMessage only falls back
		// to MD when Source is empty), so Spot.MD stays zero.
		if got.MD != "" {
			t.Fatalf("MD must not be mapped: %+v", got)
		}
		if got.Source != "mqtt" {
			t.Fatalf("source = %q, want mqtt", got.Source)
		}
	})

	t.Run("explicit source tags are lower-trimmed", func(t *testing.T) {
		if got := toProplabSpot(MQTTMessage{Source: " DXCLUSTER "}).Source; got != "dxcluster" {
			t.Fatalf("source = %q, want dxcluster", got)
		}
		if got := toProplabSpot(MQTTMessage{Source: "RBN"}).Source; got != "rbn" {
			t.Fatalf("source = %q, want rbn", got)
		}
	})

	t.Run("legacy MD fallback tags dxcluster when Source is empty", func(t *testing.T) {
		// Older cached rows carry the ingest tag in MD, not Source.
		if got := toProplabSpot(MQTTMessage{MD: "DXCLUSTER"}).Source; got != "dxcluster" {
			t.Fatalf("source = %q, want dxcluster via MD fallback", got)
		}
		if got := toProplabSpot(MQTTMessage{MD: "FT8"}).Source; got != "mqtt" {
			t.Fatalf("source = %q, want mqtt", got)
		}
	})

	t.Run("out-of-range and empty inputs degrade to a defined spot", func(t *testing.T) {
		got := toProplabSpot(MQTTMessage{})
		if got.T != 0 || got.RP != 0 || got.B != "" || got.Source != "mqtt" {
			t.Fatalf("zero message mapped to %+v, want zero fields with mqtt source", got)
		}
		// Extreme timestamps / reports pass through untouched: the bucket
		// engine owns range decisions, not the mapping.
		got = toProplabSpot(MQTTMessage{T: -1, RP: -1000})
		if got.T != -1 || got.RP != -1000 {
			t.Fatalf("passthrough mapping drifted: %+v", got)
		}
	})
}

func TestProplabCellBucketUpsertArm(t *testing.T) {
	// The additive upsert is the only writer of proplab_cell_buckets; the
	// conflict arm decides whether late flushes accumulate or clobber.
	q := proplabCellBucketUpsertSQL()
	if !strings.Contains(q, "INSERT INTO proplab_cell_buckets") {
		t.Fatalf("missing table: %q", q)
	}
	for i := 1; i <= 12; i++ {
		if !strings.Contains(q, fmt.Sprintf("$%d", i)) {
			t.Fatalf("placeholder $%d missing: %q", i, q)
		}
	}
	if strings.Contains(q, "$13") {
		t.Fatalf("unexpected 13th placeholder: %q", q)
	}
	if !strings.Contains(q, "ON CONFLICT (bucket_start, band, cell4, lane)") {
		t.Fatalf("conflict key must be the table's PK: %q", q)
	}
	// Counts accumulate; medians replace; dist_max takes the greatest.
	for _, want := range []string{
		"spot_count     = proplab_cell_buckets.spot_count + EXCLUDED.spot_count",
		"link_count     = proplab_cell_buckets.link_count + EXCLUDED.link_count",
		"reporter_count = proplab_cell_buckets.reporter_count + EXCLUDED.reporter_count",
		"snr_median     = EXCLUDED.snr_median",
		"dist_median_km = EXCLUDED.dist_median_km",
		"dist_max_km    = GREATEST(proplab_cell_buckets.dist_max_km, EXCLUDED.dist_max_km)",
	} {
		if !strings.Contains(q, want) {
			t.Fatalf("conflict arm missing %q in: %q", want, q)
		}
	}

	t.Run("args follow the placeholder order", func(t *testing.T) {
		r := proplab.CellRow{
			BucketStart: 1730000000, Band: "20m", Cell4: "JO62", Region: "EU",
			Lane: "ft8", SpotCount: 5, LinkCount: 4, ReporterCount: 3,
			SnrMedian: -12, SnrP10: -20, DistMedianKm: 900, DistMaxKm: 1800,
		}
		args := proplabCellBucketUpsertArgs(r)
		if len(args) != 12 {
			t.Fatalf("args = %d, want 12", len(args))
		}
		want := []any{r.BucketStart, r.Band, r.Cell4, r.Region, r.Lane,
			r.SpotCount, r.LinkCount, r.ReporterCount, r.SnrMedian, r.SnrP10, r.DistMedianKm, r.DistMaxKm}
		for i, w := range want {
			if args[i] != w {
				t.Fatalf("args[%d] = %v, want %v", i, args[i], w)
			}
		}
	})
}

func TestProplabSWUpsertArm(t *testing.T) {
	q := proplabSWUpsertSQL()
	if !strings.Contains(q, "INSERT INTO proplab_sw_series (series, obs_time, value)") {
		t.Fatalf("missing column arm: %q", q)
	}
	if !strings.Contains(q, "VALUES ($1,$2,$3)") {
		t.Fatalf("missing placeholder arm: %q", q)
	}
	if !strings.Contains(q, "ON CONFLICT (series, obs_time) DO UPDATE SET value = EXCLUDED.value") {
		t.Fatalf("missing conflict arm (last observation must win): %q", q)
	}
	args := proplabSWUpsertArgs(proplabSWRow{Series: "kp", ObsTime: 1730000000, Value: 3.5})
	if len(args) != 3 || args[0] != "kp" || args[1] != int64(1730000000) || args[2] != 3.5 {
		t.Fatalf("args = %v", args)
	}
}

func TestCellfeedPruneArms(t *testing.T) {
	t.Run("both tables are pruned on their own time column", func(t *testing.T) {
		targets := cellfeedPruneTargets()
		if len(targets) != 2 {
			t.Fatalf("targets = %d, want 2", len(targets))
		}
		if targets[0] != (cellfeedPruneTarget{table: "proplab_cell_buckets", col: "bucket_start"}) {
			t.Fatalf("cell-bucket target = %+v", targets[0])
		}
		if targets[1] != (cellfeedPruneTarget{table: "proplab_sw_series", col: "obs_time"}) {
			t.Fatalf("sw-series target = %+v", targets[1])
		}
	})

	t.Run("delete is ctid-batched with one cutoff parameter", func(t *testing.T) {
		q := cellfeedPruneSQL(cellfeedPruneTarget{table: "proplab_cell_buckets", col: "bucket_start"})
		if !strings.Contains(q, "DELETE FROM proplab_cell_buckets") {
			t.Fatalf("missing table arm: %q", q)
		}
		if !strings.Contains(q, "WHERE ctid IN (SELECT ctid FROM proplab_cell_buckets WHERE bucket_start < $1 LIMIT 10000)") {
			t.Fatalf("missing batched ctid arm: %q", q)
		}
		if strings.Count(q, "$") != 1 {
			t.Fatalf("prune takes exactly one cutoff parameter: %q", q)
		}
		if !strings.Contains(q, fmt.Sprintf("LIMIT %d", cellfeedPruneBatchSize)) {
			t.Fatalf("batch limit arm drifted: %q", q)
		}
		// The ctid sub-select must repeat the same table as the outer DELETE
		// (the fmt arm passes it twice) — a mismatch here deletes from the
		// wrong relation.
		if strings.Count(q, "proplab_cell_buckets") != 2 {
			t.Fatalf("table name must appear twice (outer + sub-select): %q", q)
		}
	})
}

func TestCellDedupKey(t *testing.T) {
	// The key collapses duplicate deliveries of the same spot across the two
	// ingest paths: band × lane × callsigns × bucket start.
	a := cellDedupKey("20m", "ft8", "DL1ABC", "K1ABC", 1730000000)
	if a != "20m|ft8|DL1ABC|K1ABC|1730000000" {
		t.Fatalf("key shape drifted: %q", a)
	}
	if cellDedupKey("20m", "ft8", "DL1ABC", "K1ABC", 1730000000) != a {
		t.Fatalf("same inputs must produce the same key")
	}
	variants := []string{
		cellDedupKey("40m", "ft8", "DL1ABC", "K1ABC", 1730000000),
		cellDedupKey("20m", "dcx", "DL1ABC", "K1ABC", 1730000000),
		cellDedupKey("20m", "ft8", "DL2ABC", "K1ABC", 1730000000),
		cellDedupKey("20m", "ft8", "DL1ABC", "K2ABC", 1730000000),
		cellDedupKey("20m", "ft8", "DL1ABC", "K1ABC", 1730000900),
	}
	for i, v := range variants {
		if v == a {
			t.Fatalf("variant %d must differ from the base key", i)
		}
	}
}