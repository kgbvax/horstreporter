package proplab

import (
	"math"
	"testing"
)

func TestReachActivityScoreAnchors(t *testing.T) {
	cases := []struct {
		r    float64
		want float64
	}{
		{0.4, 0}, {0.5, 0},
		{1.0, 15},
		{2.0, 50},
		{8.0, 50 + 100.0/3}, // log2(8/2)=2 → 50+100/3 ≈ 83.3
		{16.0, 100},
		{64.0, 100},
	}
	for _, c := range cases {
		got := reachActivityScore(c.r)
		if math.Abs(got-c.want) > 0.51 {
			t.Errorf("reachActivityScore(%v) = %v, want %v", c.r, got, c.want)
		}
	}
	// Monotonicity sweep.
	prev := reachActivityScore(0)
	for r := 0.1; r <= 64; r += 0.37 {
		cur := reachActivityScore(r)
		if cur < prev-1e-9 {
			t.Fatalf("score not monotone: r=%v got %v after %v", r, cur, prev)
		}
		prev = cur
	}
}

func baselineRows(band, region string, days int, links int64) []BaselineDayRow {
	out := make([]BaselineDayRow, days)
	for i := range out {
		out[i] = BaselineDayRow{Band: band, Region: region, Slot: 30, DayIndex: int64(20000 + i), LinkCount: links}
	}
	return out
}

func TestComposeReachIndexFactors(t *testing.T) {
	now := int64(1700000000)
	mkLive := func(band, region string, links int, reporters int) []DestRow {
		return []DestRow{{Band: band, Region: region, LinkCount: links, SpotCount: links, ReporterCount: reporters, DistMaxKm: 9000}}
	}

	// Ratio 2.0, full witnesses, persistence 1.0 -> index 50 (the open anchor).
	v := ComposeReach(ReachInputs{
		Now: now, LiveMinutes: 30,
		Live:        mkLive("20m", "JA", 60, 4),
		Baseline:    baselineRows("20m", "JA", 40, 1440), // p50 1440/day = 1 lpm
		Persistence: map[[2]string][2]int{{"20m", "JA"}: {4, 4}},
		SlotProfile: []BaselineDayRow{},
	})
	if len(v.Cells) != 1 {
		t.Fatalf("cells = %d, want 1", len(v.Cells))
	}
	c := v.Cells[0]
	if c.Index == nil || *c.Index != 50 {
		t.Errorf("anchor cell index = %v, want 50 (ratio=%v)", c.Index, c.ActivityRatio)
	}

	// One witness damps ×0.5 -> 25.
	v = ComposeReach(ReachInputs{
		Now: now, LiveMinutes: 30,
		Live:        mkLive("20m", "JA", 60, 1),
		Baseline:    baselineRows("20m", "JA", 40, 1440),
		Persistence: map[[2]string][2]int{{"20m", "JA"}: {4, 4}},
		SlotProfile: []BaselineDayRow{},
	})
	if v.Cells[0].Index == nil || *v.Cells[0].Index != 25 {
		t.Errorf("single-witness index = %v, want 25", v.Cells[0].Index)
	}

	// Zero witnesses: observed but no index (distinct from observed-dead).
	v = ComposeReach(ReachInputs{
		Now: now, LiveMinutes: 30,
		Live:        mkLive("20m", "JA", 60, 0),
		Baseline:    baselineRows("20m", "JA", 40, 1440),
		SlotProfile: []BaselineDayRow{},
	})
	if v.Cells[0].Index != nil {
		t.Errorf("zero-witness index = %v, want nil", v.Cells[0].Index)
	}

	// No baseline: thin proxy (lpm 2 -> curve 50) × 0.7 damping.
	v = ComposeReach(ReachInputs{
		Now: now, LiveMinutes: 30,
		Live:        mkLive("20m", "JA", 60, 4),
		Persistence: map[[2]string][2]int{{"20m", "JA"}: {4, 4}},
		SlotProfile: []BaselineDayRow{},
	})
	c = v.Cells[0]
	if !c.CellDataThin {
		t.Errorf("no-baseline cell not flagged thin")
	}
	if c.Index == nil || *c.Index != 35 {
		t.Errorf("thin index = %v, want 35 (50×0.7)", c.Index)
	}

	// Kp 6 -> absorption cap 15 even at huge activity.
	v = ComposeReach(ReachInputs{
		Now: now, LiveMinutes: 30,
		Live:        mkLive("20m", "JA", 600, 9),
		Baseline:    baselineRows("20m", "JA", 40, 1440),
		Persistence: map[[2]string][2]int{{"20m", "JA"}: {4, 4}},
		SW:          FusionSWSnapshot{Available: true, Kp: 6},
		SlotProfile: []BaselineDayRow{},
	})
	c = v.Cells[0]
	if c.Index == nil || *c.Index != reachAbsorbCap || !c.Capped || c.ClosureCause != "absorption_limited" {
		t.Errorf("absorption cell = %+v, want index 15 capped", c)
	}

	// Matching event caps at 20.
	v = ComposeReach(ReachInputs{
		Now: now, LiveMinutes: 30,
		Live:        mkLive("20m", "JA", 600, 9),
		Baseline:    baselineRows("20m", "JA", 40, 1440),
		Persistence: map[[2]string][2]int{{"20m", "JA"}: {4, 4}},
		Events:      []FusionEvent{{Title: "JA DXpedition", Region: "JA"}},
		SlotProfile: []BaselineDayRow{},
	})
	c = v.Cells[0]
	if c.Index == nil || *c.Index != reachEventCap || !c.Capped {
		t.Errorf("event cell index = %v, want 20 capped", c.Index)
	}
}

