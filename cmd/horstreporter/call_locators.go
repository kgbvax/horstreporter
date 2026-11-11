package main

import (
	"strings"
	"sync"
	"time"

	"horstreporter/internal/maidenhead"
)

// Callsign → locator cache for the ingests whose spots name stations by call
// only (RBN). PSKReporter and WSPR spots carry a self-reported grid for both
// stations, so every call seen there is a free lookup: a background pass over
// hub.history (callLocatorWalker) records them, and the QRZ worker
// (qrz_worker.go) adds the calls the feeds never showed, including the ones
// QRZ has no grid for (negative entries, so they are not asked again).
//
// The ingest hot path stays untouched: the walker reads history by sequence
// number on its own goroutine, and RBN reads with resolve, which takes a read
// lock and does not allocate.

type callLocSource uint8

const (
	callLocNone     callLocSource = iota
	callLocFeed                   // PSKReporter / WSPR self-reported grid
	callLocQRZ                    // QRZ.com record
	callLocNegative               // QRZ has no record or no grid
)

// callLocatorEntry: loc is an upper-case 4- or 6-character locator ("" for a
// negative entry); seen is the unix second the value was last confirmed.
type callLocatorEntry struct {
	loc  string
	src  callLocSource
	seen int64
}

const (
	// callLocatorMaxEntries bounds the cache. PSKReporter shows about 40k
	// distinct calls an hour; a few days of them fit.
	callLocatorMaxEntries = 250_000
	// callLocatorFeedTTL is how long a feed grid stays usable without being
	// seen again. Stations move (portable, contest sites); a week covers a
	// contest weekend and the following days.
	callLocatorFeedTTL = 7 * 24 * 3600
	// QRZ answers are kept longer: a home grid rarely changes. Negative
	// answers are retried after a week (new licences, records gaining grids).
	callLocatorQRZTTL      = 30 * 24 * 3600
	callLocatorNegativeTTL = 7 * 24 * 3600
)

// callLocatorCache maps upper-case calls to entries. Entries are pointers so a
// refresh updates seen in place: assigning to an existing string key would
// store the caller's string as the key, which for feed calls is a substring
// of an MQTT topic and would pin it. Keys and locators are cloned on insert.
type callLocatorCache struct {
	mu sync.RWMutex
	m  map[string]*callLocatorEntry
	// full is set when a feed insert was skipped at callLocatorMaxEntries;
	// the walker then shrinks the feed entries (shrinkFeed).
	full bool
}

func newCallLocatorCache() *callLocatorCache {
	return &callLocatorCache{m: make(map[string]*callLocatorEntry, 1<<14)}
}

var callLocators = newCallLocatorCache()

func (e *callLocatorEntry) expired(now int64) bool {
	ttl := int64(callLocatorFeedTTL)
	switch e.src {
	case callLocQRZ:
		ttl = callLocatorQRZTTL
	case callLocNegative:
		ttl = callLocatorNegativeTTL
	}
	return now-e.seen > ttl
}

// resolveState tells a caller where a locator came from, or what to do
// without one.
type resolveState uint8

const (
	resolveMiss     resolveState = iota // unknown: worth a QRZ lookup
	resolveFeed                         // loc from a PSKReporter / WSPR report
	resolveQRZ                          // loc from QRZ
	resolveNegative                     // QRZ has no grid: do not ask again
)

func (s resolveState) ok() bool { return s == resolveFeed || s == resolveQRZ }

// resolve returns the locator for an upper-case callsign at unix second now.
// A call with a portable suffix (/P, /QRP, /M, /A) falls back to the plain
// call; a call with a prefix (EA8/DL1ABC) or /MM, /AM does not, since the
// home grid would place the station in the wrong spot. A feed grid wins: a
// QRZ or negative answer for the portable call yields to a feed grid of the
// plain call, and the plain call's QRZ grid fills in when the portable call
// has no usable answer. It does not allocate.
func (c *callLocatorCache) resolve(call string, now int64) (string, resolveState) {
	if call == "" {
		return "", resolveNegative
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	loc, st := c.lookupLocked(call, now)
	if st == resolveFeed {
		return loc, st
	}
	if base, suffix, ok := strings.Cut(call, "/"); ok && base != "" && portableSuffix(suffix) {
		if bl, bs := c.lookupLocked(base, now); bs == resolveFeed || (bs.ok() && !st.ok()) {
			return bl, bs
		}
	}
	return loc, st
}

func (c *callLocatorCache) lookupLocked(call string, now int64) (string, resolveState) {
	e, ok := c.m[call]
	if !ok || e.expired(now) {
		return "", resolveMiss
	}
	switch e.src {
	case callLocNegative:
		return "", resolveNegative
	case callLocQRZ:
		return e.loc, resolveQRZ
	}
	return e.loc, resolveFeed
}

func portableSuffix(s string) bool {
	switch s {
	case "P", "QRP", "M", "A":
		return true
	}
	return false
}

// observeFeed records that call reported (or was reported at) locator grid at
// unix second t. Feed grids win over QRZ (they are where the station operates
// now) and over negative entries. Within the feed a 6-character grid is kept
// when a 4-character one for the same square arrives; another square replaces
// it (the station moved). Callers pass the feed's raw values: call is
// upper-cased and grid normalized only when the entry changes.
func (c *callLocatorCache) observeFeed(call, grid string, t int64) {
	if len(call) < 3 || len(grid) < 4 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.observeFeedLocked(call, grid, t)
}

func (c *callLocatorCache) observeFeedLocked(call, grid string, t int64) {
	e, ok := c.m[call]
	if !ok && !isUpperASCII(call) {
		call = strings.ToUpper(call)
		e, ok = c.m[call]
	}
	if ok && e.src == callLocFeed {
		if sameGrid(e.loc, grid) {
			if t > e.seen {
				e.seen = t
			}
			return
		}
		if t < e.seen {
			return // older report of another grid
		}
	}
	loc := maidenhead.Normalize(grid)
	if loc == "" {
		return
	}
	loc = strings.Clone(loc) // Normalize may return a substring of the feed's grid
	if ok {
		*e = callLocatorEntry{loc: loc, src: callLocFeed, seen: t}
		return
	}
	if len(c.m) >= callLocatorMaxEntries {
		c.full = true // the walker shrinks the feed entries after this pass
		return
	}
	c.m[strings.Clone(call)] = &callLocatorEntry{loc: loc, src: callLocFeed, seen: t}
}

// sameGrid reports whether the feed's grid adds nothing to the cached
// locator: the same square for a 4-character feed grid, the same subsquare
// for a 6-character one (case-insensitive; the feed's grids are mixed case).
// A 6-character grid for a cached 4-character locator is new information.
func sameGrid(cached, grid string) bool {
	if len(grid) < 6 {
		return strings.EqualFold(cached[:4], grid[:4])
	}
	return len(cached) == 6 && strings.EqualFold(cached, grid[:6])
}

func isUpperASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 'a' && s[i] <= 'z' {
			return false
		}
	}
	return true
}

