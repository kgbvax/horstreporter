package engine

import (
	"testing"
	"time"

	"horstreporter/cmd/horstprop/internal/geo"
	"horstreporter/cmd/horstprop/internal/store"
	"horstreporter/internal/propcontract"
)

var fixedNow = time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)

// homeArea returns a store centred on JO31 (home), area 3 rings, 30-min window.
func newStore() *store.Store {
	hx, hy, _ := geo.SquareXY("JO31")
	st := store.New(30*time.Minute, hx, hy, 3)
	st.Now = func() time.Time { return fixedNow }
	return st
}

func TestScoreNeutralWhenNoData(t *testing.T) {
	e := New("JO31", nil, newStore(), nil, nil, 3)
	e.now = func() time.Time { return fixedNow }
	sc := e.Score(propcontract.Spot{DXCall: "VK9XX", FreqHz: 14074000, Grid: "OH29"})
	if sc.Score == nil || *sc.Score != 50 || sc.Grade != propcontract.GradeUnknown {
		t.Fatalf("expected neutral 50/?, got score=%v grade=%s", sc.Score, sc.Grade)
	}
	if sc.Layers.Empirical.Available {
		t.Error("empirical should abstain with no reports")
	}
}

func TestScoreEmpiricalFromReports(t *testing.T) {
	st := newStore()
	// DX at OH29; receivers near home (JO31 area) hearing OH29-ish grids on 20m.
	// Strong, fresh reports → high score, GO-ish.
	for _, snr := range []int{2, 6, -3, 8, 1} {
		st.Add(propcontract.PropReport{
			TxLocator: "OH29", RxLocator: "JO31", SNRDb: snr, Band: "20m",
			Source: "mqtt", ObservedAt: fixedNow.Add(-2 * time.Minute),
		})
	}
	e := New("JO31", nil, st, nil, nil, 3)
	e.now = func() time.Time { return fixedNow }

	sc := e.Score(propcontract.Spot{DXCall: "VK9XX", FreqHz: 14074000, Grid: "OH29"})
	if !sc.Layers.Empirical.Available {
		t.Fatal("empirical layer should be available")
	}
	if sc.Score == nil || *sc.Score < 70 {
		t.Errorf("strong fresh FT8 reports should score high, got %v", sc.Score)
	}
	if sc.Confidence < 0.55 {
		t.Errorf("5 fresh reports should give decent confidence, got %.2f", sc.Confidence)
	}
	if n, _ := sc.Layers.Empirical.Detail["n_reports"].(int); n != 5 {
		t.Errorf("expected 5 reports counted, got %d", n)
	}

	// A different band with no data → abstain → neutral.
	sc40 := e.Score(propcontract.Spot{DXCall: "VK9XX", FreqHz: 7074000, Grid: "OH29"})
	if sc40.Layers.Empirical.Available || sc40.Score == nil || *sc40.Score != 50 {
		t.Errorf("40m has no data → neutral, got available=%v score=%v", sc40.Layers.Empirical.Available, sc40.Score)
	}
}

func TestReversePathReportsIgnored(t *testing.T) {
	st := newStore()
	// Receiver far from home (PM95) → forward-path filter drops it.
	st.Add(propcontract.PropReport{
		TxLocator: "OH29", RxLocator: "PM95", SNRDb: 20, Band: "20m",
		Source: "mqtt", ObservedAt: fixedNow.Add(-1 * time.Minute),
	})
	e := New("JO31", nil, st, nil, nil, 3)
	e.now = func() time.Time { return fixedNow }
	sc := e.Score(propcontract.Spot{DXCall: "VK9XX", FreqHz: 14074000, Grid: "OH29"})
	if sc.Layers.Empirical.Available {
		t.Error("report received far from home must be ignored (forward-path only)")
	}
}

func TestFt8SnrToScoreMonotonic(t *testing.T) {
	prev := ft8SnrToScore(-30)
	for snr := -25; snr <= 20; snr += 5 {
		cur := ft8SnrToScore(snr)
		if cur < prev {
			t.Errorf("ft8SnrToScore not monotonic at %d dB: %.1f < %.1f", snr, cur, prev)
		}
		prev = cur
	}
}
