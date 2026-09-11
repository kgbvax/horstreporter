package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// sw_ingest.go polls free NOAA SWPC feeds into the proplab_sw_series table,
// consumed by the pathscope module. It is all that remains of the Propagation
// Lab's space-weather context (the Fusion/D-RAP consumers were removed).
// It runs only when -proplab-sw-enable is set. Each feed is polled in its own
// goroutine so a slow or temporarily broken feed cannot block the others.
//
// Feeds:
//   - Kp 1-minute planetary index
//   - F10.7 cm solar flux (observed, daily text fallback)
//   - GOES X-ray flux + class
//   - OVATION auroral power (hemispheric estimate)
//   - D-RAP highest-affected-frequency grid

// SWPC feed URLs. Package vars (same defer-swap pattern as main_test.go's
// globals) so tests can point them at a mock upstream; the values are the
// production URLs. swDrapURL has no poller today (D-RAP consumer removed
// 2026-08-04) and stays a constant.
var (
	swKpURL      = "https://services.swpc.noaa.gov/json/planetary_k_index_1m.json"
	swF107URL    = "https://services.swpc.noaa.gov/text/daily-solar-indices.txt"
	swXrayURL    = "https://services.swpc.noaa.gov/json/goes/primary/xrays-6-hour.json"
	swOvationURL = "https://services.swpc.noaa.gov/json/ovation_aurora_latest.json"
)

const swDrapURL = "https://services.swpc.noaa.gov/text/drap_global_frequencies.txt"

type swIngestService struct {
	store  *dxPostgresStore
	stopCh chan struct{}
	wg     sync.WaitGroup
	client *http.Client
}

func startProplabSWIngest() {
	store := (*dxPostgresStore)(nil)
	if cellBucketFeed != nil {
		store = cellBucketFeed.store
	}
	svc := &swIngestService{
		store:  store,
		stopCh: make(chan struct{}),
		client: &http.Client{Timeout: 30 * time.Second},
	}
	svc.start()
}

func (s *swIngestService) start() {
	if s.store == nil {
		logInfo("Proplab SW ingest disabled: no Postgres store")
		return
	}

	// One immediate fetch so the Fusion engine has data right after startup.
	s.fetchAll()

	feeds := []struct {
		name     string
		interval time.Duration
		fn       func()
	}{
		{"kp", 5 * time.Minute, s.fetchKp},
		{"f10.7", 15 * time.Minute, s.fetchF107},
		{"xray", 1 * time.Minute, s.fetchXray},
		{"ovation", 5 * time.Minute, s.fetchOvation},
	}

	for _, f := range feeds {
		s.wg.Add(1)
		go func(f struct {
			name     string
			interval time.Duration
			fn       func()
		}) {
			defer s.wg.Done()
			ticker := time.NewTicker(f.interval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					f.fn()
				case <-s.stopCh:
					return
				}
			}
		}(f)
	}
}

func (s *swIngestService) fetchAll() {
	s.fetchKp()
	s.fetchF107()
	s.fetchXray()
	s.fetchOvation()
}

