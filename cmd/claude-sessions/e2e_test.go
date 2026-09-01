package main_test

// End-to-end tests: build the real binary and drive it against a synthetic Claude
// profile and archive. These are the tests that would have caught the two defects
// found in the PowerShell implementation, so they assert on those cases explicitly.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cs-e2e")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(dir, "claude-sessions")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}

	build := exec.Command("go", "build", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		os.Stderr.Write(out)
		os.RemoveAll(dir)
		panic("build failed: " + err.Error())
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// fixture builds a throwaway Claude profile and archive.
//
// It deliberately includes a bucket that holds memory but no transcript: that is the
// shape the PowerShell implementation silently drops from the manifest, leaving the
// project archived but impossible to place on another machine.
type fixture struct {
	claudeDir string
	archive   string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	f := fixture{
		claudeDir: filepath.Join(root, ".claude"),
		archive:   filepath.Join(root, "archive"),
	}

	projects := filepath.Join(f.claudeDir, "projects")
	mustMkdir(t, filepath.Join(projects, "e--work-airos-frontend"))
	mustWrite(t, filepath.Join(projects, "e--work-airos-frontend", "11111111-2222-3333-4444-555555555555.jsonl"),
		line(`{"type":"user","cwd":"E:\\work\\airos-frontend","sessionId":"11111111-2222-3333-4444-555555555555","timestamp":"2026-08-30T09:00:00.000Z","message":{"role":"user","content":"fix the login redirect"}}`)+
			line(`{"type":"assistant","timestamp":"2026-08-30T09:00:04.000Z","message":{"role":"assistant","content":[{"type":"text","text":"looking"}]}}`))

	// Memory but no transcript - the unroutable case.
	mustMkdir(t, filepath.Join(projects, "e--work-legacy-app", "memory"))
	mustWrite(t, filepath.Join(projects, "e--work-legacy-app", "memory", "MEMORY.md"), "- a note\n")

	mustWrite(t, filepath.Join(f.claudeDir, "settings.json"), `{"cleanupPeriodDays":3650}`)

	mustMkdir(t, filepath.Join(f.archive, "projects", "e--work-airos-frontend"))
	mustMkdir(t, filepath.Join(f.archive, "projects", "e--work-legacy-app"))
	mustWrite(t, filepath.Join(f.archive, "projects.json"), `{
      "e--work-airos-frontend": {
        "path": "E:\\work\\airos-frontend",
        "leaf": "airos-frontend",
        "remote": "https://github.com/example/airos-frontend.git",
        "machine": "OLD-BOX",
        "seen": "2026-08-30"
      }
    }`)

	return f
}

func (f fixture) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	full := append([]string{args[0], "--claude-dir", f.claudeDir}, args[1:]...)
	if args[0] == "doctor" {
		full = append(full, "--archive", f.archive)
	}
	cmd := exec.Command(binary, full...)
	// Make sure an env var on the developer's machine cannot steer the test.
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestLsFindsRealPathFromTranscript(t *testing.T) {
	f := newFixture(t)

	out, err := f.run(t, "ls")
	if err != nil {
		t.Fatalf("ls failed: %v\n%s", err, out)
	}

	// The point of the tool: the bucket name is "e--work-airos-frontend", but the
	// real path comes from the transcript's cwd, because a bucket name is lossy and
	// cannot be decoded back.
	if !strings.Contains(out, `E:\work\airos-frontend`) {
		t.Errorf("expected the real cwd in the listing, got:\n%s", out)
	}
	if !strings.Contains(out, "fix the login redirect") {
		t.Errorf("expected the first prompt in the listing, got:\n%s", out)
	}
	if !strings.Contains(out, "1 session(s).") {
		t.Errorf("expected exactly one session, got:\n%s", out)
	}
}

func TestLsJSONIsMachineReadable(t *testing.T) {
	f := newFixture(t)

	out, err := f.run(t, "ls", "--json")
	if err != nil {
		t.Fatalf("ls --json failed: %v\n%s", err, out)
	}

	var rows []struct {
		Bucket      string `json:"bucket"`
		Project     string `json:"project"`
		Session     string `json:"session"`
		FirstPrompt string `json:"firstPrompt"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].Project != `E:\work\airos-frontend` {
		t.Errorf("project = %q", rows[0].Project)
	}
	if rows[0].Session != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("session = %q", rows[0].Session)
	}
}

func TestLsProjectFilter(t *testing.T) {
	f := newFixture(t)

	out, _ := f.run(t, "ls", "--project", "airos")
	if !strings.Contains(out, "1 session(s).") {
		t.Errorf("filter should match, got:\n%s", out)
	}

	out, _ = f.run(t, "ls", "--project", "nothing-like-this")
	if !strings.Contains(out, "No sessions found") {
		t.Errorf("filter should exclude everything, got:\n%s", out)
	}
}

// The regression that matters: a bucket archived with memory but no transcript has no
// recoverable identity, so `import` could never place it. doctor must say so out
// loud rather than reporting a clean bill of health.
func TestDoctorReportsUnroutableBucket(t *testing.T) {
	f := newFixture(t)

	out, err := f.run(t, "doctor")
	if err != nil {
		t.Fatalf("doctor failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "e--work-legacy-app") {
		t.Errorf("the unroutable bucket must be named, got:\n%s", out)
	}
	if !strings.Contains(out, "no usable identity") {
		t.Errorf("expected an unroutable warning, got:\n%s", out)
	}
	if strings.Contains(out, "All checks passed") {
		t.Errorf("doctor must not pass while a bucket is unroutable:\n%s", out)
	}
}

func TestDoctorJSONShape(t *testing.T) {
	f := newFixture(t)

	out, err := f.run(t, "doctor", "--json")
	if err != nil {
		t.Fatalf("doctor --json failed: %v\n%s", err, out)
	}

	var checks []struct {
		Name   string `json:"name"`
		Level  string `json:"level"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(out), &checks); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}

	byName := map[string]string{}
	for _, c := range checks {
		byName[c.Name] = c.Level
	}
	for _, required := range []string{"claude root", "projects", "retention", "archive", "manifest"} {
		if _, ok := byName[required]; !ok {
			t.Errorf("doctor --json is missing the %q check", required)
		}
	}
	if byName["retention"] != "ok" {
		t.Errorf("retention should be ok at 3650 days, got %q", byName["retention"])
	}
	if byName["manifest"] != "warn" {
		t.Errorf("manifest should warn about the unroutable bucket, got %q", byName["manifest"])
	}
}

