package main

import (
	"strings"
	"testing"
	"time"

	"horstreporter/internal/region"
)

func TestWsprReceiverMatch(t *testing.T) {
	t.Run("callsign uses parenthesised concatenations", func(t *testing.T) {
		where, args := wsprReceiverMatch("DL1ABC", 1000)
		if !strings.Contains(where, "LIKE ($2 || '/%')") {
			t.Errorf("expected parenthesised prefix pattern, got: %s", where)
		}
		if !strings.Contains(where, "LIKE ('%/' || $2)") {
			t.Errorf("expected parenthesised suffix pattern, got: %s", where)
		}
		if !strings.Contains(where, "LIKE ('%/' || $2 || '/%')") {
			t.Errorf("expected parenthesised infix pattern, got: %s", where)
		}
		if len(args) != 2 {
			t.Fatalf("expected 2 args, got %d", len(args))
		}
		if args[0] != int64(1000) {
			t.Errorf("arg[0] = %v, want 1000", args[0])
		}
		if args[1] != "DL1ABC" {
			t.Errorf("arg[1] = %v, want DL1ABC", args[1])
		}
	})

	t.Run("locator uses 4-char substring", func(t *testing.T) {
		where, args := wsprReceiverMatch("JO31OM", 2000)
		if !strings.Contains(where, "substring(receiver_locator from 1 for 4) = $2") {
			t.Errorf("expected substring locator match, got: %s", where)
		}
		if len(args) != 2 {
			t.Fatalf("expected 2 args, got %d", len(args))
		}
		if args[1] != "JO31" {
			t.Errorf("arg[1] = %v, want JO31", args[1])
		}
	})
}

func TestDxPulseRegionBaselineKeysForSpot(t *testing.T) {
	ts := time.Date(2026, time.May, 29, 12, 10, 0, 0, time.UTC).Unix()

	t.Run("builds keys for both propagation directions", func(t *testing.T) {
		keys := dxPulseRegionBaselineKeysForSpot(ts, "20m", "FN31", "JO32")
		if len(keys) != 2 {
			t.Fatalf("expected 2 keys, got %d: %+v", len(keys), keys)
		}

		foundFn31EU := false
		foundJo32NA := false
		for _, key := range keys {
			if key.Band != "20m" {
				t.Fatalf("expected normalized band 20m, got %q", key.Band)
			}
			if key.SlotOfDay != utcSlotOfDay(ts) {
				t.Fatalf("expected slot %d, got %d", utcSlotOfDay(ts), key.SlotOfDay)
			}
			if key.DayIndex != utcDayIndex(ts) {
				t.Fatalf("expected day index %d, got %d", utcDayIndex(ts), key.DayIndex)
			}
			if key.TargetGrid4 == "FN31" && key.Region == string(region.EU) {
				foundFn31EU = true
			}
			if key.TargetGrid4 == "JO32" && key.Region == string(region.NA) {
				foundJo32NA = true
			}
		}
		if !foundFn31EU || !foundJo32NA {
			t.Fatalf("expected FN31/EU and JO32/NA keys, got %+v", keys)
		}
	})

	t.Run("deduplicates mirrored self-region keys", func(t *testing.T) {
		keys := dxPulseRegionBaselineKeysForSpot(ts, "20m", "JO32", "JO32")
		if len(keys) != 1 {
			t.Fatalf("expected 1 deduplicated key, got %d: %+v", len(keys), keys)
		}
		if keys[0].TargetGrid4 != "JO32" || keys[0].Region != string(region.EU) {
			t.Fatalf("unexpected deduplicated key: %+v", keys[0])
		}
	})

	t.Run("rejects invalid input", func(t *testing.T) {
		if keys := dxPulseRegionBaselineKeysForSpot(ts, "", "FN31", "JO32"); len(keys) != 0 {
			t.Fatalf("expected no keys for empty band, got %+v", keys)
		}
		if keys := dxPulseRegionBaselineKeysForSpot(ts, "20m", "", ""); len(keys) != 0 {
			t.Fatalf("expected no keys for missing locators, got %+v", keys)
		}
	})
}
