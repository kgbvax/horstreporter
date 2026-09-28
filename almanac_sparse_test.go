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
	enc := almanacSparseEncode(dense)
	if len(enc) != almanacSparseEncodedLen(dense) || cap(enc) != len(enc) {
		t.Fatalf("encoded len %d / cap %d, want exactly %d", len(enc), cap(enc), almanacSparseEncodedLen(dense))
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
		"bad version":       {0x02},
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
	enc, err := almanacSparseReplaceDay(nil, 31, &seg)
	if err != nil {
		t.Fatal(err)
	}
	var want [almanacSeasonCountsLen]byte
	want[almanacSegmentOffset(31)], want[almanacSeasonCountsLen-1] = 5, 6
	if !bytes.Equal(enc, almanacSparseEncode(&want)) {
		t.Fatalf("fresh row = %x", enc)
	}
	// Replace day 31 with a different segment; add day 1; idempotent.
	var seg2 [almanacSeasonSlotsPerDay]byte
	seg2[3] = 1
	enc2, _ := almanacSparseReplaceDay(enc, 31, &seg2)
	enc3, _ := almanacSparseReplaceDay(enc2, 31, &seg2)
	if !bytes.Equal(enc2, enc3) {
		t.Fatalf("replace not idempotent")
	}
	enc4, _ := almanacSparseReplaceDay(enc3, 1, &seg)
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
	enc5, err := almanacSparseReplaceDay([]byte{0x09}, 31, &seg)
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
		n := len(almanacSparseEncode(&d))
		t.Logf("%4d cells → %4d B", tc.cells, n)
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
	enc := almanacSparseEncode(&d)
	var seg [almanacSeasonSlotsPerDay]byte
	seg[10] = 3
	const n = 2000
	start := time.Now()
	for i := 0; i < n; i++ {
		if _, err := almanacSparseReplaceDay(enc, 14, &seg); err != nil {
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
	for i := 0; i < b.N; i++ {
		_ = almanacSparseEncode(&d)
	}
}

func BenchmarkAlmanacSparseEachP90(b *testing.B) {
	d := spreadRow(315)
	enc := almanacSparseEncode(&d)
	b.ReportAllocs()
	sum := 0
	for i := 0; i < b.N; i++ {
		_ = almanacSparseEach(enc, func(_, v int) { sum += v })
	}
	_ = sum
}

func BenchmarkAlmanacSparseReplaceDayP90(b *testing.B) {
	d := spreadRow(315)
	enc := almanacSparseEncode(&d)
	var seg [almanacSeasonSlotsPerDay]byte
	seg[10] = 3
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = almanacSparseReplaceDay(enc, 14, &seg)
	}
}
