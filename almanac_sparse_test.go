package main

import (
	"bytes"
	"errors"
	"math/rand"
	"testing"
	"time"
)

func sparseRoundTrip(t *testing.T, dense *[almanacSeasonCountsLen]byte) []byte {
	t.Helper()
	// v1 fixtures: the format prod already holds, which must keep decoding.
	enc := almanacSparseEncodeV1(dense)
	// The v2 encoding without SNR data carries exactly one mask byte more
	// per cell and decodes to the same counts.
	enc2 := almanacSparseEncode(dense, nil)
	if len(enc2) != almanacSparseEncodedLen(dense, nil) || cap(enc2) != len(enc2) {
		t.Fatalf("v2 encoded len %d / cap %d, want exactly %d", len(enc2), cap(enc2), almanacSparseEncodedLen(dense, nil))
	}
	var got2 [almanacSeasonCountsLen]byte
	if err := almanacSparseDecode(enc2, &got2); err != nil || got2 != *dense {
		t.Fatalf("v2 round trip mismatch: %v", err)
	}
	var got [almanacSeasonCountsLen]byte
	if err := almanacSparseDecode(enc, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got != *dense {
		t.Fatalf("round trip mismatch")
	}
	prev := -1
	n := 0
	if err := almanacSparseEach(enc, func(pos, v int) {
		if pos <= prev || v == 0 || dense[pos] != byte(v) {
			t.Fatalf("each: pos %d (prev %d) count %d, dense %d", pos, prev, v, dense[pos])
		}
		prev = pos
		n++
	}); err != nil {
		t.Fatal(err)
	}
	nz := 0
	for _, v := range dense {
		if v != 0 {
			nz++
		}
	}
	if n != nz {
		t.Fatalf("each visited %d cells, want %d non-zero", n, nz)
	}
	return enc
}

func TestAlmanacSparseRoundTrip(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		var d [almanacSeasonCountsLen]byte
		if enc := sparseRoundTrip(t, &d); !bytes.Equal(enc, []byte{0x01}) {
			t.Fatalf("empty = %x, want 01", enc)
		}
	})
	t.Run("single cell", func(t *testing.T) {
		var d [almanacSeasonCountsLen]byte
		d[almanacSegmentOffset(14)+36] = 9 // pos 660 → gap 660 = 2 varint bytes
		if enc := sparseRoundTrip(t, &d); !bytes.Equal(enc, []byte{0x01, 0x94, 0x05, 9}) {
			t.Fatalf("single = %x", enc)
		}
	})
	t.Run("first and last pos", func(t *testing.T) {
		var d [almanacSeasonCountsLen]byte
		d[0], d[almanacSeasonCountsLen-1] = 1, 255
		// pos 0: gap 0; pos 1487: gap 1486 = 0xce 0x0b.
		if enc := sparseRoundTrip(t, &d); !bytes.Equal(enc, []byte{0x01, 0x00, 1, 0xce, 0x0b, 255}) {
			t.Fatalf("first/last = %x", enc)
		}
	})
	t.Run("full dense", func(t *testing.T) {
		var d [almanacSeasonCountsLen]byte
		for i := range d {
			d[i] = byte(i%255 + 1)
		}
		if enc := sparseRoundTrip(t, &d); len(enc) != 1+2*almanacSeasonCountsLen {
			t.Fatalf("full row = %d B, want %d", len(enc), 1+2*almanacSeasonCountsLen)
		}
	})
	t.Run("saturation", func(t *testing.T) {
		var d [almanacSeasonCountsLen]byte
		satAdd8(&d[100], 200)
		satAdd8(&d[100], 200)
		satAdd8(&d[101], 1<<40)
		if d[100] != 255 || d[101] != 255 {
			t.Fatalf("satAdd8 did not saturate: %d %d", d[100], d[101])
		}
		sparseRoundTrip(t, &d)
	})
	t.Run("random", func(t *testing.T) {
		r := rand.New(rand.NewSource(1))
		for i := 0; i < 200; i++ {
			var d [almanacSeasonCountsLen]byte
			n := r.Intn(almanacSeasonCountsLen)
			for j := 0; j < n; j++ {
				d[r.Intn(almanacSeasonCountsLen)] = byte(r.Intn(256))
			}
			sparseRoundTrip(t, &d)
		}
	})
}

