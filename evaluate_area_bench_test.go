package main

import (
	"math/rand"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// realisticWindow builds n FT8 messages over `minutes` with ~2000 distinct
// squares: ~40% of ends in a European block, the rest world-wide, so a 5x5
// area around JO32 matches roughly 10-15% of messages as on the live feed.
func realisticWindow(n, minutes int) []MQTTMessage {
	return europeWindow(n, minutes, 40, 0)
}

// europeWindow is realisticWindow with euPct percent of the ends in the
// European block and clusterPct percent in JO32's operator cluster block. A
// clusterPct near 40 makes more than half the window touch the cluster, as the
// live feed does, so the area index skips and the full scan is the cost.
func europeWindow(n, minutes, euPct, clusterPct int) []MQTTMessage {
	rng := rand.New(rand.NewSource(5))
	bands := []string{"160m", "80m", "40m", "30m", "20m", "17m", "15m", "12m", "10m", "6m"}
	bandW := []int{1, 4, 8, 6, 12, 6, 7, 4, 5, 1}
	pickBand := func() string {
		r := rng.Intn(54)
		for i, w := range bandW {
			if r -= w; r < 0 {
				return bands[i]
			}
		}
		return "20m"
	}
	sub := "abcdefghijklmnopqrstuvwx"
	loc := func() string {
		var f0, f1 byte
		if rng.Intn(100) < clusterPct { // JO32's 6x6 operator cluster block: JN88..JO33
			d1, d2 := byte('0'+rng.Intn(6)), byte(0)
			if y := rng.Intn(6); y < 2 {
				f1, d2 = 'N', byte('8'+y)
			} else {
				f1, d2 = 'O', byte('0'+y-2)
			}
			return string([]byte{'J', f1, d1, d2, sub[rng.Intn(24)], sub[rng.Intn(24)]})
		}
		if rng.Intn(100) < euPct { // Europe
			f0, f1 = byte('I'+rng.Intn(3)), byte('M'+rng.Intn(3))
		} else {
			f0, f1 = byte('A'+rng.Intn(18)), byte('A'+rng.Intn(18))
		}
		return string([]byte{f0, f1, byte('0' + rng.Intn(10)), byte('0' + rng.Intn(10)), sub[rng.Intn(24)], sub[rng.Intn(24)]})
	}
	calls := make([]string, 4000)
	for i := range calls {
		calls[i] = string([]byte{byte('A' + rng.Intn(26)), byte('A' + rng.Intn(26)), byte('0' + rng.Intn(10)), byte('A' + rng.Intn(26)), byte('A' + rng.Intn(26)), byte('A' + rng.Intn(26))})
	}
	now := time.Now().Unix()
	out := make([]MQTTMessage, n)
	for i := range out {
		out[i] = MQTTMessage{T: now - int64(minutes*60) + int64(i)*int64(minutes*60)/int64(n+1), B: pickBand(), MD: "FT8", Source: "mqtt",
			SC: calls[rng.Intn(len(calls))], RC: calls[rng.Intn(len(calls))], SL: loc(), RL: loc(), RP: rng.Intn(40) - 25}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out
}

func benchEngine(b *testing.B, hist []MQTTMessage) *DxBaselineEngine {
	eng := newDxBaselineEngine(filepath.Join(b.TempDir(), "b.json"))
	_ = eng.Load()
	for _, m := range hist[:20000] {
		eng.Observe(m)
	}
	return eng
}

// Compare: go test -run xxx -bench EvaluateArea -benchtime 40x .
func BenchmarkEvaluateAreaFullScan(b *testing.B) {
	hist := realisticWindow(250_000, 15)
	eng := benchEngine(b, hist)
	area := explicitLiveArea("JO32", 2)
	now := time.Now().Unix()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		eng.EvaluateArea("JO32", false, 15, -15, hist, now, area)
	}
}

func BenchmarkEvaluateAreaIndexed(b *testing.B) {
	hist := realisticWindow(250_000, 15)
	eng := benchEngine(b, hist)
	area := explicitLiveArea("JO32", 2)
	now := time.Now().Unix()
	win := historyWindow{msgs: hist, firstSeq: 1, hubFirst: 1}
	eng.EvaluateAreaWindow("JO32", false, 15, -15, win, now, area) // build the index
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		eng.EvaluateAreaWindow("JO32", false, 15, -15, win, now, area)
	}
}

