package main_test

// Fixes from the first real two-machine run (Mac and Windows), findings a-f.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
)

// (a) The local copy gained only lines that add no turn - metadata Claude Code writes,
// and a hook attachment with a uuid of its own - so it is newer by date, yet the
// archived copy continues it. import --update takes the continuation.
func TestImportUpdateTakesAContinuationOverACopyWithOnlyMetadataAdded(t *testing.T) {
	stale := uuidLines("u1", "u2") +
		`{"type":"last-prompt","lastPrompt":"go on","leafUuid":"u2","sessionId":"` + forkSession + `"}` + "\n" +
		`{"type":"cost-state","totalCostUSD":1.5,"sessionId":"` + forkSession + `"}` + "\n" +
		`{"type":"bridge-session","bridgeSessionId":"b","sessionId":"` + forkSession + `"}` + "\n" +
		`{"type":"attachment","uuid":"h1","parentUuid":"u2","attachment":{"type":"hook_success"},"sessionId":"` + forkSession + `"}` + "\n"
	f, repo := forkFixture(t, stale, uuidLines("u1", "u2", "m3"))
	local := filepath.Join(f.claudeDir, "projects", claude.Slug(repo), forkSession+".jsonl")
	archived := filepath.Join(f.archive, "projects", "-Users-me-api", forkSession+".jsonl")
	older, newer := time.Now().Add(-2*time.Hour), time.Now().Add(-time.Minute)
	os.Chtimes(archived, older, older)
	os.Chtimes(local, newer, newer)

	out, err := f.run(t, "import", "--search-root", f.dev, "--update", "--no-plugins")
	if err != nil {
		t.Fatalf("import failed: %v\n%s", err, out)
	}
	if got := mustRead(t, local); !strings.Contains(got, `"m3"`) {
		t.Errorf("the continuation was not taken:\n%s\n%s", got, out)
	}
	if n := len(f.localBucket(t, claude.Slug(repo))); n != 1 {
		t.Errorf("filed as a fork: %v\n%s", f.localBucket(t, claude.Slug(repo)), out)
	}
}

// (b) --update refreshes sessions, not rules: a newer, smaller archived CLAUDE.md does
// not replace this machine's own. --update-profile asks for that.
func TestImportUpdateKeepsRuleFilesUnlessUpdateProfile(t *testing.T) {
	f := profileImportFixture(t)
	mustWrite(t, filepath.Join(f.archive, "profile", "CLAUDE.md"), "mac rules\n")
	local := filepath.Join(f.claudeDir, "CLAUDE.md")
	mustWrite(t, local, "windows rules, the longer of the two\n")
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(local, old, old)

	if out, err := f.run(t, "import", "--search-root", f.dev, "--no-plugins", "--update"); err != nil {
		t.Fatalf("import --update failed: %v\n%s", err, out)
	}
	if got := mustRead(t, local); got != "windows rules, the longer of the two\n" {
		t.Fatalf("--update replaced the local CLAUDE.md: %q", got)
	}
	out, err := f.run(t, "import", "--search-root", f.dev, "--no-plugins", "--update-profile")
	if err != nil {
		t.Fatalf("import --update-profile failed: %v\n%s", err, out)
	}
	if got := mustRead(t, local); got != "mac rules\n" {
		t.Errorf("--update-profile did not take the newer archived CLAUDE.md: %q\n%s", got, out)
	}
}

// (c) A subagent transcript names the project path as the session does, and is filed
// through the same rewrite. Saved tool output stays a byte copy.
func TestImportRewritesSubagentTranscripts(t *testing.T) {
	f, repo := forkFixture(t, "", "")
	os.Remove(filepath.Join(f.claudeDir, "projects", claude.Slug(repo), forkSession+".jsonl"))
	src := filepath.Join(f.archive, "projects", "-Users-me-api")
	mustWrite(t, filepath.Join(src, forkSession+".jsonl"),
		line(`{"type":"user","uuid":"u1","cwd":"/Users/me/api","sessionId":"`+forkSession+`","message":{"role":"user","content":"go"}}`))
	sub := line(`{"type":"user","uuid":"s1","isSidechain":true,"cwd":"/Users/me/api","sessionId":"` + forkSession + `","message":{"role":"user","content":"read /Users/me/api/src/x.go"}}`)
	mustWrite(t, filepath.Join(src, forkSession, "subagents", "agent-a1.jsonl"), sub)
	mustWrite(t, filepath.Join(src, forkSession, "subagents", "workflows", "wf_1", "agent-b2.jsonl"), sub)
	mustWrite(t, filepath.Join(src, forkSession, "tool-results", "t1.txt"), "/Users/me/api/src\n")

	out, err := f.run(t, "import", "--search-root", f.dev, "--no-plugins")
	if err != nil {
		t.Fatalf("import failed: %v\n%s", err, out)
	}
	dir := filepath.Join(f.claudeDir, "projects", claude.Slug(repo), forkSession)
	for _, rel := range []string{"subagents/agent-a1.jsonl", "subagents/workflows/wf_1/agent-b2.jsonl"} {
		body := mustRead(t, filepath.Join(dir, filepath.FromSlash(rel)))
		var e struct {
			Cwd     string
			Message struct{ Content string }
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &e); err != nil {
			t.Fatalf("%s is not valid JSON: %v\n%s", rel, err, body)
		}
		if e.Cwd != repo || strings.Contains(body, "/Users/me/api") || !strings.Contains(e.Message.Content, repo) {
			t.Errorf("%s not repointed to %s:\n%s", rel, repo, body)
		}
	}
	if got := mustRead(t, filepath.Join(dir, "tool-results", "t1.txt")); got != "/Users/me/api/src\n" {
		t.Errorf("saved tool output was changed: %q", got)
	}
}

// (e) A container work folder - ~/Developer with a few loose files - is placed here,
// but the repositories inside it are not work files and do not come with it. A
// project in one that is not here is reported, not filed into a bucket of its own.
func TestImportDoesNotFileAProjectIntoAPlacedFolderItIsNotIn(t *testing.T) {
	arch := t.TempDir()
	a, b := newMachine(t, "WIN", arch), newMachine(t, "MAC", arch)
	homeA, homeB := t.TempDir(), t.TempDir()
	dev := filepath.Join(homeA, "Developer")
	repo := gitRepo(t, filepath.Join(dev, "ghost"))
	mustWrite(t, filepath.Join(dev, "notes.md"), "x\n")
	workSession(t, a, repo, []string{filepath.Join(dev, "notes.md")})
	a.workRun(t, homeA, "push", "--no-html")
	// One file system stands in for two: A's repository is not on B, nor any clone.
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}

	out := b.workRun(t, homeB, "import", "--no-plugins")
	if _, err := os.Stat(filepath.Join(homeB, "Developer", "notes.md")); err != nil {
		t.Fatalf("work folder not placed: %v\n%s", err, out)
	}
	placed := filepath.Join(homeB, "Developer", "ghost")
	if _, err := os.Stat(filepath.Join(b.claudeDir, "projects", claude.Slug(placed))); !os.IsNotExist(err) {
		t.Fatalf("filed into a bucket for a folder that is not here: %v\n%s", err, out)
	}
	if !strings.Contains(out, "- "+claude.Slug(repo)+": ") || strings.Contains(out, "(matched by work folder)") {
		t.Fatalf("not reported as not found:\n%s", out)
	}
}
