package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"horstreporter/internal/proplab"
)

// proplab_server.go exposes the Propagation Lab engines over HTTP so the
// /proplab/ frontend can render A/B/C variants and let the operator override
// parameters interactively. Results are cached for a few seconds because the
// Ladder/Fusion verdicts are recomputed on a 60s tick but the UI may poll more
// frequently while a slider is being dragged.

const proplabCacheTTL = 5 * time.Second

var (
	proplabLadderCache = newProplabCache(proplabCacheTTL)
	proplabFusionCache = newProplabCache(proplabCacheTTL)
	proplabReachCache  = newProplabCache(proplabCacheTTL)
)

func init() {
	// Periodic cleanup of stale cache entries; the cache is tiny, so once a
	// minute is plenty and avoids a leak if the UI is never loaded.
	go func() {
		for range time.Tick(1 * time.Minute) {
			proplabLadderCache.cleanup()
			proplabFusionCache.cleanup()
			proplabReachCache.cleanup()
		}
	}()
}

type proplabCache struct {
	mu      sync.RWMutex
	entries map[string]*proplabCacheEntry
	ttl     time.Duration
}

type proplabCacheEntry struct {
	data    any
	expires time.Time
}

func newProplabCache(ttl time.Duration) *proplabCache {
	return &proplabCache{
		entries: make(map[string]*proplabCacheEntry),
		ttl:     ttl,
	}
}

func (c *proplabCache) get(key string) (any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.data, true
}

func (c *proplabCache) set(key string, data any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = &proplabCacheEntry{data: data, expires: time.Now().Add(c.ttl)}
}

func (c *proplabCache) cleanup() {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		if now.After(e.expires) {
			delete(c.entries, k)
		}
	}
}

func proplabParamsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resp := struct {
		B proplab.LadderParams `json:"b"`
		C proplab.FusionParams `json:"c"`
	}{
		B: proplab.DefaultLadderParams(),
		C: proplab.DefaultFusionParams(),
	}
	writeJSON(w, resp)
}

func proplabLadderHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if proplabService == nil || proplabService.disabled {
		http.Error(w, "Propagation Lab disabled", http.StatusServiceUnavailable)
		return
	}

	target, surroundings := resolveTargetQuery(r)
	params := parseLadderParamsFromQuery(r, proplabService.paramsB)

	cacheKey := proplabLadderCacheKey(target, surroundings, params)
	if cached, ok := proplabLadderCache.get(cacheKey); ok {
		writeJSON(w, cached)
		return
	}

	verdict := proplabService.LadderVerdict(target, surroundings, &params)
	proplabLadderCache.set(cacheKey, verdict)
	writeJSON(w, verdict)
}

func proplabFusionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if proplabService == nil || proplabService.disabled {
		http.Error(w, "Propagation Lab disabled", http.StatusServiceUnavailable)
		return
	}

	params := parseFusionParamsFromQuery(r, proplabService.paramsC)
	cacheKey := proplabFusionCacheKey(params)
	if cached, ok := proplabFusionCache.get(cacheKey); ok {
		writeJSON(w, cached)
		return
	}

	verdict := proplabService.FusionVerdict(&params)
	proplabFusionCache.set(cacheKey, verdict)
	writeJSON(w, verdict)
}

// proplabReachHandler serves the composed reachability product verdict.
// Note: the UI labels the target input "QTH" — resolveTargetQuery is shared
// with the rest of the app (target=/callsign=/locator=), so no rename here.
// An empty target is VALID: it yields the global (unscoped) view.
// No param overrides by design — fixed service defaults so the index means the
// same thing for every operator; tuning happens in proplab-backtest.
func proplabReachHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if proplabService == nil || proplabService.disabled {
		http.Error(w, "Propagation Lab disabled", http.StatusServiceUnavailable)
		return
	}

	target, surroundings := resolveTargetQuery(r)
	cacheKey := proplabReachCacheKey(target, surroundings)
	if cached, ok := proplabReachCache.get(cacheKey); ok {
		writeJSON(w, cached)
		return
	}

	verdict := proplabService.ReachVerdict(target, surroundings)
	proplabReachCache.set(cacheKey, verdict)
	writeJSON(w, verdict)
}

