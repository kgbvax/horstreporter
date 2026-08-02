package proplab

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// proplab_reach.go composes the operator-facing reachability verdict from the
// destination buckets (near-end field × DX-destination region), the slot-of-day
// quantile baseline, persistence counts, the space-weather/event context and
// the Ladder verdict (MUF headroom + onsets).
//
// The reachability index is a HEURISTIC 0-100 strength score — explicitly not
// a calibrated probability: only successful links are observable, never failed
// attempts. The formula is anchored to the engines' existing thresholds so it
// stays interpretable against them:
//
//	activity ratio r = live links/min ÷ slot-of-day baseline p50 links/min
//	r ≤ 0.5  -> activity 0        (fusion ClosedRatio)
//	r = 1.0  -> activity 15
//	r = 2.0  -> activity 50       (fusion OpenRatio / backtest "open" anchor)
//	r = 8.0  -> activity ≈ 83
//	r ≥ 16   -> activity 100
//
// Without a baseline the live rate itself goes through the same curve
// (lpm 2 -> 50, matching the ladder "open" intuition), flagged cell_data_thin
// and damped ×0.7. The raw activity score is multiplied by witness,
// persistence and thin-data factors, then hard-capped by typed closure causes
// (D-RAP absorption ≤ 15, auroral ≤ 25, MUF-limited ≤ 30, event-explained ≤
// 20): the index cannot argue with physics, it can only cap. Witnesses 1/2/≥3
// scale ×0.5/×0.75/×1.0; a cell with zero witnesses has no index at all (null)
// — "no data" is a distinct semantic from "observed dead".
const (
	ReachIndexScale = "reachability index, heuristic 0-100; not a calibrated probability"

	reachClosedRatio = 0.5 // fusion ClosedRatio: activity ends here
	reachOpenRatio   = 2.0 // fusion OpenRatio / backtest lpm>=2 anchor: index 50
	reachCurveSpan   = 3.0 // log2 span from the open anchor to index 100

	reachThinFactor  = 0.7 // damping when the cell has no baseline history
	reachEventCap    = 20
	reachAbsorbCap   = 15
	reachAuroralCap  = 25
	reachMufCap      = 30
	reachWitnessFull = 3 // ladders WitnessMin: full witness weight

	// Scheduled-opening gates (per 30-min UTC slot over the lookback window).
	ReachSchedulePresenceMin = 0.35 // fraction of days with activity in the slot
	ReachScheduleMedianMin   = 1.0  // median links/day in the slot

	// Surge heuristics — first-guess thresholds, tuned via proplab-backtest.
	SurgeTTLMinutes           = 240
	surgeJumpRatio            = 4.0
	surgeNewRegionIndexMin    = 60
	surgeNewRegionPrevMax     = 20
	surgeNewRegionPresenceMax = 0.25
)

// ReachCell is one (band, destination region) evaluation.
type ReachCell struct {
	Band          string   `json:"band"`
	Region        string   `json:"region"`
	Index         *int     `json:"index"` // null when witnesses == 0
	LinksPerMin   float64  `json:"links_per_min"`
	BaselineP50   float64  `json:"baseline_p50"` // links per minute per slot-of-day baseline
	ActivityRatio float64  `json:"activity_ratio"`
	Witnesses     int      `json:"witnesses"`
	Persistence   float64  `json:"persistence"` // active/total closed buckets, 120 min
	ClosureCause  string   `json:"closure_cause"`
	Capped        bool     `json:"capped"`
	CellDataThin  bool     `json:"cell_data_thin"`
	SpotsPerMin   float64  `json:"spots_per_min"`
	ExplainedBy   []string `json:"explained_by"`
}

// ReachMUF is the empirical MUF headroom readout (path-midpoint physics from
// the Ladder engine; destinations use their own semantics).
type ReachMUF struct {
	EmpiricalMHz float64   `json:"empirical_mhz"`
	OpenRuns     [][]string `json:"open_runs"`
	NextRungBand string    `json:"next_rung_band"`
	NextRungMHz  float64   `json:"next_rung_mhz"`
	NextRungOpen bool      `json:"next_rung_open"`
}

