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

type dxClusterAccountingState struct {
	connectAttempts atomic.Int64
	connected       atomic.Int64
	linesSeen       atomic.Int64
	parsedSpots     atomic.Int64
	persistedSpots  atomic.Int64
	forwardedSpots  atomic.Int64
	droppedNoLoc    atomic.Int64
}

func (a *dxClusterAccountingState) snapshot() (attempts, connected, linesSeen, parsed, persisted, forwarded, droppedNoLoc int64) {
	attempts = a.connectAttempts.Load()
	connected = a.connected.Load()
	linesSeen = a.linesSeen.Load()
	parsed = a.parsedSpots.Load()
	persisted = a.persistedSpots.Load()
	forwarded = a.forwardedSpots.Load()
	droppedNoLoc = a.droppedNoLoc.Load()
	return
}

var dxClusterAccounting = &dxClusterAccountingState{}

type dxClusterConfig struct {
	Enabled        bool
	Endpoint       string
	ReconnectDelay time.Duration
	Verbose        bool
	Username       string
	Password       string
	Resolver       CallsignLocatorResolver
	CtyResolver    *cty.Resolver // DXCC entity + country flag (cty.dat); may be nil
}

type dxClusterSpot struct {
	Spotter      string
	DXCall       string
	FrequencyKHz float64
	Comment      string
	ObservedAt   int64
}

var dxClusterLinePattern = regexp.MustCompile(`(?i)^DX\s+de\s+([A-Z0-9/\-]+)\s*:\s*([0-9]+(?:\.[0-9]+)?)\s+([A-Z0-9/\-]+)\s+(.+)$`)

func startDXClusterIngest(cfg dxClusterConfig) {
	if !cfg.Enabled {
		return
	}

	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		endpoint = "db0erf.de:7300"
	}
	reconnectDelay := cfg.ReconnectDelay
	if reconnectDelay <= 0 {
		reconnectDelay = 15 * time.Second
	}
	if cfg.Verbose {
		logInfo("DX cluster ingest enabled: endpoint=%s reconnect_delay=%s user=%v pass=%v", endpoint, reconnectDelay, strings.TrimSpace(cfg.Username) != "", strings.TrimSpace(cfg.Password) != "")
	}

	attempt := 0
	delay := reconnectDelay
	for {
		attempt++
		dxClusterAccounting.connectAttempts.Add(1)
		if cfg.Verbose {
			logInfo("DX cluster dial attempt #%d to %s", attempt, endpoint)
		}
		sessionStart := time.Now()
		err := runDXClusterSession(endpoint, cfg)
		if err != nil {
			logInfo("DX cluster session ended (%s): %v", endpoint, err)
		}
		// A session that dies within dxClusterMinHealthySession seconds was
		// almost certainly rejected (bad login, duplicate-callsign kick,
		// server-side refusal) rather than a healthy feed that ended. Hammering
		// the cluster with an immediate retry in that state is abusive — it is
		// effectively a reconnect flood against the node — so back off
		// exponentially until we hold a healthy session again.
		if time.Since(sessionStart) < dxClusterMinHealthySession {
			delay *= 2
			if delay > dxClusterMaxReconnectDelay {
				delay = dxClusterMaxReconnectDelay
			}
			logInfo("DX cluster short-lived session (%s); backing off, next attempt in %s", endpoint, delay)
		} else {
			delay = reconnectDelay
		}
		if cfg.Verbose {
			logInfo("DX cluster reconnect scheduled in %s (%s)", delay, endpoint)
		}
		time.Sleep(delay)
	}
}

const (
	// Sessions shorter than this are treated as login/connection failures
	// that must back off, not healthy feeds that ended.
	dxClusterMinHealthySession = 60 * time.Second
	dxClusterMaxReconnectDelay = 15 * time.Minute
)

