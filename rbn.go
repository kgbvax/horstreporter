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

	"horstreporter/internal/cty"
)

// Reverse Beacon Network (RBN) ingest. RBN is the CW/RTTY analog of PSKReporter: a fleet
// of "skimmers" (wideband CW/RTTY receivers) publish "<dxcall> heard by <skimmer> at <dB>
// dB" spots. During CW/SSB global contests the FT8 segment that PSKReporter feeds on
// empties out, so the activity chart and live stream flatline even though the bands are
// packed; RBN keeps producing CW/RTTY spots through those windows.
//
// Minimal scope (see docs/horstprop.md): RBN spots land in dx_raw_spots with
// source_type='rbn' and a real mode (CW/RTTY), feeding the count-based activity chart and
// (when QRZ resolves a DX locator) the live stream. RBN's dB is on a different SNR scale
// than PSKReporter FT8 SNR (0–40 dB in a ~500 Hz CW filter vs −24..+5 in 2500 Hz), so RBN is
// deliberately NOT fed to DxBaselineEngine.Observe and is excluded from the FT8
// conditions accumulator (isNonConditionsMode in dx_conditions.go) until calibration
// curves make the scales comparable. This mirrors DX-cluster's optional-QRZ shape but,
// unlike DX-cluster, skips Observe (DX-cluster RP=0 is baseline-benign; RBN's 0–40 dB
// would skew the FT8 snr_tier baseline).

type rbnAccountingState struct {
	connectAttempts atomic.Int64
	connected       atomic.Int64
	linesSeen       atomic.Int64
	parsedSpots     atomic.Int64
	persistedSpots  atomic.Int64
	forwardedSpots  atomic.Int64
	droppedNoLoc    atomic.Int64
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
	Resolver       CallsignLocatorResolver
	CtyResolver    *cty.Resolver // DXCC entity + country flag (cty.dat); may be nil
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
// and time are ignored; ObservedAt is taken from time.Now() (the live stream is real-time,
// matching DX-cluster). The skimmer class allows the RBN per-band "-#" / "-<n>" suffix and
// portable callsigns.
var rbnLinePattern = regexp.MustCompile(`(?i)^DX\s+de\s+([A-Z0-9\-/#]+)\s*:\s*([0-9]+(?:\.[0-9]+)?)\s+([A-Z0-9/\-]+)\s+(CW|RTTY|PSK[0-9]*|FT8|FT4|SSB|AM|FM|DIGI)\s+(\d+)\s*dB`)

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
	// close — same shape as the DX-cluster "login:" prompt). Use a raw bufio.Reader byte
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
		handleRBNSpot(spot, cfg.Resolver, cfg.CtyResolver)
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
// and the trailing ": ". Like dxcluster.go's awaitPrompt it works on the non-newline-
// terminated prompt stream. Bounded by the caller's read deadline.
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

func handleRBNSpot(spot rbnSpot, resolver CallsignLocatorResolver, ctyResolver *cty.Resolver) {
	band := bandFromFrequencyKHz(spot.FrequencyKHz)
	skimmerLocator := ""
	dxLocator := ""
	dxName := ""
	dxCountry := ""
	dxCountryISO := ""

	// Country + flag from cty.dat (authoritative, full prefix coverage).
	if ctyResolver != nil {
		if ent, iso, ok := ctyResolver.Resolve(spot.DXCall); ok {
			dxCountry = ent.Name
			dxCountryISO = iso
		}
	}

	if resolver != nil {
		// Skimmer locator is best-effort; not required for the live path.
		if loc, err := resolver.LookupLocator(spot.Skimmer); err == nil {
			skimmerLocator = strings.ToUpper(strings.TrimSpace(loc))
		} else if logLevel == "DEBUG" {
			logDebug("RBN QRZ lookup failed for skimmer %q: %v", spot.Skimmer, err)
		}

		// One lookup for the DX call yields locator + operator name (+ country as a
		// fallback when cty.dat didn't resolve one). The DX/RX locator is what makes a
		// spot usable for the live map (isRBNSpotUsableForLive), mirroring DX-cluster.
		if info, err := resolver.LookupInfo(spot.DXCall); err == nil {
			dxLocator = strings.ToUpper(strings.TrimSpace(info.Locator))
			dxName = strings.TrimSpace(info.Name)
			if dxCountry == "" {
				dxCountry = strings.TrimSpace(info.Country)
			}
			if dxLocator == "" && logLevel == "DEBUG" {
				logDebug("RBN QRZ lookup returned no locator for DX call %q", spot.DXCall)
			}
		} else if logLevel == "DEBUG" {
			logDebug("RBN QRZ lookup failed for DX call %q: %v", spot.DXCall, err)
		}
	}

	// Sender/receiver convention matches DX-cluster (dxcluster.go:304-318): SC = the
	// reporting station (skimmer/spotter), RC = the station being heard (DX). The live map
	// draws skimmer→DX paths and activity_by_bin matches callsign-targets on
	// receiver_callsign (the DX being heard).
	m := MQTTMessage{
		RP:         spot.DB,
		T:          spot.ObservedAt,
		SC:         spot.Skimmer,
		SL:         skimmerLocator,
		RC:         spot.DXCall,
		RL:         dxLocator,
		B:          band,
		MD:         spot.Mode,
		F:          spot.FrequencyKHz,
		OpName:     dxName,
		Country:    dxCountry,
		CountryISO: dxCountryISO,
		Source:     "rbn",
	}

	// Persist unconditionally with full fidelity (real frequency, mode, dB, skimmer) so the
	// count-based activity chart (activity_by_bin, which reads dx_raw_spots regardless of
	// source_type) benefits even when QRZ is off. RBN does NOT call DxBaselineEngine.Observe:
	// its 0–40 dB CW scale would skew the FT8-calibrated snr_tier baseline (SNR-scale
	// mismatch, see docs/horstprop.md). This is the deliberate asymmetry vs DX-cluster,
	// whose RP=0 is baseline-benign.
	if dxBaseline != nil {
		freq := spot.FrequencyKHz
		dxBaseline.PersistRawSpot(m, "rbn", spot.Skimmer, &freq, "")
		rbnAccounting.persistedSpots.Add(1)
	}

	if !isRBNSpotUsableForLive(m) {
		rbnAccounting.droppedNoLoc.Add(1)
		if logLevel == "DEBUG" {
			rl := strings.TrimSpace(m.RL)
			logDebug("RBN spot dropped (no usable DX locator): dx=%s skimmer=%s dx_locator=%q", m.RC, m.SC, rl)
		}
		return
	}

	hub.broadcastMsg(m)
	rbnAccounting.forwardedSpots.Add(1)
}

// isRBNSpotUsableForLive mirrors isDXClusterSpotUsableForLive: a valid band and a resolved
// DX/RX locator are enough for the live map (the skimmer locator is optional).
func isRBNSpotUsableForLive(m MQTTMessage) bool {
	if strings.TrimSpace(m.B) == "" {
		return false
	}
	return isLocator(strings.ToUpper(strings.TrimSpace(m.RL)))
}
