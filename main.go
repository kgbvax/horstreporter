package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"dxlens"

	"golang.org/x/crypto/acme/autocert"
	"gopkg.in/natefinch/lumberjack.v2"
)

//go:embed static
var staticFiles embed.FS

var compressStream bool
var dxBaseline *DxBaselineEngine
var streamAccounting = &streamAccountingState{}

const defaultLiveHistoryRetentionMinutes = 120

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
	dxPostgresDSN := flag.String("dx-postgres-dsn", defaultDxPostgresDSN, "Postgres DSN for DX baseline and raw spot storage")
	dxPostgresFailFast := flag.Bool("dx-postgres-fail-fast", true, "Exit immediately when Postgres init/migration fails")
	dxClusterEnable := flag.Bool("dxcluster-enable", false, "Enable optional DX cluster ingest")
	dxClusterEndpoint := flag.String("dxcluster-endpoint", "db0erf.de:7300", "DX cluster endpoint in host:port format")
	dxClusterReconnectSeconds := flag.Int("dxcluster-reconnect-seconds", 15, "Delay before reconnecting to DX cluster after disconnect")
	dxClusterVerbose := flag.Bool("dxcluster-verbose", false, "Enable verbose DX cluster connection logging")
	dxClusterUsername := flag.String("dxcluster-username", "", "Callsign sent when connecting to DX cluster")
	dxClusterPassword := flag.String("dxcluster-password", "", "Optional password sent when connecting to DX cluster")
	qrzUsernameFlag := flag.String("qrz-username", "", "QRZ username for optional callsign->locator enrichment")
	qrzPasswordFlag := flag.String("qrz-password", "", "QRZ password for optional callsign->locator enrichment")
	liveHistoryRetentionFlag := flag.Int("live-history-minutes", defaultLiveHistoryRetentionMinutes, "Maximum age of retained live spots in minutes")
	dxBaselineMaxEventsFlag := flag.Int("dx-baseline-max-events", defaultDxBaselineMaxEvents, "Maximum number of retained DX baseline events")
	opModeEnableFlag := flag.Bool("opmode-enable", true, "Deprecated: backend opmode integration endpoints are always enabled")
	opModeControlEnableFlag := flag.Bool("opmode-control-enable", false, "Allow rotate/control commands in operator mode")
	opModeAgentURLFlag := flag.String("opmode-agent-url", "", "Deprecated and ignored: backend never proxies to local operator agent")
	opModeAgentTimeoutMsFlag := flag.Int("opmode-agent-timeout-ms", 1500, "Deprecated and ignored: backend never proxies to local operator agent")
	flag.Parse()

	// Override logLevel from flag if provided
	if *logLevelFlag != "" {
		logLevel = strings.ToUpper(*logLevelFlag)
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

	dxBaseline = newDxBaselineEngine(strings.TrimSpace(*dxBaselineFile))
	if err := dxBaseline.EnablePostgres(strings.TrimSpace(*dxPostgresDSN)); err != nil {
		if *dxPostgresFailFast {
			logFatal("DX postgres init failed (dsn=%s, fail-fast=true): %v", strings.TrimSpace(*dxPostgresDSN), err)
		}
		logInfo("DX postgres init failed (dsn=%s, fail-fast=false). Continuing with in-memory fallback: %v", strings.TrimSpace(*dxPostgresDSN), err)
	} else {
		logInfo("DX postgres initialized (dsn=%s)", strings.TrimSpace(*dxPostgresDSN))
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

	if *enablePprof {
		go func() {
			logInfo("Starting internal pprof server on localhost:6060")
			if err := http.ListenAndServe("localhost:6060", nil); err != nil {
				logError("pprof server exited: %v", err)
			}
		}()
	}

	go startMQTT()

	if *dxClusterEnable {
		dxClusterUser := strings.TrimSpace(*dxClusterUsername)
		dxClusterPass := strings.TrimSpace(*dxClusterPassword)
		if dxClusterUser == "" {
			dxClusterUser = strings.TrimSpace(os.Getenv("DXCLUSTER_USERNAME"))
		}
		if dxClusterPass == "" {
			dxClusterPass = strings.TrimSpace(os.Getenv("DXCLUSTER_PASSWORD"))
		}

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
			logInfo("DX cluster QRZ enrichment enabled")
		} else {
			logInfo("DX cluster QRZ enrichment disabled (missing credentials)")
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
		})
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
		fileServer = http.FileServer(http.FS(staticFS))
		appMux.Handle("/", fileServer)
	}
	appMux.HandleFunc("/api/stream", streamHandler)
	appMux.HandleFunc("/api/capture_snapshot", captureSnapshotHandler)
	appMux.HandleFunc("/api/stats", statsHandler)
	appMux.HandleFunc("/api/dx_conditions", dxConditionsHandler)
	appMux.HandleFunc("/api/dxpulse/v1/matrix", dxPulseMatrixHandler)
	appMux.HandleFunc("/api/dxpulse/v1/summary", dxPulseSummaryHandler)
	appMux.HandleFunc("/api/square_details", squareDetailsHandler)
	appMux.HandleFunc("/api/opmode/status", opModeStatusHandler)

	// Mount DXLens (separate module) at /dxlens/. Reads HorstReporter's
	// in-memory DX baseline via a small adapter; no extra network hops.
	if dxBaseline != nil {
		provider := newDxlensProvider(dxBaseline, 5*time.Second)
		appMux.Handle("/dxlens/", dxlens.NewHandler(provider, dxlens.MountOptions{
			PathPrefix: "/dxlens/",
			NoCache:    *dev,
		}))
		logInfo("DXLens mounted at /dxlens/")
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
		retained := len(hub.history) - keepIdx
		newHistory := make([]MQTTMessage, retained)
		copy(newHistory, hub.history[keepIdx:])
		hub.history = newHistory
	}
}
