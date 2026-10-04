package main

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

// v2Window is realisticWindow with the source mix of the live hub: ~65% FT8
// from PSKReporter (empty Source), ~30% WSPR (with TX power), ~5% RBN.
func v2Window(n, minutes int) []MQTTMessage {
	hist := realisticWindow(n, minutes)
	// Stations repeat on the live feed (a few thousand distinct 6-character
	// locators per window, heavily skewed), unlike realisticWindow's
	// independent draws: remap both ends onto a skewed pool.
	rng := rand.New(rand.NewSource(17))
	pool := make([]string, 6000)
	for i := range pool {
		pool[i] = hist[rng.Intn(len(hist))].SL
	}
	pick := func() string { return pool[int(math.Pow(rng.Float64(), 2.5)*float64(len(pool)))] }
	for i := range hist {
		hist[i].SL, hist[i].RL = pick(), pick()
	}
	for i := range hist {
		switch {
		case i%20 < 6:
			hist[i].Source, hist[i].MD, hist[i].TXPower = "wspr", "WSPR", []int{20, 23, 37, 43}[i%4]
		case i%20 == 19:
			hist[i].Source, hist[i].MD = "rbn", "CW"
		default:
			hist[i].Source = ""
		}
	}
	return hist
}

func benchV2(b *testing.B, area *liveArea) {
	hist := v2Window(250_000, 15)
	now := time.Now().Unix()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		propIntelV2.EvaluateV2Area("JO32", false, 15, nil, nil, nil, hist, now, 2.0, area)
	}
}

func BenchmarkEvaluateV2GlobalMesh(b *testing.B) { benchV2(b, nil) }
func BenchmarkEvaluateV2WideArea(b *testing.B)   { benchV2(b, explicitLiveArea("JO32", 2)) }

func BenchmarkEvaluateV1(b *testing.B) {
	hist := v2Window(250_000, 15)
	now := time.Now().Unix()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		propIntel.Evaluate("JO32", false, 15, -15, hist, now, 2.0)
	}
}
