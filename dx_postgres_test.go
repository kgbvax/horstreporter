package main

import (
	"testing"
	"time"
)

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
			if key.TargetGrid4 == "FN31" && key.Region == string(dxPulseRegionEU) {
				foundFn31EU = true
			}
			if key.TargetGrid4 == "JO32" && key.Region == string(dxPulseRegionNA) {
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
		if keys[0].TargetGrid4 != "JO32" || keys[0].Region != string(dxPulseRegionEU) {
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
