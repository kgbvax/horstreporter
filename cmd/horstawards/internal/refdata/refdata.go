// Package refdata holds the small static reference tables the award engine needs:
// US states (for WAS), canonical bands, and mode classes. These are embedded in
// code rather than fetched — they are tiny, stable, and keep the binary
// self-contained (no reference downloads in v1, mirroring the repo's lean style).
package refdata

import (
	"strconv"
	"strings"
)

// usStates is the WAS target set: the 50 US states. DC is intentionally excluded
// (ARRL Worked All States is 50 states; DC is not one of them), so a DC spot is
// never flagged as WAS-needed.
var usStates = map[string]bool{
	"AL": true, "AK": true, "AZ": true, "AR": true, "CA": true, "CO": true,
	"CT": true, "DE": true, "FL": true, "GA": true, "HI": true, "ID": true,
	"IL": true, "IN": true, "IA": true, "KS": true, "KY": true, "LA": true,
	"ME": true, "MD": true, "MA": true, "MI": true, "MN": true, "MS": true,
	"MO": true, "MT": true, "NE": true, "NV": true, "NH": true, "NJ": true,
	"NM": true, "NY": true, "NC": true, "ND": true, "OH": true, "OK": true,
	"OR": true, "PA": true, "RI": true, "SC": true, "SD": true, "TN": true,
	"TX": true, "UT": true, "VT": true, "VA": true, "WA": true, "WV": true,
	"WI": true, "WY": true,
}

// usEntities is the DXCC-id set that carries US states for WAS: 291 United States,
// 110 Hawaii, 6 Alaska. A QSO/spot only counts toward WAS when its DXCC entity is
// one of these AND it carries a valid state.
var usEntities = map[string]bool{"291": true, "110": true, "6": true}

// NormState uppercases/trims a state code and returns ("",false) if it is not a
// WAS-eligible US state.
func NormState(s string) (string, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if usStates[s] {
		return s, true
	}
	return "", false
}

// IsUSEntity reports whether a DXCC id (string form) carries US states for WAS.
func IsUSEntity(dxccID string) bool {
	return usEntities[strings.TrimSpace(dxccID)]
}

// canonicalBands is the canonical lowercase band vocabulary, matching the
// frontend BAND_COLORS keys. Used to validate/normalize band strings.
var canonicalBands = map[string]bool{
	"2190m": true, "630m": true, "160m": true, "80m": true, "60m": true,
	"40m": true, "30m": true, "20m": true, "17m": true, "15m": true,
	"12m": true, "10m": true, "6m": true, "4m": true, "2m": true,
	"1.25m": true, "70cm": true, "33cm": true, "23cm": true, "13cm": true,
}

// NormBand lowercases/trims a band string and returns "" if it is not a known
// canonical band. ADIF "20M" and frontend "20m" both normalize to "20m".
func NormBand(b string) string {
	b = strings.ToLower(strings.TrimSpace(b))
	if canonicalBands[b] {
		return b
	}
	return ""
}

// bandEdgesMHz maps a canonical band to its [low,high] frequency span in MHz.
// Used to derive a band from an ADIF FREQ when no BAND field is present.
var bandEdgesMHz = []struct {
	band   string
	lo, hi float64
}{
	{"160m", 1.8, 2.0}, {"80m", 3.5, 4.0}, {"60m", 5.0, 5.45},
	{"40m", 7.0, 7.3}, {"30m", 10.1, 10.15}, {"20m", 14.0, 14.35},
	{"17m", 18.0, 18.2}, {"15m", 21.0, 21.45}, {"12m", 24.8, 25.0},
	{"10m", 28.0, 29.7}, {"6m", 50.0, 54.0}, {"4m", 70.0, 71.0},
	{"2m", 144.0, 148.0}, {"1.25m", 222.0, 225.0}, {"70cm", 420.0, 450.0},
	{"33cm", 902.0, 928.0}, {"23cm", 1240.0, 1300.0}, {"13cm", 2300.0, 2450.0},
}

// BandFromFreqMHz returns the canonical band containing freq (in MHz), or "".
func BandFromFreqMHz(freq float64) string {
	for _, e := range bandEdgesMHz {
		if freq >= e.lo && freq <= e.hi {
			return e.band
		}
	}
	return ""
}

// BandFromFreqStr parses an ADIF FREQ value (MHz, e.g. "14.074") and returns the
// canonical band, or "".
func BandFromFreqStr(s string) string {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return ""
	}
	return BandFromFreqMHz(f)
}

// modeClass maps an on-air mode (or ADIF MODE/SUBMODE) to a coarse class. Award
// slots are tracked per class (ssb/cw/data/fm) rather than per exact mode, which
// matches how operators chase per-mode awards (FT8 and RTTY both count as data).
var modeClass = map[string]string{
	"SSB": "ssb", "USB": "ssb", "LSB": "ssb",
	"CW": "cw",
	"FM": "fm", "NFM": "fm",
	"AM": "am",
	// Digital → data
	"FT8": "data", "FT4": "data", "JT65": "data", "JT9": "data", "MFSK": "data",
	"RTTY": "data", "PSK": "data", "PSK31": "data", "PSK63": "data", "BPSK": "data",
	"DATA": "data", "OLIVIA": "data", "JS8": "data", "FSK441": "data",
	"Q65": "data", "MSK144": "data", "ARDOP": "data", "VARA": "data", "DOMINO": "data",
}

// ModeClass returns the class for a mode string, or "" when unknown (treated as a
// mixed/overall slot rather than guessed).
func ModeClass(mode string) string {
	return modeClass[strings.ToUpper(strings.TrimSpace(mode))]
}
