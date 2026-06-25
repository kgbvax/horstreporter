package engine

import (
	"testing"
	"time"

	"horstreporter/internal/propcontract"
)

type fakeMUF struct {
	muf, age float64
	ok       bool
}

func (f fakeMUF) MUFAt(lat, lon float64) (float64, float64, bool) {
	return f.muf, f.age, f.ok
}

func TestMUFGateOpenAndClosed(t *testing.T) {
	spot := propcontract.Spot{DXCall: "X", FreqHz: 14074000, Grid: "OH29"} // 20m ≈ 14.07 MHz

	// Open: MUF 28 → r≈0.5 → gate 1.0. No L1 → score 50, conf = L2 conf.
	open := New("JO31", nil, nil, fakeMUF{muf: 28, age: 10, ok: true}, nil, 3)
	so := open.Score(spot)
	if !so.Layers.MUFGate.Available || *so.Layers.MUFGate.Gate < 0.99 {
		t.Fatalf("open gate expected ~1.0, got %+v", so.Layers.MUFGate)
	}
	if *so.Score != 50 || so.Confidence < 0.5 {
		t.Errorf("open MUF, no L1 → 50 at ~0.6 conf, got score=%d conf=%.2f", *so.Score, so.Confidence)
	}

	// Above MUF: MUF 7 → r≈2.0 → gate 0 → score 0.
	closed := New("JO31", nil, nil, fakeMUF{muf: 7, age: 10, ok: true}, nil, 3)
	sc := closed.Score(spot)
	if g := *sc.Layers.MUFGate.Gate; g > 0.01 {
		t.Errorf("above-MUF gate expected ~0, got %.2f", g)
	}
	if *sc.Score != 0 {
		t.Errorf("closed path → score 0, got %d", *sc.Score)
	}
}

// Empirical-first: a confident, well-supported path resists a closing MUF gate;
// a weak path does not.
func TestMUFGateSkippedOnVHF(t *testing.T) {
	muf := fakeMUF{muf: 7, age: 10, ok: true} // would gate hard on HF
	e := New("JO31", nil, nil, muf, nil, 3)
	// 2m (144.174 MHz): MUF irrelevant → gate must be unavailable.
	for _, freq := range []int64{50313000, 144174000} {
		sc := e.Score(propcontract.Spot{DXCall: "X", FreqHz: freq, Grid: "OH29"})
		if sc.Layers.MUFGate.Available {
			t.Errorf("freq %d Hz (VHF): MUF gate should be unavailable, got %+v", freq, sc.Layers.MUFGate)
		}
	}
	// 20m (HF) still gated.
	sc := e.Score(propcontract.Spot{DXCall: "X", FreqHz: 14074000, Grid: "OH29"})
	if !sc.Layers.MUFGate.Available {
		t.Error("20m (HF): MUF gate should still apply")
	}
}

func TestEmpiricalFirstVsMUFGate(t *testing.T) {
	add := func(st interface {
		Add(propcontract.PropReport)
	}, n int) {
		for i := 0; i < n; i++ {
			st.Add(propcontract.PropReport{
				TxLocator: "OH29", RxLocator: "JO31", SNRDb: 8, Band: "20m",
				Source: "mqtt", ObservedAt: fixedNow.Add(-2 * time.Minute),
			})
		}
	}
	closed := fakeMUF{muf: 7, age: 10, ok: true} // 20m well above MUF → raw gate ~0

	// Strong empirical (many fresh reports → high confidence) → protected.
	stStrong := newStore()
	add(stStrong, 12)
	eStrong := New("JO31", nil, stStrong, closed, nil, 3)
	eStrong.now = func() time.Time { return fixedNow }
	scStrong := eStrong.Score(propcontract.Spot{DXCall: "X", FreqHz: 14074000, Grid: "OH29"})
	if !scStrong.Layers.Empirical.Available || !scStrong.Layers.MUFGate.Available {
		t.Fatal("expected both layers available")
	}
	if *scStrong.Score < 50 {
		t.Errorf("strong empirical should resist the gate (≥50), got %d", *scStrong.Score)
	}

	// Single weak report (low confidence) → gate fully bites.
	stWeak := newStore()
	add(stWeak, 1)
	eWeak := New("JO31", nil, stWeak, closed, nil, 3)
	eWeak.now = func() time.Time { return fixedNow }
	scWeak := eWeak.Score(propcontract.Spot{DXCall: "X", FreqHz: 14074000, Grid: "OH29"})
	if *scWeak.Score > 15 {
		t.Errorf("weak empirical should not resist a closed gate, got %d", *scWeak.Score)
	}
	if *scWeak.Score >= *scStrong.Score {
		t.Errorf("strong (%d) should outrank weak (%d) under the same closed gate", *scStrong.Score, *scWeak.Score)
	}
}
