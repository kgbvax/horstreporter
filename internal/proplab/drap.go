package proplab

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DRAPGrid is a parsed NOAA SWPC D-Region Absorption Prediction grid. Values
// are the highest frequency (MHz) affected by 1 dB of absorption. A higher HAF
// means absorption is weak (only low bands affected); a lower HAF means even
// higher HF bands are being absorbed.
type DRAPGrid struct {
	ValidAt int64
	Lats    []float64   // latitude rows, north-to-south (descending)
	Lons    []float64   // longitude columns, west-to-east (ascending)
	Values  [][]float64 // Values[iLat][iLon] in MHz; negative means missing
}

const drapMissing = -1.0

// ParseDRAPText parses the NOAA SWPC plain-text global D-RAP product. It
// expects comment/header lines starting with '#', a "Product Valid At" line, a
// "Frequency (MHz) as a function of Latitude and Longitude" marker, a
// longitude header row, and then latitude-prefixed data rows.
func ParseDRAPText(text []byte) (*DRAPGrid, error) {
	g := &DRAPGrid{}
	sc := bufio.NewScanner(bytes.NewReader(text))
	sc.Split(bufio.ScanLines)

	var headerDone bool
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			if strings.Contains(line, "Product Valid At") {
				g.ValidAt = parseDRAPValidAt(line)
			}
			continue
		}
		if strings.HasPrefix(line, "Frequency") {
			headerDone = true
			continue
		}
		if !headerDone {
			continue
		}
		if len(g.Lons) == 0 {
			g.Lons = parseDRAPFloatFields(line)
			if len(g.Lons) == 0 {
				return nil, fmt.Errorf("drap: empty longitude header")
			}
			continue
		}
		lat, vals, ok := parseDRAPDataRow(line)
		if !ok || len(vals) == 0 {
			continue
		}
		if len(vals) != len(g.Lons) {
			// Some rows may be truncated; pad with missing rather than fail.
			for len(vals) < len(g.Lons) {
				vals = append(vals, drapMissing)
			}
			vals = vals[:len(g.Lons)]
		}
		g.Lats = append(g.Lats, lat)
		g.Values = append(g.Values, vals)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(g.Lons) == 0 || len(g.Lats) == 0 {
		return nil, fmt.Errorf("drap: no grid found")
	}
	return g, nil
}

// HAF returns the bilinear-interpolated highest-affected frequency at the
// requested lat/lon. If the point is outside the global grid or all four
// surrounding cells are missing, ok is false.
func (g *DRAPGrid) HAF(lat, lon float64) (float64, bool) {
	if len(g.Lats) == 0 || len(g.Lons) == 0 {
		return 0, false
	}
	// Longitude wrap to the grid's longitude range.
	lon = drapWrapToRange(lon, g.Lons[0], g.Lons[len(g.Lons)-1])
	// Clamp latitude to the grid's latitude range so region centroids at the
	// edges (e.g. exactly -89 or 89) still get a value.
	if lat > g.Lats[0] {
		lat = g.Lats[0]
	}
	if lat < g.Lats[len(g.Lats)-1] {
		lat = g.Lats[len(g.Lats)-1]
	}

	latIdx, latFrac := drapIndexFrac(lat, g.Lats, true)
	if latIdx < 0 || latIdx >= len(g.Lats)-1 {
		return 0, false
	}
	lonIdx, lonFrac := drapIndexFrac(lon, g.Lons, false)
	if lonIdx < 0 {
		return 0, false
	}
	nextLonIdx := lonIdx + 1
	if nextLonIdx >= len(g.Lons) {
		nextLonIdx = 0 // wrap across the dateline
	}

	v00 := g.Values[latIdx][lonIdx]
	v01 := g.Values[latIdx][nextLonIdx]
	v10 := g.Values[latIdx+1][lonIdx]
	v11 := g.Values[latIdx+1][nextLonIdx]

	v0 := drapBilerp(v00, v01, lonFrac)
	v1 := drapBilerp(v10, v11, lonFrac)
	if v0 < 0 && v1 < 0 {
		return 0, false
	}
	if v0 < 0 {
		v0 = v1
	}
	if v1 < 0 {
		v1 = v0
	}
	if v0 < 0 {
		return 0, false
	}
	return v0 + latFrac*(v1-v0), true
}

