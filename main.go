package main

import (
	"bytes"
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"dxlens"

	"horstreporter/internal/cty"

	"golang.org/x/crypto/acme/autocert"
	"gopkg.in/natefinch/lumberjack.v2"
)

// maskDSN redacts the password in a URL-style DSN so it can be logged safely.
func maskDSN(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil {
		return dsn
	}
	if _, hasPw := u.User.Password(); hasPw {
		u.User = url.UserPassword(u.User.Username(), "***")
		return u.String()
	}
	return dsn
}

// dsnSource reports where the effective Postgres DSN came from, for an
// audit-friendly startup log line that never prints the secret itself.
func dsnSource(flagVal string) string {
	if strings.TrimSpace(flagVal) != "" {
		return "flag -dx-postgres-dsn"
	}
	if strings.TrimSpace(os.Getenv("DX_POSTGRES_DSN")) != "" {
		return "env DX_POSTGRES_DSN"
	}
	return "built-in default"
}

// loadCtyResolver returns the cty.dat resolver for DX-cluster country/flag
// labelling. It uses the embedded cty.dat by default; a non-empty path overrides
// it with a file (e.g. a fresher cty.dat) when readable.
func loadCtyResolver(path string) *cty.Resolver {
	if path != "" {
		if f, err := os.Open(path); err == nil {
			defer f.Close()
			if r, err := cty.Parse(f); err == nil {
				logInfo("DX cluster cty.dat loaded from %s", path)
				return r
			} else {
				logInfo("DX cluster cty.dat parse failed (%s): %v — falling back to embedded", path, err)
			}
		} else {
			logInfo("DX cluster cty.dat not readable (%s): %v — falling back to embedded", path, err)
		}
	}
	r, err := cty.Parse(bytes.NewReader(embeddedCtyData))
	if err != nil {
		logInfo("DX cluster embedded cty.dat parse failed: %v (labelling disabled)", err)
		return nil
	}
	logInfo("DX cluster cty.dat: using embedded (%d bytes)", len(embeddedCtyData))
	return r
}

//go:embed static
var staticFiles embed.FS

// cty.dat is embedded so DX-cluster country/flag labelling works out of the box
// (no file to deploy). Refreshed whenever the binary is rebuilt; -cty-path can
// override it with a newer file at runtime.
//
//go:embed cty.dat
var embeddedCtyData []byte

var compressStream bool
var dxBaseline *DxBaselineEngine
var streamAccounting = &streamAccountingState{}

const defaultLiveHistoryRetentionMinutes = 60

var maxClients int
var logLevel = "INFO"
var liveHistoryRetentionMinutes = defaultLiveHistoryRetentionMinutes

type streamAccountingState struct {
	sessionsStarted   atomic.Int64
	sessionsCompleted atomic.Int64
	activeSessions    atomic.Int64
	bytesTotal        atomic.Int64
}

func (a *streamAccountingState) startSession() {
	a.sessionsStarted.Add(1)
	a.activeSessions.Add(1)
}

func (a *streamAccountingState) completeSession(bytes int64) {
	if bytes < 0 {
		bytes = 0
	}
	a.bytesTotal.Add(bytes)
	a.sessionsCompleted.Add(1)
	a.activeSessions.Add(-1)
}

func (a *streamAccountingState) snapshot() (started, completed, active, bytesTotal int64, avgBytes float64) {
	started = a.sessionsStarted.Load()
	completed = a.sessionsCompleted.Load()
	active = a.activeSessions.Load()
	bytesTotal = a.bytesTotal.Load()
	if completed > 0 {
		avgBytes = float64(bytesTotal) / float64(completed)
	}
	return
}

func init() {
	if lvl := os.Getenv("LOG_LEVEL"); lvl != "" {
		logLevel = strings.ToUpper(lvl)
	}
}

func moduleFromCaller(skip int) string {
	_, file, _, ok := runtime.Caller(skip)
	if !ok {
		return "UNKNOWN"
	}
	base := filepath.Base(file)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	name = strings.ReplaceAll(name, "-", "_")
	if name == "" {
		return "UNKNOWN"
	}
	return strings.ToUpper(name)
}

