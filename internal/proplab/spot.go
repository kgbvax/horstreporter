package proplab

import (
	"strings"
)

// spot.go contains the spot message type and source-tag helpers used by the
// proplab engines. It mirrors the shape of the main binary's MQTTMessage without
// importing package main.

// Spot is one decoded spot, agnostic to ingest source.
type Spot struct {
	RP     int    // signal report dB
	T      int64  // Unix timestamp
	SC     string // sender callsign
	SL     string // sender locator
	RC     string // receiver callsign
	RL     string // receiver locator
	B      string // band
	MD     string // mode
	Source string // "mqtt", "dxcluster", "rbn"
}

// SourceTypeForMessage returns the ingest lane tag for a spot.
func SourceTypeForMessage(m Spot) string {
	if src := strings.ToLower(strings.TrimSpace(m.Source)); src != "" {
		return src
	}
	if strings.ToUpper(strings.TrimSpace(m.MD)) == "DXCLUSTER" {
		return "dxcluster"
	}
	return "mqtt"
}
