package main

import (
	"math"
	"strings"
)

// dx_midpoint.go contains the geometry and propagation-mode heuristics for the
// Propagation Lab "Ladder" variant (variant B). It treats each spot as evidence
// about the ionosphere at the great-circle midpoint of the path, bins that
// midpoint into a 4-character Maidenhead square, and classifies Es vs F-layer
// paths from skip-distance signatures.
//
// The 4-char square is the natural "cell" for this layer: it is the resolution
// used by locatorSquareXY/squareXYToLocator (2 degrees longitude by 1 degree
// latitude), already used everywhere for surroundings and region aggregation.

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

// greatCircleMidpoint returns the geographic midpoint of the great-circle path
// between two lat/lon pairs. It converts each point to a 3-D unit vector,
// averages the vectors, and renormalises back to lat/lon. This is antimeridian
// safe and works near the poles.
func greatCircleMidpoint(lat1, lon1, lat2, lon2 float64) (lat, lon float64) {
	toRad := math.Pi / 180.0
	phi1 := lat1 * toRad
	phi2 := lat2 * toRad
	deltaLambda := (lon2 - lon1) * toRad

	// 3-D Cartesian unit vectors.
	x1 := math.Cos(phi1) * math.Cos(lon1*toRad)
	y1 := math.Cos(phi1) * math.Sin(lon1*toRad)
	z1 := math.Sin(phi1)

	x2 := math.Cos(phi2) * math.Cos(lon2*toRad)
	y2 := math.Cos(phi2) * math.Sin(lon2*toRad)
	z2 := math.Sin(phi2)

	x := x1 + x2
	y := y1 + y2
	z := z1 + z2

	r := math.Sqrt(x*x + y*y + z*z)
	if r == 0 {
		// Antipodal inputs collapse to the origin; any antipodal point is a
		// valid midpoint. Return the arithmetic mean as a stable fallback.
		return (lat1 + lat2) / 2, normalizeLon((lon1 + lon2) / 2)
	}
	x /= r
	y /= r
	z /= r

	lat = math.Asin(z) * (180.0 / math.Pi)
	lon = math.Atan2(y, x) * (180.0 / math.Pi)
	lon = normalizeLon(lon)

	// If the longitudinal difference between the inputs is large, the
	// Cartesian mean can settle on the shorter great-circle side, which is the
	// desired midpoint. Guard against a 180-degree flip by preferring the lon
	// that minimises the sum of great-circle distances.
	if math.Abs(deltaLambda) > math.Pi {
		altLon := normalizeLon(lon + 180)
		if haversineKm(lat, altLon, lat1, lon1)+haversineKm(lat, altLon, lat2, lon2) <
			haversineKm(lat, lon, lat1, lon1)+haversineKm(lat, lon, lat2, lon2) {
			lon = altLon
		}
	}

	return lat, lon
}

func normalizeLon(lon float64) float64 {
	for lon < -180 {
		lon += 360
	}
	for lon >= 180 {
		lon -= 360
	}
	return lon
}

// latLngToLocator4 converts a latitude/longitude pair to a 4-character
// Maidenhead square (the resolution used for midpoint cells). It returns the
// lower-left square that contains the point, which matches the convention used
// by locatorToLatLng for 4-character locators (it adds +1 degree lon and +0.5
// degree lat to return the square centre).
func latLngToLocator4(lat, lon float64) (string, bool) {
	if lat < -90 || lat > 90 || lon < -180 || lon >= 180 {
		return "", false
	}
	x := int(math.Floor((lon + 180) / 2))
	y := int(math.Floor((lat + 90) / 1))
	if x < 0 {
		x = 0
	}
	if x >= 180 {
		x = 179
	}
	if y < 0 {
		y = 0
	}
	if y >= 180 {
		y = 179
	}
	return squareXYToLocator(x, y), true
}

// midpointCell returns the 4-character Maidenhead square at the great-circle
// midpoint between two locators. The cell approximates the ionospheric
// reflection region for that path. Requires both locators to be valid and at
// least 4 characters long.
func midpointCell(senderLoc, receiverLoc string) (string, bool) {
	senderLoc = strings.ToUpper(strings.TrimSpace(senderLoc))
	receiverLoc = strings.ToUpper(strings.TrimSpace(receiverLoc))
	if !isLocator(senderLoc) || !isLocator(receiverLoc) || len(senderLoc) < 4 || len(receiverLoc) < 4 {
		return "", false
	}
	lat1, lon1 := locatorToLatLng(senderLoc)
	lat2, lon2 := locatorToLatLng(receiverLoc)
	mLat, mLon := greatCircleMidpoint(lat1, lon1, lat2, lon2)
	return latLngToLocator4(mLat, mLon)
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
