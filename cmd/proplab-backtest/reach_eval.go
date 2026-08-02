package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"horstreporter/internal/proplab"
)

// reach_eval.go extends the backtest harness with (a) a destination-bucket
// backfill from dx_raw_spots, (b) a replay of the reachability composer over
// historical ticks with calibration metrics (monotonicity, threshold scores,
// reporter holdout), and (c) the ladder validation block.
//
// Semantic note: the destination live window is rebuilt from the raw spot
// stream (same symmetric scope2 x dx_region counting as the service's
// dx_proplab_dest.go — keep the mirrored helpers in sync). The callsign
// holdout filter exists ONLY here: it removes a deterministic fraction of
// callsigns from the live aggregation while ground truth still uses the full
// persisted buckets, separating the index from its own witnesses.

// --- destination live-window replay aggregator ------------------------------

type destReplayKey struct {
	bucketStart int64
	band        string
	scope2      string
	region      string
}

type destReplayAgg struct {
	spots     int
	pairs     map[string]struct{}
	reporters map[string]struct{}
	dist      []int
}

type destReplay struct {
	acc        map[destReplayKey]*destReplayAgg
	holdoutPpm int64 // -1 disables; spots where either end falls in the holdout are skipped
}

func newDestReplay(holdoutFraction float64) *destReplay {
	d := &destReplay{acc: make(map[destReplayKey]*destReplayAgg), holdoutPpm: -1}
	if holdoutFraction > 0 {
		if holdoutFraction > 1 {
			holdoutFraction = 1
		}
		d.holdoutPpm = int64(holdoutFraction * 1_000_000)
	}
	return d
}

// holdoutCall deterministically assigns a callsign to the holdout set.
func (d *destReplay) holdoutCall(call string) bool {
	if d.holdoutPpm < 0 {
		return false
	}
	// FNV-1a, cheap and stable across runs.
	var h uint64 = 14695981039346656037
	for i := 0; i < len(call); i++ {
		h ^= uint64(call[i])
		h *= 1099511628211
	}
	return int64(h%1_000_000) < d.holdoutPpm
}

func (d *destReplay) observe(spot proplab.Spot, now int64) {
	sl := strings.ToUpper(strings.TrimSpace(spot.SL))
	rl := strings.ToUpper(strings.TrimSpace(spot.RL))
	if len(sl) < 4 || len(rl) < 4 {
		return
	}
	sc := strings.ToUpper(strings.TrimSpace(spot.SC))
	rc := strings.ToUpper(strings.TrimSpace(spot.RC))
	if d.holdoutCall(sc) || d.holdoutCall(rc) {
		return
	}
	regionS := proplab.RegionFromLocator(sl[:4])
	regionR := proplab.RegionFromLocator(rl[:4])
	if regionS == "" && regionR == "" {
		return
	}
	latS, lngS := proplab.LocatorToLatLng(sl[:4])
	latR, lngR := proplab.LocatorToLatLng(rl[:4])
	dist := int(proplab.HaversineKm(latS, lngS, latR, lngR))
	pair := sc + "|" + rc
	if rc < sc {
		pair = rc + "|" + sc
	}
	bucketStart := proplab.AlignBucketStart(spot.T)

	d.add(destReplayKey{bucketStart, spot.B, sl[:2], regionR}, sc, pair, dist)
	d.add(destReplayKey{bucketStart, spot.B, rl[:2], regionS}, rc, pair, dist)

	// Prune buckets older than the live window.
	cutoff := proplab.AlignBucketStart(now - 45*60)
	for k := range d.acc {
		if k.bucketStart < cutoff {
			delete(d.acc, k)
		}
	}
}

func (d *destReplay) add(key destReplayKey, nearCall, pair string, dist int) {
	if key.region == "" {
		return
	}
	agg := d.acc[key]
	if agg == nil {
		agg = &destReplayAgg{pairs: make(map[string]struct{}), reporters: make(map[string]struct{})}
		d.acc[key] = agg
	}
	agg.spots++
	agg.pairs[pair] = struct{}{}
	agg.reporters[nearCall] = struct{}{}
	agg.dist = append(agg.dist, dist)
}

