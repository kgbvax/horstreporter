package kc2g

import (
	"testing"
	"time"

	"horstreporter/cmd/horstprop/internal/geo"
)

// Real KC2G shape: array; string lat/lon; longitude 0–360°E; null mufd present.
const fixture = `[
 {"mufd":28.8,"time":"2026-06-25T11:58:00","station":{"latitude":"30.4","longitude":"262.3","code":"AU930"}},
 {"mufd":null,"time":"2026-06-25T11:58:00","station":{"latitude":"50.0","longitude":"10.0","code":"XX"}},
 {"mufd":14.2,"time":"2026-06-25T11:50:00","station":{"latitude":"51.5","longitude":"0.0","code":"YY"}}
]`

func TestParseStations(t *testing.T) {
	st, err := parseStations([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(st) != 2 {
		t.Fatalf("want 2 stations (null mufd skipped), got %d", len(st))
	}
	var austin *station
	for i := range st {
		if st[i].muf == 28.8 {
			austin = &st[i]
		}
	}
	if austin == nil {
		t.Fatal("Austin station missing")
	}
	if austin.loc.Lon < -100 || austin.loc.Lon > -90 { // 262.3°E → -97.7°
		t.Errorf("longitude not normalised to ±180: got %.1f", austin.loc.Lon)
	}
}

func TestMUFAt(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	c := New("")
	c.Now = func() time.Time { return now }
	c.stations = []station{
		{loc: geo.LatLon{Lat: 51.5, Lon: 0.0}, muf: 14.0, at: now.Add(-10 * time.Minute)},    // London, fresh
		{loc: geo.LatLon{Lat: 52.5, Lon: 13.4}, muf: 18.0, at: now.Add(-10 * time.Minute)},   // Berlin, fresh
		{loc: geo.LatLon{Lat: 40.0, Lon: -100.0}, muf: 30.0, at: now.Add(-10 * time.Minute)}, // US, fresh but far
		{loc: geo.LatLon{Lat: 50.0, Lon: 8.0}, muf: 99.0, at: now.Add(-5 * time.Hour)},       // near home but STALE
	}

	muf, age, ok := c.MUFAt(50.0, 8.0) // central Europe
	if !ok {
		t.Fatal("expected a MUF estimate near Europe")
	}
	if muf < 13 || muf > 19 {
		t.Errorf("MUF %.1f outside expected 14–18 (London/Berlin IDW; far+stale excluded)", muf)
	}
	if age < 9 || age > 11 {
		t.Errorf("age %.1f want ~10 min", age)
	}

	if _, _, ok := c.MUFAt(0, -150); ok {
		t.Error("mid-Pacific: no fresh station in range, expected ok=false")
	}
	if c.FreshStations() != 3 {
		t.Errorf("FreshStations=%d want 3 (stale excluded)", c.FreshStations())
	}
}
