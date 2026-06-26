// Package award is horstawards's progress engine: the slot data model, the fast
// in-memory Index the /v1/wanted query hits, and the per-program logic that turns
// a resolved spot into the needed[] vocabulary.
//
// The wanted decision is set intersection: the candidate slots a spot could fill,
// minus the slots the operator has already satisfied (the Index). The hot path is
// O(slots-per-spot) map lookups — no disk, no upstream calls.
package award

import (
	"sort"
	"strings"

	"horstreporter/internal/awardcontract"
	"horstreporter/internal/awards/refdata"
)

// Program identifies an award program. Reserved (not yet computed): "waz",
// "vucc", "iota".
type Program string

const (
	ProgDXCC Program = "dxcc"
	ProgWAS  Program = "was"
	ProgPOTA Program = "pota" // park-hunt coverage (derived from the operator's log)
)

// Status is how far a slot has progressed. Ordered so max() picks the better of
// two observations (confirmed beats worked beats unworked).
type Status uint8

const (
	StatusUnworked Status = iota
	StatusWorked
	StatusConfirmed
)

// String renders the contract status string.
func (st Status) String() string {
	switch st {
	case StatusConfirmed:
		return awardcontract.StatusConfirmed
	case StatusWorked:
		return awardcontract.StatusWorked
	default:
		return awardcontract.StatusUnworked
	}
}

// Slot is a single award target: one "thing you can work/confirm". Empty Band
// and/or ModeCl denote the mixed/overall rollup (e.g. DXCC entity overall vs
// per-band vs per-band-mode).
type Slot struct {
	Program Program `json:"program"`
	Entity  string  `json:"entity"`            // dxcc id | state code | park ref | "STATE:CA"
	Band    string  `json:"band,omitempty"`    // canonical "20m" or ""
	ModeCl  string  `json:"mode_cl,omitempty"` // "ssb"|"cw"|"data" or ""
}

// Key is the canonical comparable map key for a slot.
func (s Slot) Key() string {
	return string(s.Program) + "|" + s.Entity + "|" + s.Band + "|" + s.ModeCl
}

// SlotStatus pairs a slot with a status. Sources emit these; the Index folds them.
type SlotStatus struct {
	Slot   Slot   `json:"slot"`
	Status Status `json:"status"`
}

// Index is the fast lookup the query hits. It also records which programs have
// authoritative progress loaded: a program with no data must NOT report "needed"
// (otherwise every spot would look new). Treat the zero value as empty.
type Index struct {
	slots    map[string]Status
	programs map[Program]bool
}

// NewIndex returns an empty index.
func NewIndex() *Index {
	return &Index{slots: map[string]Status{}, programs: map[Program]bool{}}
}

// MarkProgram records that a program's progress data has been loaded (even if it
// contributed zero slots — an operator with no WAS QSOs still has "WAS loaded").
func (ix *Index) MarkProgram(p Program) { ix.programs[p] = true }

// HasProgram reports whether a program's progress is loaded/queryable.
func (ix *Index) HasProgram(p Program) bool { return ix.programs[p] }

// Set records a slot at the given status, keeping the best status seen.
func (ix *Index) Set(s Slot, st Status) {
	if cur, ok := ix.slots[s.Key()]; !ok || st > cur {
		ix.slots[s.Key()] = st
	}
}

// Status returns the best-known status of a slot (StatusUnworked if absent).
func (ix *Index) Status(s Slot) Status { return ix.slots[s.Key()] }

// Len returns the number of distinct slots tracked.
func (ix *Index) Len() int { return len(ix.slots) }

// Programs returns the sorted list of loaded programs (for health output).
func (ix *Index) Programs() []string {
	out := make([]string, 0, len(ix.programs))
	for p := range ix.programs {
		out = append(out, string(p))
	}
	sort.Strings(out)
	return out
}

// Evaluate intersects a resolved spot against the index and returns its wanted
// result (collapsed needed[] vocabulary + per-slot detail). Only programs with
// loaded progress contribute, so a missing data source yields no false wants.
func (ix *Index) Evaluate(sp awardcontract.WantedSpot) awardcontract.WantedResult {
	band := refdata.NormBand(sp.Band)
	modeCl := refdata.ModeClass(sp.Mode)

	res := awardcontract.WantedResult{ID: sp.ID}
	// needed accumulates in priority order (dxcc>band>mode>was>pota). The three
	// programs emit disjoint single tokens, so no dedup is required. Kept non-nil
	// so it serializes as [] not null.
	needed := []string{}

	if ix.HasProgram(ProgDXCC) {
		if n, slots := ix.dxccNeeded(sp.DXCCID, band, modeCl); n != "" {
			needed = append(needed, n)
			res.Slots = append(res.Slots, slots...)
		}
	}
	if ix.HasProgram(ProgWAS) {
		if n, slot, ok := ix.wasNeeded(sp.DXCCID, sp.State, band); ok {
			needed = append(needed, n)
			res.Slots = append(res.Slots, slot)
		}
	}
	if ix.HasProgram(ProgPOTA) {
		if n, slots := ix.potaNeeded(sp.POTARef); n != "" {
			needed = append(needed, n)
			res.Slots = append(res.Slots, slots...)
		}
	}

	res.Needed = needed
	return res
}

// validDXCC reports whether a DXCC id string is a real entity. ADIF/Wavelog use
// "0" (or empty) for no/deleted/unresolvable entity — those must not be tracked
// as a slot or flagged as a want.
func validDXCC(id string) bool {
	id = strings.TrimSpace(id)
	return id != "" && id != "0"
}

func toContractSlot(s Slot, st Status) awardcontract.WantedSlot {
	return awardcontract.WantedSlot{
		Program: string(s.Program),
		Entity:  s.Entity,
		Band:    s.Band,
		Mode:    s.ModeCl,
		Status:  st.String(),
	}
}

func normID(s string) string { return strings.TrimSpace(s) }
