package main

import (
	"strings"
)

type Spot struct {
	Lat        float64 `json:"lat"`
	Lng        float64 `json:"lng"`
	SNR        int     `json:"snr"`
	AgeSeconds int64   `json:"ageSeconds"`
	Locator    string  `json:"locator"`
	Band       string  `json:"band"`
	Sender     string  `json:"sender"`
	Receiver   string  `json:"receiver"`
}

// matchCall checks for an exact callsign match, or a match with common prefix/suffix modifiers (e.g., W1AW/P, DL/W1AW)
func matchCall(spotCall, target string) bool {
	if spotCall == target {
		return true
	}
	if strings.HasPrefix(spotCall, target+"/") {
		return true
	}
	if strings.HasSuffix(spotCall, "/"+target) {
		return true
	}
	if strings.Contains(spotCall, "/"+target+"/") {
		return true
	}
	return false
}

func matchAndCreateSpot(client *Client, m MQTTMessage, now int64) (Spot, bool) {
	if len(client.targets) == 0 {
		return Spot{}, false
	}

	sc, rc := strings.ToUpper(m.SC), strings.ToUpper(m.RC)
	sl, rl := strings.ToUpper(m.SL), strings.ToUpper(m.RL)

	isSender := false
	isReceiver := false

	for _, t := range client.targets {
		if matchCall(sc, t) || (isLocator(t) && sl != "" && strings.HasPrefix(sl, t)) {
			isSender = true
		}
		if matchCall(rc, t) || (isLocator(t) && rl != "" && strings.HasPrefix(rl, t)) {
			isReceiver = true
		}
	}

	if logLevel == "DEBUG" {
		logDebug("Targets '%v' | Evaluating Spot -> SC:%s RC:%s SL:%s RL:%s | isSender:%v isReceiver:%v", client.targets, sc, rc, sl, rl, isSender, isReceiver)
	}

	if !isSender && !isReceiver {
		return Spot{}, false
	}

	var remoteLocator string
	var relation string
	if isSender {
		remoteLocator = rl
		relation = "Sender"
	} else {
		remoteLocator = sl
		relation = "Receiver"
	}

	if remoteLocator == "" {
		if logLevel == "DEBUG" {
			logDebug("Targets '%v' matched as %s, but remote locator is empty. Dropping spot.", client.targets, relation)
		}
		return Spot{}, false
	}

	lat, lng := locatorToLatLng(remoteLocator)
	age := now - m.T
	if age < 0 {
		age = 0
	}

	if logLevel == "DEBUG" {
		logDebug("Targets '%v' matched successfully! Mapped to Remote Locator: %s", client.targets, remoteLocator)
	}

	return Spot{
		Lat:        lat,
		Lng:        lng,
		SNR:        m.RP,
		AgeSeconds: age,
		Locator:    remoteLocator,
		Band:       m.B,
		Sender:     m.SC,
		Receiver:   m.RC,
	}, true
}

func isLocator(s string) bool {
	if len(s) < 4 {
		return false
	}
	if s[0] < 'A' || s[0] > 'R' || s[1] < 'A' || s[1] > 'R' {
		return false
	}
	if s[2] < '0' || s[2] > '9' || s[3] < '0' || s[3] > '9' {
		return false
	}
	return true
}

func getSurroundingSquares(locator string) []string {
	if !isLocator(locator) {
		return []string{locator}
	}
	loc := strings.ToUpper(locator[:4])
	x := int(loc[0]-'A')*10 + int(loc[2]-'0')
	y := int(loc[1]-'A')*10 + int(loc[3]-'0')

	var res []string
	for dx := -1; dx <= 1; dx++ {
		for dy := -1; dy <= 1; dy++ {
			nx, ny := x+dx, y+dy
			if nx >= 0 && nx < 180 && ny >= 0 && ny < 180 {
				char0 := byte('A' + nx/10)
				char1 := byte('A' + ny/10)
				char2 := byte('0' + nx%10)
				char3 := byte('0' + ny%10)
				res = append(res, string([]byte{char0, char1, char2, char3}))
			}
		}
	}
	return res
}

func locatorToLatLng(locator string) (float64, float64) {
	locator = strings.ToUpper(locator)
	if len(locator) < 2 {
		return 0, 0
	}
	lng := float64(locator[0]-'A')*20 - 180
	lat := float64(locator[1]-'A')*10 - 90

	if len(locator) >= 4 {
		lng += float64(locator[2]-'0') * 2
		lat += float64(locator[3]-'0') * 1
		if len(locator) >= 6 {
			lng += float64(locator[4]-'A')*(5.0/60.0) + (5.0 / 120.0)
			lat += float64(locator[5]-'A')*(2.5/60.0) + (2.5 / 120.0)
		} else {
			lng += 1.0
			lat += 0.5
		}
	} else {
		lng += 10.0 // Center of field
		lat += 5.0
	}
	return lat, lng
}