func (s *swIngestService) fetchKp() {
	body, err := httpGet(s.client, swKpURL)
	if err != nil {
		logInfo("SW Kp fetch failed: %v", err)
		return
	}
	var records []struct {
		TimeTag     string  `json:"time_tag"`
		EstimatedKp float64 `json:"estimated_kp"`
		ObservedKp  int     `json:"kp_index"`
		ObservedKpS string  `json:"kp"`
	}
	if err := json.Unmarshal(body, &records); err != nil {
		logInfo("SW Kp parse failed: %v", err)
		return
	}
	now := time.Now().Unix()
	rows := make([]proplabSWRow, 0, len(records))
	var latest proplabSWRow
	for _, r := range records {
		ts, ok := parseSWPCTime(r.TimeTag)
		if !ok {
			continue
		}
		v := r.EstimatedKp
		if v <= 0 {
			v = float64(r.ObservedKp)
		}
		rows = append(rows, proplabSWRow{Series: "kp", ObsTime: ts, Value: v})
		if ts > latest.ObsTime {
			latest = proplabSWRow{Series: "kp", ObsTime: ts, Value: v}
		}
	}
	if len(rows) == 0 {
		return
	}
	// Cap how far back we store on first fetch.
	if len(rows) > 10 {
		rows = rows[len(rows)-10:]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.store.upsertProplabSW(ctx, rows); err != nil {
		logInfo("SW Kp store failed: %v", err)
		return
	}
	if latest.ObsTime == 0 {
		latest.ObsTime = now
	}
}

func (s *swIngestService) fetchF107() {
	body, err := drapTextGet(s.client, swF107URL)
	if err != nil {
		logInfo("SW F10.7 fetch failed: %v", err)
		return
	}
	v, ts, ok := parseDailySolarIndicesF107(string(body))
	if !ok {
		logInfo("SW F10.7 parse failed: no recent value found")
		return
	}
	row := proplabSWRow{Series: "F10.7", ObsTime: ts, Value: v}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.store.upsertProplabSW(ctx, []proplabSWRow{row}); err != nil {
		logInfo("SW F10.7 store failed: %v", err)
		return
	}
}

func (s *swIngestService) fetchXray() {
	body, err := httpGet(s.client, swXrayURL)
	if err != nil {
		logInfo("SW X-ray fetch failed: %v", err)
		return
	}
	var records []struct {
		TimeTag string  `json:"time_tag"`
		Flux    float64 `json:"flux"`
		Energy  string  `json:"energy"`
	}
	if err := json.Unmarshal(body, &records); err != nil {
		logInfo("SW X-ray parse failed: %v", err)
		return
	}
	// Records alternate between short (0.05-0.4nm) and long (0.1-0.8nm)
	// wavelengths. We want the long-wavelength flux used for flare class.
	var best *struct {
		TimeTag string
		Flux    float64
	}
	for _, r := range records {
		if r.Flux <= 0 {
			continue
		}
		if !strings.Contains(r.Energy, "0.1-0.8") {
			continue
		}
		if best == nil || r.TimeTag > best.TimeTag {
			best = &struct {
				TimeTag string
				Flux    float64
			}{TimeTag: r.TimeTag, Flux: r.Flux}
		}
	}
	if best == nil {
		logInfo("SW X-ray parse failed: no 0.1-0.8nm record")
		return
	}
	ts, ok := parseSWPCTime(best.TimeTag)
	if !ok {
		ts = time.Now().Unix()
	}
	row := proplabSWRow{Series: "xray", ObsTime: ts, Value: best.Flux}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.store.upsertProplabSW(ctx, []proplabSWRow{row}); err != nil {
		logInfo("SW X-ray store failed: %v", err)
		return
	}
}

func (s *swIngestService) fetchOvation() {
	body, err := httpGet(s.client, swOvationURL)
	if err != nil {
		logInfo("SW OVATION fetch failed: %v", err)
		return
	}
	var doc struct {
		ForecastTime string `json:"Forecast Time"`
		Coordinates  [][3]int
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		logInfo("SW OVATION parse failed: %v", err)
		return
	}
	var sum float64
	var count int
	for _, c := range doc.Coordinates {
		if len(c) < 3 {
			continue
		}
		v := c[2]
		if v >= 0 {
			sum += float64(v)
			count++
		}
	}
	if count == 0 {
		logInfo("SW OVATION parse failed: no coordinate values")
		return
	}
	gw := sum / float64(count)
	ts, _ := parseSWPCTime(doc.ForecastTime)
	if ts == 0 {
		ts = time.Now().Unix()
	}
	row := proplabSWRow{Series: "ovation", ObsTime: ts, Value: gw}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.store.upsertProplabSW(ctx, []proplabSWRow{row}); err != nil {
		logInfo("SW OVATION store failed: %v", err)
		return
	}
}

// drapTextGet fetches a plain-text D-RAP grid with the correct Accept header.
func drapTextGet(client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("User-Agent", "horstreporter-proplab/1.0")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, 1<<20))
}

func httpGet(client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "horstreporter-proplab/1.0")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, 1<<20))
}

// parseSWPCTime accepts "2026-07-31 06:00:00.000", ISO8601 with offset/Z,
// or a local ISO8601 without offset.
func parseSWPCTime(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		"2006-01-02 15:04:05.000",
		"2006-01-02T15:04:05-07:00",
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix(), true
		}
	}
	return 0, false
}

// parseDailySolarIndicesF107 extracts the most recent daily F10.7 value from
// the NOAA SWPC daily-solar-indices text file. It returns flux, UTC timestamp
// for that date at noon, and ok.
func parseDailySolarIndicesF107(text string) (float64, int64, bool) {
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Split(bufio.ScanLines)
	// Keep the last row with a physically valid flux, not just the last line:
	// NOAA's "today" row often carries -1 (or -999) for the unobserved flux,
	// and strconv.ParseFloat happily parses -1.0 — which would corrupt the
	// propagation model (F10.7 is ~60-300 sfu). Scan all rows and prefer the
	// newest one whose flux is a real observed value.
	var bestFlux float64
	var bestTime int64
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ":") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		flux, err := strconv.ParseFloat(fields[3], 64)
		if err != nil {
			continue
		}
		// Reject sentinels (-1, -999, -9999) and non-physical values.
		// Observed F10.7 is ~60-300 sfu; accept any positive value to stay
		// permissive but reject the common placeholders outright.
		if flux <= 0 {
			continue
		}
		dateStr := fields[0] + " " + fields[1] + " " + fields[2]
		t, err := time.Parse("2006 01 02", dateStr)
		if err != nil {
			continue
		}
		// Rows are in ascending date order; later valid rows win.
		bestFlux = flux
		bestTime = t.Add(12 * time.Hour).Unix()
	}
	if bestTime == 0 {
		return 0, 0, false
	}
	return bestFlux, bestTime, true
}
