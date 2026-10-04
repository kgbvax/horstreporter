package main

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// extractMatchedBandEventAreaRef is the pre-optimization implementation,
// kept verbatim as the behavioural reference for the allocation-free version.
func extractMatchedBandEventAreaRef(m MQTTMessage, qthSet []string, area *liveArea) (matchedBandEvent, bool) {
	band := normalizeBand(m.B)
	if band == "" {
		return matchedBandEvent{}, false
	}
	sc := strings.ToUpper(strings.TrimSpace(m.SC))
	rc := strings.ToUpper(strings.TrimSpace(m.RC))
	sl := strings.ToUpper(strings.TrimSpace(m.SL))
	rl := strings.ToUpper(strings.TrimSpace(m.RL))
	if sl == "" || rl == "" {
		return matchedBandEvent{}, false
	}
	isSender := false
	isReceiver := false
	if area != nil {
		isSender = area.contains(sl)
		isReceiver = area.contains(rl)
	} else {
		for _, t := range qthSet {
			if matchCall(sc, t) || (isLocator(t) && strings.HasPrefix(sl, t)) {
				isSender = true
			}
			if matchCall(rc, t) || (isLocator(t) && strings.HasPrefix(rl, t)) {
				isReceiver = true
			}
		}
	}
	if !isSender && !isReceiver {
		return matchedBandEvent{}, false
	}
	localLocator := sl
	remoteLocator := rl
	if !isSender && isReceiver {
		localLocator = rl
		remoteLocator = sl
	}
	if localLocator == "" || remoteLocator == "" {
		return matchedBandEvent{}, false
	}
	distanceKm := distanceKmForLocators(localLocator, remoteLocator)
	lat1, lon1 := locatorToLatLng(localLocator)
	lat2, lon2 := locatorToLatLng(remoteLocator)
	direction := ""
	if !(lat1 == 0 && lon1 == 0) && !(lat2 == 0 && lon2 == 0) {
		direction = bearingDirection(lat1, lon1, lat2, lon2)
	}
	dedupBucket := m.T / dxDedupWindowSeconds
	dedupKey := fmt.Sprintf("%d|%s|%s|%s|%s|%s", dedupBucket, band, sc, rc, sl, rl)
	qualityKey := band + "|" + sc + "|" + rc + "|" + remoteLocator
	remote4 := ""
	if isLocator(remoteLocator) {
		remote4 = remoteLocator[:4]
	}
	return matchedBandEvent{
		band: band, remote4: remote4, qualityKey: qualityKey, dedupKey: dedupKey,
		snr: m.RP, distanceKm: distanceKm, direction: direction, tx: sc, rx: rc, timestampSec: m.T,
	}, true
}

func TestExtractMatchedBandEventAreaMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	locs := []string{"JO32", "jo32lk", "JO32LK", " jo33aa ", "JO31", "IO91wx", "FN20", "fn21XA", "RR99", "rr99zz", "SS00", "J", "JO3", "JO3x", "", "  ", "AA00", "aa00aa", "Ro32"}
	calls := []string{"DK3JF", "dk3jf", " W1AW ", "DK3JF/P", "F/DK3JF", "DK3JF/M/QRP", "", "N0CALL", "dl1abc"}
	bands := []string{"20m", "20M", " 40m ", "10", "", "6m", "13cm", "160M"}
	areas := []*liveArea{nil, explicitLiveArea("JO32", 0), explicitLiveArea("JO32", 1), explicitLiveArea("FN20", 2), explicitLiveArea("IO91", 5)}
	qthSets := [][]string{{"JO32"}, {"DK3JF"}, {"JO32", "JO33", "JO31"}, {"FN20", "W1AW"}, {}}
	pick := func(xs []string) string { return xs[rng.Intn(len(xs))] }
	for i := 0; i < 200000; i++ {
		m := MQTTMessage{T: rng.Int63n(2_000_000_000), RP: rng.Intn(60) - 30, B: pick(bands), SC: pick(calls), RC: pick(calls), SL: pick(locs), RL: pick(locs)}
		area := areas[rng.Intn(len(areas))]
		qs := qthSets[rng.Intn(len(qthSets))]
		got, gok := extractMatchedBandEventArea(m, qs, area)
		want, wok := extractMatchedBandEventAreaRef(m, qs, area)
		if gok != wok || got != want {
			t.Fatalf("mismatch for %+v area=%v qs=%v\n got  %+v %v\n want %+v %v", m, area, qs, got, gok, want, wok)
		}
	}
}

func TestLiveAreaContainsCaseInsensitive(t *testing.T) {
	a := explicitLiveArea("JO32", 1)
	for _, c := range []struct {
		loc  string
		want bool
	}{{"JO32", true}, {"jo32", true}, {"jo33lk", true}, {"JO34", false}, {"jo42", true}, {"IO32", false}, {"", false}, {"JO3", false}, {"ZZ99", false}, {"jo3x", false}} {
		if got := a.contains(c.loc); got != c.want {
			t.Errorf("contains(%q) = %v, want %v", c.loc, got, c.want)
		}
	}
}

// locatorSquareXY dropped its redundant ToUpper, and clusterEndCount /
// locatorInCluster now fold case in place: they must agree with the
// upper-casing versions on any input.
func TestLocatorHelpersAgreeWithUpperCasingReference(t *testing.T) {
	refXY := func(locator string) (int, int, bool) {
		if !isLocator(locator) {
			return 0, 0, false
		}
		loc := strings.ToUpper(locator[:4])
		return int(loc[0]-'A')*10 + int(loc[2]-'0'), int(loc[1]-'A')*10 + int(loc[3]-'0'), true
	}
	rng := rand.New(rand.NewSource(11))
	alphabet := "ABCRSabcrs09 xX-/"
	for i := 0; i < 200000; i++ {
		n := rng.Intn(8)
		b := make([]byte, n)
		for j := range b {
			b[j] = alphabet[rng.Intn(len(alphabet))]
		}
		loc := string(b)
		x, y, ok := locatorSquareXY(loc)
		rx, ry, rok := refXY(loc)
		if x != rx || y != ry || ok != rok {
			t.Fatalf("locatorSquareXY(%q) = %d,%d,%v want %d,%d,%v", loc, x, y, ok, rx, ry, rok)
		}
		up := strings.ToUpper(loc)
		ax, ay := rng.Intn(180), rng.Intn(180)
		cx, cy, cok := refXY(up)
		want := false
		if cok {
			px, py := clusterAnchorXY(cx, cy)
			want = px == ax && py == ay
		}
		if got := locatorInCluster(loc, ax, ay); got != want {
			t.Fatalf("locatorInCluster(%q, %d, %d) = %v want %v", loc, ax, ay, got, want)
		}
	}
	for _, md := range []string{"", "FT8", "ft8", " Ft4 ", "dxcluster", "WSPR", "CW", "rtty", "FT8 "} {
		want := true
		switch strings.ToUpper(strings.TrimSpace(md)) {
		case "", "FT8", "FT4", "DXCLUSTER":
			want = false
		}
		if got := isNonConditionsMode(md); got != want {
			t.Errorf("isNonConditionsMode(%q) = %v want %v", md, got, want)
		}
	}
}
