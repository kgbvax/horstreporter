package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"horstreporter/internal/awards"
	"horstreporter/internal/dotenv"
)

// defaultWavelogURL is the dclnext (DARC) instance; override via WAVELOG_URL.
const defaultWavelogURL = "https://log.dclnext.darc.de/index.php"

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// envOr / envIntOr / envBoolOr / envFloatOr resolve a flag default from the
// environment (so the .env edited via the tray Settings page drives config),
// falling back to a built-in default. An explicit command-line flag still wins
// because flag defaults are only used when the flag is omitted.
func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envIntOr(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBoolOr(key string, def bool) bool {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func envFloatOr(key string, def float64) float64 {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

// .env loading is shared with horstawards via internal/dotenv.Load.

const (
	pstQueryAzCommand   = "<PST>AZ?</PST>"
	pstQueryModeCommand = "<PST>MODE?</PST>"
	normalPollInterval  = 3 * time.Second
	fastPollInterval    = normalPollInterval / 6
)

var (
	statusAzimuthRegex = regexp.MustCompile(`(?i)(?:azimuth|heading|az)\s*[:=]\s*([-+]?\d+(?:\.\d+)?)`)
	numberTokenRegex   = regexp.MustCompile(`[-+]?\d+(?:\.\d+)?`)
)

type stationConfig struct {
	Name    string
	Lat     float64
	Lng     float64
	Locator string
}

type serviceConfig struct {
	ListenAddr       string
	BackendBaseURL   string
	ControlPermitted bool
	Beamwidth3dBDeg  float64
	AllowedModes     []string
	UDPLogTraffic    bool
	UDPLogHex        bool
	UDPLogMaxBytes   int
	Timeout          time.Duration
	PSTHost          string
	PSTPort          int
	Station          stationConfig
	RigTransport     string        // none | waveloggate | log4om
	RigWaveLogGate   string        // WaveLogGate callback base URL
	RigLog4OM        string        // Log4OM Remote Control inbound UDP host:port
	WavelogURL       string        // Wavelog base URL (e.g. https://log.dclnext.darc.de/index.php)
	WavelogAPIKey    string        // Wavelog read API key (from env; never logged)
	Awards           awards.Config // local award-progress engine (in-process; logs stay local)
	EnvFile          string        // absolute path to the .env the Settings page reads/writes
	TaskName         string        // Windows scheduled task name, for clean restart-to-apply (optional)
	LogFile          string        // absolute path of the log file, when logging to a file ("" = stderr)
	DebugExternal    bool          // verbose logging of all external traffic (HTTP + PSTrotator UDP)

	// UltraBeam RCU-06 antenna controller (MQTT). Beam direction only; rotation
	// stays with PSTrotator. Disabled unless UBEnabled && UBBrokerURL is set.
	UBEnabled     bool
	UBBrokerURL   string // e.g. tcp://127.0.0.1:1883 (tls:// for an authenticated remote broker)
	UBClientID    string // must differ from ubctrl's own id; empty = auto horstoperator-<nanos>
	UBTopicPrefix string // default "ubctrl"
	UBUsername    string
	UBPassword    string // from env only (UB_PASSWORD); never logged
}

type rotatorState struct {
	AzimuthDeg float64
	Mode       string
}

type pstUDPClient struct {
	cfg                  serviceConfig
	mu                   sync.Mutex
	modeMu               sync.Mutex
	lastKnownMode        string
	lastModeQueryAttempt time.Time
	modeQueryBackoff     time.Duration // current retry interval after a failure; 0 = healthy (base interval)
}

const (
	modeQueryBaseInterval = 30 * time.Second
	modeQueryMaxInterval  = 10 * time.Minute
)

func newPSTUDPClient(cfg serviceConfig) *pstUDPClient {
	return &pstUDPClient{cfg: cfg, lastKnownMode: "forward"}
}

func (c *pstUDPClient) endpoint() string {
	return net.JoinHostPort(c.cfg.PSTHost, strconv.Itoa(c.cfg.PSTPort))
}

func (c *pstUDPClient) maybeFinalizeCommand(cmd string) string {
	base := strings.TrimRight(cmd, "\r\n")
	return base + "\r"
}

func (c *pstUDPClient) replyPort() int {
	return c.cfg.PSTPort + 1
}

func (c *pstUDPClient) getKnownMode() string {
	c.modeMu.Lock()
	defer c.modeMu.Unlock()
	if c.lastKnownMode == "" {
		return "forward"
	}
	return c.lastKnownMode
}

func (c *pstUDPClient) setKnownMode(mode string) {
	c.modeMu.Lock()
	defer c.modeMu.Unlock()
	c.lastKnownMode = normalizeMode(mode)
}

func (c *pstUDPClient) shouldQueryModeNow() bool {
	c.modeMu.Lock()
	defer c.modeMu.Unlock()
	interval := c.modeQueryBackoff
	if interval == 0 {
		interval = modeQueryBaseInterval
	}
	if c.lastModeQueryAttempt.IsZero() || time.Since(c.lastModeQueryAttempt) >= interval {
		c.lastModeQueryAttempt = time.Now()
		return true
	}
	return false
}

// backoffModeQuery widens the MODE? retry interval after a failure instead of
// giving up entirely, so polling self-heals once the controller answers again
// while still avoiding tight-loop UDP spam.
func (c *pstUDPClient) backoffModeQuery(reason error) {
	c.modeMu.Lock()
	if c.modeQueryBackoff == 0 {
		c.modeQueryBackoff = modeQueryBaseInterval
	} else {
		c.modeQueryBackoff *= 2
		if c.modeQueryBackoff > modeQueryMaxInterval {
			c.modeQueryBackoff = modeQueryMaxInterval
		}
	}
	next := c.modeQueryBackoff
	c.modeMu.Unlock()
	log.Printf("[INFO] MODE? query failed; retrying in %s (will keep trying): %v", next, reason)
}

// resetModeQueryBackoff returns to the base polling cadence after a reply is
// received, recovering from any prior backoff.
func (c *pstUDPClient) resetModeQueryBackoff() {
	c.modeMu.Lock()
	wasBackedOff := c.modeQueryBackoff != 0
	c.modeQueryBackoff = 0
	c.modeMu.Unlock()
	if wasBackedOff {
		log.Printf("[INFO] MODE? query recovered; resuming normal polling cadence")
	}
}

func (c *pstUDPClient) logUDP(direction string, operation string, payload string, remoteEndpoint string, started time.Time) {
	if !c.cfg.UDPLogTraffic {
		return
	}

	maxLen := c.cfg.UDPLogMaxBytes
	if maxLen <= 0 {
		maxLen = 256
	}

	raw := []byte(payload)
	previewBytes := raw
	truncated := false
	if len(previewBytes) > maxLen {
		previewBytes = previewBytes[:maxLen]
		truncated = true
	}

	preview := strconv.QuoteToASCII(string(previewBytes))
	if truncated {
		preview += "..."
	}

	if c.cfg.UDPLogHex {
		hexView := hex.EncodeToString(previewBytes)
		if truncated {
			hexView += "..."
		}
		log.Printf("[UDP][%s][%s] endpoint=%s bytes=%d elapsed=%s ascii=%s hex=%s", direction, operation, remoteEndpoint, len(raw), time.Since(started).Round(time.Millisecond), preview, hexView)
		return
	}

	log.Printf("[UDP][%s][%s] endpoint=%s bytes=%d elapsed=%s payload=%s", direction, operation, remoteEndpoint, len(raw), time.Since(started).Round(time.Millisecond), preview)
}

func (c *pstUDPClient) sendAndMaybeReceive(ctx context.Context, operation string, cmd string, waitReply bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	start := time.Now()
	payload := c.maybeFinalizeCommand(cmd)
	c.logUDP("tx", operation, payload, c.endpoint(), start)

	if !waitReply {
		d := net.Dialer{Timeout: c.cfg.Timeout}
		conn, err := d.DialContext(ctx, "udp", c.endpoint())
		if err != nil {
			return "", fmt.Errorf("failed to dial PSTrotator UDP endpoint %s: %w", c.endpoint(), err)
		}
		defer conn.Close()

		if deadline, ok := ctx.Deadline(); ok {
			_ = conn.SetDeadline(deadline)
		} else {
			_ = conn.SetDeadline(time.Now().Add(c.cfg.Timeout))
		}

		if _, err := conn.Write([]byte(payload)); err != nil {
			return "", fmt.Errorf("failed to write UDP command: %w", err)
		}
		return "", nil
	}

	// Bind IPv4 explicitly ("udp4", not "udp"): PSTrotator sends its reply as an
	// IPv4 broadcast (e.g. 192.168.1.255:replyPort). A dual-stack IPv6 wildcard
	// socket ([::]) receives IPv4 unicast but NOT IPv4 broadcast, so "udp" here
	// silently drops the reply and every read times out.
	listenerAddr := &net.UDPAddr{IP: net.IPv4zero, Port: c.replyPort()}
	listener, err := net.ListenUDP("udp4", listenerAddr)
	if err != nil {
		return "", fmt.Errorf("failed to bind UDP reply listener on port %d: %w", c.replyPort(), err)
	}
	defer listener.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = listener.SetReadDeadline(deadline)
	} else {
		_ = listener.SetReadDeadline(time.Now().Add(c.cfg.Timeout))
	}

	remoteAddr, err := net.ResolveUDPAddr("udp", c.endpoint())
	if err != nil {
		return "", fmt.Errorf("failed resolving PSTrotator UDP endpoint %s: %w", c.endpoint(), err)
	}

	sendConn, err := net.DialUDP("udp", nil, remoteAddr)
	if err != nil {
		return "", fmt.Errorf("failed to dial PSTrotator UDP endpoint %s: %w", c.endpoint(), err)
	}
	defer sendConn.Close()

	if _, err := sendConn.Write([]byte(payload)); err != nil {
		return "", fmt.Errorf("failed to write UDP command: %w", err)
	}

	buf := make([]byte, 2048)
	n, src, err := listener.ReadFromUDP(buf)
	if err != nil {
		return "", fmt.Errorf("failed reading UDP reply on port %d: %w", c.replyPort(), err)
	}
	reply := strings.TrimSpace(string(buf[:n]))
	remote := c.endpoint()
	if src != nil {
		remote = src.String()
	}
	c.logUDP("rx", operation, reply, remote, start)
	return reply, nil
}

func (c *pstUDPClient) GetState(ctx context.Context) (rotatorState, error) {
	azReply, err := c.sendAndMaybeReceive(ctx, "status-az", pstQueryAzCommand, true)
	if err != nil {
		return rotatorState{}, err
	}

	az, err := parseAzimuth(azReply)
	if err != nil {
		return rotatorState{}, fmt.Errorf("could not parse azimuth from PSTrotator response %q: %w", azReply, err)
	}

	mode := c.getKnownMode()
	if c.shouldQueryModeNow() {
		modeReply, modeErr := c.sendAndMaybeReceive(ctx, "status-mode", pstQueryModeCommand, true)
		if modeErr == nil {
			// Controller answered: clear any backoff so polling stays at cadence.
			c.resetModeQueryBackoff()
			if parsed := parseMode(modeReply); parsed != "" {
				mode = parsed
				c.setKnownMode(parsed)
			}
		} else {
			// Many controllers do not answer MODE? reliably; back off and retry
			// rather than disabling forever, so it recovers once fixed.
			c.backoffModeQuery(modeErr)
		}
	}

	return rotatorState{AzimuthDeg: az, Mode: normalizeMode(mode)}, nil
}

func buildRotateCommand(azimuthDeg float64) string {
	return fmt.Sprintf("<PST><TRACK>0</TRACK><AZIMUTH>%.1f</AZIMUTH></PST>", normalizeAzimuth(azimuthDeg))
}

func buildModeCommand(mode string) string {
	clean := normalizeMode(mode)
	if clean == "bidirectional" {
		return "<PST><TRACK>1</TRACK></PST>"
	}
	return "<PST><TRACK>0</TRACK></PST>"
}

func (c *pstUDPClient) RotateToAzimuth(ctx context.Context, azimuthDeg float64) error {
	cmd := buildRotateCommand(azimuthDeg)
	_, err := c.sendAndMaybeReceive(ctx, "rotate", cmd, false)
	return err
}

func (c *pstUDPClient) SetMode(ctx context.Context, mode string) error {
	cmd := buildModeCommand(mode)
	_, err := c.sendAndMaybeReceive(ctx, "mode", cmd, false)
	if err == nil {
		c.setKnownMode(mode)
	}
	return err
}

func parseAzimuth(reply string) (float64, error) {
	m := statusAzimuthRegex.FindStringSubmatch(reply)
	if len(m) > 1 {
		v, err := strconv.ParseFloat(strings.TrimSpace(m[1]), 64)
		if err == nil {
			return normalizeAzimuth(v), nil
		}
	}

	n := numberTokenRegex.FindString(reply)
	if n == "" {
		return 0, errors.New("no numeric token found")
	}
	v, err := strconv.ParseFloat(n, 64)
	if err != nil {
		return 0, err
	}
	return normalizeAzimuth(v), nil
}

func parseMode(reply string) string {
	lower := strings.ToLower(strings.TrimSpace(reply))
	if strings.Contains(lower, "mode:1") {
		return "bidirectional"
	}
	if strings.Contains(lower, "mode:0") {
		return "forward"
	}

	switch {
	case strings.Contains(lower, "bidirectional") || strings.Contains(lower, "bi-directional"):
		return "bidirectional"
	case strings.Contains(lower, "backward") || strings.Contains(lower, "reverse"):
		return "backward"
	case strings.Contains(lower, "forward"):
		return "forward"
	default:
		return ""
	}
}

func normalizeAzimuth(v float64) float64 {
	if !isFinite(v) {
		return 0
	}
	x := math.Mod(v, 360)
	if x < 0 {
		x += 360
	}
	return x
}

func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func normalizeMode(mode string) string {
	m := strings.ToLower(strings.TrimSpace(mode))
	switch m {
	case "back", "reverse", "backward":
		return "backward"
	case "bi", "bidirectional", "bi-directional":
		return "bidirectional"
	default:
		return "forward"
	}
}

func splitModes(raw string) []string {
	parts := strings.Split(raw, ",")
	seen := map[string]struct{}{}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		m := normalizeMode(part)
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	if len(out) == 0 {
		return []string{"forward", "backward", "bidirectional"}
	}
	return out
}

func hasMode(modes []string, want string) bool {
	w := normalizeMode(want)
	for _, m := range modes {
		if normalizeMode(m) == w {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("[ERROR] failed to write JSON response: %v", err)
	}
}

func decodeJSON[T any](r *http.Request, out *T) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 256*1024))
	if err := dec.Decode(out); err != nil {
		return err
	}
	return nil
}

