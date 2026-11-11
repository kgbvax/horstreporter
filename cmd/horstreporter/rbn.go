package main

import (
	"bufio"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"horstreporter/internal/maidenhead"
)

// Reverse Beacon Network (RBN) ingest. RBN is the CW/RTTY analog of PSKReporter: a fleet
// of "skimmers" (wideband CW/RTTY receivers) publish "<dxcall> heard by <skimmer> at <dB>
// dB" spots. During CW/SSB global contests the FT8 segment that PSKReporter feeds on
// empties out, so the activity chart and live stream flatline even though the bands are
// packed; RBN keeps producing CW/RTTY spots through those windows.
//
// RBN spots land in dx_raw_spots with source_type='rbn' and a real mode (CW/RTTY),
// feeding the count-based activity chart, the live stream and prop_intel v2. The telnet
// line names both stations by call only; handleRBNSpot gives them locators: the skimmer
// from RBN's published skimmer list (rbn_skimmers.go), either end from the callsign cache
// that PSKReporter / WSPR reports and QRZ lookups fill (call_locators.go, qrz_worker.go).
// A spot reaches the live stream only with both locators; one without the DX locator is
// still persisted (it counts at the skimmer's end) and its call is queued for QRZ, so the
// station's next spot resolves. RBN's dB is on a different SNR scale
// than PSKReporter FT8 SNR (0–40 dB in a ~500 Hz CW filter vs −24..+5 in 2500 Hz), so RBN is
// deliberately NOT fed to DxBaselineEngine.Observe and is excluded from the FT8
// conditions accumulator (isNonConditionsMode in dx_conditions.go) until calibration
// curves make the scales comparable (RBN's 0–40 dB would skew the FT8 snr_tier baseline).

type rbnAccountingState struct {
	connectAttempts atomic.Int64
	connected       atomic.Int64
	linesSeen       atomic.Int64
	parsedSpots     atomic.Int64
	persistedSpots  atomic.Int64
	forwardedSpots  atomic.Int64
	droppedNoLoc    atomic.Int64

	// Locator resolution per spot (handleRBNSpot).
	skimmerFromList  atomic.Int64
	skimmerFromCache atomic.Int64
	skimmerMissing   atomic.Int64
	dxFromFeed       atomic.Int64
	dxFromQRZ        atomic.Int64
	dxNoGrid         atomic.Int64 // QRZ has no grid for the call
	dxMissing        atomic.Int64 // not known yet (queued for QRZ when configured)
}

func (a *rbnAccountingState) snapshot() (attempts, connected, linesSeen, parsed, persisted, forwarded, droppedNoLoc int64) {
	attempts = a.connectAttempts.Load()
	connected = a.connected.Load()
	linesSeen = a.linesSeen.Load()
	parsed = a.parsedSpots.Load()
	persisted = a.persistedSpots.Load()
	forwarded = a.forwardedSpots.Load()
	droppedNoLoc = a.droppedNoLoc.Load()
	return
}

var rbnAccounting = &rbnAccountingState{}

type rbnConfig struct {
	Enabled        bool
	Endpoint       string
	ReconnectDelay time.Duration
	Verbose        bool
	Callsign       string // required-in-practice: the relay prompts "enter your call" before streaming
}

type rbnSpot struct {
	Skimmer      string
	DXCall       string
	FrequencyKHz float64
	Mode         string // real over-the-air mode: CW, RTTY, PSK*, FT8, FT4, SSB, …
	DB           int    // dB above noise in the skimmer's ~500 Hz CW filter
	ObservedAt   int64
}