func TestAlmanacSparseMalformed(t *testing.T) {
	cases := map[string][]byte{
		"nil":               nil,
		"empty":             {},
		"bad version":       {0x03},
		"v2 missing mask":   {0x02, 0x05, 0x01},
		"v2 missing bin":    {0x02, 0x05, 0x01, 0x03, 0x01},
		"v2 zero bin":       {0x02, 0x05, 0x01, 0x01, 0x00},
		"v2 reserved bits":  {0x02, 0x05, 0x01, 0x40},
		"bad version data":  {0x00, 0x00, 0x01},
		"zero count":        {0x01, 0x05, 0x00},
		"missing count":     {0x01, 0x05},
		"truncated varint":  {0x01, 0x80},
		"truncated varint2": {0x01, 0x01, 0x02, 0xff, 0xff},
		"pos overflow":      {0x01, 0xd0, 0x0b, 0x01},             // gap 1488
		"pos overflow sum":  {0x01, 0xce, 0x0b, 0x01, 0x01, 0x01}, // pos 1486, then 1488
		"varint overflow":   {0x01, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01, 0x01},
		"huge gap":          {0x01, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f, 0x01},
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			called := false
			err := almanacSparseEach(b, func(int, int) { called = true })
			if !errors.Is(err, errAlmanacSparseMalformed) {
				t.Fatalf("err = %v, want malformed", err)
			}
			if called {
				t.Fatalf("callback ran on a malformed row (partial apply)")
			}
			d := [almanacSeasonCountsLen]byte{7: 7}
			if err := almanacSparseDecode(b, &d); err == nil || d != ([almanacSeasonCountsLen]byte{}) {
				t.Fatalf("decode err=%v, dense not cleared", err)
			}
		})
	}
	// The last valid position is accepted.
	if err := almanacSparseValidate([]byte{0x01, 0xce, 0x0b, 0x01}); err != nil {
		t.Fatalf("pos 1487 rejected: %v", err)
	}
}

func TestAlmanacSparseReplaceDay(t *testing.T) {
	var seg [almanacSeasonSlotsPerDay]byte
	seg[0], seg[47] = 5, 6
	enc, err := almanacSparseReplaceDay(nil, 31, &seg, nil)
	if err != nil {
		t.Fatal(err)
	}
	var want [almanacSeasonCountsLen]byte
	want[almanacSegmentOffset(31)], want[almanacSeasonCountsLen-1] = 5, 6
	if !bytes.Equal(enc, almanacSparseEncode(&want, nil)) {
		t.Fatalf("fresh row = %x", enc)
	}
	// Replace day 31 with a different segment; add day 1; idempotent.
	var seg2 [almanacSeasonSlotsPerDay]byte
	seg2[3] = 1
	enc2, _ := almanacSparseReplaceDay(enc, 31, &seg2, nil)
	enc3, _ := almanacSparseReplaceDay(enc2, 31, &seg2, nil)
	if !bytes.Equal(enc2, enc3) {
		t.Fatalf("replace not idempotent")
	}
	enc4, _ := almanacSparseReplaceDay(enc3, 1, &seg, nil)
	var d [almanacSeasonCountsLen]byte
	if err := almanacSparseDecode(enc4, &d); err != nil {
		t.Fatal(err)
	}
	var want4 [almanacSeasonCountsLen]byte
	want4[0], want4[47], want4[almanacSegmentOffset(31)+3] = 5, 6, 1
	if d != want4 {
		t.Fatalf("replace lost or kept wrong cells")
	}
	// Malformed existing row: treated as absent, error reported.
	enc5, err := almanacSparseReplaceDay([]byte{0x09}, 31, &seg, nil)
	if err == nil || !bytes.Equal(enc5, enc) {
		t.Fatalf("malformed existing: err=%v enc=%x", err, enc5)
	}
}

