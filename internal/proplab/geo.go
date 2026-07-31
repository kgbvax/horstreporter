package proplab

import (
	"math"
	"strings"
)

// geo.go contains the Maidenhead locator, great-circle and region helpers that
// the proplab engines need. They are duplicated here (rather than importing
// package main) so the internal/proplab package is self-contained and can be used
// by cmd/proplab-backtest without pulling in the whole HorstReporter binary.

// IsLocator reports whether s looks like a valid Maidenhead locator (at least
// two letters followed by two digits).
func IsLocator(s string) bool {
	if len(s) < 4 {
		return false
	}
	s = strings.ToUpper(s)
	if s[0] < 'A' || s[0] > 'R' || s[1] < 'A' || s[1] > 'R' {
		return false
	}
	if s[2] < '0' || s[2] > '9' || s[3] < '0' || s[3] > '9' {
		return false
	}
	return true
}

// LocatorToLatLng returns the centre of the 4-character square for a locator.
func LocatorToLatLng(locator string) (float64, float64) {
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
		lng += 10
		lat += 5
	}
	return lat, lng
}

// LocatorSquareXY maps a 4+ char locator to integer grid-square coordinates.
func LocatorSquareXY(locator string) (x, y int, ok bool) {
	if !IsLocator(locator) {
		return 0, 0, false
	}
	loc := strings.ToUpper(locator[:4])
	x = int(loc[0]-'A')*10 + int(loc[2]-'0')
	y = int(loc[1]-'A')*10 + int(loc[3]-'0')
	return x, y, true
}

// SquareXYToLocator converts grid-square coordinates back to a 4-char locator.
func SquareXYToLocator(x, y int) string {
	return string([]byte{byte('A' + x/10), byte('A' + y/10), byte('0' + x%10), byte('0' + y%10)})
}

// HaversineKm returns the great-circle distance between two lat/lon points in km.
func HaversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0
	toRad := math.Pi / 180.0
	dLat := (lat2 - lat1) * toRad
	dLon := (lon2 - lon1) * toRad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*toRad)*math.Cos(lat2*toRad)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return r * c
}

// GreatCircleMidpoint returns the geographic midpoint of the great-circle path
// between two lat/lon pairs.
func GreatCircleMidpoint(lat1, lon1, lat2, lon2 float64) (lat, lon float64) {
	toRad := math.Pi / 180.0
	phi1 := lat1 * toRad
	phi2 := lat2 * toRad
	deltaLambda := (lon2 - lon1) * toRad

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
		return (lat1 + lat2) / 2, normalizeLon((lon1 + lon2) / 2)
	}
	x /= r
	y /= r
	z /= r

	lat = math.Asin(z) * (180.0 / math.Pi)
	lon = math.Atan2(y, x) * (180.0 / math.Pi)
	lon = normalizeLon(lon)

	if math.Abs(deltaLambda) > math.Pi {
		altLon := normalizeLon(lon + 180)
		if HaversineKm(lat, altLon, lat1, lon1)+HaversineKm(lat, altLon, lat2, lon2) <
			HaversineKm(lat, lon, lat1, lon1)+HaversineKm(lat, lon, lat2, lon2) {
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

// GetSurroundingSquares returns the target square and its 8 neighbours.
func GetSurroundingSquares(locator string) []string {
	cx, cy, ok := LocatorSquareXY(locator)
	if !ok {
		return []string{locator}
	}
	var res []string
	for dx := -1; dx <= 1; dx++ {
		for dy := -1; dy <= 1; dy++ {
			nx, ny := cx+dx, cy+dy
			if nx >= 0 && nx < 180 && ny >= 0 && ny < 180 {
				res = append(res, SquareXYToLocator(nx, ny))
			}
		}
	}
	return res
}

// LatLngToLocator4 converts a lat/lon pair to a 4-character Maidenhead square.
func LatLngToLocator4(lat, lon float64) (string, bool) {
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
	return SquareXYToLocator(x, y), true
}

// MidpointCell returns the 4-character square at the great-circle midpoint
// between two locators.
func MidpointCell(senderLoc, receiverLoc string) (string, bool) {
	senderLoc = strings.ToUpper(strings.TrimSpace(senderLoc))
	receiverLoc = strings.ToUpper(strings.TrimSpace(receiverLoc))
	if !IsLocator(senderLoc) || !IsLocator(receiverLoc) || len(senderLoc) < 4 || len(receiverLoc) < 4 {
		return "", false
	}
	lat1, lon1 := LocatorToLatLng(senderLoc)
	lat2, lon2 := LocatorToLatLng(receiverLoc)
	mLat, mLon := GreatCircleMidpoint(lat1, lon1, lat2, lon2)
	return LatLngToLocator4(mLat, mLon)
}