// rbnLinePattern parses the RBN "DX de" spot line, e.g.
//
//	DX de W3LPL-#:      14024.0  N0CALL         CW   22 dB   EN91   1234 Z
//
// capturing skimmer, frequency (kHz), DX call, mode and dB. The trailing continent/grid
// and time are ignored; ObservedAt is taken from time.Now() (the live stream is real-time). The skimmer class allows the RBN per-band "-#" / "-<n>" suffix and
// portable callsigns.
var rbnLinePattern = regexp.MustCompile(`(?i)^DX\s+de\s+([A-Z0-9\-/#]+)\s*:\s*([0-9]+(?:\.[0-9]+)?)\s+([A-Z0-9/\-]+)\s+(CW|RTTY|PSK[0-9]*|FT8|FT4|SSB|AM|FM|DIGI|JT65|JT9|JS8|FST4|WSPR|OLIVIA|CONTESTIA|HELL|MFSK[0-9]*|FSK441|MSK144|Q65|JT4)\s+(\d+)\s*dB`)

func startRBNIngest(cfg rbnConfig) {
	if !cfg.Enabled {
		return
	}

	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		endpoint = "telnet.reversebeacon.net:7000"
	}
	reconnectDelay := cfg.ReconnectDelay
	if reconnectDelay <= 0 {
		reconnectDelay = 15 * time.Second
	}
	if cfg.Verbose {
		logInfo("RBN ingest enabled: endpoint=%s reconnect_delay=%s callsign=%v", endpoint, reconnectDelay, strings.TrimSpace(cfg.Callsign) != "")
	}

	attempt := 0
	for {
		attempt++
		rbnAccounting.connectAttempts.Add(1)
		if cfg.Verbose {
			logInfo("RBN dial attempt #%d to %s", attempt, endpoint)
		}
		if err := runRBNSession(endpoint, cfg); err != nil {
			logInfo("RBN session ended (%s): %v", endpoint, err)
		}
		if cfg.Verbose {
			logInfo("RBN reconnect scheduled in %s (%s)", reconnectDelay, endpoint)
		}
		time.Sleep(reconnectDelay)
	}
}

func runRBNSession(endpoint string, cfg rbnConfig) error {
	conn, err := net.DialTimeout("tcp", endpoint, 10*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()

	logInfo("RBN connected: %s (remote=%s)", endpoint, conn.RemoteAddr())
	rbnAccounting.connected.Add(1)

	// The raw RBN relay prompts "Please enter your call:" before it streams (the prompt is
	// NOT newline-terminated, so a Scanner would only yield it at the server's ~30 s idle
	// close). Use a raw bufio.Reader byte
	// scan for the prompt, answer with the callsign, then scan for spots on the SAME reader
	// (so any spots already buffered aren't lost).
	reader := bufio.NewReaderSize(conn, 128*1024)

	callsign := strings.ToUpper(strings.TrimSpace(cfg.Callsign))
	if callsign == "" {
		// The relay will not stream without a callsign; drain the prompt so the close is
		// logged cleanly, then return — the reconnect loop will keep retrying until one is
		// configured. (Not a tight spin: each cycle costs the reconnect delay + prompt wait.)
		_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
		_ = awaitRBNPrompt(reader)
		return fmt.Errorf("rbn: relay prompts for a callsign but none configured (set -rbn-callsign / RBN_CALLSIGN)")
	}

	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	if err := awaitRBNPrompt(reader); err != nil {
		_ = conn.SetReadDeadline(time.Time{})
		return fmt.Errorf("rbn: never saw callsign prompt: %w", err)
	}
	fmt.Fprintf(conn, "%s\r\n", callsign)
	_ = conn.SetReadDeadline(time.Time{})
	if cfg.Verbose {
		logInfo("RBN: sent callsign %s at prompt", callsign)
	}

	scanner := bufio.NewScanner(reader)
	buf := make([]byte, 0, 128*1024)
	scanner.Buffer(buf, 2*1024*1024)
	linesSeen := 0
	spotsParsed := 0

	for scanner.Scan() {
		linesSeen++
		rbnAccounting.linesSeen.Add(1)
		// Refresh a per-line idle deadline so a half-open TCP connection
		// (NAT timeout, server hang without FIN) triggers a reconnect
		// instead of blocking scanner.Scan() forever.
		_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		rawLine := scanner.Text()
		line := strings.TrimSpace(rawLine)
		if logLevel == "DEBUG" {
			logDebug("RBN raw line: %q", rawLine)
		}
		if line == "" {
			continue
		}
		if cfg.Verbose && linesSeen%200 == 0 {
			logInfo("RBN stream activity: lines=%d parsed_spots=%d", linesSeen, spotsParsed)
		}
		spot, ok := parseRBNSpot(line, time.Now().Unix())
		if !ok {
			continue
		}
		spotsParsed++
		rbnAccounting.parsedSpots.Add(1)
		handleRBNSpot(spot)
	}
	if err := scanner.Err(); err != nil {
		if cfg.Verbose {
			logInfo("RBN scanner ended with error: lines=%d parsed_spots=%d", linesSeen, spotsParsed)
		}
		return err
	}
	if cfg.Verbose {
		logInfo("RBN scanner ended cleanly: lines=%d parsed_spots=%d", linesSeen, spotsParsed)
	}
	return fmt.Errorf("rbn connection closed")
}

// awaitRBNPrompt reads raw bytes until the accumulated (lowercased) tail contains the
// "your call" substring — robust to phrasing ("Please enter your call" / "enter your call:")
// and the trailing ": ". It works on the non-newline-terminated prompt stream. Bounded by the caller's read deadline.
func awaitRBNPrompt(reader *bufio.Reader) error {
	needle := "your call"
	tail := make([]byte, 0, 256)
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return err
		}
		if b >= 'A' && b <= 'Z' {
			b += 32
		}
		tail = append(tail, b)
		if len(tail) > 256 {
			tail = tail[len(tail)-256:]
		}
		if strings.Contains(string(tail), needle) {
			return nil
		}
	}
}