// ReachSurge is a self-contained "something new is happening" marker, shaped so
// the main app can later consume it for "tune to 10m, surge" hints.
type ReachSurge struct {
	Kind      string `json:"kind"` // onset | activity_jump | new_region
	Band      string `json:"band"`
	Region    string `json:"region"`
	FirstSeen int64  `json:"first_seen"`
	Strength  int    `json:"strength"`
	Detail    string `json:"detail"`
}

// ReachScheduleEntry is one usual opening window for a pair that is NOT
// currently reachable ("15m JA usually opens 05:40 UTC, in 40 min").
type ReachScheduleEntry struct {
	Band          string  `json:"band"`
	Region        string  `json:"region"`
	OpenUTC       string  `json:"open_utc"`
	CloseUTC      string  `json:"close_utc"`
	MinutesToOpen int     `json:"minutes_to_open"`
	Presence      float64 `json:"presence"` // fraction of days the opening occurs
}

// ReachVerdict is the composed product payload.
type ReachVerdict struct {
	GeneratedAt         int64               `json:"generated_at"`
	QTH                 string              `json:"qth"`
	Surroundings        bool                `json:"surroundings"`
	IndexScale          string              `json:"index_scale"`
	DataThin            bool                `json:"data_thin"`
	ScheduleUnavailable bool                `json:"schedule_unavailable"`
	SWAvailable         bool                `json:"sw_available"`
	HasDrap             bool                `json:"has_drap"`
	DrapAgeMin          int                 `json:"drap_age_min"`
	EventsActive        int                 `json:"events_active"`
	MUF                 ReachMUF            `json:"muf"`
	Cells               []ReachCell         `json:"cells"`
	Surges              []ReachSurge        `json:"surges"`
	Schedule            []ReachScheduleEntry `json:"schedule"`

	// Ratios/Indices feed DetectSurges' tick-over-tick diff; not serialized.
	Ratios  map[[2]string]float64 `json:"-"`
	Indices map[[2]string]int     `json:"-"`
}

// ReachInputs are everything the composer needs; the service assembles them per
// tick (baseline/persistence from Postgres, live from buckets + in-memory,
// SW/events from context, ladder verdict fresh per request scope).
type ReachInputs struct {
	Now          int64
	QTH          string
	Surroundings bool
	Live         []DestRow
	LiveMinutes  int
	Baseline     []BaselineDayRow // current 30-min slot
	SlotProfile  []BaselineDayRow // all slots; nil = load failed
	Persistence  map[[2]string][2]int
	SW           FusionSWSnapshot
	Events       []FusionEvent
	Ladder       LadderVerdict
}

// reachActivityScore maps an activity ratio to the 0-100 base score.
func reachActivityScore(r float64) float64 {
	if r <= reachClosedRatio {
		return 0
	}
	if r < reachOpenRatio {
		return 45 * (r - reachClosedRatio) / (reachOpenRatio - reachClosedRatio)
	}
	return math.Min(100, 50+50*math.Log2(r/reachOpenRatio)/reachCurveSpan)
}

func reachWitnessFactor(w int) (float64, bool) {
	switch {
	case w <= 0:
		return 0, false
	case w == 1:
		return 0.5, true
	case w == 2:
		return 0.75, true
	default:
		return 1.0, true
	}
}

// reachCapForCause returns the hard index cap for a typed closure cause.
func reachCapForCause(cause string) int {
	switch cause {
	case "absorption_limited":
		return reachAbsorbCap
	case "auroral":
		return reachAuroralCap
	case "muf_limited":
		return reachMufCap
	default:
		return 100
	}
}

// reachClosureCause mirrors the fusion closure classifier for a destination
// pair (D-RAP HAF is per region; the auroral gate uses the region centroid).
// No model prior is wired for reach (priorOK=false). Takes the engine as an
// argument so ComposeReach allocates it once per verdict, not once per cell.
func reachClosureCause(e *FusionEngine, band, region string, sw FusionSWSnapshot) string {
	return e.closureCause(FusionBandCount{Band: band, Region: region}, sw, 0, false, DefaultFusionParams())
}