// liveWindow returns (band, region)-aggregated rows for the trailing
// windowMinutes, scope-filtered (nil scope = global).
func (d *destReplay) liveWindow(scopes map[string]bool, windowMinutes int, now int64) []proplab.DestRow {
	start := proplab.AlignBucketStart(now) - int64(windowMinutes*60)
	merged := make(map[[2]string]*destReplayAgg)
	for k, agg := range d.acc {
		if k.bucketStart < start || k.bucketStart >= proplab.AlignBucketStart(now)+proplab.BucketSeconds {
			continue
		}
		if scopes != nil && !scopes[k.scope2] {
			continue
		}
		gk := [2]string{k.band, k.region}
		m := merged[gk]
		if m == nil {
			m = &destReplayAgg{pairs: make(map[string]struct{}), reporters: make(map[string]struct{})}
			merged[gk] = m
		}
		m.spots += agg.spots
		for p := range agg.pairs {
			m.pairs[p] = struct{}{}
		}
		for r := range agg.reporters {
			m.reporters[r] = struct{}{}
		}
		m.dist = append(m.dist, agg.dist...)
	}
	out := make([]proplab.DestRow, 0, len(merged))
	for gk, m := range merged {
		out = append(out, proplab.DestRow{
			Band:          gk[0],
			Region:        gk[1],
			SpotCount:     m.spots,
			LinkCount:     len(m.pairs),
			ReporterCount: len(m.reporters),
			DistMedianKm:  proplab.IntMedian(m.dist),
		})
	}
	return out
}

// --- SQL helpers (mirrors of dx_proplab_store.go dest queries; keep in sync) --

