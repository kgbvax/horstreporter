package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync/atomic"
)

// almanac_sparse.go is the single source of truth for the on-disk format of
// almanac_season_counts.counts (plan KTD5). A month row is logically a dense
// 31 days × 48 slots uint8 grid (pos = (dom−1)*48 + slot, 0..1487), but it is
// stored sparse because most cells are zero (prod: avg 94.8 non-zero cells of
// 1488, p50 8, p90 315) and Postgres only TOAST-compresses tuples above
// ~2 KB, so a dense 1488-byte value is stored raw (1492 B/row measured).
//
// Encoding (version 1):
//
//	byte 0         0x01 (version)
//	then, for each non-zero cell in ascending pos order:
//	  uvarint(gap)   gap = pos − prevPos − 1, prevPos starts at −1
//	  count byte     1..255 (zero cells are never stored; counts saturate
//	                 at 255, see satAdd8)
//
// An all-zero row encodes as the single version byte. Gaps are < 1488, so a
// gap takes 1 varint byte when < 128 and 2 otherwise: ≈2 B per non-zero cell.
//
// Decoders reject malformed input (unknown version, pos ≥ 1488, truncated
// varint, zero count, missing version byte) with an error and never apply a
// partial row; readers treat such a row as absent and log once
// (almanacSparseMalformed).

const almanacSparseVersion = 0x01

var errAlmanacSparseMalformed = errors.New("almanac sparse counts malformed")

// almanacSparseEncodedLen is the exact encoded size of dense.
func almanacSparseEncodedLen(dense *[almanacSeasonCountsLen]byte) int {
	n, prev := 1, -1
	for pos, v := range dense {
		if v == 0 {
			continue
		}
		n += almanacUvarintLen(uint64(pos-prev-1)) + 1
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

// almanacSparseAppend appends the encoding of dense to dst.
func almanacSparseAppend(dst []byte, dense *[almanacSeasonCountsLen]byte) []byte {
	dst = append(dst, almanacSparseVersion)
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

// almanacSparseEncode returns the encoding of dense in an exactly sized slice.
func almanacSparseEncode(dense *[almanacSeasonCountsLen]byte) []byte {
	return almanacSparseAppend(make([]byte, 0, almanacSparseEncodedLen(dense)), dense)
}

// almanacSparseValidate checks b without applying it.
func almanacSparseValidate(b []byte) error {
	if len(b) == 0 {
		return fmt.Errorf("%w: empty (no version byte)", errAlmanacSparseMalformed)
	}
	if b[0] != almanacSparseVersion {
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
	}
	return nil
}

// almanacSparseEach calls fn(pos, count) for every non-zero cell of b in
// ascending pos order. b is validated first: on error fn is never called.
func almanacSparseEach(b []byte, fn func(pos, count int)) error {
	if err := almanacSparseValidate(b); err != nil {
		return err
	}
	pos := -1
	for i := 1; i < len(b); {
		gap, n := binary.Uvarint(b[i:])
		i += n
		pos += 1 + int(gap)
		fn(pos, int(b[i]))
		i++
	}
	return nil
}

// almanacSparseDecode fills dense from b. On error dense is left all-zero.
func almanacSparseDecode(b []byte, dense *[almanacSeasonCountsLen]byte) error {
	*dense = [almanacSeasonCountsLen]byte{}
	return almanacSparseEach(b, func(pos, count int) { dense[pos] = byte(count) })
}

// almanacSparseReplaceDay is the fold's read-modify-write: it replaces day
// dom's 48-slot segment of the existing row (nil = no row yet) with seg — SET
// semantics, so re-folding a day is byte-identical — and returns the new
// encoding. A malformed existing row is treated as absent (its error is
// returned alongside the fresh encoding so the caller can log it).
func almanacSparseReplaceDay(existing []byte, dom int, seg *[almanacSeasonSlotsPerDay]byte) ([]byte, error) {
	var dense [almanacSeasonCountsLen]byte
	var derr error
	if existing != nil {
		derr = almanacSparseDecode(existing, &dense)
	}
	off := almanacSegmentOffset(dom)
	copy(dense[off:off+almanacSeasonSlotsPerDay], seg[:])
	return almanacSparseEncode(&dense), derr
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