func runDXClusterSession(endpoint string, cfg dxClusterConfig) error {
	conn, err := net.DialTimeout("tcp", endpoint, 10*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()

	logInfo("DX cluster connected: %s (remote=%s)", endpoint, conn.RemoteAddr())
	dxClusterAccounting.connected.Add(1)
	username := strings.ToUpper(strings.TrimSpace(cfg.Username))
	password := strings.TrimSpace(cfg.Password)

	reader := bufio.NewReaderSize(conn, 128*1024)
	if username == "" {
		logInfo("DX cluster: no login callsign (-dxcluster-username / DXCLUSTER_USERNAME empty); most clusters (incl. DXSpider) reject anonymous logins")
	} else if err := dxClusterLogin(conn, reader, username, password, cfg.Verbose); err != nil {
		return err
	}

	// Request server-side pagination and a startup snapshot of recent DX spots.
	sendDXClusterCommand(conn, "set/page 200", cfg.Verbose)
	sendDXClusterCommand(conn, "sh/dx 150", cfg.Verbose)

	scanner := bufio.NewScanner(reader)
	buf := make([]byte, 0, 128*1024)
	scanner.Buffer(buf, 2*1024*1024)
	linesSeen := 0
	spotsParsed := 0

	for scanner.Scan() {
		linesSeen++
		dxClusterAccounting.linesSeen.Add(1)
		// Refresh a per-line idle deadline so a half-open TCP connection
		// (NAT timeout, server hang without FIN) triggers a reconnect
		// instead of blocking scanner.Scan() forever.
		_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		rawLine := scanner.Text()
		line := strings.TrimSpace(rawLine)
		if logLevel == "DEBUG" {
			logDebug("DX cluster raw line: %q", rawLine)
		}
		if line == "" {
			continue
		}
		if cfg.Verbose && linesSeen%200 == 0 {
			logInfo("DX cluster stream activity: lines=%d parsed_spots=%d", linesSeen, spotsParsed)
		}
		spot, ok := parseDXClusterSpot(line, time.Now().Unix())
		if !ok {
			continue
		}
		spotsParsed++
		dxClusterAccounting.parsedSpots.Add(1)
		handleDXClusterSpot(spot, cfg.Resolver, cfg.CtyResolver)
	}
	if err := scanner.Err(); err != nil {
		if cfg.Verbose {
			logInfo("DX cluster scanner ended with error: lines=%d parsed_spots=%d", linesSeen, spotsParsed)
		}
		return err
	}
	if cfg.Verbose {
		logInfo("DX cluster scanner ended cleanly: lines=%d parsed_spots=%d", linesSeen, spotsParsed)
	}
	return fmt.Errorf("dx cluster connection closed")
}

// dxClusterLogin performs a prompt-aware DXSpider login. DXSpider prompts are
// NOT newline-terminated (e.g. "login: "), so we scan the raw byte stream: wait
// for "login:" before sending the callsign, then wait briefly for "password:"
// (registered / individually-assigned accounts) before sending the password.
// If no password prompt arrives the call is unregistered — DB0ERF and most
// DXSpider nodes still stream DX spots — so we proceed. Read deadlines bound
// every phase so a silent server can't hang the connection.
func dxClusterLogin(conn net.Conn, reader *bufio.Reader, username, password string, verbose bool) error {
	_ = conn.SetReadDeadline(time.Now().Add(25 * time.Second))
	if err := awaitPrompt(reader, "login:"); err != nil {
		_ = conn.SetReadDeadline(time.Time{})
		return fmt.Errorf("dx cluster: never saw login prompt: %w", err)
	}
	fmt.Fprintf(conn, "%s\r\n", username)
	if verbose {
		logInfo("DX cluster: sent callsign %s at login prompt", username)
	}

	// Registered accounts are then prompted for a password. Wait briefly; absence
	// of the prompt means the call is unregistered → proceed (read-only feed).
	_ = conn.SetReadDeadline(time.Now().Add(6 * time.Second))
	perr := awaitPrompt(reader, "password:")
	_ = conn.SetReadDeadline(time.Time{})
	if perr != nil {
		if verbose {
			logInfo("DX cluster: no password prompt within 6s; proceeding (unregistered or no-password account)")
		}
		return nil
	}
	if password == "" {
		logInfo("DX cluster: server prompted for a password but none configured — sending empty (read-only). Set -dxcluster-password / DXCLUSTER_PASSWORD for your registered account.")
	} else if verbose {
		logInfo("DX cluster: sent password at password prompt")
	}
	fmt.Fprintf(conn, "%s\r\n", password)
	return nil
}

// awaitPrompt reads bytes until the (case-insensitive) accumulated tail ends
// with prompt. Prompts here are not newline-terminated.
func awaitPrompt(reader *bufio.Reader, prompt string) error {
	prompt = strings.ToLower(prompt)
	tail := make([]byte, 0, 80)
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return err
		}
		if b >= 'A' && b <= 'Z' {
			b += 32
		}
		tail = append(tail, b)
		if len(tail) > 80 {
			tail = tail[len(tail)-80:]
		}
		if strings.HasSuffix(string(tail), prompt) {
			return nil
		}
	}
}

