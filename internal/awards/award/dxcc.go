package award

import "horstreporter/internal/awardcontract"

// dxccNeeded mirrors the agent's original confirmation-based collapse
// (cmd/horstoperator-agent/wavelog.go neededFromLookup), but driven by the
// operator's own progress index instead of Wavelog's confirmed flags. A slot is
// satisfied only when CONFIRMED — award credit needs a confirmation — so a
// worked-but-unconfirmed entity still reports needed, with its true status
// (worked) carried in the slot detail.
//
// Collapse rule: a new entity implies new band/mode, so only the most-significant
// gap is reported (dxcc > band > mode). Returns ("",nil) when nothing is needed
// or the spot carries no DXCC id.
func (ix *Index) dxccNeeded(dxccID, band, modeCl string) (string, []awardcontract.WantedSlot) {
	id := normID(dxccID)
	if !validDXCC(id) {
		return "", nil
	}

	overall := Slot{Program: ProgDXCC, Entity: id}
	if st := ix.Status(overall); st < StatusConfirmed {
		return awardcontract.NeededDXCC, []awardcontract.WantedSlot{toContractSlot(overall, st)}
	}

	// Entity confirmed overall — only meaningful to chase a new band/mode when we
	// actually know the band.
	if band == "" {
		return "", nil
	}
	bandSlot := Slot{Program: ProgDXCC, Entity: id, Band: band}
	if st := ix.Status(bandSlot); st < StatusConfirmed {
		return awardcontract.NeededBand, []awardcontract.WantedSlot{toContractSlot(bandSlot, st)}
	}

	if modeCl == "" {
		return "", nil
	}
	bandModeSlot := Slot{Program: ProgDXCC, Entity: id, Band: band, ModeCl: modeCl}
	if st := ix.Status(bandModeSlot); st < StatusConfirmed {
		return awardcontract.NeededMode, []awardcontract.WantedSlot{toContractSlot(bandModeSlot, st)}
	}
	return "", nil
}
