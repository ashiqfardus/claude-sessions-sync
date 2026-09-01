package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("settings.json no longer parses: %v\n%s", err, raw)
	}
	return doc
}

func sessionEnd(t *testing.T, doc map[string]any) []any {
	t.Helper()
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		return nil
	}
	entries, _ := hooks["SessionEnd"].([]any)
	return entries
}

// settings.json belongs to the user. It may hold formatter hooks, permissions, model
// settings and keys this program has never heard of, and every one of them must
// survive an install untouched.
func TestInstallHookPreservesEverythingElse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	seed := `{
      "model": "opus",
      "cleanupPeriodDays": 3650,
      "permissions": {"allow": ["Bash(npm test)"]},
      "someFutureKey": {"nested": [1, 2, 3]},
      "hooks": {
        "PostToolUse": [
          {"matcher": "Edit", "hooks": [{"type": "command", "command": "prettier --write"}]}
        ],
        "SessionEnd": [
          {"hooks": [{"type": "command", "command": "/usr/local/bin/someone-elses-tool"}]}
        ]
      }
    }`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	replaced, err := InstallHook(path, "/home/me/.claude/bin/claude-sessions", true)
	if err != nil {
		t.Fatal(err)
	}
	if replaced != 0 {
		t.Errorf("nothing of ours was installed before, got replaced=%d", replaced)
	}

	doc := readSettings(t, path)
	if doc["model"] != "opus" {
		t.Error("model was lost")
	}
	if doc["cleanupPeriodDays"].(float64) != 3650 {
		t.Error("cleanupPeriodDays was lost")
	}
	if _, ok := doc["permissions"]; !ok {
		t.Error("permissions were lost")
	}
	if _, ok := doc["someFutureKey"]; !ok {
		t.Error("an unknown key was lost - settings.json must round-trip")
	}

	hooks := doc["hooks"].(map[string]any)
	if _, ok := hooks["PostToolUse"]; !ok {
		t.Error("another event's hooks were lost")
	}

	entries := sessionEnd(t, doc)
	if len(entries) != 2 {
		t.Fatalf("expected the foreign hook plus ours, got %d", len(entries))
	}
	if !strings.Contains(mustJSON(t, entries[0]), "someone-elses-tool") {
		t.Error("another person's SessionEnd hook was removed")
	}
	if !strings.Contains(mustJSON(t, entries[1]), "claude-sessions") {
		t.Error("our hook was not added")
	}

	// A backup must exist, because this edits a file the user owns.
	found := false
	files, _ := os.ReadDir(dir)
	for _, f := range files {
		if strings.Contains(f.Name(), "settings.json.bak-") {
			found = true
		}
	}
	if !found {
		t.Error("no backup of settings.json was written")
	}
}

// Re-running must replace our own entry, not stack up a second one that runs the same
// work twice at the end of every session.
func TestInstallHookIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")

	if _, err := InstallHook(path, "/bin/claude-sessions", true); err != nil {
		t.Fatal(err)
	}
	replaced, err := InstallHook(path, "/bin/claude-sessions", true)
	if err != nil {
		t.Fatal(err)
	}
	if replaced != 1 {
		t.Errorf("second install should replace our entry, got replaced=%d", replaced)
	}
	if entries := sessionEnd(t, readSettings(t, path)); len(entries) != 1 {
		t.Errorf("expected exactly one hook, got %d", len(entries))
	}
}

// Installing over the PowerShell predecessor must remove it, or both archivers run at
// the end of every session and race on the manifest.
func TestInstallHookReplacesPowerShellPredecessor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	seed := `{"hooks":{"SessionEnd":[
      {"hooks":[{"type":"command","command":"powershell.exe",
        "args":["-File","C:\\tools\\sync-claude-sessions.ps1","-Quiet"]}]}
    ]}}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	replaced, err := InstallHook(path, "/bin/claude-sessions", true)
	if err != nil {
		t.Fatal(err)
	}
	if replaced != 1 {
		t.Errorf("expected the PowerShell hook to be replaced, got %d", replaced)
	}
	body := mustJSON(t, sessionEnd(t, readSettings(t, path)))
	if strings.Contains(body, "sync-claude-sessions.ps1") {
		t.Errorf("the PowerShell hook survived:\n%s", body)
	}

	// And with the flag set it must be left alone.
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallHook(path, "/bin/claude-sessions", false); err != nil {
		t.Fatal(err)
	}
	body = mustJSON(t, sessionEnd(t, readSettings(t, path)))
	if !strings.Contains(body, "sync-claude-sessions.ps1") {
		t.Errorf("--keep-powershell-hook should have preserved it:\n%s", body)
	}
}

func TestRemoveHookLeavesOthersAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	seed := `{"model":"opus","hooks":{"SessionEnd":[
      {"hooks":[{"type":"command","command":"/usr/local/bin/someone-elses-tool"}]}
    ]}}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallHook(path, "/bin/claude-sessions", true); err != nil {
		t.Fatal(err)
	}

	removed, err := RemoveHook(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("expected to remove exactly ours, got %d", removed)
	}

	doc := readSettings(t, path)
	if doc["model"] != "opus" {
		t.Error("uninstall damaged unrelated settings")
	}
	entries := sessionEnd(t, doc)
	if len(entries) != 1 || !strings.Contains(mustJSON(t, entries[0]), "someone-elses-tool") {
		t.Errorf("uninstall removed the wrong hook: %s", mustJSON(t, entries))
	}
}

// Removing the only hook should leave no empty scaffolding behind.
func TestRemoveHookCleansUpEmptyStructures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if _, err := InstallHook(path, "/bin/claude-sessions", true); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveHook(path, true); err != nil {
		t.Fatal(err)
	}
	doc := readSettings(t, path)
	if _, ok := doc["hooks"]; ok {
		t.Errorf("an empty hooks object was left behind: %v", doc)
	}
}

// Refusing to touch a file that does not parse is the safe behaviour: rewriting it
// from a partial read would destroy settings the user cannot get back.
func TestInstallHookRefusesUnparseableSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"hooks": {oh dear`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallHook(path, "/bin/claude-sessions", true); err == nil {
		t.Error("expected a refusal rather than an overwrite")
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "oh dear") {
		t.Error("the unparseable file was modified anyway")
	}
}

func TestInstallHookOnMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if _, err := InstallHook(path, "/bin/claude-sessions", true); err != nil {
		t.Fatalf("a machine with no settings.json is a normal starting state: %v", err)
	}
	if entries := sessionEnd(t, readSettings(t, path)); len(entries) != 1 {
		t.Errorf("expected our hook, got %d entries", len(entries))
	}
}

// The installed hook must be readable by the same code doctor uses to report it.
func TestInstalledHookIsRecognised(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if _, err := InstallHook(path, "/home/me/.claude/bin/claude-sessions", true); err != nil {
		t.Fatal(err)
	}
	settings, found, err := LoadSettings(path)
	if err != nil || !found {
		t.Fatalf("settings did not load: %v", err)
	}
	hooks := settings.SessionEndHooks(HookNeedle)
	if len(hooks) != 1 {
		t.Fatalf("doctor would not see the installed hook: %d found", len(hooks))
	}
	if hooks[0].Command == "" || len(hooks[0].Args) != 2 {
		t.Errorf("hook shape is wrong: %+v", hooks[0])
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
