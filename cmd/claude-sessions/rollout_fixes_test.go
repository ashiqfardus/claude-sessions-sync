package main_test

// Fixes from the first real two-machine run (Mac and Windows), findings a-f.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
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

// forkSubagentFixture is a session continued on both machines whose archived copy has
// a subagent naming the other machine's project path.
func forkSubagentFixture(t *testing.T) (importFixture, string, string) {
	t.Helper()
	f, repo := forkFixture(t, uuidLines("u1", "u2", "w1"), uuidLines("u1", "u2", "m1"))
	mustWrite(t, filepath.Join(f.archive, "projects", "-Users-me-api", forkSession, "subagents", "agent-a1.jsonl"),
		line(`{"type":"user","uuid":"s1","isSidechain":true,"cwd":"/Users/me/api","sessionId":"`+forkSession+`","message":{"role":"user","content":"read /Users/me/api/x.go"}}`))
	return f, repo, claude.ForkID(forkSession, "-users-me-api@mac-box")
}

// 4b-2: the other machine's copy is filed as a session of its own, and its subagent
// with it - repointed here and naming the fork, not the original session.
func TestImportFilesAForksSubagentWithLocalPathsAndTheForkID(t *testing.T) {
	f, repo, fid := forkSubagentFixture(t)
	out, err := f.run(t, "import", "--search-root", f.dev, "--update", "--no-plugins")
	if err != nil {
		t.Fatalf("import failed: %v\n%s", err, out)
	}
	body := mustRead(t, filepath.Join(f.claudeDir, "projects", claude.Slug(repo), fid, "subagents", "agent-a1.jsonl"))
	var e struct{ Cwd, SessionID string }
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &e); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, body)
	}
	if e.Cwd != repo || e.SessionID != fid || strings.Contains(body, forkSession) || strings.Contains(body, "/Users/me/api") {
		t.Errorf("the fork's subagent was not repointed to %s and %s:\n%s", repo, fid, body)
	}
}

// 4b-2: a fork's subagent that cannot be filed is reported, as one of the session's is.
func TestImportReportsAForksSubagentItCannotFile(t *testing.T) {
	f, repo, fid := forkSubagentFixture(t)
	// A file where the fork's subagents folder has to go.
	mustWrite(t, filepath.Join(f.claudeDir, "projects", claude.Slug(repo), fid, "subagents"), "in the way\n")
	out, err := f.run(t, "import", "--search-root", f.dev, "--update", "--no-plugins")
	if err != nil {
		t.Fatalf("import failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "x "+fid+"/subagents/agent-a1.jsonl: ") {
		t.Errorf("the fork's subagent that could not be filed was not reported:\n%s", out)
	}
}

// 4b-3: import --repoint rewrites a workflow's subagent, a level down, too.
func TestRepointRewritesANestedSubagent(t *testing.T) {
	p := stalePair(t, t.TempDir())
	agent := filepath.Join(filepath.Dir(p.b.session(p.repo)), forkSession, "subagents", "workflows", "wf_1", "agent-2.jsonl")
	l, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "read " + filepath.Join(p.folder, "রিপোর্ট.md")}})
	mustWrite(t, agent, string(l)+"\n")
	aged(t, agent)
	out := p.b.workRun(t, p.homeB, "import", "--no-plugins", "--repoint")
	if got := mustRead(t, agent); !strings.Contains(got, inJSON(p.placed())) || strings.Contains(got, inJSON(p.folder)) {
		t.Fatalf("nested subagent not repointed:\n%s\n%s", got, out)
	}
}

// 4b-3: push finds a work folder that only a workflow's subagent wrote in.
func TestPushFindsWorkInANestedSubagent(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home := t.TempDir()
	notes := filepath.Join(home, "Notes")
	mustWrite(t, filepath.Join(notes, "n.md"), "x\n")
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	tr := workSession(t, m, repo, nil, "ls")
	mustWrite(t, filepath.Join(strings.TrimSuffix(tr, ".jsonl"), "subagents", "workflows", "wf_1", "agent-1.jsonl"),
		toolLineJSON(repo, "s1", filepath.Join(notes, "n.md")))
	m.workRun(t, home, "push", "--no-html")
	for _, wf := range workShard(t, m) {
		if wf.Path == notes {
			return
		}
	}
	t.Fatalf("work folder %s not found: %+v", notes, workShard(t, m))
}