type server struct {
	cfg     serviceConfig
	client  *pstUDPClient
	rig     rigController   // nil when -rig-transport=none
	wavelog *wavelogClient  // nil when WAVELOG_API_KEY is unset
	awards  *awards.Manager // nil when no award source is configured
	ub      beamController  // nil when UltraBeam control is disabled

	pollMu    sync.RWMutex
	pollState polledAntennaState
}

type polledAntennaState struct {
	state             rotatorState
	lastUpdated       time.Time
	lastErr           error
	fastPollingActive bool
	targetAzimuthDeg  float64
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = "*"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-HorstReporter-Opmode")
		w.Header().Set("Access-Control-Max-Age", "600")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func newServer(cfg serviceConfig) *server {
	s := &server{cfg: cfg, client: newPSTUDPClient(cfg)}
	switch strings.ToLower(strings.TrimSpace(cfg.RigTransport)) {
	case "", "none":
		// rig control disabled
	case "waveloggate":
		s.rig = newWaveLogGateBackend(cfg.RigWaveLogGate)
	case "log4om":
		s.rig = newLog4OMBackend(cfg.RigLog4OM)
	default:
		log.Printf("[WARN] unknown -rig-transport %q; rig control disabled", cfg.RigTransport)
	}
	if strings.TrimSpace(cfg.WavelogAPIKey) != "" {
		s.wavelog = newWavelogClient(cfg.WavelogURL, cfg.WavelogAPIKey)
	}
	if cfg.Awards.Enabled() {
		if mgr, err := awards.New(cfg.Awards); err != nil {
			log.Printf("[WARN] awards engine disabled: %v", err)
		} else {
			s.awards = mgr
			mgr.Run(context.Background())
			log.Printf("[INFO] awards engine enabled (sources: %s)", mgr.SourceLabel())
		}
	}
	if cfg.UBEnabled && cfg.UBBrokerURL != "" {
		s.ub = newUltrabeamClient(cfg)
		log.Printf("[INFO] UltraBeam control enabled (broker: %s, prefix: %s)", cfg.UBBrokerURL, firstNonEmpty(cfg.UBTopicPrefix, defaultUltrabeamTopicPrefix))
	}
	s.startAntennaPoller()
	return s
}

func angularDistanceDeg(a, b float64) float64 {
	delta := math.Mod(math.Abs(normalizeAzimuth(a)-normalizeAzimuth(b)), 360)
	if delta > 180 {
		return 360 - delta
	}
	return delta
}

func withinTenPercentOfTarget(currentAzimuthDeg, targetAzimuthDeg float64) bool {
	target := normalizeAzimuth(targetAzimuthDeg)
	current := normalizeAzimuth(currentAzimuthDeg)
	toleranceDeg := math.Abs(target) * 0.10
	return angularDistanceDeg(current, target) <= toleranceDeg
}

func (s *server) currentPollInterval() time.Duration {
	s.pollMu.RLock()
	fast := s.pollState.fastPollingActive
	s.pollMu.RUnlock()
	if fast {
		return fastPollInterval
	}
	return normalPollInterval
}

func (s *server) updatePollState(state rotatorState, err error) {
	s.pollMu.Lock()
	defer s.pollMu.Unlock()

	if err != nil {
		s.pollState.lastErr = err
		return
	}

	s.pollState.state = state
	s.pollState.lastUpdated = time.Now()
	s.pollState.lastErr = nil

	if s.pollState.fastPollingActive {
		if withinTenPercentOfTarget(state.AzimuthDeg, s.pollState.targetAzimuthDeg) {
			s.pollState.fastPollingActive = false
			log.Printf("[INFO] Fast antenna polling disabled: heading %.1f° is within ±10%% of target %.1f°", state.AzimuthDeg, s.pollState.targetAzimuthDeg)
		}
	}
}

func (s *server) startAntennaPoller() {
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Timeout)
			state, err := s.client.GetState(ctx)
			cancel()

			s.updatePollState(state, err)
			if err != nil {
				log.Printf("[WARN] Antenna poll failed: %v", err)
			}

			time.Sleep(s.currentPollInterval())
		}
	}()
}

