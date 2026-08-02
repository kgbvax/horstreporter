package main

import (
	"strings"

	"horstreporter/internal/proplab"
)

// dx_proplab_dest.go is the destination-side data path of the Propagation Lab.
// Unlike the midpoint-cell buckets (Ladder physics: the ionosphere at the path
// midpoint decides the band), these buckets answer the operator question "which
// DX destinations do stations near my QTH reach?": scope2 is the 2-char
// Maidenhead field of the NEAR end and dx_region the 11-region destination of
// the REMOTE end. Links count symmetrically into both orientations —
// transmission direction does not matter for reachability.

// destBucketKey is the in-memory grouping for the current (open) buckets.
type destBucketKey struct {
	bucketStart int64
	band        string
	scope2      string
	region      string
}

// destBucketAgg accumulates one open destination bucket.
type destBucketAgg struct {
	spots     int
	pairs     map[string]struct{} // dedup'd station pair, sorted "A|B"
	reporters map[string]struct{} // near-end callsigns (witnesses)
	snr       []int
	dist      []int
}

func newDestBucketAgg() *destBucketAgg {
	return &destBucketAgg{
		pairs:     make(map[string]struct{}),
		reporters: make(map[string]struct{}),
	}
}

// observeDest folds one spot into the in-memory destination accumulator. The
// caller (ProplabService.Observe) has already deduplicated the spot and
// validated band/locators. Region classification uses the remote end's 4-char
// square; unclassified ends are skipped for that orientation.
func (s *ProplabService) observeDest(m MQTTMessage, band string) {
	sl := strings.ToUpper(strings.TrimSpace(m.SL))
	rl := strings.ToUpper(strings.TrimSpace(m.RL))
	if len(sl) < 4 || len(rl) < 4 {
		return
	}
	regionS := proplab.RegionFromLocator(sl[:4])
	regionR := proplab.RegionFromLocator(rl[:4])
	if regionS == "" && regionR == "" {
		return
	}

	latS, lngS := proplab.LocatorToLatLng(sl[:4])
	latR, lngR := proplab.LocatorToLatLng(rl[:4])
	dist := int(proplab.HaversineKm(latS, lngS, latR, lngR))

	sc := strings.ToUpper(strings.TrimSpace(m.SC))
	rc := strings.ToUpper(strings.TrimSpace(m.RC))
	// Pair key is direction-independent: a link between X and Y counts once per
	// bucket even if both directions are reported.
	pair := sc + "|" + rc
	if rc < sc {
		pair = rc + "|" + sc
	}
	bucketStart := proplab.AlignBucketStart(m.T)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.destAcc == nil {
		s.destAcc = make(map[destBucketKey]*destBucketAgg)
	}
	// Symmetric orientation: each end is the "near" scope for the other's view.
	s.addDestOrientation(bucketStart, band, sl[:2], regionR, sc, pair, m.RP, dist)
	s.addDestOrientation(bucketStart, band, rl[:2], regionS, rc, pair, m.RP, dist)
}

func (s *ProplabService) addDestOrientation(bucketStart int64, band, scope2, region, nearCall, pair string, rp, dist int) {
	if region == "" {
		return
	}
	key := destBucketKey{bucketStart: bucketStart, band: band, scope2: scope2, region: region}
	agg := s.destAcc[key]
	if agg == nil {
		agg = newDestBucketAgg()
		s.destAcc[key] = agg
	}
	agg.spots++
	agg.pairs[pair] = struct{}{}
	agg.reporters[nearCall] = struct{}{}
	agg.snr = append(agg.snr, rp)
	agg.dist = append(agg.dist, dist)
}

// closeDestBuckets returns rows for buckets started at or before cutoff and
// removes them from the in-memory accumulator, mirroring ladder.CloseBuckets.
func (s *ProplabService) closeDestBuckets(cutoff int64) []proplab.DestRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []proplab.DestRow
	for key, agg := range s.destAcc {
		if key.bucketStart > cutoff {
			continue
		}
		out = append(out, destRowFromAgg(key, agg))
		delete(s.destAcc, key)
	}
	return out
}

func destRowFromAgg(key destBucketKey, agg *destBucketAgg) proplab.DestRow {
	return proplab.DestRow{
		BucketStart:   key.bucketStart,
		Band:          key.band,
		Scope2:        key.scope2,
		Region:        key.region,
		SpotCount:     agg.spots,
		LinkCount:     len(agg.pairs),
		ReporterCount: len(agg.reporters),
		SnrMedian:     proplab.IntMedian(agg.snr),
		DistMedianKm:  proplab.IntMedian(agg.dist),
		DistMaxKm:     maxInts(agg.dist),
	}
}

// destLiveSnapshot aggregates the in-memory (open) buckets for the given scope
// fields, grouped back out to (band, region). windowBuckets limits how far back
// in-memory buckets are considered (open buckets are kept at most one interval
// past cutoff, so this mainly selects the current partial bucket). Empty scopes
// means no scope filter (global view). Used to merge the still-open bucket into
// the persisted live window so the reachability index freshens within ~1 min
// instead of at bucket close.
func (s *ProplabService) destLiveSnapshot(scopes map[string]bool, minBucketStart int64) []proplab.DestRow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	merged := make(map[[2]string]*destBucketAgg)
	for key, agg := range s.destAcc {
		if key.bucketStart < minBucketStart {
			continue
		}
		if scopes != nil && !scopes[key.scope2] {
			continue
		}
		gk := [2]string{key.band, key.region}
		m := merged[gk]
		if m == nil {
			m = newDestBucketAgg()
			merged[gk] = m
		}
		m.spots += agg.spots
		for p := range agg.pairs {
			m.pairs[p] = struct{}{}
		}
		for r := range agg.reporters {
			m.reporters[r] = struct{}{}
		}
		m.snr = append(m.snr, agg.snr...)
		m.dist = append(m.dist, agg.dist...)
	}
	out := make([]proplab.DestRow, 0, len(merged))
	for gk, m := range merged {
		out = append(out, proplab.DestRow{
			Band:          gk[0],
			Region:        gk[1],
			SpotCount:     m.spots,
			LinkCount:     len(m.pairs),
			ReporterCount: len(m.reporters),
			SnrMedian:     proplab.IntMedian(m.snr),
			DistMedianKm:  proplab.IntMedian(m.dist),
			DistMaxKm:     maxInts(m.dist),
		})
	}
	return out
}

func maxInts(in []int) int {
	m := 0
	for _, v := range in {
		if v > m {
			m = v
		}
	}
	return m
}

// destScopesForQTH maps a QTH locator to the set of scope2 fields the product
// view covers: just the QTH's own field, or the 3×3 field neighborhood when
// surroundings is on. Empty/invalid QTH returns nil (global view, no filter).
func destScopesForQTH(qth string, surroundings bool) map[string]bool {
	qth = strings.ToUpper(strings.TrimSpace(qth))
	if len(qth) < 2 || !proplab.IsLocator(qth) {
		return nil
	}
	scopes := map[string]bool{qth[:2]: true}
	if !surroundings {
		return scopes
	}
	f0, f1 := int(qth[0]-'A'), int(qth[1]-'A')
	for dx := -1; dx <= 1; dx++ {
		for dy := -1; dy <= 1; dy++ {
			x, y := f0+dx, f1+dy
			if x >= 0 && x < 18 && y >= 0 && y < 18 {
				scopes[string([]byte{byte('A' + x), byte('A' + y)})] = true
			}
		}
	}
	return scopes
}
