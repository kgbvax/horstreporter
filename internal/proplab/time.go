package proplab

// time.go contains the time helpers used by the proplab cell-bucket feed.

// BucketSeconds is the duration of one proplab cell bucket (15 minutes).
const BucketSeconds = 15 * 60

// AlignBucketStart rounds t down to the nearest 15-minute boundary.
func AlignBucketStart(t int64) int64 {
	return (t / BucketSeconds) * BucketSeconds
}
