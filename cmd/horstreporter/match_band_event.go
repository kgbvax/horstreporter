package main

import (
	"hash/maphash"
	"strconv"
	"strings"
)

// ownSquareArea is the live area a locator qth stands for when no wider area
// applies: its own 4-char square, or the 3x3 block with surroundings. The
// caller has checked isLocator(qth). Matching through it equals matching the
// qthSquares tokens, apart from a callsign spelled like a locator token, which
// no station can have.
func ownSquareArea(qth string, surroundings bool) *liveArea {
	radius := 0
	if surroundings {
		radius = 1
	}
	// explicitLiveArea refuses radius 0; the own square is a block too.
	x, y, ok := locatorSquareXY(qth[:4])
	if !ok {
		return nil
	}
	return &liveArea{Centre: qth[:4], BaseRadius: radius, Radius: radius, x: x, y: y}
}

// bandHit is what the window scan needs from one matching message. The byte
// slices point into the buffer passed to matchBandEvent and are valid until it
// is reused; lookups with string(b) do not allocate, so only a new key is ever
// copied into a map.
type bandHit struct {
	snr        int
	distanceKm float64
	direction  string // one of the fixed sector names
	key        []byte // dedup key: "%d|band|SC|RC|SL|RL", upper-cased
	// Set unless lite: the upper-cased fields the unique-sets are keyed by.
	tx, rx, remote []byte
}

// matchBandEvent is extractMatchedBandEventArea for the window scan, which
// calls it for every message that can matter to a request. band is the
// message's normalized, in-scope band. In lite mode (hot_bands) it leaves out
// what that endpoint does not read: the direction and the tx / rx / remote
// fields.
//
// With an area the work is an allocation-free case-folded reject, then the
// distance from locators decoded in place and the key built in kb; without one
// (callsign targets) it runs the reference implementation.
func matchBandEvent(m *MQTTMessage, qthSet []string, area *liveArea, band string, lite bool, kb []byte) (h bandHit, ok bool) {
	if area == nil {
		ev, ok := extractMatchedBandEventArea(*m, qthSet, nil)
		if !ok {
			return h, false
		}
		h.snr, h.distanceKm, h.direction = ev.snr, ev.distanceKm, ev.direction
		h.key = append(kb, ev.dedupKey...)
		if !lite {
			h.tx, h.rx, h.remote = []byte(ev.tx), []byte(ev.rx), []byte(remoteOf(ev))
		}
		return h, true
	}
	slTrim := strings.TrimSpace(m.SL)
	rlTrim := strings.TrimSpace(m.RL)
	if slTrim == "" || rlTrim == "" {
		return h, false
	}
	isSender := area.contains(slTrim)
	isReceiver := area.contains(rlTrim)
	if !isSender && !isReceiver {
		return h, false
	}
	local, remote := slTrim, rlTrim
	if !isSender && isReceiver {
		local, remote = rlTrim, slTrim
	}

	lat1, lon1 := locatorToLatLngFold(local)
	lat2, lon2 := locatorToLatLngFold(remote)
	if !(lat1 == 0 && lon1 == 0) && !(lat2 == 0 && lon2 == 0) {
		h.distanceKm = haversineKm(lat1, lon1, lat2, lon2)
		if !lite {
			h.direction = bearingDirection(lat1, lon1, lat2, lon2)
		}
	}
	h.snr = m.RP

	// "%d|band|SC|RC|SL|RL", the upper-cased fields, built in place.
	kb = strconv.AppendInt(kb, m.T/dxDedupWindowSeconds, 10)
	kb = append(kb, '|')
	kb = append(kb, band...)
	kb = append(kb, '|')
	scAt := len(kb)
	kb = appendUpperTrim(kb, m.SC)
	rcAt := len(kb) + 1
	kb = append(kb, '|')
	kb = appendUpperTrim(kb, m.RC)
	slAt := len(kb) + 1
	kb = append(kb, '|')
	kb = appendUpperTrim(kb, slTrim)
	rlAt := len(kb) + 1
	kb = append(kb, '|')
	kb = appendUpperTrim(kb, rlTrim)
	h.key = kb
	if lite {
		return h, true
	}
	h.tx = kb[scAt : rcAt-1]
	h.rx = kb[rcAt : slAt-1]
	if isSender || !isReceiver { // the remote end is the receiver
		h.remote = kb[rlAt:]
	} else {
		h.remote = kb[slAt : rlAt-1]
	}
	return h, true
}

