package pathscope

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"horstreporter/internal/region"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the read-only PostgreSQL accessor for Pathscope. It pools the
// horstreporter pgx/v5 connection and exposes the four queries that the
// scoring engine needs to fill a glance view.
//
// The store never mutates horstreporter-owned tables. It only reads from
// dx_raw_spots, proplab_cell_buckets, and proplab_sw_series. It WILL write
// to pathscope_* tables (profile, score log, alert log, etc.) through a
// separate set of methods, but those are not implemented in v0.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wires a Store around an existing pgxpool. The pool is owned by
// the caller; Close on the store does not close the pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// LiveRates counts the spots per (band, region, mode) between [since, until].
// The region is computed from the receiver_locator (i.e. the side the
// operator "transmits to"). This is the same 5-minute sliding window the
// glance view renders.
//
// lanes filters by source_type. Pathscope passes nil to count all sources.
func (s *Store) LiveRates(
	ctx context.Context,
	since, until time.Time,
	lanes []string,
) (map[Mode]map[region.Region]map[string]int, error) {
	out := make(map[Mode]map[region.Region]map[string]int)

	q := `
SELECT band, receiver_locator, mode, COUNT(*)
FROM dx_raw_spots
WHERE spot_time >= $1 AND spot_time < $2
  AND receiver_locator <> ''
`
	args := []interface{}{since.Unix(), until.Unix()}
	if len(lanes) > 0 {
		q += ` AND source_type = ANY($3)`
		args = append(args, lanes)
	}
	q += ` GROUP BY band, receiver_locator, mode`

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("pathscope: live rates: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var band, loc, mode string
		var count int
		if err := rows.Scan(&band, &loc, &mode, &count); err != nil {
			return nil, fmt.Errorf("pathscope: live rate scan: %w", err)
		}
		reg := region.FromLocator(loc)
		if !reg.IsValid() {
			continue
		}
		m := Mode(mode)
		bandCount := ensureNested(out, m, reg)
		bandCount[band] += count
	}
	return out, rows.Err()
}

// LiveSNR returns the median and p10 signal_report_db per (band, region,
// mode) for the same window. PSKREPORTER lanes only — RBN is excluded
// because its 0..40 dB CW-filter scale is incompatible with FT8's -24..+5
// 2.5 kHz scale. The comparison would falsely suggest SSB is workable at
// +20 dB "SNR" when it is really a 500 Hz CW reading.
func (s *Store) LiveSNR(
	ctx context.Context,
	since, until time.Time,
) (map[Mode]map[region.Region]SNRStats, error) {
	out := make(map[Mode]map[region.Region]SNRStats)

	// We compute the median via percentile_cont, which is well-defined
	// and fast on the spot_count range we expect. The p10 is approximated
	// by percentile_cont(0.10).
	q := `
SELECT band, receiver_locator, mode,
       percentile_cont(0.5) WITHIN GROUP (ORDER BY signal_report_db)::float8 AS p50,
       percentile_cont(0.1) WITHIN GROUP (ORDER BY signal_report_db)::float8 AS p10
FROM dx_raw_spots
WHERE spot_time >= $1 AND spot_time < $2
  AND receiver_locator <> ''
  AND source_type IN ('mqtt', 'dxcluster')
GROUP BY band, receiver_locator, mode
HAVING COUNT(*) >= 5
`
	rows, err := s.pool.Query(ctx, q, since.Unix(), until.Unix())
	if err != nil {
		return nil, fmt.Errorf("pathscope: live snr: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var band, loc, mode string
		var p50, p10 float64
		if err := rows.Scan(&band, &loc, &mode, &p50, &p10); err != nil {
			return nil, fmt.Errorf("pathscope: live snr scan: %w", err)
		}
		reg := region.FromLocator(loc)
		if !reg.IsValid() {
			continue
		}
		m := Mode(mode)
		bandMap := ensureNestedSNR(out, m)
		// Use the *highest* p50 across cells in the (band, region) — the
		// faintest path is the limiting factor, but the strongest is what
		// the operator could plausibly exploit. This is the same heuristic
		// the existing DXPulse cell uses.
		if existing, ok := bandMap[reg]; !ok || p50 > existing.P50 {
			bandMap[reg] = SNRStats{P50: p50, P10: p10}
		}
	}
	return out, rows.Err()
}

// Baseline reads the historical link rate per (band, region, mode) from
// proplab_cell_buckets, restricted to the same slot-of-day as the current
// time's slot. The lookback is in days; the slot grid is 30 minutes (48
// slots/day) and matches proplabbacktest's convention.
//
// bands is the list of bands to include (e.g. "10M", "15M"). Pass nil to
// include all known bands.
func (s *Store) Baseline(
	ctx context.Context,
	lookbackDays int,
	now time.Time,
	bands []string,
) ([]Baseline, error) {
	since := now.Add(-time.Duration(lookbackDays) * 24 * time.Hour)
	slotOfDay := slot30(now) // 0..47

	q := `
SELECT band, region, lane,
       AVG(link_count)::float8 AS mean,
       COALESCE(STDDEV_SAMP(link_count), 0)::float8 AS stddev,
       COUNT(*)::int AS samples
FROM proplab_cell_buckets
WHERE bucket_start >= $1
  AND band = ANY($2)
  AND ((bucket_start / 1800) % 48) = $3
GROUP BY band, region, lane
`
	rows, err := s.pool.Query(ctx, q, since.Unix(), pgxArray(bands), slotOfDay)
	if err != nil {
		return nil, fmt.Errorf("pathscope: baseline: %w", err)
	}
	defer rows.Close()

	var out []Baseline
	for rows.Next() {
		var band, reg, lane string
		var mean, stddev float64
		var samples int
		if err := rows.Scan(&band, &reg, &lane, &mean, &stddev, &samples); err != nil {
			return nil, fmt.Errorf("pathscope: baseline scan: %w", err)
		}
		m := modeFromLane(lane)
		if !regIsValid(reg) {
			// Empty region means "global" — keep but tag as Global.
			reg = "GLOBAL"
		}
		if math.IsNaN(stddev) || math.IsInf(stddev, 0) {
			stddev = 0
		}
		out = append(out, Baseline{
			Band:        band,
			Region:      region.Region(reg),
			Mode:        m,
			RateMean:    mean,
			RateStdDev:  stddev,
			SampleCount: samples,
		})
	}
	// Deterministic test ordering.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Band != out[j].Band {
			return out[i].Band < out[j].Band
		}
		if out[i].Region != out[j].Region {
			return out[i].Region < out[j].Region
		}
		return out[i].Mode < out[j].Mode
	})
	return out, rows.Err()
}

