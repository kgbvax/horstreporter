// Package geo holds pure geometry helpers for horstprop: Maidenhead locator
// decoding, great-circle distance/bearing, and frequency→band mapping. All
// longitudes are EAST-positive.
package geo

import (
	"math"
	"strings"
)

// LatLon is an East-positive geographic coordinate.
type LatLon struct {
	Lat float64
	Lon float64
}

// MaidenheadCenter decodes a 4- or 6-char Maidenhead locator to the centre of
// the square. Returns ok=false for malformed input.
func MaidenheadCenter(grid string) (LatLon, bool) {
	g := strings.TrimSpace(strings.ToUpper(grid))
	if len(g) != 4 && len(g) != 6 {
		return LatLon{}, false
	}
	if g[0] < 'A' || g[0] > 'R' || g[1] < 'A' || g[1] > 'R' {
		return LatLon{}, false
	}
	if g[2] < '0' || g[2] > '9' || g[3] < '0' || g[3] > '9' {
		return LatLon{}, false
	}
	lon := float64(g[0]-'A')*20 - 180
	lat := float64(g[1]-'A')*10 - 90
	lon += float64(g[2]-'0') * 2
	lat += float64(g[3] - '0')
	if len(g) == 6 {
		if g[4] < 'A' || g[4] > 'X' || g[5] < 'A' || g[5] > 'X' {
			return LatLon{}, false
		}
		lon += float64(g[4]-'A') * (2.0 / 24.0)
		lat += float64(g[5]-'A') * (1.0 / 24.0)
		lon += (2.0 / 24.0) / 2 // centre of subsquare
		lat += (1.0 / 24.0) / 2
		return LatLon{Lat: lat, Lon: lon}, true
	}
	return LatLon{Lat: lat + 0.5, Lon: lon + 1.0}, true // centre of 2x1° square
}

// HaversineKm is the great-circle distance between two points in kilometres.
func HaversineKm(a, b LatLon) float64 {
	const r = 6371.0
	dLat := rad(b.Lat - a.Lat)
	dLon := rad(b.Lon - a.Lon)
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(rad(a.Lat))*math.Cos(rad(b.Lat))*math.Sin(dLon/2)*math.Sin(dLon/2)
	return r * 2 * math.Atan2(math.Sqrt(h), math.Sqrt(1-h))
}

// BearingDeg is the initial great-circle bearing from→to, degrees 0..360.
func BearingDeg(from, to LatLon) float64 {
	y := math.Sin(rad(to.Lon-from.Lon)) * math.Cos(rad(to.Lat))
	x := math.Cos(rad(from.Lat))*math.Sin(rad(to.Lat)) -
		math.Sin(rad(from.Lat))*math.Cos(rad(to.Lat))*math.Cos(rad(to.Lon-from.Lon))
	return math.Mod(math.Atan2(y, x)*180/math.Pi+360, 360)
}

func rad(d float64) float64 { return d * math.Pi / 180 }
func deg(r float64) float64 { return r * 180 / math.Pi }

// IntermediatePoint returns the point a fraction f (0..1) of the way along the
// great circle from a to b (f=0 → a, f=1 → b). Used to sample path control
// points for the MUF gate.
func IntermediatePoint(a, b LatLon, f float64) LatLon {
	p1, l1 := rad(a.Lat), rad(a.Lon)
	p2, l2 := rad(b.Lat), rad(b.Lon)
	// central angle between the two points
	sLat := math.Sin((p2 - p1) / 2)
	sLon := math.Sin((l2 - l1) / 2)
	d := 2 * math.Asin(math.Sqrt(sLat*sLat+math.Cos(p1)*math.Cos(p2)*sLon*sLon))
	if d == 0 {
		return a
	}
	A := math.Sin((1-f)*d) / math.Sin(d)
	B := math.Sin(f*d) / math.Sin(d)
	x := A*math.Cos(p1)*math.Cos(l1) + B*math.Cos(p2)*math.Cos(l2)
	y := A*math.Cos(p1)*math.Sin(l1) + B*math.Cos(p2)*math.Sin(l2)
	z := A*math.Sin(p1) + B*math.Sin(p2)
	return LatLon{Lat: deg(math.Atan2(z, math.Hypot(x, y))), Lon: deg(math.Atan2(y, x))}
}

// SquareXY maps a 4+ char Maidenhead locator to integer grid-square coordinates
// (x in [0,180) for 2° longitude squares, y in [0,180) for 1° latitude squares).
// Matches HorstReporter's grid space so the two agree. ok=false for non-locators.
func SquareXY(locator string) (x, y int, ok bool) {
	g := strings.TrimSpace(strings.ToUpper(locator))
	if len(g) < 4 || g[0] < 'A' || g[0] > 'R' || g[1] < 'A' || g[1] > 'R' ||
		g[2] < '0' || g[2] > '9' || g[3] < '0' || g[3] > '9' {
		return 0, 0, false
	}
	return int(g[0]-'A')*10 + int(g[2]-'0'), int(g[1]-'A')*10 + int(g[3]-'0'), true
}

// SquareXYFromLatLon maps a coordinate (grid or DXCC centroid) to the same grid
// space as SquareXY, clamped to valid bounds.
func SquareXYFromLatLon(ll LatLon) (x, y int) {
	x = int(math.Floor((ll.Lon + 180) / 2))
	y = int(math.Floor(ll.Lat + 90))
	return clampSquare(x), clampSquare(y)
}

func clampSquare(v int) int {
	if v < 0 {
		return 0
	}
	if v > 179 {
		return 179
	}
	return v
}

// FreqToBand maps a frequency in Hz to an HF/6m band label, or "" if outside the
// known amateur bands.
func FreqToBand(freqHz int64) string {
	khz := freqHz / 1000
	switch {
	case khz >= 1800 && khz <= 2000:
		return "160m"
	case khz >= 3500 && khz <= 4000:
		return "80m"
	case khz >= 5250 && khz <= 5450:
		return "60m"
	case khz >= 7000 && khz <= 7300:
		return "40m"
	case khz >= 10100 && khz <= 10150:
		return "30m"
	case khz >= 14000 && khz <= 14350:
		return "20m"
	case khz >= 18068 && khz <= 18168:
		return "17m"
	case khz >= 21000 && khz <= 21450:
		return "15m"
	case khz >= 24890 && khz <= 24990:
		return "12m"
	case khz >= 28000 && khz <= 29700:
		return "10m"
	case khz >= 50000 && khz <= 54000:
		return "6m"
	default:
		return ""
	}
}
