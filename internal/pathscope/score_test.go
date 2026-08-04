package pathscope

import (
	"math"
	"testing"
	"time"

	"horstreporter/internal/region"
)

// fixedTime is a 30-min slot-aligned UTC time so seasonality isn't a factor.
var fixedTime = time.Date(2026, 8, 3, 14, 0, 0, 0, time.UTC)

// newEngine returns a ScoringEngine with a fixed clock for deterministic tests.
func newEngine() *ScoringEngine {
	e := NewScoringEngine()
	e.Now = func() time.Time { return fixedTime }
	return e
}

// TestZScoreZeros covers the degenerate inputs that produce NaN or Inf in
// naive z-score implementations.
func TestZScoreZeros(t *testing.T) {
	cases := []struct {
		live, mean, stddev float64
		want               float64
	}{
		{0, 0, 0, 0},   // nothing happening, nothing to compare
		{10, 5, 0, 0},  // zero stddev → 0 not Inf
		{10, 0, 5, 2},  // positive z
		{0, 10, 5, -2}, // negative z
	}
	for _, c := range cases {
		got := zScore(c.live, c.mean, c.stddev)
		if math.IsNaN(got) || math.IsInf(got, 0) {
			t.Errorf("zScore(%v,%v,%v) = %v (non-finite)", c.live, c.mean, c.stddev, got)
		}
		if got != c.want {
			t.Errorf("zScore(%v,%v,%v) = %v, want %v", c.live, c.mean, c.stddev, got, c.want)
		}
	}
}

// TestModeConfidenceMonotonic: more SNR above threshold → higher confidence.
func TestModeConfidenceMonotonic(t *testing.T) {
	thresh := ModeSNRThresholdDB[ModeFT8]
	steps := []float64{thresh - 12, thresh - 6, thresh, thresh + 6, thresh + 12}
	prev := -1.0
	for _, p50 := range steps {
		conf := modeConfidence(SNRStats{P50: p50, P10: p50 - 6}, ModeFT8)
		if conf <= prev {
			t.Errorf("confidence must be monotonic in p50: at %v got %v, prev %v", p50, conf, prev)
		}
		prev = conf
	}
}

// TestModeConfidenceSSBVsFT8: for the same negative SNR, SSB must read lower
// confidence than FT8 — that's the whole point of the per-mode threshold.
func TestModeConfidenceSSBVsFT8(t *testing.T) {
	stats := SNRStats{P50: -10, P10: -16}
	ft8 := modeConfidence(stats, ModeFT8)
	ssb := modeConfidence(stats, ModeSSB)
	if !(ft8 > ssb) {
		t.Errorf("FT8 conf should exceed SSB at SNR -10: ft8=%v ssb=%v", ft8, ssb)
	}
}

// TestModeConfidenceUnknownLaneReturnsHalf: when no SNR data is present
// (the live OBSERVER hasn't measured this band+region in 30 min) we return
// 0.5 instead of 0.0 so the headline probability doesn't crash.
func TestModeConfidenceUnknownLaneReturnsHalf(t *testing.T) {
	got := modeConfidence(SNRStats{}, ModeFT8)
	if got != 0.5 {
		t.Errorf("expected 0.5 for empty SNR stats, got %v", got)
	}
}

func TestIsWorkable(t *testing.T) {
	if !isWorkable(SNRStats{P50: -10}, ModeFT8) {
		t.Error("FT8 at -10 should be workable")
	}
	if isWorkable(SNRStats{P50: -10}, ModeSSB) {
		t.Error("SSB at -10 should not be workable")
	}
	if isWorkable(SNRStats{P50: -30}, ModeFT8) {
		t.Error("FT8 at -30 should not be workable")
	}
}

// TestSolarModifierCalmAndStorm: under quiet SW the multiplier is ~1; under
// storm it drops below 1. The low-band c-band lift is 1.0 so the multiplier
// there equals the storm penalty directly.
func TestSolarModifierCalmAndStorm(t *testing.T) {
	calm := solarModifier("10M", SolarContext{KP: 2, SFI: 100})
	storm := solarModifier("10M", SolarContext{KP: 8, SFI: 100})
	lowBandStorm := solarModifier("80M", SolarContext{KP: 9, SFI: 70})
	if !(calm > storm) {
		t.Errorf("calm should beat storm: calm=%v storm=%v", calm, storm)
	}
	if !(calm > lowBandStorm) {
		t.Errorf("calm should beat storm on low band: calm=%v lowBandStorm=%v", calm, lowBandStorm)
	}
	if math.Abs(lowBandStorm-0.5) > 0.01 {
		t.Errorf("low-band + Kp=9 should hit 0.5 floor: got %v", lowBandStorm)
	}
}

// TestCBandLiftHighSFI: 10m benefits from high SFI; 80m does not.
func TestCBandLiftHighSFI(t *testing.T) {
	hi := cBandLift("10M", 200)
	lo := cBandLift("10M", 70)
	if !(hi > lo) {
		t.Errorf("10m: high SFI should beat low: hi=%v lo=%v", hi, lo)
	}
	if math.Abs(hi-lo) < 0.1 {
		t.Errorf("10m lift too small: hi=%v lo=%v", hi, lo)
	}
	eightyHi := cBandLift("80M", 200)
	eightyLo := cBandLift("80M", 70)
	if eightyHi-eightyLo > 0.1 {
		t.Errorf("80m should be nearly insensitive to SFI: hi=%v lo=%v", eightyHi, eightyLo)
	}
}

