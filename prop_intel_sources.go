package main

import (
	"net/http"
	"strings"
)

// prop_intel_sources.go defines the per-ingest source profiles for the v2
// propagation-intelligence engine (/api/prop_intel/v2). Each profile captures
// how honest "path open" signals can be computed from that source: WSPR has a
// real power-budget model, PSKReporter/RBN reports carry SNR on their own
// scales, and DX-cluster spots carry no amplitude at all.

// propIntelSourceProfile describes the SNR semantics and field conventions of
// one ingest source for the v2 engine.
type propIntelSourceProfile struct {
	// PublicName is the API/DB-facing source name: "wspr" | "pskr" | "rbn" |
	// "dxcluster".
	PublicName string
	// InternalTag is the MQTTMessage.Source / dx_raw_spots.source_type value.
	InternalTag string
	// ReceiverSide picks which end of the message is the receiver/reporter
	// (the region a path "lands in"): "sc" = SC/SL (wspr, rbn, dxcluster),
	// "rc" = RC/RL (pskr, where SC/SL is the transmitter).
	ReceiverSide string
	// HasTXPower: only WSPR carries beacon TX power (dBm), enabling the
	// budget model effective_snr = snr + (50dBm - tx_power_dbm).
	HasTXPower bool
	// SSBFloorDb / CWFloorDb: effective (or raw, per basis) SNR floors for
	// declaring a cell SSB/CW-open. nil = structurally not computable from
	// this source (e.g. RBN is a CW skimmer; it can never prove SSB).
	SSBFloorDb *float64
	CWFloorDb  *float64
	// DigitalFloorDb: floor for the mode-agnostic "digital open" signal on
	// SNR-carrying sources (pskr only; wspr implies digital via budget).
	DigitalFloorDb *float64
	// PresenceMinSpots: for amplitude-free sources (dxcluster), a cell is
	// open when the window holds at least this many spots (1 spot may be a
	// busted callsign).
	PresenceMinSpots int
}

// fptr returns a pointer to a float64 constant (profile table sugar).
func fptr(v float64) *float64 { return &v }

// propIntelSourceProfiles is the canonical profile table, in display order.
// Floor rationale (heuristics, surfaced as open_basis in the v2 contract):
//   - wspr:    unchanged KTD8 budget model (prop_intel.go) — SSB +10 dB,
//     CW -5 dB effective SNR at 100W reference. Basis "budget".
//   - pskr:    FT8/FT4 reports on the 2.5 kHz-equivalent scale: a decode at
//     -24 dB proves a digital path; -18 dB implies comfortable CW; -5 dB
//     suggests an SSB-workable path. TX power unknown → the cell carries
//     unknown_power. Basis "snr_floor".
//   - rbn:     CW/RTTY skimmer, 0..40 dB scale; CW open at >= +8 dB.
//     ssb_open is nil (a CW skimmer can never prove SSB). Basis "snr_floor".
//   - dxcluster: no amplitude at all; open = someone actually worked/reported
//     the path (>= 2 spots guard against busted calls). Basis "presence".
var propIntelSourceProfiles = []propIntelSourceProfile{
	{
		PublicName:   "wspr",
		InternalTag:  "wspr",
		ReceiverSide: "sc",
		HasTXPower:   true,
		SSBFloorDb:   fptr(10.0),
		CWFloorDb:    fptr(-5.0),
	},
	{
		PublicName:     "pskr",
		InternalTag:    "mqtt",
		ReceiverSide:   "rc",
		SSBFloorDb:     fptr(-5.0),
		CWFloorDb:      fptr(-18.0),
		DigitalFloorDb: fptr(-24.0),
	},
	{
		PublicName:   "rbn",
		InternalTag:  "rbn",
		ReceiverSide: "sc",
		CWFloorDb:    fptr(8.0),
	},
	{
		PublicName:       "dxcluster",
		InternalTag:      "dxcluster",
		ReceiverSide:     "sc",
		PresenceMinSpots: 2,
	},
}

// propIntelProfilesByTag indexes profiles by MQTTMessage.Source.
var propIntelProfilesByTag = func() map[string]propIntelSourceProfile {
	m := make(map[string]propIntelSourceProfile, len(propIntelSourceProfiles))
	for _, p := range propIntelSourceProfiles {
		m[p.InternalTag] = p
	}
	return m
}()

// propIntelProfilesByName indexes profiles by their public (API) name.
var propIntelProfilesByName = func() map[string]propIntelSourceProfile {
	m := make(map[string]propIntelSourceProfile, len(propIntelSourceProfiles))
	for _, p := range propIntelSourceProfiles {
		m[p.PublicName] = p
	}
	return m
}()

// parseSourcesParam parses the sources query parameter for /api/prop_intel/v2.
// Accepts both CSV (?sources=wspr,pskr) and repeated keys
// (?sources=wspr&sources=rbn). Unknown names are dropped; an empty effective
// selection defaults to all profiles. The result is in canonical order.
func parseSourcesParam(r *http.Request) []propIntelSourceProfile {
	vals, ok := r.URL.Query()["sources"]
	wanted := make(map[string]struct{}, len(propIntelSourceProfiles))
	if ok {
		for _, v := range vals {
			for _, name := range strings.Split(v, ",") {
				name = strings.ToLower(strings.TrimSpace(name))
				if _, known := propIntelProfilesByName[name]; known {
					wanted[name] = struct{}{}
				}
			}
		}
	}
	if len(wanted) == 0 {
		return append([]propIntelSourceProfile(nil), propIntelSourceProfiles...)
	}
	out := make([]propIntelSourceProfile, 0, len(wanted))
	for _, p := range propIntelSourceProfiles {
		if _, ok := wanted[p.PublicName]; ok {
			out = append(out, p)
		}
	}
	return out
}

// receiverLocator returns the receiver/reporter-side fields per the profile's
// ReceiverSide convention: the end whose region a path "lands in".
func (p propIntelSourceProfile) receiverEnds(m MQTTMessage) (recvLoc, recvCall, otherLoc, otherCall string) {
	sl := strings.ToUpper(strings.TrimSpace(m.SL))
	rl := strings.ToUpper(strings.TrimSpace(m.RL))
	sc := strings.ToUpper(strings.TrimSpace(m.SC))
	rc := strings.ToUpper(strings.TrimSpace(m.RC))
	if p.ReceiverSide == "rc" {
		return rl, rc, sl, sc
	}
	return sl, sc, rl, rc
}
