package main

import "testing"

func TestGetSquaresWithinRings(t *testing.T) {
	if got := getSquaresWithinRings("JO32", 0); len(got) != 1 || got[0] != "JO32" {
		t.Fatalf("rings=0 => %v (want [JO32])", got)
	}
	if got := getSquaresWithinRings("JO32", 1); len(got) != 9 {
		t.Fatalf("rings=1 => %d squares (want 9)", len(got))
	}
	got := getSquaresWithinRings("JO32", 2)
	if len(got) != 25 {
		t.Fatalf("rings=2 => %d squares (want 25)", len(got))
	}
	found := false
	for _, s := range got {
		if s == "JO32" {
			found = true
		}
	}
	if !found {
		t.Error("center JO32 missing from rings=2 block")
	}
	if got := getSquaresWithinRings("W1AW", 3); len(got) != 1 || got[0] != "W1AW" {
		t.Errorf("non-locator passthrough => %v", got)
	}
}

func TestMatchAndCreateSpotArea(t *testing.T) {
	now := int64(100000)
	cx, cy, _ := locatorSquareXY("JO31")
	client := &Client{areaActive: true, areaX: cx, areaY: cy, areaRings: 1}

	// DX (sender) far away, heard by a receiver in JO30 (neighbour of JO31) →
	// matches via area (receiver in area); remote/Locator = the DX side.
	msg := MQTTMessage{SC: "VK9XX", RC: "DL1ABC", SL: "OH29", RL: "JO30", RP: 12, T: 99990, B: "20m", MD: "FT8"}
	spot, ok := matchAndCreateSpot(client, msg, now)
	if !ok {
		t.Fatal("expected area match (receiver JO30 within 1 ring of JO31)")
	}
	if spot.Locator != "OH29" {
		t.Errorf("remote locator = %q, want OH29 (DX side)", spot.Locator)
	}
	if spot.ReporterLocator != "JO30" {
		t.Errorf("reporter locator = %q, want JO30", spot.ReporterLocator)
	}

	// Both endpoints outside the area → no match.
	msg2 := msg
	msg2.RL = "PM95"
	if _, ok := matchAndCreateSpot(client, msg2, now); ok {
		t.Error("expected no match when both ends are outside the area")
	}

	// Regression guard: no targets and no area → never matches.
	if _, ok := matchAndCreateSpot(&Client{}, msg, now); ok {
		t.Error("empty client (no targets, no area) must not match")
	}
}
