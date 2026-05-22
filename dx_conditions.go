package main

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultDxWindowMinutes = 20
	maxDxWindowMinutes     = 180
	maxDxBaselineEvents    = 250000
	dxSparklineBins        = 12
	dxSparklineBinSeconds  = 10 * 60
)

type baselineBucket struct {
	Band         string  `json:"band"`
	HourOfWeek   int     `json:"hour_of_week"`
	DistanceTier int     `json:"distance_tier"`
	SnrTier      int     `json:"snr_tier"`
	Count        int64   `json:"count"`
	SumDistance  float64 `json:"sum_distance"`
	SumSNR       float64 `json:"sum_snr"`
}

type dxObservedEvent struct {
	T  int64  `json:"t"`
	B  string `json:"b"`
	SC string `json:"sc"`
	RC string `json:"rc"`
	SL string `json:"sl"`
	RL string `json:"rl"`
	RP int    `json:"rp"`
}

type baselineSnapshot struct {
	Version       int                        `json:"version"`
	SavedAt       int64                      `json:"saved_at"`
	Buckets       map[string]*baselineBucket `json:"buckets"`
	TargetBuckets map[string]*baselineBucket `json:"target_buckets,omitempty"`
	Events        []dxObservedEvent          `json:"events,omitempty"`
}

type DxBaselineEngine struct {
	mu            sync.RWMutex
	path          string
	buckets       map[string]*baselineBucket
	targetBuckets map[string]*baselineBucket
	events        []dxObservedEvent
}

type dxBandCondition struct {
	Band               string    `json:"band"`
	Score              float64   `json:"score"`
	Confidence         float64   `json:"confidence"`
	Condition          string    `json:"condition"`
	CurrentLinks       int       `json:"current_links"`
	UniqueLinks        int       `json:"unique_links"`
	RepeatRatio        float64   `json:"repeat_ratio"`
	AvgDistanceKm      float64   `json:"avg_distance_km"`
	LongHaulRatio      float64   `json:"long_haul_ratio"`
	AvgSnr             float64   `json:"avg_snr"`
	PeakSnr            int       `json:"peak_snr"`
	BaselineActivity   float64   `json:"baseline_activity"`
	TargetBaselineUsed bool      `json:"target_baseline_used"`
	Trend              string    `json:"trend"`
	TrendDelta         float64   `json:"trend_delta"`
	Sparkline          []float64 `json:"sparkline"`
}

type dxConditionsResponse struct {
	Target          string            `json:"target"`
	Surroundings    bool              `json:"surroundings"`
	WindowMinutes   int               `json:"window_minutes"`
	CurrentHourOfWk int               `json:"current_hour_of_week"`
	GeneratedAt     int64             `json:"generated_at"`
	BaselineBuckets int               `json:"baseline_buckets"`
	OverallScore    float64           `json:"overall_score"`
	Confidence      float64           `json:"confidence"`
	Condition       string            `json:"condition"`
	BestBands       []string          `json:"best_bands"`
	Trend           string            `json:"trend"`
	TrendDelta      float64           `json:"trend_delta"`
	Bands           []dxBandCondition `json:"bands"`
}

type bandAccumulator struct {
	total         int
	uniqueKeys    map[string]struct{}
	totalDistance float64
	longHaulCount int
	sumSnr        int
	peakSnr       int
	tierCounts    [5][4]int
}

func newDxBaselineEngine(path string) *DxBaselineEngine {
	return &DxBaselineEngine{
		path:          strings.TrimSpace(path),
		buckets:       make(map[string]*baselineBucket),
		targetBuckets: make(map[string]*baselineBucket),
		events:        make([]dxObservedEvent, 0, 4096),
	}
}

func (e *DxBaselineEngine) NumBuckets() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.buckets) + len(e.targetBuckets)
}

