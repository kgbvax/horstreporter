package main

import "testing"

func TestParseRBNSpot(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		wantOk bool
		want   rbnSpot
	}{
		{
			name:   "canonical CW line with skimmer -# suffix and trailing grid/time",
			line:   "DX de W3LPL-#:      14024.0  N0CALL         CW   22 dB   EN91   1234 Z",
			wantOk: true,
			want: rbnSpot{
				Skimmer:      "W3LPL-#",
				DXCall:       "N0CALL",
				FrequencyKHz: 14024.0,
				Mode:         "CW",
				DB:           22,
				ObservedAt:   1700000000,
			},
		},
		{
			name:   "RTTY line with numeric skimmer suffix",
			line:   "DX de OH8X-1:   7035.0  W1XYZ         RTTY  15 dB   KP20   0302 Z",
			wantOk: true,
			want: rbnSpot{
				Skimmer:      "OH8X-1",
				DXCall:       "W1XYZ",
				FrequencyKHz: 7035.0,
				Mode:         "RTTY",
				DB:           15,
				ObservedAt:   1700000000,
			},
		},
		{
			name:   "lowercase mode accepted and canonicalized to upper",
			line:   "DX de K1TTT-#:    14024.0  N0CALL         cw  22 dB   EN91   1234 Z",
			wantOk: true,
			want: rbnSpot{
				Skimmer:      "K1TTT-#",
				DXCall:       "N0CALL",
				FrequencyKHz: 14024.0,
				Mode:         "CW",
				DB:           22,
				ObservedAt:   1700000000,
			},
		},
		{name: "blank line", line: "", wantOk: false},
		{name: "login prompt is not a spot", line: "callsign:", wantOk: false},
		{name: "announcement is not a spot", line: "Welcome to the Reverse Beacon Network", wantOk: false},
		{name: "missing dB token", line: "DX de W3LPL-#: 14024.0 N0CALL CW 22 EN91 1234 Z", wantOk: false},
		{name: "non-numeric frequency rejected", line: "DX de W3LPL-#: abc N0CALL CW 22 dB EN91 1234 Z", wantOk: false},
		{name: "missing mode token rejected", line: "DX de W3LPL-#: 14024.0 N0CALL 22 dB EN91 1234 Z", wantOk: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseRBNSpot(tc.line, 1700000000)
			if ok != tc.wantOk {
				t.Fatalf("parseRBNSpot(%q) ok = %v, want %v", tc.line, ok, tc.wantOk)
			}
			if !ok {
				return
			}
			if got != tc.want {
				t.Fatalf("parseRBNSpot(%q) = %+v, want %+v", tc.line, got, tc.want)
			}
		})
	}
}

func TestIsNonConditionsMode(t *testing.T) {
	keep := []string{"", "FT8", "FT4", "DXCLUSTER", "ft8", "ft4", "dxcluster"}
	exclude := []string{"CW", "RTTY", "PSK31", "PSK", "SSB", "AM", "FM", "DIGI", "cw", "rtty"}
	for _, md := range keep {
		if isNonConditionsMode(md) {
			t.Errorf("isNonConditionsMode(%q) = true, want false (kept in FT8 accumulator)", md)
		}
	}
	for _, md := range exclude {
		if !isNonConditionsMode(md) {
			t.Errorf("isNonConditionsMode(%q) = false, want true (excluded from FT8 accumulator)", md)
		}
	}
}