func BenchmarkEvaluateOwnSquareFullScan(b *testing.B) {
	hist := realisticWindow(250_000, 15)
	eng := benchEngine(b, hist)
	now := time.Now().Unix()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		eng.EvaluateArea("JO32", false, 15, -15, hist, now, nil)
	}
}

func BenchmarkEvaluateOwnSquareIndexed(b *testing.B) {
	hist := realisticWindow(250_000, 15)
	eng := benchEngine(b, hist)
	now := time.Now().Unix()
	win := historyWindow{msgs: hist, firstSeq: 1, hubFirst: 1}
	eng.EvaluateAreaWindow("JO32", false, 15, -15, win, now, nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		eng.EvaluateAreaWindow("JO32", false, 15, -15, win, now, nil)
	}
}

// 60-minute windows are the heaviest hot_bands / dx_conditions requests: the
// whole hour (~1.4M messages on a busy feed) is in range. Compare
//
//	go test -run xxx -bench 'Evaluate60' -benchtime 10x -cpuprofile tmp/e60.prof .
func bench60(b *testing.B, qth string, area *liveArea) {
	bench60Eu(b, qth, area, 0)
}

func bench60Eu(b *testing.B, qth string, area *liveArea, clusterPct int) {
	bench60Full(b, qth, false, area, clusterPct)
}

func bench60Full(b *testing.B, qth string, surroundings bool, area *liveArea, clusterPct int) {
	hist := europeWindow(1_400_000, 60, 40, clusterPct)
	eng := benchEngine(b, hist)
	now := time.Now().Unix()
	win := historyWindow{msgs: hist, firstSeq: 1, hubFirst: 1}
	eng.EvaluateAreaWindow(qth, surroundings, 60, -15, win, now, area) // build the index (if one applies)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		eng.EvaluateAreaWindow(qth, surroundings, 60, -15, win, now, area)
	}
}

func BenchmarkEvaluate60Area2(b *testing.B)     { bench60(b, "JO32", explicitLiveArea("JO32", 2)) }
func BenchmarkEvaluate60OwnSquare(b *testing.B) { bench60(b, "JO32", nil) }
func BenchmarkEvaluate60FarQTH(b *testing.B)    { bench60(b, "PM95", explicitLiveArea("PM95", 2)) }
func BenchmarkEvaluate60Area2Dense(b *testing.B) {
	bench60Eu(b, "JO32", explicitLiveArea("JO32", 2), 40)
}
func BenchmarkEvaluate60OwnSquareDense(b *testing.B) { bench60Eu(b, "JO32", nil, 40) }
func BenchmarkEvaluate60Surroundings(b *testing.B)   { bench60Full(b, "JO32", true, nil, 40) }

func benchHot60(b *testing.B, qth string, surroundings bool, area *liveArea, clusterPct int) {
	hist := europeWindow(1_400_000, 60, 40, clusterPct)
	eng := benchEngine(b, hist)
	now := time.Now().Unix()
	win := historyWindow{msgs: hist, firstSeq: 1, hubFirst: 1}
	eng.HotBandsAreaWindow(qth, surroundings, 60, -15, "", win, now, area, hotBandsOptions{}) // build the index
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		eng.HotBandsAreaWindow(qth, surroundings, 60, -15, "", win, now, area, hotBandsOptions{})
	}
}

func BenchmarkHotBands60Area2(b *testing.B) {
	benchHot60(b, "JO32", false, explicitLiveArea("JO32", 2), 40)
}
func BenchmarkHotBands60Surroundings(b *testing.B) { benchHot60(b, "JO32", true, nil, 40) }
func BenchmarkHotBands60OwnSquare(b *testing.B)    { benchHot60(b, "JO32", false, nil, 40) }
