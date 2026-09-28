package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync/atomic"
)

// almanac_sparse.go is the single source of truth for the on-disk format of
// almanac_season_counts.counts (plan KTD5, KTD13). A month row is logically a
// dense 31 days × 48 slots grid (pos = (dom−1)*48 + slot, 0..1487) of cells:
// the spot count (uint8, saturating) plus, since KTD13, an SNR histogram of
// the cell's spots that carry a real SNR (PSKReporter / WSPR; DX-cluster
// spots have none). It is stored sparse because most cells are zero (prod:
// avg 94.8 non-zero cells of 1488, p50 8, p90 315) and Postgres only
// TOAST-compresses tuples above ~2 KB.
//
// Encoding version 2 (written since KTD13):
//
//	byte 0         0x02 (version)
//	then, for each non-zero-count cell in ascending pos order:
//	  uvarint(gap)   gap = pos − prevPos − 1, prevPos starts at −1
//	  count byte     1..255 (all spots; saturates at 255, see satAdd8)
//	  mask byte      bit b set ⇔ SNR bin b is non-zero (bits 6, 7 unused)
//	  bin bytes      one saturating byte (1..255) per set bit, ascending b
//
// The six disjoint SNR bins (almanacSNRBinFloors) are
// [<−20], [−20,−15), [−15,−10), [−10,−5), [−5,0), [≥0] dB; they sum to the
// cell's SNR-carrying spots (n_snr, each bin saturating). mask 0 = no SNR
// data (before the SNR collection start, or DX-cluster-only cells).
//
// Encoding version 1 (rows folded before KTD13; still read, never written):
// the same without mask and bins — every cell reads as "no SNR data".
//
// An all-zero row encodes as the single version byte. Gaps are < 1488, so a
// gap takes 1 varint byte when < 128 and 2 otherwise: v1 ≈2 B per cell, v2
// ≈3 B + 1 B per non-empty bin.
//
// Decoders reject malformed input (unknown version, pos ≥ 1488, truncated
// varint, zero count, missing mask/bin byte, zero bin, reserved mask bits,
// missing version byte) with an error and never apply a partial row; readers
// treat such a row as absent and log once (almanacSparseMalformed).

const (
	almanacSparseVersion1 = 0x01
	almanacSparseVersion2 = 0x02
	// almanacSparseVersion is the version the fold and the WSPR backfill write.
	almanacSparseVersion = almanacSparseVersion2

	// almanacSNRBins is the number of disjoint SNR histogram bins.
	almanacSNRBins = 6
	// almanacSNRTiers is the number of SNR floors (bins 1..5 lower edges).
	almanacSNRTiers = almanacSNRBins - 1
)

var errAlmanacSparseMalformed = errors.New("almanac sparse counts malformed")

// almanacSNRTierFloors are the SNR floors (dB) the Almanac supports: bin b
// (1..5) holds spots with floors[b-1] ≤ SNR < floors[b] (bin 5: ≥ 0 dB),
// bin 0 holds SNR < −20 dB.
var almanacSNRTierFloors = [almanacSNRTiers]int{-20, -15, -10, -5, 0}

// almanacSNRHist is one cell's SNR histogram (saturating bin counts).
type almanacSNRHist [almanacSNRBins]byte

// total is the cell's SNR-carrying spot count (saturated bins summed).
func (h *almanacSNRHist) total() int {
	n := 0
	for _, v := range h {
		n += int(v)
	}
	return n
}

// atLeast is the number of spots with SNR ≥ almanacSNRTierFloors[tier].
func (h *almanacSNRHist) atLeast(tier int) int {
	n := 0
	for b := tier + 1; b < almanacSNRBins; b++ {
		n += int(h[b])
	}
	return n
}

// almanacSNRHistFromCounts builds the disjoint bins from the cumulative
// counters (n_snr and the per-tier "SNR ≥ floor" counts), saturating each
// bin at 255. Inconsistent input (a cumulative count above the previous one)
// is clamped to 0 for that bin.
func almanacSNRHistFromCounts(nSNR int64, ge [almanacSNRTiers]int64) almanacSNRHist {
	var h almanacSNRHist
	prev := nSNR
	for b := 0; b < almanacSNRBins; b++ {
		next := int64(0)
		if b < almanacSNRTiers {
			next = ge[b]
		}
		satAdd8(&h[b], prev-next)
		prev = next
	}
	return h
}

// addHist adds o into h bin-wise (saturating).
func (h *almanacSNRHist) add(o *almanacSNRHist) {
	for b := range h {
		satAdd8(&h[b], int64(o[b]))
	}
}

