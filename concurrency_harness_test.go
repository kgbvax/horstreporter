package main

// Concurrent-load harness: the serial latency harness (latency_harness_test.go)
// measures one request at a time, so it cannot see per-request memory that
// scales with concurrency (hub.history window copies) or lock contention with
// the ingest append path. This one drives C parallel clients over several
// QTHs plus a live ingest appender for a fixed duration and reports
// throughput, tail latency, peak process memory and GC cost.
//
// Gated behind HORST_CONCURRENCY_HARNESS=1. Run via
// scripts/measure-concurrency.mjs (parses the single HARNESS_JSON line).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"runtime/metrics"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConcurrentLoadHarness(t *testing.T) {
	if os.Getenv("HORST_CONCURRENCY_HARNESS") == "" {
		t.Skip("concurrency harness: set HORST_CONCURRENCY_HARNESS=1 to run (invoked via scripts/measure-concurrency.mjs)")
	}
	envInt := func(name string, def int) int {
		if v := os.Getenv(name); v != "" {
			var n int
			fmt.Sscanf(v, "%d", &n)
			return n
		}
		return def
	}
	count := envInt("HORST_LATENCY_HISTORY", 1_000_000)
	clients := envInt("HORST_CONCURRENCY_CLIENTS", 16)
	seconds := envInt("HORST_CONCURRENCY_SECONDS", 12)
	ingestPerSec := envInt("HORST_CONCURRENCY_INGEST_PER_SEC", 400)

	srv := wireLatencyWorld(t, count)

	// The web app sends rings=auto on every live request (static/live-area.js);
	// the summary endpoint feeds the mobile widgets.
	endpoints := []string{
		"/api/prop_intel/v2?qth=%s&minutes=15&rings=auto",
		"/api/dx_conditions?qth=%s&minutes=15&rings=auto",
		"/api/hot_bands?qth=%s&minutes=15&rings=auto",
		"/api/prop_intel/summary?qth=%s&minutes=15",
	}
	qths := []string{"JO62QM", "JO31AB", "IO91AB", "FN20AB", "EM73AB", "JN58AB", "PM95AB", "QF22AB"}

	// Settle the heap so the baseline excludes fixture construction.
	runtime.GC()
	var ms0 runtime.MemStats
	runtime.ReadMemStats(&ms0)

	deadline := time.Now().Add(time.Duration(seconds) * time.Second)

	// Live ingest: append under the hub write lock, like broadcastMsg. Tracks
	// the worst time the append waited for the lock (reader/ingest contention).
	var maxLockWaitNs, appended int64
	stopIngest := make(chan struct{})
	var ingestWG sync.WaitGroup
	ingestWG.Add(1)
	go func() {
		defer ingestWG.Done()
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		perTick := ingestPerSec / 100
		if perTick < 1 {
			perTick = 1
		}
		for {
			select {
			case <-stopIngest:
				return
			case <-tick.C:
				for i := 0; i < perTick; i++ {
					m := MQTTMessage{T: time.Now().Unix(), B: "20m", MD: "FT8", Source: "mqtt", SC: "DK3JF", RC: "W1AW", SL: "JO62", RL: "FN20", RP: -10}
					start := time.Now()
					hub.Lock()
					w := time.Since(start).Nanoseconds()
					hub.history = append(hub.history, m)
					hub.Unlock()
					if w > atomic.LoadInt64(&maxLockWaitNs) {
						atomic.StoreInt64(&maxLockWaitNs, w)
					}
					atomic.AddInt64(&appended, 1)
				}
			}
		}
	}()

	// Peak process memory sampler (Go runtime's view of mapped memory).
	var peakTotal uint64
	stopSample := make(chan struct{})
	var sampleWG sync.WaitGroup
	sampleWG.Add(1)
	go func() {
		defer sampleWG.Done()
		sample := []metrics.Sample{{Name: "/memory/classes/total:bytes"}}
		tick := time.NewTicker(25 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stopSample:
				return
			case <-tick.C:
				metrics.Read(sample)
				if v := sample[0].Value.Uint64(); v > peakTotal {
					peakTotal = v
				}
			}
		}
	}()

	type lat struct {
		mu sync.Mutex
		ms []float64
	}
	perEP := make([]*lat, len(endpoints))
	for i := range perEP {
		perEP[i] = &lat{}
	}
	var errs int64
	var wg sync.WaitGroup
	httpClient := &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: clients}}
	for c := 0; c < clients; c++ {
		wg.Add(1)
		go func(c int) {
			defer wg.Done()
			for i := c; time.Now().Before(deadline); i++ {
				ep := i % len(endpoints)
				q := qths[(i/len(endpoints)+c)%len(qths)]
				start := time.Now()
				resp, err := httpClient.Get(srv.URL + fmt.Sprintf(endpoints[ep], q))
				if err != nil {
					atomic.AddInt64(&errs, 1)
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 200 {
					atomic.AddInt64(&errs, 1)
				}
				ms := float64(time.Since(start).Nanoseconds()) / 1e6
				perEP[ep].mu.Lock()
				perEP[ep].ms = append(perEP[ep].ms, ms)
				perEP[ep].mu.Unlock()
			}
		}(c)
	}
	wg.Wait()
	close(stopIngest)
	close(stopSample)
	ingestWG.Wait()
	sampleWG.Wait()

	var ms1 runtime.MemStats
	runtime.ReadMemStats(&ms1)

	type epStat struct {
		Endpoint string  `json:"endpoint"`
		N        int     `json:"n"`
		P50Ms    float64 `json:"p50_ms"`
		P95Ms    float64 `json:"p95_ms"`
		MaxMs    float64 `json:"max_ms"`
	}
	stats := []epStat{}
	total := 0
	for i, l := range perEP {
		sort.Float64s(l.ms)
		if len(l.ms) == 0 {
			continue
		}
		total += len(l.ms)
		stats = append(stats, epStat{
			Endpoint: strings.SplitN(fmt.Sprintf(endpoints[i], "QTH"), "?", 2)[0],
			N:        len(l.ms),
			P50Ms:    percentile(l.ms, 0.50),
			P95Ms:    percentile(l.ms, 0.95),
			MaxMs:    l.ms[len(l.ms)-1],
		})
	}
	payload := map[string]interface{}{
		"history_size":       count,
		"clients":            clients,
		"seconds":            seconds,
		"requests":           total,
		"req_per_sec":        float64(total) / float64(seconds),
		"errors":             errs,
		"endpoints":          stats,
		"peak_total_mb":      float64(peakTotal) / (1 << 20),
		"baseline_heap_mb":   float64(ms0.HeapAlloc) / (1 << 20),
		"alloc_gb":           float64(ms1.TotalAlloc-ms0.TotalAlloc) / (1 << 30),
		"num_gc":             ms1.NumGC - ms0.NumGC,
		"gc_pause_total_ms":  float64(ms1.PauseTotalNs-ms0.PauseTotalNs) / 1e6,
		"ingest_appended":    appended,
		"ingest_max_wait_ms": float64(maxLockWaitNs) / 1e6,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	fmt.Printf("HARNESS_JSON:%s\n", b)
}
