package adif

import "testing"

func TestRecordGet(t *testing.T) {
	r := Record{"STATE": "  CT  "}
	if got := r.Get("STATE"); got != "CT" {
		t.Errorf("Get trimmed = %q, want CT", got)
	}
	if got := r.Get("MISSING"); got != "" {
		t.Errorf("Get missing = %q, want empty", got)
	}
}
