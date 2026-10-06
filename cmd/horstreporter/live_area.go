package main

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// live_area.go is the adaptive "area of interest" of the live views. The live
// analytics (stream, dx_conditions, hot_bands, prop_intel v2) are scoped to the
// operator's own grid square, or the 3×3 block with surroundings. In a sparse
// FT8 region that leaves too few reports for a score, a verdict or a matrix
// cell (the baseline is fine: it is keyed on the 6×6 cluster). rings=auto asks
// the server to widen the block ring by ring until enough bands carry a full
// sample. Dense areas resolve to the base radius, so their output is unchanged.

const (
	// liveAreaMaxRadius caps widening: 3 rings = a 7×7 block of squares.
	liveAreaMaxRadius = 3
	// liveAreaMinBands is how many bands must reach liveAreaFullSampleLinks at
	// a radius for it to suffice.
	liveAreaMinBands = 3
	// liveAreaFullSampleLinks is the per-band link count at which the band
	// score's spot support saturates (spotSupport in dx_conditions.go).
	liveAreaFullSampleLinks = 25
	// liveAreaDecisionMinutes is the history span the decision looks at.
	liveAreaDecisionMinutes = 20
	// liveAreaCacheTTLSeconds keeps a decision so stream, dx_conditions,
	// hot_bands and prop_intel agree and the radius does not flicker.
	liveAreaCacheTTLSeconds = 600
	// liveAreaUndecidedTTLSeconds bounds how often a scan is repeated while the
	// history cannot support a decision (right after a restart, or a dead feed).
	liveAreaUndecidedTTLSeconds = 60
	// liveAreaCacheMaxEntries bounds the decision cache (one entry per
	// grid4 × base radius).
	liveAreaCacheMaxEntries = 1024
)

// liveArea is a resolved block of grid squares around a centre square. It is
// immutable once built (cached decisions are shared between requests).
type liveArea struct {
	// Centre is the 4-char centre square.
	Centre string `json:"centre"`
	// BaseRadius is the radius without widening: 0 (own square) or 1
	// (surroundings), or the explicit rings value.
	BaseRadius int `json:"base_radius"`
	// Radius is the radius in use; Widened is Radius > BaseRadius.
	Radius  int  `json:"radius"`
	Widened bool `json:"widened"`

	x, y int
	// baseLinks is the per-band link count inside the base block, over the
	// decision window. nil for explicit areas.
	baseLinks map[string]int
}

// contains reports whether locator's 4-char square lies in the block
// (Chebyshev distance ≤ Radius, the same test the stream's area filter uses).
func (a *liveArea) contains(locator string) bool {
	x, y, ok := locatorSquareXYFold(locator)
	if !ok {
		return false
	}
	return absInt(x-a.x) <= a.Radius && absInt(y-a.y) <= a.Radius
}

// widenedBand reports whether a band only has a full sample because the area
// was widened (its base block held fewer than liveAreaFullSampleLinks links).
func (a *liveArea) widenedBand(band string) bool {
	return a != nil && a.Widened && a.baseLinks[band] < liveAreaFullSampleLinks
}

// explicitLiveArea is the area of an explicit rings=N request: fixed radius,
// never widened, no decision involved.
func explicitLiveArea(qth string, rings int) *liveArea {
	x, y, ok := locatorSquareXY(qth)
	if !ok || rings <= 0 {
		return nil
	}
	if rings > maxAreaRings {
		rings = maxAreaRings
	}
	return &liveArea{Centre: qth[:4], BaseRadius: rings, Radius: rings, x: x, y: y}
}

// parseRingsParam reads the rings query parameter: "auto", an integer clamped
// to 0..maxAreaRings, or absent/invalid (0, not auto: legacy behaviour).
func parseRingsParam(r *http.Request) (rings int, auto bool) {
	raw := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("rings")))
	if raw == "auto" {
		return 0, true
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return 0, false
	}
	if v > maxAreaRings {
		v = maxAreaRings
	}
	return v, false
}

// liveAreaForRequest resolves the area a request asked for, or nil when it
// asked for none (no rings param, or a callsign QTH: only locators widen).
func liveAreaForRequest(r *http.Request, qth string, surroundings bool, now int64) *liveArea {
	if !isLocator(qth) {
		return nil
	}
	rings, auto := parseRingsParam(r)
	switch {
	case auto:
		return liveAreaFor(qth, surroundings, now)
	case rings > 0:
		return explicitLiveArea(qth, rings)
	}
	return nil
}