// TestAlmanacSparseSizing pins the per-row size for the prod cell counts
// (≈2 B/cell: 1 varint byte while gaps < 128, plus the count byte).
func TestAlmanacSparseSizing(t *testing.T) {
	for _, tc := range []struct {
		cells, maxBytes int
	}{{8, 1 + 2*8 + 8}, {95, 1 + 2*95 + 95}, {315, 1 + 2*315 + 30}, {1343, 1 + 2*1343}} {
		d := spreadRow(tc.cells)
		n := len(almanacSparseEncodeV1(&d))
		t.Logf("%4d cells → %4d B (v1)", tc.cells, n)
		if n > tc.maxBytes {
			t.Fatalf("%d cells → %d B, want ≤ %d", tc.cells, n, tc.maxBytes)
		}
	}
}

// spreadRow sets n cells evenly across the month (the worst case for gap
// varints at a given density).
func spreadRow(n int) [almanacSeasonCountsLen]byte {
	var d [almanacSeasonCountsLen]byte
	for i := 0; i < n; i++ {
		d[i*almanacSeasonCountsLen/n] = byte(i%250 + 1)
	}
	return d
}

// TestAlmanacSparseP90Cost bounds encode + decode of a p90 (315-cell) row:
// the fold does one decode+encode per key (~125k keys/day) inside a 60 s
// transaction, so even 50 µs/row would be ~6 s. Generous bound for CI noise
// and -race.
func TestAlmanacSparseP90Cost(t *testing.T) {
	if testing.Short() {
		t.Skip("timing")
	}
	d := spreadRow(315)
	h := spreadHist(&d)
	enc := almanacSparseEncode(&d, h)
	var seg [almanacSeasonSlotsPerDay]byte
	seg[10] = 3
	const n = 2000
	start := time.Now()
	for i := 0; i < n; i++ {
		if _, err := almanacSparseReplaceDay(enc, 14, &seg, nil); err != nil {
			t.Fatal(err)
		}
	}
	per := time.Since(start) / n
	t.Logf("p90 row replace-day (decode+encode): %v/row", per)
	if per > 200*time.Microsecond {
		t.Fatalf("p90 replace-day %v/row, want ≤ 200µs", per)
	}
}

func BenchmarkAlmanacSparseEncodeP90(b *testing.B) {
	d := spreadRow(315)
	b.ReportAllocs()
	h := spreadHist(&d)
	for i := 0; i < b.N; i++ {
		_ = almanacSparseEncode(&d, h)
	}
}

func BenchmarkAlmanacSparseEachP90(b *testing.B) {
	d := spreadRow(315)
	enc := almanacSparseEncode(&d, spreadHist(&d))
	b.ReportAllocs()
	sum := 0
	for i := 0; i < b.N; i++ {
		_ = almanacSparseEach(enc, func(_, v int) { sum += v })
	}
	_ = sum
}

func BenchmarkAlmanacSparseReplaceDayP90(b *testing.B) {
	d := spreadRow(315)
	enc := almanacSparseEncode(&d, spreadHist(&d))
	var seg [almanacSeasonSlotsPerDay]byte
	seg[10] = 3
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = almanacSparseReplaceDay(enc, 14, &seg, nil)
	}
}

// spreadHist gives every non-zero cell of d an SNR histogram with 1–4
// non-empty bins (avg 2.5), bins summing to at most the cell count.
func spreadHist(d *[almanacSeasonCountsLen]byte) *[almanacSeasonCountsLen]almanacSNRHist {
	var h [almanacSeasonCountsLen]almanacSNRHist
	i := 0
	for pos, v := range d {
		if v == 0 {
			continue
		}
		nb := i%4 + 1
		for b := 0; b < nb; b++ {
			h[pos][(b*2+i)%almanacSNRBins] = byte(i%7 + 1)
		}
		i++
	}
	return &h
}

