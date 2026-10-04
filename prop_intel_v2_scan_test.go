package main

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"horstreporter/internal/region"
)

// scanPropIntelV2WindowRef is the pre-optimisation scan (string-keyed maps,
// upper-cased locators via resolvePropIntelRemoteEndArea), kept verbatim as
// the behavioural reference.
func scanPropIntelV2WindowRef(history []MQTTMessage, profiles []propIntelSourceProfile, qthSet []string, matchArea *liveArea, now, cutoff, midpoint int64) (map[propIntelV2CellKey]*propIntelV2Acc, map[propIntelCellKey]bool, map[string]struct{}) {
	profByTag := make(map[string]propIntelSourceProfile, len(profiles))
	for _, p := range profiles {
		profByTag[p.InternalTag] = p
	}
	cellAccs := make(map[propIntelV2CellKey]*propIntelV2Acc)
	cellFromHere := make(map[propIntelCellKey]bool)
	bandsSeen := make(map[string]struct{})
	regions := newRegionMemo()

	for _, m := range history {
		if m.T > now || m.T < cutoff {
			continue
		}
		src := m.Source
		if src == "" {
			src = "mqtt"
		}
		prof, ok := profByTag[src]
		if !ok {
			continue
		}
		band := normalizeBand(m.B)
		if band == "" || !bandInScope(band) {
			continue
		}
		remoteLocator, _, matchedEnd := resolvePropIntelRemoteEndArea(m, qthSet, matchArea, prof.ReceiverSide)
		if remoteLocator == "" || !isLocator(remoteLocator) {
			continue
		}
		reg := regions.get(remoteLocator)
		if reg == region.Unknown {
			continue
		}
		regionCode := string(reg)
		ck := propIntelV2CellKey{band: band, region: regionCode, source: prof.PublicName}
		cell := cellAccs[ck]
		if cell == nil {
			cell = &propIntelV2Acc{}
			cellAccs[ck] = cell
		}
		cell.spotCount++
		bandsSeen[band] = struct{}{}
		if prof.HasTXPower && m.TXPower > 0 {
			eff := float64(m.RP) + propIntelReferencePowerDbm - float64(m.TXPower)
			if !cell.hasPower || eff > cell.bestBudgetSNR {
				cell.bestBudgetSNR = eff
			}
			cell.hasPower = true
		}
		if prof.InternalTag == "mqtt" || prof.InternalTag == "rbn" {
			if !cell.hasReport || m.RP > cell.bestReportSNR {
				cell.bestReportSNR = m.RP
			}
			cell.hasReport = true
		}
		if matchedEnd {
			cellFromHere[propIntelCellKey{band: band, region: regionCode}] = true
		}
		if m.T < midpoint {
			cell.firstHalfSpots++
		} else {
			cell.secondHalfSpots++
		}
	}
	return cellAccs, cellFromHere, bandsSeen
}

func TestScanPropIntelV2WindowMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(21))
	now := time.Now().Unix()
	// Wide spread of squares so every region shows up, plus the near-JO32 mix.
	msgs := randomWindowMsgs(rng, 40000, now-1800, now)
	letters := "ABCDEFGHIJKLMNOPQR"
	for i := range msgs {
		if i%3 == 0 {
			msgs[i].SL = string([]byte{letters[rng.Intn(18)], letters[rng.Intn(18)], byte('0' + rng.Intn(10)), byte('0' + rng.Intn(10))}) + strings.ToLower(string([]byte{letters[rng.Intn(18)], letters[rng.Intn(18)]}))
		}
		if i%3 == 1 {
			msgs[i].RL = string([]byte{letters[rng.Intn(18)], letters[rng.Intn(18)], byte('0' + rng.Intn(10)), byte('0' + rng.Intn(10))})
		}
		if i%7 == 0 {
			msgs[i].TXPower = []int{0, 20, 23, 37, 43}[rng.Intn(5)]
		}
		if i%11 == 0 {
			msgs[i].T = now - 7200 // outside the window
		}
	}
	profileSets := [][]propIntelSourceProfile{propIntelSourceProfiles, propIntelSourceProfiles[:1], propIntelSourceProfiles[1:3], {propIntelSourceProfiles[3], propIntelSourceProfiles[0]}}
	qthSets := [][]string{qthSquares("JO32", false), qthSquares("JO32", true), qthSquares("DK3JF", false), qthSquares("FN20", false)}
	areas := []*liveArea{nil, explicitLiveArea("JO32", 2), explicitLiveArea("FN20", 3)}

	cutoff := now - 900
	midpoint := cutoff + (now-cutoff)/2
	regionsSeen := map[string]bool{}
	for pi, profiles := range profileSets {
		for qi, qs := range qthSets {
			for ai, area := range areas {
				got1, got2, got3 := scanPropIntelV2Window(msgs, profiles, qs, area, now, cutoff, midpoint)
				want1, want2, want3 := scanPropIntelV2WindowRef(msgs, profiles, qs, area, now, cutoff, midpoint)
				if !reflect.DeepEqual(got1, want1) || !reflect.DeepEqual(got2, want2) || !reflect.DeepEqual(got3, want3) {
					t.Fatalf("profiles=%d qth=%d area=%d: scan differs from the reference (cells %d vs %d, fromHere %d vs %d, bands %d vs %d)",
						pi, qi, ai, len(got1), len(want1), len(got2), len(want2), len(got3), len(want3))
				}
				for k := range want1 {
					regionsSeen[k.region] = true
				}
			}
		}
	}
	if len(regionsSeen) < 8 {
		t.Fatalf("only %d regions exercised: the comparison is too narrow", len(regionsSeen))
	}
}

