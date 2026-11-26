package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

type areaHeardTestResponse struct {
	GeneratedAt   int64    `json:"generated_at"`
	QTH           string   `json:"qth"`
	Squares       []string `json:"squares"`
	WindowMinutes int      `json:"window_minutes"`
	Count         int      `json:"count"`
	Stations      []struct {
		Call string `json:"call"`
		Band string `json:"band"`
		Mode string `json:"mode"`
		Grid string `json:"grid"`
	} `json:"stations"`
}

// withAreaHeardHistory swaps hub.history for the test and restores it after.
func withAreaHeardHistory(t *testing.T, msgs []MQTTMessage) {
	t.Helper()
	hub.Lock()
	orig := hub.history
	hub.history = msgs
	hub.Unlock()
	t.Cleanup(func() {
		hub.Lock()
		hub.history = orig
		hub.Unlock()
	})
}

func getAreaHeard(t *testing.T, query string) (*httptest.ResponseRecorder, areaHeardTestResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/area_heard?"+query, nil)
	rec := httptest.NewRecorder()
	areaHeardHandler(rec, req)
	var resp areaHeardTestResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v; body %s", err, rec.Body.String())
		}
	}
	return rec, resp
}

const areaHeardQuery = "qth=JO62&surroundings=true&minutes=15&modes=FT8,FT4"

func TestAreaHeardDedupesSenderAcrossReceivers(t *testing.T) {
	now := time.Now().Unix()
	withAreaHeardHistory(t, []MQTTMessage{
		{T: now - 120, SC: "K1ABC", SL: "FN42", RC: "DL1AAA", RL: "JO62QM", B: "20m", MD: "FT8"},
		{T: now - 60, SC: "K1ABC", SL: "FN42", RC: "DL2BBB", RL: "JO51AB", B: "20m", MD: "FT8"},
	})
	rec, resp := getAreaHeard(t, areaHeardQuery)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("CORS = %q, want *", got)
	}
	if resp.Count != 1 || len(resp.Stations) != 1 {
		t.Fatalf("count = %d, stations = %+v, want one row", resp.Count, resp.Stations)
	}
	s := resp.Stations[0]
	if s.Call != "K1ABC" || s.Band != "20m" || s.Mode != "FT8" || s.Grid != "FN42" {
		t.Errorf("row = %+v", s)
	}
	if resp.QTH != "JO62" || resp.WindowMinutes != 15 || len(resp.Squares) != 9 {
		t.Errorf("header = qth %q, window %d, squares %v", resp.QTH, resp.WindowMinutes, resp.Squares)
	}
	if resp.GeneratedAt < now || resp.GeneratedAt > now+5 {
		t.Errorf("generated_at = %d, now %d", resp.GeneratedAt, now)
	}
}

func TestAreaHeardIgnoresReceiversOutsideTheBlock(t *testing.T) {
	now := time.Now().Unix()
	withAreaHeardHistory(t, []MQTTMessage{
		// Sender inside the block, receiver far away.
		{T: now - 60, SC: "DL3CCC", SL: "JO62AA", RC: "K1XYZ", RL: "FN42", B: "20m", MD: "FT8"},
		// Receiver just outside the 3x3 block.
		{T: now - 60, SC: "W2DEF", SL: "FN31", RC: "DL4DDD", RL: "JO40AA", B: "20m", MD: "FT8"},
	})
	_, resp := getAreaHeard(t, areaHeardQuery)
	if resp.Count != 0 || len(resp.Stations) != 0 {
		t.Fatalf("stations = %+v, want none", resp.Stations)
	}
}

func TestAreaHeardDefaultModesDropJT65(t *testing.T) {
	now := time.Now().Unix()
	withAreaHeardHistory(t, []MQTTMessage{
		{T: now - 60, SC: "K1ABC", SL: "FN42", RC: "DL1AAA", RL: "JO62QM", B: "20m", MD: "JT65"},
	})
	_, resp := getAreaHeard(t, "qth=JO62&surroundings=true")
	if resp.Count != 0 {
		t.Fatalf("stations = %+v, want none", resp.Stations)
	}
}

