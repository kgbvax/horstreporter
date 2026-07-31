package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"horstreporter/internal/proplab"
)

// sw_ingest.go polls free NOAA SWPC feeds for the Propagation Lab Fusion engine.
// It runs only when -proplab-sw-enable is set. Each feed is polled in its own
// goroutine so a slow or temporarily broken feed cannot block the others.
//
// Feeds:
//   - Kp 1-minute planetary index
//   - F10.7 cm solar flux (observed)
//   - GOES X-ray flux + class
//   - OVATION auroral power (hemispheric estimate)
//   - D-RAP highest-affected-frequency grid (placeholder; full grid parsing is
//     left for a later pass because the ASCII grid format is large and finicky)

const (
	swKpURL      = "https://services.swpc.noaa.gov/products/noaa-planetary-k-index-1-minute.json"
	swF107URL    = "https://services.swpc.noaa.gov/products/10cm-flux-30-day.json"
	swXrayURL    = "https://services.swpc.noaa.gov/products/goes-xray-flux-1-minute.json"
	swOvationURL = "https://services.swpc.noaa.gov/products/ovation/aurora/latest.json"
)

type swIngestService struct {
	mu     sync.RWMutex
	store  *dxPostgresStore
	stopCh chan struct{}
	wg     sync.WaitGroup
	client *http.Client

	sw proplab.FusionSWSnapshot
}

