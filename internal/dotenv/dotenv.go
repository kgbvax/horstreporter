// Package dotenv loads KEY=VALUE pairs from a .env file into the process
// environment. It is shared by the operator-side binaries (horstoperator-agent,
// horstawards) so secrets like WAVELOG_API_KEY stay out of shell history and
// process args. Values are set only if not already present, so an explicit
// environment (e.g. systemd EnvironmentFile) always wins.
package dotenv

import (
	"os"
	"strings"
)

// Load reads KEY=VALUE lines from path (if it exists; a missing file is a no-op).
// Comments (#) and blank lines are ignored, an optional leading "export " is
// stripped, and matching surrounding single/double quotes are removed. A key is
// set only when it is not already present in the environment.
func Load(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		}
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, val)
		}
	}
}
