package main

import "testing"

func TestQRZLookupCandidates(t *testing.T) {
	tests := []struct {
		name string
		call string
		want []string
	}{
		{name: "portable prefix", call: "EK/RX3DPK", want: []string{"EK/RX3DPK", "RX3DPK", "EK"}},
		{name: "portable suffix", call: "DL7VEE/P", want: []string{"DL7VEE/P", "DL7VEE", "P"}},
		{name: "prefix+suffix", call: "EA8/DL7VEE/P", want: []string{"EA8/DL7VEE/P", "DL7VEE", "EA8", "P"}},
		{name: "plain", call: "DL7VEE", want: []string{"DL7VEE"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := qrzLookupCandidates(tt.call)
			if len(got) != len(tt.want) {
				t.Fatalf("%s: expected %d candidates got %d (%v)", tt.call, len(tt.want), len(got), got)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("%s: candidate[%d] expected %q got %q (all=%v)", tt.call, i, tt.want[i], got[i], got)
				}
			}
		})
	}
}
