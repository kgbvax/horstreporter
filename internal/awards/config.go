// Package awards is the operator's local award-progress engine: it pulls the
// operator's log (Wavelog ADIF) and hunted-parks CSV, folds them into an in-memory
// slot index, and evaluates a spot's "wanted" award slots. It runs in-process
// inside the operator agent so the operator's personal log never leaves the local
// machine.
package awards

import (
	"strings"
	"time"
)

// DefaultWavelogURL matches the agent's default (DCLNext).
const DefaultWavelogURL = "https://log.dclnext.darc.de/index.php"

// Config is the resolved awards configuration. The agent populates it from its
// own env/flags (it already holds the Wavelog read key); secrets never go in
// flags.
type Config struct {
	DataDir string // directory for the persisted snapshot store

	// Wavelog ADIF source.
	WavelogURL       string
	WavelogAPIKey    string // never logged
	WavelogStationID string // required by DCLNext's get_contacts_adif
	WavelogInterval  time.Duration

	// POTA hunted-parks CSV (recommended POTA source): a local file exported from
	// POTA. Disabled when empty.
	POTAHuntedCSV string

	// POTA API source (optional; only useful against an authenticated full-list
	// endpoint — the public profile is counts-only).
	POTACall     string
	POTAToken    string // never logged
	POTABaseURL  string
	POTAInterval time.Duration

	// StaleAfter marks the index degraded once the freshest snapshot is older
	// than this.
	StaleAfter time.Duration
}

// Defaults returns the baseline configuration (non-secret intervals/URLs). The
// caller overlays Wavelog creds, station id, CSV path, and data dir.
func Defaults() Config {
	return Config{
		DataDir:         "./horstawards-data",
		WavelogURL:      DefaultWavelogURL,
		WavelogInterval: 30 * time.Minute,
		POTABaseURL:     "https://api.pota.app",
		POTAInterval:    6 * time.Hour,
		StaleAfter:      6 * time.Hour,
	}
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

// Enabled reports whether any source is configured (so the agent can skip the
// manager entirely when awards aren't set up).
func (c Config) Enabled() bool {
	return c.WavelogEnabled() || c.POTACSVEnabled() || c.POTAEnabled()
}
