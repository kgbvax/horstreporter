package main

import "testing"

func benchMsgs() []MQTTMessage {
	locs := []string{"JO31lk", "IO91wx", "FN20ab", "JO32gu", "EM73ss", "PM95tq", "JN48rl", "QF22le"}
	out := make([]MQTTMessage, 4096)
	for i := range out {
		out[i] = MQTTMessage{T: 1700000000 + int64(i), B: "20m", MD: "FT8", SC: "DK3JF", RC: "W1AW", SL: locs[i%len(locs)], RL: locs[(i*7+3)%len(locs)], RP: -10, Source: "mqtt"}
	}
	return out
}

func BenchmarkMatchAndCreateSpotArea(b *testing.B) {
	msgs := benchMsgs()
	c := &Client{qthSet: qthSquares("JO32", false), areaActive: true, areaRings: 2}
	c.areaX, c.areaY, _ = locatorSquareXY("JO32")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		matchAndCreateSpot(c, msgs[i&4095], 1700000100)
	}
}

func BenchmarkMatchAndCreateSpotQthOnly(b *testing.B) {
	msgs := benchMsgs()
	c := &Client{qthSet: qthSquares("JO32", true)}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		matchAndCreateSpot(c, msgs[i&4095], 1700000100)
	}
}