func startProplabSWIngest() {
	svc := &swIngestService{
		store:  proplabService.store,
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

func (s *swIngestService) snapshot() proplab.FusionSWSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sw
}

func (s *swIngestService) fetchKp() {
	rows, err := fetchSWPCJsonSeries(s.client, swKpURL, "Kp", "index", func(v string) (float64, bool) {
		f, err := strconv.ParseFloat(v, 64)
		return f, err == nil
	})
	if err != nil {
		logInfo("SW Kp fetch failed: %v", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.store.upsertProplabSW(ctx, rows); err != nil {
		logInfo("SW Kp store failed: %v", err)
		return
	}
	latest := rows[len(rows)-1]
	s.mu.Lock()
	s.sw.Kp = latest.Value
	s.sw.Available = true
	s.sw.FetchedAt = latest.ObsTime
	s.mu.Unlock()
}

func (s *swIngestService) fetchF107() {
	rows, err := fetchSWPCJsonSeries(s.client, swF107URL, "F10.7", "flux", func(v string) (float64, bool) {
		f, err := strconv.ParseFloat(v, 64)
		return f, err == nil
	})
	if err != nil {
		logInfo("SW F10.7 fetch failed: %v", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.store.upsertProplabSW(ctx, rows); err != nil {
		logInfo("SW F10.7 store failed: %v", err)
		return
	}
	latest := rows[len(rows)-1]
	s.mu.Lock()
	s.sw.SFI = latest.Value
	s.sw.Available = true
	s.sw.FetchedAt = latest.ObsTime
	s.mu.Unlock()
}

func (s *swIngestService) fetchXray() {
	// NOAA GOES X-ray JSON has columns: time_tag, satellite, flux, observed_flux, xray_class.
	// The "flux" column is the current observed X-ray flux in W/m^2. We store the
	// raw flux; buildFusionSW turns it back into a class string for the UI.
	rows, err := fetchSWPCJsonSeries(s.client, swXrayURL, "xray", "flux", func(v string) (float64, bool) {
		f, err := strconv.ParseFloat(v, 64)
		return f, err == nil
	})
	if err != nil {
		logInfo("SW X-ray fetch failed: %v", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.store.upsertProplabSW(ctx, rows); err != nil {
		logInfo("SW X-ray store failed: %v", err)
		return
	}
	latest := rows[len(rows)-1]
	s.mu.Lock()
	s.sw.XrayClass = xrayClassFromFlux(latest.Value)
	s.sw.Available = true
	s.sw.FetchedAt = latest.ObsTime
	s.mu.Unlock()
}

func (s *swIngestService) fetchOvation() {
	body, err := httpGet(s.client, swOvationURL)
	if err != nil {
		logInfo("SW OVATION fetch failed: %v", err)
		return
	}
	var latest struct {
		ForecastTime string `json:"Forecast Time"`
		Aurora       []int  `json:"aurora"`
	}
	if err := json.Unmarshal(body, &latest); err != nil {
		logInfo("SW OVATION parse failed: %v", err)
		return
	}
	var sum float64
	var count int
	for _, v := range latest.Aurora {
		if v >= 0 {
			sum += float64(v)
			count++
		}
	}
	if count == 0 {
		return
	}
	gw := sum / float64(count)
	ts, _ := parseSWPCTime(latest.ForecastTime)
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
	s.mu.Lock()
	s.sw.AuroraGW = gw
	s.sw.Available = true
	s.sw.FetchedAt = ts
	s.mu.Unlock()
}

// fetchSWPCJsonSeries parses the NOAA SWPC JSON table format: first row is a
// header, each subsequent row is []string. It returns the most recent rows (one
// per distinct time tag) capped to avoid storing stale history on first fetch.
func fetchSWPCJsonSeries(client *http.Client, url, seriesName, valueCol string, parse func(string) (float64, bool)) ([]proplabSWRow, error) {
	body, err := httpGet(client, url)
	if err != nil {
		return nil, err
	}
	var table [][]string
	if err := json.Unmarshal(body, &table); err != nil {
		return nil, err
	}
	if len(table) < 2 {
		return nil, fmt.Errorf("empty table")
	}
	header := table[0]
	colIdx := -1
	timeIdx := -1
	for i, h := range header {
		switch strings.ToLower(h) {
		case strings.ToLower(valueCol):
			colIdx = i
		case "time_tag":
			timeIdx = i
		}
	}
	if colIdx < 0 {
		return nil, fmt.Errorf("value column %q not found", valueCol)
	}
	if timeIdx < 0 {
		return nil, fmt.Errorf("time_tag column not found")
	}

	rows := make([]proplabSWRow, 0, len(table)-1)
	seen := make(map[int64]bool)
	for i := len(table) - 1; i > 0; i-- {
		r := table[i]
		if len(r) <= colIdx || len(r) <= timeIdx {
			continue
		}
		ts, ok := parseSWPCTime(r[timeIdx])
		if !ok || seen[ts] {
			continue
		}
		seen[ts] = true
		v, ok := parse(r[colIdx])
		if !ok {
			continue
		}
		rows = append(rows, proplabSWRow{Series: seriesName, ObsTime: ts, Value: v})
		if len(rows) >= 10 {
			break
		}
	}
	return rows, nil
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

// parseSWPCTime accepts "2026-07-31 06:00:00.000" or ISO8601 with offset.
func parseSWPCTime(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		"2006-01-02 15:04:05.000",
		"2006-01-02T15:04:05-07:00",
		"2006-01-02T15:04:05Z",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix(), true
		}
	}
	return 0, false
}

// xrayClassFromFlux converts GOES X-ray flux in W/m^2 to a class string like "M5.2".
func xrayClassFromFlux(flux float64) string {
	if flux <= 0 {
		return ""
	}
	// NOAA classes: A < 1e-7, B 1e-7..1e-6, C 1e-6..1e-5, M 1e-5..1e-4, X >= 1e-4.
	// The magnitude within a class is flux / class_floor.
	switch {
	case flux < 1e-7:
		return fmt.Sprintf("A%.1f", flux/1e-8)
	case flux < 1e-6:
		return fmt.Sprintf("B%.1f", flux/1e-7)
	case flux < 1e-5:
		return fmt.Sprintf("C%.1f", flux/1e-6)
	case flux < 1e-4:
		return fmt.Sprintf("M%.1f", flux/1e-5)
	default:
		return fmt.Sprintf("X%.1f", flux/1e-4)
	}
}
