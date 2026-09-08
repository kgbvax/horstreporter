package main

// In-process latency harness for ce-optimize run #2 (prop-latency).
//
// Measures the in-memory request path of the latency-sensitive JSON
// endpoints — /api/prop_intel, /api/prop_intel/summary, /api/prop_intel/v2,
// /api/dx_conditions, /api/hot_bands — against a synthetic hub.history at
// prod-like scale, with no Postgres configured (the climatology engines fall
// back to their in-memory buckets, which is the "warm cache" steady state).
// The PG cold path is out of measured scope by user decision (2026-09-08).
//
// Gated behind HORST_LATENCY_HARNESS=1 so a plain `go test ./...` skips the
// whole fixture in microseconds. Run via scripts/measure-prop-latency.mjs,
// which parses the single HARNESS_JSON line from stdout.

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

const latencyHarnessEnv = "HORST_LATENCY_HARNESS"

// latencyEndpoints are measured in listed order; p50/p95 reported per name.
var latencyEndpoints = []string{
	"/api/prop_intel?qth=%s&minutes=15",
	"/api/prop_intel/v2?qth=%s&minutes=15",
	"/api/dx_conditions?qth=%s&minutes=15",
	"/api/hot_bands?qth=%s&minutes=15",
	// The summary handler keeps a single-entry response cache; alternating
	// the qth between two values forces every request to recompute instead
	// of serving the 60s cache, so the metric reflects engine cost.
	"/api/prop_intel/summary?qth=%s&minutes=15",
}

// latencyFixture wires a synthetic prod-scale world: history depth and
// station mix below, then restores every package-level singleton on cleanup.
type latencyFixture struct {
	historyLen int
}

