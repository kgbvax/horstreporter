package award

import (
	"horstreporter/cmd/horstawards/internal/refdata"
	"horstreporter/internal/awardcontract"
)

// wasNeeded reports whether a spot is needed for Worked All States. WAS counts a
// QSO only when the station is in a US entity (US/AK/HI) AND carries a valid
// state. v1 chases WAS mixed (overall) first — the headline use case — and
// satisfies on confirmation. The per-band slot detail is included for future UI
// but does not change the needed flag.
//
// Returns (needed, slot, ok). ok is false when WAS does not apply to this spot.
func (ix *Index) wasNeeded(dxccID, state, band string) (string, awardcontract.WantedSlot, bool) {
	if !refdata.IsUSEntity(dxccID) {
		return "", awardcontract.WantedSlot{}, false
	}
	st, ok := refdata.NormState(state)
	if !ok {
		return "", awardcontract.WantedSlot{}, false
	}
	overall := Slot{Program: ProgWAS, Entity: st}
	if status := ix.Status(overall); status < StatusConfirmed {
		return awardcontract.NeededWAS, toContractSlot(overall, status), true
	}
	return "", awardcontract.WantedSlot{}, false
}
