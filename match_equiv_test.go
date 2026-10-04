package main

import (
	"math/rand"
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