func (s *server) beginFastPollingForTarget(targetAzimuthDeg float64) {
	target := normalizeAzimuth(targetAzimuthDeg)
	s.pollMu.Lock()
	s.pollState.targetAzimuthDeg = target
	s.pollState.fastPollingActive = true
	s.pollMu.Unlock()
	log.Printf("[INFO] Fast antenna polling enabled: target %.1f° (interval=%s)", target, fastPollInterval)
}

// Health reports whether the antenna poller is currently succeeding, with a
// short human-readable detail string. The tray UI uses this to pick its icon
// (green vs. red) and tooltip; it never blocks on the rotator.
func (s *server) Health() (ok bool, detail string) {
	s.pollMu.RLock()
	defer s.pollMu.RUnlock()
	if s.pollState.lastUpdated.IsZero() {
		if s.pollState.lastErr != nil {
			return false, "starting: " + s.pollState.lastErr.Error()
		}
		return false, "starting…"
	}
	if s.pollState.lastErr != nil {
		return false, s.pollState.lastErr.Error()
	}
	return true, fmt.Sprintf("PSTrotator OK — az %.0f° %s", s.pollState.state.AzimuthDeg, s.pollState.state.Mode)
}

func (s *server) getPolledStateOrFallback(ctx context.Context) (rotatorState, error) {
	s.pollMu.RLock()
	hasState := !s.pollState.lastUpdated.IsZero()
	state := s.pollState.state
	err := s.pollState.lastErr
	s.pollMu.RUnlock()

	if hasState && err == nil {
		return state, nil
	}

	fallbackState, fallbackErr := s.client.GetState(ctx)
	s.updatePollState(fallbackState, fallbackErr)
	if fallbackErr != nil {
		if err != nil {
			return rotatorState{}, err
		}
		return rotatorState{}, fallbackErr
	}
	return fallbackState, nil
}