// resolveLiveAreaFrom decides the radius from history (pure, no cache). The
// second result says whether the decision rests on enough evidence to cache
// for the full TTL: false when the in-memory history does not yet cover the
// decision window, or the whole widest block holds fewer than
// liveAreaFullSampleLinks links (a dead or barely started feed). Those cases
// stay at the base radius rather than widening on no data.
func resolveLiveAreaFrom(qth string, surroundings bool, now int64, history []MQTTMessage) (*liveArea, bool) {
	cx, cy, ok := locatorSquareXY(qth)
	if !ok {
		return nil, false
	}
	base := 0
	if surroundings {
		base = 1
	}
	area := &liveArea{Centre: qth[:4], BaseRadius: base, Radius: base, x: cx, y: cy}

	cutoff := now - int64(liveAreaDecisionMinutes)*60
	if since := liveHistoryCompleteSince.Load(); since > cutoff && since < now {
		return area, false
	}

	// counts[band][d]: links whose nearer end is at Chebyshev distance d.
	counts := make(map[string]*[liveAreaMaxRadius + 1]int)
	for i := range history {
		m := &history[i]
		if m.T < cutoff || m.T > now || m.RP < defaultDxCwViableMinDb {
			continue
		}
		// Same population the conditions accumulator scores: FT8/FT4 and DX
		// cluster, not RBN/WSPR, HF/low-VHF bands, both locators present.
		if isNonConditionsMode(m.MD) || !feedsClusterBaseline(*m) {
			continue
		}
		band := normalizeBand(m.B)
		if band == "" || !bandInScope(band) {
			continue
		}
		sx, sy, sok := locatorSquareXY(strings.ToUpper(strings.TrimSpace(m.SL)))
		rx, ry, rok := locatorSquareXY(strings.ToUpper(strings.TrimSpace(m.RL)))
		if !sok || !rok {
			continue
		}
		d := min(max(absInt(sx-cx), absInt(sy-cy)), max(absInt(rx-cx), absInt(ry-cy)))
		if d > liveAreaMaxRadius {
			continue
		}
		c := counts[band]
		if c == nil {
			c = new([liveAreaMaxRadius + 1]int)
			counts[band] = c
		}
		c[d]++
	}

	cum := func(c *[liveAreaMaxRadius + 1]int, r int) int {
		n := 0
		for d := 0; d <= r; d++ {
			n += c[d]
		}
		return n
	}
	total := 0
	area.baseLinks = make(map[string]int, len(counts))
	for band, c := range counts {
		total += cum(c, liveAreaMaxRadius)
		area.baseLinks[band] = cum(c, base)
	}
	if total < liveAreaFullSampleLinks {
		return area, false
	}

	radius := liveAreaMaxRadius
	for r := base; r <= liveAreaMaxRadius; r++ {
		full := 0
		for _, c := range counts {
			if cum(c, r) >= liveAreaFullSampleLinks {
				full++
			}
		}
		if full >= liveAreaMinBands {
			radius = r
			break
		}
	}
	area.Radius = radius
	area.Widened = radius > base
	return area, true
}

type liveAreaCacheEntry struct {
	area    *liveArea
	expires int64
}

var liveAreaCache = struct {
	sync.Mutex
	m map[string]liveAreaCacheEntry
}{m: make(map[string]liveAreaCacheEntry)}

// liveAreaComputeMu serializes decisions. One decision copies the last 20
// minutes of hub history (tens of MB on a busy feed), and a page load fires
// stream, dx_conditions, hot_bands and prop_intel at once; without this each
// of them would hold its own copy on a cache miss.
var liveAreaComputeMu sync.Mutex

func liveAreaCached(key string, now int64) (*liveArea, bool) {
	liveAreaCache.Lock()
	defer liveAreaCache.Unlock()
	e, hit := liveAreaCache.m[key]
	if hit && e.expires > now {
		return e.area, true
	}
	return nil, false
}

// liveAreaFor is the cached resolver the handlers use: one decision per
// (grid4, base radius) shared by every endpoint for liveAreaCacheTTLSeconds.
func liveAreaFor(qth string, surroundings bool, now int64) *liveArea {
	if !isLocator(qth) {
		return nil
	}
	key := qth[:4] + "|0"
	if surroundings {
		key = qth[:4] + "|1"
	}
	if area, hit := liveAreaCached(key, now); hit {
		return area
	}

	liveAreaComputeMu.Lock()
	defer liveAreaComputeMu.Unlock()
	// The request that held the lock before us may have decided this key.
	if area, hit := liveAreaCached(key, now); hit {
		return area
	}

	history, release := snapshotHubHistoryWindow(now, liveAreaDecisionMinutes)
	area, decided := resolveLiveAreaFrom(qth, surroundings, now, history)
	release()
	if area == nil {
		return nil
	}

	ttl := int64(liveAreaUndecidedTTLSeconds)
	if decided {
		ttl = liveAreaCacheTTLSeconds
	}
	liveAreaCache.Lock()
	if len(liveAreaCache.m) >= liveAreaCacheMaxEntries {
		for k, v := range liveAreaCache.m {
			if v.expires <= now {
				delete(liveAreaCache.m, k)
			}
		}
		if len(liveAreaCache.m) >= liveAreaCacheMaxEntries {
			liveAreaCache.m = make(map[string]liveAreaCacheEntry)
		}
	}
	liveAreaCache.m[key] = liveAreaCacheEntry{area: area, expires: now + ttl}
	liveAreaCache.Unlock()
	return area
}
