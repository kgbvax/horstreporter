// Package maidenhead holds Maidenhead grid-locator arithmetic: validation,
// square coordinates, ring/cluster blocks and lat/lng conversion.
package maidenhead

import "strings"

// IsLocator reports whether s starts with a valid upper-case Maidenhead square
// (two letters A-R, two digits). Lower-case input is not accepted; callers
// normalize first (see region.IsLocator for the case-folding variant).
func IsLocator(s string) bool {
	if len(s) < 4 {
		return false
	}
	if s[0] < 'A' || s[0] > 'R' || s[1] < 'A' || s[1] > 'R' {
		return false
	}
	if s[2] < '0' || s[2] > '9' || s[3] < '0' || s[3] > '9' {
		return false
	}
	return true
}

// SquareXY maps a 4+ char Maidenhead locator to integer grid-square
// coordinates: x in [0,180) for longitude squares (2° wide), y in [0,180) for
// latitude squares (1° tall). ok=false for non-locators.
func SquareXY(locator string) (x, y int, ok bool) {
	if !IsLocator(locator) {
		return 0, 0, false
	}
	// IsLocator already required upper-case field letters and digits, so the
	// first four bytes need no case folding (this sits on per-message paths).
	x = int(locator[0]-'A')*10 + int(locator[2]-'0')
	y = int(locator[1]-'A')*10 + int(locator[3]-'0')
	return x, y, true
}

// SquareXYFold is SquareXY(strings.ToUpper(locator)) without
// the allocation: the field and square letters are folded to upper case
// inline. It sits on the per-message hot path of the history scans, where the
// feed's locators are mixed case ("JO31lk") and ToUpper allocated every time.
func SquareXYFold(locator string) (x, y int, ok bool) {
	if len(locator) < 4 {
		return 0, 0, false
	}
	c0, c1 := locator[0], locator[1]
	if c0 >= 'a' && c0 <= 'z' {
		c0 -= 'a' - 'A'
	}
	if c1 >= 'a' && c1 <= 'z' {
		c1 -= 'a' - 'A'
	}
	d0, d1 := locator[2], locator[3]
	if c0 < 'A' || c0 > 'R' || c1 < 'A' || c1 > 'R' || d0 < '0' || d0 > '9' || d1 < '0' || d1 > '9' {
		return 0, 0, false
	}
	return int(c0-'A')*10 + int(d0-'0'), int(c1-'A')*10 + int(d1-'0'), true
}

// SquareXYToLocator is the inverse of SquareXY: grid-square coordinates to a
// 4-char locator.
func SquareXYToLocator(x, y int) string {
	return string([]byte{byte('A' + x/10), byte('A' + y/10), byte('0' + x%10), byte('0' + y%10)})
}

// SquaresWithinRings returns the (2*rings+1)² block of 4-char Maidenhead
// squares centred on locator (Chebyshev radius `rings` in grid-square space),
// clipped to the valid grid. rings<=0 yields just the centre square. This backs
// configurable "area of interest" feeds. A non-locator is returned unchanged.
func SquaresWithinRings(locator string, rings int) []string {
	cx, cy, ok := SquareXY(locator)
	if !ok {
		return []string{locator}
	}
	if rings < 0 {
		rings = 0
	}
	var res []string
	for dx := -rings; dx <= rings; dx++ {
		for dy := -rings; dy <= rings; dy++ {
			nx, ny := cx+dx, cy+dy
			if nx >= 0 && nx < 180 && ny >= 0 && ny < 180 {
				res = append(res, SquareXYToLocator(nx, ny))
			}
		}
	}
	return res
}

// ClusterSide is the side length (in grid squares) of a Grid cluster —
// the geographic unit the scoring baseline is keyed on. 6×6 squares spans
// 12° longitude × 6° latitude: small enough to distinguish US East Coast from
// West Coast, large enough that hundreds of reporters contribute in populated
// areas so the baseline reaches statistical significance. Clusters tile the
// globe's 180×180 grid into 30×30 = 900 units.
const ClusterSide = 6

// ClusterAnchor returns the anchor square of the 6×6 Grid cluster
// containing the given locator. The anchor is the cluster's top-left (NW)
// square in grid coordinates, computed by flooring each square coordinate
// down to the nearest multiple of ClusterSide — e.g. JO62 and JO73 both
// anchor to JN68. Coordinates are clamped to the 180-grid (edges near 180
// fold into the last partial cluster). ok=false for non-locators; the caller
// skips the cluster tier in that case.
func ClusterAnchor(locator string) (string, bool) {
	x, y, ok := SquareXY(locator)
	if !ok {
		return "", false
	}
	ax, ay := ClusterAnchorXY(x, y)
	return SquareXYToLocator(ax, ay), true
}

