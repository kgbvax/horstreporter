package main

import (
	"testing"
)

func TestWsprClimatologyObserve(t *testing.T) {
	// Covers F1: a WSPR spot with remote locator in EU on 20m at UTC 10:00
	// increments the (20m, EU, slot=20) bucket.
	e := newWsprClimatologyEngine("")
	now := int64(1786224360) // 2026-08-08 21:26:00 UTC → slot 42

	// Receiver in EU (JO31), sender in NA (FN31).
	m := MQTTMessage{
		RP:      5,
		T:       now,
		SC:      "DL1ABC",
		SL:      "JO31",
		RC:      "KF5XYZ",
		RL:      "FN31",
		B:       "20m",
		MD:      "WSPR",
		Source:  "wspr",
		TXPower: 20000,
	}
	e.Observe(m)

	slot := utcSlotOfDay(now)
	key := wsprClimatologyKey("20m", slot, "EU")
	b, ok := e.buckets[key]
	if !ok {
		t.Fatalf("expected bucket for 20m/EU/slot=%d, got none", slot)
	}
	if b.Count != 1 {
		t.Errorf("bucket Count = %d, want 1", b.Count)
	}

	// Verify no NA bucket (we key by receiver region only).
	naKey := wsprClimatologyKey("20m", slot, "NA")
	if _, ok := e.buckets[naKey]; ok {
		t.Error("did not expect a NA bucket (receiver region only)")
	}
}

func TestWsprClimatologyRegionResolution(t *testing.T) {
	// A spot with remote locator FN31 (NA) and another with JO62 (EU) on
	// the same band/slot increment different buckets.
	e := newWsprClimatologyEngine("")
	now := int64(1786224360)
	slot := utcSlotOfDay(now)

	// NA spot: receiver in FN31, sender in JO31.
	e.Observe(MQTTMessage{
		RP: 3, T: now, SC: "DL1ABC", SL: "JO31", RC: "KF5XYZ", RL: "FN31",
		B: "20m", MD: "WSPR", Source: "wspr", TXPower: 10000,
	})
	// EU spot: receiver in JO62, sender in FN31.
	e.Observe(MQTTMessage{
		RP: 5, T: now, SC: "DL1ABC", SL: "FN31", RC: "SM0ABC", RL: "JO62",
		B: "20m", MD: "WSPR", Source: "wspr", TXPower: 5000,
	})

	naBucket := e.buckets[wsprClimatologyKey("20m", slot, "NA")]
	if naBucket == nil || naBucket.Count != 1 {
		t.Errorf("NA bucket count = %v, want 1", naBucket)
	}
	euBucket := e.buckets[wsprClimatologyKey("20m", slot, "EU")]
	if euBucket == nil || euBucket.Count != 1 {
		t.Errorf("EU bucket count = %v, want 1", euBucket)
	}
}

func TestWsprClimatologyUnknownRegion(t *testing.T) {
	// A spot with an invalid remote locator does not increment any bucket.
	e := newWsprClimatologyEngine("")
	now := int64(1786224360)

	e.Observe(MQTTMessage{
		RP: 3, T: now, SC: "DL1ABC", SL: "INVALID", RC: "KF5XYZ", RL: "ALSOBAD",
		B: "20m", MD: "WSPR", Source: "wspr", TXPower: 10000,
	})
	if len(e.buckets) != 0 {
		t.Errorf("expected 0 buckets for invalid locators, got %d", len(e.buckets))
	}
}

func TestWsprClimatologyInMemoryStats(t *testing.T) {
	// The in-memory fallback computes mean from aggregated count / span.
	e := newWsprClimatologyEngine("")

	// Seed 10 spots over 2 hours, all in the same (20m, EU, slot=42) bucket.
	now := int64(1786224360)
	for i := 0; i < 10; i++ {
		e.Observe(MQTTMessage{
			RP: 5, T: now + int64(i*60), SC: "DL1ABC", SL: "JO31",
			RC: "KF5XYZ", RL: "JO62", B: "20m", MD: "WSPR",
			Source: "wspr", TXPower: 20000,
		})
	}

	rows := e.regionCalendarStatsFromMemory(now)
	if len(rows) == 0 {
		t.Fatal("expected at least 1 stats row, got 0")
	}
	var found bool
	for _, r := range rows {
		if r.Band == "20m" && r.Region == "EU" && r.SlotOfDay == utcSlotOfDay(now) {
			if r.Mean <= 0 {
				t.Errorf("mean = %f, want > 0", r.Mean)
			}
			// StdDev is 0 for in-memory fallback (no per-day breakdown).
			if r.StdDev != 0 {
				t.Errorf("in-memory stddev = %f, want 0 (no per-day breakdown)", r.StdDev)
			}
			found = true
		}
	}
	if !found {
		t.Error("did not find expected 20m/EU/slot row in stats")
	}
}

func TestWsprClimatologyJSONLRoundTrip(t *testing.T) {
	// Save() then Load() preserves bucket counts.
	dir := t.TempDir()
	path := dir + "/wspr_climatology.json"
	e := newWsprClimatologyEngine(path)
	now := int64(1786224360)

	e.Observe(MQTTMessage{
		RP: 5, T: now, SC: "DL1ABC", SL: "JO31", RC: "KF5XYZ", RL: "JO62",
		B: "20m", MD: "WSPR", Source: "wspr", TXPower: 20000,
	})
	e.Observe(MQTTMessage{
		RP: 3, T: now, SC: "K1ABC", SL: "FN31", RC: "DL1DEF", RL: "JO62",
		B: "20m", MD: "WSPR", Source: "wspr", TXPower: 10000,
	})

	if err := e.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	e2 := newWsprClimatologyEngine(path)
	if err := e2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	slot := utcSlotOfDay(now)
	euBucket := e2.buckets[wsprClimatologyKey("20m", slot, "EU")]
	if euBucket == nil || euBucket.Count != 1 {
		t.Errorf("EU bucket after round-trip = %v, want Count=1", euBucket)
	}
	naBucket := e2.buckets[wsprClimatologyKey("20m", slot, "NA")]
	if naBucket == nil || naBucket.Count != 1 {
		t.Errorf("NA bucket after round-trip = %v, want Count=1", naBucket)
	}
}

func TestWsprClimatologyColdStart(t *testing.T) {
	// Covers AE8: a freshly loaded climatology with 5 days of depth returns
	// SampleDays reflecting the span. (The in-memory fallback uses the span,
	// not actual per-day counts.)
	e := newWsprClimatologyEngine("")
	// Simulate 5 days of events by setting firstEventAt 5 days ago.
	fiveDaysAgo := int64(1786224360) - 5*86400
	now := int64(1786224360)
	e.firstEventAt = fiveDaysAgo
	e.lastEventAt = now
	// Seed one bucket.
	slot := utcSlotOfDay(now)
	e.buckets[wsprClimatologyKey("20m", slot, "JA")] = &wsprClimatologyBucket{
		Band: "20m", SlotOfDay: slot, Region: "JA", Count: 10,
	}

	rows := e.regionCalendarStatsFromMemory(now)
	var found bool
	for _, r := range rows {
		if r.Band == "20m" && r.Region == "JA" && r.SlotOfDay == slot {
			if r.SampleDays < 5 {
				t.Errorf("SampleDays = %d, want >= 5", r.SampleDays)
			}
			found = true
		}
	}
	if !found {
		t.Error("did not find 20m/JA/slot row in cold-start stats")
	}
}