func TestAlmanacSparseV2RoundTrip(t *testing.T) {
	var d [almanacSeasonCountsLen]byte
	var h [almanacSeasonCountsLen]almanacSNRHist
	d[0], h[0] = 3, almanacSNRHist{1, 0, 0, 0, 0, 2}
	d[700] = 9 // no SNR data (DX-cluster only)
	d[almanacSeasonCountsLen-1], h[almanacSeasonCountsLen-1] = 255, almanacSNRHist{255, 255, 255, 255, 255, 255}
	h[5] = almanacSNRHist{7} // zero-count cell: histogram dropped
	enc := almanacSparseEncode(&d, &h)
	if len(enc) != almanacSparseEncodedLen(&d, &h) {
		t.Fatalf("len %d != EncodedLen %d", len(enc), almanacSparseEncodedLen(&d, &h))
	}
	// pos 0: gap 0, count 3, mask 0b100001, bins 1, 2.
	if !bytes.Equal(enc[:6], []byte{0x02, 0x00, 3, 0x21, 1, 2}) {
		t.Fatalf("head = %x", enc[:6])
	}
	var gd [almanacSeasonCountsLen]byte
	var gh [almanacSeasonCountsLen]almanacSNRHist
	if err := almanacSparseDecodeHist(enc, &gd, &gh); err != nil {
		t.Fatal(err)
	}
	h[5] = almanacSNRHist{}
	if gd != d || gh != h {
		t.Fatalf("v2 round trip mismatch")
	}
	// Random rows.
	r := rand.New(rand.NewSource(2))
	for i := 0; i < 100; i++ {
		var d [almanacSeasonCountsLen]byte
		var h [almanacSeasonCountsLen]almanacSNRHist
		for j := r.Intn(400); j > 0; j-- {
			p := r.Intn(almanacSeasonCountsLen)
			d[p] = byte(r.Intn(255) + 1)
			for b := range h[p] {
				if r.Intn(2) == 0 {
					h[p][b] = byte(r.Intn(256))
				}
			}
		}
		enc := almanacSparseEncode(&d, &h)
		if err := almanacSparseDecodeHist(enc, &gd, &gh); err != nil || gd != d || gh != h {
			t.Fatalf("random v2 round trip %d: %v", i, err)
		}
	}
}

func TestAlmanacSparseV1ReadsAsNoSNR(t *testing.T) {
	var d [almanacSeasonCountsLen]byte
	d[10], d[1000] = 4, 200
	v1 := almanacSparseEncodeV1(&d)
	if v1[0] != 0x01 {
		t.Fatalf("v1 version byte %x", v1[0])
	}
	n := 0
	if err := almanacSparseEachCell(v1, func(pos, c int, h *almanacSNRHist) {
		n++
		if *h != (almanacSNRHist{}) || h.total() != 0 || byte(c) != d[pos] {
			t.Fatalf("v1 cell %d: count %d hist %v", pos, c, *h)
		}
	}); err != nil || n != 2 {
		t.Fatalf("v1 each: n=%d err=%v", n, err)
	}
	// Replacing a day of a v1 row writes v2 and keeps the other days' counts.
	var seg [almanacSeasonSlotsPerDay]byte
	var hseg [almanacSeasonSlotsPerDay]almanacSNRHist
	seg[1], hseg[1] = 5, almanacSNRHist{0, 0, 0, 2, 0, 3}
	enc, err := almanacSparseReplaceDay(v1, 31, &seg, &hseg)
	if err != nil || enc[0] != 0x02 {
		t.Fatalf("replace on v1: %v %x", err, enc)
	}
	var gd [almanacSeasonCountsLen]byte
	var gh [almanacSeasonCountsLen]almanacSNRHist
	if err := almanacSparseDecodeHist(enc, &gd, &gh); err != nil {
		t.Fatal(err)
	}
	p := almanacSegmentOffset(31) + 1
	if gd[10] != 4 || gd[1000] != 200 || gd[p] != 5 || gh[p] != hseg[1] || gh[10] != (almanacSNRHist{}) {
		t.Fatalf("v1→v2 replace lost data")
	}
}

