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

func TestXrayClassFromFlux(t *testing.T) {
	cases := []struct {
		flux float64
		want string
	}{
		{0, ""},
		{1e-8, "A1.0"},
		{1e-7, "B1.0"},
		{5e-7, "B5.0"},
		{3e-6, "C3.0"},
		{2e-5, "M2.0"},
		{1e-4, "X1.0"},
		{5e-4, "X5.0"},
	}
	for _, c := range cases {
		got := xrayClassFromFlux(c.flux)
		if got != c.want {
			t.Errorf("xrayClassFromFlux(%g) = %q, want %q", c.flux, got, c.want)
		}
	}
}