func drapWrapToRange(v, min, max float64) float64 {
	span := max - min
	if span <= 0 {
		return v
	}
	for v < min {
		v += 360
	}
	for v > max {
		v -= 360
	}
	return v
}

// RegionHAFMap samples the grid at each region's representative centroid and
// returns a map suitable for FusionSWSnapshot.DrapHAF.
func (g *DRAPGrid) RegionHAFMap() map[string]float64 {
	out := make(map[string]float64, len(drapRegionCentroids))
	for region, c := range drapRegionCentroids {
		if haf, ok := g.HAF(c.lat, c.lon); ok {
			out[region] = haf
		}
	}
	return out
}

func parseDRAPValidAt(line string) int64 {
	prefix := "Product Valid At :"
	idx := strings.Index(line, prefix)
	if idx < 0 {
		return 0
	}
	s := strings.TrimSpace(line[idx+len(prefix):])
	for _, layout := range []string{
		"2006-01-02 15:04 UTC",
		"2006-01-02 15:04:05 UTC",
		"2006-01-02 15:04:05.000 UTC",
		"2006-01-02 15:04",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix()
		}
	}
	return 0
}

func parseDRAPFloatFields(s string) []float64 {
	var out []float64
	for _, f := range strings.Fields(s) {
		if f == "|" {
			continue
		}
		if v, err := strconv.ParseFloat(f, 64); err == nil {
			out = append(out, v)
		}
	}
	return out
}

func parseDRAPDataRow(line string) (float64, []float64, bool) {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return 0, nil, false
	}
	// First field is latitude, optionally followed by '|'.
	lat, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, nil, false
	}
	start := 1
	if fields[1] == "|" {
		start = 2
	}
	vals := make([]float64, 0, len(fields)-start)
	for _, f := range fields[start:] {
		if v, err := strconv.ParseFloat(f, 64); err == nil {
			vals = append(vals, v)
		} else {
			vals = append(vals, drapMissing)
		}
	}
	return lat, vals, true
}

// drapIndexFrac returns the lower index and the fraction along the interval.
// For latitude (descending) we invert the fraction.
func drapIndexFrac(v float64, vals []float64, descending bool) (int, float64) {
	n := len(vals)
	if n < 2 {
		return -1, 0
	}
	// Find the interval containing v by linear scan; grids are small (<= 90).
	for i := 0; i < n-1; i++ {
		a, b := vals[i], vals[i+1]
		if descending {
			if v <= a && v >= b {
				if a == b {
					return i, 0
				}
				return i, (a - v) / (a - b)
			}
		} else {
			if v >= a && v <= b {
				if b == a {
					return i, 0
				}
				return i, (v - a) / (b - a)
			}
		}
	}
	return -1, 0
}

func drapBilerp(a, b, frac float64) float64 {
	if a < 0 {
		return b
	}
	if b < 0 {
		return a
	}
	return a + frac*(b-a)
}

type drapCentroid struct {
	lat, lon float64
}

// Approximate centroids for the 11 DXPulse regions used by RegionFromLocator.
var drapRegionCentroids = map[string]drapCentroid{
	"AN":  {lat: -75, lon: 0},
	"JA":  {lat: 38, lon: 137},
	"KH6": {lat: 21, lon: -157},
	"CAR": {lat: 18, lon: -72},
	"VK":  {lat: -30, lon: 135},
	"EU":  {lat: 50, lon: 15},
	"AF":  {lat: 5, lon: 20},
	"NA":  {lat: 45, lon: -100},
	"SA":  {lat: -15, lon: -60},
	"AS":  {lat: 35, lon: 90},
	"OC":  {lat: -25, lon: 145},
}

