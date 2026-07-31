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
		emit         = flag.String("emit", "json", "Output format: json (line-delimited) or csv")
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

	printHeader(*emit)

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
		history = append(history, spot)
		cutoff := raw.SpotTime - historyWindow
		if i := sort.Search(len(history), func(i int) bool { return history[i].T >= cutoff }); i > 0 {
			history = history[i:]
		}

		for raw.SpotTime >= nextEval {
			nowEval := nextEval
			cutoff := proplab.AlignBucketStart(nowEval - proplab.BucketSeconds)
			_ = ladder.CloseBuckets(cutoff)

			lv := ladder.Verdict(*target, *surroundings, history, nil, nil, ladderParams, nowEval)
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

			emitStep(*emit, nowEval, lv, fv)

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
		cutoff := proplab.AlignBucketStart(nowEval - proplab.BucketSeconds)
		_ = ladder.CloseBuckets(cutoff)
		lv := ladder.Verdict(*target, *surroundings, history, nil, nil, ladderParams, nowEval)
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
		emitStep(*emit, nowEval, lv, fv)
	}
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
		       ((bucket_start / 900) % 48) AS slot_of_day,
		       (bucket_start / 86400) AS day_index,
		       SUM(link_count)::bigint AS link_count,
		       SUM(spot_count)::bigint AS spot_count,
		       MAX(dist_max_km)::int AS dist_max_km
		FROM proplab_cell_buckets
		WHERE bucket_start >= $1
		  AND band = ANY($2)
		  AND ($3::text[] IS NULL OR region = ANY($3))
		  AND ((bucket_start / 900) % 48) = $4
		GROUP BY band, region, ((bucket_start / 900) % 48), (bucket_start / 86400)
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
