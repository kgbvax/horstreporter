package main

import (
	"strings"
)

type Spot struct {
	Lat             float64 `json:"lat"`
	Lng             float64 `json:"lng"`
	SNR             int     `json:"snr"`
	AgeSeconds      int64   `json:"ageSeconds"`
	Locator         string  `json:"locator"`
	ReporterLocator string  `json:"reporterLocator,omitempty"`
	SourceType      string  `json:"sourceType,omitempty"`
	Band            string  `json:"band"`
	Sender          string  `json:"sender"`
	Receiver        string  `json:"receiver"`
}

func sourceTypeForMessage(m MQTTMessage) string {
	if src := strings.ToLower(strings.TrimSpace(m.Source)); src != "" {
		return src // explicit ingest tag ("mqtt"|"dxcluster"|"rbn"), incl. backfilled rows
	}
	// Legacy fallback for messages without an explicit Source (e.g. older cached
	// rows): DX-cluster is the only ingest that historically marked itself via MD.
	if strings.ToUpper(strings.TrimSpace(m.MD)) == "DXCLUSTER" {
		return "dxcluster"
	}
	return "mqtt"
}

func reporterLocatorForMessage(m MQTTMessage) string {
	mode := strings.ToUpper(strings.TrimSpace(m.MD))
	if mode == "DXCLUSTER" {
		return strings.ToUpper(strings.TrimSpace(m.SL))
	}
	return strings.ToUpper(strings.TrimSpace(m.RL))
}

// matchCall checks for an exact callsign match, or a match with common prefix/suffix modifiers (e.g., W1AW/P, DL/W1AW).
// The three modifier checks are spelled out against qth instead of building
// "/"+qth needles: at ~250k window messages per /api/dx_conditions request
// the old concat version spent ~29% of total endpoint CPU in allocation
// (ce-optimize prop-latency, run #2 profile).
func matchCall(spotCall, qth string) bool {
	if spotCall == qth {
		return true
	}
	// HasPrefix(spotCall, qth+"/")
	if n := len(qth); len(spotCall) > n && spotCall[:n] == qth && spotCall[n] == '/' {
		return true
	}
	// HasSuffix(spotCall, "/"+qth)
	if n := len(qth); len(spotCall) > n && spotCall[len(spotCall)-n:] == qth && spotCall[len(spotCall)-n-1] == '/' {
		return true
	}
	// Contains(spotCall, "/"+qth+"/") — scan slash positions, no needle.
	if needleLen := len(qth) + 2; len(spotCall) >= needleLen {
		for i := 0; i+needleLen <= len(spotCall); i++ {
			if spotCall[i] != '/' {
				continue
			}
			if spotCall[i+1:i+1+len(qth)] == qth && spotCall[i+1+len(qth)] == '/' {
				return true
			}
		}
	}
	return false
}

func matchAndCreateSpot(client *Client, m MQTTMessage, now int64) (Spot, bool) {
	if len(client.qthSet) == 0 && !client.areaActive {
		return Spot{}, false
	}

	sc, rc := strings.ToUpper(m.SC), strings.ToUpper(m.RC)
	sl, rl := strings.ToUpper(m.SL), strings.ToUpper(m.RL)

	isSender := false
	isReceiver := false

	for _, t := range client.qthSet {
		if matchCall(sc, t) || (isLocator(t) && sl != "" && strings.HasPrefix(sl, t)) {
			isSender = true
		}
		if matchCall(rc, t) || (isLocator(t) && rl != "" && strings.HasPrefix(rl, t)) {
			isReceiver = true
		}
	}

	// Area-of-interest match (O(1), additive): a sender/receiver locator within
	// areaRings grid-squares of the area centre also counts. Backs region feeds.
	if client.areaActive {
		if x, y, ok := locatorSquareXY(sl); ok && absInt(x-client.areaX) <= client.areaRings && absInt(y-client.areaY) <= client.areaRings {
			isSender = true
		}
		if x, y, ok := locatorSquareXY(rl); ok && absInt(x-client.areaX) <= client.areaRings && absInt(y-client.areaY) <= client.areaRings {
			isReceiver = true
		}
	}

	if logLevel == "DEBUG" {
		logDebug("QTH '%v' | Evaluating Spot -> SC:%s RC:%s SL:%s RL:%s | isSender:%v isReceiver:%v", client.qthSet, sc, rc, sl, rl, isSender, isReceiver)
	}

	if !isSender && !isReceiver {
		return Spot{}, false
	}

	var remoteLocator string
	var relation string
	if isSender {
		remoteLocator = rl
		relation = "Sender"
	} else {
		remoteLocator = sl
		relation = "Receiver"
	}

	if remoteLocator == "" {
		if logLevel == "DEBUG" {
			logDebug("QTH '%v' matched as %s, but remote locator is empty. Dropping spot.", client.qthSet, relation)
		}
		return Spot{}, false
	}

	lat, lng := locatorToLatLng(remoteLocator)
	age := now - m.T
	if age < 0 {
		age = 0
	}

	if logLevel == "DEBUG" {
		logDebug("QTH '%v' matched successfully! Mapped to Remote Locator: %s", client.qthSet, remoteLocator)
	}

	return Spot{
		Lat:             lat,
		Lng:             lng,
		SNR:             m.RP,
		AgeSeconds:      age,
		Locator:         remoteLocator,
		ReporterLocator: reporterLocatorForMessage(m),
		SourceType:      sourceTypeForMessage(m),
		Band:            m.B,
		Sender:          m.SC,
		Receiver:        m.RC,
	}, true
}

