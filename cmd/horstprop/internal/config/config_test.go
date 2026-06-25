package config

import (
	"strings"
	"testing"
)

func TestStreamURL(t *testing.T) {
	c := Defaults()
	if c.StreamURL() != "" {
		t.Errorf("no base URL should yield empty stream URL, got %q", c.StreamURL())
	}

	c.HorstBaseURL = "http://127.0.0.1:8080/"
	c.HomeGrid = "JO32we"
	c.AreaRings = 5
	c.WindowMinutes = 30

	got := c.StreamURL()
	for _, want := range []string{"http://127.0.0.1:8080/api/stream?", "target=JO32", "rings=5", "minutes=30"} {
		if !strings.Contains(got, want) {
			t.Errorf("StreamURL %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "JO32WE") {
		t.Errorf("target should be the 4-char square, got %q", got)
	}

	// minutes clamps to the stream's 60-minute ceiling.
	c.WindowMinutes = 120
	if !strings.Contains(c.StreamURL(), "minutes=60") {
		t.Errorf("WindowMinutes should clamp to 60, got %q", c.StreamURL())
	}
}

func TestFromEnvAreaRings(t *testing.T) {
	t.Setenv("HORSTPROP_AREA_RINGS", "7")
	t.Setenv("HORSTPROP_HORST_URL", "http://example:8080")
	c := FromEnv(Defaults())
	if c.AreaRings != 7 {
		t.Errorf("AreaRings=%d want 7", c.AreaRings)
	}
	if !c.FeedEnabled() {
		t.Error("FeedEnabled should be true when HORST_URL set")
	}
}
