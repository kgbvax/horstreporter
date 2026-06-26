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
	trigs   []chan struct{} // per-source manual-refresh signals (buffered, size 1)

	mu         sync.RWMutex
	snaps      map[string]*source.Snapshot
	index      *award.Index
	degraded   bool
	persistErr map[string]string // source name -> last persist error ("" = ok)
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
	m := &Manager{cfg: cfg, store: st, snaps: snaps, index: award.NewIndex(), persistErr: map[string]string{}}

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
	m.trigs = make([]chan struct{}, len(m.sources))
	for i := range m.trigs {
		m.trigs[i] = make(chan struct{}, 1)
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
	for i, src := range m.sources {
		go m.sourceLoop(ctx, src, m.trigs[i])
	}
}

func (m *Manager) sourceLoop(ctx context.Context, src source.Source, trigger <-chan struct{}) {
	m.refreshOne(ctx, src)
	t := time.NewTicker(src.MinInterval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.refreshOne(ctx, src)
		case <-trigger:
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

// TriggerRefresh signals every source to refresh now (non-blocking).
func (m *Manager) TriggerRefresh() {
	for _, ch := range m.trigs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Health returns a status snapshot for the agent's /v1/status.
func (m *Manager) Health() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	srcs := make([]map[string]any, 0, len(m.snaps))
	for name, snap := range m.snaps {
		entry := map[string]any{
			"name":       name,
			"taken_at":   snap.TakenAt,
			"slots":      len(snap.Slots),
			"stats":      snap.Stats,
			"persist_ok": m.persistErr[name] == "",
		}
		if pe := m.persistErr[name]; pe != "" {
			entry["last_persist_error"] = pe
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
