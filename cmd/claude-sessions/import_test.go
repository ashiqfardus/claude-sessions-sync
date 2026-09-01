package main_test

// Tests for the project's whole reason to exist: filing an archive onto a machine
// where the projects live at different paths.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// importFixture is a machine that has an archive but none of the local buckets, as a
// freshly-set-up laptop would.
type importFixture struct {
	claudeDir string
	archive   string
	dev       string // where projects live on THIS machine
}

func newImportFixture(t *testing.T) importFixture {
	t.Helper()
	root := t.TempDir()
	f := importFixture{
		claudeDir: filepath.Join(root, ".claude"),
		archive:   filepath.Join(root, "archive"),
		dev:       filepath.Join(root, "dev"),
	}
	mustMkdir(t, filepath.Join(f.claudeDir, "projects"))
	mustMkdir(t, f.dev)
	return f
}

// archiveProject writes a transcript into the archive as another machine left it.
func (f importFixture) archiveProject(t *testing.T, bucket, recordedPath, sessionID, prompt string) {
	t.Helper()
	escaped, err := json.Marshal(recordedPath)
	if err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","cwd":` + string(escaped) +
		`,"sessionId":"` + sessionID + `","timestamp":"2026-08-30T09:00:00.000Z",` +
		`"message":{"role":"user","content":"` + prompt + `"}}` + "\n"
	mustWrite(t, filepath.Join(f.archive, "projects", bucket, sessionID+".jsonl"), line)
}

func (f importFixture) writeManifest(t *testing.T, shard map[string]any) {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"schemaVersion": 1,
		"machine":       "OLD-BOX",
		"projects":      shard,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(f.archive, "manifest", "OLD-BOX.json"), string(data))
}

// makeRepo creates a folder with a git remote, without needing git installed.
func (f importFixture) makeRepo(t *testing.T, name, remote string) string {
	t.Helper()
	dir := filepath.Join(f.dev, name)
	mustMkdir(t, dir)
	if remote != "" {
		mustWrite(t, filepath.Join(dir, ".git", "config"),
			"[core]\n\tbare = false\n[remote \"origin\"]\n\turl = "+remote+"\n")
	}
	return dir
}

func (f importFixture) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	full := append([]string{args[0], "--claude-dir", f.claudeDir, "--archive", f.archive}, args[1:]...)
	cmd := exec.Command(binary, full...)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (f importFixture) localBucket(t *testing.T, name string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.claudeDir, "projects", name))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// The headline case: the same repository checked out at a different path, identified
// by its git remote and filed under the local bucket with its paths rewritten.
func TestImportMatchesByGitRemote(t *testing.T) {
	f := newImportFixture(t)
	local := f.makeRepo(t, "api", "git@github.com:example/api.git")

	f.archiveProject(t, "D--work-api", `D:\work\api`, "11111111-1111-1111-1111-111111111111", "fix the retry logic")
	f.writeManifest(t, map[string]any{
		"D--work-api": map[string]any{
			"path": `D:\work\api`, "leaf": "api",
			// A different URL form for the same repository - normalisation must match.
			"remote": "https://github.com/example/api.git",
			"seen":   "2026-08-30",
		},
	})

	out, err := f.run(t, "import", "--search-root", f.dev)
	if err != nil {
		t.Fatalf("import failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "matched by git remote") {
		t.Errorf("expected a git remote match, got:\n%s", out)
	}

	files := f.localBucket(t, slugOf(local))
	if len(files) != 1 {
		t.Fatalf("expected the transcript in the local bucket, got %v (out:\n%s)", files, out)
	}

	// And the recorded path inside must now point at this machine's folder.
	raw, err := os.ReadFile(filepath.Join(f.claudeDir, "projects", slugOf(local), files[0]))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if strings.Contains(body, `D:\\work\\api`) {
		t.Error("the old project path is still recorded in the transcript")
	}
	if !strings.Contains(body, jsonEscape(local)) {
		t.Errorf("the transcript does not point at the local path:\n%s", body)
	}
	// It must still be valid JSON, or Claude Code cannot read it.
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatalf("rewrite produced invalid JSON:\n%s", line)
		}
	}
}

