// Package config holds horstprop's runtime configuration and its defaults.
// Values come from flags (wired in main) with environment-variable fallbacks;
// no secrets are stored in the repo.
package config

import (
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Config is the resolved horstprop configuration.
type Config struct {
	Listen        string // scoring API listen address
	HomeGrid      string // operator home Maidenhead locator
	StationCall   string // operator callsign (kept out of the repo; env/.env)
	CtyPath       string // path to AD1C cty.dat; empty = callsign→centroid disabled
	HorstBaseURL  string // HorstReporter base URL (e.g. http://127.0.0.1:8080); empty = feed off
	AreaRings     int    // size of the area of interest, in Maidenhead-square rings around home
	MatchRings    int    // how near (grid-square rings) a report must be to the DX to count
	WindowMinutes int    // Layer-1 rolling-store TTL window (also stream backfill depth)
	KC2GEnable    bool   // Layer-2 MUF gate from KC2G
	KC2GURL       string // KC2G stations endpoint
	ModelEnable   bool   // Layer-3 propagation model (Phase 3; scaffold only)
}

// Defaults returns the baseline configuration (home grid/call match the brief).
func Defaults() Config {
	return Config{
		Listen:        "127.0.0.1:9970",
		HomeGrid:      "JO32we",
		StationCall:   "DL9ET",
		CtyPath:       "",
		HorstBaseURL:  "",
		AreaRings:     3, // ~3 squares ≈ 200–450 km around home
		MatchRings:    3, // how close a reception must be to the DX to count
		WindowMinutes: 30,
		KC2GEnable:    true,
		KC2GURL:       "https://prop.kc2g.com/api/stations.json",
		ModelEnable:   false,
	}
}

// FromEnv overlays HORSTPROP_* environment variables onto cfg and returns it.
// Flags should be applied by the caller after this (flags win).
func FromEnv(cfg Config) Config {
	if v := os.Getenv("HORSTPROP_LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := os.Getenv("HORSTPROP_HOME_GRID"); v != "" {
		cfg.HomeGrid = v
	}
	if v := os.Getenv("HORSTPROP_STATION_CALL"); v != "" {
		cfg.StationCall = v
	}
	if v := os.Getenv("HORSTPROP_CTY_PATH"); v != "" {
		cfg.CtyPath = v
	}
	if v := os.Getenv("HORSTPROP_HORST_URL"); v != "" {
		cfg.HorstBaseURL = v
	}
	if v := os.Getenv("HORSTPROP_AREA_RINGS"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
			cfg.AreaRings = n
		}
	}
	if v := os.Getenv("HORSTPROP_MATCH_RINGS"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
			cfg.MatchRings = n
		}
	}
	if v := os.Getenv("HORSTPROP_WINDOW_MINUTES"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			cfg.WindowMinutes = n
		}
	}
	if v := os.Getenv("HORSTPROP_KC2G_URL"); v != "" {
		cfg.KC2GURL = v
	}
	if v := os.Getenv("HORSTPROP_KC2G_ENABLE"); v != "" {
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			cfg.KC2GEnable = b
		}
	}
	return cfg
}

// FeedEnabled reports whether a propagation feed source is configured.
func (c Config) FeedEnabled() bool { return strings.TrimSpace(c.HorstBaseURL) != "" }

// StreamURL composes the HorstReporter SSE URL for the area-of-interest feed:
// target = the home square, rings = the configured area size, minutes = the
// rolling window (stream caps at 60). Returns "" if no feed is configured.
func (c Config) StreamURL() string {
	if !c.FeedEnabled() {
		return ""
	}
	target := strings.ToUpper(strings.TrimSpace(c.HomeGrid))
	if len(target) > 4 {
		target = target[:4] // matcher keys on the 4-char square
	}
	mins := c.WindowMinutes
	if mins <= 0 {
		mins = 15
	}
	if mins > 60 {
		mins = 60
	}
	q := url.Values{}
	q.Set("target", target)
	q.Set("rings", strconv.Itoa(c.AreaRings))
	q.Set("minutes", strconv.Itoa(mins))
	return strings.TrimRight(c.HorstBaseURL, "/") + "/api/stream?" + q.Encode()
}
