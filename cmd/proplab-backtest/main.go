package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"horstreporter/internal/proplab"
)

const defaultDSN = "postgres://dxuser@localhost:5432/dxdata?sslmode=disable"

func main() {
	var (
		dbDSN        = flag.String("db", envOrDefault("DATABASE_URL", defaultDSN), "Postgres DSN (or DATABASE_URL env)")
		startArg     = flag.String("start", "", "Start time (RFC3339 or Unix timestamp)")
		endArg       = flag.String("end", "", "End time (RFC3339 or Unix timestamp; default now)")
		durationArg  = flag.String("duration", "24h", "If -start omitted, replay the last N (e.g. 24h)")
		target       = flag.String("target", "", "Target 4- or 6-character Maidenhead locator")
		surroundings = flag.Bool("surroundings", false, "Include the 8 surrounding grid squares in the target scope")
		stepMin      = flag.Int("step-minutes", 15, "Evaluation interval in minutes")
		lookbackDays = flag.Int("fusion-lookback-days", 45, "Fusion baseline lookback window in days")
		emit         = flag.String("emit", "json", "Output format: json (line-delimited), csv, or none")
		metricsOnly  = flag.Bool("metrics-only", false, "Print only the final scoring metrics summary")

		destBackfillDays = flag.Int("dest-backfill-days", 0, "Backfill proplab_dest_buckets from dx_raw_spots for the last N days (2h batches), then exit")
		destEval         = flag.Bool("dest-eval", false, "Replay the reachability composer over destination buckets; print calibration metrics. -target scopes the replay (empty = global).")
		destHoldout      = flag.Float64("dest-holdout", 0, "Reporter callsign holdout fraction for reach calibration (e.g. 0.25); holdout callsigns are excluded from the live window but not from ground truth")
	)
	flag.Parse()

	if *target != "" && !proplab.IsLocator(*target) {
		log.Fatalf("invalid target locator %q", *target)
	}

	now := time.Now().Unix()
	endTS := parseTimeArg(*endArg, now)
	startTS := parseTimeArg(*startArg, 0)
	if startTS == 0 {
		if d, err := time.ParseDuration(*durationArg); err == nil {
			startTS = endTS - int64(d.Seconds())
		} else {
			log.Fatalf("invalid -duration %q: %v", *durationArg, err)
		}
	}
	if startTS >= endTS {
		log.Fatalf("start time must be before end time")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *dbDSN)
	if err != nil {
		log.Fatalf("db connect: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("db ping: %v", err)
	}

	if *destBackfillDays > 0 {
		if err := runDestBackfill(ctx, pool, *destBackfillDays, endTS); err != nil {
			log.Fatalf("dest backfill: %v", err)
		}
		fmt.Println("dest backfill complete")
		return
	}

	var destRep *destReplay
	var reachK *reachKeeper
	if *destEval {
		scopeList, scopeMap := destScopesForTarget(*target, *surroundings)
		destRep = newDestReplay(*destHoldout)
		reachK = newReachKeeper(scopeList, scopeMap)
	}
	ladderVal := newLadderValKeeper()

	ladder := proplab.NewLadderEngine()
	fusion := proplab.NewFusionEngine()

	ladderParams := proplab.DefaultLadderParams()
	fusionParams := proplab.DefaultFusionParams()
	fusionParams.LookbackDays = *lookbackDays

	stepSec := int64(*stepMin * 60)
	nextEval := proplab.AlignBucketStart(startTS) + stepSec
	if nextEval > endTS {
		nextEval = endTS
	}

	var history []proplab.Spot
	historyWindow := int64(30 * 60)

	// Scoring accumulator for the whole replay window.
	scoreKeeper := newScoreKeeper()

	rows, err := pool.Query(ctx, `
		SELECT spot_time, band, sender_locator, receiver_locator,
		       sender_callsign, receiver_callsign, signal_report_db, source_type
		FROM dx_raw_spots
		WHERE spot_time >= $1 AND spot_time <= $2
		ORDER BY spot_time ASC
	`, startTS, endTS)
	if err != nil {
		log.Fatalf("query raw spots: %v", err)
	}
	defer rows.Close()

	if !*metricsOnly {
		printHeader(*emit)
	}

	for rows.Next() {
		var raw struct {
			SpotTime     int64
			Band         string
			SenderLoc    string
			ReceiverLoc  string
			SenderCall   string
			ReceiverCall string
			SNR          int
			SourceType   string
		}
		if err := rows.Scan(&raw.SpotTime, &raw.Band, &raw.SenderLoc, &raw.ReceiverLoc,
			&raw.SenderCall, &raw.ReceiverCall, &raw.SNR, &raw.SourceType); err != nil {
			log.Fatalf("scan raw spot: %v", err)
		}

		spot := proplab.Spot{
			T:      raw.SpotTime,
			B:      raw.Band,
			SC:     raw.SenderCall,
			SL:     raw.SenderLoc,
			RC:     raw.ReceiverCall,
			RL:     raw.ReceiverLoc,
			RP:     raw.SNR,
			Source: strings.ToLower(strings.TrimSpace(raw.SourceType)),
		}

		ladder.Observe(spot)
		if destRep != nil {
			destRep.observe(spot, raw.SpotTime)
		}
		history = append(history, spot)
		cutoff := raw.SpotTime - historyWindow
		if i := sort.Search(len(history), func(i int) bool { return history[i].T >= cutoff }); i > 0 {
			history = history[i:]
		}

		for raw.SpotTime >= nextEval {
			nowEval := nextEval
			lv, fv, err := evalAt(ctx, pool, ladder, fusion, *target, *surroundings, fusionParams, ladderParams, nowEval)
			if err != nil {
				log.Fatalf("eval at %d: %v", nowEval, err)
			}

			scoreKeeper.observeStep(nowEval, lv, fv)
			ladderVal.observeStep(nowEval, lv)
			if reachK != nil {
				if err := reachK.observeStep(ctx, pool, destRep, lv, *target, *surroundings, nowEval, endTS); err != nil {
					log.Fatalf("reach eval at %d: %v", nowEval, err)
				}
			}
			if !*metricsOnly {
				emitStep(*emit, nowEval, lv, fv)
			}

			nextEval += stepSec
			if nextEval > endTS {
				nextEval = endTS
			}
			if nextEval <= nowEval {
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("raw spot rows: %v", err)
	}

	// Final evaluation at endTS if no spot landed exactly there.
	if nextEval == endTS {
		nowEval := endTS
		lv, fv, err := evalAt(ctx, pool, ladder, fusion, *target, *surroundings, fusionParams, ladderParams, nowEval)
		if err != nil {
			log.Fatalf("final eval: %v", err)
		}
		scoreKeeper.observeStep(nowEval, lv, fv)
		ladderVal.observeStep(nowEval, lv)
		if reachK != nil {
			if err := reachK.observeStep(ctx, pool, destRep, lv, *target, *surroundings, nowEval, endTS); err != nil {
				log.Fatalf("final reach eval: %v", err)
			}
		}
		if !*metricsOnly {
			emitStep(*emit, nowEval, lv, fv)
		}
	}

	metrics := scoreKeeper.metrics()
	if *metricsOnly {
		printMetricsJSON(metrics)
		return
	}
	// Always append a metrics summary to stderr so it doesn't contaminate CSV/JSON output on stdout.
	printMetricsText(metrics)

	// Ladder validation + reach calibration go to stderr as JSON blocks.
	fmt.Fprintln(os.Stderr, "\n=== ladder validation (model B evidence) ===")
	printJSONTo(os.Stderr, ladderVal.metrics())
	if reachK != nil {
		fmt.Fprintln(os.Stderr, "\n=== reach calibration ===")
		printJSONTo(os.Stderr, reachK.metrics())
	}
}

func printJSONTo(w *os.File, v any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// evalAt runs one Ladder + Fusion evaluation at a given timestamp.
func evalAt(ctx context.Context, pool *pgxpool.Pool, ladder *proplab.LadderEngine, fusion *proplab.FusionEngine,
	target string, surroundings bool, fusionParams proplab.FusionParams, ladderParams proplab.LadderParams, nowEval int64) (proplab.LadderVerdict, proplab.FusionVerdict, error) {
	cutoff := proplab.AlignBucketStart(nowEval - proplab.BucketSeconds)
	_ = ladder.CloseBuckets(cutoff)

	lv := ladder.Verdict(target, surroundings, nil, nil, nil, ladderParams, nowEval)
	live := ladder.RegionCounts(proplab.AlignBucketStart(nowEval - 20*60))
	var baseline []proplab.BaselineDayRow
	if len(live) > 0 {
		bands, regions := liveBandsRegions(live)
		slot := proplab.UTCSlotOfDay(nowEval)
		baseline, _ = loadCellBaseline(ctx, pool, bands, regions, slot, fusionParams.LookbackDays, nowEval)
	}
	sw := loadSW(ctx, pool, nowEval)
	events := loadEvents(ctx, pool, nowEval)
	fv := fusion.Verdict(live, baseline, events, sw, nil, fusionParams, nowEval)
	return lv, fv, nil
}

func parseTimeArg(s string, fallback int64) int64 {
	if s == "" {
		return fallback
	}
	if i, err := parseUnix(s); err == nil {
		return i
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Unix()
	}
	log.Fatalf("cannot parse time %q (expected RFC3339 or Unix timestamp)", s)
	return 0
}

func parseUnix(s string) (int64, error) {
	var i int64
	_, err := fmt.Sscanf(s, "%d", &i)
	return i, err
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func liveBandsRegions(live []proplab.FusionBandCount) (bands, regions []string) {
	bandSet := make(map[string]struct{})
	regionSet := make(map[string]struct{})
	for _, c := range live {
		bandSet[c.Band] = struct{}{}
		regionSet[c.Region] = struct{}{}
	}
	bands = make([]string, 0, len(bandSet))
	regions = make([]string, 0, len(regionSet))
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

func loadCellBaseline(ctx context.Context, pool *pgxpool.Pool, bands, regions []string, slot, lookbackDays int, now int64) ([]proplab.BaselineDayRow, error) {
	if len(bands) == 0 {
		return nil, nil
	}
	if len(regions) == 0 {
		regions = nil
	}
	if lookbackDays <= 0 {
		lookbackDays = 45
	}
	start := now - int64(lookbackDays*24*60*60)
	rows, err := pool.Query(ctx, `
		SELECT band, region,
		       (((bucket_start / 900) % 96) / 2) AS slot_of_day,
		       (bucket_start / 86400) AS day_index,
		       SUM(link_count)::bigint AS link_count,
		       SUM(spot_count)::bigint AS spot_count,
		       MAX(dist_max_km)::int AS dist_max_km
		FROM proplab_cell_buckets
		WHERE bucket_start >= $1
		  AND band = ANY($2)
		  AND ($3::text[] IS NULL OR region = ANY($3))
		  AND (((bucket_start / 900) % 96) / 2) = $4
		GROUP BY band, region, (((bucket_start / 900) % 96) / 2), (bucket_start / 86400)
	`, start, bands, regions, slot)
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

func loadSW(ctx context.Context, pool *pgxpool.Pool, now int64) proplab.FusionSWSnapshot {
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT ON (series) series, obs_time, value
		FROM proplab_sw_series
		WHERE obs_time <= $1
		ORDER BY series, obs_time DESC
	`, now)
	if err != nil {
		return proplab.FusionSWSnapshot{}
	}
	defer rows.Close()
	sw := proplab.FusionSWSnapshot{}
	for rows.Next() {
		var series string
		var obs int64
		var val float64
		if err := rows.Scan(&series, &obs, &val); err != nil {
			continue
		}
		sw.Available = true
		switch series {
		case "kp":
			sw.Kp = val
		case "F10.7":
			sw.SFI = val
		case "xray":
			sw.XrayClass = xrayClassFromFlux(val)
		case "ovation":
			sw.AuroraGW = val
		}
		if obs > sw.FetchedAt {
			sw.FetchedAt = obs
		}
	}

	var drapText []byte
	var drapObs int64
	if err := pool.QueryRow(ctx, `
		SELECT obs_time, haf_grid
		FROM proplab_drap_snapshots
		WHERE obs_time <= $1
		ORDER BY obs_time DESC
		LIMIT 1
	`, now).Scan(&drapObs, &drapText); err == nil {
		if grid, err := proplab.ParseDRAPText(drapText); err == nil {
			sw.DrapHAF = grid.RegionHAFMap()
			sw.HasDrap = true
			sw.DrapAgeMin = int((now - grid.ValidAt) / 60)
			if grid.ValidAt > sw.FetchedAt {
				sw.FetchedAt = grid.ValidAt
			}
		}
	}
	return sw
}

func xrayClassFromFlux(flux float64) string {
	// NOAA class thresholds: A < 1e-7, B 1e-7..1e-6, C 1e-6..1e-5, M 1e-5..1e-4, X >= 1e-4 W/m^2.
	if flux <= 0 {
		return ""
	}
	switch {
	case flux >= 1e-4:
		return fmt.Sprintf("X%.1f", flux/1e-4)
	case flux >= 1e-5:
		return fmt.Sprintf("M%.1f", flux/1e-5)
	case flux >= 1e-6:
		return fmt.Sprintf("C%.1f", flux/1e-6)
	case flux >= 1e-7:
		return fmt.Sprintf("B%.1f", flux/1e-7)
	default:
		return fmt.Sprintf("A%.1f", flux/1e-8)
	}
}

func loadEvents(ctx context.Context, pool *pgxpool.Pool, now int64) []proplab.FusionEvent {
	rows, err := pool.Query(ctx, `
		SELECT source, title, band_mask, locator4, start_utc, end_utc
		FROM proplab_events
		WHERE start_utc <= $1 AND end_utc >= $1
	`, now)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []proplab.FusionEvent
	for rows.Next() {
		var r proplab.FusionEvent
		var start, end int64
		if err := rows.Scan(&r.Source, &r.Title, &r.BandMask, &r.Locator4, &start, &end); err != nil {
			continue
		}
		r.Region = proplab.RegionFromLocator(r.Locator4)
		r.EndsInMin = int((end - now) / 60)
		if r.EndsInMin < 0 {
			r.EndsInMin = 0
		}
		out = append(out, r)
	}
	return out
}

// scoreKeeper tracks per-band ground-truth openings and how early each engine
// declared them open, plus false-positive counts.
type scoreKeeper struct {
	bandState map[string]*bandScoreState
}

type bandScoreState struct {
	firstGroundOpen      int64 // first evaluation ts where band was actually open
	groundOpenAt         map[int64]bool
	ladderFirstOpen      int64
	fusionFirstOpen      int64
	ladderFalsePositives int
	fusionFalsePositives int
	ladderOpenSteps      int
	fusionOpenSteps      int
	steps                int
}

// We treat a band as "actually open" when it shows ≥2 distinct links per minute
// in the Ladder live window. This is a simple reference threshold derived from
// the same observations the engines use, so it is not an independent label.
const groundTruthLinksPerMinute = 2.0

func newScoreKeeper() *scoreKeeper {
	return &scoreKeeper{bandState: make(map[string]*bandScoreState)}
}

func (k *scoreKeeper) state(band string) *bandScoreState {
	s := k.bandState[band]
	if s == nil {
		s = &bandScoreState{groundOpenAt: make(map[int64]bool)}
		k.bandState[band] = s
	}
	return s
}

func (k *scoreKeeper) observeStep(ts int64, lv proplab.LadderVerdict, fv proplab.FusionVerdict) {
	ladderOpen := bandStateMap(lv.Bands, func(b proplab.LadderBandVerdict) bool {
		return b.State == "open" || b.State == "rising"
	})
	fusionOpen := bandStateMap(fv.Bands, func(b proplab.FusionBandVerdict) bool {
		return b.State == proplab.StateOpenConfirmed || b.State == proplab.StateOpenUnconfirmed
	})
	groundOpen := bandStateMap(lv.Bands, func(b proplab.LadderBandVerdict) bool {
		return b.LinksPerMinute >= groundTruthLinksPerMinute
	})

	for band, isOpen := range groundOpen {
		s := k.state(band)
		s.steps++
		if isOpen {
			s.groundOpenAt[ts] = true
			if s.firstGroundOpen == 0 {
				s.firstGroundOpen = ts
			}
		}
	}
	// Also count steps for bands seen by engines even if ground truth absent this step.
	for band := range ladderOpen {
		s := k.state(band)
		if s.steps == 0 {
			s.steps++
		}
		if ladderOpen[band] {
			s.ladderOpenSteps++
			if s.ladderFirstOpen == 0 || (s.firstGroundOpen > 0 && ts < s.ladderFirstOpen) {
				s.ladderFirstOpen = ts
			}
			if s.firstGroundOpen == 0 || ts < s.firstGroundOpen {
				s.ladderFalsePositives++
			}
		}
	}
	for band := range fusionOpen {
		s := k.state(band)
		if s.steps == 0 {
			s.steps++
		}
		if fusionOpen[band] {
			s.fusionOpenSteps++
			if s.fusionFirstOpen == 0 || (s.firstGroundOpen > 0 && ts < s.fusionFirstOpen) {
				s.fusionFirstOpen = ts
			}
			if s.firstGroundOpen == 0 || ts < s.firstGroundOpen {
				s.fusionFalsePositives++
			}
		}
	}
}

func bandStateMap[T any](bands []T, openFn func(T) bool) map[string]bool {
	out := make(map[string]bool)
	for _, b := range bands {
		var band string
		switch v := any(b).(type) {
		case proplab.LadderBandVerdict:
			band = v.Band
		case proplab.FusionBandVerdict:
			band = v.Band
		}
		if band == "" {
			continue
		}
		out[band] = openFn(b)
	}
	return out
}

// metrics aggregates the tracked state into recall / precision / lead-time numbers.
func (k *scoreKeeper) metrics() map[string]any {
	bandMetrics := make(map[string]any)
	var ladderRecallSum, fusionRecallSum float64
	var ladderRecallBands, fusionRecallBands int
	var ladderFPRSum, fusionFPRSum float64
	var ladderPrecBands, fusionPrecBands int
	var ladderLeadSum, fusionLeadSum int64
	var ladderLeadCount, fusionLeadCount int

	for band, s := range k.bandState {
		if s.steps == 0 {
			continue
		}
		openSteps := len(s.groundOpenAt)
		bm := map[string]any{
			"steps":                s.steps,
			"ground_open_steps":    openSteps,
			"ladder_open_steps":    s.ladderOpenSteps,
			"fusion_open_steps":    s.fusionOpenSteps,
			"ladder_false_positives": s.ladderFalsePositives,
			"fusion_false_positives": s.fusionFalsePositives,
		}
		if openSteps > 0 {
			ladderRecall := float64(s.ladderOpenSteps) / float64(openSteps)
			fusionRecall := float64(s.fusionOpenSteps) / float64(openSteps)
			bm["ladder_recall"] = ladderRecall
			bm["fusion_recall"] = fusionRecall
			ladderRecallSum += ladderRecall
			fusionRecallSum += fusionRecall
			ladderRecallBands++
			fusionRecallBands++
		}
		if s.ladderOpenSteps+s.ladderFalsePositives > 0 {
			prec := float64(s.ladderOpenSteps) / float64(s.ladderOpenSteps+s.ladderFalsePositives)
			bm["ladder_precision"] = prec
			ladderFPRSum += prec
			ladderPrecBands++
		}
		if s.fusionOpenSteps+s.fusionFalsePositives > 0 {
			prec := float64(s.fusionOpenSteps) / float64(s.fusionOpenSteps+s.fusionFalsePositives)
			bm["fusion_precision"] = prec
			fusionFPRSum += prec
			fusionPrecBands++
		}
		if s.firstGroundOpen > 0 && s.ladderFirstOpen > 0 {
			lead := (s.firstGroundOpen - s.ladderFirstOpen) / 60
			bm["ladder_lead_min"] = lead
			ladderLeadSum += lead
			ladderLeadCount++
		}
		if s.firstGroundOpen > 0 && s.fusionFirstOpen > 0 {
			lead := (s.firstGroundOpen - s.fusionFirstOpen) / 60
			bm["fusion_lead_min"] = lead
			fusionLeadSum += lead
			fusionLeadCount++
		}
		bandMetrics[band] = bm
	}

	out := map[string]any{
		"bands":     bandMetrics,
		"evaluated": len(k.bandState),
	}
	if ladderRecallBands > 0 {
		out["mean_ladder_recall"] = ladderRecallSum / float64(ladderRecallBands)
	}
	if fusionRecallBands > 0 {
		out["mean_fusion_recall"] = fusionRecallSum / float64(fusionRecallBands)
	}
	if ladderPrecBands > 0 {
		out["mean_ladder_precision"] = ladderFPRSum / float64(ladderPrecBands)
	}
	if fusionPrecBands > 0 {
		out["mean_fusion_precision"] = fusionFPRSum / float64(fusionPrecBands)
	}
	if ladderLeadCount > 0 {
		out["median_ladder_lead_min"] = ladderLeadSum / int64(ladderLeadCount)
	}
	if fusionLeadCount > 0 {
		out["median_fusion_lead_min"] = fusionLeadSum / int64(fusionLeadCount)
	}
	return out
}

func printHeader(format string) {
	if format == "csv" {
		fmt.Println("ts,slot,band,ladder_state,ladder_confidence,ladder_muf,fusion_state,fusion_confidence,fusion_closure")
	}
}

func emitStep(format string, ts int64, lv proplab.LadderVerdict, fv proplab.FusionVerdict) {
	slot := proplab.UTCSlotOfDay(ts)
	if format == "csv" {
		fusionByBand := make(map[string]proplab.FusionBandVerdict)
		for _, b := range fv.Bands {
			fusionByBand[b.Band] = b
		}
		for _, b := range lv.Bands {
			fb := fusionByBand[b.Band]
			fmt.Printf("%d,%d,%s,%s,%.2f,%.1f,%s,%.2f,%s\n",
				ts, slot, b.Band, b.State, b.Confidence, lv.EmpiricalMUF,
				fb.State, fb.Confidence, fb.ClosureType)
		}
		return
	}
	if format == "none" {
		return
	}

	ladderMap := make(map[string]map[string]any)
	for _, b := range lv.Bands {
		ladderMap[b.Band] = map[string]any{
			"state":      b.State,
			"confidence": b.Confidence,
			"reason":     b.Reason,
		}
	}
	fusionMap := make(map[string]map[string]any)
	for _, b := range fv.Bands {
		fusionMap[b.Band] = map[string]any{
			"state":         b.State,
			"confidence":    b.Confidence,
			"label":         b.Label,
			"reason":        b.Reason,
			"closureType":   b.ClosureType,
			"activityRatio": b.ActivityRatio,
		}
	}
	line, _ := json.Marshal(map[string]any{
		"ts":           ts,
		"slot":         slot,
		"dataThin":     lv.DataThin,
		"empiricalMUF": lv.EmpiricalMUF,
		"ladder":       ladderMap,
		"fusion":       fusionMap,
	})
	fmt.Println(string(line))
}

func printMetricsJSON(metrics map[string]any) {
	b, _ := json.Marshal(metrics)
	fmt.Println(string(b))
}

func printMetricsText(metrics map[string]any) {
	fmt.Fprintln(os.Stderr, "# proplab-backtest metrics")
	for k, v := range metrics {
		if k == "bands" {
			continue
		}
		fmt.Fprintf(os.Stderr, "%s: %v\n", k, v)
	}
	bands, _ := metrics["bands"].(map[string]any)
	if len(bands) > 0 {
		fmt.Fprintln(os.Stderr, "bands:")
		keys := make([]string, 0, len(bands))
		for b := range bands {
			keys = append(keys, b)
		}
		sort.Strings(keys)
		for _, b := range keys {
			fmt.Fprintf(os.Stderr, "  %s: %v\n", b, bands[b])
		}
	}
}