// ClusterAnchorXY maps a square's grid coordinates to its cluster anchor's
// coordinates. Split out of ClusterAnchor so hot loops can test cluster
// membership by comparing coordinates without building an anchor string.
func ClusterAnchorXY(x, y int) (int, int) {
	ax := (x / ClusterSide) * ClusterSide
	ay := (y / ClusterSide) * ClusterSide
	// Clamp to the valid grid — squares near 180 fold into the last cluster.
	if ax > 180-ClusterSide {
		ax = 180 - ClusterSide
	}
	if ay > 180-ClusterSide {
		ay = 180 - ClusterSide
	}
	return ax, ay
}

// InCluster reports whether locator falls in the cluster anchored at
// grid coordinates (ax, ay) — the same membership rule the baseline write path
// applies via ClusterAnchor, without allocating.
func InCluster(locator string, ax, ay int) bool {
	x, y, ok := SquareXYFold(locator)
	if !ok {
		return false
	}
	cx, cy := ClusterAnchorXY(x, y)
	return cx == ax && cy == ay
}

// SurroundingSquares returns the qth square and its 8 neighbours.
func SurroundingSquares(locator string) []string {
	return SquaresWithinRings(locator, 1)
}

// QTHSquares is the one place a QTH becomes the token set spots are matched
// against. A locator QTH is matched at its 4-char square (a 6-char "FN76OJ"
// used to match only that subsquare through the HasPrefix tests, leaving sparse
// operators with almost nothing); with surroundings it is the 3×3 block around
// it. Anything else (a callsign) is matched as given. qth must already be
// normalized (uppercase, see resolveQTHQuery/normalizeQTHToken); the caller
// keeps the full locator for map centre and distances.
func QTHSquares(qth string, surroundings bool) []string {
	if !IsLocator(qth) {
		return []string{qth}
	}
	rings := 0
	if surroundings {
		rings = 1
	}
	return SquaresWithinRings(qth[:4], rings)
}

// ToLatLng returns the centre of the locator's square (or subsquare for 6+
// chars). Shorter input yields the centre of the field; under 2 chars is 0,0.
func ToLatLng(locator string) (float64, float64) {
	locator = strings.ToUpper(locator)
	if len(locator) < 2 {
		return 0, 0
	}
	lng := float64(locator[0]-'A')*20 - 180
	lat := float64(locator[1]-'A')*10 - 90

	if len(locator) >= 4 {
		lng += float64(locator[2]-'0') * 2
		lat += float64(locator[3]-'0') * 1
		if len(locator) >= 6 {
			lng += float64(locator[4]-'A')*(5.0/60.0) + (5.0 / 120.0)
			lat += float64(locator[5]-'A')*(2.5/60.0) + (2.5 / 120.0)
		} else {
			lng += 1.0
			lat += 0.5
		}
	} else {
		lng += 10.0 // Center of field
		lat += 5.0
	}
	return lat, lng
}

// FromLatLng converts a lat/lng point to a Maidenhead locator of the
// given precision (2 chars = field, 4 chars = square). Used to derive a
// cluster anchor from the DXCC entity centroid when QRZ has no locator for
// a callsign QTH. precision=0 defaults to 4.
func FromLatLng(lat, lng float64, precision int) string {
	if precision <= 0 {
		precision = 4
	}
	// Clamp to the valid Maidenhead grid.
	if lat < -90 {
		lat = -90
	}
	if lat > 90 {
		lat = 90
	}
	if lng < -180 {
		lng = -180
	}
	if lng > 180 {
		lng = 180
	}
	fieldLng := int((lng + 180) / 20)
	fieldLat := int((lat + 90) / 10)
	if fieldLng >= 18 {
		fieldLng = 17
	}
	if fieldLat >= 18 {
		fieldLat = 17
	}
	if precision == 2 {
		return string([]byte{byte('A' + fieldLng), byte('A' + fieldLat)})
	}
	// Sub-square position within the field: columns span 2° of longitude, rows
	// 1° of latitude. SquareXY packs field+subsquare into x = field*10+col
	// and y = field*10+row over a 0..179 domain, so clamp the row/col to 0..9 —
	// an input clamped onto the grid's far edge (lat=90 / lng=180) lands on the
	// next field index and must fold back into field RR, not spill past it.
	col := int((lng + 180 - float64(fieldLng)*20) / 2)
	row := int(lat + 90 - float64(fieldLat)*10)
	if col >= 10 {
		col = 9
	}
	if row >= 10 {
		row = 9
	}
	return SquareXYToLocator(fieldLng*10+col, fieldLat*10+row)
}
