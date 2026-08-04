// Package region provides the DXPulse 11-region taxonomy and the Maidenhead
// locator helpers used throughout horstreporter. It is the single source of
// truth shared between the main binary, the propagation lab, and the Pathscope
// submodule. The region polygons mirror the dxPulseRegionForLatLng rules in the
// original main-package code; behaviour is unchanged.
package region

// Region is one of the DXPulse 11 macro-regions used as the destination
// bucket for both DXPulse and Pathscope. The string values are stable wire
// identifiers — they appear in API responses, database columns, and URL
// parameters, and must not change without a migration.
type Region string

const (
	Unknown Region = "??"
	EU      Region = "EU"
	NA      Region = "NA"
	SA      Region = "SA"
	AF      Region = "AF"
	AS      Region = "AS"
	OC      Region = "OC"
	AN      Region = "AN"
	JA      Region = "JA"
	VK      Region = "VK"
	KH6     Region = "KH6"
	CAR     Region = "CAR"
)

// AllRegions returns the regions in display order. AN and OC come last so
// the glance view's column order matches the existing DXPulse UI.
func AllRegions() []Region {
	return []Region{
		EU, NA, SA, AF, AS, JA, OC, VK, KH6, CAR, AN,
	}
}

// IsValid reports whether r is one of the named regions (Unknown is not).
func (r Region) IsValid() bool {
	switch r {
	case EU, NA, SA, AF, AS, OC, AN, JA, VK, KH6, CAR:
		return true
	}
	return false
}

// String returns the wire identifier.
func (r Region) String() string { return string(r) }

// FromLatLng classifies a (lat, lng) point into one of the 11 regions using
// the DXPulse bounding-box rules. Unknown is returned when the point falls
// outside every region. Behaviour is identical to the legacy
// dxPulseRegionForLatLng in the main package.
func FromLatLng(lat, lng float64) Region {
	if lat <= -60 {
		return AN
	}
	if lat >= 30 && lat <= 46 && lng >= 128 && lng <= 146 {
		return JA
	}
	if lat >= 18 && lat <= 29 && lng >= -161 && lng <= -154 {
		return KH6
	}
	if lat >= 10 && lat <= 25 && lng >= -85 && lng <= -60 {
		return CAR
	}
	if lat >= -50 && lat <= -10 && lng >= 110 && lng <= 180 {
		return VK
	}
	if lat >= 35 && lat <= 72 && lng >= -15 && lng <= 45 {
		return EU
	}
	if lat >= -40 && lat <= 37 && lng >= -20 && lng <= 55 {
		return AF
	}
	if lat >= 15 && lat <= 84 && lng >= -170 && lng <= -50 {
		return NA
	}
	if lat >= -60 && lat < 15 && lng >= -90 && lng <= -30 {
		return SA
	}
	if lat >= 0 && lat <= 78 && lng >= 40 && lng <= 180 {
		return AS
	}
	if lat >= -50 && lat <= 30 && (lng >= 130 || lng <= -130) {
		return OC
	}
	return Unknown
}

// FromLocator maps a Maidenhead locator (4 or 6 chars) to its region. Returns
// Unknown for an invalid locator. The centroid of the 4-char square is used,
// matching the legacy dxPulseRegionForLocator behaviour.
func FromLocator(loc string) Region {
	lat, lng, ok := LocatorCentroid(loc)
	if !ok {
		return Unknown
	}
	return FromLatLng(lat, lng)
}
