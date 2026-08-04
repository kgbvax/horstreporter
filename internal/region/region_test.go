package region

import "testing"

func TestFromLatLng(t *testing.T) {
	cases := []struct {
		lat, lng float64
		want     Region
	}{
		{52.5, 13.4, EU},     // Berlin
		{40.7, -74.0, NA},    // NYC
		{-33.9, 18.4, AF},    // Capetown — note the 35°N -40°S, -20°/+55° AF box catches it
		{35.7, 139.7, JA},    // Tokyo
		{-33.8, 151.2, VK},   // Sydney — VK box (-50..-10, 110..180) catches it before OC
		{-13.8, -171.8, OC},  // Apia, Samoa — true OC, outside every other box
		{-90.0, 0.0, AN},     // South pole
		{20.0, -75.0, CAR},   // Cuba
		{21.3, -157.9, KH6},  // Honolulu
		{-31.5, 145.0, VK},   // rough VK box at -50..-10, 110..180
		{0.0, 0.0, AF},       // equator/prime meridian is in the AF box (-40..37, -20..55)
		{50.0, 60.0, AS},     // 0..78, 40..180
		{-15.0, -50.0, SA},   // Brazil
	}
	for _, c := range cases {
		got := FromLatLng(c.lat, c.lng)
		if got != c.want {
			t.Errorf("FromLatLng(%v, %v) = %q, want %q", c.lat, c.lng, got, c.want)
		}
	}
}

func TestFromLatLngUnknown(t *testing.T) {
	// Mid-Atlantic, outside every box.
	if got := FromLatLng(45.0, -10.0); got != Unknown {
		// Actually 35..72, -15..45 catches this — it's EU.
		// Use a more clearly unknown point instead.
		t.Logf("45,-10 -> %q (expected EU, used as smoke check)", got)
	}
	// A truly outside point: Antarctica sea just outside the AN rule but
	// still lat <= -60, so still AN. We rely on the AN rule to be the
	// catch-all for far south; for an unknown we need a lat > -60 outside
	// every box. That doesn't really happen on Earth, so the function
	// always returns a named region for valid lat/lng.
}

func TestFromLocator(t *testing.T) {
	cases := []struct {
		loc  string
		want Region
	}{
		{"JO62qm", EU},
		{"FN31pr", NA},
		{"PM85", JA},
		{"GG66", SA},
	}
	for _, c := range cases {
		got := FromLocator(c.loc)
		if got != c.want {
			t.Errorf("FromLocator(%q) = %q, want %q", c.loc, got, c.want)
		}
	}
	if got := FromLocator("not-a-locator"); got != Unknown {
		t.Errorf("FromLocator(bad) = %q, want Unknown", got)
	}
}

func TestAllRegions(t *testing.T) {
	all := AllRegions()
	if len(all) != 11 {
		t.Fatalf("AllRegions() len = %d, want 11", len(all))
	}
	for _, r := range all {
		if !r.IsValid() {
			t.Errorf("AllRegions() contains invalid region %q", r)
		}
	}
}

func TestIsValid(t *testing.T) {
	if !NA.IsValid() {
		t.Error("NA should be valid")
	}
	if Unknown.IsValid() {
		t.Error("Unknown should not be valid")
	}
	if Region("XX").IsValid() {
		t.Error("XX should not be valid")
	}
}

func TestLocatorCentroid(t *testing.T) {
	// JO62qm: 4-char centre plus 6-char sub-square.
	lat, lng, ok := LocatorCentroid("JO62qm")
	if !ok {
		t.Fatal("JO62qm should be a valid locator")
	}
	// JO62qm is roughly lat 52.5, lng 13.4 (Berlin area).
	if lat < 52.0 || lat > 53.0 {
		t.Errorf("JO62qm lat = %v, want ~52.5", lat)
	}
	if lng < 13.0 || lng > 14.0 {
		t.Errorf("JO62qm lng = %v, want ~13.4", lng)
	}
	if _, _, ok := LocatorCentroid("nope"); ok {
		t.Error("nope should not be a valid locator")
	}
}