// almanacSparseEncodedLen is the exact v2 encoded size of dense/hist.
func almanacSparseEncodedLen(dense *[almanacSeasonCountsLen]byte, hist *[almanacSeasonCountsLen]almanacSNRHist) int {
	n, prev := 1, -1
	for pos, v := range dense {
		if v == 0 {
			continue
		}
		n += almanacUvarintLen(uint64(pos-prev-1)) + 2
		if hist != nil {
			for _, b := range hist[pos] {
				if b != 0 {
					n++
				}
			}
		}
		prev = pos
	}
	return n
}

func almanacUvarintLen(x uint64) int {
	n := 1
	for x >= 0x80 {
		x >>= 7
		n++
	}
	return n
}

// almanacSparseAppend appends the v2 encoding of dense (counts) and hist
// (SNR histograms, nil = no SNR data) to dst. Histograms of zero-count cells
// are dropped.
func almanacSparseAppend(dst []byte, dense *[almanacSeasonCountsLen]byte, hist *[almanacSeasonCountsLen]almanacSNRHist) []byte {
	dst = append(dst, almanacSparseVersion2)
	prev := -1
	for pos, v := range dense {
		if v == 0 {
			continue
		}
		dst = binary.AppendUvarint(dst, uint64(pos-prev-1))
		dst = append(dst, v)
		var mask byte
		if hist != nil {
			for b, c := range hist[pos] {
				if c != 0 {
					mask |= 1 << uint(b)
				}
			}
		}
		dst = append(dst, mask)
		if mask != 0 {
			for _, c := range hist[pos] {
				if c != 0 {
					dst = append(dst, c)
				}
			}
		}
		prev = pos
	}
	return dst
}

// almanacSparseEncode returns the v2 encoding of dense and hist (nil = no
// SNR data) in an exactly sized slice.
func almanacSparseEncode(dense *[almanacSeasonCountsLen]byte, hist *[almanacSeasonCountsLen]almanacSNRHist) []byte {
	return almanacSparseAppend(make([]byte, 0, almanacSparseEncodedLen(dense, hist)), dense, hist)
}

// almanacSparseEncodeV1 returns the legacy v1 encoding (no SNR). Never
// written by production code any more; kept so tests can build the rows prod
// already holds.
func almanacSparseEncodeV1(dense *[almanacSeasonCountsLen]byte) []byte {
	dst := []byte{almanacSparseVersion1}
	prev := -1
	for pos, v := range dense {
		if v == 0 {
			continue
		}
		dst = binary.AppendUvarint(dst, uint64(pos-prev-1))
		dst = append(dst, v)
		prev = pos
	}
	return dst
}

// almanacSparseValidate checks b (v1 or v2) without applying it.
func almanacSparseValidate(b []byte) error {
	if len(b) == 0 {
		return fmt.Errorf("%w: empty (no version byte)", errAlmanacSparseMalformed)
	}
	v2 := false
	switch b[0] {
	case almanacSparseVersion1:
	case almanacSparseVersion2:
		v2 = true
	default:
		return fmt.Errorf("%w: version 0x%02x", errAlmanacSparseMalformed, b[0])
	}
	pos := -1
	for i := 1; i < len(b); {
		gap, n := binary.Uvarint(b[i:])
		if n <= 0 {
			return fmt.Errorf("%w: bad varint at byte %d", errAlmanacSparseMalformed, i)
		}
		i += n
		if gap >= almanacSeasonCountsLen || pos+1+int(gap) >= almanacSeasonCountsLen {
			return fmt.Errorf("%w: pos overflow at byte %d", errAlmanacSparseMalformed, i)
		}
		pos += 1 + int(gap)
		if i >= len(b) {
			return fmt.Errorf("%w: missing count at byte %d", errAlmanacSparseMalformed, i)
		}
		if b[i] == 0 {
			return fmt.Errorf("%w: zero count at byte %d", errAlmanacSparseMalformed, i)
		}
		i++
		if !v2 {
			continue
		}
		if i >= len(b) {
			return fmt.Errorf("%w: missing snr mask at byte %d", errAlmanacSparseMalformed, i)
		}
		mask := b[i]
		i++
		if mask>>almanacSNRBins != 0 {
			return fmt.Errorf("%w: reserved snr mask bits at byte %d", errAlmanacSparseMalformed, i-1)
		}
		for bin := 0; bin < almanacSNRBins; bin++ {
			if mask&(1<<uint(bin)) == 0 {
				continue
			}
			if i >= len(b) {
				return fmt.Errorf("%w: missing snr bin at byte %d", errAlmanacSparseMalformed, i)
			}
			if b[i] == 0 {
				return fmt.Errorf("%w: zero snr bin at byte %d", errAlmanacSparseMalformed, i)
			}
			i++
		}
	}
	return nil
}

