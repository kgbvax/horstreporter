package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"horstreporter/internal/awardcontract"
)

// newWavelogAwardsServer wires a server with both a mock Wavelog and a mock
// horstawards backend.
func newWavelogAwardsServer(t *testing.T, wl http.HandlerFunc, aw http.HandlerFunc) *server {
	t.Helper()
	wlSrv := httptest.NewServer(wl)
	awSrv := httptest.NewServer(aw)
	t.Cleanup(wlSrv.Close)
	t.Cleanup(awSrv.Close)
	return &server{
		cfg:     serviceConfig{},
		wavelog: newWavelogClient(wlSrv.URL, "test-key"),
		awards:  newAwardsClient(awSrv.URL),
	}
}

func decodeEnrich(t *testing.T, rec *httptest.ResponseRecorder) (bool, []enrichResult) {
	t.Helper()
	var resp struct {
		OK       bool           `json:"ok"`
		Degraded bool           `json:"degraded"`
		Results  []enrichResult `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return resp.Degraded, resp.Results
}

// When horstawards is healthy it is authoritative: its needed[] supersedes the
// Wavelog confirmed-flag verdict and adds was/pota.
func TestHandleEnrich_AwardsSupersedes(t *testing.T) {
	wl := func(w http.ResponseWriter, r *http.Request) {
		// Wavelog would say ATNO (not confirmed).
		_ = json.NewEncoder(w).Encode(wavelogLookup{
			Callsign: "W1AW", DXCC: "UNITED STATES", DXCCID: "291", State: "CT",
			DXCCConfirmed: false,
		})
	}
	aw := func(w http.ResponseWriter, r *http.Request) {
		var req awardcontract.WantedRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if !req.PermitLookup {
			t.Errorf("agent must send permit_lookup=true")
		}
		results := make([]awardcontract.WantedResult, 0, len(req.Spots))
		for _, sp := range req.Spots {
			// Confirm the agent forwarded resolved attributes.
			if sp.DXCCID != "291" || sp.State != "CT" {
				t.Errorf("forwarded attrs wrong: %+v", sp)
			}
			results = append(results, awardcontract.WantedResult{
				ID:     sp.ID,
				Needed: []string{"dxcc", "was"},
				Slots:  []awardcontract.WantedSlot{{Program: "was", Entity: "CT", Status: "unworked"}},
			})
		}
		_ = json.NewEncoder(w).Encode(awardcontract.WantedResponse{OK: true, Degraded: false, Results: results})
	}
	s := newWavelogAwardsServer(t, wl, aw)

	rec := httptest.NewRecorder()
	body := `{"permit_lookup":true,"spots":[{"id":"W1AW|20m|SSB","call":"W1AW","band":"20m","mode":"SSB"}]}`
	s.handleEnrich(rec, httptest.NewRequest(http.MethodPost, "/v1/operate/enrich", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	degraded, results := decodeEnrich(t, rec)
	if degraded || len(results) != 1 {
		t.Fatalf("envelope degraded=%v results=%d", degraded, len(results))
	}
	if strings.Join(results[0].Needed, ",") != "dxcc,was" {
		t.Errorf("needed=%v, want [dxcc was] from awards", results[0].Needed)
	}
	if len(results[0].Slots) != 1 || results[0].Slots[0].Program != "was" {
		t.Errorf("slots not passed through: %+v", results[0].Slots)
	}
	// Wavelog DXCC block still present (entity resolution unchanged).
	if results[0].DXCC == nil || results[0].DXCC.Entity != "UNITED STATES" {
		t.Errorf("dxcc block lost: %+v", results[0].DXCC)
	}
}

// The POTA park ref from the spot (parsed from the comment, carried in the enrich
// request) must be forwarded to horstawards — the Wavelog callsign lookup has no
// park, so it can only come from the request.
func TestHandleEnrich_ForwardsPotaRef(t *testing.T) {
	wl := func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(wavelogLookup{Callsign: "W1ABC", DXCC: "UNITED STATES", DXCCID: "291"})
	}
	var gotRef string
	aw := func(w http.ResponseWriter, r *http.Request) {
		var req awardcontract.WantedRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.Spots) == 1 {
			gotRef = req.Spots[0].POTARef
		}
		results := []awardcontract.WantedResult{}
		for _, sp := range req.Spots {
			needed := []string{}
			if sp.POTARef != "" {
				needed = append(needed, "pota")
			}
			results = append(results, awardcontract.WantedResult{ID: sp.ID, Needed: needed})
		}
		_ = json.NewEncoder(w).Encode(awardcontract.WantedResponse{OK: true, Results: results})
	}
	s := newWavelogAwardsServer(t, wl, aw)

	rec := httptest.NewRecorder()
	body := `{"permit_lookup":true,"spots":[{"id":"W1ABC|20m|SSB","call":"W1ABC","band":"20m","mode":"SSB","pota_ref":"K-1234"}]}`
	s.handleEnrich(rec, httptest.NewRequest(http.MethodPost, "/v1/operate/enrich", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if gotRef != "K-1234" {
		t.Errorf("horstawards received pota_ref=%q, want K-1234", gotRef)
	}
	_, results := decodeEnrich(t, rec)
	if len(results) != 1 || strings.Join(results[0].Needed, ",") != "pota" {
		t.Errorf("expected needed [pota] from park ref, got %+v", results)
	}
}

// When horstawards is down, enrichment must continue with the Wavelog verdict and
// only mark degraded.
func TestHandleEnrich_AwardsDownTolerant(t *testing.T) {
	wl := func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(wavelogLookup{Callsign: "3Y0J", DXCC: "BOUVET", DXCCID: "24", DXCCConfirmed: false})
	}
	aw := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }
	s := newWavelogAwardsServer(t, wl, aw)

	rec := httptest.NewRecorder()
	body := `{"permit_lookup":true,"spots":[{"id":"3Y0J|17m|SSB","call":"3Y0J","band":"17m","mode":"SSB"}]}`
	s.handleEnrich(rec, httptest.NewRequest(http.MethodPost, "/v1/operate/enrich", strings.NewReader(body)))

	degraded, results := decodeEnrich(t, rec)
	if !degraded {
		t.Error("expected degraded=true when awards is down")
	}
	if len(results) != 1 || strings.Join(results[0].Needed, ",") != "dxcc" {
		t.Errorf("expected Wavelog needed [dxcc] retained, got %+v", results)
	}
}

// When horstawards is reachable but its index is cold (degraded), the Wavelog
// verdict is retained (not overwritten with empty awards needed) and degraded is
// surfaced.
func TestHandleEnrich_AwardsColdRetainsWavelog(t *testing.T) {
	wl := func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(wavelogLookup{Callsign: "3Y0J", DXCC: "BOUVET", DXCCID: "24", DXCCConfirmed: false})
	}
	aw := func(w http.ResponseWriter, r *http.Request) {
		var req awardcontract.WantedRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		results := make([]awardcontract.WantedResult, 0, len(req.Spots))
		for _, sp := range req.Spots {
			results = append(results, awardcontract.WantedResult{ID: sp.ID, Needed: []string{}})
		}
		_ = json.NewEncoder(w).Encode(awardcontract.WantedResponse{OK: true, Degraded: true, Results: results})
	}
	s := newWavelogAwardsServer(t, wl, aw)

	rec := httptest.NewRecorder()
	body := `{"permit_lookup":true,"spots":[{"id":"3Y0J|17m|SSB","call":"3Y0J","band":"17m","mode":"SSB"}]}`
	s.handleEnrich(rec, httptest.NewRequest(http.MethodPost, "/v1/operate/enrich", strings.NewReader(body)))

	degraded, results := decodeEnrich(t, rec)
	if !degraded {
		t.Error("expected degraded=true when awards index cold")
	}
	if len(results) != 1 || strings.Join(results[0].Needed, ",") != "dxcc" {
		t.Errorf("expected Wavelog needed [dxcc] retained on cold index, got %+v", results)
	}
}