func TestDeriveSchedule(t *testing.T) {
	now := int64(1700000000)
	nowSlot := UTCSlotOfDay(now)

	// One pair usable for two runs: slots nowSlot+2..+5 and a wrap run 47,0,1.
	mk := func(band, region string, slots []int) []BaselineDayRow {
		var out []BaselineDayRow
		for _, sl := range slots {
			for d := 0; d < 20; d++ {
				out = append(out, BaselineDayRow{Band: band, Region: region, Slot: sl, DayIndex: int64(20000 + d), LinkCount: 10})
			}
		}
		return out
	}
	openSlots := []int{(nowSlot + 2) % 48, (nowSlot + 3) % 48, (nowSlot + 4) % 48, (nowSlot + 5) % 48}
	wrap := []int{47, 0, 1}
	profile := append(mk("15m", "JA", openSlots), mk("12m", "VK", wrap)...)

	got := deriveSchedule(profile, nil, now)
	if len(got) != 2 {
		t.Fatalf("schedule entries = %d, want 2: %+v", len(got), got)
	}
	ja := got[0]
	if ja.Band != "15m" || ja.Region != "JA" {
		t.Fatalf("first entry = %+v, want 15m JA (nearest opening)", ja)
	}
	if ja.MinutesToOpen != 60 {
		t.Errorf("minutes_to_open = %d, want 60", ja.MinutesToOpen)
	}
	if ja.OpenUTC != slotHHMM((nowSlot+2)%48) || ja.CloseUTC != slotHHMM((nowSlot+6)%48) {
		t.Errorf("window = %s-%s, want %s-%s", ja.OpenUTC, ja.CloseUTC, slotHHMM((nowSlot+2)%48), slotHHMM((nowSlot+6)%48))
	}
	vk := got[1]
	if vk.MinutesToOpen != ((47-nowSlot+48)%48)*30 {
		t.Errorf("wrap minutes_to_open = %d", vk.MinutesToOpen)
	}

	// Currently-reachable pair is excluded.
	got = deriveSchedule(profile, map[[2]string]bool{{"15m", "JA"}: true}, now)
	if len(got) != 1 || got[0].Band != "12m" {
		t.Errorf("with JA live: entries = %+v, want only 12m VK", got)
	}

	// Low presence (<0.35) suppresses: 20 days, only 5 active.
	var sparse []BaselineDayRow
	for d := 0; d < 20; d++ {
		links := int64(0)
		if d < 5 {
			links = 10
		}
		sparse = append(sparse, BaselineDayRow{Band: "15m", Region: "JA", Slot: 10, DayIndex: int64(21000 + d), LinkCount: links})
	}
	if got := deriveSchedule(sparse, nil, (1700000000+86400)); len(got) != 0 {
		t.Errorf("sparse presence produced %d entries, want 0", len(got))
	}
}