// storeQRZ records a QRZ answer for call (loc "" = no grid / not found) at
// unix second t. It never replaces a feed grid that is still fresh. It is
// never refused for size: QRZ entries are bounded by the lookup rate, and a
// dropped answer would send the worker to QRZ for the same call again.
func (c *callLocatorCache) storeQRZ(call, loc string, t int64) {
	call = strings.ToUpper(strings.TrimSpace(call))
	if call == "" {
		return
	}
	src := callLocQRZ
	if loc == "" {
		src = callLocNegative
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[call]; ok {
		if e.src == callLocFeed && !e.expired(t) {
			return
		}
		*e = callLocatorEntry{loc: loc, src: src, seen: t}
		return
	}
	c.m[call] = &callLocatorEntry{loc: loc, src: src, seen: t}
}

// prune drops expired entries.
func (c *callLocatorCache) prune(now int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.m {
		if e.expired(now) {
			delete(c.m, k)
		}
	}
}

// shrinkFeed runs after a pass that hit callLocatorMaxEntries: it drops feed
// entries not seen for a day, then (still above 90% of the cap) for six
// hours. QRZ and negative entries stay. It returns how many it dropped.
func (c *callLocatorCache) shrinkFeed(now int64) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.full {
		return 0
	}
	c.full = false
	dropped := 0
	for _, age := range []int64{24 * 3600, 6 * 3600} {
		if len(c.m) < callLocatorMaxEntries*9/10 {
			break
		}
		for k, e := range c.m {
			if e.src == callLocFeed && now-e.seen > age {
				delete(c.m, k)
				dropped++
			}
		}
	}
	return dropped
}

// counts returns the entries per source (stats).
func (c *callLocatorCache) counts() (feed, qrz, negative int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, e := range c.m {
		switch e.src {
		case callLocFeed:
			feed++
		case callLocQRZ:
			qrz++
		case callLocNegative:
			negative++
		}
	}
	return
}

// callLocatorWalker feeds the cache from hub.history. Each pass reads only
// the messages appended since the last one (by sequence number); after the
// startup backfill swaps the history, the retired range makes the next pass
// start at the front again, so backfilled spots are recorded too.
type callLocatorWalker struct {
	lastSeq uint64
}

// pass records the new history messages into c and returns how many it read.
func (w *callLocatorWalker) pass(h *Hub, c *callLocatorCache) int {
	h.RLock()
	high := h.highSeqLocked()
	idx := 0
	if w.lastSeq > h.baseSeq {
		idx = int(w.lastSeq - h.baseSeq)
	}
	if idx > len(h.history) {
		idx = len(h.history)
	}
	view := h.windowFromLocked(idx)
	h.RUnlock()
	w.lastSeq = high

	// Batches under one write lock: the RBN resolve path only ever waits for
	// one batch.
	const batch = 4096
	for start := 0; start < len(view); start += batch {
		end := start + batch
		if end > len(view) {
			end = len(view)
		}
		c.mu.Lock()
		for i := start; i < end; i++ {
			m := &view[i]
			switch m.Source {
			case "", "mqtt", "wspr":
			default:
				continue
			}
			if len(m.SC) >= 3 && len(m.SL) >= 4 {
				c.observeFeedLocked(m.SC, m.SL, m.T)
			}
			if len(m.RC) >= 3 && len(m.RL) >= 4 {
				c.observeFeedLocked(m.RC, m.RL, m.T)
			}
		}
		c.mu.Unlock()
	}
	if n := c.shrinkFeed(time.Now().Unix()); n > 0 {
		logInfo("callsign cache full: dropped %d feed calls not seen recently", n)
	}
	return len(view)
}

const callLocatorWalkInterval = 30 * time.Second

// startCallLocatorWalker runs the walker forever, pruning hourly.
func startCallLocatorWalker() {
	var w callLocatorWalker
	lastPrune := time.Now()
	for {
		w.pass(hub, callLocators)
		if time.Since(lastPrune) > time.Hour {
			callLocators.prune(time.Now().Unix())
			lastPrune = time.Now()
		}
		time.Sleep(callLocatorWalkInterval)
	}
}