// TestScoreCellEndToEnd exercises the full pipeline with a representative
// input: JA on 20m during a normal day, FT8 dominant, calm SW.
func TestScoreCellEndToEnd(t *testing.T) {
	e := newEngine()
	liveRates := map[Mode]int{
		ModeFT8:  30,
		ModeCW:   5,
		ModeSSB:  2,
	}
	liveSNR := map[Mode]SNRStats{
		ModeFT8: {P50: -10, P10: -16, Dist: 9500},
		ModeCW:  {P50: -5, P10: -10, Dist: 9400},
		ModeSSB: {P50: 8, P10: 2, Dist: 9600},
	}
	baselines := []Baseline{
		{Band: "20M", Region: region.JA, Mode: ModeFT8, RateMean: 15, RateStdDev: 5, SampleCount: 60},
		{Band: "20M", Region: region.JA, Mode: ModeCW, RateMean: 3, RateStdDev: 1.5, SampleCount: 60},
		{Band: "20M", Region: region.JA, Mode: ModeSSB, RateMean: 1, RateStdDev: 1, SampleCount: 60},
	}
	solar := SolarContext{KP: 2, SFI: 110, XrayFluxC: 1e-5, AuroraGW: 30}

	cell := e.ScoreCell("20M", region.JA, liveRates, baselines, liveSNR, solar)

	if cell.Band != "20M" || cell.Region != region.JA {
		t.Fatalf("identity wrong: %+v", cell)
	}
	if cell.Score <= 0 {
		t.Errorf("expected positive z-score for surge, got %v", cell.Score)
	}
	if cell.Probability < 0.5 || cell.Probability > 1 {
		t.Errorf("probability out of range: %v", cell.Probability)
	}
	if cell.Confidence <= 0 {
		t.Errorf("confidence should be > 0 with SNR data, got %v", cell.Confidence)
	}
	if len(cell.ModeBreakdown) != len(AllModes()) {
		t.Errorf("expected every mode in breakdown, got %v", cell.ModeBreakdown)
	}
	// FT8 should be the strongest contributor.
	if cell.ModeBreakdown[0].Mode != ModeFT8 {
		t.Errorf("expected FT8 strongest, got %v", cell.ModeBreakdown[0].Mode)
	}
	// Per-mode breakdown must be sorted descending by score.
	for i := 1; i < len(cell.ModeBreakdown); i++ {
		if cell.ModeBreakdown[i-1].Score < cell.ModeBreakdown[i].Score {
			t.Errorf("mode breakdown not sorted: %+v", cell.ModeBreakdown)
		}
	}
}

// TestScoreCellEmptyBaseline: brand-new band/region with no history. We
// should not crash; the score should be 0 (no evidence either way) and
// the probability should default to 0.5 via the confidence prior.
func TestScoreCellEmptyBaseline(t *testing.T) {
	e := newEngine()
	liveRates := map[Mode]int{ModeFT8: 5}
	cell := e.ScoreCell("6M", region.NA, liveRates, nil, nil, SolarContext{})
	if cell.Score != 0 {
		t.Errorf("empty baseline score should be 0, got %v", cell.Score)
	}
	if cell.Probability <= 0 || cell.Probability > 0.6 {
		t.Errorf("empty baseline probability should hover near 0.5, got %v", cell.Probability)
	}
}

// TestPickBestBaselineAggregate: when no ModeAny baseline exists, the
// aggregator should average every per-mode baseline.
func TestPickBestBaselineAggregate(t *testing.T) {
	baselines := []Baseline{
		{Band: "20M", Region: region.JA, Mode: ModeFT8, RateMean: 10, RateStdDev: 4, SampleCount: 50},
		{Band: "20M", Region: region.JA, Mode: ModeCW, RateMean: 14, RateStdDev: 6, SampleCount: 70},
	}
	got := pickBestBaseline("20M", region.JA, baselines)
	if got.Mode != ModeAny {
		t.Errorf("aggregate mode should be ModeAny, got %v", got.Mode)
	}
	if got.RateMean != 12 {
		t.Errorf("expected mean 12, got %v", got.RateMean)
	}
	if got.RateStdDev != 5 {
		t.Errorf("expected stddev 5, got %v", got.RateStdDev)
	}
	if got.SampleCount != 70 {
		t.Errorf("expected max sample count 70, got %v", got.SampleCount)
	}
}

// TestScoreCellProbabilityBounded: even with extreme inputs, the probability
// must stay in [0, 1].
func TestScoreCellProbabilityBounded(t *testing.T) {
	e := newEngine()
	liveRates := map[Mode]int{ModeFT8: 1000}
	baselines := []Baseline{
		{Band: "10M", Region: region.JA, Mode: ModeFT8, RateMean: 1, RateStdDev: 0.1, SampleCount: 100},
	}
	liveSNR := map[Mode]SNRStats{ModeFT8: {P50: 20, P10: 16}}
	cell := e.ScoreCell("10M", region.JA, liveRates, baselines, liveSNR, SolarContext{KP: 1, SFI: 200})
	if cell.Probability < 0 || cell.Probability > 1 {
		t.Errorf("probability out of bounds: %v", cell.Probability)
	}
}
