package store

import (
	"testing"
	"time"

	"horstreporter/cmd/horstprop/internal/geo"
	"horstreporter/internal/propcontract"
)

var now = time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)

func newStore() *Store {
	hx, hy, _ := geo.SquareXY("JO31")
	s := New(30*time.Minute, hx, hy, 3)
	s.Now = func() time.Time { return now }
	return s
}

func add(s *Store, tx, rx string, snr int, age time.Duration) {
	s.Add(propcontract.PropReport{
		TxLocator: tx, RxLocator: rx, SNRDb: snr, Band: "20m",
		Source: "mqtt", ObservedAt: now.Add(-age),
	})
}

func TestAddForwardPathOnly(t *testing.T) {
	s := newStore()
	add(s, "OH29", "JO31", 10, time.Minute) // rx in home area → kept
	add(s, "OH29", "JO30", 10, time.Minute) // rx neighbour → kept
	add(s, "OH29", "PM95", 10, time.Minute) // rx far → dropped
	add(s, "OH29", "", 10, time.Minute)     // no rx → dropped
	if got := s.Len(); got != 2 {
		t.Fatalf("Len=%d want 2 (only home-area receivers retained)", got)
	}
}

func TestLookupMatchRings(t *testing.T) {
	s := newStore()
	dxX, dxY, _ := geo.SquareXY("OH29")
	add(s, "OH29", "JO31", 5, time.Minute) // exact DX square
	add(s, "OH28", "JO31", 7, time.Minute) // adjacent square (within rings)
	add(s, "AA00", "JO31", 9, time.Minute) // far from DX → outside match radius

	got := s.Lookup("20m", dxX, dxY, 3)
	if len(got) != 2 {
		t.Fatalf("Lookup got %d reports, want 2 (within 3 rings of DX)", len(got))
	}
	if len(s.Lookup("40m", dxX, dxY, 3)) != 0 {
		t.Error("wrong band should return nothing")
	}
}

func TestPruneTTL(t *testing.T) {
	s := newStore()
	add(s, "OH29", "JO31", 5, 5*time.Minute)  // fresh
	add(s, "OH29", "JO31", 5, 45*time.Minute) // expired (> 30m window)
	if s.Len() != 2 {
		t.Fatalf("pre-prune Len=%d want 2", s.Len())
	}
	s.Prune()
	if s.Len() != 1 {
		t.Fatalf("post-prune Len=%d want 1 (expired dropped)", s.Len())
	}
}
