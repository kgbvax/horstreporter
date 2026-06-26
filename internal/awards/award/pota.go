package award

import "horstreporter/internal/awardcontract"

// potaNeeded reports whether a POTA spot is wanted: a park you have not hunted
// yet. POTA hunting tracks "have I logged this park", so the satisfied bar is
// WORKED (a confirmation is not required to count a park as hunted). It requires
// the spot to carry a park ref — without one we cannot tell that a spot is a POTA
// activation, so we report nothing rather than guess from the station's home
// state. (Plumbing the park ref from the spot comment is a frontend/agent
// follow-up; until then POTA simply does not fire.)
func (ix *Index) potaNeeded(potaRef string) (string, []awardcontract.WantedSlot) {
	ref := normID(potaRef)
	if ref == "" {
		return "", nil
	}
	park := Slot{Program: ProgPOTA, Entity: ref}
	if st := ix.Status(park); st < StatusWorked {
		return awardcontract.NeededPOTA, []awardcontract.WantedSlot{toContractSlot(park, st)}
	}
	return "", nil // park already hunted
}
