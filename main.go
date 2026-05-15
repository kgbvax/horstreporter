package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/acme/autocert"
	"gopkg.in/natefinch/lumberjack.v2"
)

var spotRecorder *lumberjack.Logger

//go:embed static
var staticFiles embed.FS

var compressStream bool

var maxClients int
var logLevel = "INFO"

func init() {
	if lvl := os.Getenv("LOG_LEVEL"); lvl != "" {
		logLevel = strings.ToUpper(lvl)
	}
}

func logDebug(format string, v ...interface{}) {
	if logLevel == "DEBUG" {
		log.Printf("[DEBUG] "+format, v...)
	}
}

func logInfo(format string, v ...interface{}) {
	if logLevel == "DEBUG" || logLevel == "INFO" {
		log.Printf("[INFO] "+format, v...)
	}
}

func main() {
	port := flag.String("port", "8080", "HTTP/HTTPS server port")
	certFile := flag.String("cert", "", "Path to TLS certificate file")
	keyFile := flag.String("key", "", "Path to TLS key file")
	domain := flag.String("domain", "", "Domain for Let's Encrypt (enables automatic TLS)")
	dev := flag.Bool("dev", false, "Enable development mode (disables caching of static files)")
	flag.BoolVar(&compressStream, "compress", false, "Enable gzip compression for the SSE stream")
	enablePprof := flag.Bool("pprof", false, "Enable pprof profiling on localhost:6060")
	logFile := flag.String("log-file", "", "Path to the log file (enables file logging with rotation)")
	logMaxAge := flag.Int("log-max-age", 30, "Maximum number of days to retain old log files")
	logMaxBackups := flag.Int("log-max-backups", 7, "Maximum number of old log files to retain")
	logMaxSize := flag.Int("log-max-size", 100, "Maximum size in megabytes of the log file before it gets rotated")
	flag.IntVar(&maxClients, "max-clients", 150, "Maximum number of concurrent SSE clients (0 = unlimited)")
	recordSpots := flag.String("record-spots", "", "Path to a file to record all incoming spots as JSONL")
	flag.Parse()

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

	if *recordSpots != "" {
		spotRecorder = &lumberjack.Logger{
			Filename:   *recordSpots,
			MaxSize:    *logMaxSize,
			MaxBackups: *logMaxBackups,
			MaxAge:     *logMaxAge,
			Compress:   true,
		}
		logInfo("Recording incoming spots to JSONL file: %s", *recordSpots)
	}

	if *enablePprof {
		go func() {
			logInfo("Starting internal pprof server on localhost:6060")
			log.Println(http.ListenAndServe("localhost:6060", nil))
		}()
	}

	go startMQTT()

	go func() {
		for range time.Tick(5 * time.Minute) {
			hub.Lock()
			cutoff := time.Now().Unix() - 2*3600 // Prune history older than 2h

			keepIdx := sort.Search(len(hub.history), func(i int) bool {
				return hub.history[i].T >= cutoff
			})

			if keepIdx == len(hub.history) {
				if len(hub.history) > 0 {
					hub.history = make([]MQTTMessage, 0)
				}
			} else if keepIdx > 0 {
				retained := len(hub.history) - keepIdx
				newHistory := make([]MQTTMessage, retained)
				copy(newHistory, hub.history[keepIdx:])
				hub.history = newHistory
			}
			hub.Unlock()
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
			log.Fatal("Failed to load embedded static files:", err)
		}
		fileServer = http.FileServer(http.FS(staticFS))
		appMux.Handle("/", fileServer)
	}
	appMux.HandleFunc("/api/stream", streamHandler)
	appMux.HandleFunc("/api/stats", statsHandler)

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
		log.Fatal(server.ListenAndServeTLS("", ""))
	} else if *certFile != "" && *keyFile != "" {
		logInfo("HorstReporter starting HTTPS server with provided certs on port %s...", *port)
		log.Fatal(http.ListenAndServeTLS(":"+*port, *certFile, *keyFile, appMux))
	} else {
		logInfo("HorstReporter starting HTTP server on port %s...", *port)
		log.Fatal(http.ListenAndServe(":"+*port, appMux))
	}
}
