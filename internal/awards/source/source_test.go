package source

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"horstreporter/internal/awards/award"
)

func statusOf(snap *Snapshot, s award.Slot) (award.Status, bool) {
	for _, ss := range snap.Slots {
		if ss.Slot.Key() == s.Key() {
			return ss.Status, true
		}
	}
	return 0, false
}

func TestWavelogADIFRefresh(t *testing.T) {
	// Two-page export: page 1 returns two QSOs, page 2 reports none (stop).
	page1 := `<CALL:4>3Y0J<DXCC:2>24<BAND:3>20M<MODE:3>SSB<EOR>` +
		`<CALL:4>W1AW<DXCC:3>291<STATE:2>CT<BAND:3>40M<MODE:2>CW<QSL_RCVD:1>Y<EOR>`
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			FetchFromID int64 `json:"fetchfromid"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		if body.FetchFromID == 0 {
			_ = json.NewEncoder(w).Encode(map[string]any{"exported_qsos": 2, "lastfetchedid": 2, "adif": page1})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"exported_qsos": 0, "lastfetchedid": 2, "adif": ""})
	}))
	defer srv.Close()

	s := NewWavelogADIF(srv.URL, "secret", "", time.Minute)
	snap, err := s.Refresh(context.Background(), nil)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if calls < 2 {
		t.Errorf("expected pagination (>=2 calls), got %d", calls)
	}

	// DXCC entity 24 worked (unconfirmed), 291 confirmed.
	if st, ok := statusOf(snap, award.Slot{Program: award.ProgDXCC, Entity: "24"}); !ok || st != award.StatusWorked {
		t.Errorf("dxcc 24 = %v ok=%v, want worked", st, ok)
	}
	if st, ok := statusOf(snap, award.Slot{Program: award.ProgDXCC, Entity: "291"}); !ok || st != award.StatusConfirmed {
		t.Errorf("dxcc 291 = %v ok=%v, want confirmed", st, ok)
	}
	// WAS CT confirmed (US entity + state + QSL).
	if st, ok := statusOf(snap, award.Slot{Program: award.ProgWAS, Entity: "CT"}); !ok || st != award.StatusConfirmed {
		t.Errorf("was CT = %v ok=%v, want confirmed", st, ok)
	}
	// Per-band DXCC slot present.
	if _, ok := statusOf(snap, award.Slot{Program: award.ProgDXCC, Entity: "24", Band: "20m"}); !ok {
		t.Errorf("expected per-band dxcc slot 24/20m")
	}
	if snap.Stats["qso_count"] != 2 {
		t.Errorf("qso_count = %d, want 2", snap.Stats["qso_count"])
	}
	// No POTA QSOs → POTA coverage NOT declared (only dxcc + was).
	if hasProgram(snap, award.ProgPOTA) {
		t.Errorf("POTA should not be declared for a log with no POTA QSOs: %v", snap.Programs)
	}
	if !hasProgram(snap, award.ProgDXCC) || !hasProgram(snap, award.ProgWAS) {
		t.Errorf("expected dxcc+was coverage, got %v", snap.Programs)
	}
}

func hasProgram(snap *Snapshot, p award.Program) bool {
	for _, x := range snap.Programs {
		if x == p {
			return true
		}
	}
	return false
}

func TestWavelogADIFPOTA(t *testing.T) {
	adif := `<CALL:4>W1AW<DXCC:3>291<STATE:2>CA<BAND:3>20M<MODE:3>SSB<SIG:4>POTA<SIG_INFO:6>K-1234<EOR>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			FetchFromID int64 `json:"fetchfromid"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.FetchFromID == 0 {
			_ = json.NewEncoder(w).Encode(map[string]any{"exported_qsos": 1, "lastfetchedid": 1, "adif": adif})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"exported_qsos": 0, "lastfetchedid": 1, "adif": ""})
	}))
	defer srv.Close()

	snap, err := NewWavelogADIF(srv.URL, "k", "", time.Minute).Refresh(context.Background(), nil)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if st, ok := statusOf(snap, award.Slot{Program: award.ProgPOTA, Entity: "K-1234"}); !ok || st != award.StatusWorked {
		t.Errorf("pota park = %v ok=%v, want worked", st, ok)
	}
	// A POTA QSO is present → POTA coverage IS declared.
	if !hasProgram(snap, award.ProgPOTA) {
		t.Errorf("expected POTA coverage when the log has POTA QSOs: %v", snap.Programs)
	}
}

func TestWavelogADIFStringifiedNumerics(t *testing.T) {
	// DCLNext returns exported_qsos/lastfetchedid as JSON strings — flexInt must
	// accept them.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			FetchFromID int64 `json:"fetchfromid"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.FetchFromID == 0 {
			_, _ = w.Write([]byte(`{"exported_qsos":"1","lastfetchedid":"7","adif":"<CALL:4>W1AW<DXCC:3>291<STATE:2>CT<BAND:3>20M<EOR>"}`))
			return
		}
		_, _ = w.Write([]byte(`{"exported_qsos":"0","lastfetchedid":"7","adif":""}`))
	}))
	defer srv.Close()
	snap, err := NewWavelogADIF(srv.URL, "k", "1", time.Minute).Refresh(context.Background(), nil)
	if err != nil {
		t.Fatalf("Refresh with stringified numerics: %v", err)
	}
	if snap.Cursor != "7" {
		t.Errorf("cursor = %q, want 7 (parsed from string)", snap.Cursor)
	}
	if _, ok := statusOf(snap, award.Slot{Program: award.ProgWAS, Entity: "CT"}); !ok {
		t.Errorf("expected WAS CT slot")
	}
}

func TestWavelogADIFHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := NewWavelogADIF(srv.URL, "k", "", time.Minute).Refresh(context.Background(), nil); err == nil {
		t.Errorf("expected error on HTTP 500")
	}
}

func TestPOTARefresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"callsign": "W1AW",
			"hunter_parks": []map[string]any{
				{"reference": "K-1234"},
				{"reference": "K-5678"},
			},
		})
	}))
	defer srv.Close()

	snap, err := NewPOTA(srv.URL, "W1AW", "", time.Hour).Refresh(context.Background(), nil)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, ok := statusOf(snap, award.Slot{Program: award.ProgPOTA, Entity: "K-1234"}); !ok {
		t.Errorf("expected park K-1234")
	}
	if _, ok := statusOf(snap, award.Slot{Program: award.ProgPOTA, Entity: "K-5678"}); !ok {
		t.Errorf("expected park K-5678")
	}
	if !hasProgram(snap, award.ProgPOTA) {
		t.Errorf("expected POTA coverage when parks parsed: %v", snap.Programs)
	}
}

func TestPOTANoParksNoCoverage(t *testing.T) {
	// A profile with no parks list must not declare POTA coverage (no false wants).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"callsign": "W1AW"})
	}))
	defer srv.Close()
	snap, err := NewPOTA(srv.URL, "W1AW", "", time.Hour).Refresh(context.Background(), nil)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if hasProgram(snap, award.ProgPOTA) {
		t.Errorf("POTA coverage must not be declared with no parks: %v", snap.Programs)
	}
}

func TestPOTAUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	if _, err := NewPOTA(srv.URL, "W1AW", "", time.Hour).Refresh(context.Background(), nil); err == nil {
		t.Errorf("expected error on 401")
	}
}

func TestPOTACSV(t *testing.T) {
	cases := []struct {
		name, body string
		wantRefs   []string
	}{
		{
			name:     "header with Reference column",
			body:     "Reference,Name,QSOs\nK-1234,Some Park,5\nDL-0123,Another,2\n",
			wantRefs: []string{"K-1234", "DL-0123"},
		},
		{
			name:     "no header, ref scanned from cells",
			body:     "2026-06-01,K-0817,booming\n2026-06-02,VK-1234,weak\n",
			wantRefs: []string{"K-0817", "VK-1234"},
		},
		{
			name:     "lowercase refs normalized",
			body:     "reference\nk-5678\n",
			wantRefs: []string{"K-5678"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := dir + "/hunted.csv"
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			snap, err := NewPOTACSV(path, time.Hour).Refresh(context.Background(), nil)
			if err != nil {
				t.Fatalf("Refresh: %v", err)
			}
			if !hasProgram(snap, award.ProgPOTA) {
				t.Fatalf("expected POTA coverage, programs=%v", snap.Programs)
			}
			for _, ref := range tc.wantRefs {
				if st, ok := statusOf(snap, award.Slot{Program: award.ProgPOTA, Entity: ref}); !ok || st != award.StatusWorked {
					t.Errorf("park %s = %v ok=%v, want worked", ref, st, ok)
				}
			}
		})
	}
}

func TestPOTACSVEmptyNoCoverage(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/empty.csv"
	if err := os.WriteFile(path, []byte("Reference,Name\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := NewPOTACSV(path, time.Hour).Refresh(context.Background(), nil)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if hasProgram(snap, award.ProgPOTA) {
		t.Errorf("empty CSV must not declare POTA coverage: %v", snap.Programs)
	}
}

func TestPOTACSVMissingFile(t *testing.T) {
	if _, err := NewPOTACSV("/no/such/file.csv", time.Hour).Refresh(context.Background(), nil); err == nil {
		t.Errorf("expected error for missing CSV file")
	}
}

func TestBuildIndexFoldsAndMarks(t *testing.T) {
	s1 := &Snapshot{
		Source:   "wavelog-adif",
		Programs: []award.Program{award.ProgDXCC},
		Slots:    []award.SlotStatus{{Slot: award.Slot{Program: award.ProgDXCC, Entity: "1"}, Status: award.StatusWorked}},
	}
	s2 := &Snapshot{
		Source:   "wavelog-adif-2",
		Programs: []award.Program{award.ProgDXCC},
		Slots:    []award.SlotStatus{{Slot: award.Slot{Program: award.ProgDXCC, Entity: "1"}, Status: award.StatusConfirmed}},
	}
	ix := BuildIndex([]*Snapshot{s1, s2})
	if !ix.HasProgram(award.ProgDXCC) {
		t.Errorf("dxcc not marked loaded")
	}
	if got := ix.Status(award.Slot{Program: award.ProgDXCC, Entity: "1"}); got != award.StatusConfirmed {
		t.Errorf("folded status = %v, want confirmed (best of two)", got)
	}
}
