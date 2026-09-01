package main_test

// Tests for behaviour that only shows up with more than one machine, or with a
// destination pointed somewhere it should not be.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
)

// INDEX.md is one file at the archive root. Building it from only the local machine's
// sessions means every push erases the other machines' listings - the same defect the
// sharded manifest exists to prevent, left in the index by the first version of push.
func TestIndexKeepsOtherMachinesSessions(t *testing.T) {
	f := newFixture(t)

	// A shard as another machine would have left it.
	other := archive.Shard{
		SchemaVersion: 1,
		Machine:       "MACBOOK",
		Projects: map[string]archive.Project{
			"-Users-me-dev-web": {Path: "/Users/me/dev/web", Leaf: "web", Seen: "2026-08-30"},
		},
		Sessions: []archive.Session{{
			Bucket: "-Users-me-dev-web", Project: "/Users/me/dev/web",
			ID:      "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			Updated: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
			Size:    2048, Prompt: "rename the payments module",
		}},
	}
	data, err := json.Marshal(other)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(f.archive, "manifest", "MACBOOK.json"), string(data))

	if out, err := f.push(t); err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}

	index, err := os.ReadFile(filepath.Join(f.archive, "INDEX.md"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(index)

	if !strings.Contains(got, "rename the payments module") {
		t.Errorf("pushing from this machine erased the other machine's sessions:\n%s", got)
	}
	if !strings.Contains(got, "/Users/me/dev/web") {
		t.Errorf("the other machine's project is missing from the index:\n%s", got)
	}
	if !strings.Contains(got, "fix the login redirect") {
		t.Errorf("this machine's own sessions are missing:\n%s", got)
	}
	// With more than one contributor the index should say which machine each came from.
	if !strings.Contains(got, "MACBOOK") {
		t.Errorf("expected machine attribution in a multi-machine index:\n%s", got)
	}
}

func TestPushDoesNotDisturbAnotherMachinesShard(t *testing.T) {
	f := newFixture(t)
	otherPath := filepath.Join(f.archive, "manifest", "MACBOOK.json")
	mustWrite(t, otherPath,
		`{"schemaVersion":1,"machine":"MACBOOK","projects":{"-Users-me-dev-web":{"path":"/Users/me/dev/web","leaf":"web","seen":"2026-08-30"}}}`)
	before, err := os.ReadFile(otherPath)
	if err != nil {
		t.Fatal(err)
	}

	if out, err := f.push(t); err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}

	after, err := os.ReadFile(otherPath)
	if err != nil {
		t.Fatalf("another machine's shard disappeared: %v", err)
	}
	if string(before) != string(after) {
		t.Error("a push must never write another machine's shard")
	}

	// And the compatibility view must still carry both.
	merged, _, err := archive.Merged(f.archive)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := merged["-Users-me-dev-web"]; !ok {
		t.Error("the other machine's project vanished from the merged view")
	}
	if _, ok := merged["e--work-airos-frontend"]; !ok {
		t.Error("this machine's project is missing from the merged view")
	}
}

// Pointing the archive inside ~/.claude makes every push write into the directory it
// reads, so the next push copies its own output, forever.
func TestRefusesDestinationInsideClaudeDir(t *testing.T) {
	f := newFixture(t)
	inside := filepath.Join(f.claudeDir, "archive")

	out, err := f.push(t, "--archive", inside)
	if err == nil {
		t.Errorf("push should refuse an archive inside the Claude directory:\n%s", out)
	}
	if !strings.Contains(out, "copy into its own source") {
		t.Errorf("expected an explanation, got:\n%s", out)
	}

	// config must refuse it too, and must not create the folder on the way to
	// refusing.
	out, err = f.run(t, "config", "set-destination", inside)
	if err == nil {
		t.Errorf("config should refuse it as well:\n%s", out)
	}
	if _, statErr := os.Stat(inside); statErr == nil {
		t.Error("a rejected destination must not be created")
	}
}

// "sync-claude-sessions.ps1" contains "claude-sessions", so a substring match counts
// the PowerShell script as this binary too and reports two archivers when one is
// installed. Confirmed as a false positive against a real settings.json.
func TestDoctorDoesNotMistakePowerShellHookForTwoArchivers(t *testing.T) {
	f := newFixture(t)
	mustWrite(t, filepath.Join(f.claudeDir, "settings.json"), `{
      "cleanupPeriodDays": 3650,
      "hooks": {
        "SessionEnd": [
          {"hooks": [{"type": "command", "command": "powershell.exe",
            "args": ["-File", "C:\\Users\\me\\.claude\\tools\\sync-claude-sessions.ps1", "-Quiet"]}]}
        ]
      }
    }`)

	out, err := f.run(t, "doctor")
	if err != nil {
		t.Fatalf("doctor failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "two archivers") {
		t.Errorf("only the PowerShell hook is installed; this is a false positive:\n%s", out)
	}
	if !strings.Contains(out, "sync-claude-sessions.ps1") {
		t.Errorf("the installed hook should still be reported:\n%s", out)
	}
}

func TestDoctorDetectsGenuineDuplicateArchivers(t *testing.T) {
	f := newFixture(t)
	mustWrite(t, filepath.Join(f.claudeDir, "settings.json"), `{
      "cleanupPeriodDays": 3650,
      "hooks": {
        "SessionEnd": [
          {"hooks": [{"type": "command", "command": "powershell.exe",
            "args": ["-File", "C:\\tools\\sync-claude-sessions.ps1", "-Quiet"]}]},
          {"hooks": [{"type": "command", "command": "claude-sessions",
            "args": ["push", "--quiet"]}]}
        ]
      }
    }`)

	out, err := f.run(t, "doctor")
	if err != nil {
		t.Fatalf("doctor failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "two archivers") {
		t.Errorf("both archivers really are installed here:\n%s", out)
	}
}
