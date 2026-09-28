package main

import "time"

// Almanac tuning constants (plan KTD3/KTD4). Starting defaults; U9 tunes them
// against the JO62/JO32 openings benchmark. Keep every Almanac threshold in
// this block so later units share one source of truth.
const (
	// almanacMinActiveDays30 is M_min for the 30-day view: a (area, band) lane
	// needs at least this many active days out of 30 before it is shown.
	almanacMinActiveDays30 = 10
	// almanacMinActiveDaysSeasonal is M_min for the seasonal (per-month) view.
	almanacMinActiveDaysSeasonal = 8

	// almanacWidenBandShare: the area stops widening at the smallest radius
	// where STRICTLY MORE than this share of the in-scope bands meet M_min.
	almanacWidenBandShare = 0.5
	// almanacMaxWidenRadius caps widening (rings around the centre grid4).
	almanacMaxWidenRadius = 2

	// almanacAreaCacheTTL is how long a resolved callsign → area stays cached
	// (QRZ-backed and locator results).
	almanacAreaCacheTTL = 6 * time.Hour
	// almanacAreaApproxCacheTTL is the shorter TTL for DXCC-centroid fallbacks,
	// so a transient QRZ failure is upgraded to the real locator soon.
	almanacAreaApproxCacheTTL = 30 * time.Minute
	// almanacAreaNegativeCacheTTL caches unresolvable callsigns so repeat
	// requests don't hit QRZ.
	almanacAreaNegativeCacheTTL = 10 * time.Minute
	// almanacAreaCacheMaxEntries bounds the resolver cache.
	almanacAreaCacheMaxEntries = 4096
)

// almanacInScopeBands is the Almanac band set: 160 m … 10 m, 6 m and up
// excluded (KTD3). Order follows propIntelBandOrder (HF low → high).
var almanacInScopeBands = []string{"160m", "80m", "60m", "40m", "30m", "20m", "17m", "15m", "12m", "10m"}
