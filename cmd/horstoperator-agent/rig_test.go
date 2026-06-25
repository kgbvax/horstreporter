package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRigModeForBackend(t *testing.T) {
	cases := []struct {
		mode   string
		freqHz int64
		want   string
	}{
		{"CW", 7030000, "cw"},
		{"FT8", 14074000, "data"},
		{"SSB", 14200000, "usb"}, // >=10 MHz -> USB
		{"SSB", 7100000, "lsb"},  // <10 MHz -> LSB
		{"", 14074000, "usb"},    // empty -> sideband by freq
		{"USB", 14000000, "usb"},
		{"RTTY", 14080000, "rtty"},
	}
	for _, c := range cases {
		if got := rigModeForBackend(c.mode, c.freqHz); got != c.want {
			t.Errorf("rigModeForBackend(%q,%d)=%q want %q", c.mode, c.freqHz, got, c.want)
		}
	}
}

// newRigServer builds a server with a WaveLogGate backend pointing at mockURL,
// without starting the antenna poller (avoids real UDP).
func newRigServer(mockURL string, controlPermitted bool) *server {
	return &server{
		cfg:    serviceConfig{ControlPermitted: controlPermitted},
		client: newPSTUDPClient(serviceConfig{PSTHost: "127.0.0.1", PSTPort: 12000}),
		rig:    newWaveLogGateBackend(mockURL),
	}
}

func TestHandleRigTune_OK(t *testing.T) {
	var gotPath string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer mock.Close()

	s := newRigServer(mock.URL, true)
	rec := httptest.NewRecorder()
	body := `{"permit_control":true,"freq_hz":14074000,"mode":"FT8"}`
	s.handleRigTune(rec, httptest.NewRequest(http.MethodPost, "/v1/rig/tune", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/14074000/data" {
		t.Errorf("WaveLogGate got path %q, want /14074000/data", gotPath)
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["ok"] != true || resp["rig_mode"] != "data" {
		t.Errorf("unexpected response: %v", resp)
	}
}

func TestHandleRigTune_Gates(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer mock.Close()

	// permit_control=false -> 403
	s := newRigServer(mock.URL, true)
	rec := httptest.NewRecorder()
	s.handleRigTune(rec, httptest.NewRequest(http.MethodPost, "/v1/rig/tune", strings.NewReader(`{"permit_control":false,"freq_hz":14074000}`)))
	if rec.Code != http.StatusForbidden {
		t.Errorf("permit_control=false: status %d want 403", rec.Code)
	}

	// server control disabled -> 403
	s2 := newRigServer(mock.URL, false)
	rec = httptest.NewRecorder()
	s2.handleRigTune(rec, httptest.NewRequest(http.MethodPost, "/v1/rig/tune", strings.NewReader(`{"permit_control":true,"freq_hz":14074000}`)))
	if rec.Code != http.StatusForbidden {
		t.Errorf("control disabled: status %d want 403", rec.Code)
	}

	// missing freq -> 400
	rec = httptest.NewRecorder()
	s.handleRigTune(rec, httptest.NewRequest(http.MethodPost, "/v1/rig/tune", strings.NewReader(`{"permit_control":true}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("no freq: status %d want 400", rec.Code)
	}

	// no backend configured -> 503
	s3 := &server{cfg: serviceConfig{ControlPermitted: true}}
	rec = httptest.NewRecorder()
	s3.handleRigTune(rec, httptest.NewRequest(http.MethodPost, "/v1/rig/tune", strings.NewReader(`{"permit_control":true,"freq_hz":14074000}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no backend: status %d want 503", rec.Code)
	}
}

func TestOperate_TuneOnly(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer mock.Close()

	s := newRigServer(mock.URL, true)
	rec := httptest.NewRecorder()
	// no azimuth_deg → antenna leg skipped (no UDP); rig leg should succeed
	body := `{"permit_control":true,"freq_hz":14074000,"mode":"SSB"}`
	s.handleOperate(rec, httptest.NewRequest(http.MethodPost, "/v1/operate", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	rig, _ := resp["rig"].(map[string]any)
	ant, _ := resp["antenna"].(map[string]any)
	if rig["ok"] != true {
		t.Errorf("rig leg should be ok: %v", rig)
	}
	if ant["ok"] != false || ant["skipped"] == nil {
		t.Errorf("antenna leg should be skipped: %v", ant)
	}
}

func TestRigCORSPreflight(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/rig/tune", func(http.ResponseWriter, *http.Request) {})
	h := withCORS(mux)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/v1/rig/tune", nil))
	if rec.Code != http.StatusNoContent {
		t.Errorf("OPTIONS preflight: status %d want 204", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Error("missing Access-Control-Allow-Origin on preflight")
	}
}