func sendDXClusterCommand(conn net.Conn, command string, verbose bool) {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return
	}
	if verbose || logLevel == "DEBUG" {
		logInfo("DX cluster command: %s", cmd)
	}
	if _, err := fmt.Fprintf(conn, "%s\n", cmd); err != nil {
		if verbose || logLevel == "DEBUG" {
			logInfo("DX cluster command failed (%s): %v", cmd, err)
		}
	}
}

func parseDXClusterSpot(line string, observedAt int64) (dxClusterSpot, bool) {
	m := dxClusterLinePattern.FindStringSubmatch(strings.TrimSpace(line))
	if len(m) != 5 {
		return dxClusterSpot{}, false
	}

	freq, err := strconv.ParseFloat(m[2], 64)
	if err != nil || freq <= 0 {
		return dxClusterSpot{}, false
	}

	spotter := strings.ToUpper(strings.TrimSpace(m[1]))
	dxCall := strings.ToUpper(strings.TrimSpace(m[3]))
	comment := strings.TrimSpace(m[4])
	if spotter == "" || dxCall == "" {
		return dxClusterSpot{}, false
	}

	return dxClusterSpot{
		Spotter:      spotter,
		DXCall:       dxCall,
		FrequencyKHz: freq,
		Comment:      comment,
		ObservedAt:   observedAt,
	}, true
}

func handleDXClusterSpot(spot dxClusterSpot, resolver CallsignLocatorResolver, ctyResolver *cty.Resolver) {
	band := bandFromFrequencyKHz(spot.FrequencyKHz)
	spotterLocator := ""
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
		if spotterLocator == "" {
			if loc, err := resolver.LookupLocator(spot.Spotter); err == nil {
				spotterLocator = strings.ToUpper(strings.TrimSpace(loc))
				if spotterLocator == "" && logLevel == "DEBUG" {
					logDebug("DX cluster QRZ lookup returned no locator for spotter %q", spot.Spotter)
				}
			} else if logLevel == "DEBUG" {
				logDebug("DX cluster QRZ lookup failed for spotter %q: %v", spot.Spotter, err)
			}
		}

		// One lookup for the DX call yields locator + operator name (+ country as a
		// fallback when cty.dat didn't resolve one).
		if info, err := resolver.LookupInfo(spot.DXCall); err == nil {
			dxLocator = strings.ToUpper(strings.TrimSpace(info.Locator))
			dxName = strings.TrimSpace(info.Name)
			if dxCountry == "" {
				dxCountry = strings.TrimSpace(info.Country)
			}
			if dxLocator == "" && logLevel == "DEBUG" {
				logDebug("DX cluster QRZ lookup returned no locator for DX call %q", spot.DXCall)
			}
		} else if logLevel == "DEBUG" {
			logDebug("DX cluster QRZ lookup failed for DX call %q: %v", spot.DXCall, err)
		}
	}

	m := MQTTMessage{
		RP:         0,
		T:          spot.ObservedAt,
		SC:         spot.Spotter,
		SL:         spotterLocator,
		RC:         spot.DXCall,
		RL:         dxLocator,
		B:          band,
		MD:         "DXCLUSTER",
		F:          spot.FrequencyKHz,
		CM:         spot.Comment,
		OpName:     dxName,
		Country:    dxCountry,
		CountryISO: dxCountryISO,
		Source:     "dxcluster",
	}

	if dxBaseline != nil {
		freq := spot.FrequencyKHz
		dxBaseline.PersistRawSpot(m, "dxcluster", spot.Spotter, &freq, spot.Comment)
		dxClusterAccounting.persistedSpots.Add(1)
	}

	if !isDXClusterSpotUsableForLive(m) {
		dxClusterAccounting.droppedNoLoc.Add(1)
		if logLevel == "DEBUG" {
			sl := strings.TrimSpace(m.SL)
			rl := strings.TrimSpace(m.RL)
			logDebug("DX cluster spot dropped (no usable locators): dx=%s spotter=%s spotter_locator=%q dx_locator=%q", m.RC, m.SC, sl, rl)
		}
		return
	}

	if dxBaseline != nil {
		dxBaseline.Observe(m)
	}
	hub.broadcastMsg(m)
	dxClusterAccounting.forwardedSpots.Add(1)
}

func isDXClusterSpotUsableForLive(m MQTTMessage) bool {
	if strings.TrimSpace(m.B) == "" {
		return false
	}
	rl := strings.ToUpper(strings.TrimSpace(m.RL))
	// DX cluster spots only need the DX call (RX) locator for mapping.
	// Spotter locator is a nice-to-have, but not required.
	return isLocator(rl)
}

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