func TestAreaHeardDropsReportsOlderThanWindow(t *testing.T) {
	now := time.Now().Unix()
	withAreaHeardHistory(t, []MQTTMessage{
		{T: now - 16*60, SC: "K1ABC", SL: "FN42", RC: "DL1AAA", RL: "JO62QM", B: "20m", MD: "FT8"},
	})
	_, resp := getAreaHeard(t, areaHeardQuery)
	if resp.Count != 0 {
		t.Fatalf("stations = %+v, want none", resp.Stations)
	}
}

func TestAreaHeardDropsNonPSKReporterSources(t *testing.T) {
	now := time.Now().Unix()
	withAreaHeardHistory(t, []MQTTMessage{
		{T: now - 60, SC: "K1ABC", SL: "FN42", RC: "DL1AAA", RL: "JO62QM", B: "20m", MD: "FT8", Source: "rbn"},
		{T: now - 60, SC: "K1ABC", SL: "FN42", RC: "DL1AAA", RL: "JO62QM", B: "20m", MD: "FT8", Source: "wspr"},
	})
	_, resp := getAreaHeard(t, areaHeardQuery)
	if resp.Count != 0 {
		t.Fatalf("stations = %+v, want none", resp.Stations)
	}
}

func TestAreaHeardGridIsFromLatestReport(t *testing.T) {
	now := time.Now().Unix()
	withAreaHeardHistory(t, []MQTTMessage{
		{T: now - 30, SC: "K1ABC", SL: "FN43", RC: "DL1AAA", RL: "JO62QM", B: "20m", MD: "FT8"},
		{T: now - 300, SC: "K1ABC", SL: "FN42", RC: "DL2BBB", RL: "JO62QM", B: "20m", MD: "FT8"},
	})
	_, resp := getAreaHeard(t, areaHeardQuery)
	if len(resp.Stations) != 1 || resp.Stations[0].Grid != "FN43" {
		t.Fatalf("stations = %+v, want one row with grid FN43", resp.Stations)
	}
}

func TestAreaHeardWithoutQTHIsBadRequest(t *testing.T) {
	withAreaHeardHistory(t, nil)
	for _, q := range []string{"minutes=15", "qth=DL1AAA"} {
		rec, _ := getAreaHeard(t, q)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, rec.Code)
		}
	}
}

func TestAreaHeardRowsAreSorted(t *testing.T) {
	now := time.Now().Unix()
	withAreaHeardHistory(t, []MQTTMessage{
		{T: now - 60, SC: "W2DEF", SL: "FN31", RC: "DL1AAA", RL: "JO62QM", B: "40m", MD: "FT8"},
		{T: now - 60, SC: "K1ABC", SL: "FN42", RC: "DL1AAA", RL: "JO62QM", B: "20m", MD: "FT8"},
		{T: now - 60, SC: "K1ABC", SL: "FN42", RC: "DL1AAA", RL: "JO62QM", B: "20m", MD: "FT4"},
		{T: now - 60, SC: "", SL: "FN42", RC: "DL1AAA", RL: "JO62QM", B: "20m", MD: "FT8"},
		{T: now - 60, SC: "AA1AA", SL: "FN42", RC: "DL1AAA", RL: "JO62QM", B: "20m", MD: "FT8"},
	})
	want := []string{"20m AA1AA FT8", "20m K1ABC FT4", "20m K1ABC FT8", "40m W2DEF FT8"}
	for i := 0; i < 3; i++ {
		_, resp := getAreaHeard(t, areaHeardQuery)
		var got []string
		for _, s := range resp.Stations {
			got = append(got, s.Band+" "+s.Call+" "+s.Mode)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d: rows = %v, want %v", i, got, want)
		}
	}
}
