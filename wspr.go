package main

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"horstreporter/internal/cty"
)

// wsprUserAgent identifies HorstReporter to the volunteer-run wspr.live
// service (site URL + operator callsign) so its admins can reach us.
const wsprUserAgent = "horstreporter/1.0 (+https://horstreporter.kgbvax.net; DL9ET)"

// almanacWSPRUserAgent is the same identity, tagged for the archive backfill.
const almanacWSPRUserAgent = "horstreporter/1.0 (+https://horstreporter.kgbvax.net; DL9ET; almanac backfill)"

// wspr.go ingests WSPR (Weak Signal Propagation Reporter) spots from the
// wspr.live ClickHouse HTTP interface (https://db1.wspr.live). WSPR is a
// beacon mode: low-power transmitters run continuously, so it shows whether a
// path is open at all even when nobody is actively operating (the gap FT8
// leaves on dead bands / off-peak hours / during contests).
//
// Scope (reference-only): WSPR spots land in dx_raw_spots with
// source_type='wspr' and feed the count-based activity chart and (when the
// receiver locator is valid) the live stream. WSPR is deliberately NOT fed to
// DxBaselineEngine.Observe and is excluded from the FT8 conditions
// accumulator (isNonConditionsMode) — WSPR SNR is on the same 2500 Hz scale
// as FT8, but WSPR stations transmit at wildly varying power (0.1–100 W+),
// so raw SNR conflates station capability with propagation. It is a
// propagation reference, not a scoring input (mirrors RBN's CW/RTTY handling).
//
// Reliability: wspr.live is a third-party volunteer service (no SLA, 20
// req/min limit). The poller uses a short HTTP timeout and degrades
// gracefully on failure (log + skip, never crash); the default 60s poll stays
// well under the rate limit.

type wsprAccountingState struct {
	pollAttempts   atomic.Int64
	pollFailures   atomic.Int64
	rowsSeen       atomic.Int64
	parsedSpots    atomic.Int64
	persistedSpots atomic.Int64
	forwardedSpots atomic.Int64
	droppedNoLoc   atomic.Int64
	duplicateRows  atomic.Int64
}

func (a *wsprAccountingState) snapshot() (attempts, failures, rowsSeen, parsed, persisted, forwarded, droppedNoLoc int64) {
	attempts = a.pollAttempts.Load()
	failures = a.pollFailures.Load()
	rowsSeen = a.rowsSeen.Load()
	parsed = a.parsedSpots.Load()
	persisted = a.persistedSpots.Load()
	forwarded = a.forwardedSpots.Load()
	droppedNoLoc = a.droppedNoLoc.Load()
	return
}

var wsprAccounting = &wsprAccountingState{}

// wsprLookbackSeconds is the poll query window (`time > now() - N`). Each poll
// re-reads the whole window, so consecutive polls overlap and the same row is
// returned several times; wsprSeen collapses those repeats.
const wsprLookbackSeconds = 300

// wsprSeenTTL keeps dedup keys for twice the lookback window so a row can't
// re-enter after its key is pruned while it is still inside the query window.
const wsprSeenTTL = 2 * wsprLookbackSeconds

type wsprKey struct {
	t      int64
	band   string
	rx, tx string
	freqHz int64
}

func wsprKeyFor(m MQTTMessage) wsprKey {
	return wsprKey{t: m.T, band: m.B, rx: m.SC, tx: m.RC, freqHz: int64(math.Round(m.F * 1000))}
}

// wsprSeenSet remembers recently ingested WSPR rows (first-seen unix time per
// key) so overlapping polls don't persist, count and broadcast a spot again.
type wsprSeenSet struct {
	mu sync.Mutex
	m  map[wsprKey]int64
}

var wsprSeen = &wsprSeenSet{m: make(map[wsprKey]int64)}

// markNew records m and reports whether it was not seen before.
func (s *wsprSeenSet) markNew(m MQTTMessage, now int64) bool {
	k := wsprKeyFor(m)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.m[k]; dup {
		return false
	}
	s.m[k] = now
	return true
}

// prune drops keys first seen more than wsprSeenTTL ago.
func (s *wsprSeenSet) prune(now int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, first := range s.m {
		if first < now-wsprSeenTTL {
			delete(s.m, k)
		}
	}
}

func (s *wsprSeenSet) reset() {
	s.mu.Lock()
	s.m = make(map[wsprKey]int64)
	s.mu.Unlock()
}

// seedFromHistory marks already-held WSPR history rows as seen so a restart
// (which backfills history from Postgres) doesn't re-ingest the first poll.
func (s *wsprSeenSet) seedFromHistory(history []MQTTMessage, now int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range history {
		if m.Source == "wspr" && m.T >= now-wsprSeenTTL {
			s.m[wsprKeyFor(m)] = now
		}
	}
}