// buildLatencyHistory generates count messages ascending in T (handlers
// binary-search on T, so ordering is load-bearing), spread over 60 minutes.
// Mix: ~70% FT8 (mqtt source), ~30% WSPR (wspr source with TXPower), bands
// and locators drawn from fixed pools so the region/band accumulators see a
// realistic spread. Deterministic seed for stable baselines.
func buildLatencyHistory(count int) []MQTTMessage {
	locators := []string{"JO62", "JO31", "JN58", "IO91", "FN20", "EM73", "PM95", "QF22", "OJ12"}
	bands := []string{"160m", "80m", "40m", "30m", "20m", "17m", "15m", "12m", "10m", "6m", "2m"}
	rng := rand.New(rand.NewSource(1700000000))
	now := time.Now().Unix()
	window := int64(60 * 60)

	out := make([]MQTTMessage, 0, count)
	for i := 0; i < count; i++ {
		ts := now - window + int64(i)*window/int64(count+1)
		band := bands[rng.Intn(len(bands))]
		sl := locators[rng.Intn(len(locators))]
		rl := locators[rng.Intn(len(locators))]
		m := MQTTMessage{T: ts, B: band, SL: sl, RL: rl}
		if rng.Intn(10) < 7 {
			m.MD = "FT8"
			m.Source = "mqtt"
			m.SC = "DK3JF"
			m.RC = "W1AW"
			m.RP = rng.Intn(40) - 25
		} else {
			m.MD = "WSPR"
			m.Source = "wspr"
			m.SC = "RXCALL"
			m.RC = "TXCALL"
			m.RP = rng.Intn(30) - 25
			m.TXPower = []int{20, 23, 37, 43}[rng.Intn(4)]
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out
}

func percentile(sortedMs []float64, p float64) float64 {
	if len(sortedMs) == 0 {
		return 0
	}
	idx := int(math.Ceil(p*float64(len(sortedMs)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sortedMs) {
		idx = len(sortedMs) - 1
	}
	return math.Round(sortedMs[idx]*1000) / 1000
}

func TestPropLatencyHarness(t *testing.T) {
	if os.Getenv(latencyHarnessEnv) == "" {
		t.Skip("latency harness: set HORST_LATENCY_HARNESS=1 to run (invoked via scripts/measure-prop-latency.mjs)")
	}

	count := 1_000_000
	if v := os.Getenv("HORST_LATENCY_HISTORY"); v != "" {
		fmt.Sscanf(v, "%d", &count)
	}
	requests := 60
	if v := os.Getenv("HORST_LATENCY_REQUESTS"); v != "" {
		fmt.Sscanf(v, "%d", &requests)
	}

	// --- wire the engines the way main.go does (no Postgres store) ---------
	dir := t.TempDir()
	dxEng := newDxBaselineEngine(filepath.Join(dir, "dx_baseline.json"))
	if err := dxEng.Load(); err != nil {
		t.Fatalf("baseline Load: %v", err)
	}

	savedHubHistory := hub.history
	savedBaseline := dxBaseline
	savedClim := wsprClimatology
	savedPropIntelBaseline := propIntel.baseline
	defer func() {
		hub.Lock()
		hub.history = savedHubHistory
		hub.Unlock()
		dxBaseline = savedBaseline
		wsprClimatology = savedClim
		propIntel.baseline = savedPropIntelBaseline
	}()

	history := buildLatencyHistory(count)
	// Feed the engines the same stream the ingest would: the climatology and
	// baseline buckets then carry realistic occupancy for Evaluate/HotBands.
	for _, m := range history {
		dxEng.Observe(m)
	}
	clim := newWsprClimatologyEngine("")
	for _, m := range history {
		if m.Source == "wspr" {
			clim.Observe(m)
		}
	}

	hub.Lock()
	hub.history = history
	hub.Unlock()
	dxBaseline = dxEng
	wsprClimatology = clim
	propIntel.baseline = dxEng

	resetPropIntelSummaryCache()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/prop_intel", propIntelHandler)
	mux.HandleFunc("/api/prop_intel/summary", propIntelSummaryHandler)
	mux.HandleFunc("/api/prop_intel/v2", propIntelV2Handler)
	mux.HandleFunc("/api/dx_conditions", dxConditionsHandler)
	mux.HandleFunc("/api/hot_bands", hotBandsHandler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := &http.Client{Timeout: 30 * time.Second}
	qths := []string{"JO62QM", "JO31AB"}

	type endpointStat struct {
		Endpoint    string   `json:"endpoint"`
		P50Ms       float64  `json:"p50_ms"`
		P95Ms       float64  `json:"p95_ms"`
		MaxMs       float64  `json:"max_ms"`
		MeanMs      float64  `json:"mean_ms"`
		Samples     int      `json:"samples"`
		StatusCodes map[int]int `json:"status_codes"`
	}

	out := map[string]interface{}{
		"history_size":    count,
		"requests_per_ep": requests,
		"endpoints":       []endpointStat{},
	}

	for i, tpl := range latencyEndpoints {
		// Summary alternates qth to defeat its single-entry cache; the other
		// endpoints have no response cache and always recompute.
		useAlt := tpl == "/api/prop_intel/summary?qth=%s&minutes=15"
		// Warmup (not measured): primes pools, caches, and the TCP conn.
		for w := 0; w < 5; w++ {
			q := qths[w%len(qths)]
			if !useAlt {
				q = qths[0]
			}
			resp, err := client.Get(srv.URL + fmt.Sprintf(tpl, q))
			if err != nil {
				t.Fatalf("warmup %s: %v", tpl, err)
			}
			resp.Body.Close()
		}

		samples := make([]float64, 0, requests)
		statuses := map[int]int{}
		sum := 0.0
		for r := 0; r < requests; r++ {
			q := qths[0]
			if useAlt {
				q = qths[r%len(qths)]
			}
			start := time.Now()
			resp, err := client.Get(srv.URL + fmt.Sprintf(tpl, q))
			if err != nil {
				t.Fatalf("request %s: %v", tpl, err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			elapsed := float64(time.Since(start).Nanoseconds()) / 1e6
			samples = append(samples, elapsed)
			sum += elapsed
			statuses[resp.StatusCode]++
		}
		sort.Float64s(samples)
		stat := endpointStat{
			Endpoint:    fmt.Sprintf(tpl, "QTH"),
			P50Ms:       percentile(samples, 0.50),
			P95Ms:       percentile(samples, 0.95),
			MaxMs:       samples[len(samples)-1],
			MeanMs:      math.Round(sum/float64(len(samples))*1000) / 1000,
			Samples:     len(samples),
			StatusCodes: statuses,
		}
		out["endpoints"] = append(out["endpoints"].([]endpointStat), stat)
		_ = i
	}

	payload := map[string]interface{}{
		"history_size": out["history_size"],
		"requests_per_ep": out["requests_per_ep"],
		"endpoints":    out["endpoints"],
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	fmt.Printf("HARNESS_JSON:%s\n", b)
}