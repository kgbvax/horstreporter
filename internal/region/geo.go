package region

import "strings"

// IsLocator reports whether s looks like a valid Maidenhead locator (at least
// two letters followed by two digits). Mirrors the legacy isLocator in
// package main.
func IsLocator(s string) bool {
	if len(s) < 4 {
		return false
	}
	s = strings.ToUpper(s)
	if s[0] < 'A' || s[0] > 'R' || s[1] < 'A' || s[1] > 'R' {
		return false
	}
	if s[2] < '0' || s[2] > '9' || s[3] < '0' || s[3] > '9' {
		return false
	}
	return true
}

// LocatorCentroid returns the centre (lat, lng) of the 4-character square for
// a locator. 6-character locators are honoured to give a more accurate
// midpoint; shorter inputs return ok=false. Mirrors locatorToLatLng.
func LocatorCentroid(locator string) (lat, lng float64, ok bool) {
	locator = strings.ToUpper(locator)
	if len(locator) < 2 {
		return 0, 0, false
	}
	lng = float64(locator[0]-'A')*20 - 180
	lat = float64(locator[1]-'A')*10 - 90

	if len(locator) >= 4 {
		lng += float64(locator[2]-'0') * 2
		lat += float64(locator[3]-'0') * 1
		if len(locator) >= 6 {
			lng += float64(locator[4]-'A')*(5.0/60.0) + (5.0 / 120.0)
			lat += float64(locator[5]-'A')*(2.5/60.0) + (2.5 / 120.0)
		} else {
			lng += 1.0
			lat += 0.5
		}
	} else {
		lng += 10.0 // centre of field
		lat += 5.0
	}
	if !IsLocator(locator) {
		return lat, lng, false
	}
	return lat, lng, true
}
