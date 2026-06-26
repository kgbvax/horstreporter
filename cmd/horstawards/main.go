// Command horstawards is a standalone, operator-side award-progress service. It
// owns the operator's "slot index" (which DXCC entities / US states / POTA parks
// they have worked or confirmed) and answers POST /v1/wanted: given a batch of
// resolved spots, which award slots each one would fill.
//
// It mirrors cmd/horstprop: a separate binary in the same module, decoupled over
// HTTP, sharing only the contract types in internal/awardcontract. Progress is
// pulled from pluggable sources (Wavelog ADIF export; optionally the POTA API) on
// a slow schedule and folded into an in-memory index; the query path is a pure
// map lookup. The Chase Queue reaches it via the operator agent, which merges the
// result into its existing needed[] vocabulary.
//
// Secrets (WAVELOG_API_KEY, POTA_TOKEN) come only from env / a repo-root .env —
// never flags — and are never logged. See docs/horstawards.md.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"horstreporter/cmd/horstawards/internal/api"
	"horstreporter/cmd/horstawards/internal/award"
	"horstreporter/cmd/horstawards/internal/config"
	"horstreporter/cmd/horstawards/internal/source"
	"horstreporter/cmd/horstawards/internal/store"
	"horstreporter/internal/dotenv"
)

const version = "v1"

func main() {
	// Load .env (if present) before reading env so WAVELOG_API_KEY etc. resolve
	// regardless of how the service is launched.
	dotenv.Load(".env")

	cfg := config.FromEnv(config.Defaults())

	listen := flag.String("listen", cfg.Listen, "wanted API listen address")
	dataDir := flag.String("data-dir", cfg.DataDir, "directory for the persisted snapshot store")
	wavelogStation := flag.String("wavelog-station-id", cfg.WavelogStationID, "Wavelog station profile id (required for DCLNext)")
	potaCSV := flag.String("pota-hunted-csv", cfg.POTAHuntedCSV, "path to a POTA hunted-parks CSV export (recommended POTA source)")
	flag.Parse()
	cfg.Listen, cfg.DataDir, cfg.WavelogStationID = *listen, *dataDir, *wavelogStation
	cfg.POTAHuntedCSV = strings.TrimSpace(*potaCSV)

	st, err := store.New(cfg.DataDir)
	if err != nil {
		log.Fatalf("horstawards: %v", err)
	}

	app, err := newApp(cfg, st)
	if err != nil {
		log.Fatalf("horstawards: %v", err)
	}
	if len(app.sources) == 0 {
		log.Printf("horstawards: WARNING no sources enabled — set WAVELOG_API_KEY (and optionally POTA_CALLSIGN). The index will stay empty and /v1/wanted will report degraded.")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	app.run(ctx)

	srv := &http.Server{Addr: cfg.Listen, Handler: api.New(app).Handler()}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	log.Printf("horstawards %s listening on %s (sources: %s, data: %s)", version, cfg.Listen, app.sourceLabel(), cfg.DataDir)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("horstawards: %v", err)
	}
	// Let in-flight refresh goroutines unwind (bounded) so none is torn down
	// mid-write at process exit.
	app.wait(10 * time.Second)
}

// app is the running service: it owns the source set, the persisted snapshots, the
// live index, and the refresh schedulers. It implements api.Service.
type app struct {
	cfg      config.Config
	store    *store.Store
	sources  []source.Source
	triggers []chan struct{} // per-source manual-refresh signals (buffered, size 1)

	wg sync.WaitGroup // tracks refresh goroutines for graceful shutdown

	mu         sync.RWMutex
	snaps      map[string]*source.Snapshot
	index      *award.Index
	degraded   bool
	persistErr map[string]string // source name -> last persist error ("" = ok)
}

func newApp(cfg config.Config, st *store.Store) (*app, error) {
	snaps, err := st.Load()
	if err != nil {
		return nil, err
	}

	a := &app{cfg: cfg, store: st, snaps: snaps, index: award.NewIndex(), persistErr: map[string]string{}}

	if cfg.WavelogEnabled() {
		if cfg.WavelogStationID == "" {
			log.Printf("horstawards: WARNING WAVELOG_STATION_ID is unset — Wavelog's get_contacts_adif requires a station id and will return HTTP 400 without it (set WAVELOG_STATION_ID or -wavelog-station-id)")
		}
		a.sources = append(a.sources, source.NewWavelogADIF(cfg.WavelogURL, cfg.WavelogAPIKey, cfg.WavelogStationID, cfg.WavelogInterval))
	}
	if cfg.POTACSVEnabled() {
		a.sources = append(a.sources, source.NewPOTACSV(cfg.POTAHuntedCSV, cfg.POTAInterval))
	}
	if cfg.POTAEnabled() {
		a.sources = append(a.sources, source.NewPOTA(cfg.POTABaseURL, cfg.POTACall, cfg.POTAToken, cfg.POTAInterval))
	}
	a.triggers = make([]chan struct{}, len(a.sources))
	for i := range a.triggers {
		a.triggers[i] = make(chan struct{}, 1)
	}

	// Build the initial index from any persisted snapshots, so we serve warm after
	// a restart. (Lock held trivially; no goroutines run yet.)
	a.mu.Lock()
	a.rebuildIndexLocked()
	a.mu.Unlock()
	return a, nil
}