func loadDestBaselineDB(ctx context.Context, pool *pgxpool.Pool, scopes, bands, regions []string, slot, lookbackDays int, now int64) ([]proplab.BaselineDayRow, error) {
	if len(bands) == 0 {
		return nil, nil
	}
	start := now - int64(lookbackDays*24*60*60)
	rows, err := pool.Query(ctx, `
		SELECT band, dx_region,
		       (((bucket_start / 900) % 96) / 2) AS slot_of_day,
		       (bucket_start / 86400) AS day_index,
		       SUM(link_count)::bigint AS link_count,
		       SUM(spot_count)::bigint AS spot_count,
		       MAX(dist_max_km)::int AS dist_max_km
		FROM proplab_dest_buckets
		WHERE bucket_start >= $1
		  AND band = ANY($2)
		  AND ($3::text[] IS NULL OR dx_region = ANY($3))
		  AND ($4::text[] IS NULL OR scope2 = ANY($4))
		  AND (((bucket_start / 900) % 96) / 2) = $5
		GROUP BY band, dx_region, (((bucket_start / 900) % 96) / 2), (bucket_start / 86400)
	`, start, bands, regions, scopes, slot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []proplab.BaselineDayRow
	for rows.Next() {
		var r proplab.BaselineDayRow
		if err := rows.Scan(&r.Band, &r.Region, &r.Slot, &r.DayIndex, &r.LinkCount, &r.SpotCount, &r.DistMaxKm); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func loadDestSlotProfileDB(ctx context.Context, pool *pgxpool.Pool, scopes, bands []string, lookbackDays int, now int64) ([]proplab.BaselineDayRow, error) {
	start := now - int64(lookbackDays*24*60*60)
	rows, err := pool.Query(ctx, `
		SELECT band, dx_region,
		       (((bucket_start / 900) % 96) / 2) AS slot_of_day,
		       (bucket_start / 86400) AS day_index,
		       SUM(link_count)::bigint AS link_count,
		       SUM(spot_count)::bigint AS spot_count,
		       MAX(dist_max_km)::int AS dist_max_km
		FROM proplab_dest_buckets
		WHERE bucket_start >= $1
		  AND band = ANY($2)
		  AND ($3::text[] IS NULL OR scope2 = ANY($3))
		GROUP BY band, dx_region, (((bucket_start / 900) % 96) / 2), (bucket_start / 86400)
	`, start, bands, scopes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []proplab.BaselineDayRow
	for rows.Next() {
		var r proplab.BaselineDayRow
		if err := rows.Scan(&r.Band, &r.Region, &r.Slot, &r.DayIndex, &r.LinkCount, &r.SpotCount, &r.DistMaxKm); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func loadDestPersistenceDB(ctx context.Context, pool *pgxpool.Pool, scopes, bands, regions []string, windowMinutes, minLinks int, now int64) (map[[2]string][2]int, error) {
	out := make(map[[2]string][2]int)
	if len(bands) == 0 {
		return out, nil
	}
	end := (now / proplab.BucketSeconds) * proplab.BucketSeconds
	start := end - int64(windowMinutes*60)
	rows, err := pool.Query(ctx, `
		SELECT band, dx_region,
		       COUNT(*) FILTER (WHERE link_count >= $5)::int AS active,
		       COUNT(*)::int AS total
		FROM proplab_dest_buckets
		WHERE bucket_start >= $1 AND bucket_start < $2
		  AND band = ANY($3)
		  AND ($4::text[] IS NULL OR dx_region = ANY($4))
		  AND ($6::text[] IS NULL OR scope2 = ANY($6))
		GROUP BY band, dx_region
	`, start, end, bands, regions, minLinks, scopes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var band, region string
		var active, total int
		if err := rows.Scan(&band, &region, &active, &total); err != nil {
			return nil, err
		}
		out[[2]string{band, region}] = [2]int{active, total}
	}
	return out, rows.Err()
}

// loadDestForwardTruth returns per-pair links-per-minute over [now, now+60min)
// — the ground truth proxy: did the pair produce forward activity?
func loadDestForwardTruth(ctx context.Context, pool *pgxpool.Pool, scopes []string, now int64) (map[[2]string]float64, error) {
	out := make(map[[2]string]float64)
	rows, err := pool.Query(ctx, `
		SELECT band, dx_region, SUM(link_count)::int
		FROM proplab_dest_buckets
		WHERE bucket_start >= $1 AND bucket_start < $2
		  AND ($3::text[] IS NULL OR scope2 = ANY($3))
		GROUP BY band, dx_region
	`, (now/60)*60, (now/60)*60+3600, scopes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var band, region string
		var links int
		if err := rows.Scan(&band, &region, &links); err != nil {
			return nil, err
		}
		out[[2]string{band, region}] = float64(links) / 60.0
	}
	return out, rows.Err()
}

// --- dest bucket backfill from dx_raw_spots ---------------------------------

// runDestBackfill backfills proplab_dest_buckets from dx_raw_spots in 2h
// batches. Regions cannot be classified in SQL without duplicating the Go
// bounding boxes, so raw rows are streamed and aggregated in Go; only the
// grouped rows hit the DB. Idempotent-safe via additive ON CONFLICT — but
// running it twice over the same window double-counts (no distinct-set
// merge possible), so it's meant for one-shot bootstrap.
func runDestBackfill(ctx context.Context, pool *pgxpool.Pool, days int, endTS int64) error {
	start := endTS - int64(days)*86400
	batch := int64(2 * 3600)
	for t := start; t < endTS; t += batch {
		end := t + batch
		if end > endTS {
			end = endTS
		}
		n, err := destBackfillBatch(ctx, pool, t, end)
		if err != nil {
			return err
		}
		fmt.Printf("backfill [%s .. %s): %d grouped rows\n",
			timeUTC(t), timeUTC(end), n)
	}
	return nil
}

func destBackfillBatch(ctx context.Context, pool *pgxpool.Pool, start, end int64) (int, error) {
	rows, err := pool.Query(ctx, `
		SELECT spot_time, band, sender_locator, receiver_locator,
		       sender_callsign, receiver_callsign, signal_report_db
		FROM dx_raw_spots
		WHERE spot_time >= $1 AND spot_time < $2
		ORDER BY spot_time ASC
	`, start, end)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type aggKey struct {
		bucketStart int64
		band        string
		scope2      string
		region      string
	}
	acc := make(map[aggKey]*destReplayAgg)
	add := func(bucketStart int64, band, scope2, region, nearCall, pair string, snr, dist int) {
		if region == "" || band == "" {
			return
		}
		k := aggKey{bucketStart, band, scope2, region}
		a := acc[k]
		if a == nil {
			a = &destReplayAgg{pairs: make(map[string]struct{}), reporters: make(map[string]struct{})}
			acc[k] = a
		}
		a.spots++
		a.pairs[pair] = struct{}{}
		a.reporters[nearCall] = struct{}{}
		a.dist = append(a.dist, dist)
	}

	for rows.Next() {
		var (
			st                int64
			band              string
			sl, rl, sc, rc    string
			snr               int
		)
		if err := rows.Scan(&st, &band, &sl, &rl, &sc, &rc, &snr); err != nil {
			return 0, err
		}
		sl = strings.ToUpper(strings.TrimSpace(sl))
		rl = strings.ToUpper(strings.TrimSpace(rl))
		band = proplab.NormalizeBand(band)
		if len(sl) < 4 || len(rl) < 4 || !proplab.BandInScope(band) {
			continue
		}
		regionS := proplab.RegionFromLocator(sl[:4])
		regionR := proplab.RegionFromLocator(rl[:4])
		if regionS == "" && regionR == "" {
			continue
		}
		latS, lngS := proplab.LocatorToLatLng(sl[:4])
		latR, lngR := proplab.LocatorToLatLng(rl[:4])
		dist := int(proplab.HaversineKm(latS, lngS, latR, lngR))
		sc = strings.ToUpper(strings.TrimSpace(sc))
		rc = strings.ToUpper(strings.TrimSpace(rc))
		pair := sc + "|" + rc
		if rc < sc {
			pair = rc + "|" + sc
		}
		bucketStart := proplab.AlignBucketStart(st)
		add(bucketStart, band, sl[:2], regionR, sc, pair, snr, dist)
		add(bucketStart, band, rl[:2], regionS, rc, pair, snr, dist)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(acc) == 0 {
		return 0, nil
	}

	n := 0
	batchCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for k, a := range acc {
		_, err := pool.Exec(batchCtx, `
			INSERT INTO proplab_dest_buckets
			(bucket_start, band, scope2, dx_region, spot_count, link_count, reporter_count,
			 snr_median, dist_median_km, dist_max_km)
			VALUES ($1,$2,$3,$4,$5,$6,$7,0,$8,$8)
			ON CONFLICT (bucket_start, band, scope2, dx_region)
			DO UPDATE SET
				spot_count     = proplab_dest_buckets.spot_count + EXCLUDED.spot_count,
				link_count     = proplab_dest_buckets.link_count + EXCLUDED.link_count,
				reporter_count = proplab_dest_buckets.reporter_count + EXCLUDED.reporter_count,
				dist_max_km    = GREATEST(proplab_dest_buckets.dist_max_km, EXCLUDED.dist_max_km)
		`, k.bucketStart, k.band, k.scope2, k.region, a.spots, len(a.pairs), len(a.reporters),
			proplab.IntMedian(a.dist))
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func timeUTC(ts int64) string {
	return time.Unix(ts, 0).UTC().Format(time.RFC3339)
}

// --- scope derivation (mirrors destScopesForQTH in dx_proplab_dest.go) -------

func destScopesForTarget(target string, surroundings bool) ([]string, map[string]bool) {
	target = strings.ToUpper(strings.TrimSpace(target))
	if len(target) < 2 || !proplab.IsLocator(target) {
		return nil, nil
	}
	m := map[string]bool{target[:2]: true}
	if surroundings {
		f0, f1 := int(target[0]-'A'), int(target[1]-'A')
		for dx := -1; dx <= 1; dx++ {
			for dy := -1; dy <= 1; dy++ {
				x, y := f0+dx, f1+dy
				if x >= 0 && x < 18 && y >= 0 && y < 18 {
					m[string([]byte{byte('A' + x), byte('A' + y)})] = true
				}
			}
		}
	}
	list := make([]string, 0, len(m))
	for s := range m {
		list = append(list, s)
	}
	sort.Strings(list)
	return list, m
}

// --- reach calibration keeper ------------------------------------------------

const destTruthLinksPerMinute = 2.0

type reachKeeper struct {
	scopes   []string
	scopeMap map[string]bool
	profile  []proplab.BaselineDayRow
	prev     *proplab.ReachVerdict

	// Calibration bins: 0..9 = index deciles, 10 = nil-index cells.
	binCount     [11]int
	binTruth     [11]int
	binFwdLpmSum [11]float64

	tp, fp, fn, tn int

	surgeByKind  map[string]int
	surgeTruth   map[string]int // surges whose pair turned true in the forward window
	scoredTicks  int
}

func newReachKeeper(scopes []string, scopeMap map[string]bool) *reachKeeper {
	return &reachKeeper{
		scopes:      scopes,
		scopeMap:    scopeMap,
		surgeByKind: map[string]int{},
		surgeTruth:  map[string]int{},
	}
}

func (k *reachKeeper) observeStep(ctx context.Context, pool *pgxpool.Pool, rep *destReplay,
	lv proplab.LadderVerdict, target string, surroundings bool, nowEval, endTS int64) error {

	live := rep.liveWindow(k.scopeMap, 30, nowEval)
	bands, regions := destLiveBandsRegionsBT(live)

	var baseline []proplab.BaselineDayRow
	var persistence map[[2]string][2]int
	if len(live) > 0 {
		slot := proplab.UTCSlotOfDay(nowEval)
		baseline, _ = loadDestBaselineDB(ctx, pool, k.scopes, bands, regions, slot, 45, nowEval)
		persistence, _ = loadDestPersistenceDB(ctx, pool, k.scopes, bands, regions, 120, 2, nowEval)
	}
	if persistence == nil {
		persistence = map[[2]string][2]int{}
	}
	if k.profile == nil {
		profile, _ := loadDestSlotProfileDB(ctx, pool, k.scopes, destBandList(), 45, nowEval)
		k.profile = profile
	}

	v := proplab.ComposeReach(proplab.ReachInputs{
		Now:          nowEval,
		QTH:          target,
		Surroundings: surroundings,
		Live:         live,
		LiveMinutes:  30,
		Baseline:     baseline,
		SlotProfile:  k.profile,
		Persistence:  persistence,
		SW:           loadSW(ctx, pool, nowEval),
		Events:       loadEvents(ctx, pool, nowEval),
		Ladder:       lv,
	})

	// Surge accounting (diff vs previous tick).
	for _, sg := range proplab.DetectSurges(k.prev, v, k.profile, nowEval) {
		k.surgeByKind[sg.Kind]++
		if nowEval+3600 <= endTS {
			truth, _ := loadDestForwardTruth(ctx, pool, k.scopes, nowEval)
			if truth[[2]string{sg.Band, sg.Region}] >= destTruthLinksPerMinute {
				k.surgeTruth[sg.Kind]++
			}
		}
	}
	cur := v
	k.prev = &cur

	// Calibration: bin the index, compare to forward truth. Skip the tail of the
	// replay where the forward window runs past the data.
	if nowEval+3600 > endTS {
		return nil
	}
	truth, err := loadDestForwardTruth(ctx, pool, k.scopes, nowEval)
	if err != nil {
		return err
	}
	k.scoredTicks++
	for _, c := range v.Cells {
		fwd := truth[[2]string{c.Band, c.Region}]
		isTrue := fwd >= destTruthLinksPerMinute
		bin := 10
		if c.Index != nil {
			bin = *c.Index / 10
			if bin > 9 {
				bin = 9
			}
		}
		k.binCount[bin]++
		k.binFwdLpmSum[bin] += fwd
		if isTrue {
			k.binTruth[bin]++
		}
		pred := c.Index != nil && *c.Index >= 50
		switch {
		case pred && isTrue:
			k.tp++
		case pred && !isTrue:
			k.fp++
		case !pred && isTrue:
			k.fn++
		default:
			k.tn++
		}
	}
	return nil
}

func (k *reachKeeper) metrics() map[string]any {
	bins := []map[string]any{}
	for i := 0; i <= 10; i++ {
		if k.binCount[i] == 0 {
			continue
		}
		label := fmt.Sprintf("%d-%d", i*10, i*10+9)
		if i == 10 {
			label = "nil-index"
		}
		bins = append(bins, map[string]any{
			"bin":            label,
			"cells":          k.binCount[i],
			"truth_rate":     roundF(float64(k.binTruth[i]) / float64(k.binCount[i])),
			"mean_fwd_lpm":   roundF(k.binFwdLpmSum[i] / float64(k.binCount[i])),
		})
	}
	precision, recall, accuracy := 0.0, 0.0, 0.0
	if k.tp+k.fp > 0 {
		precision = float64(k.tp) / float64(k.tp+k.fp)
	}
	if k.tp+k.fn > 0 {
		recall = float64(k.tp) / float64(k.tp+k.fn)
	}
	if total := k.tp + k.fp + k.fn + k.tn; total > 0 {
		accuracy = float64(k.tp+k.tn) / float64(total)
	}
	surges := map[string]any{}
	for kind, n := range k.surgeByKind {
		surges[kind] = map[string]any{"count": n, "forward_truth_hits": k.surgeTruth[kind]}
	}
	return map[string]any{
		"scored_ticks":          k.scoredTicks,
		"calibration_bins":      bins,
		"index_ge_50_precision": roundF(precision),
		"index_ge_50_recall":    roundF(recall),
		"index_ge_50_accuracy":  roundF(accuracy),
		"tp_fp_fn_tn":           []int{k.tp, k.fp, k.fn, k.tn},
		"surges":                surges,
	}
}

func roundF(f float64) float64 {
	return math.Round(f*1000) / 1000
}

func destBandList() []string {
	return []string{"160m", "80m", "60m", "40m", "30m", "20m", "17m", "15m", "12m", "10m", "6m", "4m", "2m"}
}

func destLiveBandsRegionsBT(live []proplab.DestRow) (bands, regions []string) {
	bandSet := make(map[string]struct{})
	regionSet := make(map[string]struct{})
	for _, r := range live {
		bandSet[r.Band] = struct{}{}
		regionSet[r.Region] = struct{}{}
	}
	for b := range bandSet {
		bands = append(bands, b)
	}
	for r := range regionSet {
		regions = append(regions, r)
	}
	sort.Strings(bands)
	sort.Strings(regions)
	return bands, regions
}

// --- ladder validation block -------------------------------------------------

// ladderValKeeper validates model B against the ground-truth proxy
// (links_per_minute >= 2.0), step by step: confusion counts per band, the lpm
// distribution at which the ladder declares open, CUSUM onset lead/false
// alarms, and open-runs contiguity violations.
type ladderValKeeper struct {
	bands        map[string]*ladderValBand
	contigViol   int
	openTicks    int
}

type ladderValBand struct {
	tp, fp, fn, tn int
	lpmAtOpen      []float64
	onsetPending   int64 // tick of pending onset awaiting truth, 0 = none
	onsetFalse     int
	leads          []int64
	truthOpen      bool
}

func newLadderValKeeper() *ladderValKeeper {
	return &ladderValKeeper{bands: make(map[string]*ladderValBand)}
}

func (k *ladderValKeeper) bandState(band string) *ladderValBand {
	s := k.bands[band]
	if s == nil {
		s = &ladderValBand{}
		k.bands[band] = s
	}
	return s
}

func (k *ladderValKeeper) observeStep(ts int64, lv proplab.LadderVerdict) {
	open := make(map[string]bool)
	anyOpen := false
	for _, b := range lv.Bands {
		open[b.Band] = b.State == "open" || b.State == "rising"
		s := k.bandState(b.Band)
		truth := b.LinksPerMinute >= groundTruthLinksPerMinute
		pred := open[b.Band]
		switch {
		case pred && truth:
			s.tp++
		case pred && !truth:
			s.fp++
		case !pred && truth:
			s.fn++
		default:
			s.tn++
		}
		if pred {
			s.lpmAtOpen = append(s.lpmAtOpen, b.LinksPerMinute)
			anyOpen = true
		}

		// Onset tracking: pending onset resolves when truth turns true (lead)
		// or expires after 4h (false alarm).
		if s.onsetPending > 0 {
			if truth {
				s.leads = append(s.leads, ts-s.onsetPending)
				s.onsetPending = 0
			} else if ts-s.onsetPending > 4*3600 {
				s.onsetFalse++
				s.onsetPending = 0
			}
		}
		if b.OnsetMinAgo >= 0 && s.onsetPending == 0 {
			s.onsetPending = ts - int64(b.OnsetMinAgo)*60
		}
		s.truthOpen = truth
	}

	// Contiguity: an F-ladder band open while a lower F band is closed is a
	// physics-incoherent ladder (contest pileups, Es on 10m aside — 12m/10m
	// are allowed to break the run from below on Es days; we still count and
	// let the reader judge the rate).
	if anyOpen {
		k.openTicks++
		for i, band := range proplab.LadderFLane {
			if !open[band] {
				continue
			}
			for j := 0; j < i; j++ {
				if _, seen := bandSeen(lv, proplab.LadderFLane[j]); seen && !open[proplab.LadderFLane[j]] {
					k.contigViol++
					break
				}
			}
		}
	}
}

func bandSeen(lv proplab.LadderVerdict, band string) (proplab.LadderBandVerdict, bool) {
	for _, b := range lv.Bands {
		if b.Band == band {
			return b, true
		}
	}
	return proplab.LadderBandVerdict{}, false
}

func (k *ladderValKeeper) metrics() map[string]any {
	perBand := map[string]any{}
	var allLpm []float64
	for band, s := range k.bands {
		allLpm = append(allLpm, s.lpmAtOpen...)
		bm := map[string]any{
			"tp_fp_fn_tn": []int{s.tp, s.fp, s.fn, s.tn},
		}
		if s.tp+s.fp > 0 {
			bm["precision"] = roundF(float64(s.tp) / float64(s.tp+s.fp))
		}
		if s.tp+s.fn > 0 {
			bm["recall"] = roundF(float64(s.tp) / float64(s.tp+s.fn))
		}
		if len(s.lpmAtOpen) > 0 {
			sort.Float64s(s.lpmAtOpen)
			bm["lpm_at_open_min"] = roundF(s.lpmAtOpen[0])
			bm["lpm_at_open_p25"] = roundF(proplab.PercentileFloat(s.lpmAtOpen, 0.25))
			bm["lpm_at_open_p50"] = roundF(proplab.PercentileFloat(s.lpmAtOpen, 0.50))
		}
		if len(s.leads) > 0 {
			sort.Slice(s.leads, func(i, j int) bool { return s.leads[i] < s.leads[j] })
			bm["onset_lead_min_median"] = s.leads[len(s.leads)/2] / 60
			bm["onset_confirmed"] = len(s.leads)
		}
		bm["onset_false_alarms"] = s.onsetFalse
		perBand[band] = bm
	}
	out := map[string]any{
		"bands":                    perBand,
		"open_ticks":               k.openTicks,
		"contiguity_violations":    k.contigViol,
	}
	if k.openTicks > 0 {
		out["contiguity_violation_rate"] = roundF(float64(k.contigViol) / float64(k.openTicks))
	}
	if len(allLpm) > 0 {
		sort.Float64s(allLpm)
		out["lpm_at_open_p50_all"] = roundF(proplab.PercentileFloat(allLpm, 0.5))
		out["lpm_at_open_p10_all"] = roundF(proplab.PercentileFloat(allLpm, 0.1))
	}
	return out
}
