package proplab

import "testing"

// TestSQLSlotOfDayMatchesUTCSlotOfDay guards the SQL slot expression used by the
// store's baseline queries, (((bucket_start / 900) % 96) / 2), against the Go
// reference UTCSlotOfDay. A previous version used ((bucket_start / 900) % 48),
// which wrapped the 96 fifteen-minute buckets per day twice and aliased slots
// ±12h. Sweeps every 15-minute boundary across several days.
func TestSQLSlotOfDayMatchesUTCSlotOfDay(t *testing.T) {
	base := int64(1700000000)
	for day := 0; day < 7; day++ {
		for i := int64(0); i < 96; i++ {
			ts := base + int64(day)*86400 + i*900
			sqlSlot := int((ts / 900 % 96) / 2)
			if sqlSlot != UTCSlotOfDay(ts) {
				t.Fatalf("ts=%d: sql slot %d != UTCSlotOfDay %d", ts, sqlSlot, UTCSlotOfDay(ts))
			}
		}
	}
}
