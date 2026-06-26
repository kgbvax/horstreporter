package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"horstreporter/cmd/horstawards/internal/award"
	"horstreporter/internal/awardcontract"
)

type fakeService struct {
	ix         *award.Index
	degraded   bool
	refreshHit int
}

func (f *fakeService) Snapshot() (*award.Index, bool) { return f.ix, f.degraded }
func (f *fakeService) Degraded() bool                 { return f.degraded }
func (f *fakeService) Health() map[string]any         { return map[string]any{"index_slots": f.ix.Len()} }
func (f *fakeService) TriggerRefresh()                { f.refreshHit++ }

func newTestServer(degraded bool) (*fakeService, http.Handler) {
	ix := award.NewIndex()
	ix.MarkProgram(award.ProgDXCC)
	// entity 24 absent → ATNO
	f := &fakeService{ix: ix, degraded: degraded}
	return f, New(f).Handler()
}

// jsonReq builds a POST with the application/json content type the handlers now
// require.
func jsonReq(path string, body []byte) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestHandleWantedOK(t *testing.T) {
	_, h := newTestServer(false)
	body, _ := json.Marshal(awardcontract.WantedRequest{
		PermitLookup: true,
		Spots:        []awardcontract.WantedSpot{{ID: "x", DXCCID: "24", Band: "20m", Mode: "SSB"}},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jsonReq("/v1/wanted", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp awardcontract.WantedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.OK || len(resp.Results) != 1 {
		t.Fatalf("resp = %#v", resp)
	}
	if got := resp.Results[0].Needed; len(got) != 1 || got[0] != "dxcc" {
		t.Errorf("needed = %v, want [dxcc]", got)
	}
}

func TestHandleWantedRequiresJSON(t *testing.T) {
	_, h := newTestServer(false)
	// No Content-Type → 415 (forces a preflight for cross-origin browsers).
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/wanted", bytes.NewReader([]byte(`{"permit_lookup":true}`)))
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want 415", rec.Code)
	}
}

func TestHandleWantedPermitGate(t *testing.T) {
	_, h := newTestServer(false)
	body, _ := json.Marshal(awardcontract.WantedRequest{PermitLookup: false})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jsonReq("/v1/wanted", body))
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestHandleWantedMethod(t *testing.T) {
	_, h := newTestServer(false)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/wanted", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestHandleWantedBadJSON(t *testing.T) {
	_, h := newTestServer(false)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jsonReq("/v1/wanted", []byte("{bad")))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandleWantedDegraded(t *testing.T) {
	_, h := newTestServer(true)
	body, _ := json.Marshal(awardcontract.WantedRequest{PermitLookup: true, Spots: []awardcontract.WantedSpot{{ID: "x"}}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jsonReq("/v1/wanted", body))
	var resp awardcontract.WantedResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.Degraded {
		t.Errorf("expected degraded=true")
	}
}

func TestHandleRefreshGate(t *testing.T) {
	f, h := newTestServer(false)
	// Without permit → 403, no trigger.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jsonReq("/v1/refresh", []byte(`{"permit_refresh":false}`)))
	if rec.Code != http.StatusForbidden || f.refreshHit != 0 {
		t.Errorf("ungated refresh: code=%d hits=%d", rec.Code, f.refreshHit)
	}
	// With permit in JSON body → 202, trigger.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, jsonReq("/v1/refresh", []byte(`{"permit_refresh":true}`)))
	if rec.Code != http.StatusAccepted || f.refreshHit != 1 {
		t.Errorf("gated refresh: code=%d hits=%d", rec.Code, f.refreshHit)
	}
}

// The old query-param permit path is gone: a simple cross-site POST (no JSON
// content type) can no longer trigger a refresh.
func TestHandleRefreshNoQueryParamBypass(t *testing.T) {
	f, h := newTestServer(false)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/refresh?permit_refresh=true", nil))
	if rec.Code == http.StatusAccepted || f.refreshHit != 0 {
		t.Errorf("query-param bypass still works: code=%d hits=%d", rec.Code, f.refreshHit)
	}
}

func TestHandleHealth(t *testing.T) {
	_, h := newTestServer(false)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	if m["service"] != "horstawards" {
		t.Errorf("health = %#v", m)
	}
}

func TestCORSLoopbackAllowed(t *testing.T) {
	_, h := newTestServer(false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/v1/wanted", nil)
	req.Header.Set("Origin", "http://127.0.0.1:5173")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "http://127.0.0.1:5173" {
		t.Errorf("loopback origin should be allowed, got %q", rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORSCrossOriginRejected(t *testing.T) {
	_, h := newTestServer(false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/v1/wanted", nil)
	req.Header.Set("Origin", "https://evil.example")
	h.ServeHTTP(rec, req)
	// Preflight gets no Allow-Origin → browser blocks the real cross-origin POST.
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("cross-origin must not receive Allow-Origin, got %q", rec.Header().Get("Access-Control-Allow-Origin"))
	}
}