// SolarContext returns the most recent proplab_sw_series row, fanning out
// across the four series (kp, sfi, xray, aurora). If a series has no
// recent row, the corresponding field is left at its zero value.
//
// The schema column is `obs_time` (Unix seconds), not `observed_at`. Both
// the public API and the SQL are mapped here: API names are 'sfi', 'xray_flux',
// 'aurora_gw'; the table stores them as 'F10.7', 'xray', 'ovation'.
//
// Aliases: the table stores 'F10.7' (solar flux index 10.7cm) and 'ovation'
// (auroral oval power). We map both to the API name. If either column is
// re-renamed in the future, only this switch needs to change.
func (s *Store) SolarContext(ctx context.Context) (SolarContext, error) {
	q := `
SELECT series, value::float8, to_timestamp(obs_time)
FROM proplab_sw_series
WHERE obs_time >= EXTRACT(EPOCH FROM NOW() - INTERVAL '6 hours')
  AND series IN ('kp', 'F10.7', 'xray', 'ovation')
`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return SolarContext{}, fmt.Errorf("pathscope: solar: %w", err)
	}
	defer rows.Close()

	var sc SolarContext
	var latest time.Time
	for rows.Next() {
		var series string
		var value float64
		var observed time.Time
		if err := rows.Scan(&series, &value, &observed); err != nil {
			return SolarContext{}, fmt.Errorf("pathscope: solar scan: %w", err)
		}
		// Map the wire-name to the API field. Only the most recent sample
		// wins — older rows for the same series are ignored.
		switch series {
		case "kp":
			if observed.Before(sc.ObservedAt) {
				continue
			}
			sc.KP = value
		case "F10.7":
			if observed.Before(sc.ObservedAt) {
				continue
			}
			sc.SFI = value
		case "xray":
			if observed.Before(sc.ObservedAt) {
				continue
			}
			sc.XrayFluxC = value
		case "ovation":
			if observed.Before(sc.ObservedAt) {
				continue
			}
			sc.AuroraGW = value
		default:
			continue
		}
		if observed.After(latest) {
			latest = observed
		}
	}
	sc.ObservedAt = latest
	return sc, rows.Err()
}

// --- helpers ---

// slot30 returns the 30-minute slot-of-day for a UTC time, 0..47.
// Mirrors the `(((bucket_start/900)%96)/2)` computation in
// dx_proplab_store.go but expanded for readability.
func slot30(t time.Time) int {
	min := t.UTC().Hour()*60 + t.UTC().Minute()
	return (min / 30) % 48
}

// modeFromLane maps the proplab `lane` identifier to a Pathscope Mode.
// The lane names are documented in internal/proplab/lane.go.
func modeFromLane(lane string) Mode {
	switch lane {
	case "ft8":
		return ModeFT8
	case "rbn":
		return ModeCW // RBN is overwhelmingly CW; treat as CW for pathscope
	case "dcx":
		return ModeAny // DX cluster is mixed-mode
	}
	return ModeAny
}

// regIsValid mirrors the region package's IsValid; we keep this in store_pg
// so the SQL path doesn't have to import region just for the empty check.
func regIsValid(s string) bool {
	if s == "" || s == "??" || s == "GLOBAL" {
		return false
	}
	return region.Region(s).IsValid()
}

// pgxArray returns a pgx-friendly slice wrapper. pgx/v5 accepts []string
// directly, but we keep the helper here so the call sites read consistently.
func pgxArray(bands []string) []string {
	if bands == nil {
		return []string{}
	}
	return bands
}

// ensureNested creates the (mode, region) -> bandCount map entry if missing.
func ensureNested(
	out map[Mode]map[region.Region]map[string]int,
	m Mode,
	r region.Region,
) map[string]int {
	if out[m] == nil {
		out[m] = make(map[region.Region]map[string]int)
	}
	if out[m][r] == nil {
		out[m][r] = make(map[string]int)
	}
	return out[m][r]
}

// ensureNestedSNR creates the (mode, region) -> SNRStats map entry.
func ensureNestedSNR(
	out map[Mode]map[region.Region]SNRStats,
	m Mode,
) map[region.Region]SNRStats {
	if out[m] == nil {
		out[m] = make(map[region.Region]SNRStats)
	}
	return out[m]
}