// ComposeReach evaluates all live destination pairs into the product verdict.
// Pure: every input is in ReachInputs, so the backtest harness replays exactly
// what the live service composes.
func ComposeReach(in ReachInputs) ReachVerdict {
	liveMin := in.LiveMinutes
	if liveMin <= 0 {
		liveMin = 30
	}

	fe := NewFusionEngine()

	// Per-pair baseline rows.
	baseByKey := make(map[[2]string][]BaselineDayRow)
	for _, r := range in.Baseline {
		baseByKey[[2]string{r.Band, r.Region}] = append(baseByKey[[2]string{r.Band, r.Region}], r)
	}

	v := ReachVerdict{
		GeneratedAt:         in.Now,
		QTH:                 strings.ToUpper(strings.TrimSpace(in.QTH)),
		Surroundings:        in.Surroundings,
		IndexScale:          ReachIndexScale,
		ScheduleUnavailable: in.SlotProfile == nil,
		SWAvailable:         in.SW.Available,
		HasDrap:             in.SW.HasDrap,
		DrapAgeMin:          in.SW.DrapAgeMin,
		EventsActive:        len(in.Events),
		MUF:                 reachMUFHeadroom(in.Ladder),
		Cells:               []ReachCell{},
		Surges:              []ReachSurge{},
		Schedule:            []ReachScheduleEntry{},
		Ratios:              make(map[[2]string]float64),
		Indices:             make(map[[2]string]int),
	}

	liveOpen := make(map[[2]string]bool)
	for _, row := range in.Live {
		key := [2]string{row.Band, row.Region}
		lpm := float64(row.LinkCount) / float64(liveMin)
		spm := float64(row.SpotCount) / float64(liveMin)

		baseRows := baseByKey[key]
		_, p50, _, _ := weightedBaselineStats(baseRows, DefaultFusionParams())
		basePerMin := p50 / 1440.0

		var ratio float64
		thin := false
		if basePerMin > 0 {
			ratio = lpm / basePerMin
		} else {
			// No baseline history: run the live rate itself through the curve
			// (lpm 2 -> 50) and damp; honest, but visibly thin.
			ratio = lpm
			thin = true
		}
		a := reachActivityScore(ratio)
		wf, hasIndex := reachWitnessFactor(row.ReporterCount)

		pers := 0.0
		if pt, ok := in.Persistence[key]; ok && pt[1] > 0 {
			pers = float64(pt[0]) / float64(pt[1])
		}

		cause := reachClosureCause(fe, row.Band, row.Region, in.SW)
		explained := eventExplanations(row.Band, row.Region, in.Events)

		c := ReachCell{
			Band:          row.Band,
			Region:        row.Region,
			LinksPerMin:   round3(lpm),
			BaselineP50:   round3(basePerMin),
			ActivityRatio: round3(ratio),
			Witnesses:     row.ReporterCount,
			Persistence:   round3(pers),
			ClosureCause:  cause,
			CellDataThin:  thin,
			SpotsPerMin:   round3(spm),
			ExplainedBy:   explained,
		}
		if hasIndex {
			factor := wf * (0.8 + 0.2*pers)
			if thin {
				factor *= reachThinFactor
			}
			idx := int(math.Round(a * factor))
			cap := reachCapForCause(cause)
			if len(explained) > 0 && cap > reachEventCap {
				cap = reachEventCap
			}
			if idx > cap {
				idx = cap
				c.Capped = true
			}
			c.Index = &idx
			v.Indices[key] = idx
		}
		v.Ratios[key] = ratio
		v.Cells = append(v.Cells, c)
		if c.Index != nil && *c.Index >= 50 {
			liveOpen[key] = true
		}
	}

	sort.Slice(v.Cells, func(i, j int) bool {
		if v.Cells[i].Band != v.Cells[j].Band {
			return bandRank(v.Cells[i].Band) < bandRank(v.Cells[j].Band)
		}
		return v.Cells[i].Region < v.Cells[j].Region
	})

	v.DataThin = len(v.Cells) == 0

	if !v.ScheduleUnavailable {
		v.Schedule = deriveSchedule(in.SlotProfile, liveOpen, in.Now)
	}

	// Band-level onsets come straight from the Ladder verdict (midpoint
	// semantics; region is the midpoint region of the alarm cell).
	for _, b := range in.Ladder.Bands {
		if b.OnsetMinAgo >= 0 {
			v.Surges = append(v.Surges, ReachSurge{
				Kind:      "onset",
				Band:      b.Band,
				Region:    b.OnsetRegion,
				FirstSeen: in.Now - int64(b.OnsetMinAgo*60),
				Strength:  60,
				Detail:    fmt.Sprintf("CUSUM opening detected ~%d min ago", b.OnsetMinAgo),
			})
		}
	}
	return v
}