// Two folders with the same name and nothing to tell them apart must be reported, not
// guessed at: filing a transcript under the wrong project is silent and unfindable.
func TestImportRefusesToGuessBetweenSameNamedFolders(t *testing.T) {
	f := newImportFixture(t)
	f.makeRepo(t, "api", "")
	mustMkdir(t, filepath.Join(f.dev, "nested"))
	mustMkdir(t, filepath.Join(f.dev, "nested", "api"))

	f.archiveProject(t, "D--work-api", `D:\work\api`, "22222222-2222-2222-2222-222222222222", "hello")
	f.writeManifest(t, map[string]any{
		"D--work-api": map[string]any{"path": `D:\work\api`, "leaf": "api", "remote": "", "seen": "2026-08-30"},
	})

	out, err := f.run(t, "import", "--search-root", f.dev)
	if err != nil {
		t.Fatalf("import failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "ambiguous") && !strings.Contains(out, "no git remote settles it") {
		t.Errorf("expected an ambiguity report, got:\n%s", out)
	}
	if strings.Contains(out, "filed: 1") {
		t.Errorf("nothing should have been filed:\n%s", out)
	}
}

// A folder of the right name that is a different repository is not a match.
func TestImportRejectsSameNameDifferentRepo(t *testing.T) {
	f := newImportFixture(t)
	f.makeRepo(t, "api", "git@github.com:someone-else/api.git")

	f.archiveProject(t, "D--work-api", `D:\work\api`, "33333333-3333-3333-3333-333333333333", "hello")
	f.writeManifest(t, map[string]any{
		"D--work-api": map[string]any{
			"path": `D:\work\api`, "leaf": "api",
			"remote": "https://github.com/example/api.git", "seen": "2026-08-30",
		},
	})

	out, err := f.run(t, "import", "--search-root", f.dev)
	if err != nil {
		t.Fatalf("import failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "different repository") {
		t.Errorf("expected the mismatch to be reported, got:\n%s", out)
	}
}

// Identical layout: the recorded path exists here, so nothing needs rewriting.
func TestImportSameLayoutDoesNotRewrite(t *testing.T) {
	f := newImportFixture(t)
	local := f.makeRepo(t, "api", "")

	f.archiveProject(t, "same-layout", local, "44444444-4444-4444-4444-444444444444", "unchanged")
	f.writeManifest(t, map[string]any{
		"same-layout": map[string]any{"path": local, "leaf": "api", "remote": "", "seen": "2026-08-30"},
	})

	out, err := f.run(t, "import", "--search-root", f.dev)
	if err != nil {
		t.Fatalf("import failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "matched by recorded path") {
		t.Errorf("expected a recorded-path match, got:\n%s", out)
	}
}

func TestImportDryRunWritesNothing(t *testing.T) {
	f := newImportFixture(t)
	local := f.makeRepo(t, "api", "git@github.com:example/api.git")
	f.archiveProject(t, "D--work-api", `D:\work\api`, "55555555-5555-5555-5555-555555555555", "hello")
	f.writeManifest(t, map[string]any{
		"D--work-api": map[string]any{
			"path": `D:\work\api`, "leaf": "api",
			"remote": "git@github.com:example/api.git", "seen": "2026-08-30",
		},
	})

	if out, err := f.run(t, "import", "--search-root", f.dev, "--dry-run"); err != nil {
		t.Fatalf("dry run failed: %v\n%s", err, out)
	}
	if files := f.localBucket(t, slugOf(local)); len(files) != 0 {
		t.Errorf("a dry run must not file anything, found %v", files)
	}
}

// Re-running must not duplicate or clobber what is already filed.
func TestImportIsIdempotent(t *testing.T) {
	f := newImportFixture(t)
	local := f.makeRepo(t, "api", "git@github.com:example/api.git")
	f.archiveProject(t, "D--work-api", `D:\work\api`, "66666666-6666-6666-6666-666666666666", "hello")
	f.writeManifest(t, map[string]any{
		"D--work-api": map[string]any{
			"path": `D:\work\api`, "leaf": "api",
			"remote": "git@github.com:example/api.git", "seen": "2026-08-30",
		},
	})

	if out, err := f.run(t, "import", "--search-root", f.dev); err != nil {
		t.Fatalf("first import failed: %v\n%s", err, out)
	}
	out, err := f.run(t, "import", "--search-root", f.dev)
	if err != nil {
		t.Fatalf("second import failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "1 already present") {
		t.Errorf("the second run should skip what is already filed:\n%s", out)
	}
	if files := f.localBucket(t, slugOf(local)); len(files) != 1 {
		t.Errorf("expected exactly one transcript, got %v", files)
	}
}

func TestImportJSON(t *testing.T) {
	f := newImportFixture(t)
	f.makeRepo(t, "api", "git@github.com:example/api.git")
	f.archiveProject(t, "D--work-api", `D:\work\api`, "77777777-7777-7777-7777-777777777777", "hello")
	f.writeManifest(t, map[string]any{
		"D--work-api": map[string]any{
			"path": `D:\work\api`, "leaf": "api",
			"remote": "git@github.com:example/api.git", "seen": "2026-08-30",
		},
	})

	out, err := f.run(t, "import", "--search-root", f.dev, "--json")
	if err != nil {
		t.Fatalf("import --json failed: %v\n%s", err, out)
	}
	var rows []struct {
		Bucket string `json:"bucket"`
		Local  string `json:"localPath"`
		How    string `json:"matchedBy"`
		Filed  int    `json:"filed"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	if len(rows) != 1 || rows[0].Filed != 1 || rows[0].How != "git remote" {
		t.Errorf("unexpected rows: %+v", rows)
	}
}

// pull is the blunt counterpart: same bucket name, no rewriting.
func TestPullFilesByBucketName(t *testing.T) {
	f := newImportFixture(t)
	f.archiveProject(t, "D--work-api", `D:\work\api`, "88888888-8888-8888-8888-888888888888", "hello")

	out, err := f.run(t, "pull")
	if err != nil {
		t.Fatalf("pull failed: %v\n%s", err, out)
	}
	if files := f.localBucket(t, "D--work-api"); len(files) != 1 {
		t.Errorf("expected the transcript under the same bucket name, got %v\n%s", files, out)
	}
	// It should point the user at import, which is what they usually want.
	if !strings.Contains(out, "import") {
		t.Errorf("pull should mention import:\n%s", out)
	}

	out, err = f.run(t, "pull")
	if err != nil {
		t.Fatalf("second pull failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "1 already present") {
		t.Errorf("pull should skip what exists:\n%s", out)
	}
}

// A rewrite that would produce unparseable JSON must abandon the file rather than
// leave a corrupt transcript behind - it is worse than no transcript.
func TestRestoreRefusesToWriteInvalidJSON(t *testing.T) {
	f := newImportFixture(t)
	source := filepath.Join(t.TempDir(), "from-old-laptop")
	// A line whose JSON validity depends on the escaping being preserved.
	mustWrite(t, filepath.Join(source, "99999999-9999-9999-9999-999999999999.jsonl"),
		`{"type":"user","cwd":"D:\\work\\api","message":{"role":"user","content":"hi"}}`+"\n")

	target := filepath.Join(f.dev, "api")
	mustMkdir(t, target)

	// restore takes no --archive: it files a folder you point it at.
	cmd := exec.Command(binary, "restore", "--claude-dir", f.claudeDir,
		"--source", source, "--project-path", target, "--rewrite-from", `D:\work\api`)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=")
	raw0, err := cmd.CombinedOutput()
	out := string(raw0)
	if err != nil {
		t.Fatalf("restore failed: %v\n%s", err, out)
	}

	files := f.localBucket(t, slugOf(target))
	if len(files) != 1 {
		t.Fatalf("expected one filed transcript, got %v\n%s", files, out)
	}
	raw, err := os.ReadFile(filepath.Join(f.claudeDir, "projects", slugOf(target), files[0]))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatalf("restore wrote invalid JSON:\n%s", line)
		}
	}
	if !strings.Contains(out, "json-escaped") {
		t.Errorf("expected a report of which forms were rewritten:\n%s", out)
	}
}

func slugOf(path string) string {
	return strings.NewReplacer(":", "-", `\`, "-", "/", "-", " ", "-").Replace(path)
}

func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return strings.Trim(string(b), `"`)
}
