package main

import (
	"errors"
	"fmt"
	"math/bits"
	"strings"
	"sync"
	"time"

	"horstreporter/internal/cty"
)

// Almanac area resolution (plan U1, R4/R5, KTD3): turn a QTH (locator or
// callsign) into a normalized grid4 centre plus a location source, then pick
// the widening radius from per-(grid4, band) active-day masks.

var (
	// errAlmanacInvalidQTH: the QTH is neither a valid Maidenhead locator nor
	// callsign-shaped (e.g. "XX99", "").
	errAlmanacInvalidQTH = errors.New("almanac: invalid qth")
	// errAlmanacUnresolved: a callsign-shaped QTH resolved via neither QRZ nor
	// the DXCC centroid.
	errAlmanacUnresolved = errors.New("almanac: qth could not be located")
)

// almanacArea is a resolved Almanac centre.
type almanacArea struct {
	Grid4  string            // normalized 4-char Maidenhead square, e.g. "JO32"
	Source qthLocationSource // locator | qrz | dxcc
}

// Approximate reports whether the centre is only the DXCC entity centroid
// (R5: the Almanac must say the location is approximate).
func (a almanacArea) Approximate() bool { return a.Source == qthSourceDXCC }

// isAlmanacLocatorShape: two letters + two digits, optionally followed by two
// letters and optionally two more digits (4/6/8 chars). Tokens of this shape
// are treated as locators and must then pass strict validation.
func isAlmanacLocatorShape(s string) bool {
	if len(s) != 4 && len(s) != 6 && len(s) != 8 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		letter := c >= 'A' && c <= 'Z'
		digit := c >= '0' && c <= '9'
		switch i {
		case 0, 1, 4, 5:
			if !letter {
				return false
			}
		default:
			if !digit {
				return false
			}
		}
	}
	return true
}

// validAlmanacLocator is strict Maidenhead on an uppercased locator-shaped
// token: fields A–R, squares 0–9, subsquares A–X, extended 0–9.
func validAlmanacLocator(s string) bool {
	if !isAlmanacLocatorShape(s) || !isLocator(s) {
		return false
	}
	if len(s) >= 6 && (s[4] > 'X' || s[5] > 'X') {
		return false
	}
	return true
}

// isAlmanacCallsignShape: 3–15 chars of A–Z/0–9/'/', with at least one letter
// and one digit.
func isAlmanacCallsignShape(s string) bool {
	if len(s) < 3 || len(s) > 15 {
		return false
	}
	var hasL, hasD bool
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			hasL = true
		case c >= '0' && c <= '9':
			hasD = true
		case c == '/':
		default:
			return false
		}
	}
	return hasL && hasD
}

// resolveAlmanacAreaWith resolves qth without caching. Locators are validated
// strictly and truncated to grid4; callsigns go through resolveQTHLocator
// (QRZ, then cty.dat centroid). qrz and ctyRes may be nil.
func resolveAlmanacAreaWith(qth string, qrz CallsignLocatorResolver, ctyRes *cty.Resolver) (almanacArea, error) {
	q := normalizeQTHToken(qth)
	if q == "" {
		return almanacArea{}, fmt.Errorf("%w: empty", errAlmanacInvalidQTH)
	}
	if isAlmanacLocatorShape(q) {
		if !validAlmanacLocator(q) {
			return almanacArea{}, fmt.Errorf("%w: %q is not a valid Maidenhead locator", errAlmanacInvalidQTH, q)
		}
		return almanacArea{Grid4: q[:4], Source: qthSourceLocator}, nil
	}
	if isLocator(q) {
		// Locator prefix with a malformed tail ("JO32A", "JO32AB1"): reject
		// rather than let the lenient isLocator path accept it downstream.
		return almanacArea{}, fmt.Errorf("%w: %q is not a valid Maidenhead locator", errAlmanacInvalidQTH, q)
	}
	if !isAlmanacCallsignShape(q) {
		return almanacArea{}, fmt.Errorf("%w: %q is neither a locator nor a callsign", errAlmanacInvalidQTH, q)
	}
	loc, src, ok := resolveQTHLocator(q, qrz, ctyRes)
	if !ok {
		return almanacArea{}, fmt.Errorf("%w: %s", errAlmanacUnresolved, q)
	}
	return almanacArea{Grid4: strings.ToUpper(loc[:4]), Source: src}, nil
}