func TestAlmanacSparseReplaceDayKeepsOtherDaysHistograms(t *testing.T) {
	var d [almanacSeasonCountsLen]byte
	var h [almanacSeasonCountsLen]almanacSNRHist
	d[3], h[3] = 6, almanacSNRHist{1, 1, 1, 1, 1, 1}
	p := almanacSegmentOffset(2) + 4
	d[p], h[p] = 2, almanacSNRHist{0, 2}
	enc := almanacSparseEncode(&d, &h)
	var seg [almanacSeasonSlotsPerDay]byte
	seg[4] = 9 // day 2 re-folded without SNR data
	out, err := almanacSparseReplaceDay(enc, 2, &seg, nil)
	if err != nil {
		t.Fatal(err)
	}
	var gd [almanacSeasonCountsLen]byte
	var gh [almanacSeasonCountsLen]almanacSNRHist
	if err := almanacSparseDecodeHist(out, &gd, &gh); err != nil {
		t.Fatal(err)
	}
	if gh[3] != h[3] || gd[3] != 6 || gd[p] != 9 || gh[p] != (almanacSNRHist{}) {
		t.Fatalf("replace-day must keep other days' histograms and SET the day's own")
	}
}

func TestAlmanacSNRHistFromCounts(t *testing.T) {
	// 10 SNR spots: 1 below −20, 2 in [−20,−15), 0, 3 in [−10,−5), 1, 3 ≥ 0.
	h := almanacSNRHistFromCounts(10, [almanacSNRTiers]int64{9, 7, 7, 4, 3})
	if h != (almanacSNRHist{1, 2, 0, 3, 1, 3}) || h.total() != 10 {
		t.Fatalf("hist = %v", h)
	}
	for tier, want := range []int{9, 7, 7, 4, 3} {
		if got := h.atLeast(tier); got != want {
			t.Fatalf("atLeast(%d) = %d, want %d", tier, got, want)
		}
	}
	// Saturation per bin; inconsistent input never goes negative.
	h = almanacSNRHistFromCounts(1000, [almanacSNRTiers]int64{1000, 1000, 1000, 1000, 1000})
	if h != (almanacSNRHist{0, 0, 0, 0, 0, 255}) {
		t.Fatalf("saturated hist = %v", h)
	}
	h = almanacSNRHistFromCounts(2, [almanacSNRTiers]int64{5, 1, 0, 0, 0})
	if h != (almanacSNRHist{0, 4, 1, 0, 0, 0}) {
		t.Fatalf("inconsistent hist = %v", h)
	}
}

// TestAlmanacSparseV2SizingEstimate extrapolates the prod storage of the v2
// format: the v1 baseline measured ~1.6 GB/year (249.6k key-months per
// month, avg 94.8 non-zero cells); v2 adds a mask byte plus one byte per
// non-empty SNR bin per cell (fixture: 2.5 bins avg), stored at fillfactor
// 70. Target ≤ 3.5 GB/year.
func TestAlmanacSparseV2SizingEstimate(t *testing.T) {
	d := spreadRow(95)
	v1 := len(almanacSparseEncodeV1(&d))
	v2 := len(almanacSparseEncode(&d, spreadHist(&d)))
	const rowsPerYear = 249_600 * 12
	est := 1.6e9 + float64(rowsPerYear)*float64(v2-v1)/0.70
	t.Logf("95-cell row: v1 %d B, v2 %d B (+%d B/row, %.2f B/cell); est %.2f GB/year", v1, v2, v2-v1, float64(v2-v1)/95, est/1e9)
	if est > 3.5e9 {
		t.Fatalf("v2 estimate %.2f GB/year exceeds 3.5 GB", est/1e9)
	}
}
