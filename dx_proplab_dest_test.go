package main

import (
	"testing"

	"horstreporter/internal/proplab"
)

// TestObserveDestSymmetric verifies that destination buckets count links
// symmetrically into near-end field × remote destination region, dedupe station
// pairs across orientations, and flush closed buckets.
func TestObserveDestSymmetric(t *testing.T) {
	s := newProplabService(nil, false, 0)

	now := int64(1700000000)
	s.Observe(MQTTMessage{RP: -12, T: now, SC: "DL1ABC", SL: "JO62QM", RC: "G0ABC", RL: "JO50AA", B: "20m", MD: "FT8"})
	s.Observe(MQTTMessage{RP: -18, T: now, SC: "DL1ABC", SL: "JO62QM", RC: "W1AW", RL: "FN31AA", B: "20m", MD: "FT8"})
	// Same pair reversed in the same bucket must not double the link count.
	s.Observe(MQTTMessage{RP: -10, T: now, SC: "G0ABC", SL: "JO50AA", RC: "DL1ABC", RL: "JO62QM", B: "20m", MD: "FT8"})

	rows := s.destLiveSnapshot(map[string]bool{"JO": true}, 0)
	got := make(map[[2]string]proplab.DestRow)
	for _, r := range rows {
		got[[2]string{r.Band, r.Region}] = r
	}
	eu := got[[2]string{"20m", "EU"}]
	if eu.LinkCount != 1 {
		t.Errorf("EU LinkCount = %d, want 1 (pair dedup across orientations)", eu.LinkCount)
	}
	if eu.ReporterCount != 2 {
		t.Errorf("EU ReporterCount = %d, want 2 (both JO near ends)", eu.ReporterCount)
	}
	if eu.SpotCount != 4 {
		t.Errorf("EU SpotCount = %d, want 4 (2 spots × 2 JO orientations)", eu.SpotCount)
	}
	na := got[[2]string{"20m", "NA"}]
	if na.LinkCount != 1 || na.ReporterCount != 1 {
		t.Errorf("NA got links=%d reporters=%d, want 1/1", na.LinkCount, na.ReporterCount)
	}

	// Scope filter: FN field sees the W1AW link with EU destination.
	fnRows := s.destLiveSnapshot(map[string]bool{"FN": true}, 0)
	if len(fnRows) != 1 || fnRows[0].Region != "EU" {
		t.Errorf("FN scope rows = %+v, want a single EU row", fnRows)
	}

	// Flush: closed buckets leave the accumulator; live snapshot empties.
	closed := s.closeDestBuckets(proplab.AlignBucketStart(now))
	if len(closed) == 0 {
		t.Fatalf("closeDestBuckets returned no rows")
	}
	byKey := make(map[string]proplab.DestRow)
	for _, r := range closed {
		byKey[r.Scope2+"/"+r.Region] = r
	}
	joEU := byKey["JO/EU"]
	if joEU.LinkCount != 1 || joEU.ReporterCount != 2 {
		t.Errorf("closed JO/EU = %+v, want links=1 reporters=2", joEU)
	}
	if _, ok := byKey["FN/EU"]; !ok {
		t.Errorf("missing closed FN/EU orientation row")
	}
	if again := s.closeDestBuckets(proplab.AlignBucketStart(now)); len(again) != 0 {
		t.Errorf("second close returned %d rows, want 0", len(again))
	}
}

// TestDestBands pins the canonical band list: an earlier "clever" slice
// construction (order[:0:cap] then append) yielded len 0 at runtime, silently
// neutering every dest SQL with len(bands)==0 guards.
func TestDestBands(t *testing.T) {
	b := destBands()
	if len(b) != 13 || b[0] != "160m" || b[12] != "2m" {
		t.Errorf("destBands() = %v, want 13 canonical bands", b)
	}
}

func TestDestScopesForQTH(t *testing.T) {
	if got := destScopesForQTH("", false); got != nil {
		t.Errorf("empty QTH = %v, want nil (global)", got)
	}
	if got := destScopesForQTH("JO62qm", false); len(got) != 1 || !got["JO"] {
		t.Errorf("plain QTH = %v, want {JO}", got)
	}
	got := destScopesForQTH("JO62qm", true)
	if len(got) != 9 || !got["JN"] || !got["JP"] || !got["IO"] {
		t.Errorf("surroundings = %v, want 9 fields incl JN/JP/IO", got)
	}
	// Edge of the field grid: wrap-around must not produce invalid fields.
	got = destScopesForQTH("AA00aa", true)
	if len(got) != 4 {
		t.Errorf("corner QTH = %v, want 4 fields", got)
	}
}
