package main_test

// The home folder on one machine is the home folder on another, even when the paths
// share nothing (#4): C:\Users\alice on Windows, /Users/alice or /home/bob elsewhere.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
)

// runAsHome runs the binary with home pointing at dir, on every OS.
func (f importFixture) runAsHome(t *testing.T, home string, args ...string) (string, error) {
	t.Helper()
	full := append([]string{args[0], "--claude-dir", f.claudeDir, "--archive", f.archive}, args[1:]...)
	cmd := exec.Command(binary, full...)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=", "HOME="+home, "USERPROFILE="+home)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// writeShard writes WIN-BOX's shard; home is the home folder it recorded, "" for none.
func (f importFixture) writeShard(t *testing.T, claudeRoot, home string, projects map[string]any) {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"schemaVersion": 1, "machine": "WIN-BOX", "claudeRoot": claudeRoot, "home": home, "projects": projects,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(f.archive, "manifest", "WIN-BOX.json"), string(data))
}

const homeSession = "44444444-4444-4444-4444-444444444444"

func TestImportFilesAnotherMachinesHomeIntoThisHome(t *testing.T) {
	f := newImportFixture(t)
	home := filepath.Dir(f.claudeDir) // this machine's home holds its .claude

	f.archiveProject(t, "C--Users-alice", `C:\Users\alice`, homeSession, "what is in my downloads")
	mustWrite(t, filepath.Join(f.archive, "projects", "C--Users-alice", "memory", "MEMORY.md"), "- a note\n")
	f.writeShard(t, `C:\Users\alice\.claude`, `C:\Users\alice`, map[string]any{
		"C--Users-alice": map[string]any{"path": `C:\Users\alice`, "leaf": "alice", "seen": "2026-08-30"},
	})

	out, err := f.runAsHome(t, home, "import")
	if err != nil {
		t.Fatalf("import failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "matched by home folder") {
		t.Errorf("expected the home-folder rung to be named:\n%s", out)
	}

	bucket := claude.Slug(home)
	got := f.localBucket(t, bucket)
	if len(got) == 0 || !contains(got, homeSession+".jsonl") {
		t.Fatalf("home session not filed under %s; bucket holds %v\n%s", bucket, got, out)
	}
	if _, err := os.Stat(filepath.Join(f.claudeDir, "projects", bucket, "memory", "MEMORY.md")); err != nil {
		t.Errorf("home memory not filed: %v\n%s", err, out)
	}
	body, _ := os.ReadFile(filepath.Join(f.claudeDir, "projects", bucket, homeSession+".jsonl"))
	if strings.Contains(string(body), `C:\\Users\\alice`) {
		t.Errorf("the old home path was not rewritten:\n%s", body)
	}
}

// Another machine's Desktop is this home's, however many folders named Desktop there
// are here (Windows has one per profile). The shard's home is written with the other
// separator and case to show neither matters.
func TestImportFilesAnotherMachinesDesktopIntoThisHomes(t *testing.T) {
	f := newImportFixture(t)
	home := filepath.Dir(f.claudeDir)
	desktop := filepath.Join(home, "Desktop")
	mustMkdir(t, desktop)
	mustMkdir(t, filepath.Join(home, "dev", "Desktop")) // a second one: ambiguous by name

	f.archiveProject(t, "C--Users-alice-Desktop", `C:\Users\alice\Desktop`, homeSession, "tidy my desktop")
	f.writeShard(t, `C:\Users\alice\.claude`, `c:/users/Alice/`, map[string]any{
		"C--Users-alice-Desktop": map[string]any{"path": `C:\Users\alice\Desktop`, "leaf": "Desktop", "seen": "2026-08-30"},
	})

	out, err := f.runAsHome(t, home, "import", "--search-root", home)
	if err != nil {
		t.Fatalf("import failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "-> "+desktop+" (matched by home folder)") {
		t.Fatalf("not filed for this home's Desktop:\n%s", out)
	}
	if got := f.localBucket(t, claude.Slug(desktop)); !contains(got, homeSession+".jsonl") {
		t.Fatalf("bucket holds %v\n%s", got, out)
	}
}

// Same username on two Macs: the recorded path exists here, which is the stronger
// answer and must stay the one used.
func TestImportSameHomePathStillMatchesByRecordedPath(t *testing.T) {
	f := newImportFixture(t)
	home := filepath.Dir(f.claudeDir)

	f.archiveProject(t, claude.Slug(home), home, homeSession, "hello")
	f.writeShard(t, filepath.Join(home, ".claude"), home, map[string]any{
		claude.Slug(home): map[string]any{"path": home, "leaf": filepath.Base(home), "seen": "2026-08-30"},
	})

	out, err := f.runAsHome(t, home, "import")
	if err != nil {
		t.Fatalf("import failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "matched by recorded path") {
		t.Errorf("expected the recorded path to win:\n%s", out)
	}
}

// A profile outside <home>/.claude (CLAUDE_CONFIG_DIR) says nothing about where that
// machine's home was, so nothing is inferred from it.
func TestImportDoesNotInferHomeFromACustomProfile(t *testing.T) {
	f := newImportFixture(t)
	home := filepath.Dir(f.claudeDir)

	f.archiveProject(t, "C--Users-alice", `C:\Users\alice`, homeSession, "hello")
	f.writeShard(t, `D:\profiles\work`, "", map[string]any{
		"C--Users-alice": map[string]any{"path": `C:\Users\alice`, "leaf": "alice", "seen": "2026-08-30"},
	})

	out, err := f.runAsHome(t, home, "import")
	if err != nil {
		t.Fatalf("import failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "home folder") || len(f.localBucket(t, claude.Slug(home))) > 0 {
		t.Errorf("home was inferred from a custom profile:\n%s", out)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
