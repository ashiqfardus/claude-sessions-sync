package claude

import (
	"os"
	"path/filepath"
	"testing"
)

// Every install writes a settings.json backup, and every upgrade is an install: one
// machine had thirteen.
func TestInstallKeepsOnlyRecentBackups(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(settings, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, stamp := range []string{"20260101-000000", "20260201-000000", "20260301-000000", "20260401-000000"} {
		if err := os.WriteFile(settings+".bak-"+stamp, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := InstallHook(settings, "/bin/claude-sessions", true); err != nil {
		t.Fatal(err)
	}
	left, _ := filepath.Glob(settings + ".bak-*")
	if len(left) != keepBackups {
		t.Fatalf("%d backup(s) left, want %d: %v", len(left), keepBackups, left)
	}
	for _, l := range left {
		if filepath.Base(l) == "settings.json.bak-20260101-000000" {
			t.Error("the oldest backup was kept instead of the newest")
		}
	}
}
