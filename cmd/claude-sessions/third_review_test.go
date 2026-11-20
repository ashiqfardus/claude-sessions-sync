package main_test

// Regressions from the third review: forks, memory and machine names over several
// rounds and machines.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
)

// H2 on the user's own layout: a new project on WIN archived only by the per-reply hook
// (no manifest entry, no index row). MAC imports, both continue, MAC files WIN's copy
// under an owner-less tag; once WIN's sweep runs, the tag changes and it is filed again.
func TestThirdReviewDifferentLayoutNewProjectHookOnlyForkFiledTwice(t *testing.T) {
	archive := t.TempDir()
	mac, win := newMachine(t, "MAC", archive), newMachine(t, "WIN", archive)
	macRepo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	winRepo := gitRepo(t, filepath.Join(t.TempDir(), "work", "api"))
	win.run(t, "push", "--no-html") // installed earlier: a shard, but not this project

	mustWrite(t, win.session(winRepo), sessionLines(winRepo, "u1"))
	t0 := time.Now().Add(-2 * time.Hour)
	os.Chtimes(win.session(winRepo), t0, t0)
	win.hook(t, win.session(winRepo))
	away(t, winRepo, func() { mac.run(t, "import", "--search-root", filepath.Dir(macRepo), "--update", "--no-plugins") })
	if len(mac.sessions(t, macRepo)) != 1 {
		t.Fatalf("MAC did not get WIN's session: %v", mac.sessions(t, macRepo))
	}

	mustWrite(t, win.session(winRepo), sessionLines(winRepo, "u1", "w1"))
	t1 := time.Now().Add(-time.Hour)
	os.Chtimes(win.session(winRepo), t1, t1)
	win.hook(t, win.session(winRepo)) // WIN dies here
	mustWrite(t, mac.session(macRepo), sessionLines(macRepo, "u1", "m1"))
	away(t, winRepo, func() { mac.run(t, "import", "--search-root", filepath.Dir(macRepo), "--update", "--no-plugins") })
	t.Logf("MAC after WIN died: %v", mac.sessions(t, macRepo))

	win.run(t, "push", "--no-html") // WIN is back: its sweep
	away(t, winRepo, func() { mac.run(t, "import", "--search-root", filepath.Dir(macRepo), "--update", "--no-plugins") })
	if got := mac.sessions(t, macRepo); len(got) != 2 {
		t.Errorf("MAC holds %v, want its session and one copy of WIN's", got)
	}
}

// A fork copy the user deleted (they kept the other) comes back on the next import.
func TestThirdReviewDeletedForkComesBack(t *testing.T) {
	archive := t.TempDir()
	mac, win := newMachine(t, "MAC", archive), newMachine(t, "WIN", archive)
	macRepo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	winRepo := gitRepo(t, filepath.Join(t.TempDir(), "work", "api"))
	mustWrite(t, mac.session(macRepo), sessionLines(macRepo, "u1", "m1"))
	mustWrite(t, win.session(winRepo), sessionLines(winRepo, "u1", "w1"))
	mac.run(t, "push", "--no-html")
	win.run(t, "push", "--no-html")
	imp := func() {
		away(t, winRepo, func() { mac.run(t, "import", "--search-root", filepath.Dir(macRepo), "--update", "--no-plugins") })
	}
	imp()
	before := mac.sessions(t, macRepo)
	for _, s := range before {
		if s != forkSession+".jsonl" {
			os.Remove(filepath.Join(filepath.Dir(mac.session(macRepo)), s)) // user deletes the copy they do not want
		}
	}
	imp()
	t.Logf("before %v, after delete+import %v", before, mac.sessions(t, macRepo))
	if len(mac.sessions(t, macRepo)) != 1 {
		t.Errorf("a fork the user deleted was filed again")
	}
}

