package main

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func withLiveHistoryState(t *testing.T, since, gapStart, gapEnd int64, retention int) {
	t.Helper()
	oldSince, oldGS, oldGE, oldRet := liveHistoryCompleteSince.Load(), liveHistoryGapStart.Load(), liveHistoryGapEnd.Load(), liveHistoryRetentionMinutes
	liveHistoryCompleteSince.Store(since)
	liveHistoryGapStart.Store(gapStart)
	liveHistoryGapEnd.Store(gapEnd)
	liveHistoryRetentionMinutes = retention
	t.Cleanup(func() {
		liveHistoryCompleteSince.Store(oldSince)
		liveHistoryGapStart.Store(oldGS)
		liveHistoryGapEnd.Store(oldGE)
		liveHistoryRetentionMinutes = oldRet
	})
}

func TestHubCoversWindow(t *testing.T) {
	now := time.Now().Unix()
	cases := []struct {
		name               string
		since, gs, ge      int64
		retention, minutes int
		want               bool
	}{
		{"complete, inside retention", now - 3600, 0, 0, 60, 15, true},
		{"no constraint recorded", 0, 0, 0, 60, 15, true},
		{"longer than retention", now - 7200, 0, 0, 60, 90, false},
		{"history younger than the window", now - 300, 0, 0, 60, 15, false},
		// A restart gap does not make the hub differ from Postgres (both lack the
		// downtime), so it must not send the window to Postgres.
		{"gap inside the window", now - 3600, now - 600, now - 540, 60, 15, true},
		{"gap older than the window", now - 3600, now - 2000, now - 1900, 60, 15, true},
		{"zero minutes", 0, 0, 0, 60, 0, false},
		{"unlimited retention", 0, 0, 0, 0, 120, true},
	}
	for _, c := range cases {
		withLiveHistoryState(t, c.since, c.gs, c.ge, c.retention)
		if got := hubCoversWindow(now, c.minutes); got != c.want {
			t.Errorf("%s: hubCoversWindow = %v, want %v", c.name, got, c.want)
		}
	}
}

// For a locator qth the hub-derived series must equal the series the
// area-based binning produces for the same square (it is that function), and
// the position list from the area index must not change it.
func TestHubActivityByBinMatchesFullScanAndAreaIndex(t *testing.T) {
	resetAreaIndex(t)
	rng := rand.New(rand.NewSource(5))
	now := time.Now().Unix()
	withLiveHistoryState(t, now-3600, 0, 0, 60)
	msgs := randomWindowMsgs(rng, 30000, now-900, now)
	win := historyWindow{msgs: msgs, firstSeq: 1, hubFirst: 1}

	for _, tc := range []struct {
		qth          string
		surroundings bool
	}{{"JO32", false}, {"JO32", true}, {"JO32QM", false}, {"FN20", false}} {
		full, ok := hubActivityByBin(tc.qth, tc.surroundings, msgs, nil, false, -15, 15, now)
		if !ok {
			t.Fatalf("%+v: hub should cover the window", tc)
		}
		key, _ := areaIndexKeyFor(tc.qth, tc.surroundings, nil, "")
		pos, usePos := areaIdx.positions(key, win)
		viaIndex, _ := hubActivityByBin(tc.qth, tc.surroundings, msgs, pos, usePos, -15, 15, now)
		if !reflect.DeepEqual(full, viaIndex) {
			t.Fatalf("%+v: series differs when walking the index positions", tc)
		}
		radius := 0
		if tc.surroundings {
			radius = 1
		}
		x, y, _ := locatorSquareXY(tc.qth[:4])
		want := buildActivityByBinFromHistory(msgs, &liveArea{Centre: tc.qth[:4], BaseRadius: radius, Radius: radius, x: x, y: y}, -15, 15, now)
		if len(want) == 0 {
			if full != nil {
				t.Fatalf("%+v: no matches must return nil, got %v", tc, full)
			}
			continue
		}
		if !reflect.DeepEqual(full, want) {
			t.Fatalf("%+v: hub series differs from the area binning", tc)
		}
	}
}

func TestHubActivityByBinDeclinesWhatItCannotAnswer(t *testing.T) {
	now := time.Now().Unix()
	withLiveHistoryState(t, now-3600, 0, 0, 60)
	msgs := []MQTTMessage{{T: now - 10, B: "20m", MD: "FT8", SL: "JO32aa", RL: "FN20bb", RP: -5}}
	if _, ok := hubActivityByBin("DK3JF", false, msgs, nil, false, -15, 15, now); ok {
		t.Error("a callsign qth must go to Postgres")
	}
	if _, ok := hubActivityByBin("JO32", false, msgs, nil, false, -15, 180, now); ok {
		t.Error("a window beyond the retention must go to Postgres")
	}
	got, ok := hubActivityByBin("JO32", false, msgs, nil, false, -15, 15, now)
	if !ok || len(got["20m"]) != activityBins {
		t.Fatalf("expected a 20m series, got %v ok=%v", got, ok)
	}
}

func TestTargetArmsTimeFirstDefeatsLocatorIndexes(t *testing.T) {
	arms, args, next := appendTargetArmsOpt(nil, nil, 5, []string{"PM95"}, []string{"DK3JF"}, true)
	if next != 5+2+1 || len(args) != 3 || len(arms) != 2 {
		t.Fatalf("arms=%v args=%v next=%d", arms, args, next)
	}
	want := "(((sender_locator || '') ~>=~ $5 AND (sender_locator || '') ~<~ $6) OR ((receiver_locator || '') ~>=~ $5 AND (receiver_locator || '') ~<~ $6))"
	if arms[0] != want {
		t.Fatalf("time-first arm:\n got  %s\n want %s", arms[0], want)
	}
	plain, _, _ := appendTargetArms(nil, nil, 5, []string{"PM95"}, nil)
	if plain[0] != "((sender_locator ~>=~ $5 AND sender_locator ~<~ $6) OR (receiver_locator ~>=~ $5 AND receiver_locator ~<~ $6))" {
		t.Fatalf("the default arm changed: %s", plain[0])
	}
}
