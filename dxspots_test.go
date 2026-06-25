package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDxSpotsHandler(t *testing.T) {
	now := time.Now().Unix()
	// Seed hub.history with a mix of dxcluster + mqtt messages.
	hub.Lock()
	hub.history = []MQTTMessage{
		{T: now - 300, MD: "DXCLUSTER", SC: "DL1ABC", RC: "VK9XX", RL: "OH29", SL: "JO31", B: "20m", F: 14074.0, CM: "up 5"},
		{T: now - 120, MD: "FT8", SC: "G3ABC", RC: "W1AW", B: "20m"},                                                  // not a cluster spot → excluded
		{T: now - 60, MD: "DXCLUSTER", SC: "EA4XYZ", RC: "VK9XX", RL: "OH29", B: "20m", F: 14075.0, CM: "still here"}, // newer VK9XX/20m → wins dedup
		{T: now - 30, MD: "DXCLUSTER", SC: "F5ABC", RC: "3Y0J", RL: "IB59", B: "17m", F: 18145.0, CM: "QRT soon"},
	}
	hub.Unlock()
	defer func() { hub.Lock(); hub.history = nil; hub.Unlock() }()

	rec := httptest.NewRecorder()
	dxSpotsHandler(rec, httptest.NewRequest(http.MethodGet, "/api/dxspots?minutes=15", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var spots []dxSpot
	if err := json.Unmarshal(rec.Body.Bytes(), &spots); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(spots) != 2 {
		t.Fatalf("want 2 spots (FT8 excluded, VK9XX deduped), got %d: %+v", len(spots), spots)
	}
	// Newest first → 3Y0J (30s) before VK9XX (60s).
	if spots[0].DXCall != "3Y0J" || spots[1].DXCall != "VK9XX" {
		t.Errorf("order/dedup wrong: %+v", spots)
	}
	// Dedup kept the newer VK9XX spot (freq 14075, EA4XYZ).
	if spots[1].FreqKHz != 14075.0 || spots[1].Spotter != "EA4XYZ" {
		t.Errorf("dedup should keep newest VK9XX: %+v", spots[1])
	}
	if spots[1].Comment != "still here" || spots[1].DXLocator != "OH29" {
		t.Errorf("comment/locator not surfaced: %+v", spots[1])
	}
}
