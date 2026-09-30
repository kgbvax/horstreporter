package main

import (
	"encoding/json"
	"strconv"
)

// v2 stream wire format (opt-in via ?v=2). Spots travel as tuples inside one
// `spots` event per flush, without the derived lat/lng (the browser computes
// them from the locator) or the "mqtt" default:
//
//	event: spots
//	id: <epoch>-<seq>                                     (live frames only)
//	data: {"n":<server unix s>,"s":[[loc,band,snr,age,rep?,src?,snd?,rcv?],…]}
//
// age = n - spot time (may be negative under clock skew; the client clamps).
// Trailing empty string elements are trimmed. src: "" mqtt, "d" dxcluster,
// "r" rbn, "w" wspr, anything else verbatim.

// v2SourceCode maps a sourceType to its v2 code.
func v2SourceCode(src string) string {
	switch src {
	case "", "mqtt":
		return ""
	case "dxcluster":
		return "d"
	case "rbn":
		return "r"
	case "wspr":
		return "w"
	}
	return src
}

// appendV2String appends s as a JSON string. Printable ASCII without quote or
// backslash takes a fast path; everything else goes through encoding/json.
func appendV2String(buf []byte, s string) []byte {
	plain := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c > 0x7e || c == '"' || c == '\\' {
			plain = false
			break
		}
	}
	if plain {
		buf = append(buf, '"')
		buf = append(buf, s...)
		return append(buf, '"')
	}
	b, _ := json.Marshal(s)
	return append(buf, b...)
}

// appendV2Spot appends one spot tuple; n is the frame's server time.
func appendV2Spot(buf []byte, s Spot, n int64) []byte {
	rep := s.ReporterLocator
	src := v2SourceCode(s.SourceType)
	var snd, rcv string
	if s.SourceType == "dxcluster" {
		snd, rcv = s.Sender, s.Receiver
	}

	// Number of optional string elements to emit after age.
	extra := 0
	switch {
	case rcv != "":
		extra = 4
	case snd != "":
		extra = 3
	case src != "":
		extra = 2
	case rep != "":
		extra = 1
	}

	buf = append(buf, '[')
	buf = appendV2String(buf, s.Locator)
	buf = append(buf, ',')
	buf = appendV2String(buf, s.Band)
	buf = append(buf, ',')
	buf = strconv.AppendInt(buf, int64(s.SNR), 10)
	buf = append(buf, ',')
	buf = strconv.AppendInt(buf, n-s.T, 10)
	for i, v := range [4]string{rep, src, snd, rcv} {
		if i >= extra {
			break
		}
		buf = append(buf, ',')
		buf = appendV2String(buf, v)
	}
	return append(buf, ']')
}

// appendV2SpotsFrame appends a complete `spots` event. id may be empty.
func appendV2SpotsFrame(buf []byte, spots []Spot, n int64, id string) []byte {
	buf = append(buf, "event: spots\n"...)
	if id != "" {
		buf = append(buf, "id: "...)
		buf = append(buf, id...)
		buf = append(buf, '\n')
	}
	buf = append(buf, `data: {"n":`...)
	buf = strconv.AppendInt(buf, n, 10)
	buf = append(buf, `,"s":[`...)
	for i, s := range spots {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = appendV2Spot(buf, s, n)
	}
	return append(buf, "]}\n\n"...)
}

// streamEventID formats a resume id.
func streamEventID(seq uint64) string {
	return hubEpoch + "-" + strconv.FormatUint(seq, 10)
}

// parseStreamEventID splits an id produced by streamEventID.
func parseStreamEventID(id string) (epoch string, seq uint64, ok bool) {
	for i := len(id) - 1; i > 0; i-- {
		if id[i] == '-' {
			v, err := strconv.ParseUint(id[i+1:], 10, 64)
			if err != nil {
				return "", 0, false
			}
			return id[:i], v, true
		}
	}
	return "", 0, false
}
