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