func (e *DxBaselineEngine) Observe(m MQTTMessage) {
	band := normalizeBand(m.B)
	if band == "" {
		return
	}
	sl := strings.ToUpper(strings.TrimSpace(m.SL))
	rl := strings.ToUpper(strings.TrimSpace(m.RL))
	if sl == "" || rl == "" {
		return
	}
	ts := m.T
	if ts <= 0 {
		ts = time.Now().Unix()
	}
	hour := utcHourOfWeek(ts)
	distTier := distanceTierForLocators(sl, rl)
	snrTier := snrTierFromDb(m.RP)
	distanceKm := distanceKmForLocators(sl, rl)

	e.mu.Lock()
	defer e.mu.Unlock()

	e.observeBucket(e.buckets, baselineKey(band, hour, distTier, snrTier), band, hour, distTier, snrTier, distanceKm, float64(m.RP))

	senderTarget := normalizeTargetToken(m.SC)
	if senderTarget != "" {
		e.observeBucket(e.targetBuckets, baselineTargetKey(senderTarget, band, hour, distTier, snrTier), band, hour, distTier, snrTier, distanceKm, float64(m.RP))
	}
	receiverTarget := normalizeTargetToken(m.RC)
	if receiverTarget != "" {
		e.observeBucket(e.targetBuckets, baselineTargetKey(receiverTarget, band, hour, distTier, snrTier), band, hour, distTier, snrTier, distanceKm, float64(m.RP))
	}
	if isLocator(sl) {
		e.observeBucket(e.targetBuckets, baselineTargetKey(sl[:4], band, hour, distTier, snrTier), band, hour, distTier, snrTier, distanceKm, float64(m.RP))
	}
	if isLocator(rl) {
		e.observeBucket(e.targetBuckets, baselineTargetKey(rl[:4], band, hour, distTier, snrTier), band, hour, distTier, snrTier, distanceKm, float64(m.RP))
	}

	e.events = append(e.events, dxObservedEvent{T: ts, B: band, SC: strings.ToUpper(m.SC), RC: strings.ToUpper(m.RC), SL: sl, RL: rl, RP: m.RP})
	if len(e.events) > maxDxBaselineEvents {
		drop := len(e.events) - maxDxBaselineEvents
		if drop < 1 {
			drop = 1
		}
		if drop > len(e.events) {
			drop = len(e.events)
		}
		e.events = append([]dxObservedEvent(nil), e.events[drop:]...)
	}
}

func (e *DxBaselineEngine) observeBucket(store map[string]*baselineBucket, key, band string, hour, distTier, snrTier int, distanceKm, snr float64) {
	b := store[key]
	if b == nil {
		b = &baselineBucket{Band: band, HourOfWeek: hour, DistanceTier: distTier, SnrTier: snrTier}
		store[key] = b
	}
	b.Count++
	b.SumDistance += distanceKm
	b.SumSNR += snr
}

