package proplab

// dx_midpoint.go contains the propagation-mode heuristics for the Propagation
// Lab "Ladder" variant (variant B). Geometry helpers live in geo.go so the
// package is self-contained and can be consumed by cmd/proplab-backtest.

// ladderFLane is the canonical daytime F-layer MUF-ladder ordering. Bands open
// and close in this order as the MUF rises and falls (40m first/last, 10m
// last/first). Two or more adjacent open bands in this slice are treated as
// propagation-coherent; a lone open band surrounded by quiet neighbors is more
// likely an operator-activity spike.
var ladderFLane = []string{"40m", "30m", "20m", "17m", "15m", "12m", "10m"}

// ladderFLaneIndex maps a band to its position in the ladder; -1 if not present.
var ladderFLaneIndex = make(map[string]int)

// ladderEsLane is the sporadic-E lane. These bands are classified by skip
// distance rather than by the MUF ladder. 10m is intentionally in the F-ladder
// because most 10m openings are F2; short-skip 10m Es is handled as a special
// case only when the distance signature is strong.
var ladderEsLane = []string{"6m", "4m", "2m"}

// ladderLowLane is the night/absorption lane (D-layer dominated, grayline and
// terminator-driven). These bands are not part of the MUF ladder either.
var ladderLowLane = []string{"160m", "80m", "60m"}

func init() {
	for i, b := range ladderFLane {
		ladderFLaneIndex[b] = i
	}
}

// isLadderFBand reports whether band participates in the MUF-ladder coherence
// classifier.
func isLadderFBand(band string) bool {
	_, ok := ladderFLaneIndex[band]
	return ok
}

// isLadderEsBand reports whether band is in the Es skip-distance lane.
func isLadderEsBand(band string) bool {
	for _, b := range ladderEsLane {
		if b == band {
			return true
		}
	}
	return false
}

// isLadderLowBand reports whether band is in the low-band night/absorption lane.
func isLadderLowBand(band string) bool {
	for _, b := range ladderLowLane {
		if b == band {
			return true
		}
	}
	return false
}

// ladderAdjacent reports whether two F-ladder bands are adjacent in the ladder.
// Non-ladder bands are never adjacent to anything.
func ladderAdjacent(a, b string) bool {
	ia, okA := ladderFLaneIndex[a]
	ib, okB := ladderFLaneIndex[b]
	if !okA || !okB {
		return false
	}
	return absInt(ia-ib) == 1
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// esSkipClassify decides whether a spot on an Es-lane band matches a classic
// single-hop sporadic-E skip distance. h'Es is roughly 100 km; single-hop ground
// range for an Es layer is typically 800–2200 km. Distances below the range may
// be high-angle/local scatter, and distances above are multi-hop or F-layer.
// Returns isEs=false for non-Es-lane bands regardless of distance.
func esSkipClassify(band string, distKm float64) (isEs bool, hops int) {
	if !isLadderEsBand(band) || distKm <= 0 {
		return false, 0
	}
	if distKm >= 800 && distKm <= 2200 {
		return true, 1
	}
	if distKm > 2200 && distKm <= 4400 {
		return true, 2
	}
	return false, 0
}

// ladderOpenBands takes a set of open F-ladder bands and returns the contiguous
// runs of adjacent ladder bands, plus the length of the longest run. Runs are
// returned in ladder order. Bands not in the ladder are ignored.
func ladderOpenBands(open map[string]bool) (runs [][]string, maxRun int) {
	if len(open) == 0 {
		return nil, 0
	}
	var current []string
	for _, band := range ladderFLane {
		if open[band] {
			current = append(current, band)
		} else {
			if len(current) > 0 {
				runs = append(runs, current)
				if len(current) > maxRun {
					maxRun = len(current)
				}
				current = nil
			}
		}
	}
	if len(current) > 0 {
		runs = append(runs, current)
		if len(current) > maxRun {
			maxRun = len(current)
		}
	}
	return runs, maxRun
}

// bandInOpenRun reports whether the given F-ladder band is part of a contiguous
// run of adjacent open bands whose length is at least minRun. open may contain
// non-ladder bands, which are ignored.
func bandInOpenRun(band string, open map[string]bool, minRun int) bool {
	if !isLadderFBand(band) || len(open) == 0 || minRun <= 1 {
		return true
	}
	runs, _ := ladderOpenBands(open)
	for _, run := range runs {
		if len(run) < minRun {
			continue
		}
		for _, b := range run {
			if b == band {
				return true
			}
		}
	}
	return false
}
