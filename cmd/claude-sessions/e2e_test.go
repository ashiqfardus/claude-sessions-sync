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
	// doctor must exit non-zero on a failure so it is usable from monitoring.
	if err == nil {
		t.Errorf("doctor must exit non-zero when checks fail:\n%s", out)
	}
	if !strings.Contains(out, ".credentials.json is present in the archive") {
		t.Errorf("expected a secrets failure, got:\n%s", out)
	}
	if !strings.Contains(out, "1 failure(s)") {
		t.Errorf("secrets in the archive must be a failure, not a warning:\n%s", out)
	}
}

// A root-only check would miss this: a naive recursive copy puts the credentials
// file inside a bucket, not at the archive root.
func TestDoctorFindsSecretsNestedInArchive(t *testing.T) {
	f := newFixture(t)
	mustWrite(t, filepath.Join(f.archive, "projects", "e--work-airos-frontend", ".credentials.json"),
		`{"token":"not-a-real-token"}`)

	out, err := f.run(t, "doctor")
	if err == nil {
		t.Errorf("nested secrets must still fail the run:\n%s", out)
	}
	if !strings.Contains(out, ".credentials.json is present in the archive") {
		t.Errorf("expected the nested secret to be found, got:\n%s", out)
	}
}

func TestDoctorFailsWhenArchiveMissing(t *testing.T) {
	f := newFixture(t)
	f.archive = filepath.Join(t.TempDir(), "not-mounted")

	out, err := f.run(t, "doctor")
	if err == nil {
		t.Errorf("an unreachable archive must exit non-zero:\n%s", out)
	}
	if !strings.Contains(out, "not reachable") {
		t.Errorf("expected an unreachable-archive report, got:\n%s", out)
	}
	// It must still be a readable report rather than a crash.
	if !strings.Contains(out, "claude root") {
		t.Errorf("expected the full report before the failure, got:\n%s", out)
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

// --- the commands added after the multi-hat review --------------------------

func TestSearchFindsTextAcrossSessions(t *testing.T) {
	f := newFixture(t)

	out, err := f.run(t, "search", "login redirect")
	if err != nil {
		t.Fatalf("search failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "1 match(es)") {
		t.Errorf("expected one match, got:\n%s", out)
	}
	if !strings.Contains(out, "claude --resume 11111111-2222-3333-4444-555555555555") {
		t.Errorf("search should print how to resume the session, got:\n%s", out)
	}
}

func TestSearchNoMatch(t *testing.T) {
	f := newFixture(t)

	out, err := f.run(t, "search", "nothing whatsoever like this")
	if err != nil {
		t.Fatalf("a miss is not an error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "No matches") {
		t.Errorf("expected a no-match message, got:\n%s", out)
	}
}

func TestSearchRoleFilter(t *testing.T) {
	f := newFixture(t)

	// "looking" appears only in the assistant's reply.
	out, _ := f.run(t, "search", "--role", "user", "looking")
	if !strings.Contains(out, "No matches") {
		t.Errorf("--role user must exclude assistant text, got:\n%s", out)
	}
	out, _ = f.run(t, "search", "--role", "assistant", "looking")
	if !strings.Contains(out, "1 match(es)") {
		t.Errorf("--role assistant should match, got:\n%s", out)
	}
}

func TestStatsCountsProjects(t *testing.T) {
	f := newFixture(t)

	out, err := f.run(t, "stats")
	if err != nil {
		t.Fatalf("stats failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "1 session(s) across 1 project(s)") {
		t.Errorf("unexpected totals:\n%s", out)
	}
	if !strings.Contains(out, "1 bucket(s) hold memory but no transcripts") {
		t.Errorf("the memory-only bucket should be reported:\n%s", out)
	}
}

// config is what makes doctor's advice actionable: without it there is no way to
// persist a destination.
func TestConfigSetAndShowDestination(t *testing.T) {
	f := newFixture(t)
	target := filepath.Join(t.TempDir(), "my-archive")

	out, err := f.run(t, "config", "set-destination", target)
	if err != nil {
		t.Fatalf("set-destination failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Destination set to") {
		t.Errorf("unexpected output:\n%s", out)
	}

	out, err = f.run(t, "config")
	if err != nil {
		t.Fatalf("config show failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, target) {
		t.Errorf("the saved destination should be shown, got:\n%s", out)
	}
}

// Creating the parent silently is how someone ends up believing they have a cloud
// backup when the sync client was never mounted.
func TestConfigRefusesMissingParent(t *testing.T) {
	f := newFixture(t)
	target := filepath.Join(t.TempDir(), "not-mounted", "archive")

	out, err := f.run(t, "config", "set-destination", target)
	if err == nil {
		t.Errorf("expected a refusal when the parent does not exist:\n%s", out)
	}
	if !strings.Contains(out, "is the sync client mounted") {
		t.Errorf("expected an explanation, got:\n%s", out)
	}
}

func TestCompletionScripts(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		out, err := exec.Command(binary, "completion", shell).CombinedOutput()
		if err != nil {
			t.Errorf("completion %s failed: %v", shell, err)
		}
		if !strings.Contains(string(out), "claude-sessions") {
			t.Errorf("completion %s produced nothing usable:\n%s", shell, out)
		}
	}
	if _, err := exec.Command(binary, "completion").CombinedOutput(); err == nil {
		t.Error("completion with no shell must exit non-zero")
	}
}

func TestVersionAcceptsLowercaseV(t *testing.T) {
	for _, flag := range []string{"version", "--version", "-v", "-V"} {
		out, err := exec.Command(binary, flag).CombinedOutput()
		if err != nil {
			t.Errorf("%s failed: %v", flag, err)
		}
		if !strings.Contains(string(out), "claude-sessions") {
			t.Errorf("%s printed %q", flag, out)
		}
	}
}

func TestLsSinceUntilFilters(t *testing.T) {
	f := newFixture(t)

	out, _ := f.run(t, "ls", "--since", "2099-01-01")
	if !strings.Contains(out, "No sessions found") {
		t.Errorf("--since in the future should exclude everything:\n%s", out)
	}
	out, _ = f.run(t, "ls", "--since", "2000-01-01")
	if !strings.Contains(out, "1 session(s).") {
		t.Errorf("--since in the past should include everything:\n%s", out)
	}
	out, err := f.run(t, "ls", "--since", "not-a-date")
	if err == nil {
		t.Errorf("a malformed date must be rejected:\n%s", out)
	}
}
