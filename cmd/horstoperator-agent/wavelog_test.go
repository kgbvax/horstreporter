package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNeededFromLookup(t *testing.T) {
	cases := []struct {
		name string
		in   *wavelogLookup
		want []string
	}{
		{"atno", &wavelogLookup{DXCCConfirmed: false}, []string{"dxcc"}},
		{"new-band", &wavelogLookup{DXCCConfirmed: true, DXCCConfirmedBand: false}, []string{"band"}},
		{"new-mode", &wavelogLookup{DXCCConfirmed: true, DXCCConfirmedBand: true, DXCCConfirmedBandMode: false}, []string{"mode"}},
		{"nothing-needed", &wavelogLookup{DXCCConfirmed: true, DXCCConfirmedBand: true, DXCCConfirmedBandMode: true}, []string{}},
		{"nil", nil, nil},
	}
	for _, c := range cases {
		got := neededFromLookup(c.in)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: neededFromLookup=%v want %v", c.name, got, c.want)
		}
	}
}

func newWavelogServer(t *testing.T, handler http.HandlerFunc) (*server, *httptest.Server) {
	t.Helper()
	mock := httptest.NewServer(handler)
	t.Cleanup(mock.Close)
	s := &server{
		cfg:     serviceConfig{},
		wavelog: newWavelogClient(mock.URL, "test-key"),
	}
	return s, mock
}

func TestHandleEnrich_OK(t *testing.T) {
	var gotPath, gotKey, gotCall string
	s, _ := newWavelogServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotKey, _ = body["key"].(string)
		gotCall, _ = body["callsign"].(string)
		// ATNO: not confirmed; worked the callsign before on another band.
		_ = json.NewEncoder(w).Encode(wavelogLookup{
			Callsign: "3Y0J", DXCC: "BOUVET", DXCCID: "24", Cont: "AF", DXCCFlag: "BV",
			CallWorked: true, CallWorkedBand: false,
			DXCCConfirmed: false,
		})
	})

	rec := httptest.NewRecorder()
	body := `{"permit_lookup":true,"spots":[{"id":"3Y0J|17m|SSB","call":"3Y0J","band":"17m","mode":"SSB"}]}`
	s.handleEnrich(rec, httptest.NewRequest(http.MethodPost, "/v1/operate/enrich", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/api/private_lookup" {
		t.Errorf("wavelog path %q, want /api/private_lookup", gotPath)
	}
	if gotKey != "test-key" || gotCall != "3Y0J" {
		t.Errorf("forwarded key=%q call=%q", gotKey, gotCall)
	}

	var resp struct {
		OK       bool           `json:"ok"`
		Degraded bool           `json:"degraded"`
		Results  []enrichResult `json:"results"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.OK || resp.Degraded || len(resp.Results) != 1 {
		t.Fatalf("unexpected envelope: %+v", resp)
	}
	r0 := resp.Results[0]
	if r0.ID != "3Y0J|17m|SSB" {
		t.Errorf("id=%q", r0.ID)
	}
	if r0.DXCC == nil || r0.DXCC.Entity != "BOUVET" || r0.DXCC.Cont != "AF" {
		t.Errorf("dxcc=%+v", r0.DXCC)
	}
	if strings.Join(r0.Needed, ",") != "dxcc" {
		t.Errorf("needed=%v want [dxcc]", r0.Needed)
	}
	if r0.WorkedBefore == nil || !r0.WorkedBefore.Worked || r0.WorkedBefore.WorkedBand {
		t.Errorf("worked_before=%+v", r0.WorkedBefore)
	}
}

func TestHandleEnrich_Gates(t *testing.T) {
	// no wavelog client → 503
	s0 := &server{cfg: serviceConfig{}}
	rec := httptest.NewRecorder()
	s0.handleEnrich(rec, httptest.NewRequest(http.MethodPost, "/v1/operate/enrich", strings.NewReader(`{"permit_lookup":true,"spots":[]}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no client: status %d want 503", rec.Code)
	}

	// permit_lookup=false → 403
	s, _ := newWavelogServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	rec = httptest.NewRecorder()
	s.handleEnrich(rec, httptest.NewRequest(http.MethodPost, "/v1/operate/enrich", strings.NewReader(`{"permit_lookup":false,"spots":[]}`)))
	if rec.Code != http.StatusForbidden {
		t.Errorf("permit_lookup=false: status %d want 403", rec.Code)
	}
}

func TestHandleEnrich_DegradedOnUpstreamError(t *testing.T) {
	s, _ := newWavelogServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	rec := httptest.NewRecorder()
	body := `{"permit_lookup":true,"spots":[{"id":"X|20m|CW","call":"VK6LC","band":"20m","mode":"CW"}]}`
	s.handleEnrich(rec, httptest.NewRequest(http.MethodPost, "/v1/operate/enrich", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var resp struct {
		Degraded bool           `json:"degraded"`
		Results  []enrichResult `json:"results"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.Degraded {
		t.Error("expected degraded=true on upstream 500")
	}
	// Spot still present, just without enrichment.
	if len(resp.Results) != 1 || resp.Results[0].DXCC != nil {
		t.Errorf("expected 1 bare result, got %+v", resp.Results)
	}
}

func TestWavelogCacheHit(t *testing.T) {
	var calls int
	s, _ := newWavelogServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(wavelogLookup{Callsign: "VK6LC", DXCC: "AUSTRALIA", DXCCConfirmed: true, DXCCConfirmedBand: true, DXCCConfirmedBandMode: true})
	})
	req := func() {
		rec := httptest.NewRecorder()
		body := `{"permit_lookup":true,"spots":[{"id":"a","call":"VK6LC","band":"20m","mode":"CW"}]}`
		s.handleEnrich(rec, httptest.NewRequest(http.MethodPost, "/v1/operate/enrich", strings.NewReader(body)))
	}
	req()
	req()
	if calls != 1 {
		t.Errorf("expected 1 upstream call (cache hit on 2nd), got %d", calls)
	}
}