// 4b-3: filed by an older build with no record, a workflow's hostile subagent is marked
// by the first run as one a level up is (TestFirstRunMarksSubagent).
func TestFirstRunMarksNestedSubagent(t *testing.T) {
	arch := t.TempDir()
	evil, mac := newMachine(t, "EVIL", arch), newMachine(t, "MAC", arch)
	homeE, homeM := t.TempDir(), t.TempDir()
	private := filepath.Join(homeM, "Private")
	mustWrite(t, filepath.Join(private, "salary.txt"), "very private\n")
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	mustWrite(t, mac.session(repo), sessionLines(repo, "m1"))
	mac.workRun(t, homeM, "push", "--no-html")
	configAs(t, evil.claudeDir, "EVIL", "work", "off")
	repoE := gitRepo(t, filepath.Join(t.TempDir(), "dev", "evil"))
	tr := workSession(t, evil, repoE, nil, "ls")
	mustWrite(t, filepath.Join(strings.TrimSuffix(tr, ".jsonl"), "subagents", "workflows", "wf_1", "agent-1.jsonl"),
		toolLineJSON(private, "s1", filepath.Join(private, "salary.txt")))
	evil.workRun(t, homeE, "push", "--no-html")
	mac.workRun(t, homeM, "import", "--no-plugins")
	_ = os.Remove(filepath.Join(mac.claudeDir, "session-sync-base", "imported.json"))
	mac.workRun(t, homeM, "push", "--no-html")
	noExfil(t, arch)
}

// treeState is every file under dirs with its size and time, to show a run wrote
// nothing. Folders are left out: the lock is a file made and removed in ~/.claude.
func treeState(t *testing.T, dirs ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, d := range dirs {
		_ = filepath.WalkDir(d, func(p string, e os.DirEntry, err error) error {
			if err == nil && e.Type().IsRegular() {
				if info, ierr := e.Info(); ierr == nil {
					out[p] = info.ModTime().String() + "/" + strconv.FormatInt(info.Size(), 10)
				}
			}
			return nil
		})
	}
	return out
}

// 4b-5: pull follows the same profile rule as import.
func TestPullUpdateProfile(t *testing.T) {
	f := profileImportFixture(t)
	mustWrite(t, filepath.Join(f.archive, "profile", "CLAUDE.md"), "mac rules\n")
	local := filepath.Join(f.claudeDir, "CLAUDE.md")
	mustWrite(t, local, "windows rules, the longer of the two\n")
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(local, old, old)

	before := treeState(t, f.claudeDir, f.archive)
	if out, err := f.run(t, "pull", "--no-plugins", "--update-profile", "--dry-run"); err != nil {
		t.Fatalf("pull --dry-run failed: %v\n%s", err, out)
	}
	if after := treeState(t, f.claudeDir, f.archive); !reflect.DeepEqual(before, after) {
		t.Fatalf("a dry run wrote:\nbefore %v\nafter  %v", before, after)
	}
	if out, err := f.run(t, "pull", "--no-plugins", "--update"); err != nil {
		t.Fatalf("pull --update failed: %v\n%s", err, out)
	}
	if got := mustRead(t, local); got != "windows rules, the longer of the two\n" {
		t.Fatalf("pull --update replaced the local CLAUDE.md: %q", got)
	}
	out, err := f.run(t, "pull", "--no-plugins", "--update-profile")
	if err != nil {
		t.Fatalf("pull --update-profile failed: %v\n%s", err, out)
	}
	if got := mustRead(t, local); got != "mac rules\n" {
		t.Errorf("pull --update-profile did not take the newer archived CLAUDE.md: %q\n%s", got, out)
	}
	kept, _ := filepath.Glob(filepath.Join(f.archive, "profile", "replaced", "*", "*", "CLAUDE.md"))
	if len(kept) != 1 || mustRead(t, kept[0]) != "windows rules, the longer of the two\n" {
		t.Errorf("the replaced local CLAUDE.md is not kept in profile/replaced/: %v\n%s", kept, out)
	}
}
