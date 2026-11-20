package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stopHooks(t *testing.T, doc map[string]any) []any {
	t.Helper()
	hooks, _ := doc["hooks"].(map[string]any)
	entries, _ := hooks["Stop"].([]any)
	return entries
}

// After every reply the current session is archived, so a machine that dies or is
// put away mid-session loses minutes, not up to half an hour.
func TestInstallHookAddsPerReplyBackupOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	for i := 0; i < 2; i++ {
		if _, err := InstallHook(path, "/bin/claude-sessions", true); err != nil {
			t.Fatal(err)
		}
	}
	entries := stopHooks(t, readSettings(t, path))
	if len(entries) != 1 {
		t.Fatalf("want one Stop hook after installing twice, got %d", len(entries))
	}
	if body := mustJSON(t, entries[0]); !strings.Contains(body, `"--from-hook"`) {
		t.Errorf("the Stop hook does not run the per-reply push: %s", body)
	}
}

func TestRemoveHookRemovesPerReplyBackupOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	seed := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/usr/local/bin/notify-me"}]}]}}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallHook(path, "/bin/claude-sessions", true); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveHook(path, true); err != nil {
		t.Fatal(err)
	}
	entries := stopHooks(t, readSettings(t, path))
	if len(entries) != 1 || !strings.Contains(mustJSON(t, entries[0]), "notify-me") {
		t.Errorf("uninstall left ours or removed theirs: %s", mustJSON(t, entries))
	}
}