// DetectSurges diffs the current verdict against the previous tick's. Returns
// NEW surges only; the service carries them with their original FirstSeen for
// SurgeTTLMinutes. On the very first tick (prev nil) everything is suppressed —
// a boot flush of stale "news" is worse than one quiet minute.
func DetectSurges(prev *ReachVerdict, cur ReachVerdict, slotProfile []BaselineDayRow, now int64) []ReachSurge {
	if prev == nil {
		return nil
	}
	slot := UTCSlotOfDay(now)
	var out []ReachSurge
	for _, c := range cur.Cells {
		key := [2]string{c.Band, c.Region}
		prevRatio, prevOK := prev.Ratios[key]
		prevIdx, idxOK := prev.Indices[key]

		if c.ActivityRatio >= surgeJumpRatio && (!prevOK || prevRatio < reachOpenRatio) {
			strength := int(math.Min(100, math.Round(50+12*math.Log2(c.ActivityRatio))))
			out = append(out, ReachSurge{
				Kind:      "activity_jump",
				Band:      c.Band,
				Region:    c.Region,
				FirstSeen: now,
				Strength:  strength,
				Detail:    fmt.Sprintf("links/min %.1fx baseline (was %.1fx)", c.ActivityRatio, prevRatio),
			})
			continue
		}
		if c.Index != nil && *c.Index >= surgeNewRegionIndexMin &&
			(!idxOK || prevIdx < surgeNewRegionPrevMax) &&
			slotPresence(slotProfile, key, slot) < surgeNewRegionPresenceMax {
			out = append(out, ReachSurge{
				Kind:      "new_region",
				Band:      c.Band,
				Region:    c.Region,
				FirstSeen: now,
				Strength:  *c.Index,
				Detail:    fmt.Sprintf("reachable on %.0f%% of days at this slot", 100*slotPresence(slotProfile, key, slot)),
			})
		}
	}
	return out
}

// slotPresence is the fraction of days with any activity for the pair in slot.
func slotPresence(profile []BaselineDayRow, key [2]string, slot int) float64 {
	days, active := 0, 0
	for _, r := range profile {
		if r.Band == key[0] && r.Region == key[1] && r.Slot == slot {
			days++
			if r.LinkCount > 0 {
				active++
			}
		}
	}
	if days == 0 {
		return 0
	}
	return float64(active) / float64(days)
}

