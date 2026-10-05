package main

import (
	"math/rand"
	"sort"
	"strings"
	"testing"
)

// matchAndCreateSpotRef is the pre-optimisation implementation (upper-cases
// every locator up front), kept as the behavioural reference.
func matchAndCreateSpotRef(client *Client, m MQTTMessage, now int64) (Spot, bool) {
	if len(client.qthSet) == 0 && !client.areaActive {
		return Spot{}, false
	}

	sc, rc := strings.ToUpper(m.SC), strings.ToUpper(m.RC)
	sl, rl := strings.ToUpper(m.SL), strings.ToUpper(m.RL)

	isSender := false
	isReceiver := false

	for _, t := range client.qthSet {
		if matchCall(sc, t) || (isLocator(t) && sl != "" && strings.HasPrefix(sl, t)) {
			isSender = true
		}
		if matchCall(rc, t) || (isLocator(t) && rl != "" && strings.HasPrefix(rl, t)) {
			isReceiver = true
		}
	}

	// Area-of-interest match (O(1), additive): a sender/receiver locator within
	// areaRings grid-squares of the area centre also counts. Backs region feeds.
	if client.areaActive {
		if x, y, ok := locatorSquareXY(sl); ok && absInt(x-client.areaX) <= client.areaRings && absInt(y-client.areaY) <= client.areaRings {
			isSender = true
		}
		if x, y, ok := locatorSquareXY(rl); ok && absInt(x-client.areaX) <= client.areaRings && absInt(y-client.areaY) <= client.areaRings {
			isReceiver = true
		}
	}

	if logLevel == "DEBUG" {
		logDebug("QTH '%v' | Evaluating Spot -> SC:%s RC:%s SL:%s RL:%s | isSender:%v isReceiver:%v", client.qthSet, sc, rc, sl, rl, isSender, isReceiver)
	}

	if !isSender && !isReceiver {
		return Spot{}, false
	}

	var remoteLocator string
	var relation string
	if isSender {
		remoteLocator = rl
		relation = "Sender"
	} else {
		remoteLocator = sl
		relation = "Receiver"
	}

	if remoteLocator == "" {
		if logLevel == "DEBUG" {
			logDebug("QTH '%v' matched as %s, but remote locator is empty. Dropping spot.", client.qthSet, relation)
		}
		return Spot{}, false
	}

	lat, lng := locatorToLatLng(remoteLocator)
	age := ageClamped(now, m.T)

	if logLevel == "DEBUG" {
		logDebug("QTH '%v' matched successfully! Mapped to Remote Locator: %s", client.qthSet, remoteLocator)
	}

	return Spot{
		Lat:             lat,
		Lng:             lng,
		SNR:             m.RP,
		AgeSeconds:      age,
		Locator:         remoteLocator,
		ReporterLocator: reporterLocatorForMessage(m),
		SourceType:      sourceTypeForMessage(m),
		Band:            m.B,
		Sender:          m.SC,
		Receiver:        m.RC,
		T:               m.T,
	}, true
}

func TestMatchAndCreateSpotMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	locs := []string{"JO32", "jo32lk", "JO32LK", "JO31", "jo33", "IO91wx", "FN20", "fn21XA", "RR99", "SS00", "J", "JO3", "", "AA00aa", "Jo32", "JO3x"}
	calls := []string{"DK3JF", "dk3jf", "W1AW", "DK3JF/P", "F/DK3JF", "DK3JF/M/QRP", "", "N0CALL", "dl1abc"}
	qthSets := [][]string{nil, {"JO32"}, {"DK3JF"}, {"JO32", "JO33", "JO31"}, {"FN20", "W1AW"}, qthSquares("JO32", true)}
	pick := func(xs []string) string { return xs[rng.Intn(len(xs))] }
	for i := 0; i < 300000; i++ {
		m := MQTTMessage{T: rng.Int63n(2_000_000_000), RP: rng.Intn(60) - 30, B: "20m", MD: "FT8", SC: pick(calls), RC: pick(calls), SL: pick(locs), RL: pick(locs), Source: pick([]string{"", "mqtt", "wspr", "rbn", "dxcluster"})}
		c := &Client{qthSet: qthSets[rng.Intn(len(qthSets))]}
		if rng.Intn(2) == 0 {
			c.areaActive = true
			c.areaRings = rng.Intn(4)
			c.areaX, c.areaY, _ = locatorSquareXY(strings.ToUpper(pick(locs)))
		}
		got, gok := matchAndCreateSpot(c, m, 2_000_000_100)
		want, wok := matchAndCreateSpotRef(c, m, 2_000_000_100)
		if gok != wok || got != want {
			t.Fatalf("mismatch for %+v client=%+v\n got  %+v %v\n want %+v %v", m, c, got, gok, want, wok)
		}
	}
}