// almanacSparseEachCell calls fn(pos, count, hist) for every non-zero cell
// of b (v1 or v2) in ascending pos order; hist is all-zero for v1 rows and
// cells without SNR data, and is only valid during the call. b is validated
// first: on error fn is never called.
func almanacSparseEachCell(b []byte, fn func(pos, count int, hist *almanacSNRHist)) error {
	if err := almanacSparseValidate(b); err != nil {
		return err
	}
	v2 := b[0] == almanacSparseVersion2
	var h almanacSNRHist
	pos := -1
	for i := 1; i < len(b); {
		gap, n := binary.Uvarint(b[i:])
		i += n
		pos += 1 + int(gap)
		c := int(b[i])
		i++
		h = almanacSNRHist{}
		if v2 {
			mask := b[i]
			i++
			for bin := 0; bin < almanacSNRBins; bin++ {
				if mask&(1<<uint(bin)) != 0 {
					h[bin] = b[i]
					i++
				}
			}
		}
		fn(pos, c, &h)
	}
	return nil
}

// almanacSparseEach calls fn(pos, count) for every non-zero cell of b (v1 or
// v2) in ascending pos order. b is validated first: on error fn is never
// called.
func almanacSparseEach(b []byte, fn func(pos, count int)) error {
	return almanacSparseEachCell(b, func(pos, count int, _ *almanacSNRHist) { fn(pos, count) })
}

// almanacSparseDecode fills dense (counts) from b. On error dense is left
// all-zero.
func almanacSparseDecode(b []byte, dense *[almanacSeasonCountsLen]byte) error {
	return almanacSparseDecodeHist(b, dense, nil)
}

// almanacSparseDecodeHist fills dense (counts) and hist (may be nil) from b.
// On error both are left all-zero.
func almanacSparseDecodeHist(b []byte, dense *[almanacSeasonCountsLen]byte, hist *[almanacSeasonCountsLen]almanacSNRHist) error {
	*dense = [almanacSeasonCountsLen]byte{}
	if hist != nil {
		*hist = [almanacSeasonCountsLen]almanacSNRHist{}
	}
	return almanacSparseEachCell(b, func(pos, count int, h *almanacSNRHist) {
		dense[pos] = byte(count)
		if hist != nil {
			hist[pos] = *h
		}
	})
}

// almanacSparseReplaceDay is the fold's read-modify-write: it replaces day
// dom's 48-slot segment of the existing row (nil = no row yet; v1 or v2) with
// seg and hseg (the day's SNR histograms, nil = no SNR data) — SET semantics,
// so re-folding a day is byte-identical — and returns the new v2 encoding.
// The other days keep their counts and histograms (a v1 row's days have no
// SNR data). A malformed existing row is treated as absent (its error is
// returned alongside the fresh encoding so the caller can log it).
func almanacSparseReplaceDay(existing []byte, dom int, seg *[almanacSeasonSlotsPerDay]byte, hseg *[almanacSeasonSlotsPerDay]almanacSNRHist) ([]byte, error) {
	var dense [almanacSeasonCountsLen]byte
	var hist [almanacSeasonCountsLen]almanacSNRHist
	var derr error
	if existing != nil {
		derr = almanacSparseDecodeHist(existing, &dense, &hist)
	}
	off := almanacSegmentOffset(dom)
	copy(dense[off:off+almanacSeasonSlotsPerDay], seg[:])
	if hseg != nil {
		copy(hist[off:off+almanacSeasonSlotsPerDay], hseg[:])
	} else {
		clear(hist[off : off+almanacSeasonSlotsPerDay])
	}
	return almanacSparseEncode(&dense, &hist), derr
}

// almanacSparseMalformedLogged makes almanacSparseMalformed log only once per
// process (a corrupt row would otherwise log on every read).
var almanacSparseMalformedLogged atomic.Bool

// almanacSparseMalformed reports a malformed seasonal row (treated as absent).
func almanacSparseMalformed(where, grid, band, region string, ym int, err error) {
	if almanacSparseMalformedLogged.CompareAndSwap(false, true) {
		logError("almanac: malformed almanac_season_counts row treated as absent (%s: %s/%s/%s/%d): %v (further malformed rows are not logged)",
			where, grid, band, region, ym, err)
	}
}
