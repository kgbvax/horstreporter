// Package store persists source snapshots to a single JSON file with atomic
// (temp-file + rename) writes. The access pattern is "load all snapshots at
// startup → rebuild the in-memory index; write whole snapshots on refresh", so a
// key/value document fits and the hot query path never touches disk.
//
// A JSON file (not an embedded KV engine) keeps the build pure-Go with zero new
// dependencies; atomic rename gives the same mid-write corruption safety.
package store

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"horstreporter/internal/awards/source"
)

const fileName = "awards.json"

// Store is the on-disk snapshot store, keyed by source name.
type Store struct {
	dir  string
	path string
	mu   sync.Mutex
}

// doc is the persisted document shape.
type doc struct {
	SchemaVersion int                         `json:"schema_version"`
	Snapshots     map[string]*source.Snapshot `json:"snapshots"`
}

const schemaVersion = 1

// New ensures dir exists and returns a Store. It also sweeps any stale temp files
// left by an abruptly-killed write (the rename is atomic, so a leftover .tmp-* is
// harmless, just clutter).
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("store: create data dir %q: %w", dir, err)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, fileName+".tmp-*")); len(matches) > 0 {
		for _, m := range matches {
			_ = os.Remove(m)
		}
	}
	return &Store{dir: dir, path: filepath.Join(dir, fileName)}, nil
}

// Load reads all persisted snapshots. A missing file is not an error (returns an
// empty map) — first run starts cold.
func (s *Store) Load() (map[string]*source.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Store) loadLocked() (map[string]*source.Snapshot, error) {
	raw, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return map[string]*source.Snapshot{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read %q: %w", s.path, err)
	}
	var d doc
	if err := json.Unmarshal(raw, &d); err != nil {
		// A corrupt store must not be fatal: the service can start cold and re-pull
		// from sources. Move the bad file aside (so the next SaveSnapshot, which
		// load-merges, doesn't keep failing) and start empty.
		bad := s.path + ".corrupt"
		if rerr := os.Rename(s.path, bad); rerr != nil {
			_ = os.Remove(s.path)
		}
		log.Printf("horstawards: store %q was unreadable (%v); moved aside to %q and starting cold", s.path, err, bad)
		return map[string]*source.Snapshot{}, nil
	}
	if d.Snapshots == nil {
		d.Snapshots = map[string]*source.Snapshot{}
	}
	return d.Snapshots, nil
}

// SaveSnapshot persists one source's snapshot, leaving the others intact. The
// write is atomic: a temp file in the same dir is written then renamed over the
// target, so a crash mid-write never corrupts the existing store.
func (s *Store) SaveSnapshot(snap *source.Snapshot) error {
	if snap == nil || snap.Source == "" {
		return fmt.Errorf("store: refusing to save nil/unnamed snapshot")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	snaps, err := s.loadLocked()
	if err != nil {
		return err
	}
	snaps[snap.Source] = snap

	d := doc{SchemaVersion: schemaVersion, Snapshots: snaps}
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return fmt.Errorf("store: marshal: %w", err)
	}

	tmp, err := os.CreateTemp(s.dir, fileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("store: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("store: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("store: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("store: close temp: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("store: rename temp: %w", err)
	}
	return nil
}