type almanacAreaCacheEntry struct {
	area    almanacArea
	err     error
	expires time.Time
}

// almanacAreaCache is a TTL cache of callsign → area resolutions so repeat
// Almanac requests never re-hit QRZ. Locator(-prefixed) inputs and validation errors are
// cheap and bypass it.
type almanacAreaCache struct {
	mu      sync.Mutex
	entries map[string]almanacAreaCacheEntry
	now     func() time.Time
}

func newAlmanacAreaCache() *almanacAreaCache {
	return &almanacAreaCache{entries: map[string]almanacAreaCacheEntry{}, now: time.Now}
}

func (c *almanacAreaCache) resolve(qth string, qrz CallsignLocatorResolver, ctyRes *cty.Resolver) (almanacArea, error) {
	key := normalizeQTHToken(qth)
	if key == "" || isLocator(key) || !isAlmanacCallsignShape(key) {
		return resolveAlmanacAreaWith(key, qrz, ctyRes)
	}
	now := c.now()
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && now.Before(e.expires) {
		c.mu.Unlock()
		return e.area, e.err
	}
	c.mu.Unlock()

	// Resolve outside the lock: QRZ is a network call.
	area, err := resolveAlmanacAreaWith(key, qrz, ctyRes)
	ttl := almanacAreaCacheTTL
	switch {
	case err != nil:
		ttl = almanacAreaNegativeCacheTTL
	case area.Approximate():
		ttl = almanacAreaApproxCacheTTL
	}

	c.mu.Lock()
	if len(c.entries) >= almanacAreaCacheMaxEntries {
		for k, e := range c.entries {
			if !now.Before(e.expires) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= almanacAreaCacheMaxEntries {
			c.entries = map[string]almanacAreaCacheEntry{}
		}
	}
	c.entries[key] = almanacAreaCacheEntry{area: area, err: err, expires: now.Add(ttl)}
	c.mu.Unlock()
	return area, err
}

// resolveAlmanacArea resolves qth to the Almanac centre using the engine's QRZ
// and cty.dat resolvers, cached per engine. A nil engine resolves locators only.
func (e *DxBaselineEngine) resolveAlmanacArea(qth string) (almanacArea, error) {
	if e == nil {
		return resolveAlmanacAreaWith(qth, nil, nil)
	}
	e.mu.Lock()
	if e.almanacAreas == nil {
		e.almanacAreas = newAlmanacAreaCache()
	}
	cache := e.almanacAreas
	qrz := e.callsignResolver
	ctyRes := e.ctyResolver
	e.mu.Unlock()
	return cache.resolve(qth, qrz, ctyRes)
}

// almanacGridBand keys per-(grid4, band) data: the active-day masks, the
// fold's active pairs and the WSPR backfill's area activity.
type almanacGridBand struct {
	Grid string
	Band string
}

// almanacBandsNeeded is how many of n in-scope bands must meet M_min for a
// radius to suffice: strictly more than almanacWidenBandShare of them.
func almanacBandsNeeded(n int) int {
	return int(float64(n)*almanacWidenBandShare) + 1
}

// almanacBandsMeeting counts in-scope bands whose active days, unioned (OR of
// day masks, not summed) across squares, reach mMin.
func almanacBandsMeeting(squares []string, masks map[almanacGridBand]uint64, mMin int) int {
	n := 0
	for _, band := range almanacInScopeBands {
		var union uint64
		for _, sq := range squares {
			union |= masks[almanacGridBand{Grid: sq, Band: band}]
		}
		if bits.OnesCount64(union) >= mMin {
			n++
		}
	}
	return n
}

// chooseAlmanacRadius picks the smallest radius r in 0..almanacMaxWidenRadius
// at which strictly more than half of the in-scope bands have at least mMin
// active days across the (edge-clipped) ring block around center; if none
// does, it returns the cap. masks holds per-(grid4, band) day bitmasks (bit i
// = active on day i). center is normalized to its uppercase grid4.
func chooseAlmanacRadius(center string, masks map[almanacGridBand]uint64, mMin int) (radius int, squares []string) {
	c := almanacNormalizeCentre(center)
	need := almanacBandsNeeded(len(almanacInScopeBands))
	for r := 0; r <= almanacMaxWidenRadius; r++ {
		squares = getSquaresWithinRings(c, r)
		if almanacBandsMeeting(squares, masks, mMin) >= need {
			return r, squares
		}
	}
	return almanacMaxWidenRadius, squares
}
