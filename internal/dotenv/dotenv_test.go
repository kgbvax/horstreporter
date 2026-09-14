package dotenv

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadQuoteRoundTrip pins the loader half of the .env round-trip contract:
// values written by the agent's quoteEnvValue (surrounding double quotes,
// embedded quotes NOT backslash-escaped) must Load back to the original
// value. The loader strips only the surrounding quotes and never unescapes —
// quoteEnvValue relies on exactly that.
func TestLoadQuoteRoundTrip(t *testing.T) {
	values := []string{
		"plain",
		"with space",
		`Shack #1 "main"`,
		`say "hi"`,
		`ends with quote"`,
		`'single'`,
		"hash#inside",
	}
	for i, v := range values {
		key := "ROUND_TRIP_VAR"
		os.Unsetenv(key)
		path := filepath.Join(t.TempDir(), ".env")
		content := key + `="` + v + `"` + "\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		Load(path)
		if got := os.Getenv(key); got != v {
			t.Errorf("case %d: Load(%q) set %s=%q, want %q", i, content, key, got, v)
			continue
		}
		os.Unsetenv(key)
	}
}

// TestLoadEnvDoesNotOverride pins the set-only-if-unset rule (a systemd
// EnvironmentFile or exported var always wins over the .env).
func TestLoadEnvDoesNotOverride(t *testing.T) {
	key := "LOAD_NO_OVERRIDE_VAR"
	t.Setenv(key, "from-env")
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(key+"=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	Load(path)
	if got := os.Getenv(key); got != "from-env" {
		t.Errorf("%s = %q, want the pre-set env value", key, got)
	}
}