func logWithLevel(level, format string, v ...interface{}) {
	module := moduleFromCaller(3)
	prefix := "[" + level + "][" + module + "] " + format
	log.Printf(prefix, v...)
}

func logDebug(format string, v ...interface{}) {
	if logLevel == "DEBUG" {
		logWithLevel("DEBUG", format, v...)
	}
}

func logInfo(format string, v ...interface{}) {
	if logLevel == "DEBUG" || logLevel == "INFO" {
		logWithLevel("INFO", format, v...)
	}
}

func logError(format string, v ...interface{}) {
	logWithLevel("ERROR", format, v...)
}

func logFatal(format string, v ...interface{}) {
	module := moduleFromCaller(3)
	prefix := "[FATAL][" + module + "] " + format
	log.Fatalf(prefix, v...)
}

func main() {
	port := flag.String("port", "8080", "HTTP/HTTPS server port")
	certFile := flag.String("cert", "", "Path to TLS certificate file")
	keyFile := flag.String("key", "", "Path to TLS key file")
	domain := flag.String("domain", "", "Domain for Let's Encrypt (enables automatic TLS)")
	dev := flag.Bool("dev", false, "Enable development mode (disables caching of static files)")
	flag.BoolVar(&compressStream, "compress", false, "Enable gzip compression for the SSE stream")
	enablePprof := flag.Bool("pprof", false, "Enable pprof profiling on localhost:6060")
	logLevelFlag := flag.String("log-level", "", "Log level: DEBUG, INFO, WARN (default: INFO if env LOG_LEVEL not set)")
	logFile := flag.String("log-file", "", "Path to the log file (enables file logging with rotation)")
	logMaxAge := flag.Int("log-max-age", 30, "Maximum number of days to retain old log files")
	logMaxBackups := flag.Int("log-max-backups", 7, "Maximum number of old log files to retain")
	logMaxSize := flag.Int("log-max-size", 100, "Maximum size in megabytes of the log file before it gets rotated")
	flag.IntVar(&maxClients, "max-clients", 150, "Maximum number of concurrent SSE clients (0 = unlimited)")
	dxBaselineFile := flag.String("dx-baseline-file", "dx_baseline.json", "Path to persistent DX baseline bucket storage")
	dxPostgresDSN := flag.String("dx-postgres-dsn", "", "Postgres DSN for DX baseline and raw spot storage (falls back to env DX_POSTGRES_DSN, then the built-in default; keep secrets out of argv via the env var)")
	dxPostgresFailFast := flag.Bool("dx-postgres-fail-fast", true, "Exit immediately when Postgres init/migration fails")
	horstpropURL := flag.String("horstprop-url", "http://127.0.0.1:9970", "Reverse-proxy /horstprop/* to this local horstprop scoring service (empty disables the mount)")
	pathscopeURL := flag.String("pathscope-url", "http://127.0.0.1:9960", "Reverse-proxy /pathscope/* to this local pathscope scoring service (empty disables the mount)")
	dxClusterEnable := flag.Bool("dxcluster-enable", false, "Enable optional DX cluster ingest")
	dxClusterEndpoint := flag.String("dxcluster-endpoint", "db0erf.de:7300", "DX cluster endpoint in host:port format")
	dxClusterReconnectSeconds := flag.Int("dxcluster-reconnect-seconds", 15, "Delay before reconnecting to DX cluster after disconnect")
	dxClusterVerbose := flag.Bool("dxcluster-verbose", false, "Enable verbose DX cluster connection logging")
	dxClusterUsername := flag.String("dxcluster-username", "", "Callsign sent when connecting to DX cluster")
	dxClusterPassword := flag.String("dxcluster-password", "", "Optional password sent when connecting to DX cluster")
	rbnEnable := flag.Bool("rbn-enable", false, "Enable optional RBN (Reverse Beacon Network) CW/RTTY telnet ingest")
	rbnEndpoint := flag.String("rbn-endpoint", "telnet.reversebeacon.net:7000", "RBN raw telnet endpoint (host:port). :7000 = CW/RTTY no-auth; :7001 = FT8 (redundant with PSKReporter)")
	rbnCallsign := flag.String("rbn-callsign", "", "Callsign sent at the RBN relay's \"enter your call\" prompt (required-in-practice to get a spot stream; no password/auth). Falls back to env RBN_CALLSIGN")
	rbnReconnectSeconds := flag.Int("rbn-reconnect-seconds", 15, "Delay before reconnecting to RBN after disconnect")
	rbnVerbose := flag.Bool("rbn-verbose", false, "Enable verbose RBN connection logging")
	wsprEnable := flag.Bool("wspr-enable", false, "Enable optional WSPR ingest (wspr.live ClickHouse HTTP interface)")
	wsprEndpoint := flag.String("wspr-endpoint", "https://db1.wspr.live", "WSPR ClickHouse HTTP endpoint (base URL)")
	wsprPollSeconds := flag.Int("wspr-poll-seconds", 60, "Seconds between WSPR polls (wspr.live rate-limits to ~20 req/min)")
	wsprVerbose := flag.Bool("wspr-verbose", false, "Enable verbose WSPR polling logs")
	ctyPath := flag.String("cty-path", os.Getenv("CTY_DAT_PATH"), "Path to AD1C cty.dat for DX-cluster country/flag labelling (empty disables)")
	qrzUsernameFlag := flag.String("qrz-username", "", "QRZ username for optional callsign->locator enrichment")
	qrzPasswordFlag := flag.String("qrz-password", "", "QRZ password for optional callsign->locator enrichment")
	liveHistoryRetentionFlag := flag.Int("live-history-minutes", defaultLiveHistoryRetentionMinutes, "Maximum age of retained live spots in minutes")
	dxBaselineMaxEventsFlag := flag.Int("dx-baseline-max-events", defaultDxBaselineMaxEvents, "Maximum number of retained DX baseline events")
	dxRawSpotRetentionDaysFlag := flag.Int("dx-raw-spot-retention-days", 60, "Delete dx_raw_spots rows older than this many days (0 disables retention).")
	proplabCellRetentionDaysFlag := flag.Int("proplab-cell-retention-days", 60, "Delete proplab cell bucket / SW series rows older than this many days (0 disables retention).")
	proplabSWEnableFlag := flag.Bool("proplab-sw-enable", false, "Enable space-weather index series ingest (NOAA SWPC kp/F10.7/xray/OVATION; consumed by pathscope)")
	opModeEnableFlag := flag.Bool("opmode-enable", true, "Deprecated: backend opmode integration endpoints are always enabled")
	opModeControlEnableFlag := flag.Bool("opmode-control-enable", false, "Allow rotate/control commands in operator mode")
	opModeAgentURLFlag := flag.String("opmode-agent-url", "", "Deprecated and ignored: backend never proxies to local operator agent")
	opModeAgentTimeoutMsFlag := flag.Int("opmode-agent-timeout-ms", 1500, "Deprecated and ignored: backend never proxies to local operator agent")
	flag.Parse()

	// Override logLevel from flag if provided
	if *logLevelFlag != "" {
		logLevel = strings.ToUpper(*logLevelFlag)
	}

	// Configure log file early so all startup messages (Postgres init, backfill, etc.) go to the file.
	if *logFile != "" {
		log.SetOutput(&lumberjack.Logger{
			Filename:   *logFile,
			MaxSize:    *logMaxSize,
			MaxBackups: *logMaxBackups,
			MaxAge:     *logMaxAge,
			Compress:   true,
		})
		logInfo("Logging configured to write to file: %s (MaxAge: %d days, MaxBackups: %d, MaxSize: %d MB)", *logFile, *logMaxAge, *logMaxBackups, *logMaxSize)
	}

	if *liveHistoryRetentionFlag > 0 {
		liveHistoryRetentionMinutes = *liveHistoryRetentionFlag
	}
	if *dxBaselineMaxEventsFlag > 0 {
		dxBaselineMaxEvents = *dxBaselineMaxEventsFlag
	}

	if !*opModeEnableFlag {
		logInfo("-opmode-enable=false is deprecated and ignored; backend opmode endpoints remain active")
	}
	if strings.TrimSpace(*opModeAgentURLFlag) != "" {
		logInfo("-opmode-agent-url is deprecated and ignored; browser must call local operator agent directly")
	}
	if *opModeAgentTimeoutMsFlag != 1500 {
		logInfo("-opmode-agent-timeout-ms is deprecated and ignored; backend no longer calls operator agent")
	}
	configureOpMode(*opModeControlEnableFlag)

	// Resolve the Postgres DSN: explicit flag wins (for ad-hoc/dev), then the
	// DX_POSTGRES_DSN env var (the production path — keeps the secret out of
	// argv / systemctl status), then the built-in default.
	dxPostgresDSNResolved := strings.TrimSpace(*dxPostgresDSN)
	if dxPostgresDSNResolved == "" {
		dxPostgresDSNResolved = strings.TrimSpace(os.Getenv("DX_POSTGRES_DSN"))
	}
	if dxPostgresDSNResolved == "" {
		dxPostgresDSNResolved = defaultDxPostgresDSN
	}

	logInfo("DX postgres DSN source: %s", dsnSource(*dxPostgresDSN))

	dxBaseline = newDxBaselineEngine(strings.TrimSpace(*dxBaselineFile))
	// Wire the DXCC cty.dat resolver into the baseline engine so the regional
	// baseline can derive the operator's region for callsign targets (QRZ
	// locator → region; fallback to DXCC entity centroid → region). The cty
	// resolver is always available (embedded cty.dat); QRZ is wired later when
	// credentials are present (shared with DX-cluster/RBN ingest).
	dxBaseline.SetResolvers(nil, loadCtyResolver(strings.TrimSpace(*ctyPath)))
	if err := dxBaseline.EnablePostgres(dxPostgresDSNResolved); err != nil {
		if *dxPostgresFailFast {
			logFatal("DX postgres init failed (dsn=%s, fail-fast=true): %v", maskDSN(dxPostgresDSNResolved), err)
		}
		logInfo("DX postgres init failed (dsn=%s, fail-fast=false). Continuing with in-memory fallback: %v", maskDSN(dxPostgresDSNResolved), err)
	} else {
		logInfo("DX postgres initialized (dsn=%s)", maskDSN(dxPostgresDSNResolved))
		includeDXCluster := *dxClusterEnable
		backfillMinutes := liveHistoryRetentionMinutes
		if backfillMinutes <= 0 {
			backfillMinutes = defaultLiveHistoryRetentionMinutes
		}
		if cached, err := dxBaseline.LoadRecentSpotCache(backfillMinutes, time.Now().Unix(), includeDXCluster); err != nil {
			logInfo("Startup spot-cache backfill failed (last %d minutes, include_dxcluster=%v): %v", backfillMinutes, includeDXCluster, err)
		} else if len(cached) > 0 {
			hub.Lock()
			hub.history = append(make([]MQTTMessage, 0, len(cached)), cached...)
			hub.Unlock()
			logInfo("Startup spot-cache backfill loaded %d spots from dx_raw_spots (last %d minutes, include_dxcluster=%v)", len(cached), backfillMinutes, includeDXCluster)
		} else {
			logInfo("Startup spot-cache backfill found no spots in dx_raw_spots for the last %d minutes (include_dxcluster=%v)", backfillMinutes, includeDXCluster)
		}
	}

	cellBucketFeed = newCellBucketFeed(dxBaseline, *proplabCellRetentionDaysFlag)
	{
		// Seed the cell bucket engine with the most recent 15 minutes of live
		// history so pathscope has fresh rows immediately after restart.
		cellBackfillMinutes := 15
		if cached, err := dxBaseline.LoadRecentSpotCache(cellBackfillMinutes, time.Now().Unix(), *dxClusterEnable); err != nil {
			logInfo("Cell bucket startup spot-cache backfill failed (last %d minutes): %v", cellBackfillMinutes, err)
		} else if len(cached) > 0 {
			cellBucketFeed.Backfill(cached)
			logInfo("Cell bucket startup spot-cache backfill fed %d spots (last %d minutes)", len(cached), cellBackfillMinutes)
		}
		cellBucketFeed.Start()
		logInfo("Cell bucket feed enabled (retention=%dd, sw=%v)", *proplabCellRetentionDaysFlag, *proplabSWEnableFlag)
		if *proplabSWEnableFlag {
			go startProplabSWIngest()
		}
	}

	if *enablePprof {
		go func() {
			logInfo("Starting internal pprof server on localhost:6060")
			if err := http.ListenAndServe("localhost:6060", nil); err != nil {
				logError("pprof server exited: %v", err)
			}
		}()
	}

	go startMQTT()

	// QRZ callsign->locator enrichment + cty.dat DXCC resolver are shared by the optional
	// DX-cluster, RBN and WSPR ingests; construct once when any is enabled.
	if *dxClusterEnable || *rbnEnable || *wsprEnable {
		qrzUsername := strings.TrimSpace(*qrzUsernameFlag)
		qrzPassword := strings.TrimSpace(*qrzPasswordFlag)
		if qrzUsername == "" {
			qrzUsername = strings.TrimSpace(os.Getenv("QRZ_USERNAME"))
		}
		if qrzPassword == "" {
			qrzPassword = strings.TrimSpace(os.Getenv("QRZ_PASSWORD"))
		}
		resolver := CallsignLocatorResolver(nil)
		if qrzUsername != "" && qrzPassword != "" {
			resolver = newQRZLookupClient(qrzUsername, qrzPassword)
			logInfo("QRZ callsign enrichment enabled (shared by DX-cluster + RBN + regional baseline)")
			// Wire QRZ into the baseline engine so the regional baseline can
			// resolve callsign targets to a locator (→ region). The cty
			// resolver was already set at startup; this upgrades the QRZ slot.
			dxBaseline.SetResolvers(resolver, nil)
		} else {
			logInfo("QRZ callsign enrichment disabled (missing credentials); DX-cluster/RBN spots are chart-only; regional baseline uses DXCC center fallback")
		}
		ctyResolver := loadCtyResolver(strings.TrimSpace(*ctyPath))

		if *dxClusterEnable {
			dxClusterUser := strings.TrimSpace(*dxClusterUsername)
			dxClusterPass := strings.TrimSpace(*dxClusterPassword)
			if dxClusterUser == "" {
				dxClusterUser = strings.TrimSpace(os.Getenv("DXCLUSTER_USERNAME"))
			}
			if dxClusterPass == "" {
				dxClusterPass = strings.TrimSpace(os.Getenv("DXCLUSTER_PASSWORD"))
			}
			reconnectDelay := time.Duration(*dxClusterReconnectSeconds) * time.Second
			go startDXClusterIngest(dxClusterConfig{
				Enabled:        true,
				Endpoint:       strings.TrimSpace(*dxClusterEndpoint),
				ReconnectDelay: reconnectDelay,
				Verbose:        *dxClusterVerbose,
				Username:       dxClusterUser,
				Password:       dxClusterPass,
				Resolver:       resolver,
				CtyResolver:    ctyResolver,
			})
		}

		if *rbnEnable {
			rbnCall := strings.TrimSpace(*rbnCallsign)
			if rbnCall == "" {
				rbnCall = strings.TrimSpace(os.Getenv("RBN_CALLSIGN"))
			}
			// RBN only needs a callsign for identification at the relay prompt (no
			// password). When none is set, reuse the operator's DX-cluster callsign —
			// it's the same station identity and is already configured on prod via
			// DXCLUSTER_USERNAME, so activating RBN needs only -rbn-enable.
			if rbnCall == "" {
				rbnCall = strings.TrimSpace(os.Getenv("DXCLUSTER_USERNAME"))
			}
			reconnectDelay := time.Duration(*rbnReconnectSeconds) * time.Second
			go startRBNIngest(rbnConfig{
				Enabled:        true,
				Endpoint:       strings.TrimSpace(*rbnEndpoint),
				ReconnectDelay: reconnectDelay,
				Verbose:        *rbnVerbose,
				Callsign:       rbnCall,
				Resolver:       resolver,
				CtyResolver:    ctyResolver,
			})
		}

		if *wsprEnable {
			go startWSPRIngest(wsprConfig{
				Enabled:     true,
				Endpoint:    strings.TrimSpace(*wsprEndpoint),
				PollSeconds: *wsprPollSeconds,
				Verbose:     *wsprVerbose,
				Resolver:    resolver,
				CtyResolver: ctyResolver,
			})
		}
	}

	go func() {
		for range time.Tick(5 * time.Minute) {
			pruneLiveHistory(time.Now().Unix(), liveHistoryRetentionMinutes)
		}
	}()

	appMux := http.NewServeMux()
	var fileServer http.Handler
	if *dev {
		logInfo("Development mode enabled: Serving static files directly from the disk.")
		fileServer = http.FileServer(http.Dir("static"))
		appMux.Handle("/", noCache(fileServer))
	} else {
		staticFS, err := fs.Sub(staticFiles, "static")
		if err != nil {
			logFatal("Failed to load embedded static files: %v", err)
		}
		appMux.Handle("/", cachedStaticHandler(staticFS))
	}
	appMux.HandleFunc("/api/stream", streamHandler)
	appMux.HandleFunc("/api/capture_snapshot", captureSnapshotHandler)
	appMux.HandleFunc("/api/stats", statsHandler)
	appMux.HandleFunc("/api/dx_conditions", dxConditionsHandler)
	appMux.HandleFunc("/api/hot_bands", hotBandsHandler)
	appMux.HandleFunc("/api/square_details", squareDetailsHandler)
	appMux.HandleFunc("/api/dxspots", dxSpotsHandler)
	appMux.HandleFunc("/api/opmode/status", opModeStatusHandler)

	// Reverse-proxy /horstprop/* to the local horstprop scoring service so the
	// Chase Queue reaches it same-origin (horstprop itself stays bound to
	// localhost). Thin passthrough; no scoring logic lives here.
	if proxy, ok := newHorstpropProxy(*horstpropURL); ok {
		appMux.Handle("/horstprop/", proxy)
		logInfo("horstprop proxy mounted at /horstprop/ -> %s", *horstpropURL)
	}

	// Reverse-proxy /pathscope/* to the local pathscope scoring service.
	// pathscope exposes its own static assets AND an SSE stream; the
	// proxy uses FlushInterval: -1 (set in newPathscopeProxy) so the
	// SSE bytes flush per event rather than buffering. Thin passthrough;
	// no scoring logic lives here.
	if proxy, ok := newPathscopeProxy(*pathscopeURL); ok {
		appMux.Handle("/pathscope/", proxy)
		logInfo("pathscope proxy mounted at /pathscope/ -> %s", *pathscopeURL)
	}

	// Mount DXLens (separate module) at /dxlens/. Reads HorstReporter's
	// in-memory DX baseline via a small adapter; no extra network hops.
	// Cache TTL is 60s: the historic-data queries (recent-24h, region stats)
	// scan tens of millions of rows and cost several seconds each; the rose
	// and region calendar are propagation views, not real-time tickers.
	if dxBaseline != nil {
		provider := newDxlensProvider(dxBaseline, 60*time.Second)
		appMux.Handle("/dxlens/", dxlens.NewHandler(provider, dxlens.MountOptions{
			PathPrefix: "/dxlens/",
			NoCache:    *dev,
		}))
		logInfo("DXLens mounted at /dxlens/")
		// Pre-warm the snapshot so the heavy PG queries run during startup
		// rather than on the first user request. Async so it doesn't delay
		// the HTTP listener.
		go func() {
			started := time.Now()
			snap := provider.Snapshot()
			if snap == nil {
				logInfo("DXLens snapshot pre-warm: no snapshot built")
				return
			}
			logInfo("DXLens snapshot pre-warm complete in %s (recent_24h=%v, region_stats=%v)",
				time.Since(started).Round(time.Millisecond),
				len(snap.Recent24hBuckets) > 0,
				len(snap.RegionCalendarStats) > 0)
		}()
	}

	// dx_raw_spots retention loop: prune rows older than the configured
	// window once an hour. Disabled when retention is set to 0 days.
	if dxBaseline != nil && *dxRawSpotRetentionDaysFlag > 0 {
		retentionDays := *dxRawSpotRetentionDaysFlag
		go func() {
			// First run shortly after startup so any backlog is cleared
			// without waiting an hour; afterwards, hourly.
			firstDelay := 2 * time.Minute
			t := time.NewTimer(firstDelay)
			defer t.Stop()
			for {
				<-t.C
				cutoff := time.Now().Unix() - int64(retentionDays)*24*60*60
				n, err := dxBaseline.PruneRawSpotsOlderThan(cutoff)
				if err != nil {
					logInfo("dx_raw_spots prune failed (cutoff=%d, retention=%dd): %v", cutoff, retentionDays, err)
				} else if n > 0 {
					logInfo("dx_raw_spots prune deleted %d rows older than %d days", n, retentionDays)
				}
				t.Reset(1 * time.Hour)
			}
		}()
		logInfo("dx_raw_spots retention enabled: %d days", retentionDays)
	}

	if *domain != "" {
		logInfo("HorstReporter starting HTTPS server with Let's Encrypt for domain %s on port %s...", *domain, *port)
		m := &autocert.Manager{
			Cache:      autocert.DirCache("certs"), // Stores certificates in a local "certs" folder
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(*domain),
		}
		server := &http.Server{
			Addr:      ":" + *port,
			TLSConfig: m.TLSConfig(),
			Handler:   appMux,
		}
		if err := server.ListenAndServeTLS("", ""); err != nil {
			logFatal("HTTPS server failed: %v", err)
		}
	} else if *certFile != "" && *keyFile != "" {
		logInfo("HorstReporter starting HTTPS server with provided certs on port %s...", *port)
		if err := http.ListenAndServeTLS(":"+*port, *certFile, *keyFile, appMux); err != nil {
			logFatal("HTTPS server failed: %v", err)
		}
	} else {
		logInfo("HorstReporter starting HTTP server on port %s...", *port)
		if err := http.ListenAndServe(":"+*port, appMux); err != nil {
			logFatal("HTTP server failed: %v", err)
		}
	}
}

func pruneLiveHistory(now int64, retentionMinutes int) {
	if retentionMinutes <= 0 {
		retentionMinutes = defaultLiveHistoryRetentionMinutes
	}
	cutoff := now - int64(retentionMinutes*60)

	hub.Lock()
	defer hub.Unlock()

	keepIdx := sort.Search(len(hub.history), func(i int) bool {
		return hub.history[i].T >= cutoff
	})

	if keepIdx == len(hub.history) {
		if len(hub.history) > 0 {
			hub.history = make([]MQTTMessage, 0)
		}
		return
	}
	if keepIdx > 0 {
		// Reslice in place instead of make+copy of the retained tail. Every
		// hub.history reader copies its window out under hub.RLock()/Lock() and
		// no reader caches the slice header across this call, so dropping the
		// expired prefix by moving the slice start is safe — and it avoids an
		// ~86MB tail copy under the write lock that blocked the 20k/min ingest
		// append path every 5 min. The backing array self-compacts on the next
		// append-driven reallocation, so the dead prefix is reclaimed shortly.
		hub.history = hub.history[keepIdx:]
	}
}