func (s *server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/v1/status", s.handleStatus)
	mux.HandleFunc("/v1/station", s.handleStation)
	mux.HandleFunc("/v1/antenna/state", s.handleAntennaState)
	mux.HandleFunc("/v1/antenna/rotate", s.handleRotate)
	mux.HandleFunc("/v1/antenna/mode", s.handleMode)
	mux.HandleFunc("/v1/rig/tune", s.handleRigTune)
	mux.HandleFunc("/v1/operate", s.handleOperate)
	mux.HandleFunc("/v1/operate/enrich", s.handleEnrich)
	mux.HandleFunc("/v1/config", s.handleConfig)
	mux.HandleFunc("/v1/pst/test", s.handlePSTTest)
	mux.HandleFunc("/v1/diagnostics", s.handleDiagnostics)
	mux.HandleFunc("/v1/restart", s.handleRestart)
	mux.HandleFunc("/config", s.handleConfigPage)
	mux.Handle("/", s.newBackendProxyHandler())
}

func (s *server) newBackendProxyHandler() http.Handler {
	backendBase := strings.TrimSpace(s.cfg.BackendBaseURL)
	if backendBase == "" {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error": "backend proxy is disabled; set -backend-url to enable forwarding for non-/v1 routes",
			})
		})
	}

	u, err := url.Parse(backendBase)
	if err != nil {
		log.Fatalf("[FATAL] invalid backend-url %q: %v", backendBase, err)
	}

	proxy := httputil.NewSingleHostReverseProxy(u)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Header.Set("X-HorstOperator-Agent", "1")
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": fmt.Sprintf("backend proxy request failed: %v", err),
		})
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
			return
		}
		proxy.ServeHTTP(w, r)
	})
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}

	capabilities := map[string]any{
		"control":      s.cfg.ControlPermitted,
		"mode_control": s.cfg.ControlPermitted,
		"modes":        s.cfg.AllowedModes,
	}
	if s.rig != nil {
		c := s.rig.Capabilities()
		capabilities["rig"] = map[string]any{"tune": c.Tune, "preview": c.Preview, "split": c.Split}
	}
	if s.wavelog != nil {
		// "awards" => the in-process award engine is running (covers
		// dxcc/band/mode/was/pota). The frontend keys off this.
		capabilities["lookup"] = map[string]any{"wavelog": true, "awards": s.awards != nil}
	}

	resp := map[string]any{
		"service":           "horstoperator-agent",
		"version":           "v1",
		"control_permitted": s.cfg.ControlPermitted,
		"capabilities":      capabilities,
		"pstrotator": map[string]any{
			"endpoint":   net.JoinHostPort(s.cfg.PSTHost, strconv.Itoa(s.cfg.PSTPort)),
			"reply_port": s.cfg.PSTPort + 1,
			"reachable":  true,
			"note":       "liveness validated via /v1/antenna/state polling",
		},
	}
	if s.awards != nil {
		resp["awards"] = s.awards.Health()
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *server) handleStation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"station": map[string]any{
			"name":    s.cfg.Station.Name,
			"lat":     s.cfg.Station.Lat,
			"lng":     s.cfg.Station.Lng,
			"locator": strings.ToUpper(strings.TrimSpace(s.cfg.Station.Locator)),
		},
	})
}

