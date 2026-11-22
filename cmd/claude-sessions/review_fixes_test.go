package main_test

// Regressions from the review of the fork, hook, profile and map changes. Each one
// needs a second push or import, or a switch back to a machine, to show: the cases a
// single round never reaches.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
)

// 1. The first push keeps this machine's copy as a fork; the next one, with nothing
// changed here, must not then copy it over the other machine's.
func TestPushTwiceAfterAForkKeepsTheOtherMachinesTurns(t *testing.T) {
	f := newFixture(t)
	_, a := f.seedSession(t, uuidLines("u1", "u2", "a1", "a2", "a3"), uuidLines("u1", "u2", "b1"), true)
	for i := 0; i < 2; i++ {
		if out, err := f.push(t, "--no-html"); err != nil {
			t.Fatalf("push %d: %v\n%s", i+1, err, out)
		}
	}
	if got := mustRead(t, a); !strings.Contains(got, `"b1"`) {
		t.Errorf("the other machine's turn was overwritten by the second push:\n%s", got)
	}
}

func TestHookThenSweepsAfterAForkKeepTheOtherMachinesTurns(t *testing.T) {
	f := newFixture(t)
	l, a := f.seedSession(t, uuidLines("u1", "u2", "a1", "a2", "a3"), uuidLines("u1", "u2", "b1"), true)
	f.pushFromHook(t, l)
	for i := 0; i < 2; i++ {
		if out, err := f.push(t, "--no-html"); err != nil {
			t.Fatalf("push: %v\n%s", err, out)
		}
	}
	if got := mustRead(t, a); !strings.Contains(got, `"b1"`) {
		t.Errorf("the other machine's turn was lost after a hook and two sweeps:\n%s", got)
	}
}

