package award

import (
	"reflect"
	"sort"
	"testing"

	"horstreporter/internal/awardcontract"
)

// newLoadedIndex builds an index with the given programs marked loaded and slots
// set, so Evaluate gates work correctly in tests.
func newLoadedIndex(programs []Program, slots map[Slot]Status) *Index {
	ix := NewIndex()
	for _, p := range programs {
		ix.MarkProgram(p)
	}
	for s, st := range slots {
		ix.Set(s, st)
	}
	return ix
}

func TestEvaluateDXCC(t *testing.T) {
	dxccLoaded := []Program{ProgDXCC}
	tests := []struct {
		name  string
		slots map[Slot]Status
		spot  awardcontract.WantedSpot
		want  []string
	}{
		{
			name:  "ATNO: entity never worked",
			slots: nil,
			spot:  awardcontract.WantedSpot{DXCCID: "24", Band: "20m", Mode: "SSB"},
			want:  []string{"dxcc"},
		},
		{
			name:  "worked-not-confirmed still ATNO",
			slots: map[Slot]Status{{Program: ProgDXCC, Entity: "24"}: StatusWorked},
			spot:  awardcontract.WantedSpot{DXCCID: "24", Band: "20m", Mode: "SSB"},
			want:  []string{"dxcc"},
		},
		{
			name: "new band: entity confirmed overall, not on this band",
			slots: map[Slot]Status{
				{Program: ProgDXCC, Entity: "24"}: StatusConfirmed,
			},
			spot: awardcontract.WantedSpot{DXCCID: "24", Band: "20m", Mode: "SSB"},
			want: []string{"band"},
		},
		{
			name: "new mode: entity+band confirmed, not this mode",
			slots: map[Slot]Status{
				{Program: ProgDXCC, Entity: "24"}:              StatusConfirmed,
				{Program: ProgDXCC, Entity: "24", Band: "20m"}: StatusConfirmed,
			},
			spot: awardcontract.WantedSpot{DXCCID: "24", Band: "20m", Mode: "SSB"},
			want: []string{"mode"},
		},
		{
			name: "fully satisfied",
			slots: map[Slot]Status{
				{Program: ProgDXCC, Entity: "24"}:                             StatusConfirmed,
				{Program: ProgDXCC, Entity: "24", Band: "20m"}:                StatusConfirmed,
				{Program: ProgDXCC, Entity: "24", Band: "20m", ModeCl: "ssb"}: StatusConfirmed,
			},
			spot: awardcontract.WantedSpot{DXCCID: "24", Band: "20m", Mode: "SSB"},
			want: []string{},
		},
		{
			name:  "no dxcc id: nothing",
			slots: nil,
			spot:  awardcontract.WantedSpot{Band: "20m", Mode: "SSB"},
			want:  []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ix := newLoadedIndex(dxccLoaded, tt.slots)
			got := ix.Evaluate(tt.spot).Needed
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("needed = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEvaluateProgramNotLoaded(t *testing.T) {
	// DXCC program not marked loaded → no dxcc want even though the entity is absent.
	ix := newLoadedIndex(nil, nil)
	if got := ix.Evaluate(awardcontract.WantedSpot{DXCCID: "24", Band: "20m"}).Needed; len(got) != 0 {
		t.Errorf("with no programs loaded, needed = %v, want empty", got)
	}
}

func TestEvaluateWAS(t *testing.T) {
	loaded := []Program{ProgDXCC, ProgWAS}
	t.Run("US state needed", func(t *testing.T) {
		ix := newLoadedIndex(loaded, map[Slot]Status{
			{Program: ProgDXCC, Entity: "291"}: StatusConfirmed, // DXCC already done so it doesn't mask WAS
		})
		got := ix.Evaluate(awardcontract.WantedSpot{DXCCID: "291", State: "CT", Band: "20m", Mode: "CW"}).Needed
		if !containsStr(got, "was") {
			t.Errorf("needed = %v, want to contain was", got)
		}
	})
	t.Run("non-US entity: no WAS", func(t *testing.T) {
		ix := newLoadedIndex(loaded, nil)
		got := ix.Evaluate(awardcontract.WantedSpot{DXCCID: "24", State: "CT", Band: "20m"}).Needed
		if containsStr(got, "was") {
			t.Errorf("needed = %v, want no was for non-US entity", got)
		}
	})
	t.Run("state confirmed: no WAS", func(t *testing.T) {
		ix := newLoadedIndex(loaded, map[Slot]Status{
			{Program: ProgWAS, Entity: "CT"}: StatusConfirmed,
		})
		got := ix.Evaluate(awardcontract.WantedSpot{DXCCID: "291", State: "CT", Band: "20m"}).Needed
		if containsStr(got, "was") {
			t.Errorf("needed = %v, want no was when state confirmed", got)
		}
	})
}

func TestEvaluatePOTA(t *testing.T) {
	t.Run("new park when POTA loaded", func(t *testing.T) {
		ix := newLoadedIndex([]Program{ProgPOTA}, nil)
		got := ix.Evaluate(awardcontract.WantedSpot{POTARef: "K-1234"}).Needed
		if !reflect.DeepEqual(got, []string{"pota"}) {
			t.Errorf("needed = %v, want [pota]", got)
		}
	})
	t.Run("hunted park: nothing", func(t *testing.T) {
		ix := newLoadedIndex([]Program{ProgPOTA}, map[Slot]Status{
			{Program: ProgPOTA, Entity: "K-1234"}: StatusWorked,
		})
		got := ix.Evaluate(awardcontract.WantedSpot{POTARef: "K-1234"}).Needed
		if len(got) != 0 {
			t.Errorf("needed = %v, want empty for hunted park", got)
		}
	})
	t.Run("no park ref: no POTA want (state alone never fires)", func(t *testing.T) {
		ix := newLoadedIndex([]Program{ProgPOTA}, nil)
		got := ix.Evaluate(awardcontract.WantedSpot{State: "CA"}).Needed
		if len(got) != 0 {
			t.Errorf("needed = %v, want empty without a park ref", got)
		}
	})
	t.Run("park ref but POTA not loaded: no false want", func(t *testing.T) {
		ix := newLoadedIndex([]Program{ProgDXCC}, nil)
		got := ix.Evaluate(awardcontract.WantedSpot{POTARef: "K-1234"}).Needed
		if len(got) != 0 {
			t.Errorf("needed = %v, want empty when POTA not loaded", got)
		}
	})
}

func TestEvaluateCombinedOrder(t *testing.T) {
	ix := newLoadedIndex([]Program{ProgDXCC, ProgWAS}, nil)
	// US entity, never worked → dxcc ATNO; also a needed state → was. Order dxcc>was.
	got := ix.Evaluate(awardcontract.WantedSpot{DXCCID: "291", State: "CT", Band: "20m", Mode: "SSB"}).Needed
	if !reflect.DeepEqual(got, []string{"dxcc", "was"}) {
		t.Errorf("needed = %v, want [dxcc was]", got)
	}
}

func TestSlotKeyStable(t *testing.T) {
	a := Slot{Program: ProgDXCC, Entity: "24", Band: "20m", ModeCl: "ssb"}
	if a.Key() != "dxcc|24|20m|ssb" {
		t.Errorf("Key = %q", a.Key())
	}
}

func TestIndexSetKeepsBestStatus(t *testing.T) {
	ix := NewIndex()
	s := Slot{Program: ProgDXCC, Entity: "1"}
	ix.Set(s, StatusConfirmed)
	ix.Set(s, StatusWorked) // must not downgrade
	if ix.Status(s) != StatusConfirmed {
		t.Errorf("status downgraded to %v", ix.Status(s))
	}
}

func TestSlotDetailIncludesStatus(t *testing.T) {
	ix := newLoadedIndex([]Program{ProgDXCC}, map[Slot]Status{
		{Program: ProgDXCC, Entity: "24"}: StatusWorked,
	})
	res := ix.Evaluate(awardcontract.WantedSpot{DXCCID: "24", Band: "20m", Mode: "SSB"})
	if len(res.Slots) == 0 || res.Slots[0].Status != awardcontract.StatusWorked {
		t.Errorf("slot detail = %#v, want status worked", res.Slots)
	}
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestProgramsSorted(t *testing.T) {
	ix := NewIndex()
	ix.MarkProgram(ProgWAS)
	ix.MarkProgram(ProgDXCC)
	got := ix.Programs()
	want := append([]string(nil), got...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Programs not sorted: %v", got)
	}
}
