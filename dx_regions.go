package main

// dx_regions.go holds the DXPulse region classification primitives that
// outlived the DXPulse feature itself: the coarse band-of-Earth regions used
// by the DX baseline's region counts (dx_conditions), the persisted
// dx_region_baseline_daily keying (dx_postgres), and the dxlens region
// calendar. The matrix/summary UI and /api/dxpulse endpoints were removed.

type dxPulseRegion string

const (
	dxPulseRegionUnknown dxPulseRegion = "??"
	dxPulseRegionEU      dxPulseRegion = "EU"
	dxPulseRegionNA      dxPulseRegion = "NA"
	dxPulseRegionSA      dxPulseRegion = "SA"
	dxPulseRegionAF      dxPulseRegion = "AF"
	dxPulseRegionAS      dxPulseRegion = "AS"
	dxPulseRegionOC      dxPulseRegion = "OC"
	dxPulseRegionAN      dxPulseRegion = "AN"
	dxPulseRegionJA      dxPulseRegion = "JA"
	dxPulseRegionVK      dxPulseRegion = "VK"
	dxPulseRegionKH6     dxPulseRegion = "KH6"
	dxPulseRegionCAR     dxPulseRegion = "CAR"
)

var dxPulseAllRegions = []dxPulseRegion{
	dxPulseRegionEU,
	dxPulseRegionNA,
	dxPulseRegionSA,
	dxPulseRegionAF,
	dxPulseRegionAS,
	dxPulseRegionOC,
	dxPulseRegionAN,
	dxPulseRegionJA,
	dxPulseRegionVK,
	dxPulseRegionKH6,
	dxPulseRegionCAR,
}

func dxPulseRegionForLocator(loc string) dxPulseRegion {
	if !isLocator(loc) {
		return dxPulseRegionUnknown
	}
	lat, lng := locatorToLatLng(loc)
	return dxPulseRegionForLatLng(lat, lng)
}

func dxPulseRegionForLatLng(lat, lng float64) dxPulseRegion {
	if lat <= -60 {
		return dxPulseRegionAN
	}
	if lat >= 30 && lat <= 46 && lng >= 128 && lng <= 146 {
		return dxPulseRegionJA
	}
	if lat >= 18 && lat <= 29 && lng >= -161 && lng <= -154 {
		return dxPulseRegionKH6
	}
	if lat >= 10 && lat <= 25 && lng >= -85 && lng <= -60 {
		return dxPulseRegionCAR
	}
	if lat >= -50 && lat <= -10 && lng >= 110 && lng <= 180 {
		return dxPulseRegionVK
	}
	if lat >= 35 && lat <= 72 && lng >= -15 && lng <= 45 {
		return dxPulseRegionEU
	}
	if lat >= -40 && lat <= 37 && lng >= -20 && lng <= 55 {
		return dxPulseRegionAF
	}
	if lat >= 15 && lat <= 84 && lng >= -170 && lng <= -50 {
		return dxPulseRegionNA
	}
	if lat >= -60 && lat < 15 && lng >= -90 && lng <= -30 {
		return dxPulseRegionSA
	}
	if lat >= 0 && lat <= 78 && lng >= 40 && lng <= 180 {
		return dxPulseRegionAS
	}
	if lat >= -50 && lat <= 30 && (lng >= 130 || lng <= -130) {
		return dxPulseRegionOC
	}
	return dxPulseRegionUnknown
}

// utcDayIndex floors a unix timestamp to a day number (negative-safe).
func utcDayIndex(ts int64) int64 {
	const secPerDay = int64(24 * 60 * 60)
	if ts >= 0 {
		return ts / secPerDay
	}
	return (ts - (secPerDay - 1)) / secPerDay
}
