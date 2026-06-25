// Package store is horstprop's Layer-1 rolling store of reception reports heard
// within the home area, bucketed by (band, tx-square) for O(area) path lookups.
// It only retains "forward path" reports — a DX-side station heard by a receiver
// near home — so a lookup answers "is the DX's grid being heard at home, on this
// band, recently, and how strong".
package store

import (
	"sync"
	"time"

	"horstreporter/cmd/horstprop/internal/geo"
	"horstreporter/internal/propcontract"
)

// Report is a retained reception (SNR + when it was observed).
type Report struct {
	SNRDb int
	At    time.Time
}

type bucketKey struct {
	band string
	x, y int
}

// Store holds the rolling window. Safe for concurrent feed writes + API reads.
type Store struct {
	mu        sync.Mutex
	window    time.Duration
	homeX     int
	homeY     int
	homeRings int
	buckets   map[bucketKey][]Report
	Now       func() time.Time // injectable clock
}

// New builds a store for the given window and home area (in grid-square space).
func New(window time.Duration, homeX, homeY, homeRings int) *Store {
	return &Store{
		window:    window,
		homeX:     homeX,
		homeY:     homeY,
		homeRings: homeRings,
		buckets:   map[bucketKey][]Report{},
		Now:       time.Now,
	}
}

// Window returns the rolling TTL window.
func (s *Store) Window() time.Duration { return s.window }

// Add ingests a report. It keeps only reports whose receiver is within the home
// area (forward path); reverse-path / out-of-area reports are dropped.
func (s *Store) Add(pr propcontract.PropReport) {
	rx, ry, ok := geo.SquareXY(pr.RxLocator)
	if !ok || absInt(rx-s.homeX) > s.homeRings || absInt(ry-s.homeY) > s.homeRings {
		return
	}
	tx, ty, ok := geo.SquareXY(pr.TxLocator)
	if !ok || pr.Band == "" {
		return
	}
	s.mu.Lock()
	k := bucketKey{band: pr.Band, x: tx, y: ty}
	s.buckets[k] = append(s.buckets[k], Report{SNRDb: pr.SNRDb, At: pr.ObservedAt})
	s.mu.Unlock()
}

// Lookup returns non-expired reports on band whose tx-square is within
// matchRings of (dxX, dxY). It prunes expired entries it touches.
func (s *Store) Lookup(band string, dxX, dxY, matchRings int) []Report {
	cutoff := s.Now().Add(-s.window)
	var out []Report
	s.mu.Lock()
	defer s.mu.Unlock()
	for dx := -matchRings; dx <= matchRings; dx++ {
		for dy := -matchRings; dy <= matchRings; dy++ {
			k := bucketKey{band: band, x: dxX + dx, y: dxY + dy}
			reps, ok := s.buckets[k]
			if !ok {
				continue
			}
			kept := reps[:0]
			for _, r := range reps {
				if r.At.After(cutoff) {
					kept = append(kept, r)
					out = append(out, r)
				}
			}
			if len(kept) == 0 {
				delete(s.buckets, k)
			} else {
				s.buckets[k] = kept
			}
		}
	}
	return out
}

// Prune drops all expired reports (call periodically).
func (s *Store) Prune() {
	cutoff := s.Now().Add(-s.window)
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, reps := range s.buckets {
		kept := reps[:0]
		for _, r := range reps {
			if r.At.After(cutoff) {
				kept = append(kept, r)
			}
		}
		if len(kept) == 0 {
			delete(s.buckets, k)
		} else {
			s.buckets[k] = kept
		}
	}
}

// Len returns the number of retained reports (for health/metrics).
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, reps := range s.buckets {
		n += len(reps)
	}
	return n
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
