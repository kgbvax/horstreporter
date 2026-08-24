package proplab

// buckets.go is the cell-bucket ingest accumulator — all that remains of the
// Propagation Lab engines. It exists to feed proplab_cell_buckets (read by
// the pathscope module) and nothing else; the Ladder/Fusion/Reach engines,
// the A/B/C lab UI and the backtest harness were removed. Kept helpers
// (midpoint cells, lanes, bands, geo, time) are primitives needed by this
// path and by sibling consumers (e.g. internal/pathscope documents its
// modes against lane.go).

import (
	"strings"
	"sync"

	"horstreporter/internal/region"
)

// cellBucket is the in-memory accumulator for one cell×band×lane×bucket.
type cellBucket struct {
	links     map[string]struct{}
	reporters map[string]struct{}
	snrs      []int
	spotCount int
	sumDistKm float64
	maxDistKm float64
	region    string
}

// bucketKey indexes the in-memory accumulator map.
type bucketKey struct {
	BucketStart int64
	Band        string
	Cell4       string
	Lane        string
}

// CellBucketEngine accumulates spots into 15-minute midpoint-cell buckets and
// releases closed buckets as CellRow for persistence. It is not safe for
// concurrent use except via Observe (which locks); callers hold the lock
// semantic by using Observe / CloseBuckets only.
type CellBucketEngine struct {
	mu      sync.Mutex
	buckets map[bucketKey]*cellBucket
}

// NewCellBucketEngine creates a fresh accumulator.
func NewCellBucketEngine() *CellBucketEngine {
	return &CellBucketEngine{buckets: make(map[bucketKey]*cellBucket)}
}

// Observe ingests one spot into the current buckets. Safe for concurrent use.
func (e *CellBucketEngine) Observe(m Spot) {
	band := NormalizeBand(m.B)
	if !BandInScope(band) {
		return
	}
	cell, ok := MidpointCell(m.SL, m.RL)
	if !ok {
		return
	}
	lane := LaneForSourceType(SourceTypeForMessage(m))
	bucketStart := AlignBucketStart(m.T)

	lat1, lon1 := LocatorToLatLng(strings.ToUpper(strings.TrimSpace(m.SL)))
	lat2, lon2 := LocatorToLatLng(strings.ToUpper(strings.TrimSpace(m.RL)))
	dist := HaversineKm(lat1, lon1, lat2, lon2)

	sc := strings.ToUpper(strings.TrimSpace(m.SC))
	rc := strings.ToUpper(strings.TrimSpace(m.RC))

	e.mu.Lock()
	defer e.mu.Unlock()

	key := bucketKey{BucketStart: bucketStart, Band: band, Cell4: cell, Lane: lane}
	b := e.buckets[key]
	if b == nil {
		b = &cellBucket{
			links:     make(map[string]struct{}),
			reporters: make(map[string]struct{}),
			region:    string(region.FromLocator(cell)),
		}
		e.buckets[key] = b
	}
	b.spotCount++
	b.links[sc+"|"+rc] = struct{}{}
	b.reporters[sc] = struct{}{}
	b.reporters[rc] = struct{}{}
	b.sumDistKm += dist
	if dist > b.maxDistKm {
		b.maxDistKm = dist
	}
	if lane != "dcx" {
		b.snrs = append(b.snrs, m.RP)
	}
}

// CloseBuckets finalises buckets whose start is before or at cutoff, returns
// them as CellRow values, and removes them from memory.
func (e *CellBucketEngine) CloseBuckets(cutoff int64) []CellRow {
	e.mu.Lock()
	defer e.mu.Unlock()

	rows := make([]CellRow, 0, len(e.buckets))
	for k, b := range e.buckets {
		if k.BucketStart > cutoff {
			continue
		}
		r := CellRow{
			BucketStart:   k.BucketStart,
			Band:          k.Band,
			Cell4:         k.Cell4,
			Region:        b.region,
			Lane:          k.Lane,
			SpotCount:     b.spotCount,
			LinkCount:     len(b.links),
			ReporterCount: len(b.reporters),
			DistMaxKm:     int(b.maxDistKm),
		}
		if len(b.snrs) > 0 {
			r.SnrMedian = IntMedian(b.snrs)
			r.SnrP10 = int(PercentileInt(b.snrs, 0.10))
		}
		if b.spotCount > 0 {
			r.DistMedianKm = int(b.sumDistKm / float64(b.spotCount))
		}
		rows = append(rows, r)
		delete(e.buckets, k)
	}
	return rows
}