func parseRBNSpot(line string, observedAt int64) (rbnSpot, bool) {
	m := rbnLinePattern.FindStringSubmatch(strings.TrimSpace(line))
	if len(m) != 6 {
		return rbnSpot{}, false
	}

	freq, err := strconv.ParseFloat(m[2], 64)
	if err != nil || freq <= 0 {
		return rbnSpot{}, false
	}
	db, err := strconv.Atoi(m[5])
	if err != nil {
		return rbnSpot{}, false
	}

	skimmer := strings.ToUpper(strings.TrimSpace(m[1]))
	dxCall := strings.ToUpper(strings.TrimSpace(m[3]))
	mode := strings.ToUpper(strings.TrimSpace(m[4]))
	if skimmer == "" || dxCall == "" || mode == "" {
		return rbnSpot{}, false
	}

	return rbnSpot{
		Skimmer:      skimmer,
		DXCall:       dxCall,
		FrequencyKHz: freq,
		Mode:         mode,
		DB:           db,
		ObservedAt:   observedAt,
	}, true
}

func handleRBNSpot(spot rbnSpot) {
	band := bandFromFrequencyKHz(spot.FrequencyKHz)

	// Sender/receiver convention (shared with WSPR): SC = the
	// reporting station (skimmer/spotter), RC = the station being heard (DX). The live map
	// draws skimmer→DX paths and activity_by_bin matches callsign-targets on
	// receiver_callsign (the DX being heard).
	m := MQTTMessage{
		RP:     spot.DB,
		T:      spot.ObservedAt,
		SC:     spot.Skimmer,
		RC:     spot.DXCall,
		B:      band,
		MD:     spot.Mode,
		F:      spot.FrequencyKHz,
		Source: "rbn",
	}

	resolveRBNLocators(&m, spot)

	// Persist unconditionally with full fidelity (real frequency, mode, dB, skimmer) and
	// whichever locators resolved, so the activity chart counts the spot at the skimmer's
	// end even before the DX call resolves. RBN does NOT call DxBaselineEngine.Observe:
	// its 0–40 dB CW scale would skew the FT8-calibrated snr_tier baseline (SNR-scale
	// mismatch, see docs/horstprop.md).
	if dxBaseline != nil {
		freq := spot.FrequencyKHz
		dxBaseline.PersistRawSpot(m, "rbn", spot.Skimmer, &freq, "")
		rbnAccounting.persistedSpots.Add(1)
	}

	if !isRBNSpotUsableForLive(m) {
		rbnAccounting.droppedNoLoc.Add(1)
		if logLevel == "DEBUG" {
			logDebug("RBN spot not forwarded (locator missing): dx=%s dx_locator=%q skimmer=%s skimmer_locator=%q", m.RC, m.RL, m.SC, m.SL)
		}
		return
	}

	hub.broadcastMsg(m)
	rbnAccounting.forwardedSpots.Add(1)
}