type wsprConfig struct {
	Enabled     bool
	Endpoint    string
	PollSeconds int
	Verbose     bool
	Resolver    CallsignLocatorResolver
	CtyResolver *cty.Resolver
}

// wsprSpot is one row from the wspr.rx table (FORMAT JSON).
type wsprSpot struct {
	Time      string `json:"time"`      // "2006-01-02 15:04:05" UTC
	Band      int    `json:"band"`      // wspr.live band code (1=160m, 3=80m, …)
	RxSign    string `json:"rx_sign"`   // receiver callsign
	RxLoc     string `json:"rx_loc"`    // receiver Maidenhead locator
	TxSign    string `json:"tx_sign"`   // transmitter callsign
	TxLoc     string `json:"tx_loc"`    // transmitter Maidenhead locator
	Distance  int    `json:"distance"`  // km
	Frequency int64  `json:"frequency"` // Hz
	Power     int    `json:"power"`     // dBm (often unreliable)
	SNR       int    `json:"snr"`       // dB (2500 Hz reference)
}

// wsprJSONResponse is the ClickHouse FORMAT JSON envelope.
type wsprJSONResponse struct {
	Data []wsprSpot `json:"data"`
}

// bandFromWSPR maps a wspr.live band code to the HorstReporter band string.
// Returns "" for out-of-scope bands.
func bandFromWSPR(band int) string {
	switch band {
	case 1:
		return "160m"
	case 3:
		return "80m"
	case 5:
		return "60m"
	case 7:
		return "40m"
	case 10:
		return "30m"
	case 14:
		return "20m"
	case 18:
		return "17m"
	case 21:
		return "15m"
	case 24:
		return "12m"
	case 28:
		return "10m"
	case 50:
		return "6m"
	case 70:
		return "4m"
	case 144:
		return "2m"
	default:
		return ""
	}
}

func startWSPRIngest(cfg wsprConfig) {
	if !cfg.Enabled {
		return
	}
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		endpoint = "https://db1.wspr.live"
	}
	pollSeconds := cfg.PollSeconds
	if pollSeconds <= 0 {
		pollSeconds = 60
	}
	client := &http.Client{Timeout: 20 * time.Second}
	if cfg.Verbose {
		logInfo("WSPR ingest enabled: endpoint=%s poll=%ds", endpoint, pollSeconds)
	}

	// One immediate poll so the map has data right after startup.
	fetchWSPRSpots(client, endpoint, cfg)

	ticker := time.NewTicker(time.Duration(pollSeconds) * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		fetchWSPRSpots(client, endpoint, cfg)
	}
}

// fetchWSPRSpots queries the last ~300s of in-scope WSPR spots and feeds them
// through the same pipeline as RBN: persist to dx_raw_spots, then broadcast
// live-usable spots to the hub.
func fetchWSPRSpots(client *http.Client, endpoint string, cfg wsprConfig) {
	wsprAccounting.pollAttempts.Add(1)

	// Build the ClickHouse query. WSPR transmits on 2-minute even/odd cycles,
	// so a 300s (5 min) lookback catches 2-3 cycles per poll. Filter by
	// in-scope bands so the query stays fast (the DB is optimized for
	// time+band).
	bandList := "1,3,5,7,10,14,18,21,24,28,50,70,144"
	q := "SELECT time, band, rx_sign, rx_loc, tx_sign, tx_loc, distance, frequency, power, snr " +
		"FROM wspr.rx WHERE time > now() - " + strconv.Itoa(wsprLookbackSeconds) + " AND band IN (" + bandList + ") FORMAT JSON"
	u := endpoint + "/?query=" + url.QueryEscape(q)

	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		wsprAccounting.pollFailures.Add(1)
		logInfo("WSPR request build failed: %v", err)
		return
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", wsprUserAgent)
	res, err := client.Do(req)
	if err != nil {
		wsprAccounting.pollFailures.Add(1)
		logInfo("WSPR fetch failed: %v", err)
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		wsprAccounting.pollFailures.Add(1)
		logInfo("WSPR fetch failed: HTTP %d", res.StatusCode)
		return
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		wsprAccounting.pollFailures.Add(1)
		logInfo("WSPR read failed: %v", err)
		return
	}

	var resp wsprJSONResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		wsprAccounting.pollFailures.Add(1)
		logInfo("WSPR JSON parse failed: %v", err)
		return
	}

	now := time.Now().Unix()
	wsprSeen.prune(now)
	for _, s := range resp.Data {
		wsprAccounting.rowsSeen.Add(1)
		m, ok := wsprMessageFromRow(s, now)
		if !ok {
			continue
		}
		if !wsprSeen.markNew(m, now) {
			wsprAccounting.duplicateRows.Add(1)
			continue
		}
		ingestWSPRMessage(m)
	}
	if cfg.Verbose && len(resp.Data) > 0 {
		logInfo("WSPR poll: %d spots", len(resp.Data))
	}
}

