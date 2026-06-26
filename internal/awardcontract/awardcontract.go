// Package awardcontract holds the shared award types used by the awards engine
// (internal/awards) and the operator agent: the WantedSpot input, the
// WantedResult/WantedSlot output, and the needed[] vocabulary. Award progress
// runs in-process inside the agent, so these are plain in-process types (no HTTP
// boundary); keeping them in one importable place mirrors internal/propcontract.
package awardcontract

// Needed[] vocabulary. The frontend (static/dxcluster.js wantInfo) maps these to
// wanted badges. dxcc/band/mode predate horstawards (Wavelog confirmed-flag
// logic); was/pota are added by the award engine. Unknown values are ignored by
// the frontend, so this set can grow additively.
const (
	NeededDXCC = "dxcc" // all-time-new entity (ATNO)
	NeededBand = "band" // worked entity, new on this band
	NeededMode = "mode" // worked on band, new mode
	NeededWAS  = "was"  // new US state for Worked All States
	NeededPOTA = "pota" // new POTA park (or new state for POTA)
)

// Slot status strings used in WantedSlot.Status. "worked" means logged but not
// confirmed; "confirmed" means a QSL/LoTW/eQSL credit exists.
const (
	StatusUnworked  = "unworked"
	StatusWorked    = "worked"
	StatusConfirmed = "confirmed"
)

// WantedSpot is one resolved spot to evaluate. The agent fills the attribute
// fields from its Wavelog private_lookup result; horstawards intersects them
// against the operator's award-progress index. Band is canonical ("20m"); Mode
// is the on-air mode ("SSB","FT8",…).
type WantedSpot struct {
	ID      string `json:"id"`
	Call    string `json:"call"`
	DXCCID  string `json:"dxcc_id,omitempty"`
	State   string `json:"state,omitempty"`
	CQZ     string `json:"cqz,omitempty"`
	Grid    string `json:"grid,omitempty"`
	IOTA    string `json:"iota,omitempty"`
	POTARef string `json:"pota_ref,omitempty"`
	Band    string `json:"band"`
	Mode    string `json:"mode"`
}

// WantedSlot is per-spot detail: one award slot the spot could fill and its
// current status. Additive — the frontend keys off Needed; Slots is for richer
// UI/debugging later.
type WantedSlot struct {
	Program string `json:"program"`        // dxcc|was|pota
	Entity  string `json:"entity"`         // dxcc id | state code | park ref | "STATE:CA"
	Band    string `json:"band,omitempty"` // "" = mixed/overall rollup
	Mode    string `json:"mode,omitempty"` // mode class; "" = mixed
	Status  string `json:"status"`         // unworked|worked|confirmed
}

// WantedResult is the per-spot output. Needed is the collapsed vocabulary the
// frontend consumes; Slots is optional detail.
type WantedResult struct {
	ID     string       `json:"id"`
	Needed []string     `json:"needed"`
	Slots  []WantedSlot `json:"slots,omitempty"`
}
