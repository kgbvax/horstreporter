// Package source defines the pluggable progress-source interface and the two v1
// adapters (Wavelog ADIF export, POTA API). Each source pulls upstream, computes
// the award slots it covers, and returns a Snapshot. The store persists snapshots
// and BuildIndex folds them into the fast award.Index the query hits.
//
// Adding a logger or award authority later is a new adapter implementing Source —
// no change to the engine or contract.
package source

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"horstreporter/cmd/horstawards/internal/award"
)

// refuseCrossHostRedirect is an http.Client.CheckRedirect policy that blocks
// redirects to a different host. Adapters that carry the read-only Wavelog key in
// the request body use it so a misconfigured/MITM cross-host 307/308 can't replay
// the key to another host (Go strips the Authorization header on cross-host
// redirects but not the body).
func refuseCrossHostRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > 0 && req.URL.Host != via[0].URL.Host {
		return fmt.Errorf("refusing cross-host redirect to %q", req.URL.Host)
	}
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	return nil
}

// Snapshot is one source's contribution to the index at a point in time: the flat
// list of satisfied slots, which programs it authoritatively covers, and an opaque
// cursor for incremental sync. Persisted as-is by the store (JSON).
type Snapshot struct {
	Source   string             `json:"source"`
	TakenAt  time.Time          `json:"taken_at"`
	Programs []award.Program    `json:"programs"`
	Slots    []award.SlotStatus `json:"slots"`
	Cursor   string             `json:"cursor,omitempty"`
	Stats    map[string]int     `json:"stats,omitempty"`
}

// Source is a pluggable award-progress provider.
type Source interface {
	// Name is the stable snapshot key (e.g. "wavelog-adif").
	Name() string
	// Programs lists the award programs this source authoritatively covers, so the
	// index can mark them loaded even when the source contributes zero slots.
	Programs() []award.Program
	// Refresh pulls upstream and returns a fresh Snapshot. prev is the previously
	// persisted snapshot (nil on first run) for incremental sync.
	Refresh(ctx context.Context, prev *Snapshot) (*Snapshot, error)
	// MinInterval is the recommended refresh cadence.
	MinInterval() time.Duration
}

// BuildIndex folds a set of snapshots into a fresh award.Index, taking the best
// status per slot across sources and marking every covered program as loaded.
func BuildIndex(snaps []*Snapshot) *award.Index {
	ix := award.NewIndex()
	for _, snap := range snaps {
		if snap == nil {
			continue
		}
		for _, p := range snap.Programs {
			ix.MarkProgram(p)
		}
		for _, ss := range snap.Slots {
			ix.Set(ss.Slot, ss.Status)
		}
	}
	return ix
}

// foldSlots accumulates SlotStatus values keyed by slot, keeping the best status,
// and returns the flattened list. Helper for adapters.
func foldSlots(m map[string]award.SlotStatus) []award.SlotStatus {
	out := make([]award.SlotStatus, 0, len(m))
	for _, ss := range m {
		out = append(out, ss)
	}
	return out
}

// putSlot records a slot at status into m, keeping the best status seen.
func putSlot(m map[string]award.SlotStatus, s award.Slot, st award.Status) {
	k := s.Key()
	if cur, ok := m[k]; !ok || st > cur.Status {
		m[k] = award.SlotStatus{Slot: s, Status: st}
	}
}