func (s *server) handleAntennaState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.Timeout)
	defer cancel()

	state, err := s.getPolledStateOrFallback(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}

	s.pollMu.RLock()
	fastPolling := s.pollState.fastPollingActive
	targetAzimuth := s.pollState.targetAzimuthDeg
	s.pollMu.RUnlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"station": map[string]any{
			"name":    s.cfg.Station.Name,
			"lat":     s.cfg.Station.Lat,
			"lng":     s.cfg.Station.Lng,
			"locator": strings.ToUpper(strings.TrimSpace(s.cfg.Station.Locator)),
		},
		"antenna": map[string]any{
			"azimuth_deg":        state.AzimuthDeg,
			"mode":               state.Mode,
			"beamwidth_3db_deg":  s.cfg.Beamwidth3dBDeg,
			"available_modes":    s.cfg.AllowedModes,
			"fast_polling":       fastPolling,
			"target_azimuth_deg": targetAzimuth,
		},
	})
}

type rotateRequest struct {
	PermitControl bool    `json:"permit_control"`
	AzimuthDeg    float64 `json:"azimuth_deg"`
}

func (s *server) handleRotate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}

	if !s.cfg.ControlPermitted {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "control disabled by agent configuration"})
		return
	}

	var req rotateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if !req.PermitControl {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "permit_control must be true"})
		return
	}
	if !isFinite(req.AzimuthDeg) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "azimuth_deg is required"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.Timeout)
	defer cancel()

	az := normalizeAzimuth(req.AzimuthDeg)
	if err := s.client.RotateToAzimuth(ctx, az); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	s.beginFastPollingForTarget(az)

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                   true,
		"azimuth_deg":          az,
		"fast_polling_enabled": true,
		"poll_interval_ms":     int(fastPollInterval / time.Millisecond),
	})
}

type modeRequest struct {
	PermitControl bool   `json:"permit_control"`
	Mode          string `json:"mode"`
}

func (s *server) handleMode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}

	if !s.cfg.ControlPermitted {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "control disabled by agent configuration"})
		return
	}

	var req modeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if !req.PermitControl {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "permit_control must be true"})
		return
	}

	mode := normalizeMode(req.Mode)
	if !hasMode(s.cfg.AllowedModes, mode) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "mode not allowed by agent configuration"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.Timeout)
	defer cancel()
	if err := s.client.SetMode(ctx, mode); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"mode": mode,
	})
}

var maidenheadRegex = regexp.MustCompile(`^[A-R]{2}[0-9]{2}([A-X]{2})?$`)

