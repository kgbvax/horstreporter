package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"horstreporter/internal/awards"
)

// fakeWavelog is a single httptest server that doubles as Wavelog: it answers
// /api/private_lookup (the agent's per-spot attribute resolution) and
// /api/get_contacts_adif (the local award engine's log pull).
func fakeWavelog(t *testing.T, lookup wavelogLookup, adif string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/private_lookup"):
			_ = json.NewEncoder(w).Encode(lookup)
		case strings.HasSuffix(r.URL.Path, "/api/get_contacts_adif"):
			var body struct {
				FetchFromID int64 `json:"fetchfromid"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.FetchFromID == 0 {
				_ = json.NewEncoder(w).Encode(map[string]any{"exported_qsos": "1", "lastfetchedid": "1", "adif": adif})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"exported_qsos": "0", "lastfetchedid": "1", "adif": ""})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func loadedManager(t *testing.T, srvURL string, cfgMut func(*awards.Config)) *awards.Manager {
	t.Helper()
	c := awards.Defaults()
	c.DataDir = t.TempDir()
	c.WavelogURL = srvURL
	c.WavelogAPIKey = "k"
	c.WavelogStationID = "1"
	c.StaleAfter = time.Hour
	if cfgMut != nil {
		cfgMut(&c)
	}
	mgr, err := awards.New(c)
	if err != nil {
		t.Fatalf("awards.New: %v", err)
	}
	mgr.RefreshNow(context.Background())
	return mgr
}

func enrich(t *testing.T, s *server, body string) (bool, []enrichResult) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.handleEnrich(rec, httptest.NewRequest(http.MethodPost, "/v1/operate/enrich", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Degraded bool           `json:"degraded"`
		Results  []enrichResult `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return resp.Degraded, resp.Results
}

// The local index is authoritative: a US station whose DXCC is already worked but
// whose state is new for WAS surfaces "was", superseding Wavelog's DXCC verdict.
func TestHandleEnrich_LocalAwardsWAS(t *testing.T) {
	// Log: one confirmed US QSO in MA on 20m SSB → DXCC 291 satisfied; WAS has MA
	// but not CT.
	adif := `<CALL:4>W1AA<DXCC:3>291<STATE:2>MA<BAND:3>20M<MODE:3>SSB<QSL_RCVD:1>Y<EOR>`
	// private_lookup for the spot: US/CT, DXCC already confirmed.
	srv := fakeWavelog(t, wavelogLookup{
		Callsign: "W1AW", DXCC: "UNITED STATES", DXCCID: "291", State: "CT",
		DXCCConfirmed: true, DXCCConfirmedBand: true, DXCCConfirmedBandMode: true,
	}, adif)
	s := &server{cfg: serviceConfig{}, wavelog: newWavelogClient(srv.URL, "k"), awards: loadedManager(t, srv.URL, nil)}

	degraded, results := enrich(t, s, `{"permit_lookup":true,"spots":[{"id":"W1AW|20m|SSB","call":"W1AW","band":"20m","mode":"SSB"}]}`)
	if degraded {
		t.Errorf("expected not degraded (index loaded)")
	}
	if len(results) != 1 || strings.Join(results[0].Needed, ",") != "was" {
		t.Errorf("needed = %v, want [was] from local index", results[0].Needed)
	}
}

// POTA park-hunt fires from the local CSV via the spot's pota_ref.
func TestHandleEnrich_LocalAwardsPOTA(t *testing.T) {
	adif := `<CALL:4>W1AA<DXCC:3>291<STATE:2>MA<SIG:4>POTA<SIG_INFO:6>K-0001<EOR>`
	srv := fakeWavelog(t, wavelogLookup{Callsign: "K1ABC", DXCC: "UNITED STATES", DXCCID: "291", DXCCConfirmed: true, DXCCConfirmedBand: true, DXCCConfirmedBandMode: true}, adif)
	csv := t.TempDir() + "/hunted.csv"
	if err := os.WriteFile(csv, []byte("Reference\nK-0001\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := loadedManager(t, srv.URL, func(c *awards.Config) { c.POTAHuntedCSV = csv })
	s := &server{cfg: serviceConfig{}, wavelog: newWavelogClient(srv.URL, "k"), awards: mgr}

	// K-0001 already hunted → no pota; K-9999 new → pota.
	_, hunted := enrich(t, s, `{"permit_lookup":true,"spots":[{"id":"a","call":"K1ABC","band":"20m","mode":"SSB","pota_ref":"K-0001"}]}`)
	if contains(hunted[0].Needed, "pota") {
		t.Errorf("hunted park should not be wanted: %v", hunted[0].Needed)
	}
	_, fresh := enrich(t, s, `{"permit_lookup":true,"spots":[{"id":"b","call":"K1ABC","band":"20m","mode":"SSB","pota_ref":"K-9999"}]}`)
	if !contains(fresh[0].Needed, "pota") {
		t.Errorf("new park should be wanted: %v", fresh[0].Needed)
	}
}

// A cold (never-refreshed) index is degraded → the agent keeps the Wavelog verdict.
func TestHandleEnrich_AwardsColdFallsBackToWavelog(t *testing.T) {
	srv := fakeWavelog(t, wavelogLookup{Callsign: "3Y0J", DXCC: "BOUVET", DXCCID: "24", DXCCConfirmed: false}, "")
	c := awards.Defaults()
	c.DataDir = t.TempDir()
	c.WavelogURL = srv.URL
	c.WavelogAPIKey = "k"
	c.WavelogStationID = "1"
	c.StaleAfter = time.Hour
	mgr, err := awards.New(c) // NOT refreshed → cold → degraded
	if err != nil {
		t.Fatal(err)
	}
	s := &server{cfg: serviceConfig{}, wavelog: newWavelogClient(srv.URL, "k"), awards: mgr}

	degraded, results := enrich(t, s, `{"permit_lookup":true,"spots":[{"id":"3Y0J|17m|SSB","call":"3Y0J","band":"17m","mode":"SSB"}]}`)
	if !degraded {
		t.Errorf("expected degraded when index cold")
	}
	if len(results) != 1 || strings.Join(results[0].Needed, ",") != "dxcc" {
		t.Errorf("expected Wavelog fallback [dxcc], got %v", results[0].Needed)
	}
}

// No awards manager at all → Wavelog-only behavior (unchanged from before awards).
func TestHandleEnrich_NoAwardsManager(t *testing.T) {
	srv := fakeWavelog(t, wavelogLookup{Callsign: "3Y0J", DXCC: "BOUVET", DXCCID: "24", DXCCConfirmed: false}, "")
	s := &server{cfg: serviceConfig{}, wavelog: newWavelogClient(srv.URL, "k")} // awards nil
	degraded, results := enrich(t, s, `{"permit_lookup":true,"spots":[{"id":"x","call":"3Y0J","band":"17m","mode":"SSB"}]}`)
	if degraded {
		t.Errorf("no awards manager should not mark degraded")
	}
	if strings.Join(results[0].Needed, ",") != "dxcc" {
		t.Errorf("expected [dxcc] from Wavelog, got %v", results[0].Needed)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