// deriveSchedule finds usual opening windows per (band, region) from the
// full-day slot profile: slots where the pair is active on at least
// ReachSchedulePresenceMin of days with median ≥ ReachScheduleMedianMin links,
// merged into contiguous runs (with 47→0 wrap). Pairs currently reachable are
// skipped — this panel answers "what's next", not "what's on".
func deriveSchedule(profile []BaselineDayRow, liveOpen map[[2]string]bool, now int64) []ReachScheduleEntry {
	params := DefaultFusionParams()
	type pairKey = [2]string
	// usable[pair][slot] and presence[pair][slot]
	rowsByPairSlot := make(map[pairKey]map[int][]BaselineDayRow)
	presence := make(map[pairKey]map[int]float64)
	for _, r := range profile {
		pk := pairKey{r.Band, r.Region}
		m := rowsByPairSlot[pk]
		if m == nil {
			m = make(map[int][]BaselineDayRow)
			rowsByPairSlot[pk] = m
		}
		m[r.Slot] = append(m[r.Slot], r)
	}
	for pk, slots := range rowsByPairSlot {
		pm := make(map[int]float64)
		for sl, rows := range slots {
			days, active := 0, 0
			for _, r := range rows {
				days++
				if r.LinkCount > 0 {
					active++
				}
			}
			if days > 0 {
				pm[sl] = float64(active) / float64(days)
			}
		}
		presence[pk] = pm
	}

	nowSlot := UTCSlotOfDay(now)
	var out []ReachScheduleEntry
	for pk, slots := range rowsByPairSlot {
		if liveOpen[pk] {
			continue
		}
		usable := make([]bool, 48)
		for sl := 0; sl < 48; sl++ {
			if presence[pk][sl] >= ReachSchedulePresenceMin {
				_, p50, _, _ := weightedBaselineStats(slots[sl], params)
				if p50 >= ReachScheduleMedianMin {
					usable[sl] = true
				}
			}
		}
		runs := usableRuns(usable)
		for _, run := range runs {
			if run[1]-run[0] >= 47 { // covers (almost) the whole day: no "opening"
				continue
			}
			start, end := run[0], run[1]
			// Now inside the run (but not exactly at its start) -> already in session.
			minsTo := ((start - nowSlot + 48) % 48) * 30
			if slotInRun(nowSlot, start, end) && minsTo != 0 {
				continue
			}
			// Average presence across the run.
			sum, n := 0.0, 0
			for sl := start; ; sl = (sl + 1) % 48 {
				sum += presence[pk][sl]
				n++
				if sl == end {
					break
				}
			}
			out = append(out, ReachScheduleEntry{
				Band:          pk[0],
				Region:        pk[1],
				OpenUTC:       slotHHMM(start),
				CloseUTC:      slotHHMM((end + 1) % 48),
				MinutesToOpen: minsTo,
				Presence:      round3(sum / float64(n)),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MinutesToOpen != out[j].MinutesToOpen {
			return out[i].MinutesToOpen < out[j].MinutesToOpen
		}
		return out[i].Band < out[j].Band
	})
	return out
}

// usableRuns returns maximal contiguous [start, end] true-runs, treating 47
// and 0 as adjacent.
func usableRuns(u []bool) [][2]int {
	n := len(u)
	all := true
	for _, b := range u {
		if !b {
			all = false
			break
		}
	}
	if all {
		return [][2]int{{0, n - 1}}
	}
	// Linearize starting after a false slot so wrap runs are contiguous.
	start := -1
	for i, b := range u {
		if !b {
			start = i + 1
			break
		}
	}
	if start == -1 {
		return nil
	}
	var runs [][2]int
	runStart := -1
	for k := 0; k < n; k++ {
		i := (start + k) % n
		if u[i] {
			if runStart == -1 {
				runStart = i
			}
		} else if runStart != -1 {
			runs = append(runs, [2]int{runStart, (i - 1 + n) % n})
			runStart = -1
		}
	}
	if runStart != -1 {
		runs = append(runs, [2]int{runStart, (start - 1 + n) % n})
	}
	return runs
}

func slotInRun(nowSlot, start, end int) bool {
	if start <= end {
		return nowSlot >= start && nowSlot <= end
	}
	return nowSlot >= start || nowSlot <= end // wrap
}

func slotHHMM(slot int) string {
	return fmt.Sprintf("%02d:%02d", slot/2, (slot%2)*30)
}

// reachMUFHeadroom reads the ladder verdict into the headroom panel payload:
// current empirical MUF plus the next rung of the F-ladder and whether it is
// already open. Path-midpoint physics; labeled as such in the UI.
func reachMUFHeadroom(lv LadderVerdict) ReachMUF {
	state := make(map[string]string)
	for _, b := range lv.Bands {
		state[b.Band] = b.State
	}
	open := func(band string) bool {
		return state[band] == "open" || state[band] == "rising"
	}
	m := ReachMUF{EmpiricalMHz: lv.EmpiricalMUF, OpenRuns: lv.OpenRuns}
	highest := -1
	for i, band := range LadderFLane {
		if open(band) {
			highest = i
		}
	}
	next := 0
	if highest >= 0 && highest+1 < len(LadderFLane) {
		next = highest + 1
	} else if highest >= len(LadderFLane)-1 && highest >= 0 {
		m.NextRungBand = "6m"
		m.NextRungMHz = BandMHz["6m"]
		m.NextRungOpen = open("6m")
		return m
	}
	m.NextRungBand = LadderFLane[next]
	m.NextRungMHz = BandMHz[LadderFLane[next]]
	m.NextRungOpen = open(m.NextRungBand)
	return m
}

func bandRank(band string) int {
	order := []string{"160m", "80m", "60m", "40m", "30m", "20m", "17m", "15m", "12m", "10m", "6m", "4m", "2m"}
	for i, b := range order {
		if b == band {
			return i
		}
	}
	return len(order)
}

func round3(f float64) float64 {
	return math.Round(f*1000) / 1000
}
