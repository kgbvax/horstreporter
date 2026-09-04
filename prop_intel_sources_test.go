package main

import (
	"net/http/httptest"
	"testing"
)

func TestSourceProfilesSanity(t *testing.T) {
	if len(propIntelSourceProfiles) != 4 {
		t.Fatalf("expected 4 profiles, got %d", len(propIntelSourceProfiles))
	}
	seenTag := map[string]string{}
	seenName := map[string]string{}
	for _, p := range propIntelSourceProfiles {
		if p.PublicName == "" || p.InternalTag == "" {
			t.Fatalf("profile missing names: %+v", p)
		}
		if prev, dup := seenTag[p.InternalTag]; dup {
			t.Fatalf("internal tag %q claimed by both %q and %q", p.InternalTag, prev, p.PublicName)
		}
		seenTag[p.InternalTag] = p.PublicName
		if prev, dup := seenName[p.PublicName]; dup {
			t.Fatalf("public name %q duplicated (%q)", p.PublicName, prev)
		}
		seenName[p.PublicName] = p.InternalTag
		if p.ReceiverSide != "sc" && p.ReceiverSide != "rc" {
			t.Fatalf("profile %q has invalid ReceiverSide %q", p.PublicName, p.ReceiverSide)
		}
	}

	wspr := propIntelProfilesByName["wspr"]
	if !wspr.HasTXPower || wspr.SSBFloorDb == nil || wspr.CWFloorDb == nil {
		t.Fatalf("wspr must keep the budget model: %+v", wspr)
	}
	if *wspr.SSBFloorDb != 10.0 || *wspr.CWFloorDb != -5.0 {
		t.Fatalf("wspr floors diverged from v1 budget model: %+v", wspr)
	}

	rbn := propIntelProfilesByName["rbn"]
	if rbn.SSBFloorDb != nil {
		t.Fatalf("rbn is a CW skimmer — ssb floor must be nil: %+v", rbn)
	}
	if rbn.CWFloorDb == nil || *rbn.CWFloorDb != 8.0 {
		t.Fatalf("rbn cw floor expected +8: %+v", rbn)
	}

	dxc := propIntelProfilesByName["dxcluster"]
	if dxc.SSBFloorDb != nil || dxc.CWFloorDb != nil || dxc.DigitalFloorDb != nil {
		t.Fatalf("dxcluster carries no amplitude — all floors must be nil: %+v", dxc)
	}
	if dxc.PresenceMinSpots < 2 {
		t.Fatalf("dxcluster presence floor must guard against busted calls: %+v", dxc)
	}

	pskr := propIntelProfilesByName["pskr"]
	if pskr.ReceiverSide != "rc" {
		t.Fatalf("pskr receiver is the reporter (RC/RL): %+v", pskr)
	}
	for _, other := range []string{"wspr", "rbn", "dxcluster"} {
		if propIntelProfilesByName[other].ReceiverSide != "sc" {
			t.Fatalf("%s receiver side must be sc", other)
		}
	}
}

func TestParseSourcesParam(t *testing.T) {
	names := func(ps []propIntelSourceProfile) []string {
		out := make([]string, len(ps))
		for i, p := range ps {
			out[i] = p.PublicName
		}
		return out
	}
	join := func(xs []string) string {
		s := ""
		for i, x := range xs {
			if i > 0 {
				s += ","
			}
			s += x
		}
		return s
	}

	// Empty → all four, canonical order.
	r := httptest.NewRequest("GET", "/api/prop_intel/v2", nil)
	got := join(names(parseSourcesParam(r)))
	if got != "wspr,pskr,rbn,dxcluster" {
		t.Fatalf("empty sources param = all four, got %q", got)
	}

	// CSV.
	r = httptest.NewRequest("GET", "/api/prop_intel/v2?sources=rbn,wspr", nil)
	got = join(names(parseSourcesParam(r)))
	if got != "wspr,rbn" {
		t.Fatalf("csv must come back in canonical order, got %q", got)
	}

	// Repeated keys.
	r = httptest.NewRequest("GET", "/api/prop_intel/v2?sources=rbn&sources=dxcluster", nil)
	got = join(names(parseSourcesParam(r)))
	if got != "rbn,dxcluster" {
		t.Fatalf("repeated keys, got %q", got)
	}

	// Unknown values dropped; all-unknown falls back to all four.
	r = httptest.NewRequest("GET", "/api/prop_intel/v2?sources=wspr,bogus", nil)
	got = join(names(parseSourcesParam(r)))
	if got != "wspr" {
		t.Fatalf("unknown dropped, got %q", got)
	}
	r = httptest.NewRequest("GET", "/api/prop_intel/v2?sources=bogus,fake", nil)
	got = join(names(parseSourcesParam(r)))
	if got != "wspr,pskr,rbn,dxcluster" {
		t.Fatalf("all-unknown = all four, got %q", got)
	}
}

func TestReceiverEnds(t *testing.T) {
	m := MQTTMessage{SC: "skimmer", SL: " jo62qm ", RC: "dx", RL: "kn87aa"}
	recvLoc, recvCall, otherLoc, otherCall := propIntelProfilesByName["rbn"].receiverEnds(m)
	if recvLoc != "JO62QM" || recvCall != "SKIMMER" || otherLoc != "KN87AA" || otherCall != "DX" {
		t.Fatalf("sc-side receiver pick wrong: %q %q %q %q", recvLoc, recvCall, otherLoc, otherCall)
	}
	recvLoc, recvCall, otherLoc, otherCall = propIntelProfilesByName["pskr"].receiverEnds(m)
	if recvLoc != "KN87AA" || recvCall != "DX" || otherLoc != "JO62QM" || otherCall != "SKIMMER" {
		t.Fatalf("rc-side receiver pick wrong: %q %q %q %q", recvLoc, recvCall, otherLoc, otherCall)
	}
}
