package main_test

// End-to-end tests for push. It is the first command that writes, so these assert on
// what must never happen as much as on what should.

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func (f fixture) push(t *testing.T, extra ...string) (string, error) {
	t.Helper()
	args := append([]string{"push", "--claude-dir", f.claudeDir, "--archive", f.archive}, extra...)
	cmd := exec.Command(binary, args...)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestPushCopiesSessionsAndMemory(t *testing.T) {
	f := newFixture(t)

	out, err := f.push(t)
	if err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}

	transcript := filepath.Join(f.archive, "projects", "e--work-airos-frontend",
		"11111111-2222-3333-4444-555555555555.jsonl")
	if _, err := os.Stat(transcript); err != nil {
		t.Errorf("transcript was not copied: %v", err)
	}
	// Memory is small and equally unrecoverable, so it travels with the sessions.
	memory := filepath.Join(f.archive, "projects", "e--work-legacy-app", "memory", "MEMORY.md")
	if _, err := os.Stat(memory); err != nil {
		t.Errorf("memory was not copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.archive, "INDEX.md")); err != nil {
		t.Errorf("INDEX.md was not written: %v", err)
	}
}

func TestPushSecondRunCopiesNothing(t *testing.T) {
	f := newFixture(t)

	if out, err := f.push(t); err != nil {
		t.Fatalf("first push failed: %v\n%s", err, out)
	}
	out, err := f.push(t)
	if err != nil {
		t.Fatalf("second push failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "copied 0 file(s)") {
		t.Errorf("an unchanged archive must copy nothing on the second run, got:\n%s", out)
	}
}

// Simulate what Google Drive actually does: the copy reads back very slightly older
// than its source. An exact comparison re-uploads everything, forever - this shipped
// as a bug in the predecessor and re-sent 19 files every 30 minutes.
func TestPushToleratesRoundedDestinationTimestamps(t *testing.T) {
	f := newFixture(t)
	if out, err := f.push(t); err != nil {
		t.Fatalf("first push failed: %v\n%s", err, out)
	}

	copied := filepath.Join(f.archive, "projects", "e--work-airos-frontend",
		"11111111-2222-3333-4444-555555555555.jsonl")
	info, err := os.Stat(copied)
	if err != nil {
		t.Fatal(err)
	}
	rounded := info.ModTime().Add(-900 * time.Millisecond)
	if err := os.Chtimes(copied, rounded, rounded); err != nil {
		t.Fatal(err)
	}

	out, err := f.push(t)
	if err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "copied 0 file(s)") {
		t.Errorf("a rounded destination timestamp must not trigger a re-copy, got:\n%s", out)
	}
}

func TestPushWritesShardedManifestWithIdentity(t *testing.T) {
	f := newFixture(t)
	if out, err := f.push(t); err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}

	entries, err := os.ReadDir(filepath.Join(f.archive, "manifest"))
	if err != nil {
		t.Fatalf("no manifest directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one shard for this machine, got %d", len(entries))
	}

	raw, err := os.ReadFile(filepath.Join(f.archive, "manifest", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var shard struct {
		SchemaVersion int    `json:"schemaVersion"`
		Machine       string `json:"machine"`
		Projects      map[string]struct {
			Path   string `json:"path"`
			Leaf   string `json:"leaf"`
			Source string `json:"source"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(raw, &shard); err != nil {
		t.Fatal(err)
	}
	if shard.SchemaVersion != 1 {
		t.Errorf("schemaVersion = %d, want 1", shard.SchemaVersion)
	}
	if shard.Machine == "" {
		t.Error("a shard must name the machine that wrote it")
	}

	p, ok := shard.Projects["e--work-airos-frontend"]
	if !ok {
		t.Fatal("the project with transcripts is missing from the manifest")
	}
	if p.Path != `E:\work\airos-frontend` {
		t.Errorf("path = %q, want the cwd read from the transcript", p.Path)
	}
	if p.Source != "transcript" {
		t.Errorf("source = %q, want transcript", p.Source)
	}

	if _, err := os.Stat(filepath.Join(f.archive, "projects.json")); err != nil {
		t.Errorf("projects.json compatibility view missing: %v", err)
	}
}

// The gap doctor has reported since the first review: a bucket with memory but no
// transcript has no cwd to read, so identity falls back to ~/.claude.json.
func TestPushRecoversIdentityForMemoryOnlyBucket(t *testing.T) {
	f := newFixture(t)
	mustWrite(t, filepath.Join(f.claudeDir, ".claude.json"),
		`{"projects":{"E:\\work\\legacy-app":{"allowedTools":[]}}}`)

	if out, err := f.push(t); err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}

	entries, _ := os.ReadDir(filepath.Join(f.archive, "manifest"))
	if len(entries) == 0 {
		t.Fatal("no manifest shard written")
	}
	raw, err := os.ReadFile(filepath.Join(f.archive, "manifest", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var shard struct {
		Projects map[string]struct {
			Path   string `json:"path"`
			Source string `json:"source"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(raw, &shard); err != nil {
		t.Fatal(err)
	}

	p, ok := shard.Projects["e--work-legacy-app"]
	if !ok {
		t.Fatal("the memory-only bucket is still absent from the manifest")
	}
	if p.Path != `E:\work\legacy-app` {
		t.Errorf("path = %q, want the path recovered from .claude.json", p.Path)
	}
	if p.Source != "claude-json" {
		t.Errorf("source = %q, want claude-json", p.Source)
	}

	out, err := f.run(t, "doctor")
	if err != nil {
		t.Fatalf("doctor failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "no usable identity") {
		t.Errorf("the bucket should now be routable:\n%s", out)
	}
}

// .claude.json is read for project paths but must never be copied: a live session
// rewrites it on exit. .credentials.json must never be touched at all.
func TestPushNeverCopiesSecrets(t *testing.T) {
	f := newFixture(t)
	mustWrite(t, filepath.Join(f.claudeDir, ".claude.json"), `{"projects":{}}`)
	mustWrite(t, filepath.Join(f.claudeDir, ".credentials.json"), `{"token":"not-a-real-token"}`)

	if out, err := f.push(t); err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}

	for _, name := range []string{".claude.json", ".credentials.json"} {
		var found []string
		_ = filepath.WalkDir(f.archive, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && d.Name() == name {
				found = append(found, path)
			}
			return nil
		})
		if len(found) > 0 {
			t.Errorf("%s must never reach the archive, found at %v", name, found)
		}
	}
}

func TestPushDryRunWritesNothing(t *testing.T) {
	f := newFixture(t)

	out, err := f.push(t, "--dry-run")
	if err != nil {
		t.Fatalf("dry run failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "would copy") {
		t.Errorf("expected a dry-run report, got:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(f.archive, "INDEX.md")); err == nil {
		t.Error("a dry run must not write INDEX.md")
	}
	if _, err := os.Stat(filepath.Join(f.archive, "manifest")); err == nil {
		t.Error("a dry run must not write a manifest")
	}
}

// A SessionEnd hook that fails breaks the end of the user's session. This is the most
// important behaviour in the command.
func TestPushQuietNeverFails(t *testing.T) {
	f := newFixture(t)
	f.archive = filepath.Join(t.TempDir(), "not-mounted", "archive")

	out, err := f.push(t, "--quiet")
	if err != nil {
		t.Errorf("push --quiet must exit 0 even with an unreachable archive: %v\n%s", err, out)
	}

	// And it must leave a trace, because nobody is watching a hook run.
	raw, readErr := os.ReadFile(filepath.Join(f.claudeDir, "session-sync.log"))
	if readErr != nil {
		t.Fatalf("expected a log entry: %v", readErr)
	}
	if !strings.Contains(string(raw), "SKIP") {
		t.Errorf("the log should record why it skipped:\n%s", raw)
	}
}

// The sweep and the hook can fire at the same moment; two pushes must not interleave
// writes to the same manifest and index.
func TestPushRefusesWhenLocked(t *testing.T) {
	f := newFixture(t)
	mustWrite(t, filepath.Join(f.claudeDir, "session-sync.lock"), "99999 2026-09-01T00:00:00Z\n")

	out, err := f.push(t)
	if err == nil {
		t.Errorf("a held lock should stop a second push:\n%s", out)
	}
	if !strings.Contains(out, "lock held") {
		t.Errorf("expected a lock message, got:\n%s", out)
	}

	if out, err := f.push(t, "--quiet"); err != nil {
		t.Errorf("--quiet must still exit 0 when locked: %v\n%s", err, out)
	}
}

// stdout must carry only the result. Progress goes to stderr, so that piping the
// output into jq works without --quiet.
func TestPushJSON(t *testing.T) {
	f := newFixture(t)

	cmd := exec.Command(binary, "push", "--claude-dir", f.claudeDir, "--archive", f.archive, "--json")
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=")
	stdout, err := cmd.Output() // stderr deliberately excluded
	out := string(stdout)
	if err != nil {
		t.Fatalf("push --json failed: %v\n%s", err, out)
	}
	var res struct {
		Copied   int    `json:"copied"`
		Sessions int    `json:"sessions"`
		Projects int    `json:"projects"`
		Archive  string `json:"archive"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	if res.Copied == 0 || res.Sessions != 1 {
		t.Errorf("unexpected result: %+v", res)
	}
	if res.Archive == "" {
		t.Error("the report should name the archive it wrote to")
	}
}

// push renders the pages at the end, as the PowerShell implementation did. The
// wiring was previously verified only by watching a log line.
func TestPushRendersHTML(t *testing.T) {
	f := newFixture(t)

	if out, err := f.push(t); err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}

	index := filepath.Join(f.archive, "html", "index.html")
	if _, err := os.Stat(index); err != nil {
		t.Fatalf("push did not render the index: %v", err)
	}
	page := filepath.Join(f.archive, "html", "e--work-airos-frontend",
		"11111111-2222-3333-4444-555555555555.html")
	raw, err := os.ReadFile(page)
	if err != nil {
		t.Fatalf("push did not render the session page: %v", err)
	}
	if !strings.Contains(string(raw), "fix the login redirect") {
		t.Error("the rendered page is missing the conversation")
	}
}

func TestPushNoHTMLSkipsRendering(t *testing.T) {
	f := newFixture(t)

	if out, err := f.push(t, "--no-html"); err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(f.archive, "html")); err == nil {
		t.Error("--no-html should not write any pages")
	}
}