// matchBandEvent (the window scan's version) must give what the reference
// extraction gives, with and without an area, in full and lite mode, on mixed
// case, padded and malformed input.
func TestMatchBandEventMatchesExtract(t *testing.T) {
	rng := rand.New(rand.NewSource(77))
	locs := []string{"JO32", "jo32lk", "JO32LK", " jo33aa ", "JO31", "IO91wx", "FN20", "fn21XA", "RR99", "rr99zz", "SS00", "J", "JO3", "JO3x", "", "  ", "AA00", "aa00aa", "Ro32", "jo32é", "JO32LK  "}
	calls := []string{"DK3JF", "dk3jf", " W1AW ", "DK3JF/P", "F/DK3JF", "", "N0CALL", "dl1abc", "ÄB1CD", "dk3jf "}
	bands := []string{"20m", "20M", " 40m ", "10", "6m", "160M", "80m", "2m", "12m"}
	areas := []*liveArea{nil, explicitLiveArea("JO32", 0), explicitLiveArea("JO32", 1), explicitLiveArea("FN20", 2), explicitLiveArea("IO91", 5)}
	qthSets := [][]string{{"JO32"}, {"DK3JF"}, {"FN20", "W1AW"}}
	pick := func(xs []string) string { return xs[rng.Intn(len(xs))] }
	var buf [160]byte
	matched := 0
	for i := 0; i < 300000; i++ {
		m := MQTTMessage{T: rng.Int63n(2_000_000_000), RP: rng.Intn(60) - 30, B: pick(bands), SC: pick(calls), RC: pick(calls), SL: pick(locs), RL: pick(locs)}
		bi := feedBandIndex(m.B)
		if bi < 0 {
			continue
		}
		band := inScopeBandNames[bi]
		area := areas[rng.Intn(len(areas))]
		qs := qthSets[rng.Intn(len(qthSets))]
		want, wok := extractMatchedBandEventArea(m, qs, area)
		for _, lite := range []bool{false, true} {
			got, gok := matchBandEvent(&m, qs, area, band, lite, buf[:0])
			if gok != wok {
				t.Fatalf("matched=%v want %v for %+v area=%v qs=%v lite=%v", gok, wok, m, area, qs, lite)
			}
			if !wok {
				continue
			}
			if string(got.key) != want.dedupKey || got.snr != want.snr || got.distanceKm != want.distanceKm {
				t.Fatalf("key/snr/distance differ for %+v area=%v lite=%v\n got  %q %d %v\n want %q %d %v", m, area, lite, got.key, got.snr, got.distanceKm, want.dedupKey, want.snr, want.distanceKm)
			}
			if lite {
				continue
			}
			remote := want.qualityKey[strings.LastIndexByte(want.qualityKey, '|')+1:]
			if got.direction != want.direction || string(got.tx) != want.tx || string(got.rx) != want.rx || string(got.remote) != remote {
				t.Fatalf("details differ for %+v area=%v\n got  %q %q %q %q\n want %q %q %q %q", m, area, got.direction, got.tx, got.rx, got.remote, want.direction, want.tx, want.rx, remote)
			}
		}
		if wok {
			matched++
		}
	}
	if matched < 10000 {
		t.Fatalf("only %d matches: the comparison is too weak", matched)
	}
}

func TestLocatorToLatLngFoldMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	alphabet := "ABCRSabcrsxX09 -é"
	for i := 0; i < 200000; i++ {
		n := rng.Intn(9)
		var b strings.Builder
		for j := 0; j < n; j++ {
			r := []rune(alphabet)
			b.WriteRune(r[rng.Intn(len(r))])
		}
		loc := b.String()
		la, lo := locatorToLatLngFold(loc)
		wa, wo := locatorToLatLng(loc)
		if la != wa || lo != wo {
			t.Fatalf("locatorToLatLngFold(%q) = %v,%v want %v,%v", loc, la, lo, wa, wo)
		}
	}
}

// A locator qth is matched through its own-square live area in the window
// scan; that must pick the same messages, with the same events, as the token
// set (qthSquares) the scan used before. (The token set also matched a
// callsign spelled like the locator token itself, which no station can have;
// the area index has always ignored that case too.)
func TestOwnSquareAreaMatchesQthSquares(t *testing.T) {
	rng := rand.New(rand.NewSource(13))
	locs := []string{"JO32", "jo32lk", "JO32LK", " jo33aa ", "JO31", "IO91wx", "FN20", "fn21XA", "J", "JO3", "", "jo22", "JO42xx", "KO32", "jo3"}
	calls := []string{"DK3JF", "dk3jf", " W1AW ", "", "N0CALL", "DL/DK3JF", "DK3JF/P"}
	pick := func(xs []string) string { return xs[rng.Intn(len(xs))] }
	for _, qth := range []string{"JO32", "JO32QM", "FN20", "IO91WX"} {
		for _, surr := range []bool{false, true} {
			set := qthSquares(qth, surr)
			area := ownSquareArea(qth, surr)
			hits := 0
			for i := 0; i < 50000; i++ {
				m := MQTTMessage{T: rng.Int63n(2_000_000_000), RP: rng.Intn(60) - 30, B: "20m", SC: pick(calls), RC: pick(calls), SL: pick(locs), RL: pick(locs)}
				want, wok := extractMatchedBandEventArea(m, set, nil)
				got, gok := extractMatchedBandEventArea(m, nil, area)
				if gok != wok || got != want {
					t.Fatalf("qth=%s surr=%v: area path differs for %+v\n got  %+v %v\n want %+v %v", qth, surr, m, got, gok, want, wok)
				}
				if wok {
					hits++
				}
			}
			if hits == 0 {
				t.Fatalf("qth=%s surr=%v matched nothing", qth, surr)
			}
		}
	}
}

// The selection-based percentiles must equal the sorting reference.
func TestMedianAndP90MatchSortingReference(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for i := 0; i < 3000; i++ {
		n := rng.Intn(60)
		if i%50 == 0 {
			n = 5000 + rng.Intn(20000)
		}
		f := make([]float64, n)
		in := make([]int, n)
		spread := []int{3, 80, 5000, 100000}[rng.Intn(4)]
		for j := range f {
			in[j] = rng.Intn(spread) - spread/2
			f[j] = float64(rng.Intn(spread)) * 1.37
			if i%7 == 0 && j > 0 {
				f[j], in[j] = f[j-1], in[j-1] // many duplicates
			}
		}
		if i%5 == 0 { // sorted and reversed inputs
			sort.Float64s(f)
			sort.Ints(in)
			if i%10 == 0 {
				for a, b := 0, n-1; a < b; a, b = a+1, b-1 {
					f[a], f[b] = f[b], f[a]
					in[a], in[b] = in[b], in[a]
				}
			}
		}
		m, p := medianAndP90Float(f)
		if m != percentileFloat(f, 0.5) || p != percentileFloat(f, 0.9) {
			t.Fatalf("float n=%d: got %v %v want %v %v", n, m, p, percentileFloat(f, 0.5), percentileFloat(f, 0.9))
		}
		mi, pi := medianAndP90Int(in)
		if mi != percentileInt(in, 0.5) || pi != percentileInt(in, 0.9) {
			t.Fatalf("int n=%d spread=%d: got %v %v want %v %v", n, spread, mi, pi, percentileInt(in, 0.5), percentileInt(in, 0.9))
		}
	}
}