func (e *DxBaselineEngine) Load() error {
	if e == nil || e.path == "" {
		return nil
	}
	raw, err := os.ReadFile(e.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var snap baselineSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return err
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.buckets = make(map[string]*baselineBucket, len(snap.Buckets))
	for k, v := range snap.Buckets {
		if v == nil {
			continue
		}
		cp := *v
		e.buckets[k] = &cp
	}

	e.targetBuckets = make(map[string]*baselineBucket, len(snap.TargetBuckets))
	for k, v := range snap.TargetBuckets {
		if v == nil {
			continue
		}
		cp := *v
		e.targetBuckets[k] = &cp
	}

	if len(snap.Events) > maxDxBaselineEvents {
		snap.Events = snap.Events[len(snap.Events)-maxDxBaselineEvents:]
	}
	e.events = append([]dxObservedEvent(nil), snap.Events...)
	return nil
}

func (e *DxBaselineEngine) Save() error {
	if e == nil || e.path == "" {
		return nil
	}
	e.mu.RLock()
	snap := baselineSnapshot{
		Version:       2,
		SavedAt:       time.Now().Unix(),
		Buckets:       cloneBuckets(e.buckets),
		TargetBuckets: cloneBuckets(e.targetBuckets),
		Events:        append([]dxObservedEvent(nil), e.events...),
	}
	e.mu.RUnlock()

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(e.path, data, 0o644)
}

func cloneBuckets(src map[string]*baselineBucket) map[string]*baselineBucket {
	out := make(map[string]*baselineBucket, len(src))
	for k, v := range src {
		if v == nil {
			continue
		}
		cp := *v
		out[k] = &cp
	}
	return out
}

func (e *DxBaselineEngine) Evaluate(target string, surroundings bool, minutes int, history []MQTTMessage, now int64) dxConditionsResponse {
	target = normalizeTargetToken(target)
	if minutes <= 0 {
		minutes = defaultDxWindowMinutes
	}
	if minutes > maxDxWindowMinutes {
		minutes = maxDxWindowMinutes
	}

	resp := dxConditionsResponse{
		Target:          target,
		Surroundings:    surroundings,
		WindowMinutes:   minutes,
		CurrentHourOfWk: utcHourOfWeek(now),
		GeneratedAt:     now,
		BestBands:       []string{},
		Bands:           []dxBandCondition{},
		Condition:       "Poor",
		Trend:           "stable",
	}

	if target == "" {
		return resp
	}

	targets := []string{target}
	if surroundings && isLocator(target) {
		targets = getSurroundingSquares(target)
	}

	e.mu.RLock()
	globalBuckets := cloneBuckets(e.buckets)
	targetBuckets := cloneBuckets(e.targetBuckets)
	events := append([]dxObservedEvent(nil), e.events...)
	e.mu.RUnlock()

	resp.BaselineBuckets = len(globalBuckets) + len(targetBuckets)

	cutoff := now - int64(minutes*60)
	bandAcc := make(map[string]*bandAccumulator)
	for _, m := range history {
		if m.T < cutoff || m.T > now {
			continue
		}
		band, remoteLoc, qualityKey, snr, distanceKm, matched := extractMatchedBandEvent(m, targets)
		if !matched {
			continue
		}
		acc := bandAcc[band]
		if acc == nil {
			acc = &bandAccumulator{uniqueKeys: make(map[string]struct{}), peakSnr: -999}
			bandAcc[band] = acc
		}

		acc.total++
		acc.uniqueKeys[qualityKey] = struct{}{}
		acc.totalDistance += distanceKm
		if distanceKm >= 2000 {
			acc.longHaulCount++
		}
		acc.sumSnr += snr
		if snr > acc.peakSnr {
			acc.peakSnr = snr
		}

		dTier := distanceTierFromKm(distanceKm)
		sTier := snrTierFromDb(snr)
		acc.tierCounts[dTier][sTier]++

		_ = remoteLoc
	}

	if len(bandAcc) == 0 {
		return resp
	}

	bands := make([]dxBandCondition, 0, len(bandAcc))
	totalScoreWeighted := 0.0
	totalConfWeighted := 0.0
	totalWeight := 0.0
	trendAccumulator := 0.0

	for band, acc := range bandAcc {
		if acc.total == 0 {
			continue
		}

		baselineActivity, targetBaselineUsed := baselineActivityForBand(globalBuckets, targetBuckets, targets, band, resp.CurrentHourOfWk)
		historicalBandSeries := buildBandSparkline(events, targets, band, now)
		trend, trendDelta := computeTrend(historicalBandSeries)

		uniqueCount := len(acc.uniqueKeys)
		repeatRatio := 0.0
		if acc.total > 0 {
			repeatRatio = clamp01(float64(acc.total-uniqueCount) / float64(acc.total))
		}
		avgDistance := 0.0
		if acc.total > 0 {
			avgDistance = acc.totalDistance / float64(acc.total)
		}
		longHaulRatio := 0.0
		if acc.total > 0 {
			longHaulRatio = clamp01(float64(acc.longHaulCount) / float64(acc.total))
		}
		avgSnr := 0.0
		if acc.total > 0 {
			avgSnr = float64(acc.sumSnr) / float64(acc.total)
		}

		currentDensity := float64(acc.total) / math.Max(1, float64(minutes))
		uplift := 0.0
		if baselineActivity > 0 {
			uplift = (currentDensity - baselineActivity) / baselineActivity
		}
		upliftNorm := clamp((uplift+1.0)/2.0, 0.0, 1.8) / 1.8

		activityScore := clamp01(float64(acc.total)/15.0) * 30.0
		uniquenessScore := clamp01(float64(uniqueCount)/float64(maxInt(acc.total, 1))) * 18.0
		distanceScore := clamp01(avgDistance/7000.0)*10.0 + longHaulRatio*10.0
		snrScore := clamp01((avgSnr+24.0)/34.0) * 12.0
		baselineScore := clamp01(upliftNorm) * 20.0
		repeatPenalty := repeatRatio * 10.0

		bandScore := clamp(activityScore+uniquenessScore+distanceScore+snrScore+baselineScore-repeatPenalty, 0, 100)

		baselineSupport := baselineSupportForBand(globalBuckets, targetBuckets, targets, band, resp.CurrentHourOfWk)
		windowSupport := clamp01(float64(minutes) / 45.0)
		sampleSupport := clamp01(float64(acc.total) / 30.0)
		baselineSupportNorm := clamp01(float64(baselineSupport) / 3000.0)
		bandConfidence := clamp(100.0*windowSupport*sampleSupport*baselineSupportNorm, 5, 99)
		if baselineSupport == 0 {
			bandConfidence = clamp(100.0*windowSupport*sampleSupport*0.25, 5, 45)
		}

		bands = append(bands, dxBandCondition{
			Band:               band,
			Score:              bandScore,
			Confidence:         bandConfidence,
			Condition:          dxConditionLabel(bandScore),
			CurrentLinks:       acc.total,
			UniqueLinks:        uniqueCount,
			RepeatRatio:        repeatRatio,
			AvgDistanceKm:      avgDistance,
			LongHaulRatio:      longHaulRatio,
			AvgSnr:             avgSnr,
			PeakSnr:            acc.peakSnr,
			BaselineActivity:   baselineActivity,
			TargetBaselineUsed: targetBaselineUsed,
			Trend:              trend,
			TrendDelta:         trendDelta,
			Sparkline:          historicalBandSeries,
		})

		weight := float64(acc.total)
		totalScoreWeighted += bandScore * weight
		totalConfWeighted += bandConfidence * weight
		totalWeight += weight
		trendAccumulator += trendDelta * weight
	}

	sort.Slice(bands, func(i, j int) bool {
		if bands[i].Score == bands[j].Score {
			return bands[i].Confidence > bands[j].Confidence
		}
		return bands[i].Score > bands[j].Score
	})

	resp.Bands = bands
	if totalWeight > 0 {
		resp.OverallScore = round1(totalScoreWeighted / totalWeight)
		resp.Confidence = round1(totalConfWeighted / totalWeight)
		resp.TrendDelta = round2(trendAccumulator / totalWeight)
	}
	resp.Condition = dxConditionLabel(resp.OverallScore)
	if resp.TrendDelta > 0.2 {
		resp.Trend = "rising"
	} else if resp.TrendDelta < -0.2 {
		resp.Trend = "falling"
	} else {
		resp.Trend = "stable"
	}

	for i := 0; i < len(bands) && i < 3; i++ {
		resp.BestBands = append(resp.BestBands, bands[i].Band)
	}

	return resp
}

func extractMatchedBandEvent(m MQTTMessage, targets []string) (band string, remoteLocator string, qualityKey string, snr int, distanceKm float64, matched bool) {
	band = normalizeBand(m.B)
	if band == "" {
		return "", "", "", 0, 0, false
	}
	sc := strings.ToUpper(strings.TrimSpace(m.SC))
	rc := strings.ToUpper(strings.TrimSpace(m.RC))
	sl := strings.ToUpper(strings.TrimSpace(m.SL))
	rl := strings.ToUpper(strings.TrimSpace(m.RL))
	if sl == "" || rl == "" {
		return "", "", "", 0, 0, false
	}

	isSender := false
	isReceiver := false
	for _, t := range targets {
		if matchCall(sc, t) || (isLocator(t) && strings.HasPrefix(sl, t)) {
			isSender = true
		}
		if matchCall(rc, t) || (isLocator(t) && strings.HasPrefix(rl, t)) {
			isReceiver = true
		}
	}
	if !isSender && !isReceiver {
		return "", "", "", 0, 0, false
	}

	if isSender {
		remoteLocator = rl
	} else {
		remoteLocator = sl
	}
	if remoteLocator == "" {
		return "", "", "", 0, 0, false
	}

	distanceKm = distanceKmForLocators(sl, rl)
	snr = m.RP
	qualityKey = band + "|" + sc + "|" + rc + "|" + remoteLocator
	return band, remoteLocator, qualityKey, snr, distanceKm, true
}

func buildBandSparkline(events []dxObservedEvent, targets []string, band string, now int64) []float64 {
	series := make([]float64, dxSparklineBins)
	if len(events) == 0 {
		return series
	}
	windowStart := now - int64(dxSparklineBins*dxSparklineBinSeconds)
	for _, e := range events {
		if e.T < windowStart || e.T > now {
			continue
		}
		if e.B != band {
			continue
		}
		if !eventMatchesTargets(e, targets) {
			continue
		}
		idx := int((e.T - windowStart) / dxSparklineBinSeconds)
		if idx < 0 {
			idx = 0
		}
		if idx >= dxSparklineBins {
			idx = dxSparklineBins - 1
		}
		series[idx] += eventQualityWeight(e)
	}

	maxV := 0.0
	for _, v := range series {
		if v > maxV {
			maxV = v
		}
	}
	if maxV > 0 {
		for i := range series {
			series[i] = round2((series[i] / maxV) * 100.0)
		}
	}
	return series
}

func eventMatchesTargets(e dxObservedEvent, targets []string) bool {
	sc := strings.ToUpper(strings.TrimSpace(e.SC))
	rc := strings.ToUpper(strings.TrimSpace(e.RC))
	sl := strings.ToUpper(strings.TrimSpace(e.SL))
	rl := strings.ToUpper(strings.TrimSpace(e.RL))
	for _, t := range targets {
		if matchCall(sc, t) || matchCall(rc, t) {
			return true
		}
		if isLocator(t) && (strings.HasPrefix(sl, t) || strings.HasPrefix(rl, t)) {
			return true
		}
	}
	return false
}

func eventQualityWeight(e dxObservedEvent) float64 {
	d := distanceKmForLocators(e.SL, e.RL)
	s := float64(snrTierFromDb(e.RP))
	return 1.0 + clamp01(d/6000.0) + (s * 0.25)
}

func computeTrend(series []float64) (string, float64) {
	if len(series) < 6 {
		return "stable", 0
	}
	mid := len(series) / 2
	prev := average(series[:mid])
	curr := average(series[mid:])
	delta := curr - prev
	if delta > 6 {
		return "rising", round2(delta / 10.0)
	}
	if delta < -6 {
		return "falling", round2(delta / 10.0)
	}
	return "stable", round2(delta / 10.0)
}

func baselineActivityForBand(global, targetBuckets map[string]*baselineBucket, targets []string, band string, hour int) (float64, bool) {
	total := 0.0
	count := 0.0
	for _, t := range targets {
		for d := 0; d <= 4; d++ {
			for s := 0; s <= 3; s++ {
				if b := targetBuckets[baselineTargetKey(t, band, hour, d, s)]; b != nil {
					total += float64(b.Count)
					count++
				}
			}
		}
	}
	if count > 0 {
		return (total / count) / 60.0, true
	}

	for d := 0; d <= 4; d++ {
		for s := 0; s <= 3; s++ {
			if b := global[baselineKey(band, hour, d, s)]; b != nil {
				total += float64(b.Count)
				count++
			}
		}
	}
	if count == 0 {
		return 0, false
	}
	return (total / count) / 60.0, false
}

func baselineSupportForBand(global, targetBuckets map[string]*baselineBucket, targets []string, band string, hour int) int64 {
	var support int64
	for _, t := range targets {
		for d := 0; d <= 4; d++ {
			for s := 0; s <= 3; s++ {
				if b := targetBuckets[baselineTargetKey(t, band, hour, d, s)]; b != nil {
					support += b.Count
				}
			}
		}
	}
	if support > 0 {
		return support
	}
	for d := 0; d <= 4; d++ {
		for s := 0; s <= 3; s++ {
			if b := global[baselineKey(band, hour, d, s)]; b != nil {
				support += b.Count
			}
		}
	}
	return support
}

func utcHourOfWeek(ts int64) int {
	t := time.Unix(ts, 0).UTC()
	return int(t.Weekday())*24 + t.Hour()
}

func normalizeBand(raw string) string {
	b := strings.ToLower(strings.TrimSpace(raw))
	if b == "" {
		return ""
	}
	if strings.HasSuffix(b, "m") {
		return b
	}
	return b + "m"
}

func baselineKey(band string, hourOfWeek, distanceTier, snrTier int) string {
	return band + "|" + itoa(hourOfWeek) + "|" + itoa(distanceTier) + "|" + itoa(snrTier)
}

func baselineTargetKey(target, band string, hourOfWeek, distanceTier, snrTier int) string {
	return normalizeTargetToken(target) + "|" + baselineKey(band, hourOfWeek, distanceTier, snrTier)
}

func normalizeTargetToken(t string) string {
	t = strings.ToUpper(strings.TrimSpace(t))
	if t == "" {
		return ""
	}
	if isLocator(t) {
		if len(t) >= 4 {
			return t[:4]
		}
	}
	return t
}

func snrTierFromDb(db int) int {
	switch {
	case db <= -16:
		return 0
	case db <= -9:
		return 1
	case db <= -2:
		return 2
	default:
		return 3
	}
}

func distanceTierForLocators(a, b string) int {
	km := distanceKmForLocators(a, b)
	return distanceTierFromKm(km)
}

func distanceTierFromKm(km float64) int {
	switch {
	case km < 500:
		return 0
	case km < 1500:
		return 1
	case km < 3000:
		return 2
	case km < 7000:
		return 3
	default:
		return 4
	}
}

func distanceKmForLocators(a, b string) float64 {
	la1, lo1 := locatorToLatLng(strings.ToUpper(strings.TrimSpace(a)))
	la2, lo2 := locatorToLatLng(strings.ToUpper(strings.TrimSpace(b)))
	if (la1 == 0 && lo1 == 0) || (la2 == 0 && lo2 == 0) {
		return 0
	}
	return haversineKm(la1, lo1, la2, lo2)
}

func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0
	toRad := math.Pi / 180.0
	dLat := (lat2 - lat1) * toRad
	dLon := (lon2 - lon1) * toRad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*toRad)*math.Cos(lat2*toRad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return r * c
}

func dxConditionLabel(score float64) string {
	switch {
	case score >= 78:
		return "Excellent"
	case score >= 58:
		return "Good"
	case score >= 38:
		return "Fair"
	default:
		return "Poor"
	}
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round2(v float64) float64 { return math.Round(v*100) / 100 }

func average(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func itoa(v int) string {
	return strconvItoa(v)
}

func strconvItoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := false
	if v < 0 {
		neg = true
		v = -v
	}
	buf := [20]byte{}
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + (v % 10))
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