func isLocator(s string) bool {
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

// locatorSquareXY maps a 4+ char Maidenhead locator to integer grid-square
// coordinates: x in [0,180) for longitude squares (2° wide), y in [0,180) for
// latitude squares (1° tall). ok=false for non-locators.
func locatorSquareXY(locator string) (x, y int, ok bool) {
	if !isLocator(locator) {
		return 0, 0, false
	}
	loc := strings.ToUpper(locator[:4])
	x = int(loc[0]-'A')*10 + int(loc[2]-'0')
	y = int(loc[1]-'A')*10 + int(loc[3]-'0')
	return x, y, true
}

func squareXYToLocator(x, y int) string {
	return string([]byte{byte('A' + x/10), byte('A' + y/10), byte('0' + x%10), byte('0' + y%10)})
}

// getSquaresWithinRings returns the (2*rings+1)² block of 4-char Maidenhead
// squares centred on locator (Chebyshev radius `rings` in grid-square space),
// clipped to the valid grid. rings<=0 yields just the centre square. This backs
// configurable "area of interest" feeds. A non-locator is returned unchanged.
func getSquaresWithinRings(locator string, rings int) []string {
	cx, cy, ok := locatorSquareXY(locator)
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
				res = append(res, squareXYToLocator(nx, ny))
			}
		}
	}
	return res
}

// gridClusterSide is the side length (in grid squares) of a Grid cluster —
// the geographic unit the scoring baseline is keyed on. 6×6 squares spans
// 12° longitude × 6° latitude: small enough to distinguish US East Coast from
// West Coast, large enough that hundreds of reporters contribute in populated
// areas so the baseline reaches statistical significance. Clusters tile the
// globe's 180×180 grid into 30×30 = 900 units.
const gridClusterSide = 6

// locatorClusterAnchor returns the anchor square of the 6×6 Grid cluster
// containing the given locator. The anchor is the cluster's top-left (NW)
// square in grid coordinates, computed by flooring each square coordinate
// down to the nearest multiple of gridClusterSide — e.g. JO62 and JO73 both
// anchor to JN68. Coordinates are clamped to the 180-grid (edges near 180
// fold into the last partial cluster). ok=false for non-locators; the caller
// skips the cluster tier in that case.
func locatorClusterAnchor(locator string) (string, bool) {
	x, y, ok := locatorSquareXY(locator)
	if !ok {
		return "", false
	}
	ax := (x / gridClusterSide) * gridClusterSide
	ay := (y / gridClusterSide) * gridClusterSide
	// Clamp to the valid grid — squares near 180 fold into the last cluster.
	if ax > 180-gridClusterSide {
		ax = 180 - gridClusterSide
	}
	if ay > 180-gridClusterSide {
		ay = 180 - gridClusterSide
	}
	return squareXYToLocator(ax, ay), true
}

// getSurroundingSquares returns the qth square and its 8 neighbours.
func getSurroundingSquares(locator string) []string {
	return getSquaresWithinRings(locator, 1)
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func locatorToLatLng(locator string) (float64, float64) {
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

// latLngToLocator converts a lat/lng point to a Maidenhead locator of the
// given precision (2 chars = field, 4 chars = square). Used to derive a
// cluster anchor from the DXCC entity centroid when QRZ has no locator for
// a callsign QTH. precision=0 defaults to 4.
func latLngToLocator(lat, lng float64, precision int) string {
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
	// 1° of latitude. locatorSquareXY packs field+subsquare into x = field*10+col
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
	return squareXYToLocator(fieldLng*10+col, fieldLat*10+row)
}