// Shared layout: the base A keeps is A's own pushed MEMORY.md; B then pushes its newer
// MEMORY.md, which never had A's entry. A's import reads that as "deleted there".
func TestThirdReviewSharedLayoutMemoryEntryDroppedAsDeletedThere(t *testing.T) {
	archive := t.TempDir()
	a, b := newMachine(t, "BOX-A", archive), newMachine(t, "BOX-B", archive)
	repo := gitRepo(t, filepath.Join(t.TempDir(), "api"))
	root := filepath.Dir(repo)
	aIdx := filepath.Join(a.claudeDir, "projects", claude.Slug(repo), "memory", "MEMORY.md")
	bIdx := filepath.Join(b.claudeDir, "projects", claude.Slug(repo), "memory", "MEMORY.md")
	at := func(p string, d time.Duration) { x := time.Now().Add(d); os.Chtimes(p, x, x) }

	mustWrite(t, a.session(repo), sessionLines(repo, "u1"))
	mustWrite(t, aIdx, "- [E1](e1.md) - one\n")
	at(a.session(repo), -5*time.Hour)
	at(aIdx, -5*time.Hour)
	a.run(t, "push", "--no-html")
	b.run(t, "import", "--search-root", root, "--update", "--no-plugins")
	b.run(t, "push", "--no-html")

	mustWrite(t, aIdx, "- [E1](e1.md) - one\n- [E2](e2.md) - two\n") // A adds E2
	at(aIdx, -4*time.Hour)
	a.run(t, "push", "--no-html")

	mustWrite(t, b.session(repo), sessionLines(repo, "u1", "b1")) // B works: newest
	at(b.session(repo), -3*time.Hour)
	b.run(t, "push", "--no-html")
	a.run(t, "import", "--search-root", root, "--update", "--no-plugins") // base := A's own

	mustWrite(t, bIdx, "- [E1](e1.md) - one\n- [E3](e3.md) - three\n") // B adds E3, never saw E2
	at(bIdx, -2*time.Hour)
	b.run(t, "push", "--no-html")
	a.run(t, "import", "--search-root", root, "--update", "--no-plugins")

	got := mustRead(t, aIdx)
	t.Logf("A's MEMORY.md:\n%s", got)
	if !strings.Contains(got, "e2.md") {
		t.Errorf("A's own entry E2 was dropped - nobody deleted it")
	}
}

