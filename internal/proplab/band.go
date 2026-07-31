package proplab

import (
	"strings"
)

// band.go contains the band normalization tables used by the Ladder and Fusion
// engines. They mirror the corresponding helpers in the main binary.

// NormalizeBand turns "20" into "20m" while passing already-normalized strings
// through unchanged.
func NormalizeBand(raw string) string {
	b := strings.ToLower(strings.TrimSpace(raw))
	if b == "" {
		return ""
	}
	if strings.HasSuffix(b, "m") {
		return b
	}
	return b + "m"
}

// BandsInScope is the canonical set of HF/VHF bands the proplab analyses.
var BandsInScope = map[string]struct{}{
	"160m": {}, "80m": {}, "60m": {}, "40m": {}, "30m": {}, "20m": {},
	"17m": {}, "15m": {}, "12m": {}, "10m": {}, "6m": {}, "4m": {}, "2m": {},
}

// BandInScope reports whether a normalized band is analysed.
func BandInScope(band string) bool {
	_, ok := BandsInScope[band]
	return ok
}

// LadderFLane is the canonical daytime F-layer MUF-ladder ordering.
var LadderFLane = []string{"40m", "30m", "20m", "17m", "15m", "12m", "10m"}

// BandMHz maps the lower edge of each band to MHz.
var BandMHz = map[string]float64{
	"160m": 1.8, "80m": 3.5, "60m": 5.3, "40m": 7.0, "30m": 10.1,
	"20m": 14.0, "17m": 18.1, "15m": 21.0, "12m": 24.9, "10m": 28.0,
	"6m": 50.0, "4m": 70.0, "2m": 144.0,
}

// BandLowerEdgeMHz returns the lower edge of a normalized band in MHz.
func BandLowerEdgeMHz(band string) float64 {
	return BandMHz[strings.ToLower(strings.TrimSpace(band))]
}

// bandRangesKHz defines the inclusive lower/upper frequency limits for each
// canonical band. Used by BandForFrequencyKHz.
var bandRangesKHz = []struct {
	low, high float64
	band      string
}{
	{1800, 2000, "160m"},
	{3500, 4000, "80m"},
	{5300, 5700, "60m"},
	{7000, 7300, "40m"},
	{10100, 10150, "30m"},
	{14000, 14350, "20m"},
	{18068, 18168, "17m"},
	{21000, 21450, "15m"},
	{24890, 24990, "12m"},
	{28000, 29700, "10m"},
	{50000, 54000, "6m"},
	{70000, 71000, "4m"},
	{144000, 148000, "2m"},
}

// BandForFrequencyKHz maps a frequency in kHz to a canonical band, or "" if
// it falls outside known ranges.
func BandForFrequencyKHz(kHz float64) string {
	for _, r := range bandRangesKHz {
		if kHz >= r.low && kHz <= r.high {
			return r.band
		}
	}
	return ""
}

// UTC slot of day helpers are in time.go.