// resolveRBNLocators sets m.SL (skimmer) and m.RL (DX) from the skimmer list
// and the callsign cache, and queues the calls neither knows for a QRZ lookup.
func resolveRBNLocators(m *MQTTMessage, spot rbnSpot) {
	now := spot.ObservedAt
	if loc, ok := rbnSkimmers.lookup(spot.Skimmer); ok {
		m.SL = loc
		rbnAccounting.skimmerFromList.Add(1)
	} else {
		base := skimmerBaseCall(strings.TrimSuffix(spot.Skimmer, "-#"))
		if loc, st := callLocators.resolve(base, now); st.ok() {
			m.SL = loc
			rbnAccounting.skimmerFromCache.Add(1)
		} else {
			rbnAccounting.skimmerMissing.Add(1)
			if st == resolveMiss {
				qrzLookups.enqueue(base)
			}
		}
	}

	loc, st := callLocators.resolve(spot.DXCall, now)
	switch st {
	case resolveFeed:
		rbnAccounting.dxFromFeed.Add(1)
	case resolveQRZ:
		rbnAccounting.dxFromQRZ.Add(1)
	case resolveNegative:
		rbnAccounting.dxNoGrid.Add(1)
	default:
		rbnAccounting.dxMissing.Add(1)
		qrzLookups.enqueue(spot.DXCall)
	}
	m.RL = loc
}

// isRBNSpotUsableForLive: a valid band and both locators, like every other
// live source (the map draws skimmer→DX paths; the area match reads either end).
func isRBNSpotUsableForLive(m MQTTMessage) bool {
	if strings.TrimSpace(m.B) == "" {
		return false
	}
	return maidenhead.IsLocator(strings.ToUpper(strings.TrimSpace(m.RL))) &&
		maidenhead.IsLocator(strings.ToUpper(strings.TrimSpace(m.SL)))
}

// bandFromFrequencyKHz maps a spot frequency in kHz to its amateur band name, or ""
// when it falls outside every band.
func bandFromFrequencyKHz(freq float64) string {
	switch {
	case freq >= 1800 && freq < 2000:
		return "160m"
	case freq >= 3500 && freq < 4000:
		return "80m"
	case freq >= 5250 && freq < 5450:
		return "60m"
	case freq >= 7000 && freq < 7300:
		return "40m"
	case freq >= 10100 && freq < 10150:
		return "30m"
	case freq >= 14000 && freq < 14350:
		return "20m"
	case freq >= 18068 && freq < 18168:
		return "17m"
	case freq >= 21000 && freq < 21450:
		return "15m"
	case freq >= 24890 && freq < 24990:
		return "12m"
	case freq >= 28000 && freq < 29700:
		return "10m"
	case freq >= 50000 && freq < 54000:
		return "6m"
	case freq >= 70000 && freq < 71000:
		return "4m"
	case freq >= 144000 && freq < 148000:
		return "2m"
	case freq >= 430000 && freq < 440000:
		return "70cm"
	default:
		return ""
	}
}
