// Command pathscope is the standalone Pathscope HTTP server. It reads from
// the horstreporter PostgreSQL database (dx_raw_spots, proplab_cell_buckets,
// proplab_sw_series) and serves the band×region glance view plus a detailed
// cell view at /api/pathscope/v1/, plus a Server-Sent Events stream at
// /api/pathscope/v1/stream that pushes a fresh glance every 5 s.
//
// Pathscope is a sibling binary in the same module, sharing the same
// connection pool semantics as horstprop: read-only PG access, no MQTT
// ingest. The SSE stream is served by the pathscope binary directly; the
// main horstreporter binary reverse-proxies /pathscope/* to it via
// pathscope_mount.go so the SSE flows through the main binary's TLS
// termination without an nginx buffering config change.
//
// Configuration via env (preferred) or flag:
//
//	PATHSCOPE_DSN          Postgres DSN (defaults to DX_POSTGRES_DSN)
//	PATHSCOPE_LISTEN       listen address (default :9960)
//	PATHSCOPE_QTH          operator Maidenhead locator (default JO62qm)
//	PATHSCOPE_STATIC_DIR   override the embedded static dir (dev only)
//	PATHSCOPE_TICK_INTERVAL  how often to re-score and broadcast (default 5s)
//
// The DSN convention is identical to the main horstreporter binary so the
// two share the same systemd EnvironmentFile.
package main

import (
	"context"
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"horstreporter/cmd/pathscope/internal/server"
	"horstreporter/internal/pathscope"
	"horstreporter/internal/region"

	"github.com/jackc/pgx/v5/pgxpool"
)

// version is exposed via /api/pathscope/v1/health.
const version = "v0"

//go:embed static
var staticFS embed.FS

func main() {
	listen := flag.String("listen", envOr("PATHSCOPE_LISTEN", ":9960"),
		"listen address")
	dsn := flag.String("dsn", envOr("PATHSCOPE_DSN", envOr("DX_POSTGRES_DSN", "")),
		"Postgres DSN (falls back to DX_POSTGRES_DSN; prefer env, keep secrets out of argv)")
	qth := flag.String("qth", envOr("PATHSCOPE_QTH", "JO62qm"),
		"operator home Maidenhead locator (used for regional bias and the home widget)")
	staticDir := flag.String("static-dir", envOr("PATHSCOPE_STATIC_DIR", ""),
		"override the embedded static dir (dev only; empty means use embedded)")
	tickInterval := flag.Duration("tick-interval", envDuration("PATHSCOPE_TICK_INTERVAL", 5*time.Second),
		"how often to re-score and broadcast on /api/pathscope/v1/stream (e.g. 2s, 30s)")
	flag.Parse()

	if !region.IsLocator(*qth) {
		log.Printf("pathscope: WARNING qth %q is not a valid 4-char Maidenhead locator; using centroid of 'JN58td' (placeholder EU)", *qth)
		*qth = "JN58td"
	}
	home := region.FromLocator(*qth)

	dsnResolved := strings.TrimSpace(*dsn)
	if dsnResolved == "" {
		log.Fatalf("pathscope: no DSN — set PATHSCOPE_DSN or DX_POSTGRES_DSN, or pass -dsn")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pcfg, err := pgxpool.ParseConfig(dsnResolved)
	if err != nil {
		log.Fatalf("pathscope: parse dsn: %v", err)
	}
	pcfg.MaxConns = 8
	pcfg.MinConns = 1
	pcfg.MaxConnLifetime = 30 * time.Minute
	pcfg.HealthCheckPeriod = 1 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		log.Fatalf("pathscope: connect: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("pathscope: ping: %v", err)
	}

	store := pathscope.NewStore(pool)
	engine := pathscope.NewScoringEngine()

	srv := server.New(server.Deps{
		Store:      store,
		Engine:     engine,
		QTH:        *qth,
		HomeRegion: home,
		Version:    version,
		Static:     staticFS,
		StaticDir:  *staticDir,
		Interval:   *tickInterval,
	})

	httpSrv := &http.Server{
		Addr:              *listen,
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // SSE: streams are long-lived; no WriteTimeout
		IdleTimeout:       2 * time.Minute,
	}

	srv.StartScoring(ctx)
	defer srv.Stop()

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	}()

	log.Printf("pathscope %s listening on %s (qth %s, home region %s, tick %s)", version, *listen, *qth, home, *tickInterval)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("pathscope: %v", err)
	}
}

// envOr returns the value of the env var if non-empty, otherwise def.
func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// envDuration returns the value of the env var parsed as a duration, or
// def if the env var is missing or unparseable. Mirrors envOr but for
// time.Duration values so PATHSCOPE_TICK_INTERVAL accepts "5s", "2m", etc.
func envDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Printf("pathscope: invalid %s=%q (%v); using default %s", key, v, err, def)
		return def
	}
	return d
}

// silence unused import warnings in case future deployments pivot on fs.
var _ fs.FS = embed.FS{}
