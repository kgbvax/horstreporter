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
		if rng.Intn(100) < 40 { // Europe
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
