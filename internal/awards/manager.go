package awards

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"horstreporter/internal/awardcontract"
	"horstreporter/internal/awards/award"
	"horstreporter/internal/awards/source"
	"horstreporter/internal/awards/store"
)

// Manager owns the operator's award-progress index: the source set, the persisted
// snapshots, the live index, and the per-source refresh schedulers. It runs
// in-process inside the agent; Evaluate is a pure in-memory lookup (no I/O).
type Manager struct {
	cfg     Config
	store   *store.Store
	sources []source.Source

	mu         sync.RWMutex
	snaps      map[string]*source.Snapshot
	index      *award.Index
	degraded   bool
	persistErr map[string]string // source name -> last persist error ("" = ok)
	lastErr    map[string]string // source name -> last refresh error ("" = ok)
}

// New opens the store, builds the enabled sources, and warms the index from any
// persisted snapshots. Call Run to start the refresh schedulers.
func New(cfg Config) (*Manager, error) {
	st, err := store.New(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	snaps, err := st.Load()
	if err != nil {
		return nil, err
	}
	m := &Manager{cfg: cfg, store: st, snaps: snaps, index: award.NewIndex(), persistErr: map[string]string{}, lastErr: map[string]string{}}

	if cfg.WavelogEnabled() {
		if strings.TrimSpace(cfg.WavelogStationID) == "" {
			log.Printf("[WARN] awards: WAVELOG_STATION_ID is unset — Wavelog's get_contacts_adif requires a station id and returns HTTP 400 without it")
		}
		m.sources = append(m.sources, source.NewWavelogADIF(cfg.WavelogURL, cfg.WavelogAPIKey, cfg.WavelogStationID, cfg.WavelogInterval))
	}
	if cfg.POTACSVEnabled() {
		m.sources = append(m.sources, source.NewPOTACSV(cfg.POTAHuntedCSV, cfg.POTAInterval))
	}
	if cfg.POTAEnabled() {
		m.sources = append(m.sources, source.NewPOTA(cfg.POTABaseURL, cfg.POTACall, cfg.POTAToken, cfg.POTAInterval))
	}

	// Warm the index from persisted snapshots so we serve immediately after a
	// restart (no goroutines running yet; lock held trivially).
	m.mu.Lock()
	m.rebuildIndexLocked()
	m.mu.Unlock()
	return m, nil
}

// Run starts one refresh goroutine per source. The agent passes
// context.Background() (it has no graceful-shutdown context); goroutines exit when
// the process does.
func (m *Manager) Run(ctx context.Context) {
	for _, src := range m.sources {
		go m.sourceLoop(ctx, src)
	}
}

func (m *Manager) sourceLoop(ctx context.Context, src source.Source) {
	m.refreshOne(ctx, src)
	t := time.NewTicker(src.MinInterval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.refreshOne(ctx, src)
		}
	}
}

func (m *Manager) refreshOne(ctx context.Context, src source.Source) {
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	m.mu.RLock()
	prev := m.snaps[src.Name()]
	m.mu.RUnlock()

	snap, err := src.Refresh(cctx, prev)
	if err != nil {
		m.mu.Lock()
		m.lastErr[src.Name()] = err.Error()
		m.mu.Unlock()
		log.Printf("[WARN] awards: source %q refresh failed: %v", src.Name(), err)
		return
	}
	persistErr := ""
	if err := m.store.SaveSnapshot(snap); err != nil {
		persistErr = err.Error()
		log.Printf("[WARN] awards: persist %q snapshot failed: %v", src.Name(), err)
	}
	m.mu.Lock()
	m.snaps[src.Name()] = snap
	m.persistErr[src.Name()] = persistErr
	delete(m.lastErr, src.Name())
	m.rebuildIndexLocked()
	m.mu.Unlock()
	log.Printf("[INFO] awards: source %q refreshed — %d slots, stats=%v", src.Name(), len(snap.Slots), snap.Stats)
}

// rebuildIndexLocked folds all snapshots into a fresh index and recomputes the
// degraded flag. Caller holds m.mu for writing.
func (m *Manager) rebuildIndexLocked() {
	list := make([]*source.Snapshot, 0, len(m.snaps))
	var freshest time.Time
	for _, snap := range m.snaps {
		list = append(list, snap)
		if snap.TakenAt.After(freshest) {
			freshest = snap.TakenAt
		}
	}
	m.index = source.BuildIndex(list)
	m.degraded = freshest.IsZero() || time.Since(freshest) > m.cfg.StaleAfter
}

// Evaluate intersects a resolved spot against the current progress index. Pure
// in-memory; safe for concurrent use.
func (m *Manager) Evaluate(sp awardcontract.WantedSpot) awardcontract.WantedResult {
	m.mu.RLock()
	ix := m.index
	m.mu.RUnlock()
	return ix.Evaluate(sp)
}

// Degraded reports whether the index has never loaded or is stale beyond
// StaleAfter — i.e. its verdicts should be treated as unavailable (the agent then
// falls back to Wavelog confirmed-flags).
func (m *Manager) Degraded() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.degraded
}

// RefreshNow refreshes every source synchronously (sequentially) and returns when
// the index reflects them. Used at first start for a warm index and by tests; the
// scheduled path uses the per-source goroutines started by Run.
func (m *Manager) RefreshNow(ctx context.Context) {
	for _, src := range m.sources {
		m.refreshOne(ctx, src)
	}
}

// Health returns a status snapshot for the agent's /v1/status.
func (m *Manager) Health() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	// Iterate the currently-enabled sources (not just loaded snapshots) so a
	// source that has never loaded still appears, with its last_error.
	srcs := make([]map[string]any, 0, len(m.sources))
	for _, src := range m.sources {
		name := src.Name()
		entry := map[string]any{"name": name, "persist_ok": m.persistErr[name] == ""}
		if snap := m.snaps[name]; snap != nil {
			entry["taken_at"] = snap.TakenAt
			entry["slots"] = len(snap.Slots)
			entry["stats"] = snap.Stats
		} else {
			entry["slots"] = 0
		}
		if pe := m.persistErr[name]; pe != "" {
			entry["last_persist_error"] = pe
		}
		if le := m.lastErr[name]; le != "" {
			entry["last_error"] = le
		}
		srcs = append(srcs, entry)
	}
	return map[string]any{
		"degraded":        m.degraded,
		"index_slots":     m.index.Len(),
		"programs_loaded": m.index.Programs(),
		"sources_enabled": len(m.sources),
		"sources":         srcs,
	}
}

// Diagnostic returns a one-line, human-readable reason for the current state of
// each enabled source (last refresh error, or "no data yet"), for the agent's
// readiness panel. Empty when every source has loaded cleanly.
func (m *Manager) Diagnostic() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var parts []string
	for _, src := range m.sources {
		name := src.Name()
		switch {
		case m.lastErr[name] != "":
			parts = append(parts, name+": "+m.lastErr[name])
		case m.snaps[name] == nil:
			parts = append(parts, name+": no data yet")
		}
	}
	return strings.Join(parts, "; ")
}

// SourceLabel is a short "+"-joined list of enabled source names, for logging.
func (m *Manager) SourceLabel() string {
	if len(m.sources) == 0 {
		return "none"
	}
	names := make([]string, 0, len(m.sources))
	for _, s := range m.sources {
		names = append(names, s.Name())
	}
	return strings.Join(names, "+")
}