// run starts one refresh goroutine per source, tracked by a.wg for shutdown.
func (a *app) run(ctx context.Context) {
	for i, src := range a.sources {
		a.wg.Add(1)
		go a.sourceLoop(ctx, src, a.triggers[i])
	}
}

// wait blocks until all refresh goroutines have unwound, bounded by a deadline so
// a stuck refresh can't hang shutdown forever.
func (a *app) wait(timeout time.Duration) {
	done := make(chan struct{})
	go func() { a.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
		log.Printf("horstawards: shutdown timed out waiting for refresh goroutines")
	}
}

// sourceLoop refreshes a source at startup and on its interval (or a manual
// trigger), persisting and re-indexing on success. Failures are logged and
// retried on the next tick; never fatal.
func (a *app) sourceLoop(ctx context.Context, src source.Source, trigger <-chan struct{}) {
	defer a.wg.Done()
	a.refreshOne(ctx, src)
	t := time.NewTicker(src.MinInterval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.refreshOne(ctx, src)
		case <-trigger:
			a.refreshOne(ctx, src)
		}
	}
}

// refreshOne runs a single source refresh with a bounded context.
func (a *app) refreshOne(ctx context.Context, src source.Source) {
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	a.mu.RLock()
	prev := a.snaps[src.Name()]
	a.mu.RUnlock()

	snap, err := src.Refresh(cctx, prev)
	if err != nil {
		log.Printf("horstawards: source %q refresh failed: %v", src.Name(), err)
		return
	}
	// Persist (best-effort) and record the outcome for /v1/health. A failed write
	// must not stop us indexing in memory — but it means a restart reverts to the
	// last persisted state, so surface it.
	persistErr := ""
	if err := a.store.SaveSnapshot(snap); err != nil {
		persistErr = err.Error()
		log.Printf("horstawards: persist %q snapshot failed: %v", src.Name(), err)
	}
	// Single critical section: update the snapshot, its persist status, and rebuild
	// the index together so readers never see a snaps/index mismatch.
	a.mu.Lock()
	a.snaps[src.Name()] = snap
	a.persistErr[src.Name()] = persistErr
	a.rebuildIndexLocked()
	a.mu.Unlock()
	log.Printf("horstawards: source %q refreshed — %d slots, stats=%v", src.Name(), len(snap.Slots), snap.Stats)
}

// rebuildIndexLocked folds all current snapshots into a fresh index and recomputes
// the degraded flag (no snapshots, or the freshest is older than StaleAfter). The
// caller must hold a.mu for writing.
func (a *app) rebuildIndexLocked() {
	list := make([]*source.Snapshot, 0, len(a.snaps))
	var freshest time.Time
	for _, snap := range a.snaps {
		list = append(list, snap)
		if snap.TakenAt.After(freshest) {
			freshest = snap.TakenAt
		}
	}
	a.index = source.BuildIndex(list)
	a.degraded = freshest.IsZero() || time.Since(freshest) > a.cfg.StaleAfter
}

// --- api.Service ---

// Snapshot returns the current index and degraded flag together under a single
// RLock, so a /v1/wanted response can't pair an old index with a new degraded
// value (or vice versa) across a concurrent rebuild.
func (a *app) Snapshot() (*award.Index, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.index, a.degraded
}

func (a *app) Degraded() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.degraded
}

func (a *app) Health() map[string]any {
	a.mu.RLock()
	defer a.mu.RUnlock()
	sources := make([]map[string]any, 0, len(a.snaps))
	for name, snap := range a.snaps {
		entry := map[string]any{
			"name":       name,
			"taken_at":   snap.TakenAt,
			"slots":      len(snap.Slots),
			"stats":      snap.Stats,
			"persist_ok": a.persistErr[name] == "",
		}
		if pe := a.persistErr[name]; pe != "" {
			entry["last_persist_error"] = pe
		}
		sources = append(sources, entry)
	}
	return map[string]any{
		"index_slots":     a.index.Len(),
		"programs_loaded": a.index.Programs(),
		"sources_enabled": len(a.sources),
		"sources":         sources,
		"stale_after_sec": int(a.cfg.StaleAfter.Seconds()),
	}
}

// TriggerRefresh signals every source loop to refresh now (non-blocking).
func (a *app) TriggerRefresh() {
	for _, ch := range a.triggers {
		select {
		case ch <- struct{}{}:
		default: // a refresh is already pending for this source
		}
	}
}

func (a *app) sourceLabel() string {
	if len(a.sources) == 0 {
		return "none"
	}
	names := make([]string, 0, len(a.sources))
	for _, s := range a.sources {
		names = append(names, s.Name())
	}
	return strings.Join(names, "+")
}