// normalizeLocator validates a 4- or 6-character Maidenhead locator and returns
// it upper-cased. The first two pairs are case-folded; an invalid grid is an error.
func normalizeLocator(raw string) (string, error) {
	loc := strings.ToUpper(strings.TrimSpace(raw))
	if loc == "" {
		return "", errors.New("empty")
	}
	if !maidenheadRegex.MatchString(loc) {
		return "", fmt.Errorf("invalid Maidenhead locator %q (expected e.g. JO62 or JO62qm)", raw)
	}
	return loc, nil
}

// locatorToLatLng returns the center lat/lng of a validated Maidenhead locator.
// Mirrors locatorToLatLng in the backend (spot.go); kept local since the agent
// is a standalone package.
func locatorToLatLng(locator string) (lat, lng float64) {
	locator = strings.ToUpper(locator)
	if len(locator) < 2 {
		return 0, 0
	}
	lng = float64(locator[0]-'A')*20 - 180
	lat = float64(locator[1]-'A')*10 - 90
	if len(locator) >= 4 {
		lng += float64(locator[2]-'0') * 2
		lat += float64(locator[3]-'0') * 1
		if len(locator) >= 6 {
			lng += float64(locator[4]-'A')*(5.0/60.0) + (5.0 / 120.0)
			lat += float64(locator[5]-'A')*(2.5/60.0) + (2.5 / 120.0)
		} else {
			lng += 1.0
			lat += 0.5
		}
	} else {
		lng += 10.0
		lat += 5.0
	}
	return lat, lng
}

// setupLogging directs log output to a file when requested. The windowsgui (tray)
// build has no console, so without this its log lines vanish; in that case we
// default to a file next to the exe. Returns the resolved path ("" = stderr).
func setupLogging(logFile string, tray bool) string {
	path := strings.TrimSpace(logFile)
	if path == "" {
		if !tray {
			return "" // console build: stderr is fine
		}
		path = defaultLogPath()
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Printf("[WARN] could not open log file %q: %v", path, err)
		return ""
	}
	if tray {
		// No usable console on the GUI build, so write to the file only (a
		// MultiWriter including the dead stderr would error and drop file writes).
		log.SetOutput(f)
	} else {
		log.SetOutput(io.MultiWriter(os.Stderr, f))
	}
	log.Printf("[INFO] logging to %s", path)
	return path
}

// defaultLogPath puts the log next to the exe (i.e. the install dir on Windows),
// falling back to the working directory if the exe path can't be resolved.
func defaultLogPath() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "horstoperator-agent.log")
	}
	return "horstoperator-agent.log"
}

