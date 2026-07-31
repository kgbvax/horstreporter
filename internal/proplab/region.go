package proplab

// RegionFromLocator maps a 4-char locator to one of the DXPulse 11-region names.
// It mirrors dxPulseRegionForLatLng in the main binary so the backtest harness
// and the engine produce the same region labels without importing package main.
func RegionFromLocator(loc string) string {
	if !IsLocator(loc) {
		return ""
	}
	lat, lng := LocatorToLatLng(loc)
	if lat <= -60 {
		return "AN"
	}
	if lat >= 30 && lat <= 46 && lng >= 128 && lng <= 146 {
		return "JA"
	}
	if lat >= 18 && lat <= 29 && lng >= -161 && lng <= -154 {
		return "KH6"
	}
	if lat >= 10 && lat <= 25 && lng >= -85 && lng <= -60 {
		return "CAR"
	}
	if lat >= -50 && lat <= -10 && lng >= 110 && lng <= 180 {
		return "VK"
	}
	if lat >= 35 && lat <= 72 && lng >= -15 && lng <= 45 {
		return "EU"
	}
	if lat >= -40 && lat <= 37 && lng >= -20 && lng <= 55 {
		return "AF"
	}
	if lat >= 15 && lat <= 84 && lng >= -170 && lng <= -50 {
		return "NA"
	}
	if lat >= -60 && lat < 15 && lng >= -90 && lng <= -30 {
		return "SA"
	}
	if lat >= 0 && lat <= 78 && lng >= 40 && lng <= 180 {
		return "AS"
	}
	if lat >= -50 && lat <= 30 && (lng >= 130 || lng <= -130) {
		return "OC"
	}
	return ""
}
