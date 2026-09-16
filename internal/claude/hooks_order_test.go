package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Install used to re-sort every key in settings.json alphabetically and escape
// "2>&1" as "2\u003e\u00261" - a whole-file diff for adding one hook.
func TestInstallHookKeepsKeyOrderAndText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	seed := "\xef\xbb\xbf" + `{
  "theme": "dark",
  "model": "opus[1m]",
  "cleanupPeriodDays": 3650,
  "big": 12345678901234567890,
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {
            "type": "command",
            "command": "rtk hook claude 2>&1"
          }
        ]
      }
    ]
  },
  "autoUpdatesChannel": "latest"
}
`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallHook(path, `C:\Users\me\.claude\bin\claude-sessions.exe`, true); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)

	order := []string{`"theme"`, `"model"`, `"cleanupPeriodDays"`, `"big"`, `"hooks"`, `"PreToolUse"`, `"SessionEnd"`, `"autoUpdatesChannel"`}
	last := -1
	for _, key := range order {
		i := strings.Index(got, key)
		if i < 0 || i < last {
			t.Fatalf("key order changed at %s:\n%s", key, got)
		}
		last = i
	}
	for _, want := range []string{
		`"command": "rtk hook claude 2>&1"`,
		`"big": 12345678901234567890`, // not rounded through float64
		`"command": "C:\\Users\\me\\.claude\\bin\\claude-sessions.exe"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}

	// Removing it again restores the original, BOM aside.
	if _, err := RemoveHook(path, true); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if string(raw) != strings.TrimPrefix(seed, "\xef\xbb\xbf") {
		t.Errorf("install + uninstall did not round-trip:\n%s", raw)
	}
}

// Matching "claude-sessions" anywhere deleted the user's own hooks - and a whole
// group at a time, taking unrelated hooks that shared it.
func TestHooksThatAreNotOursSurvive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	seed := `{"hooks":{"SessionEnd":[
	  {"hooks":[
	    {"type":"command","command":"/b/claude-sessions","args":["push","--quiet"]},
	    {"type":"command","command":"notify-send done"}
	  ]},
	  {"hooks":[{"type":"command","command":"/b/claude-sessions render --force"}]},
	  {"hooks":[{"type":"command","command":"~/dev/claude-sessions-sync/backup.sh"}]}
	]}}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	removed, err := RemoveHook(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed %d hooks, want 1", removed)
	}
	raw, _ := os.ReadFile(path)
	for _, survivor := range []string{"notify-send done", "render --force", "backup.sh"} {
		if !strings.Contains(string(raw), survivor) {
			t.Errorf("%q was removed:\n%s", survivor, raw)
		}
	}
	if strings.Contains(string(raw), `"push"`) {
		t.Errorf("our hook is still there:\n%s", raw)
	}
}

func TestIsArchiveHook(t *testing.T) {
	cases := []struct {
		command      string
		args         []string
		ours, legacy bool
	}{
		{`C:\Users\me\.claude\bin\claude-sessions.exe`, []string{"push", "--quiet"}, true, false},
		{`"C:\Program Files\cs\claude-sessions.exe" push --quiet`, nil, true, false},
		{"/home/me/.claude/bin/claude-sessions push --quiet", nil, true, false},
		{"claude-sessions push", nil, true, false},
		{"/b/claude-sessions", []string{"render"}, false, false},
		{"~/dev/claude-sessions-sync/push.sh", nil, false, false},
		{"my-claude-sessions push", nil, false, false},
		{"powershell.exe", []string{"-File", `C:\t\sync-claude-sessions.ps1`, "-Quiet"}, false, true},
	}
	for _, tc := range cases {
		ours, legacy := IsArchiveHook(tc.command, tc.args)
		if ours != tc.ours || legacy != tc.legacy {
			t.Errorf("IsArchiveHook(%q, %v) = %v, %v", tc.command, tc.args, ours, legacy)
		}
	}
}
