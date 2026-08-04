// Package pathscope provides the RAH (Rate-Anomaly Hybrid) scoring engine
// and the PostgreSQL-backed cell store that feed the Pathscope glance view.
// The package is consumed by the cmd/pathscope binary; it has no main, no
// open-ended goroutines, and no embedded static assets.
package pathscope

import (
	"math"
	"sort"
	"strings"
	"time"

	"horstreporter/internal/region"
)

// Mode is the wire identifier of a single mode lane. Values are stable.
type Mode string

// String returns the wire identifier (e.g. "FT8", "CW").
func (m Mode) String() string { return string(m) }

const (
	ModeFT8  Mode = "FT8"
	ModeFT4  Mode = "FT4"
	ModeCW   Mode = "CW"
	ModeRTTY Mode = "RTTY"
	ModeSSB  Mode = "SSB"
	ModeAny  Mode = "ANY" // aggregate / unknown
)

// AllModes returns the supported modes in display order. The order matches
// the existing DXPulse lane order so the glance mini-bars read consistently.
func AllModes() []Mode {
	return []Mode{ModeFT8, ModeFT4, ModeCW, ModeRTTY, ModeSSB}
}

// ModeSNRThresholdDB is the minimum median SNR at which a path is considered
// plausibly workable for a given mode. SSB is the noisiest because there is
// no narrow-band DSP gain; FT8 is the most permissive.
//
// These values are intentionally conservative — they describe the *floor*
// below which a mode is almost certainly not workable, not the SNR at which
// a contact is guaranteed. They are the constants C_mode in the RAH spec.
var ModeSNRThresholdDB = map[Mode]float64{
	ModeFT8:  -24,
	ModeFT4:  -18,
	ModeCW:   -15,
	ModeRTTY: -10,
	ModeSSB:  +6,
}

// ModeConfidenceScale is the divisor used in the sigmoid confidence function.
// A scale of 6 dB means the confidence reads ~0.5 at SNR_threshold and ~0.88
// 6 dB above the threshold. It is intentionally loose so the badge reads
// "workable" rather than "optimal".
const ModeConfidenceScale = 6.0

// RateObservation is the raw 5-minute count of spots arriving at a (band,
// region, lane) bucket. The same shape is used by historical baselines.
type RateObservation struct {
	Band   string
	Region region.Region
	Mode   Mode
	Count  int
}

// SNRStats is the per-bucket signal-to-noise summary we read from
// proplab_cell_buckets (snr_median, snr_p10) plus the distance context.
type SNRStats struct {
	P50  float64 // median, dB
	P10  float64 // 10th percentile, dB
	Dist float64 // median great-circle distance, km
}

// Baseline is the historical reference for a (band, region, mode) bucket.
// All three fields are required so the score can fall back to a z-score
// against the broader RateBaseline when one mode is missing.
type Baseline struct {
	Band   string
	Region region.Region
	Mode   Mode

	RateMean    float64
	RateStdDev  float64 // 0 means "no variance"; treat as below
	SNRMedian   float64 // historical snr_median
	SNRStdDev   float64
	SampleCount int
}

// SolarContext is the propagated space-weather snapshot at score time. It is
// intentionally compact — the cmd/pathscope binary polls the SW series and
// passes the latest sample in.
type SolarContext struct {
	ObservedAt  time.Time
	KP          float64 // 0..9
	SFI         float64 // 70..300
	XrayFluxC   float64 // W/m^2
	AuroraGW    float64 // nT
}

// CellScore is the scored output for a single (band, region) cell. It is the
// shape the glance view renders.
type CellScore struct {
	Band       string
	Region     region.Region
	ObservedAt time.Time

	// Score is the unweighted z-score (rate anomaly) clamped to [-3, +6].
	// Color mapping in the UI uses Score for the cell background.
	Score float64

	// Probability is the smoothed 0..1 chance of a successful contact in
	// the next 30 minutes, used for the headline number.
	Probability float64

	// Confidence is the mode-aware confidence in the headline number. It is
	// a 0..1 derived from the median SNR relative to the mode threshold.
	Confidence float64

	// ModeBreakdown carries a per-mode z-score and confidence so the cell
	// mini-bars can show which mode is the strongest right now.
	ModeBreakdown []ModeCellContribution

	// BaselineSamples is the number of historical (band, region, mode) cells
	// that contributed to the score. 0 means the band/region is brand-new.
	BaselineSamples int

	// SolarModifier is the multiplier applied to RateMean due to current
	// space weather. Reported so the UI can tag the cell.
	SolarModifier float64
}

// ModeCellContribution is one row of the per-mode breakdown.
type ModeCellContribution struct {
	Mode       Mode
	Score      float64
	Confidence float64
	Workable   bool // true if SNR_P50 >= threshold for this mode
}

