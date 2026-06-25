package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"horstreporter/cmd/horstprop/internal/engine"
	"horstreporter/internal/propcontract"
)

func newTestServer() http.Handler {
	return New(engine.New("JO32we", nil, nil, nil, nil, 3), "JO32we", false, "L2-mufgate").Handler()
}

func TestScoreOK(t *testing.T) {
	h := newTestServer()
	body := `{"dx_call":"VK9XX","freq_hz":14074000,"mode":"FT8","grid":"OH29"}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/score", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var sc propcontract.Score
	if err := json.Unmarshal(rec.Body.Bytes(), &sc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sc.DXCall != "VK9XX" || sc.Band != "20m" || sc.Grade != propcontract.GradeUnknown {
		t.Errorf("unexpected score: %+v", sc)
	}
	// No store → neutral 50 / confidence 0.15 / grade "?" (§7.4).
	if sc.Score == nil || *sc.Score != 50 || sc.Confidence != 0.15 {
		t.Errorf("expected neutral 50/0.15, got score=%v conf=%v", sc.Score, sc.Confidence)
	}
	if sc.DistanceKm < 11000 || sc.DistanceKm > 12000 {
		t.Errorf("geometry distance %.0f km looks wrong", sc.DistanceKm)
	}
	if sc.Layers.Empirical.Available || sc.Layers.MUFGate.Available || sc.Layers.Model.Available {
		t.Errorf("all layers should be unavailable in skeleton: %+v", sc.Layers)
	}
}

func TestScoreValidation(t *testing.T) {
	h := newTestServer()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/score", strings.NewReader(`{"mode":"CW"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing fields: status %d want 400", rec.Code)
	}

	// GET is now supported; with no params it fails validation (400, not 405).
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/score", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("GET /v1/score (no params): status %d want 400", rec.Code)
	}

	// GET with query params scores successfully.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/score?dx_call=W1X&freq_hz=14074000&grid=FN20", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /v1/score with params: status %d want 200", rec.Code)
	}

	// PUT is rejected.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/v1/score", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT /v1/score: status %d want 405", rec.Code)
	}
}

func TestDebugEndpoint(t *testing.T) {
	h := newTestServer()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/debug?dx_call=W1X&freq_hz=14074000&grid=FN20", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/debug: status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`"score"`, `"diagnostics"`, `"control_points"`, `"home_grid"`} {
		if !strings.Contains(body, want) {
			t.Errorf("debug body missing %s: %s", want, body)
		}
	}
}

func TestHealth(t *testing.T) {
	h := newTestServer()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("health status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "L2-mufgate") {
		t.Errorf("health body unexpected: %s", rec.Body.String())
	}
}
