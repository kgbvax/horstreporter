package main

// Per-second counts of the ends that fall in an operator cluster block, per
// in-scope band. dx_conditions needs them for the "band vs its own normal"
// ratio (the regional count), and counting them used to mean visiting every
// message of the window: on prod that was ~1.4M messages, each one touching
// its locator strings in cold memory, about 100 ms at the 60-minute window for
// a QTH in a dense region. The counters ride on the area index's incremental
// scan instead (area_index.go): a request only classifies the messages that
// arrived since the previous one and sums at most a window's worth of
// per-second buckets.
//
// Counting by the message timestamp is what the evaluation does too (it keeps
// a message when liveStart <= T <= now), so a bucket sum over that range gives
// the same number as the walk, up to messages that sit on the wrong side of
// the window's sequence boundary because the feed's timestamps are not strictly
// ordered.
//
// Seconds close to the newest message live in a dense slice; anything outside
// its range (a sender with a bad clock, an old window) goes to a small map so
// the sum stays exact. When even that fills up the entry is marked unusable
// and the caller walks the window instead.

// clusterBandSlots is the number of in-scope bands (inScopeBandNames order).
const clusterBandSlots = len(inScopeBandNames)

const (
	// secondCountsMaxFar bounds the out-of-range seconds an entry keeps.
	secondCountsMaxFar = 4096
	// secondCountsForward is how far past the live end the dense range reaches.
	secondCountsForward = 20 * 60
	// secondCountsSlack is how far before the first message the dense range starts.
	secondCountsSlack = 120
	// secondCountsTrimEvery is the hysteresis for dropping old dense seconds.
	secondCountsTrimEvery = 300
)

type secondCounts struct {
	started bool
	span    int64 // seconds of history a request can reach back
	base    int64 // second of dense[0]
	dense   [][clusterBandSlots]int32
	far     map[int64]*[clusterBandSlots]int32
}

func (s *secondCounts) reset() {
	s.started = false
	s.base = 0
	s.dense = s.dense[:0]
	s.far = nil
}

// setSpan fixes how much history the dense range keeps: what a live rate can
// cover (the retention, or the longest window when there is none) plus slack.
func (s *secondCounts) setSpan() {
	s.span = int64(liveRateWindowMinutes(maxDxWindowMinutes)+5) * 60
}

// add counts n ends in band slot for second t. It reports false when the
// out-of-range map is full and the counts can no longer be trusted.
func (s *secondCounts) add(t int64, slot int, n int32) bool {
	if !s.started {
		s.started = true
		s.base = t - secondCountsSlack
		s.dense = s.dense[:0]
	}
	if i := t - s.base; i >= 0 && i < s.span+secondCountsForward {
		for int64(len(s.dense)) <= i {
			s.dense = append(s.dense, [clusterBandSlots]int32{})
		}
		s.dense[i][slot] += n
		return true
	}
	b := s.far[t]
	if b == nil {
		if len(s.far) >= secondCountsMaxFar {
			return false
		}
		if s.far == nil {
			s.far = make(map[int64]*[clusterBandSlots]int32)
		}
		b = new([clusterBandSlots]int32)
		s.far[t] = b
	}
	b[slot] += n
	return true
}

// trim forgets seconds no request at `now` or later can reach.
func (s *secondCounts) trim(now int64) {
	if !s.started || now == 0 {
		return
	}
	keep := now - s.span
	if keep <= s.base+secondCountsTrimEvery {
		return
	}
	if cut := keep - s.base; cut >= int64(len(s.dense)) {
		s.dense = s.dense[:0]
	} else {
		s.dense = s.dense[cut:]
	}
	s.base = keep
	for t := range s.far {
		if t < keep {
			delete(s.far, t)
		}
	}
}

// sum is the per-band count of ends whose message time lies in [from, to].
func (s *secondCounts) sum(from, to int64) (out [clusterBandSlots]int) {
	if !s.started || to < from {
		return out
	}
	lo, hi := from-s.base, to-s.base
	if lo < 0 {
		lo = 0
	}
	if hi >= int64(len(s.dense)) {
		hi = int64(len(s.dense)) - 1
	}
	for i := lo; i <= hi; i++ {
		b := &s.dense[i]
		for j := range b {
			out[j] += int(b[j])
		}
	}
	for t, b := range s.far {
		if t >= from && t <= to {
			for j := range b {
				out[j] += int(b[j])
			}
		}
	}
	return out
}
