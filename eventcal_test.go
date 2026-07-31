package main

import (
	"strings"
	"testing"
	"time"
)

func TestExtractBandMask(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"", ""},
		{"CQ WW 160m contest", "160m"},
		{"IARU HF Championship 80m-10m", "80m-10m"},
		{"ARRL 10m contest and 6m", "10m, 6m"},
		{"Bands: 160m, 80m, 40m", "160m, 80m, 40m"},
	}
	for _, c := range cases {
		got := extractBandMask(c.text)
		if got != c.want {
			t.Errorf("extractBandMask(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}

func TestExtractLocator(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"", ""},
		{"Bouvet 3Y0J locator JJ06", "JJ06"},
		{"At anchor in JO62qm", "JO62QM"},
		{"no locator here", ""},
	}
	for _, c := range cases {
		got := extractLocator(c.text)
		if got != c.want {
			t.Errorf("extractLocator(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}

func TestParseEventWindow(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC).Unix()
	start, end, ok := parseEventWindow("Contest", "2026-07-31 06:00:00", "", now)
	if !ok {
		t.Fatal("expected ok")
	}
	if end-start != 48*60*60 {
		t.Errorf("duration = %d, want 48h", end-start)
	}

	// Old event should be skipped.
	_, _, ok = parseEventWindow("Old", "2026-07-01 06:00:00", "", now)
	if ok {
		t.Error("expected old event to be skipped")
	}

	// Vague title without date falls back to current UTC day.
	start, end, ok = parseEventWindow("Some contest", "", "", now)
	if !ok {
		t.Fatal("expected ok for vague title")
	}
	if !strings.HasSuffix(time.Unix(start, 0).UTC().Format("15:04"), "00:00") {
		t.Errorf("expected midnight start, got %s", time.Unix(start, 0).UTC())
	}
}