func main() {
	// Resolve the .env path first (overridable via HORSTOP_ENV_FILE) and load it
	// before reading flags, so env-backed flag defaults below see its values and
	// the tray Settings page edits the same file. Stored absolute so restart and
	// any CWD change still find it.
	envFile := envOr("HORSTOP_ENV_FILE", ".env")
	if abs, err := filepath.Abs(envFile); err == nil {
		envFile = abs
	}
	dotenv.Load(envFile)

	listenAddr := flag.String("listen", envOr("LISTEN", "127.0.0.1:9955"), "HTTP listen address for local operator agent")
	backendURL := flag.String("backend-url", envOr("BACKEND_URL", ""), "Optional HorstReporter backend base URL for reverse proxy (e.g. https://horstreporter.kgbvax.net)")
	pstHost := flag.String("pst-host", envOr("PST_HOST", "127.0.0.1"), "PSTrotator host")
	pstPort := flag.Int("pst-port", envIntOr("PST_PORT", 12000), "PSTrotator UDP port")
	pstTimeoutMs := flag.Int("pst-timeout-ms", envIntOr("PST_TIMEOUT_MS", 1500), "PSTrotator UDP timeout in milliseconds")
	pstLogTraffic := flag.Bool("pst-log-traffic", false, "Enable UDP TX/RX logging for PSTrotator traffic diagnostics")
	pstLogTrafficHex := flag.Bool("pst-log-traffic-hex", false, "Include hex dump (truncated) in UDP traffic logs")
	pstLogMaxBytes := flag.Int("pst-log-max-bytes", 256, "Maximum bytes shown in UDP traffic log previews")

	stationName := flag.String("station-name", envOr("STATION_NAME", "operator-station"), "Station display name")
	stationLocator := flag.String("station-locator", envOr("STATION_LOCATOR", ""), "Station Maidenhead locator (required, e.g. JO62qm)")

	controlPermitted := flag.Bool("control-permitted", envBoolOr("CONTROL_PERMITTED", true), "Allow rotate/mode control commands")
	beamwidth3db := flag.Float64("beamwidth-3db-deg", envFloatOr("BEAMWIDTH_3DB_DEG", 60), "Antenna 3dB beamwidth reported to UI")
	allowedModesRaw := flag.String("allowed-modes", envOr("ALLOWED_MODES", "forward,backward,bidirectional"), "Comma-separated allowed antenna modes")

	rigTransport := flag.String("rig-transport", envOr("RIG_TRANSPORT", "none"), "Rig control backend: none|waveloggate|log4om (waveloggate = WaveLogGate HTTP callback; log4om = Log4OM Remote Control UDP; both tune VFO A, preview/split need a future rigctld/FLRig backend)")
	rigWaveLogGate := flag.String("rig-waveloggate-url", envOr("RIG_WAVELOGGATE_URL", "http://127.0.0.1:54321"), "WaveLogGate callback base URL (used when -rig-transport=waveloggate)")
	rigLog4OM := flag.String("rig-log4om-addr", envOr("RIG_LOG4OM_ADDR", log4omDefaultAddr), "Log4OM Remote Control inbound UDP host:port (used when -rig-transport=log4om)")
	trayEnabled := flag.Bool("tray", false, "Run with a Windows tray icon showing live status (Windows only; ignored elsewhere)")
	logFilePath := flag.String("log-file", envOr("LOG_FILE", ""), "Write logs to this file; the tray (GUI) build has no console, so it defaults to a file next to the exe")
	debugLogging := flag.Bool("debug-logging", envBoolOr("DEBUG_LOGGING", false), "Verbose logging of all external traffic (PSTrotator UDP + Wavelog/backend/rig HTTP); secrets redacted")
	taskName := flag.String("task-name", envOr("HORSTOP_TASK", ""), "Windows scheduled task name; lets the Settings page restart-to-apply cleanly via the task")
	potaHuntedCSV := flag.String("pota-hunted-csv", strings.TrimSpace(os.Getenv("POTA_HUNTED_CSV")), "Path to a POTA hunted-parks CSV export (enables POTA 'wanted'); empty = disabled")
	awardsDataDir := flag.String("awards-data-dir", firstNonEmpty(strings.TrimSpace(os.Getenv("HORSTAWARDS_DATA_DIR")), "./horstawards-data"), "Local directory for the award-progress snapshot store")

	ubEnabled := flag.Bool("ub-enabled", envBoolOr("UB_ENABLED", false), "Enable UltraBeam beam-direction control over MQTT")
	ubBrokerURL := flag.String("ub-broker-url", envOr("UB_BROKER_URL", ""), "UltraBeam MQTT broker URL, e.g. tcp://127.0.0.1:1883 (tls:// for an authenticated remote broker)")
	ubClientID := flag.String("ub-client-id", envOr("UB_CLIENT_ID", ""), "UltraBeam MQTT client ID; must differ from ubctrl's own id. Empty = auto horstoperator-<nanos>")
	ubTopicPrefix := flag.String("ub-topic-prefix", envOr("UB_TOPIC_PREFIX", defaultUltrabeamTopicPrefix), "UltraBeam ubctrl topic prefix")
	ubUsername := flag.String("ub-username", envOr("UB_USERNAME", ""), "UltraBeam MQTT username (if the broker requires auth)")

	flag.Parse()

	// Direct logs to a file when asked, or always on the tray (GUI) build since it
	// has no console and would otherwise discard every log line.
	resolvedLogFile := setupLogging(*logFilePath, *trayEnabled)

	// Debug logging: capture all outbound HTTP (Wavelog/backend/rig/POTA + reverse
	// proxy) by wrapping the default transport, before any client issues a request.
	// The PSTrotator UDP half is enabled via UDPLogTraffic in the config below.
	if *debugLogging {
		enableExternalDebugLogging()
	}

	locator, err := normalizeLocator(*stationLocator)
	if err != nil {
		if *trayEnabled {
			// In tray mode we must still boot so the operator can set the locator
			// via the Settings page; an empty locator just yields a 0,0 station
			// until they save and restart.
			log.Printf("[WARN] station-locator not set/invalid (%v); open the tray → Settings to configure it", err)
			locator = ""
		} else {
			log.Fatalf("[FATAL] station-locator is required: %v", err)
		}
	}

	// Station position is derived from the Maidenhead locator (grid-square center).
	var lat, lng float64
	if locator != "" {
		lat, lng = locatorToLatLng(locator)
	}

	timeout := time.Duration(*pstTimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 1500 * time.Millisecond
	}

	cfg := serviceConfig{
		ListenAddr:       strings.TrimSpace(*listenAddr),
		BackendBaseURL:   strings.TrimRight(strings.TrimSpace(*backendURL), "/"),
		ControlPermitted: *controlPermitted,
		Beamwidth3dBDeg:  math.Max(1, *beamwidth3db),
		AllowedModes:     splitModes(*allowedModesRaw),
		UDPLogTraffic:    *pstLogTraffic || *debugLogging,
		UDPLogHex:        *pstLogTrafficHex,
		UDPLogMaxBytes:   *pstLogMaxBytes,
		Timeout:          timeout,
		PSTHost:          strings.TrimSpace(*pstHost),
		PSTPort:          *pstPort,
		Station: stationConfig{
			Name:    strings.TrimSpace(*stationName),
			Lat:     lat,
			Lng:     lng,
			Locator: locator,
		},
		RigTransport:   strings.TrimSpace(*rigTransport),
		RigWaveLogGate: strings.TrimSpace(*rigWaveLogGate),
		RigLog4OM:      strings.TrimSpace(*rigLog4OM),
		WavelogURL:     firstNonEmpty(strings.TrimSpace(os.Getenv("WAVELOG_URL")), defaultWavelogURL),
		WavelogAPIKey:  strings.TrimSpace(os.Getenv("WAVELOG_API_KEY")),
		EnvFile:        envFile,
		TaskName:       strings.TrimSpace(*taskName),
		LogFile:        resolvedLogFile,
		DebugExternal:  *debugLogging,

		UBEnabled:     *ubEnabled,
		UBBrokerURL:   strings.TrimSpace(*ubBrokerURL),
		UBClientID:    strings.TrimSpace(*ubClientID),
		UBTopicPrefix: strings.TrimSpace(*ubTopicPrefix),
		UBUsername:    strings.TrimSpace(*ubUsername),
		UBPassword:    os.Getenv("UB_PASSWORD"),
	}

	// Local award-progress engine: runs in-process so the operator's Wavelog log
	// and POTA CSV never leave this machine. Reuses the agent's Wavelog creds.
	awCfg := awards.Defaults()
	awCfg.DataDir = strings.TrimSpace(*awardsDataDir)
	awCfg.WavelogURL = cfg.WavelogURL
	awCfg.WavelogAPIKey = cfg.WavelogAPIKey
	awCfg.WavelogStationID = strings.TrimSpace(os.Getenv("WAVELOG_STATION_ID"))
	awCfg.POTAHuntedCSV = strings.TrimSpace(*potaHuntedCSV)
	if v := strings.TrimSpace(os.Getenv("POTA_CALLSIGN")); v != "" {
		awCfg.POTACall = strings.ToUpper(v)
	}
	awCfg.POTAToken = strings.TrimSpace(os.Getenv("POTA_TOKEN"))
	if v := strings.TrimSpace(os.Getenv("POTA_BASE_URL")); v != "" {
		awCfg.POTABaseURL = v
	}
	cfg.Awards = awCfg

	if cfg.ListenAddr == "" {
		log.Fatal("[FATAL] listen address must not be empty")
	}
	if cfg.BackendBaseURL != "" {
		u, err := url.Parse(cfg.BackendBaseURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			log.Fatalf("[FATAL] backend-url must be a valid absolute URL: %q", cfg.BackendBaseURL)
		}
	}
	if cfg.PSTHost == "" {
		log.Fatal("[FATAL] pst-host must not be empty")
	}
	if cfg.PSTPort <= 0 || cfg.PSTPort > 65535 {
		log.Fatal("[FATAL] pst-port must be in range 1..65535")
	}
	if cfg.UDPLogMaxBytes < 32 {
		cfg.UDPLogMaxBytes = 32
	}
	if cfg.UBEnabled {
		if err := validateBrokerURL(cfg.UBBrokerURL); err != nil {
			log.Printf("[WARN] UltraBeam control disabled: %v", err)
			cfg.UBEnabled = false
		}
	}

	mux := http.NewServeMux()
	srv := newServer(cfg)
	srv.registerRoutes(mux)
	handler := withCORS(mux)

	log.Printf("[INFO] horstoperator-agent listening on %s", cfg.ListenAddr)
	log.Printf("[INFO] PSTrotator UDP endpoint: %s", net.JoinHostPort(cfg.PSTHost, strconv.Itoa(cfg.PSTPort)))
	log.Printf("[INFO] PSTrotator UDP protocol profile: queryAZ=%q queryMode=%q rotate=<PST><TRACK>0</TRACK><AZIMUTH>x</AZIMUTH></PST> mode=<PST><TRACK>0|1</TRACK></PST> terminator=CR reply-port=%d", pstQueryAzCommand, pstQueryModeCommand, cfg.PSTPort+1)
	log.Printf("[INFO] Control permitted: %v, allowed modes: %s", cfg.ControlPermitted, strings.Join(cfg.AllowedModes, ","))
	switch strings.ToLower(cfg.RigTransport) {
	case "waveloggate":
		log.Printf("[INFO] Rig control: WaveLogGate (tune VFO A) via %s", cfg.RigWaveLogGate)
	case "log4om":
		log.Printf("[INFO] Rig control: Log4OM Remote Control (tune VFO A) via UDP %s", cfg.RigLog4OM)
	default:
		log.Printf("[INFO] Rig control: disabled (-rig-transport=%s)", cfg.RigTransport)
	}
	if cfg.WavelogAPIKey != "" {
		log.Printf("[INFO] Wavelog enrichment: enabled (%s)", cfg.WavelogURL)
	} else {
		log.Printf("[INFO] Wavelog enrichment: disabled (set WAVELOG_API_KEY to enable)")
	}
	log.Printf("[INFO] Station: %s (lat=%.6f lng=%.6f locator=%s)", cfg.Station.Name, cfg.Station.Lat, cfg.Station.Lng, strings.ToUpper(cfg.Station.Locator))
	if cfg.UDPLogTraffic {
		log.Printf("[INFO] UDP traffic diagnostics enabled (hex=%v, max-bytes=%d)", cfg.UDPLogHex, cfg.UDPLogMaxBytes)
	}
	if cfg.BackendBaseURL != "" {
		log.Printf("[INFO] Backend reverse proxy enabled: %s", cfg.BackendBaseURL)
	} else {
		log.Printf("[INFO] Backend reverse proxy disabled (set -backend-url to forward UI/API traffic)")
	}

	if err := serveAgent(cfg, srv, handler, *trayEnabled); err != nil {
		log.Fatalf("[FATAL] local agent server failed: %v", err)
	}
}

// serveHTTP runs the blocking HTTP server. Shared by the headless and tray paths.
func serveHTTP(cfg serviceConfig, handler http.Handler) error {
	return http.ListenAndServe(cfg.ListenAddr, handler)
}
