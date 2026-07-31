package proplab

import "time"

// time.go contains the time helpers used by the proplab engines.

// BucketSeconds is the duration of one proplab cell bucket (15 minutes).
const BucketSeconds = 15 * 60

// UTCSlotOfDay returns the 30-minute slot index (0..47) of the given Unix timestamp.
func UTCSlotOfDay(ts int64) int {
	t := time.Unix(ts, 0).UTC()
	return t.Hour()*2 + t.Minute()/30
}

// AlignBucketStart rounds t down to the nearest 15-minute boundary.
func AlignBucketStart(t int64) int64 {
	return (t / BucketSeconds) * BucketSeconds
}
