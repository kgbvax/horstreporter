package main

import "testing"

func TestDeriveWaveLogGateWSURL(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"http://127.0.0.1:54321", "ws://127.0.0.1:54322/", false},
		{"http://127.0.0.1:54321/", "ws://127.0.0.1:54322/", false},
		{"https://shack.local:54321", "wss://shack.local:54322/", false},
		{"http://192.168.1.50", "ws://192.168.1.50:54322/", false},
		{"", "", true},
		{"://bad", "", true},
	}
	for _, c := range cases {
		got, err := deriveWaveLogGateWSURL(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("deriveWaveLogGateWSURL(%q) expected error, got %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("deriveWaveLogGateWSURL(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("deriveWaveLogGateWSURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestWaveLogGateWSApplyMessage(t *testing.T) {
	c := &waveLogGateWS{}

	// welcome handshake: flips connected, no radio state yet.
	c.applyMessage([]byte(`{"type":"welcome","message":"hi"}`))
	if _, have := c.RadioState(); have {
		t.Fatal("welcome should not produce a radio state")
	}

	// simplex status
	c.applyMessage([]byte(`{"type":"radio_status","frequency":14074000,"mode":"USB","radio":"WLGate"}`))
	st, have := c.RadioState()
	if !have {
		t.Fatal("expected radio state after status message")
	}
	if st.FreqHz != 14074000 || st.Mode != "USB" {
		t.Errorf("got freq=%d mode=%q, want 14074000/USB", st.FreqHz, st.Mode)
	}
	if st.Split {
		t.Error("simplex status must not report split")
	}
	if !st.Online {
		t.Error("status with freq should be online")
	}

	// split status: distinct RX frequency
	c.applyMessage([]byte(`{"type":"radio_status","frequency":14074000,"mode":"CW","frequency_rx":14080000,"mode_rx":"CW"}`))
	st, _ = c.RadioState()
	if !st.Split {
		t.Fatal("expected split when frequency_rx differs")
	}
	if st.FreqRxHz != 14080000 {
		t.Errorf("split RX = %d, want 14080000", st.FreqRxHz)
	}

	// equal RX frequency is NOT split
	c.applyMessage([]byte(`{"type":"radio_status","frequency":14074000,"mode":"CW","frequency_rx":14074000}`))
	st, _ = c.RadioState()
	if st.Split {
		t.Error("equal frequency_rx must not be treated as split")
	}
}