// 2. Two machines' MEMORY.md for one project: --update keeps every entry of both,
// here and, after the next push, in the archive.
func TestImportUpdateMergesBothMachinesMemoryIndex(t *testing.T) {
	f := newImportFixture(t)
	home := filepath.Dir(f.claudeDir)
	local := filepath.Join(f.claudeDir, "projects", claude.Slug(home), "memory", "MEMORY.md")
	mustWrite(t, local, "- [Mac only](mac.md) — here\n- [Shared](shared.md) — old text\n")
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(local, old, old)
	f.archiveProject(t, "C--Users-alice", `C:\Users\alice`, homeSession, "hi")
	mustWrite(t, filepath.Join(f.archive, "projects", "C--Users-alice", "memory", "MEMORY.md"),
		"- [Shared](shared.md) — new text\n- [Windows only](win.md) — there\n")
	f.writeShard(t, `C:\Users\alice\.claude`, `C:\Users\alice`, map[string]any{"C--Users-alice": map[string]any{"path": `C:\Users\alice`, "leaf": "alice"}})

	if out, err := f.runAsHome(t, home, "import", "--update"); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	got := mustRead(t, local)
	for _, want := range []string{"Mac only", "Windows only", "new text"} {
		if !strings.Contains(got, want) {
			t.Errorf("merged MEMORY.md lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "old text") {
		t.Errorf("an entry both indexes have kept the older text:\n%s", got)
	}
	if out, err := f.runAsHome(t, home, "push", "--no-html"); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if got := mustRead(t, filepath.Join(f.archive, "projects", claude.Slug(home), "memory", "MEMORY.md")); !strings.Contains(got, "Mac only") {
		t.Errorf("this machine's entry was lost from the archive:\n%s", got)
	}
}

// 2. A memory file replaced by --update leaves this machine's version behind.
func TestImportUpdateKeepsAReplacedMemoryFile(t *testing.T) {
	f := newImportFixture(t)
	home := filepath.Dir(f.claudeDir)
	dir := filepath.Join(f.claudeDir, "projects", claude.Slug(home), "memory")
	mustWrite(t, filepath.Join(dir, "note.md"), "mine\n")
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(filepath.Join(dir, "note.md"), old, old)
	f.archiveProject(t, "C--Users-alice", `C:\Users\alice`, homeSession, "hi")
	mustWrite(t, filepath.Join(f.archive, "projects", "C--Users-alice", "memory", "note.md"), "theirs\n")
	f.writeShard(t, `C:\Users\alice\.claude`, `C:\Users\alice`, map[string]any{"C--Users-alice": map[string]any{"path": `C:\Users\alice`, "leaf": "alice"}})

	if out, err := f.runAsHome(t, home, "import", "--update"); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	kept, _ := filepath.Glob(filepath.Join(dir, ".replaced", "*", "note.md"))
	if len(kept) != 1 || mustRead(t, kept[0]) != "mine\n" {
		t.Errorf("the replaced memory file was not kept: %v", kept)
	}
}

// 3. The local rule file --update-profile replaced is still recoverable after the next push.
func TestReplacedRuleFileSurvivesTheNextPush(t *testing.T) {
	f := profileImportFixture(t)
	mustWrite(t, filepath.Join(f.archive, "profile", "CLAUDE.md"), "THEIRS\n")
	local := filepath.Join(f.claudeDir, "CLAUDE.md")
	mustWrite(t, local, "MINE\n")
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(local, old, old)

	if out, err := f.run(t, "import", "--search-root", f.dev, "--no-plugins", "--update-profile"); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if out, err := f.run(t, "push", "--no-html"); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	kept, _ := filepath.Glob(filepath.Join(f.archive, "profile", "replaced", "*", "*", "CLAUDE.md"))
	if len(kept) != 1 || mustRead(t, kept[0]) != "MINE\n" {
		t.Errorf("the replaced CLAUDE.md is not recoverable after the next push: %v", kept)
	}
}

// 4. The other machine resumed its copy of this machine's session and added a turn.
func TestImportBringsBackAForkContinuedElsewhere(t *testing.T) {
	f, repo := forkFixture(t, uuidLines("u1", "u2", "w1"), uuidLines("u1", "u2", "w1"))
	echo := claude.ForkID(forkSession, strings.ToLower(claude.Slug(repo)+"@"+f.machineName(t)))
	mustWrite(t, filepath.Join(f.archive, "projects", "-Users-me-api", echo+".jsonl"), uuidLines("u1", "u2", "w1", "m9"))

	out, err := f.run(t, "import", "--search-root", f.dev, "--update")
	if err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if !anySessionHas(t, f, repo, `"m9"`) {
		t.Errorf("a turn added to the fork copy on the other machine never arrived:\n%s", out)
	}
}

// 5. The fork copy was continued here and the other machine continued its original:
// the other machine's new turn must arrive, and the output must name where.
func TestImportFilesTheOtherSideWhenAForkForksAgain(t *testing.T) {
	f, repo := forkFixture(t, uuidLines("u1", "u2", "a1"), uuidLines("u1", "u2", "b1"))
	if out, err := f.run(t, "import", "--search-root", f.dev, "--update"); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	first := claude.ForkID(forkSession, "-users-me-api@mac-box")
	fork := filepath.Join(f.claudeDir, "projects", claude.Slug(repo), first+".jsonl")
	mustWrite(t, fork, mustRead(t, fork)+`{"type":"user","uuid":"a3","message":{"role":"user","content":"x"}}`+"\n")
	arch := filepath.Join(f.archive, "projects", "-Users-me-api", forkSession+".jsonl")
	mustWrite(t, arch, uuidLines("u1", "u2", "b1", "b2"))

	out, err := f.run(t, "import", "--search-root", f.dev, "--update")
	if err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	holder := ""
	for _, name := range f.localBucket(t, claude.Slug(repo)) {
		if strings.Contains(mustRead(t, filepath.Join(f.claudeDir, "projects", claude.Slug(repo), name)), `"b2"`) {
			holder = strings.TrimSuffix(name, ".jsonl")
		}
	}
	if holder == "" {
		t.Fatalf("the other machine's new turn is in no local session:\n%s", out)
	}
	if !strings.Contains(out, holder) {
		t.Errorf("the output does not name the session holding the new turn (%s):\n%s", holder, out)
	}
	if got := mustRead(t, fork); !strings.Contains(got, `"a3"`) {
		t.Errorf("the turn added here to the fork copy was lost:\n%s", got)
	}
}

// 6. Shared layout: the archived copy is OTHER-BOX's, even though this machine pushed
// the project last. Its fork is tagged with OTHER-BOX, never with this machine.
func TestImportTagsAForkWithTheMachineThatWroteIt(t *testing.T) {
	f := newImportFixture(t)
	repo := f.makeRepo(t, "api", "git@github.com:example/api.git")
	bucket := claude.Slug(repo)
	// A session recorded at this repo. The path goes into JSON, so its backslashes -
	// all of a Windows path's - are escaped, or every line is unreadable there.
	here := func(s string) string {
		return strings.ReplaceAll(s, `E:\\work\\api`, strings.ReplaceAll(repo, `\`, `\\`))
	}
	mustWrite(t, filepath.Join(f.claudeDir, "projects", bucket, forkSession+".jsonl"), here(uuidLines("u1", "u2", "a1")))
	arch := filepath.Join(f.archive, "projects", bucket, forkSession+".jsonl")
	mustWrite(t, arch, here(uuidLines("u1", "u2", "c1")))
	old := time.Now().Add(-3 * time.Hour)
	os.Chtimes(arch, old, old)
	info, _ := os.Stat(arch)
	f.writeShardAs(t, "OTHER-BOX", map[string]any{
		bucket: map[string]any{"path": repo, "leaf": "api", "remote": "git@github.com:example/api.git", "seen": "2026-01-01T00:00:00Z"},
	})
	idx, _ := json.Marshal(map[string]any{"schemaVersion": 1, "machine": "OTHER-BOX", "sessions": []map[string]any{
		{"bucket": bucket, "id": forkSession, "size": info.Size(), "updated": info.ModTime()},
	}})
	mustWrite(t, filepath.Join(f.archive, "index", "OTHER-BOX.json"), string(idx))
	if out, err := f.run(t, "push", "--no-html"); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}

	out, err := f.run(t, "import", "--search-root", f.dev, "--update")
	if err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	mine := claude.ForkID(forkSession, strings.ToLower(bucket+"@"+f.machineName(t)))
	theirs := claude.ForkID(forkSession, strings.ToLower(bucket+"@OTHER-BOX"))
	got := f.localBucket(t, bucket)
	if contains(got, mine+".jsonl") || !contains(got, theirs+".jsonl") {
		t.Errorf("OTHER-BOX's copy was not tagged as OTHER-BOX's: %v\n%s", got, out)
	}
}

// 9. pull is the shared-layout command; a forked session must come across there too.
func TestPullUpdateFilesTheOtherCopyOfAForkedSession(t *testing.T) {
	f := newImportFixture(t)
	repo := f.makeRepo(t, "api", "git@github.com:example/api.git")
	bucket := claude.Slug(repo)
	mustWrite(t, filepath.Join(f.claudeDir, "projects", bucket, forkSession+".jsonl"), uuidLines("u1", "u2", "a1"))
	mustWrite(t, filepath.Join(f.archive, "projects", bucket, forkSession+".jsonl"), uuidLines("u1", "u2", "c1"))
	f.writeShardAs(t, "OTHER-BOX", map[string]any{bucket: map[string]any{"path": repo, "leaf": "api"}})

	out, err := f.run(t, "pull", "--update", "--no-plugins")
	if err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	if !anySessionHas(t, f, repo, `"c1"`) {
		t.Errorf("pull --update did not file the other machine's copy:\n%s", out)
	}
}

// 10. A mapped project is repointed inside other projects' sessions too.
func TestMappedProjectIsRepointedInOtherProjectsSessions(t *testing.T) {
	f := newImportFixture(t)
	api := f.makeRepo(t, "api", "git@github.com:example/api.git")
	renamed := f.makeRepo(t, "new-name", "")
	f.archiveProject(t, "D--work-old-name", `D:\work\old-name`, mapSession, "hello")
	mustWrite(t, filepath.Join(f.archive, "projects", "D--work-api", forkSession+".jsonl"),
		`{"type":"user","uuid":"u1","cwd":"D:\\work\\api","message":{"role":"user","content":"see D:\\work\\old-name\\notes.txt"}}`+"\n")
	f.writeManifest(t, map[string]any{
		"D--work-old-name": map[string]any{"path": `D:\work\old-name`, "leaf": "old-name"},
		"D--work-api":      map[string]any{"path": `D:\work\api`, "leaf": "api", "remote": "git@github.com:example/api.git"},
	})
	if out, err := f.config(t, "map", "D--work-old-name", renamed); err != nil {
		t.Fatalf("config map: %v\n%s", err, out)
	}

	if out, err := f.run(t, "import", "--search-root", f.dev); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	body := mustRead(t, filepath.Join(f.claudeDir, "projects", claude.Slug(api), forkSession+".jsonl"))
	if strings.Contains(body, `old-name`) {
		t.Errorf("the mapped project's old path was left in another project's session:\n%s", body)
	}
}

// 13. Drive offline is routine for the per-reply hook: it must not log every reply.
func TestPushFromHookDoesNotLogRoutineSkips(t *testing.T) {
	f := newFixture(t)
	f.archive = filepath.Join(t.TempDir(), "unmounted", "claude-sessions")
	f.pushFromHook(t, filepath.Join(f.claudeDir, "projects", "e--work-web-app", hookSession+".jsonl"))
	if raw, _ := os.ReadFile(filepath.Join(f.claudeDir, "session-sync.log")); strings.Contains(string(raw), "SKIP") {
		t.Errorf("the per-reply hook logged a routine skip:\n%s", raw)
	}
}

// 7. The reason the destination is unreachable reaches the log, so doctor can read it.
func TestPushLogsWhyTheDestinationIsUnreachable(t *testing.T) {
	f := newFixture(t)
	f.archive = filepath.Join(t.TempDir(), "unmounted", "claude-sessions")
	f.push(t, "--no-html", "--quiet")
	raw, _ := os.ReadFile(filepath.Join(f.claudeDir, "session-sync.log"))
	if !strings.Contains(string(raw), "no such file") && !strings.Contains(string(raw), "cannot find") {
		t.Errorf("the log does not say why the destination is unreachable:\n%s", raw)
	}
}

// 8. A large forked session is throttled on its fork copy too.
func TestPushFromHookThrottlesALargeForkedSession(t *testing.T) {
	f := newFixture(t)
	big := `{"type":"x","text":"` + strings.Repeat("a", 6<<20) + `"}` + "\n"
	l, _ := f.seedSession(t, uuidLines("u1")+big+uuidLines("a1"), uuidLines("u1")+big+uuidLines("b1"), true)
	f.pushFromHook(t, l) // keeps the fork
	forks, _ := filepath.Glob(filepath.Join(f.archive, "forks", forkBucket, forkSession, "*.jsonl"))
	if len(forks) != 1 {
		t.Fatalf("the fork was not kept: %v", forks)
	}
	mustWrite(t, l, mustRead(t, l)+uuidLines("a2"))
	// The next reply a minute after the one the fork holds: within the throttle.
	if fork, err := os.Stat(forks[0]); err == nil {
		os.Chtimes(l, fork.ModTime().Add(time.Minute), fork.ModTime().Add(time.Minute))
	}
	f.pushFromHook(t, l)
	if strings.Contains(mustRead(t, forks[0]), `"a2"`) {
		t.Error("a large forked session was re-copied right after its last copy")
	}
}

func anySessionHas(t *testing.T, f importFixture, repo, text string) bool {
	t.Helper()
	for _, name := range f.localBucket(t, claude.Slug(repo)) {
		if strings.Contains(mustRead(t, filepath.Join(f.claudeDir, "projects", claude.Slug(repo), name)), text) {
			return true
		}
	}
	return false
}
