package engine

import (
	"testing"

	"horstreporter/cmd/horstprop/internal/model"
	"horstreporter/internal/propcontract"
)

type fakeModel struct {
	score int
	conf  float64
	ok    bool
}

func (f fakeModel) Predict(model.Request) (model.Prediction, bool) {
	return model.Prediction{Score: f.score, Confidence: f.conf, Detail: map[string]any{"src": "fake"}}, f.ok
}

// With no empirical store, the model is the fallback base (§7.4).
func TestModelFallbackBase(t *testing.T) {
	e := New("JO31", nil, nil, nil, fakeModel{score: 70, conf: 0.5, ok: true}, 3)
	sc := e.Score(propcontract.Spot{DXCall: "X", FreqHz: 14074000, Grid: "OH29"})
	if !sc.Layers.Model.Available {
		t.Fatal("model layer should be available")
	}
	if sc.Score == nil || *sc.Score != 70 {
		t.Errorf("no L1/L2 → model is the base, want 70, got %v", sc.Score)
	}
	if sc.Confidence != 0.5 {
		t.Errorf("confidence should be the model's 0.5, got %.2f", sc.Confidence)
	}
}

// Disabled model abstains → no model layer, neutral result.
func TestModelDisabledAbstains(t *testing.T) {
	e := New("JO31", nil, nil, nil, model.Disabled{}, 3)
	sc := e.Score(propcontract.Spot{DXCall: "X", FreqHz: 14074000, Grid: "OH29"})
	if sc.Layers.Model.Available {
		t.Error("Disabled model must abstain")
	}
	if sc.Score == nil || *sc.Score != 50 {
		t.Errorf("disabled model, no other layers → neutral 50, got %v", sc.Score)
	}
}
