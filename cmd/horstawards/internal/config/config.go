// Package config holds horstawards's runtime configuration and its defaults.
// Non-secret values come from flags (wired in main) with environment-variable
// fallbacks; secrets (Wavelog API key) come only from env / a repo-root .env —
// never flags — mirroring cmd/horstoperator-agent. No secrets are stored in the
// repo.
package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultWavelogURL matches the agent's default (DCLNext).
const DefaultWavelogURL = "https://log.dclnext.darc.de/index.php"

// Config is the resolved horstawards configuration.
type Config struct {
	Listen  string // wanted API listen address
	DataDir string // directory for the persisted snapshot store

	// Wavelog ADIF source (reuses the agent's read-only key/url convention).
	WavelogURL       string
	WavelogAPIKey    string // from env/.env only; never logged
	WavelogStationID string // optional station profile id (empty = server default)
	WavelogInterval  time.Duration

	// POTA hunted-parks CSV (the recommended POTA source): the operator exports
	// their hunted parks from POTA as CSV and points horstawards at the file.
	// Authoritative and simple; disabled when empty.
	POTAHuntedCSV string

	// POTA API source (optional; the public profile can't enumerate hunts, so this
	// only helps against an authenticated endpoint returning a full parks list).
	POTACall     string
	POTAToken    string // optional bearer token; from env only; never logged
	POTABaseURL  string
	POTAInterval time.Duration

	// StaleAfter marks the index degraded once the freshest snapshot is older
	// than this (the UI can then warn that "wanted" data may be out of date).
	StaleAfter time.Duration
}

// Defaults returns the baseline configuration.
func Defaults() Config {
	return Config{
		Listen:           "127.0.0.1:9956",
		DataDir:          "./horstawards-data",
		WavelogURL:       DefaultWavelogURL,
		WavelogStationID: "",
		WavelogInterval:  30 * time.Minute,
		POTAHuntedCSV:    "",
		POTACall:         "",
		POTAToken:        "",
		POTABaseURL:      "https://api.pota.app",
		POTAInterval:     6 * time.Hour,
		StaleAfter:       6 * time.Hour,
	}
}

// FromEnv overlays HORSTAWARDS_*/WAVELOG_*/POTA_* environment variables onto cfg
// and returns it. Flags should be applied by the caller after this (flags win
// for non-secrets). Secrets are env-only.
func FromEnv(cfg Config) Config {
	if v := os.Getenv("HORSTAWARDS_LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := os.Getenv("HORSTAWARDS_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("WAVELOG_URL"); v != "" {
		cfg.WavelogURL = strings.TrimSpace(v)
	}
	// Secret: env/.env only, never a flag.
	cfg.WavelogAPIKey = strings.TrimSpace(os.Getenv("WAVELOG_API_KEY"))
	if v := os.Getenv("WAVELOG_STATION_ID"); v != "" {
		cfg.WavelogStationID = strings.TrimSpace(v)
	}
	if d := envDuration("HORSTAWARDS_WAVELOG_INTERVAL"); d > 0 {
		cfg.WavelogInterval = d
	}
	if v := os.Getenv("POTA_HUNTED_CSV"); v != "" {
		cfg.POTAHuntedCSV = strings.TrimSpace(v)
	}
	if v := os.Getenv("POTA_CALLSIGN"); v != "" {
		cfg.POTACall = strings.ToUpper(strings.TrimSpace(v))
	}
	cfg.POTAToken = strings.TrimSpace(os.Getenv("POTA_TOKEN"))
	if v := os.Getenv("POTA_BASE_URL"); v != "" {
		cfg.POTABaseURL = strings.TrimSpace(v)
	}
	if d := envDuration("HORSTAWARDS_POTA_INTERVAL"); d > 0 {
		cfg.POTAInterval = d
	}
	if d := envDuration("HORSTAWARDS_STALE_AFTER"); d > 0 {
		cfg.StaleAfter = d
	}
	return cfg
}

func envDuration(key string) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return 0
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Minute
	}
	return 0
}

// WavelogEnabled reports whether the Wavelog ADIF source can run.
func (c Config) WavelogEnabled() bool {
	return strings.TrimSpace(c.WavelogAPIKey) != "" && strings.TrimSpace(c.WavelogURL) != ""
}

// POTACSVEnabled reports whether the POTA hunted-parks CSV source can run.
func (c Config) POTACSVEnabled() bool {
	return strings.TrimSpace(c.POTAHuntedCSV) != ""
}

// POTAEnabled reports whether the POTA API source can run.
func (c Config) POTAEnabled() bool {
	return strings.TrimSpace(c.POTACall) != "" && strings.TrimSpace(c.POTABaseURL) != ""
}