// remoteOf is the remote locator of an event built by the reference
// implementation: the quality key is "band|tx|rx|remote".
func remoteOf(ev matchedBandEvent) string {
	if i := strings.LastIndexByte(ev.qualityKey, '|'); i >= 0 {
		return ev.qualityKey[i+1:]
	}
	return ""
}

// appendUpperTrim appends strings.ToUpper(strings.TrimSpace(s)) to dst without
// building either string for the ASCII text the feeds send.
func appendUpperTrim(dst []byte, s string) []byte {
	s = strings.TrimSpace(s)
	start := len(dst)
	dst = append(dst, s...)
	var high byte
	for i := start; i < len(dst); i++ {
		c := dst[i]
		high |= c
		if c-'a' < 26 { // unsigned wrap: true for 'a'..'z'
			dst[i] = c - ('a' - 'A')
		}
	}
	if high >= 0x80 { // non-ASCII: let the library decide how to fold it
		return append(dst[:start], strings.ToUpper(s)...)
	}
	return dst
}

// locatorToLatLngFold is locatorToLatLng(strings.ToUpper(locator)) without the
// upper-cased copy.
func locatorToLatLngFold(locator string) (float64, float64) {
	if len(locator) < 2 {
		return 0, 0
	}
	for i := 0; i < len(locator); i++ {
		if locator[i] >= 0x80 {
			return locatorToLatLng(locator)
		}
	}
	up := func(c byte) byte {
		if 'a' <= c && c <= 'z' {
			return c - ('a' - 'A')
		}
		return c
	}
	lng := float64(up(locator[0])-'A')*20 - 180
	lat := float64(up(locator[1])-'A')*10 - 90
	if len(locator) >= 4 {
		lng += float64(up(locator[2])-'0') * 2
		lat += float64(up(locator[3])-'0') * 1
		if len(locator) >= 6 {
			lng += float64(up(locator[4])-'A')*(5.0/60.0) + (5.0 / 120.0)
			lat += float64(up(locator[5])-'A')*(2.5/60.0) + (2.5 / 120.0)
		} else {
			lng += 1.0
			lat += 0.5
		}
	} else {
		lng += 10.0
		lat += 5.0
	}
	return lat, lng
}

// keySet is a set of byte strings kept as 128-bit hashes (two independently
// seeded 64-bit hashes of the bytes), for the evaluation's dedup and unique
// counts: it only ever asks "seen before?" and "how many?", and a 128-bit
// collision over a window's few hundred thousand keys has probability ~1e-27.
type keySet map[[2]uint64]struct{}

var keySetSeeds = [2]maphash.Seed{maphash.MakeSeed(), maphash.MakeSeed()}

func hashKey(b []byte) [2]uint64 {
	return [2]uint64{maphash.Bytes(keySetSeeds[0], b), maphash.Bytes(keySetSeeds[1], b)}
}

// add inserts b and reports whether it was new. One map operation: right for
// a set that mostly receives new keys (the dedup set).
func (s keySet) add(b []byte) bool {
	k := hashKey(b)
	n := len(s)
	s[k] = struct{}{}
	return len(s) != n
}

// note inserts b if absent. Looking up first is cheaper than assigning when
// most keys are already there (a few thousand stations seen again and again).
func (s keySet) note(b []byte) {
	k := hashKey(b)
	if _, ok := s[k]; !ok {
		s[k] = struct{}{}
	}
}

// squareSet counts distinct 4-char grid squares with a bitmap indexed by the
// square's grid coordinates: no hashing for a set that has at most 32400
// members and is hit by every message.
type squareSet struct {
	bits [(180*180 + 63) / 64]uint64
	n    int
}

// add records the square of an upper-cased locator that isLocator accepted.
func (s *squareSet) add(loc []byte) {
	i := (int(loc[0]-'A')*10+int(loc[2]-'0'))*180 + int(loc[1]-'A')*10 + int(loc[3]-'0')
	w, b := i>>6, uint64(1)<<(uint(i)&63)
	if s.bits[w]&b == 0 {
		s.bits[w] |= b
		s.n++
	}
}

func (s *squareSet) count() int {
	if s == nil {
		return 0
	}
	return s.n
}