func TestRawRegionMemoMatchesCachedLookup(t *testing.T) {
	rng := rand.New(rand.NewSource(8))
	m := newRawRegionMemo()
	letters := "ABCDEFGHIJKLMNOPQRS"
	for i := 0; i < 20000; i++ {
		b := []byte{letters[rng.Intn(19)], letters[rng.Intn(19)], byte('0' + rng.Intn(10)), byte('0' + rng.Intn(10)), letters[rng.Intn(18)] + 32*byte(rng.Intn(2)), letters[rng.Intn(18)] + 32*byte(rng.Intn(2))}
		raw := string(b[:4+2*rng.Intn(2)])
		want := -1
		if up := strings.ToUpper(raw); isLocator(up) {
			if reg := regionFromLocatorCached(up); reg != region.Unknown {
				want = regionIndex[reg]
			}
		}
		if got := m.get(raw); got != want {
			t.Fatalf("get(%q) = %d, want %d", raw, got, want)
		}
	}
	for _, raw := range []string{"", "J", "ZZ99", "jo3x"} {
		if got := m.get(raw); got != -1 {
			t.Fatalf("get(%q) = %d, want -1", raw, got)
		}
	}
}

func TestEveryRegionHasAnIndexAndBandHelpersAgree(t *testing.T) {
	for i, r := range region.AllRegions() {
		if regionIndex[r] != i {
			t.Fatalf("regionIndex[%s] = %d, want %d", r, regionIndex[r], i)
		}
	}
	// FromLocator may only return AllRegions members or Unknown, or the index
	// would silently file a region under EU.
	for a := 'A'; a <= 'R'; a++ {
		for b := 'A'; b <= 'R'; b++ {
			for d := '0'; d <= '9'; d += 3 {
				loc := string([]rune{a, b, d, '5'})
				if r := regionFromLocatorCached(loc); r != region.Unknown {
					if _, ok := regionIndex[r]; !ok {
						t.Fatalf("%s resolves to %q, which has no index", loc, r)
					}
				}
			}
		}
	}
	if len(inScopeBandNames) != len(propIntelBandOrder) {
		t.Fatalf("%d in-scope names vs %d in propIntelBandOrder", len(inScopeBandNames), len(propIntelBandOrder))
	}
	for i, b := range inScopeBandNames {
		if propIntelBandOrder[i] != b || inScopeBandIndex(b) != i {
			t.Fatalf("band %q: order/index mismatch", b)
		}
		if _, ok := bandsInScope[b]; !ok {
			t.Fatalf("band %q missing from bandsInScope", b)
		}
	}
	for b := range bandsInScope {
		if !bandInScope(b) {
			t.Fatalf("bandsInScope has %q but bandInScope rejects it", b)
		}
	}
	for _, raw := range []string{"20m", "20M", " 20m ", "20", "", "13cm", "70cm", "xyz", "160M", " 6 "} {
		want := -1
		if nb := normalizeBand(raw); nb != "" && bandInScope(nb) {
			want = inScopeBandIndex(nb)
		}
		if got := feedBandIndex(raw); got != want {
			t.Fatalf("feedBandIndex(%q) = %d, want %d", raw, got, want)
		}
	}
}