func TestDetectSurges(t *testing.T) {
	now := int64(1700000000)
	one := 70
	cur := ReachVerdict{
		GeneratedAt: now,
		Cells: []ReachCell{{
			Band: "10m", Region: "SA", Index: &one, ActivityRatio: 5.0,
		}},
		Ratios:  map[[2]string]float64{{"10m", "SA"}: 5.0},
		Indices: map[[2]string]int{{"10m", "SA"}: 70},
	}

	// First tick: suppress.
	if s := DetectSurges(nil, cur, nil, now); len(s) != 0 {
		t.Errorf("first tick surges = %+v, want none", s)
	}

	// Activity jump from a quiet previous ratio.
	prev := &ReachVerdict{Ratios: map[[2]string]float64{{"10m", "SA"}: 1.0}, Indices: map[[2]string]int{}}
	s := DetectSurges(prev, cur, nil, now)
	if len(s) != 1 || s[0].Kind != "activity_jump" {
		t.Fatalf("jump surges = %+v", s)
	}
	if s[0].Strength < 60 || s[0].Strength > 100 {
		t.Errorf("jump strength = %d, want 60..100", s[0].Strength)
	}

	// No jump when previous ratio was already high.
	prev = &ReachVerdict{Ratios: map[[2]string]float64{{"10m", "SA"}: 3.0}, Indices: map[[2]string]int{{"10m", "SA"}: 60}}
	if s := DetectSurges(prev, cur, nil, now); len(s) != 0 {
		t.Errorf("already-high surges = %+v, want none", s)
	}

	// New region gated on slot presence. Keep the ratio BELOW the jump
	// threshold so only the new-region rule can fire.
	cur2 := ReachVerdict{
		GeneratedAt: now,
		Cells: []ReachCell{{
			Band: "10m", Region: "SA", Index: &one, ActivityRatio: 3.0,
		}},
		Ratios:  map[[2]string]float64{{"10m", "SA"}: 3.0},
		Indices: map[[2]string]int{{"10m", "SA"}: 70},
	}
	highPresence := []BaselineDayRow{}
	slot := UTCSlotOfDay(now)
	for d := 0; d < 20; d++ {
		highPresence = append(highPresence, BaselineDayRow{Band: "10m", Region: "SA", Slot: slot, DayIndex: int64(20000 + d), LinkCount: 5})
	}
	prev = &ReachVerdict{Ratios: map[[2]string]float64{{"10m", "SA"}: 1.5}, Indices: map[[2]string]int{{"10m", "SA"}: 10}}
	if s := DetectSurges(prev, cur2, highPresence, now); len(s) != 0 {
		t.Errorf("common-slot surges = %+v, want none", s)
	}

	// Rare at this slot -> new_region.
	lowPresence := []BaselineDayRow{}
	for d := 0; d < 20; d++ {
		links := int64(0)
		if d < 2 {
			links = 5
		}
		lowPresence = append(lowPresence, BaselineDayRow{Band: "10m", Region: "SA", Slot: slot, DayIndex: int64(20000 + d), LinkCount: links})
	}
	s = DetectSurges(prev, cur2, lowPresence, now)
	if len(s) != 1 || s[0].Kind != "new_region" || s[0].Strength != 70 {
		t.Fatalf("new_region surges = %+v", s)
	}
}

func TestReachMUFHeadroom(t *testing.T) {
	lv := LadderVerdict{EmpiricalMUF: 18.1, OpenRuns: [][]string{{"20m", "17m"}}, Bands: []LadderBandVerdict{
		{Band: "40m", State: "open"}, {Band: "30m", State: "open"}, {Band: "20m", State: "open"}, {Band: "17m", State: "open"},
		{Band: "15m", State: "closed"},
	}}
	m := reachMUFHeadroom(lv)
	if m.NextRungBand != "15m" || m.NextRungOpen || m.NextRungMHz != 21.0 {
		t.Errorf("headroom = %+v, want next 15m closed 21.0", m)
	}

	m = reachMUFHeadroom(LadderVerdict{})
	if m.NextRungBand != "40m" || m.NextRungMHz != 7.0 {
		t.Errorf("empty headroom = %+v, want next 40m", m)
	}

	bands := []LadderBandVerdict{}
	for _, b := range LadderFLane {
		bands = append(bands, LadderBandVerdict{Band: b, State: "open"})
	}
	m = reachMUFHeadroom(LadderVerdict{Bands: bands, EmpiricalMUF: 28.0})
	if m.NextRungBand != "6m" || m.NextRungMHz != 50.0 {
		t.Errorf("full-ladder headroom = %+v, want next 6m", m)
	}
}

func TestUsableRunsWrap(t *testing.T) {
	u := make([]bool, 48)
	u[46], u[47], u[0], u[1] = true, true, true, true
	runs := usableRuns(u)
	if len(runs) != 1 || runs[0] != [2]int{46, 1} {
		t.Errorf("wrap runs = %v, want [{46 1}]", runs)
	}
	// All false.
	if runs := usableRuns(make([]bool, 48)); len(runs) != 0 {
		t.Errorf("all-false runs = %v, want none", runs)
	}
	// Two separate runs.
	u2 := make([]bool, 48)
	u2[5], u2[6], u2[10] = true, true, true
	runs = usableRuns(u2)
	if len(runs) != 2 {
		t.Errorf("two runs = %v, want 2", runs)
	}
}