// H1: shared layout. The importing machine is the bucket's newest-seen owner, so its
// import skips memory entirely - another machine's memory edit never arrives.
func TestThirdReviewSharedLayoutMemorySkippedWhenImporterIsNewest(t *testing.T) {
	archive := t.TempDir()
	a, b := newMachine(t, "BOX-A", archive), newMachine(t, "BOX-B", archive)
	repo := gitRepo(t, filepath.Join(t.TempDir(), "api"))
	root := filepath.Dir(repo)
	aMem := filepath.Join(a.claudeDir, "projects", claude.Slug(repo), "memory")
	bMem := filepath.Join(b.claudeDir, "projects", claude.Slug(repo), "memory")

	mustWrite(t, a.session(repo), sessionLines(repo, "u1"))
	mustWrite(t, filepath.Join(aMem, "MEMORY.md"), "- [A](a.md) - a\n")
	mustWrite(t, filepath.Join(aMem, "a.md"), "a\n")
	old := time.Now().Add(-3 * time.Hour)
	for _, p := range []string{a.session(repo), filepath.Join(aMem, "MEMORY.md"), filepath.Join(aMem, "a.md")} {
		os.Chtimes(p, old, old)
	}
	a.run(t, "push", "--no-html")
	b.run(t, "import", "--search-root", root, "--update", "--no-plugins")
	if _, err := os.Stat(filepath.Join(bMem, "a.md")); err != nil {
		t.Fatalf("B did not get A's memory at all: %v", err)
	}
	b.run(t, "push", "--no-html")

	// A adds a memory entry (1h ago) and pushes it.
	t1 := time.Now().Add(-time.Hour)
	mustWrite(t, filepath.Join(aMem, "MEMORY.md"), "- [A](a.md) - a\n- [A2](a2.md) - a2\n")
	mustWrite(t, filepath.Join(aMem, "a2.md"), "a2\n")
	os.Chtimes(filepath.Join(aMem, "MEMORY.md"), t1, t1)
	os.Chtimes(filepath.Join(aMem, "a2.md"), t1, t1)
	a.run(t, "push", "--no-html")

	// B works in the project now (newer transcript), its sweep pushes, then the user
	// runs import --update on B.
	mustWrite(t, b.session(repo), sessionLines(repo, "u1", "b1"))
	b.run(t, "push", "--no-html")
	out := b.run(t, "import", "--search-root", root, "--update", "--no-plugins")
	t.Logf("B import:\n%s", out)
	got, _ := os.ReadFile(filepath.Join(bMem, "MEMORY.md"))
	if !strings.Contains(string(got), "a2.md") {
		t.Errorf("A's new MEMORY.md entry never reached B:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(bMem, "a2.md")); err != nil {
		t.Errorf("A's new memory file a2.md never reached B: %v", err)
	}
}

// H2: shared layout. B imported A's session and continued it, archived only by the
// per-reply hook (B's index never recorded it), then B "died". A continued too.
// A's import tags the fork with no machine; when B comes back and its index catches
// up, the tag changes and the same conversation is filed a second time.
func TestThirdReviewSharedLayoutForkTagChangesWhenIndexCatchesUp(t *testing.T) {
	archive := t.TempDir()
	a, b := newMachine(t, "BOX-A", archive), newMachine(t, "BOX-B", archive)
	repo := gitRepo(t, filepath.Join(t.TempDir(), "api"))
	root := filepath.Dir(repo)

	// B was installed earlier: it has pushed, but never this session.
	other := filepath.Join(b.claudeDir, "projects", claude.Slug(repo), "11111111-1111-1111-1111-111111111111.jsonl")
	mustWrite(t, other, strings.ReplaceAll(sessionLines(repo, "z1"), forkSession, "11111111-1111-1111-1111-111111111111"))
	old := time.Now().Add(-5 * time.Hour)
	os.Chtimes(other, old, old)
	b.run(t, "push", "--no-html")

	mustWrite(t, a.session(repo), sessionLines(repo, "u1"))
	t0 := time.Now().Add(-2 * time.Hour)
	os.Chtimes(a.session(repo), t0, t0)
	a.run(t, "push", "--no-html")

	b.run(t, "import", "--search-root", root, "--update", "--no-plugins")
	mustWrite(t, b.session(repo), sessionLines(repo, "u1", "b1"))
	t1 := time.Now().Add(-time.Hour)
	os.Chtimes(b.session(repo), t1, t1)
	b.hook(t, b.session(repo)) // archived by the Stop hook only; B then dies

	mustWrite(t, a.session(repo), sessionLines(repo, "u1", "a1"))
	a.run(t, "push", "--no-html")
	a.run(t, "import", "--search-root", root, "--update", "--no-plugins")
	t.Logf("A after first import: %v", a.sessions(t, repo))
	a.run(t, "push", "--no-html")

	// B comes back: its sweep runs.
	b.run(t, "push", "--no-html")
	a.run(t, "import", "--search-root", root, "--update", "--no-plugins")
	a.run(t, "push", "--no-html")
	// A also holds B's other session (one shared layout, one bucket); what must not
	// happen is a second copy of B's turn in S.
	copies := 0
	for _, s := range a.sessions(t, repo) {
		if strings.Contains(mustRead(t, filepath.Join(filepath.Dir(a.session(repo)), s)), `"b1"`) {
			copies++
		}
	}
	if copies != 1 {
		t.Errorf("A holds %d copies of B's conversation, want 1: %v", copies, a.sessions(t, repo))
	}
	b.run(t, "import", "--search-root", root, "--update", "--no-plugins")
	if got := b.sessions(t, repo); len(got) != 3 { // other, S, A's fork
		t.Errorf("B holds %v, want 3 (its two sessions + one fork of A's), not its own copy back", got)
	}
}

// A pinned machine name is saved in config, so the sweep - which sees no shell
// environment - uses it too, and one machine never pushes under two names.
func TestThirdReviewSavedMachineNameHoldsForTheSweep(t *testing.T) {
	archive := t.TempDir()
	a, b := newMachine(t, "MAC", archive), newMachine(t, "BOX-B", archive)
	repo := gitRepo(t, filepath.Join(t.TempDir(), "api"))
	root := filepath.Dir(repo)

	mustWrite(t, b.session(repo), sessionLines(repo, "u1", "b1"))
	old := time.Now().Add(-time.Hour)
	os.Chtimes(b.session(repo), old, old)
	b.run(t, "push", "--no-html")
	plain := func(args ...string) { // no CLAUDE_SESSIONS_MACHINE, as for a sweep or the Dock
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=", "CLAUDE_SESSIONS_MACHINE=")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	plain("config", "--claude-dir", a.claudeDir, "set-machine", "MAC")
	mustWrite(t, a.session(repo), sessionLines(repo, "u1", "a1")) // forked
	plain("push", "--claude-dir", a.claudeDir, "--archive", a.archive, "--no-html")

	// The sweep: launchd does not pass the shell's environment.
	sweep := exec.Command(binary, "push", "--claude-dir", a.claudeDir, "--archive", a.archive, "--quiet", "--sweep", "--no-html")
	sweep.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=", "CLAUDE_SESSIONS_MACHINE=")
	if out, err := sweep.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	shards, _ := os.ReadDir(filepath.Join(archive, "manifest"))
	forks, _ := os.ReadDir(filepath.Join(archive, "forks"))
	var forkFiles []string
	filepath.Walk(filepath.Join(archive, "forks"), func(p string, i os.FileInfo, _ error) error {
		if i != nil && !i.IsDir() {
			forkFiles = append(forkFiles, p)
		}
		return nil
	})
	t.Logf("shards: %d, fork buckets %d, fork files %v", len(shards), len(forks), forkFiles)
	b.run(t, "import", "--search-root", root, "--update", "--no-plugins")
	if got := b.sessions(t, repo); len(got) != 2 {
		t.Errorf("B holds %v, want its session and one fork of A's", got)
	}
	if len(shards) != 2 {
		t.Errorf("want two machines in the archive, got %d shards", len(shards))
	}
}
