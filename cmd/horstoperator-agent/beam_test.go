package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeBeam is a test double for beamController that records published modes.
type fakeBeam struct {
	published  []string
	online     bool
	mode       string
	publishErr error
}

func (f *fakeBeam) Publish(mode string) error {
	if f.publishErr != nil {
		return f.publishErr
	}
	f.published = append(f.published, mode)
	return nil
}

func (f *fakeBeam) Status() ultrabeamState {
	return ultrabeamState{Mode: f.mode, Online: f.online}
}

func (f *fakeBeam) Online() bool { return f.online }

func newBeamTestServer(ub beamController, controlPermitted bool) *server {
	return &server{
		cfg: serviceConfig{
			ControlPermitted: controlPermitted,
			Timeout:          20 * time.Millisecond,
			Beamwidth3dBDeg:  60,
			AllowedModes:     []string{"forward", "backward", "bidirectional"},
			PSTHost:          "127.0.0.1",
			PSTPort:          1,
		},
		ub: ub,
	}
}

func postBeam(t *testing.T, s *server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/antenna/beam", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleBeam(rec, req)
	return rec
}

func TestHandleBeamPublishesCanonicalReverse(t *testing.T) {
	fb := &fakeBeam{online: true}
	s := newBeamTestServer(fb, true)

	rec := postBeam(t, s, `{"permit_control":true,"mode":"reverse"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK   bool   `json:"ok"`
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK || resp.Mode != "reverse" {
		t.Fatalf("resp = %+v, want ok=true mode=reverse", resp)
	}
	if len(fb.published) != 1 || fb.published[0] != "reverse" {
		t.Fatalf("published = %v, want [reverse]", fb.published)
	}
}

func TestHandleBeamRequiresPermitControl(t *testing.T) {
	s := newBeamTestServer(&fakeBeam{online: true}, true)
	rec := postBeam(t, s, `{"permit_control":false,"mode":"reverse"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestHandleBeamControlDisabledByAgent(t *testing.T) {
	s := newBeamTestServer(&fakeBeam{online: true}, false)
	rec := postBeam(t, s, `{"permit_control":true,"mode":"reverse"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestHandleBeamInvalidMode(t *testing.T) {
	s := newBeamTestServer(&fakeBeam{online: true}, true)
	rec := postBeam(t, s, `{"permit_control":true,"mode":"sideways"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleBeamNotConfigured(t *testing.T) {
	s := newBeamTestServer(nil, true) // s.ub == nil
	rec := postBeam(t, s, `{"permit_control":true,"mode":"reverse"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestHandleBeamPublishFailureIsNotFalseSuccess(t *testing.T) {
	fb := &fakeBeam{online: false, publishErr: errors.New("broker not connected")}
	s := newBeamTestServer(fb, true)
	rec := postBeam(t, s, `{"permit_control":true,"mode":"forward"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	var resp struct {
		OK bool `json:"ok"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.OK {
		t.Fatal("ok = true on publish failure, want false")
	}
}

func TestHandleBeamWrongMethod(t *testing.T) {
	s := newBeamTestServer(&fakeBeam{online: true}, true)
	req := httptest.NewRequest(http.MethodGet, "/v1/antenna/beam", nil)
	rec := httptest.NewRecorder()
	s.handleBeam(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodPost {
		t.Fatalf("Allow = %q, want POST", got)
	}
}

func TestHandleAntennaStateUsesUltrabeamMode(t *testing.T) {
	fb := &fakeBeam{online: true, mode: "reverse"}
	s := newBeamTestServer(fb, true)
	// Seed a cached rotator state so getPolledStateOrFallback returns without UDP.
	s.pollState = polledAntennaState{
		state:       rotatorState{AzimuthDeg: 90, Mode: "forward"},
		lastUpdated: time.Now(),
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/antenna/state", nil)
	rec := httptest.NewRecorder()
	s.handleAntennaState(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Antenna struct {
			Mode          string   `json:"mode"`
			AzimuthDeg    *float64 `json:"azimuth_deg"`
			AzimuthOnline bool     `json:"azimuth_online"`
			BeamOnline    bool     `json:"beam_online"`
		} `json:"antenna"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Antenna.Mode != "reverse" {
		t.Fatalf("mode = %q, want reverse (UltraBeam-sourced)", resp.Antenna.Mode)
	}
	if resp.Antenna.AzimuthDeg == nil || *resp.Antenna.AzimuthDeg != 90 {
		t.Fatalf("azimuth_deg = %v, want 90 (PSTrotator-sourced, unchanged)", resp.Antenna.AzimuthDeg)
	}
	if !resp.Antenna.AzimuthOnline || !resp.Antenna.BeamOnline {
		t.Fatalf("expected azimuth_online && beam_online, got az=%v beam=%v", resp.Antenna.AzimuthOnline, resp.Antenna.BeamOnline)
	}
}

func TestHandleAntennaStateDegradesAzimuthNot502(t *testing.T) {
	fb := &fakeBeam{online: true, mode: "reverse"}
	s := newBeamTestServer(fb, true)
	// No cached rotator state + a non-responsive PST endpoint -> rotator poll
	// fails fast. With UltraBeam live, the response must still be 200.
	s.client = newPSTUDPClient(s.cfg)

	req := httptest.NewRequest(http.MethodGet, "/v1/antenna/state", nil)
	rec := httptest.NewRecorder()
	s.handleAntennaState(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (must not 502 when UltraBeam is live); body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Antenna struct {
			Mode          string   `json:"mode"`
			AzimuthDeg    *float64 `json:"azimuth_deg"`
			AzimuthOnline bool     `json:"azimuth_online"`
			BeamOnline    bool     `json:"beam_online"`
		} `json:"antenna"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Antenna.Mode != "reverse" {
		t.Fatalf("mode = %q, want reverse (alarm keeps evaluating)", resp.Antenna.Mode)
	}
	if resp.Antenna.AzimuthOnline {
		t.Fatal("azimuth_online = true, want false (rotator degraded)")
	}
	if resp.Antenna.AzimuthDeg != nil {
		t.Fatalf("azimuth_deg = %v, want omitted while degraded", *resp.Antenna.AzimuthDeg)
	}
	if !resp.Antenna.BeamOnline {
		t.Fatal("beam_online = false, want true (UltraBeam unaffected by rotator outage)")
	}
}

func TestHandleStatusAdvertisesUltrabeamCapability(t *testing.T) {
	t.Run("online when connected", func(t *testing.T) {
		s := newBeamTestServer(&fakeBeam{online: true}, true)
		caps := statusCapabilities(t, s)
		ub, ok := caps["ultrabeam"].(map[string]any)
		if !ok {
			t.Fatalf("no ultrabeam capability; caps=%v", caps)
		}
		if ub["online"] != true {
			t.Fatalf("ultrabeam.online = %v, want true", ub["online"])
		}
	})

	t.Run("offline when disconnected", func(t *testing.T) {
		s := newBeamTestServer(&fakeBeam{online: false}, true)
		caps := statusCapabilities(t, s)
		ub := caps["ultrabeam"].(map[string]any)
		if ub["online"] != false {
			t.Fatalf("ultrabeam.online = %v, want false", ub["online"])
		}
	})

	t.Run("absent when disabled", func(t *testing.T) {
		s := newBeamTestServer(nil, true)
		caps := statusCapabilities(t, s)
		if _, ok := caps["ultrabeam"]; ok {
			t.Fatal("ultrabeam capability present when disabled, want absent")
		}
	})
}

func statusCapabilities(t *testing.T, s *server) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	rec := httptest.NewRecorder()
	s.handleStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp struct {
		Capabilities map[string]any `json:"capabilities"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.Capabilities
}