// ScoringEngine combines rate and SNR into a single RAH cell score. The
// implementation is intentionally pure — no clock, no I/O, no goroutines — so
// it can be unit-tested with synthetic inputs.
type ScoringEngine struct {
	// Now is the wall-clock reference for the cell. Tests inject a fixed
	// time so the seasonality fallback is deterministic.
	Now func() time.Time

	// ZClampRange is the inclusive range the raw z-score is clamped to.
	// Anything outside is rendered as the extreme color.
	ZClampRange [2]float64

	// ProbabilityCurve maps a (score, confidence) pair to a 0..1 probability.
	// It is called often enough to keep it inline; the default is a logistic
	// over Score with Confidence as a multiplicative prior.
	ProbabilityCurve func(score, confidence float64) float64
}

// NewScoringEngine returns an engine with the RAH v0 defaults.
func NewScoringEngine() *ScoringEngine {
	return &ScoringEngine{
		Now:          time.Now,
		ZClampRange:  [2]float64{-3, 6},
		ProbabilityCurve: func(score, confidence float64) float64 {
			// Logistic in steady-state space. Score = 0 -> 0.5,
			// score = +3 -> ~0.88, score = -3 -> ~0.12 before confidence.
			raw := 1 / (1 + math.Exp(-score))
			// Confidence pulls toward 0.5 when low (don't overstate a
			// guess we don't believe in) and toward 1 when high.
			return 0.5 + (raw-0.5)*confidence
		},
	}
}

// ScoreCell computes a CellScore for one (band, region) cell given the
// current 5-minute rates per mode, the historical baselines, the live SNR
// stats, and the solar context. Baseline lookup is by (band, region, mode)
// first, then falls back to (band, region) aggregate when a mode-specific
// baseline is missing — this prevents brand-new lanes from going blank.
func (e *ScoringEngine) ScoreCell(
	band string,
	reg region.Region,
	liveRates map[Mode]int,
	baselines []Baseline,
	liveSNR map[Mode]SNRStats,
	solar SolarContext,
) CellScore {
	now := e.Now()
	cell := CellScore{
		Band:        band,
		Region:      reg,
		ObservedAt:  now,
		ModeBreakdown: make([]ModeCellContribution, 0, len(AllModes())),
	}

	// 1. Live rate aggregated across all modes.
	var liveAll int
	for _, c := range liveRates {
		liveAll += c
	}

	// 2. Pick the best baseline: across all modes for this band/region.
	bestBase := pickBestBaseline(band, reg, baselines)

	// 3. Z-score of the live rate against the aggregate baseline.
	z := zScore(float64(liveAll), bestBase.RateMean, bestBase.RateStdDev)
	cell.Score = clamp(z, e.ZClampRange[0], e.ZClampRange[1])
	cell.BaselineSamples = bestBase.SampleCount

	// 4. Solar modifier — single multiplier used both for the score and
	// for the headline tag. Bands that benefit from high SFI get a lift;
	// high Kp penalises polar paths.
	solarMod := solarModifier(band, solar)
	cell.SolarModifier = solarMod

	// 5. Per-mode breakdown. Each mode gets its own z-score (scaled from
	// the live rate) and its own confidence from the live SNR.
	var totalConfidence float64
	var confCount int
	for _, m := range AllModes() {
		base := findBaseline(band, reg, m, baselines)
		if !base.IsValid() {
			base = bestBase // fall back to aggregate
		}
		modeCount := liveRates[m]
		mz := zScore(float64(modeCount), base.RateMean, base.RateStdDev)
		mz = clamp(mz, e.ZClampRange[0], e.ZClampRange[1])

		// Confidence is the sigmoid of how far the live median SNR is
		// above the mode threshold. -ve (no SNR data) maps to 0.5 — we
		// don't know, so we don't claim either way.
		stats := liveSNR[m]
		conf := modeConfidence(stats, m)
		totalConfidence += conf
		confCount++

		cell.ModeBreakdown = append(cell.ModeBreakdown, ModeCellContribution{
			Mode:       m,
			Score:      mz,
			Confidence: conf,
			Workable:   isWorkable(stats, m),
		})
	}

	// 6. Headline confidence: average across modes, biased low if any mode
	// is below the threshold. We don't want to display "0.7" when three
	// of the five modes are unworkable.
	if confCount > 0 {
		cell.Confidence = math.Round(totalConfidence/float64(confCount)*100) / 100
	}

	// 7. Probability: logistic on the bounded score, scaled by headline
	// confidence. This is the "P of contact in the next 30 min" number.
	cell.Probability = clamp(
		e.ProbabilityCurve(cell.Score, cell.Confidence)*solarMod,
		0, 1,
	)

	// 8. Sort the mode breakdown so the strongest mode is first — this is
	// what the mini-bar renders left-to-right.
	sort.SliceStable(cell.ModeBreakdown, func(i, j int) bool {
		return cell.ModeBreakdown[i].Score > cell.ModeBreakdown[j].Score
	})

	return cell
}

