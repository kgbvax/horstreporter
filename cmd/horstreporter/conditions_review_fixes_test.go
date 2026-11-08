package main

import "testing"

// A flat band whose newest bin is still half-filled must not read "falling".
func TestComputeTrendIgnoresFillingNewestBin(t *testing.T) {
	series := []float64{100, 100, 100, 100, 100, 100, 100, 100, 100, 100, 100, 40}
	if trend, _ := computeTrend(series); trend != "stable" {
		t.Fatalf("trend = %q, want stable", trend)
	}
	rising := []float64{10, 10, 10, 10, 10, 10, 90, 90, 90, 90, 90, 30}
	if trend, _ := computeTrend(rising); trend != "rising" {
		t.Fatalf("trend = %q, want rising", trend)
	}
}

func TestSustainedRecentBinsSkipsFillingNewestBin(t *testing.T) {
	if got := sustainedRecentBins([]float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 80, 90, 10}, 30); got != 2 {
		t.Fatalf("sustained = %d, want 2 (newest partial bin skipped, not counted)", got)
	}
	if got := sustainedRecentBins([]float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 80, 90, 95}, 30); got != 3 {
		t.Fatalf("sustained = %d, want 3", got)
	}
	if got := sustainedRecentBins([]float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 10, 10}, 30); got != 0 {
		t.Fatalf("sustained = %d, want 0", got)
	}
}

// A thin but excellent sample (raw 70, confidence 0.32) against q25/q75 of
// 35/55 used to score 22 and read red; shrunk toward the baseline middle it
// reads yellow, and with full support green.
func TestBandStatusScoreShrinksTowardBaselineMiddle(t *testing.T) {
	const q25, q75 = 35.0, 55.0
	thin := bandStatusScore(70, 70*0.32, 0.32, q25, q75, true)
	if got := classifyBandStatus(thin, q25, q75, true, 8); got != "yellow" {
		t.Fatalf("thin excellent sample: status %q (score %.1f), want yellow", got, thin)
	}
	full := bandStatusScore(70, 70, 1, q25, q75, true)
	if got := classifyBandStatus(full, q25, q75, true, 40); got != "green" {
		t.Fatalf("full support: status %q, want green", got)
	}
	poor := bandStatusScore(10, 10, 1, q25, q75, true)
	if got := classifyBandStatus(poor, q25, q75, true, 40); got != "red" {
		t.Fatalf("poor, full support: status %q, want red", got)
	}
}
