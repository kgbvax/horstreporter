package proplab

import "testing"

func TestBandForFrequencyKHz(t *testing.T) {
	cases := []struct {
		kHz  float64
		want string
	}{
		{14060, "20m"},
		{7052, "40m"},
		{10120, "30m"},
		{28400, "10m"},
		{50125, "6m"},
		{145500, "2m"},
		{999, ""},
	}
	for _, c := range cases {
		got := BandForFrequencyKHz(c.kHz)
		if got != c.want {
			t.Errorf("BandForFrequencyKHz(%v) = %q, want %q", c.kHz, got, c.want)
		}
	}
}