// A credentials file inside the archive means auth material is being synced to a
// cloud folder. That has to fail loudly, not warn.
func TestDoctorFailsOnSecretsInArchive(t *testing.T) {
	f := newFixture(t)
	mustWrite(t, filepath.Join(f.archive, ".credentials.json"), `{"token":"not-a-real-token"}`)

	out, err := f.run(t, "doctor")
	if err != nil {
		t.Fatalf("doctor failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, ".credentials.json is present in the archive") {
		t.Errorf("expected a secrets failure, got:\n%s", out)
	}
	if !strings.Contains(out, "1 failure(s)") {
		t.Errorf("secrets in the archive must be a failure, not a warning:\n%s", out)
	}
}

func TestDoctorFailsWhenArchiveMissing(t *testing.T) {
	f := newFixture(t)
	f.archive = filepath.Join(t.TempDir(), "not-mounted")

	out, err := f.run(t, "doctor")
	if err != nil {
		t.Fatalf("doctor should report, not crash: %v\n%s", err, out)
	}
	if !strings.Contains(out, "not reachable") {
		t.Errorf("expected an unreachable-archive failure, got:\n%s", out)
	}
}

func TestUnknownCommandExitsNonZero(t *testing.T) {
	cmd := exec.Command(binary, "definitely-not-a-command")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("an unknown command must exit non-zero")
	}
	if !strings.Contains(string(out), "unknown command") {
		t.Errorf("expected a usage message, got:\n%s", out)
	}
}

func line(s string) string { return s + "\n" }

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
