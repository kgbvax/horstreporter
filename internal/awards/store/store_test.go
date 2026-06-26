package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"horstreporter/internal/awards/award"
	"horstreporter/internal/awards/source"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	snap := &source.Snapshot{
		Source:   "wavelog-adif",
		TakenAt:  time.Now().UTC().Truncate(time.Second),
		Programs: []award.Program{award.ProgDXCC, award.ProgWAS},
		Slots: []award.SlotStatus{
			{Slot: award.Slot{Program: award.ProgDXCC, Entity: "24"}, Status: award.StatusWorked},
			{Slot: award.Slot{Program: award.ProgWAS, Entity: "CT", Band: "20m"}, Status: award.StatusConfirmed},
		},
		Cursor: "42",
		Stats:  map[string]int{"qso_count": 2},
	}
	if err := s.SaveSnapshot(snap); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// Reopen from disk (new Store) and verify.
	s2, _ := New(dir)
	got, err := s2.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	g := got["wavelog-adif"]
	if g == nil {
		t.Fatalf("snapshot not loaded")
	}
	if g.Cursor != "42" || len(g.Slots) != 2 || g.Stats["qso_count"] != 2 {
		t.Errorf("round-trip mismatch: %#v", g)
	}
	if g.Slots[1].Status != award.StatusConfirmed {
		t.Errorf("status not preserved: %v", g.Slots[1].Status)
	}
}

func TestSaveSnapshotPreservesOthers(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	if err := s.SaveSnapshot(&source.Snapshot{Source: "a", TakenAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSnapshot(&source.Snapshot{Source: "b", TakenAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Load()
	if got["a"] == nil || got["b"] == nil {
		t.Errorf("saving b dropped a: %#v", got)
	}
}

func TestLoadMissingFile(t *testing.T) {
	s, _ := New(t.TempDir())
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load on missing file should not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map, got %#v", got)
	}
}

func TestLoadCorruptFileStartsCold(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	// Write a garbled store file.
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("corrupt file must not be fatal: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected cold start (empty map), got %#v", got)
	}
	// Bad file moved aside, and a subsequent save works.
	if _, err := os.Stat(filepath.Join(dir, fileName+".corrupt")); err != nil {
		t.Errorf("expected corrupt file moved aside: %v", err)
	}
	if err := s.SaveSnapshot(&source.Snapshot{Source: "a", TakenAt: time.Now()}); err != nil {
		t.Errorf("save after corrupt recovery failed: %v", err)
	}
}

func TestSaveNilRejected(t *testing.T) {
	s, _ := New(t.TempDir())
	if err := s.SaveSnapshot(nil); err == nil {
		t.Errorf("expected error saving nil snapshot")
	}
	if err := s.SaveSnapshot(&source.Snapshot{Source: ""}); err == nil {
		t.Errorf("expected error saving unnamed snapshot")
	}
}
