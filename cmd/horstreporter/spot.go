package main

import (
	"strings"

	"horstreporter/internal/maidenhead"
)

type Spot struct {
	Lat             float64 `json:"lat"`
	Lng             float64 `json:"lng"`
	SNR             int     `json:"snr"`
	AgeSeconds      int64   `json:"ageSeconds"`
	Locator         string  `json:"locator"`
	ReporterLocator string  `json:"reporterLocator,omitempty"`
	SourceType      string  `json:"sourceType,omitempty"`
	Band            string  `json:"band"`
	Sender          string  `json:"sender"`
	Receiver        string  `json:"receiver"`

	// Stream-only bookkeeping (never serialized): the source message's unix
	// time (exact spot time for the v2 wire) and its hub sequence number.
	T   int64  `json:"-"`
	Seq uint64 `json:"-"`
}

func sourceTypeForMessage(m MQTTMessage) string {
	if src := strings.ToLower(strings.TrimSpace(m.Source)); src != "" {
		return src // explicit ingest tag ("mqtt"|"dxcluster"|"rbn"), incl. backfilled rows
	}
	// Legacy fallback for messages without an explicit Source (e.g. older cached
	// rows): DX-cluster is the only ingest that historically marked itself via MD.
	if strings.ToUpper(strings.TrimSpace(m.MD)) == "DXCLUSTER" {
		return "dxcluster"
	}
	return "mqtt"
}

func reporterLocatorForMessage(m MQTTMessage) string {
	// reporterLocator is the receiving (reporting) station's square. For
	// PSKReporter that is RL; DX cluster, RBN and WSPR use SC/SL for the
	// reporting station (RC/RL is the station heard).
	switch sourceTypeForMessage(m) {
	case "dxcluster", "rbn", "wspr":
		return strings.ToUpper(strings.TrimSpace(m.SL))
	}
	return strings.ToUpper(strings.TrimSpace(m.RL))
}

// matchCall checks for an exact callsign match, or a match with common prefix/suffix modifiers (e.g., W1AW/P, DL/W1AW).
// The three modifier checks are spelled out against qth instead of building
// "/"+qth needles: at ~250k window messages per /api/dx_conditions request
// the old concat version spent ~29% of total endpoint CPU in allocation
// (ce-optimize prop-latency, run #2 profile).
func matchCall(spotCall, qth string) bool {
	if spotCall == qth {
		return true
	}
	// HasPrefix(spotCall, qth+"/")
	if n := len(qth); len(spotCall) > n && spotCall[:n] == qth && spotCall[n] == '/' {
		return true
	}
	// HasSuffix(spotCall, "/"+qth)
	if n := len(qth); len(spotCall) > n && spotCall[len(spotCall)-n:] == qth && spotCall[len(spotCall)-n-1] == '/' {
		return true
	}
	// Contains(spotCall, "/"+qth+"/") — scan slash positions, no needle.
	if needleLen := len(qth) + 2; len(spotCall) >= needleLen {
		for i := 0; i+needleLen <= len(spotCall); i++ {
			if spotCall[i] != '/' {
				continue
			}
			if spotCall[i+1:i+1+len(qth)] == qth && spotCall[i+1+len(qth)] == '/' {
				return true
			}
		}
	}
	return false
}

func matchAndCreateSpot(client *Client, m MQTTMessage, now int64) (Spot, bool) {
	if len(client.qthSet) == 0 && !client.areaActive {
		return Spot{}, false
	}

	// Runs once per history message on every stream open and once per client
	// per live message. Callsigns arrive upper-case (ToUpper returns them
	// as-is), but locators are mixed case ("JO31lk"), so locators are compared
	// case-insensitively in place instead of being upper-cased into a new
	// string for every message; the one upper-cased copy is made for a match.
	sc, rc := strings.ToUpper(m.SC), strings.ToUpper(m.RC)

	isSender := false
	isReceiver := false

	for _, t := range client.qthSet {
		if matchCall(sc, t) || (maidenhead.IsLocator(t) && hasPrefixFold(m.SL, t)) {
			isSender = true
		}
		if matchCall(rc, t) || (maidenhead.IsLocator(t) && hasPrefixFold(m.RL, t)) {
			isReceiver = true
		}
	}

	// Area-of-interest match (O(1), additive): a sender/receiver locator within
	// areaRings grid-squares of the area centre also counts. Backs region feeds.
	if client.areaActive {
		if x, y, ok := maidenhead.SquareXYFold(m.SL); ok && absInt(x-client.areaX) <= client.areaRings && absInt(y-client.areaY) <= client.areaRings {
			isSender = true
		}
		if x, y, ok := maidenhead.SquareXYFold(m.RL); ok && absInt(x-client.areaX) <= client.areaRings && absInt(y-client.areaY) <= client.areaRings {
			isReceiver = true
		}
	}

	if logLevel == "DEBUG" {
		logDebug("QTH '%v' | Evaluating Spot -> SC:%s RC:%s SL:%s RL:%s | isSender:%v isReceiver:%v", client.qthSet, sc, rc, strings.ToUpper(m.SL), strings.ToUpper(m.RL), isSender, isReceiver)
	}

	if !isSender && !isReceiver {
		return Spot{}, false
	}

	var remoteLocator string
	var relation string
	if isSender {
		remoteLocator = strings.ToUpper(m.RL)
		relation = "Sender"
	} else {
		remoteLocator = strings.ToUpper(m.SL)
		relation = "Receiver"
	}

	if remoteLocator == "" {
		if logLevel == "DEBUG" {
			logDebug("QTH '%v' matched as %s, but remote locator is empty. Dropping spot.", client.qthSet, relation)
		}
		return Spot{}, false
	}

	lat, lng := maidenhead.ToLatLng(remoteLocator)
	age := ageClamped(now, m.T)

	if logLevel == "DEBUG" {
		logDebug("QTH '%v' matched successfully! Mapped to Remote Locator: %s", client.qthSet, remoteLocator)
	}

	return Spot{
		Lat:             lat,
		Lng:             lng,
		SNR:             m.RP,
		AgeSeconds:      age,
		Locator:         remoteLocator,
		ReporterLocator: reporterLocatorForMessage(m),
		SourceType:      sourceTypeForMessage(m),
		Band:            m.B,
		Sender:          m.SC,
		Receiver:        m.RC,
		T:               m.T,
	}, true
}

// hasPrefixFold reports whether s starts with prefix, ignoring ASCII case.
// prefix must already be upper case (qthSet entries are). An empty s never
// matches a non-empty prefix, like the strings.HasPrefix(ToUpper(s), prefix)
// it replaces.
func hasPrefixFold(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		if c != prefix[i] {
			return false
		}
	}
	return true
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
