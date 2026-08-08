package main

import "testing"

func TestBandFromWSPR(t *testing.T) {
	cases := []struct {
		band int
		want string
	}{
		{1, "160m"},
		{3, "80m"},
		{5, "60m"},
		{7, "40m"},
		{10, "30m"},
		{14, "20m"},
		{18, "17m"},
		{21, "15m"},
		{24, "12m"},
		{28, "10m"},
		{50, "6m"},
		{70, "4m"},
		{144, "2m"},
		{0, ""},   // MF
		{-1, ""},  // LF
		{432, ""}, // 70cm
		{1296, ""}, // 23cm
	}
	for _, c := range cases {
		if got := bandFromWSPR(c.band); got != c.want {
			t.Errorf("bandFromWSPR(%d) = %q, want %q", c.band, got, c.want)
		}
	}
}

func TestParseWSPRTime(t *testing.T) {
	// 2026-08-08 21:26:00 UTC
	ts, err := parseWSPRTime("2026-08-08 21:26:00")
	if err != nil {
		t.Fatalf("parseWSPRTime: %v", err)
	}
	if ts != 1786224360 {
		t.Errorf("parseWSPRTime = %d, want 1786224360", ts)
	}
	if _, err := parseWSPRTime("not a time"); err == nil {
		t.Error("parseWSPRTime should error on garbage")
	}
}

func TestIsWSPRSpotUsableForLive(t *testing.T) {
	// Valid band + valid receiver locator → usable.
	if !isWSPRSpotUsableForLive(MQTTMessage{B: "20m", RL: "JO31"}) {
		t.Error("20m + JO31 should be usable")
	}
	// Missing band → not usable.
	if isWSPRSpotUsableForLive(MQTTMessage{B: "", RL: "JO31"}) {
		t.Error("empty band should not be usable")
	}
	// Missing/invalid receiver locator → not usable.
	if isWSPRSpotUsableForLive(MQTTMessage{B: "20m", RL: ""}) {
		t.Error("empty receiver locator should not be usable")
	}
	if isWSPRSpotUsableForLive(MQTTMessage{B: "20m", RL: "W1AW"}) {
		t.Error("callsign as receiver locator should not be usable")
	}
}

func TestIsNonConditionsModeWSPR(t *testing.T) {
	// WSPR must be excluded from the FT8-calibrated conditions accumulator.
	if !isNonConditionsMode("WSPR") {
		t.Error("isNonConditionsMode(WSPR) = false, want true (excluded from FT8 baseline)")
	}
	if !isNonConditionsMode("wspr") {
		t.Error("isNonConditionsMode(wspr) = false, want true (case-insensitive)")
	}
}