// handleWSPRSpot converts a wspr.rx row into an MQTTMessage and feeds it
// through persist + live broadcast, mirroring handleRBNSpot. It does not
// dedup; the poller does that via wsprSeen before calling ingestWSPRMessage.
func handleWSPRSpot(s wsprSpot, now int64, cfg wsprConfig) {
	if m, ok := wsprMessageFromRow(s, now); ok {
		ingestWSPRMessage(m)
	}
}

// wsprMessageFromRow converts a wspr.rx row into an MQTTMessage. It reports
// false for out-of-scope bands and rows missing a callsign.
func wsprMessageFromRow(s wsprSpot, now int64) (MQTTMessage, bool) {
	band := bandFromWSPR(s.Band)
	if band == "" {
		return MQTTMessage{}, false
	}
	ts, err := parseWSPRTime(s.Time)
	if err != nil || ts <= 0 {
		ts = now
	}

	// Sender/receiver convention matches RBN/DX-cluster: SC = the reporting
	// station (receiver), RC = the station being heard (transmitter). The
	// live map draws receiver→transmitter paths; activity_by_bin matches
	// callsign-targets on receiver_callsign (the transmitter being heard).
	m := MQTTMessage{
		RP:      s.SNR,
		T:       ts,
		SC:      strings.ToUpper(strings.TrimSpace(s.RxSign)),
		SL:      strings.ToUpper(strings.TrimSpace(s.RxLoc)),
		RC:      strings.ToUpper(strings.TrimSpace(s.TxSign)),
		RL:      strings.ToUpper(strings.TrimSpace(s.TxLoc)),
		B:       band,
		MD:      "WSPR",
		F:       float64(s.Frequency) / 1000.0, // Hz → kHz
		Source:  "wspr",
		TXPower: s.Power,
	}
	if m.SC == "" || m.RC == "" {
		return MQTTMessage{}, false
	}
	return m, true
}

// ingestWSPRMessage persists a WSPR spot and, when it has a usable locator,
// broadcasts it to clients whose area it touches.
func ingestWSPRMessage(m MQTTMessage) {
	wsprAccounting.parsedSpots.Add(1)

	// Persist unconditionally so the count-based activity chart benefits even
	// when QRZ is off. WSPR does NOT call DxBaselineEngine.Observe: its SNR is
	// on the same scale as FT8 but power varies wildly, so it would skew the
	// FT8-calibrated snr_tier baseline (reference-only, like RBN).
	if dxBaseline != nil {
		freq := m.F
		dxBaseline.PersistRawSpot(m, "wspr", m.SC, &freq, "")
		wsprAccounting.persistedSpots.Add(1)
	}

	// Feed the WSPR climatology accumulator (band × region × slot-of-day).
	// This is the WSPR-native typical reference for atypical-surge detection,
	// parallel to the FT8 dx_region_baseline_daily but keyed by WSPR spots.
	if wsprClimatology != nil {
		wsprClimatology.Observe(m)
	}

	if !isWSPRSpotUsableForLive(m) {
		wsprAccounting.droppedNoLoc.Add(1)
		if logLevel == "DEBUG" {
			logDebug("WSPR spot dropped (no usable receiver locator): tx=%s rx=%s rx_loc=%q", m.RC, m.SC, m.RL)
		}
		return
	}

	// WSPR is scoped like every other source: only clients whose QTH/area
	// includes the transmitter or receiver get it (from-your-QTH only), so the
	// live stream matches the history dump.
	hub.broadcastMsg(m)
	wsprAccounting.forwardedSpots.Add(1)
}

// isWSPRSpotUsableForLive mirrors isRBNSpotUsableForLive: a valid band and a
// resolved receiver locator are enough for the live map.
func isWSPRSpotUsableForLive(m MQTTMessage) bool {
	if strings.TrimSpace(m.B) == "" {
		return false
	}
	return isLocator(strings.ToUpper(strings.TrimSpace(m.RL)))
}

// parseWSPRTime parses the ClickHouse DateTime string "2006-01-02 15:04:05"
// (UTC) into a Unix timestamp.
func parseWSPRTime(s string) (int64, error) {
	t, err := time.Parse("2006-01-02 15:04:05", strings.TrimSpace(s))
	if err != nil {
		return 0, err
	}
	return t.Unix(), nil
}
