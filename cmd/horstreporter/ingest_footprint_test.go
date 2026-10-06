package main

// In-process ingest-footprint harness for ce-optimize run #3 (ingest-storage).
//
// Drives the exact production MQTT ingest path (ingestPSKRMessage: payload
// parse, topic backfill, normalization, dxBaseline.Observe + hub.broadcastMsg)
// over a synthetic PSKR spot stream and reports per-spot CPU, allocation
// count, and allocation bytes, plus flush row-width accounting for the
// dx_raw_spots pipeline (the dominant disk consumer) and the hub.history
// memory share.
//
// Gated behind HORST_INGEST_HARNESS=1 so a plain `go test ./...` skips the
// fixture in microseconds. Run via scripts/measure-ingest-footprint.mjs,
// which parses the single HARNESS_JSON line from stdout.

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"unsafe"
)

const ingestHarnessEnv = "HORST_INGEST_HARNESS"

// buildIngestStream generates count realistic PSKR v2 spots. Mix mirrors the
// live feed: mostly topic-encoded (JSON carries only rp/t; the handler
// backfills from the topic — the dominant path on the real feed), a minority
// fully JSON-encoded. Callsigns/locators vary deterministically so string
// interning in the fixture can't flatter the engine maps.
func buildIngestStream(count int) ([]string, [][]byte) {
	bands := []string{"20m", "40m", "30m", "15m", "17m", "80m", "10m", "6m"}
	suffixes := []string{"", "", "", "/P", "/M"}
	topics := make([]string, 0, count)
	payloads := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		band := bands[i%len(bands)]
		sc := fmt.Sprintf("DK%dABC%s", 1+i%80, suffixes[i%len(suffixes)])
		rc := fmt.Sprintf("W%dXYZ%s", 1+i%500, suffixes[(i+1)%len(suffixes)])
		sl := fmt.Sprintf("JO6%drq", i%10)
		rl := fmt.Sprintf("FN3%drq", i%10)
		// Topic format: pskr/filter/v2/{band}/{mode}/{senderCall}/{receiverCall}/{senderLocator}/{receiverLocator}
		topic := "pskr/filter/v2/" + band + "/FT8/" + sc + "/" + rc + "/" + sl + "/" + rl
		if i%5 == 0 {
			// Fully JSON-encoded spot (band/mode/calls/locators in the payload).
			payloads = append(payloads, []byte(fmt.Sprintf(
				`{"t":1700000000,"rp":%d,"b":"%s","md":"FT8","sc":"%s","rc":"%s","sl":"%s","rl":"%s"}`,
				-30+(i%45), band, sc, rc, sl, rl)))
		} else {
			payloads = append(payloads, []byte(fmt.Sprintf(`{"t":1700000000,"rp":%d}`, -30+(i%45))))
		}
		topics = append(topics, topic)
	}
	return topics, payloads
}

func TestIngestFootprintHarness(t *testing.T) {
	if os.Getenv(ingestHarnessEnv) == "" {
		t.Skip("ingest harness: set HORST_INGEST_HARNESS=1 to run (invoked via scripts/measure-ingest-footprint.mjs)")
	}

	count := 200_000
	if v := os.Getenv("HORST_INGEST_SPOTS"); v != "" {
		fmt.Sscanf(v, "%d", &count)
	}

	// --- wire fresh engines exactly as main.go does (no Postgres store) -----
	dir := t.TempDir()
	dxEng := newDxBaselineEngine(filepath.Join(dir, "dx_baseline.json"))
	if err := dxEng.Load(); err != nil {
		t.Fatalf("baseline Load: %v", err)
	}
	propEng := newPropBaselineEngine("", "")

	savedHubHistory := hub.history
	savedBaseline := dxBaseline
	savedPropBaseline := propBaseline
	defer func() {
		hub.Lock()
		hub.history = savedHubHistory
		hub.Unlock()
		dxBaseline = savedBaseline
		propBaseline = savedPropBaseline
	}()
	hub.Lock()
	hub.history = nil
	hub.Unlock()
	dxBaseline = dxEng
	propBaseline = propEng

	topics, payloads := buildIngestStream(count)

	// Warmup: one pass to grow engine maps/slices to steady state so the
	// measured passes aren't dominated by first-touch allocation.
	for i := 0; i < count/10; i++ {
		ingestPSKRMessage(topics[i], payloads[i])
	}
	hub.Lock()
	hub.history = nil
	hub.Unlock()
	runtime.GC()

	var memBefore, memAfter runtime.MemStats
	runtime.ReadMemStats(&memBefore)
	start := time.Now()
	for i := 0; i < count; i++ {
		ingestPSKRMessage(topics[i], payloads[i])
	}
	elapsed := time.Since(start)
	runtime.ReadMemStats(&memAfter)

	perSpotNs := math.Round(float64(elapsed.Nanoseconds())/float64(count)*1000) / 1000
	allocsPerSpot := math.Round(float64(memAfter.Mallocs-memBefore.Mallocs)/float64(count)*100) / 100
	allocBytesPerSpot := math.Round(float64(memAfter.TotalAlloc-memBefore.TotalAlloc)/float64(count)*100) / 100

	// Parse-only stage (diagnostic): unmarshal only — isolates the JSON cost
	// share of ingest (topic backfill + normalization + engines are the rest).
	runtime.GC()
	start = time.Now()
	for i := 0; i < count; i++ {
		var m MQTTMessage
		_ = json.Unmarshal(payloads[i], &m)
	}
	parseNs := time.Since(start)

	// Row-width accounting for the dx_raw_spots pipeline (diagnostic): the
	// flusher inserts 13 columns per spot (buildRawSpotInsertSQL: spot_time,
	// band, sender/receiver callsign+locator, mode, signal_report_db,
	// source_grid4, source_type, spotter, frequency_khz, comment — spot_geom
	// was dropped in run #3 H5 as write-only). Width = summed wire width of
	// the argument values plus a PG tuple overhead estimate (23B header + 2B
	// null bitmap per column; alignment padding ignored) — an estimate
	// grounded in the real INSERT shape.
	argWidth := 0
	for _, a := range []interface{}{
		int64(8),       // spot_time
		len("20m"),     // band
		len("DK1ABC"),  // sender_callsign
		len("W1XYZ/P"), // receiver_callsign
		len("JO62RQ"),  // sender_locator
		len("FN31RQ"),  // receiver_locator
		len("FT8"),     // mode
		8,              // signal_report_db (float)
		len("JO62"),    // source_grid4
		len("mqtt"),    // source_type
		len(""),        // spotter_callsign (empty on mqtt)
		8,              // frequency_khz (NULL, arg placeholder)
		len(""),        // comment (empty)
	} {
		if w, ok := a.(int); ok {
			argWidth += w
		} else {
			argWidth += 8
		}
	}
	tupleOverhead := 23 + 13*2
	rowWidth := argWidth + tupleOverhead

	out := map[string]interface{}{
		"spots_measured":                 count,
		"ingest_cpu_ns_per_spot":         perSpotNs,
		"ingest_allocs_per_spot":         allocsPerSpot,
		"ingest_alloc_bytes_per_spot":    allocBytesPerSpot,
		"parse_only_ns_per_spot":         math.Round(float64(parseNs.Nanoseconds())/float64(count)*1000) / 1000,
		"raw_spot_row_width_bytes":       rowWidth,
		"raw_spot_row_width_is_estimate": true,
		"mqtt_message_struct_bytes":      int(unsafe.Sizeof(MQTTMessage{})),
		"history_entries_after_run":      count,
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	fmt.Printf("HARNESS_JSON:%s\n", b)
}