// Baseline.IsValid reports whether the baseline has any samples to score
// against. Baselines with zero samples are not usable for a z-score; the
// caller must fall back to an aggregate or skip the cell.
func (b Baseline) IsValid() bool { return b.SampleCount > 0 }

// findBaseline returns the baseline for (band, region, mode) or an invalid
// baseline if none is present.
func findBaseline(band string, reg region.Region, mode Mode, all []Baseline) Baseline {
	for _, b := range all {
		if b.Band == band && b.Region == reg && b.Mode == mode {
			return b
		}
	}
	return Baseline{}
}

// pickBestBaseline prefers the (band, region, ANY) baseline; if missing,
// it averages every per-mode baseline available for (band, region).
func pickBestBaseline(band string, reg region.Region, all []Baseline) Baseline {
	if b := findBaseline(band, reg, ModeAny, all); b.IsValid() {
		return b
	}
	var sumMean, sumStdDev float64
	var n int
	maxSamples := 0
	for _, b := range all {
		if b.Band == band && b.Region == reg && b.Mode != ModeAny {
			sumMean += b.RateMean
			sumStdDev += b.RateStdDev
			n++
			if b.SampleCount > maxSamples {
				maxSamples = b.SampleCount
			}
		}
	}
	if n == 0 {
		return Baseline{}
	}
	return Baseline{
		Band:        band,
		Region:      reg,
		Mode:        ModeAny,
		RateMean:    sumMean / float64(n),
		RateStdDev:  sumStdDev / float64(n),
		SampleCount: maxSamples,
	}
}

// zScore is the standard z; a zero stddev is treated as "no variance" and
// returns 0 — we don't want to divide by zero or to manufacture z=+Infinity
// when the network is genuinely quiet.
func zScore(live, mean, stddev float64) float64 {
	if stddev <= 0 || live <= 0 && mean <= 0 {
		return 0
	}
	return (live - mean) / stddev
}

// modeConfidence is the sigmoid over (P50_SNR - ModeThreshold). When stats
// are unavailable (P50 == 0 and P10 == 0) we return 0.5 — explicit "don't
// know" rather than zero, which would tank the headline probability.
func modeConfidence(s SNRStats, m Mode) float64 {
	thresh, ok := ModeSNRThresholdDB[m]
	if !ok {
		return 0.5
	}
	if s.P50 == 0 && s.P10 == 0 {
		return 0.5
	}
	// Translate P50 distance above threshold into a logit.
	x := (s.P50 - thresh) / ModeConfidenceScale
	return 1 / (1 + math.Exp(-x))
}

// isWorkable is the binary "is this mode above the threshold" check. SSB
// is special — without a narrow filter the noise floor makes it impractical
// below the threshold.
func isWorkable(s SNRStats, m Mode) bool {
	thresh, ok := ModeSNRThresholdDB[m]
	if !ok {
		return false
	}
	return s.P50 >= thresh
}

// solarModifier returns a 0.5..1.5 multiplier driven by the current SW
// conditions. It is intentionally gentle — the score is dominated by the
// z-score; SW is a secondary signal.
//
// C_band: 10/12/15m get a lift when SFI is high; 80/160m get a small lift
// when Kp is low. Other bands read 1.0.
//
// storm_penalty: any Kp > 5 pulls the multiplier down at every band.
func solarModifier(band string, s SolarContext) float64 {
	m := 1.0
	m *= cBandLift(band, s.SFI)
	m *= stormPenalty(s.KP)
	return m
}

// cBandLift maps SFI to a 0.85..1.25 multiplier for the HF bands where F2
// propagation matters most. Bands absent from the table return 1.0. The
// table is case-insensitive — the DB stores "20m" but the engine matches
// "20M" because they're the same band.
func cBandLift(band string, sfi float64) float64 {
	switch strings.ToUpper(band) {
	case "10M", "12M", "15M":
		// 70 -> 0.85, 200 -> 1.25 linear.
		return 0.85 + clamp((sfi-70)/130, 0, 1)*0.40
	case "17M", "20M":
		// 70 -> 0.95, 200 -> 1.10.
		return 0.95 + clamp((sfi-70)/130, 0, 1)*0.15
	case "30M", "40M", "60M", "80M", "160M":
		// Low bands benefit negligibly from SFI; small lift only.
		return 1.0 + clamp((sfi-70)/130, 0, 1)*0.05
	}
	return 1.0
}

// stormPenalty returns 1.0 when Kp <= 5 and decays linearly to 0.5 at Kp=9.
// The floor of 0.5 prevents the headline probability from going to zero
// during a major storm — we still want to say "rare but possible".
func stormPenalty(kp float64) float64 {
	if kp <= 5 {
		return 1.0
	}
	return clamp(1.0-(kp-5)/8, 0.5, 1.0)
}

// clamp returns x constrained to [lo, hi].
func clamp(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}
