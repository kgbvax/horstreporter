package main

import (
	"testing"

	"horstreporter/internal/maidenhead"
)

// A 6-char QTH used to match only spots in that exact subsquare, so a sparse
// operator saw nothing. Through maidenhead.QTHSquares it matches the whole 4-char square.
func TestSixCharQthMatchesWholeSquare(t *testing.T) {
	m := MQTTMessage{B: "20m", SC: "K1ABC", SL: "FN76AB", RC: "DL1XYZ", RL: "JO32AB", RP: -10}
	client := &Client{qthSet: maidenhead.QTHSquares("FN76OJ", false)}
	if _, ok := matchAndCreateSpot(client, m, m.T); !ok {
		t.Fatal("spot from FN76AB must match qth FN76OJ")
	}
	ev, ok := extractMatchedBandEvent(m, maidenhead.QTHSquares("FN76OJ", false))
	if !ok {
		t.Fatal("extractMatchedBandEvent must match qth FN76OJ at grid4")
	}
	if ev.band != "20m" {
		t.Fatalf("band = %q", ev.band)
	}
	if _, ok := extractMatchedBandEvent(m, maidenhead.QTHSquares("FN31PR", false)); ok {
		t.Fatal("spot from FN76 must not match qth FN31PR")
	}
}

func TestReporterLocatorForMessage(t *testing.T) {
	cases := []struct {
		name string
		m    MQTTMessage
		want string
	}{
		{"pskreporter uses RL", MQTTMessage{SL: "JO62", RL: "FN31", Source: "mqtt"}, "FN31"},
		{"no source defaults to RL", MQTTMessage{SL: "JO62", RL: "FN31"}, "FN31"},
		{"dxcluster uses SL", MQTTMessage{SL: "JO62", RL: "FN31", Source: "dxcluster"}, "JO62"},
		{"legacy dxcluster MD uses SL", MQTTMessage{SL: "JO62", RL: "FN31", MD: "DXCLUSTER"}, "JO62"},
		{"rbn uses SL", MQTTMessage{SL: "jo62", RL: "FN31", Source: "rbn"}, "JO62"},
		{"wspr uses SL", MQTTMessage{SL: "JO62", RL: "FN31", Source: "wspr"}, "JO62"},
	}
	for _, c := range cases {
		if got := reporterLocatorForMessage(c.m); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