// scanPropIntelWindowRef is the pre-optimisation v1 scan, kept as the
// behavioural reference for scanPropIntelWindow.
func scanPropIntelWindowRef(history []MQTTMessage, qthSet []string, now, cutoff, midpoint int64) (map[propIntelCellKey]*propIntelCellAcc, map[string]struct{}) {
	acc := make(map[propIntelCellKey]*propIntelCellAcc)
	bandsSeen := make(map[string]struct{})
	regions := newRegionMemo()
	for _, m := range history {
		if m.T > now || m.T < cutoff {
			continue
		}
		if m.Source != "wspr" {
			continue
		}
		band := normalizeBand(m.B)
		if band == "" || !bandInScope(band) {
			continue
		}
		remoteLocator, remoteCall, matchedEnd := resolvePropIntelRemoteEnd(m, qthSet, "sc")
		if remoteLocator == "" || !isLocator(remoteLocator) {
			continue
		}
		reg := regions.get(remoteLocator)
		if reg == region.Unknown {
			continue
		}
		key := propIntelCellKey{band: band, region: string(reg)}
		cell := acc[key]
		if cell == nil {
			cell = &propIntelCellAcc{uniqueSenders: make(map[string]struct{}), sources: make(map[string]struct{})}
			acc[key] = cell
		}
		if remoteCall != "" {
			cell.uniqueSenders[remoteCall] = struct{}{}
		}
		cell.sources["wspr"] = struct{}{}
		cell.spotCount++
		bandsSeen[band] = struct{}{}
		if m.TXPower > 0 {
			cell.hasPower = true
			effectiveSNR := float64(m.RP) + propIntelReferencePowerDbm - float64(m.TXPower)
			if effectiveSNR > cell.bestBudgetSNR {
				cell.bestBudgetSNR = effectiveSNR
			}
		}
		if !cell.fromHere && matchedEnd {
			cell.fromHere = true
		}
		if m.T < midpoint {
			cell.firstHalfSpots++
		} else {
			cell.secondHalfSpots++
		}
	}
	return acc, bandsSeen
}

func TestScanPropIntelWindowMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(33))
	now := time.Now().Unix()
	msgs := randomWindowMsgs(rng, 40000, now-1800, now)
	letters := "ABCDEFGHIJKLMNOPQR"
	calls := []string{"DK3JF", "dl1abc", " W1AW ", "F5XYZ/P", "JA1ZZZ", "", "N0CALL"}
	for i := range msgs {
		msgs[i].Source = []string{"wspr", "wspr", "", "rbn"}[i%4]
		msgs[i].SC, msgs[i].RC = calls[rng.Intn(len(calls))], calls[rng.Intn(len(calls))]
		if i%3 == 0 {
			msgs[i].SL = string([]byte{letters[rng.Intn(18)], letters[rng.Intn(18)], byte('0' + rng.Intn(10)), byte('0' + rng.Intn(10))}) + strings.ToLower(string([]byte{letters[rng.Intn(18)], letters[rng.Intn(18)]}))
		}
		if i%5 == 0 {
			msgs[i].TXPower = []int{0, 20, 23, 37, 43}[rng.Intn(5)]
		}
	}
	cutoff := now - 900
	midpoint := cutoff + (now-cutoff)/2
	for _, qs := range [][]string{qthSquares("JO32", false), qthSquares("JO32", true), qthSquares("DK3JF", false), qthSquares("FN20", true), nil} {
		g1, g2 := scanPropIntelWindow(msgs, qs, now, cutoff, midpoint)
		w1, w2 := scanPropIntelWindowRef(msgs, qs, now, cutoff, midpoint)
		if !reflect.DeepEqual(g1, w1) || !reflect.DeepEqual(g2, w2) {
			t.Fatalf("qthSet %v: v1 scan differs from the reference (%d vs %d cells)", qs, len(g1), len(w1))
		}
		if len(w1) < 10 {
			t.Fatalf("qthSet %v: only %d cells, comparison too narrow", qs, len(w1))
		}
	}
}
