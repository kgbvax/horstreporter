package main

import (
	"fmt"
	"strings"
)

// almanac_summary.go: the optional "almanac" field of
// /api/prop_intel/summary (plan U6, R17, KTD10). It is filled only from the
// Almanac cache (almanacService.warm): no Postgres read and no QRZ lookup run
// on the widget request path. On a cold cache the field is omitted and the
// summary body is unchanged.

// propIntelSummaryAlmanacMaxEntries caps the structured agenda entries (the
// widget shows a glance, the full agenda is /api/almanac).
const propIntelSummaryAlmanacMaxEntries = 3

// propIntelSummaryAlmanacNowMax caps the "Now usually" pairs in the text.
const propIntelSummaryAlmanacNowMax = 3

// propIntelSummaryAlmanac is the compact widget view of the Almanac agenda.
type propIntelSummaryAlmanac struct {
	// Grid4 is the Almanac centre the agenda was computed for.
	Grid4 string `json:"grid4"`
	// Approximate: the centre is only the DXCC centroid of a callsign QTH.
	Approximate bool `json:"approximate"`
	// Text is a one-line "usually open now / next" summary.
	Text    string                         `json:"text"`
	Entries []propIntelSummaryAlmanacEntry `json:"entries"`
}

// propIntelSummaryAlmanacEntry is one agenda window (times are UTC HH:MM).
type propIntelSummaryAlmanacEntry struct {
	Band   string `json:"band"`
	Region string `json:"region"`
	Start  string `json:"start"`
	End    string `json:"end"`
	// Status is "ongoing" or "upcoming"; StartsInMin is 0 when ongoing.
	Status      string `json:"status"`
	StartsInMin int    `json:"starts_in_min"`
	// N of M: the window's peak slot opened on N of M active days.
	N         int  `json:"n"`
	M         int  `json:"m"`
	OpenToday bool `json:"open_today"`
}

// almanacSummaryArea resolves qth without any network call: locators
// directly, callsigns only from the engine's Almanac area cache (filled by
// earlier /api/almanac requests). ok=false means "don't know yet".
func almanacSummaryArea(qth string) (almanacArea, bool) {
	q := normalizeQTHToken(qth)
	if isAlmanacLocatorShape(q) || isLocator(q) {
		area, err := resolveAlmanacAreaWith(q, nil, nil)
		return area, err == nil
	}
	if !isAlmanacCallsignShape(q) {
		return almanacArea{}, false
	}
	return dxBaseline.cachedAlmanacArea(q)
}

// cachedAlmanacArea returns a still-valid cached resolution for a callsign.
func (e *DxBaselineEngine) cachedAlmanacArea(call string) (almanacArea, bool) {
	if e == nil {
		return almanacArea{}, false
	}
	e.mu.Lock()
	cache := e.almanacAreas
	e.mu.Unlock()
	if cache == nil {
		return almanacArea{}, false
	}
	return cache.cached(call)
}

// cached peeks at the cache without resolving.
func (c *almanacAreaCache) cached(qth string) (almanacArea, bool) {
	key := normalizeQTHToken(qth)
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || e.err != nil || !now.Before(e.expires) {
		return almanacArea{}, false
	}
	return e.area, true
}

// propIntelSummaryAlmanacFor builds the field for qth, or nil when the
// Almanac is unavailable, its cache is cold for that area, or the area is
// data-poor (no lane slot has m >= m_min): an empty agenda there means "not
// enough data", never "closed" (R3), so the widget must not show it.
func propIntelSummaryAlmanacFor(qth string) *propIntelSummaryAlmanac {
	if almanacSvc == nil {
		return nil
	}
	area, ok := almanacSummaryArea(qth)
	if !ok {
		return nil
	}
	resp, ok := almanacSvc.warm(area)
	if !ok || resp == nil || !almanacResponseHasKnownSlot(resp) {
		return nil
	}
	out := &propIntelSummaryAlmanac{
		Grid4:       resp.Area.Grid4,
		Approximate: resp.Area.Approximate,
		Text:        propIntelSummaryAlmanacText(resp.Agenda),
		Entries:     make([]propIntelSummaryAlmanacEntry, 0, propIntelSummaryAlmanacMaxEntries),
	}
	for _, e := range resp.Agenda {
		if len(out.Entries) == propIntelSummaryAlmanacMaxEntries {
			break
		}
		out.Entries = append(out.Entries, propIntelSummaryAlmanacEntry{
			Band: e.Band, Region: e.Region, Start: e.Start, End: e.End,
			Status: e.Status, StartsInMin: e.StartsInMin,
			N: e.PeakN, M: e.PeakM, OpenToday: e.OpenToday,
		})
	}
	return out
}

// almanacResponseHasKnownSlot reports whether any lane has a slot with at
// least m_min active days (a known, non-"unknown" cell).
func almanacResponseHasKnownSlot(resp *almanacResponse) bool {
	for i := range resp.Lanes {
		for _, m := range resp.Lanes[i].M {
			if int(m) >= resp.MMin {
				return true
			}
		}
	}
	return false
}

// propIntelSummaryAlmanacText renders the agenda (ongoing first, then by
// start) as e.g. "Now usually: 20m NA, 17m AS. Next: 40m OC ~21:00".
func propIntelSummaryAlmanacText(agenda []almanacAgendaEntry) string {
	var now []string
	seen := map[string]bool{}
	var next *almanacAgendaEntry
	for i := range agenda {
		e := &agenda[i]
		if e.Status == "ongoing" {
			pair := e.Band + " " + e.Region
			if !seen[pair] && len(now) < propIntelSummaryAlmanacNowMax {
				seen[pair] = true
				now = append(now, pair)
			}
			continue
		}
		if next == nil {
			next = e
		}
	}
	var parts []string
	if len(now) > 0 {
		parts = append(parts, "Now usually: "+strings.Join(now, ", "))
	}
	if next != nil {
		parts = append(parts, fmt.Sprintf("Next: %s %s ~%s", next.Band, next.Region, next.Start))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("Nothing usually open in the next %d h", almanacAgendaLookAheadSlots/2)
	}
	return strings.Join(parts, ". ")
}
