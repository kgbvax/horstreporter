package main

import (
	"testing"
)

func TestParseSWPCTime(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"2026-07-31 06:00:00.000", 1785477600, true},
		{"2026-07-31 06:00:00", 1785477600, true},
		{"", 0, false},
		{"not a date", 0, false},
	}
	for _, c := range cases {
		got, ok := parseSWPCTime(c.in)
		if ok != c.ok {
			t.Errorf("parseSWPCTime(%q) ok=%v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && got != c.want {
			t.Errorf("parseSWPCTime(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseDailySolarIndicesF107(t *testing.T) {
	// NOAA daily-solar-indices.txt column layout (whitespace-separated):
	//   fields[0..2] = YYYY MM DD
	//   fields[3]    = F10.7 cm radio flux
	//   fields[4]    = SESC sunspot number
	//   fields[5+]   = area, new regions, mean field, x-ray bkgd, flares...
	// Missing values use -999 as a sentinel.
	cases := []struct {
		name     string
		text     string
		wantOk   bool
		wantFlux float64
	}{
		{
			name: "today row with -999 sentinel skipped, yesterday used",
			text: "# NOAA SWPC daily solar indices\n" +
				":Product: daily-solar-indices.txt\n" +
				"#                         Sunspot\n" +
				"#  Date     10.7cm Number  ...\n" +
				"2026 07 30  142.5    120     550      2    -999      *   4  1  0\n" +
				"2026 07 31  -999     0       0        0    -999      *   0  0  0\n",
			wantOk:   true,
			wantFlux: 142.5,
		},
		{
			name: "all rows -999 sentinel, no valid flux",
			text: "# NOAA SWPC\n" +
				"2026 07 30  -999     0       0\n" +
				"2026 07 31  -999     0       0\n",
			wantOk:   false,
			wantFlux: 0,
		},
		{
			name:     "empty input",
			text:     "",
			wantOk:   false,
			wantFlux: 0,
		},
		{
			name: "single valid row",
			text: "2026 07 30  142.5    120     550      2    -999      *   4  1  0\n",
			wantOk:   true,
			wantFlux: 142.5,
		},
		{
			name: "multiple valid rows, latest (ascending date) wins",
			text: "2026 07 29  140.0    100     410      2    -999      *   4  0  0\n" +
				"2026 07 30  152.5    130     460      0    -999      *   1  1  0\n",
			wantOk:   true,
			wantFlux: 152.5,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			flux, _, ok := parseDailySolarIndicesF107(c.text)
			if ok != c.wantOk {
				t.Fatalf("ok = %v, want %v", ok, c.wantOk)
			}
			if ok && flux != c.wantFlux {
				t.Errorf("flux = %g, want %g", flux, c.wantFlux)
			}
		})
	}
}