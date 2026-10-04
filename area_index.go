package main

import (
	"sort"
	"strings"
	"sync"
)

// Per-area index over hub.history.
//
// dx_conditions and hot_bands scan the whole requested window on every
// request (each message is tested against the area and the operator's cluster
// block), but only a few percent of the window can matter to any one area.
// The index remembers, per (area, cluster) key, which history sequence
// numbers matched. Hub history is append-only with stable sequence numbers
// (hub.go), so a request only has to test the messages that arrived since the
// previous request for that key, and hands the evaluation the positions of the
// matching messages to walk. Cost per request goes from the whole window to the
// new arrivals plus the matches.
//
// The predicate is a superset of every test the evaluation applies to a
// message (see areaIndexKey.wants), so evaluating the filtered slice gives the
// same result as walking the whole window; area_index_test.go checks that on
// random data.

// historyWindow is a read-only view of hub history plus the sequence numbers
// the area index needs. firstSeq == 0 marks a window without sequence info
// (tests, other callers): it is never indexed.
type historyWindow struct {
	msgs     []MQTTMessage
	firstSeq uint64 // sequence number of msgs[0]
	hubFirst uint64 // sequence number of the oldest message hub history retains
}

const (
	// Beyond this radius an area matches a large share of the feed and the
	// index would cost memory for no gain.
	areaIndexMaxRadius = 6
	// Distinct (area, cluster) keys kept; least recently used is evicted.
	areaIndexMaxEntries = 32
	// Above this share of matching messages (matches / window > Num/Den) the
	// index is skipped; see positions.
	areaIndexMaxShareNum = 1
	areaIndexMaxShareDen = 2
)

// areaIndexEnabled is a test seam (equivalence tests run both ways).
var areaIndexEnabled = true

type areaIndexKey struct {
	x, y, radius int // area centre square and Chebyshev radius
	clusterOK    bool
	cx, cy       int // operator cluster anchor (valid when clusterOK)
}

func (k areaIndexKey) endIn(loc string) bool {
	x, y, ok := locatorSquareXYFold(loc)
	if !ok {
		return false
	}
	if absInt(x-k.x) <= k.radius && absInt(y-k.y) <= k.radius {
		return true
	}
	if k.clusterOK {
		ax, ay := clusterAnchorXY(x, y)
		return ax == k.cx && ay == k.cy
	}
	return false
}

// wants reports whether a message can influence an evaluation for this key:
// one of its ends lies in the area block (the area/qthSet match and the
// activity series) or in the cluster block (the regional count). Locators are
// trimmed like the evaluation trims them.
func (k areaIndexKey) wants(m *MQTTMessage) bool {
	return k.endIn(strings.TrimSpace(m.SL)) || k.endIn(strings.TrimSpace(m.RL))
}

type areaIndexEntry struct {
	mu       sync.Mutex
	base     uint64   // seq the offsets in `offs` are relative to (== from)
	from     uint64   // oldest seq covered
	scanned  uint64   // newest seq examined; covered range is [from, scanned]
	offs     []uint32 // ascending offsets (seq - base) of matching messages
	lastUsed uint64   // areaIndexCache.tick at last use, for eviction; guarded by areaIndexCache.mu
}

type areaIndexCache struct {
	mu   sync.Mutex
	m    map[areaIndexKey]*areaIndexEntry
	tick uint64 // use counter: a clock can repeat a value, a counter cannot
}

var areaIdx = &areaIndexCache{m: make(map[areaIndexKey]*areaIndexEntry)}

func (c *areaIndexCache) entry(k areaIndexKey) *areaIndexEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.m[k]
	if e == nil {
		if len(c.m) >= areaIndexMaxEntries {
			var oldestKey areaIndexKey
			oldest := ^uint64(0)
			for kk, ee := range c.m {
				if ee.lastUsed < oldest {
					oldest, oldestKey = ee.lastUsed, kk
				}
			}
			delete(c.m, oldestKey)
		}
		e = &areaIndexEntry{}
		c.m[k] = e
	}
	c.tick++
	e.lastUsed = c.tick
	return e
}

// positions returns the indexes into win.msgs of the messages that match k,
// ascending (history order), after bringing the entry up to date. ok is false
// when the area matches a large share of the window: walking all of it is then
// as cheap as walking the matches, and the evaluation skips the rest itself.
func (c *areaIndexCache) positions(k areaIndexKey, win historyWindow) (pos []uint32, ok bool) {
	n := len(win.msgs)
	if n == 0 {
		return nil, false
	}
	e := c.entry(k)
	last := win.firstSeq + uint64(n) - 1

	e.mu.Lock()
	defer e.mu.Unlock()

	// Coverage must reach back to the window start and connect to it.
	// Anything else (first use, a longer window than before, a gap after the
	// entry went unused) is rebuilt from the window.
	if e.scanned == 0 || win.firstSeq < e.from || win.firstSeq > e.scanned+1 || last < e.scanned || last-e.base >= 1<<31 {
		e.base, e.from, e.scanned = win.firstSeq, win.firstSeq, win.firstSeq-1
		e.offs = e.offs[:0]
	}
	// Forget what hub history no longer retains.
	if win.hubFirst > e.from {
		cut := sort.Search(len(e.offs), func(i int) bool { return e.base+uint64(e.offs[i]) >= win.hubFirst })
		e.offs = append(e.offs[:0], e.offs[cut:]...)
		e.from = win.hubFirst
		if e.scanned+1 < e.from {
			e.scanned = e.from - 1
		}
	}
	// Test only what arrived since the last request for this key.
	for seq := e.scanned + 1; seq <= last; seq++ {
		if seq < win.firstSeq {
			continue
		}
		if k.wants(&win.msgs[seq-win.firstSeq]) {
			e.offs = append(e.offs, uint32(seq-e.base))
		}
	}
	e.scanned = last

	lo := sort.Search(len(e.offs), func(i int) bool { return e.base+uint64(e.offs[i]) >= win.firstSeq })
	matches := e.offs[lo:]
	if len(matches)*areaIndexMaxShareDen > n*areaIndexMaxShareNum {
		return nil, false
	}
	pos = make([]uint32, len(matches))
	shift := win.firstSeq - e.base // seq - firstSeq == off - shift
	for i, off := range matches {
		pos[i] = uint32(uint64(off) - shift)
	}
	return pos, true
}

// areaIndexKeyFor decides whether an evaluation can use the index and with
// which key. matchArea is the wide area (radius > 1) when set; otherwise the
// evaluation matches the qth's own square (or the 3x3 block with
// surroundings), which needs a locator qth: callsign targets match by call,
// not by square.
func areaIndexKeyFor(qth string, surroundings bool, matchArea *liveArea, operatorCluster string) (areaIndexKey, bool) {
	var k areaIndexKey
	switch {
	case matchArea != nil:
		if matchArea.Radius > areaIndexMaxRadius {
			return k, false
		}
		k.x, k.y, k.radius = matchArea.x, matchArea.y, matchArea.Radius
	case isLocator(qth):
		x, y, ok := locatorSquareXY(qth[:4])
		if !ok {
			return k, false
		}
		k.x, k.y = x, y
		if surroundings {
			k.radius = 1
		}
	default:
		return k, false
	}
	if cx, cy, ok := locatorSquareXY(operatorCluster); ok {
		k.clusterOK, k.cx, k.cy = true, cx, cy
	}
	return k, true
}
