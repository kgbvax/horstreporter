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
	// almanacPreliminaryMinActiveDays is the floor of the effective M_min of
	// an SNR-floored view while fewer than M_min days carry SNR data (the
	// "preliminary" view): M_min_eff = min(M_min, max(this, covered SNR
	// days)), so a lane needs at least 2 SNR days before it reads known.
	almanacPreliminaryMinActiveDays = 2

	// almanacWidenBandShare: the area stops widening at the smallest radius
	// where STRICTLY MORE than this share of the in-scope bands have at least
	// almanacWidenMinKnownSlots known slots.
	almanacWidenBandShare = 0.5
	// almanacWidenMinKnownSlots: a band counts toward widening at a radius when
	// at least this many of its 30-min slots are known, i.e. reach M_min alive,
	// area-active days (8 slots = 4 h). Days-with-any-spot is not enough: a
	// sparse square passes that test while every slot still reads "not enough
	// data".
	almanacWidenMinKnownSlots = 8
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

// almanacEffectiveMMin is the M_min of an SNR-floored view (30-day or one
// seasonal month) given how many of its days carry SNR data: the full M_min
// once enough days are covered, else the covered days, floored at
// almanacPreliminaryMinActiveDays.
func almanacEffectiveMMin(full, coveredSNRDays int) int {
	return min(full, max(almanacPreliminaryMinActiveDays, coveredSNRDays))
}

// almanacInScopeBands is the Almanac band set: 160 m … 10 m, 6 m and up
// excluded (KTD3). Order follows propIntelBandOrder (HF low → high).
var almanacInScopeBands = []string{"160m", "80m", "60m", "40m", "30m", "20m", "17m", "15m", "12m", "10m"}

// 30-day Almanac statistic, agenda and cache (plan U2: KTD2, KTD4, KTD9).
const (
	// almanacWindowDays is the typical window: the 30 complete UTC days
	// ending yesterday. Today only feeds the "open today" overlay (KTD11).
	almanacWindowDays = 30

	// almanacOpenMinSpotsPSKR is k for the PSKR/DX-cluster layer: a
	// (band, region, slot) is open on a day when the ring's summed spot count
	// in that slot reaches k.
	almanacOpenMinSpotsPSKR = 2
	// almanacOpenMinSpotsWSPR is k for the WSPR backfill layer (seasonal
	// drill-down only; never used by the 30-day view, R6).
	almanacOpenMinSpotsWSPR = 1

	// almanacUsuallyShare: the agenda counts a slot as "usually" open when
	// N/M >= this share (and the cell is known: M >= M_min).
	almanacUsuallyShare = 0.5
	// almanacAgendaLookAheadSlots: the agenda lists windows that are open now
	// or start within the next 12 h (24 half-hour slots).
	almanacAgendaLookAheadSlots = 24
	// almanacAgendaBridgeSlots: gaps of at most this many non-usual slots
	// inside a window are bridged.
	almanacAgendaBridgeSlots = 1

	// almanacAliveFraction: slot s of day d is "alive" (ingest was running)
	// when its ingest total is > 0 and at least this fraction of the slot's
	// 30-day median total.
	almanacAliveFraction = 0.10

	// almanacTypicalCacheTTL: the typical part (n/m arrays + radius) is kept
	// this long, or until the fold watermark changes / the UTC day rolls.
	almanacTypicalCacheTTL = 6 * time.Hour
	// almanacTodayCacheTTL: the "open today" overlay (KTD11).
	almanacTodayCacheTTL = 120 * time.Second
	// almanacQueryTimeout bounds one read transaction. prop_intel uses 1.5 s,
	// but on prod (2026-09-28) the cold typical read measured ~0.6 s of SQL
	// (season 64 ms, tail 368 ms, tail active days 181 ms) plus cold-cache I/O,
	// landing at 1.3-1.6 s and timing out. The typical part is cached for 6 h,
	// so one slower cold read is cheap; the negative cache still caps retries.
	almanacQueryTimeout = 4 * time.Second
	// almanacNegCacheTTL: a failed/timed-out read is remembered this long per
	// key before Postgres is tried again (503 meanwhile).
	almanacNegCacheTTL = 30 * time.Second
	// almanacCacheMaxEntries bounds the per-grid4 LRU (≈10 KB per entry).
	almanacCacheMaxEntries = 256
)

// Seasonal drill-down (plan U4: KTD7). M_min is almanacMinActiveDaysSeasonal;
// k per layer is almanacOpenMinSpotsPSKR / almanacOpenMinSpotsWSPR.
const (
	// almanacSeasonLookbackMonths bounds the year-months the drill-down reads
	// (the current month and the 59 before it): five years covers the WSPR
	// backfill depth (-almanac-wspr-backfill-years, default 3) with room to
	// grow, and caps the rows one request streams.
	almanacSeasonLookbackMonths = 60
	// almanacSeasonCacheTTL: a drill-down entry is kept this long, or until
	// the fold watermark changes, the UTC day rolls or the landing view's
	// radius changes.
	almanacSeasonCacheTTL = almanacTypicalCacheTTL
)