func proplabReachCacheKey(target string, surroundings bool) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%v", target, surroundings)))
	return hex.EncodeToString(sum[:])
}

func proplabLadderCacheKey(target string, surroundings bool, p proplab.LadderParams) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%v|", target, surroundings)
	b, _ := json.Marshal(p)
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

func proplabFusionCacheKey(p proplab.FusionParams) string {
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func parseLadderParamsFromQuery(r *http.Request, defaults proplab.LadderParams) proplab.LadderParams {
	q := r.URL.Query()
	p := defaults
	p.MinLinks = parseIntDefault(q.Get("min_links"), p.MinLinks)
	p.SnrFloorFT8dB = parseIntDefault(q.Get("snr_floor_ft8"), p.SnrFloorFT8dB)
	p.SnrFloorRBNdB = parseIntDefault(q.Get("snr_floor_rbn"), p.SnrFloorRBNdB)
	p.WitnessMin = parseIntDefault(q.Get("witness_min"), p.WitnessMin)
	p.EsMinKm = parseIntDefault(q.Get("es_min_km"), p.EsMinKm)
	p.EsMaxKm = parseIntDefault(q.Get("es_max_km"), p.EsMaxKm)
	p.CoherenceMinBands = parseIntDefault(q.Get("coherence_min_bands"), p.CoherenceMinBands)
	p.CusumDrift = parseFloatDefault(q.Get("cusum_drift"), p.CusumDrift)
	p.CusumThreshold = parseFloatDefault(q.Get("cusum_threshold"), p.CusumThreshold)
	p.EwmaAlpha = parseFloatDefault(q.Get("ewma_alpha"), p.EwmaAlpha)
	p.ExpectedLookbackDays = parseIntDefault(q.Get("expected_lookback_days"), p.ExpectedLookbackDays)
	p.TermMinEastDeg = parseFloatDefault(q.Get("term_min_east_deg"), p.TermMinEastDeg)
	p.TermMaxEastDeg = parseFloatDefault(q.Get("term_max_east_deg"), p.TermMaxEastDeg)
	p.TermDegPerHour = parseFloatDefault(q.Get("term_deg_per_hour"), p.TermDegPerHour)
	return p
}

func parseFusionParamsFromQuery(r *http.Request, defaults proplab.FusionParams) proplab.FusionParams {
	q := r.URL.Query()
	p := defaults
	p.LookbackDays = parseIntDefault(q.Get("lookback_days"), p.LookbackDays)
	p.QuantileLo = parseFloatDefault(q.Get("quantile_lo"), p.QuantileLo)
	p.QuantileHi = parseFloatDefault(q.Get("quantile_hi"), p.QuantileHi)
	p.GuardbandSigma = parseFloatDefault(q.Get("guardband_sigma"), p.GuardbandSigma)
	p.GuardbandWeight = parseFloatDefault(q.Get("guardband_weight"), p.GuardbandWeight)
	p.WitnessPerDayMin = parseFloatDefault(q.Get("witness_per_day_min"), p.WitnessPerDayMin)
	p.OpenRatio = parseFloatDefault(q.Get("open_ratio"), p.OpenRatio)
	p.ClosedRatio = parseFloatDefault(q.Get("closed_ratio"), p.ClosedRatio)
	p.KpAbsorb = parseFloatDefault(q.Get("kp_absorb"), p.KpAbsorb)
	p.SfiLow = parseFloatDefault(q.Get("sfi_low"), p.SfiLow)
	if v := q.Get("xray_min_class"); v != "" {
		p.XrayMinClass = v
	}
	return p
}

func parseFloatDefault(raw string, fallback float64) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil {
		return v
	}
	return fallback
}

func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(data)
}
