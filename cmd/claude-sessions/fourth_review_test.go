package main_test

// Regressions from the fourth review: deletions that came back, forks after a push,
// renamed machines, backups, conflict copies.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
)

// configAs runs `config` for a machine directly: it takes no --archive.
func configAs(t *testing.T, claudeDir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(binary, append([]string{"config", "--claude-dir", claudeDir}, args...)...)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=", "CLAUDE_SESSIONS_MACHINE="+name)
	o, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("config %v: %v\n%s", args, err, o)
	}
	return string(o)
}

// renamedFrom renames m from old to m.name as set-machine does once m has pushed under
// old: the archive's record for old carries the profile's install ID. That record is
// then forgotten, as the rename's advice says.
func renamedFrom(t *testing.T, m machine, old string) {
	t.Helper()
	cfgPath := filepath.Join(m.claudeDir, "session-sync.json")
	cfg := map[string]any{}
	if raw, err := os.ReadFile(cfgPath); err == nil {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
	}
	cfg["installId"] = "install-of-" + m.name
	raw, _ := json.Marshal(cfg)
	mustWrite(t, cfgPath, string(raw))
	shard := filepath.Join(m.archive, "manifest", old+".json")
	mustWrite(t, shard, `{"machine":"`+old+`","installId":"install-of-`+m.name+`"}`)
	if out := configAs(t, m.claudeDir, old, "set-machine", m.name); !strings.Contains(out, "--forget "+old) {
		t.Fatalf("set-machine did not take %s for this computer's:\n%s", old, out)
	}
	if err := os.Remove(shard); err != nil {
		t.Fatal(err)
	}
}

// R4-1: B adds X, A takes it and pushes, B deletes X before it next imports. The
// deletion should stay ("what either deleted stays deleted").
func TestFourthReviewMemoryEchoResurrects(t *testing.T) {
	archive := t.TempDir()
	a, b := newMachine(t, "BOX-A", archive), newMachine(t, "BOX-B", archive)
	aRepo := gitRepo(t, filepath.Join(t.TempDir(), "x", "api"))
	bRepo := gitRepo(t, filepath.Join(t.TempDir(), "y", "api"))
	aMem := filepath.Join(a.claudeDir, "projects", claude.Slug(aRepo), "memory")
	bMem := filepath.Join(b.claudeDir, "projects", claude.Slug(bRepo), "memory")
	mustWrite(t, a.session(aRepo), sessionLines(aRepo, "u1"))
	mustWrite(t, b.session(bRepo), sessionLines(bRepo, "u1"))
	mustWrite(t, filepath.Join(aMem, "MEMORY.md"), "- [K](k.md) - k\n")
	mustWrite(t, filepath.Join(aMem, "k.md"), "k\n")
	mustWrite(t, filepath.Join(bMem, "MEMORY.md"), "- [K](k.md) - k\n")
	mustWrite(t, filepath.Join(bMem, "k.md"), "k\n")

	// Round 0: both in sync.
	a.run(t, "push", "--no-html")
	b.run(t, "push", "--no-html")
	away(t, bRepo, func() { a.run(t, "import", "--search-root", filepath.Dir(aRepo), "--update", "--no-plugins") })
	away(t, aRepo, func() { b.run(t, "import", "--search-root", filepath.Dir(bRepo), "--update", "--no-plugins") })

	// B adds X, pushes.
	later := time.Now().Add(5 * time.Minute)
	mustWrite(t, filepath.Join(bMem, "MEMORY.md"), "- [K](k.md) - k\n- [X](x.md) - x\n")
	mustWrite(t, filepath.Join(bMem, "x.md"), "x\n")
	os.Chtimes(filepath.Join(bMem, "MEMORY.md"), later, later)
	os.Chtimes(filepath.Join(bMem, "x.md"), later, later)
	b.run(t, "push", "--no-html")

	// Switch to A: import, work, push.
	away(t, bRepo, func() { a.run(t, "import", "--search-root", filepath.Dir(aRepo), "--update", "--no-plugins") })
	if !strings.Contains(mustRead(t, filepath.Join(aMem, "MEMORY.md")), "x.md") {
		t.Fatal("A did not take X")
	}
	a.run(t, "push", "--no-html")

	// Back on B: before importing, B forgets X (Claude edits memory).
	later2 := time.Now().Add(10 * time.Minute)
	mustWrite(t, filepath.Join(bMem, "MEMORY.md"), "- [K](k.md) - k\n")
	os.Remove(filepath.Join(bMem, "x.md"))
	os.Chtimes(filepath.Join(bMem, "MEMORY.md"), later2, later2)
	b.run(t, "push", "--no-html")
	away(t, aRepo, func() { b.run(t, "import", "--search-root", filepath.Dir(bRepo), "--update", "--no-plugins") })

	got := mustRead(t, filepath.Join(bMem, "MEMORY.md"))
	if strings.Contains(got, "x.md") {
		t.Errorf("B: entry deleted on B came back from A's echo:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(bMem, "x.md")); err == nil {
		t.Error("B: memory file deleted on B came back from A's echo")
	}
}

// R4-5: README: "Don't want the extra copy? Delete it. It comes back only if the
// other computer adds new turns to it." The sweep pushes the filed fork before the
// user gets round to deleting it.
func TestFourthReviewDeletedForkComesBackAfterAPush(t *testing.T) {
	archive := t.TempDir()
	mac, win := newMachine(t, "MAC", archive), newMachine(t, "WIN", archive)
	macRepo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	winRepo := gitRepo(t, filepath.Join(t.TempDir(), "work", "api"))
	mustWrite(t, mac.session(macRepo), sessionLines(macRepo, "u1", "m1"))
	mustWrite(t, win.session(winRepo), sessionLines(winRepo, "u1", "w1"))
	mac.run(t, "push", "--no-html")
	win.run(t, "push", "--no-html")
	away(t, winRepo, func() { mac.run(t, "import", "--search-root", filepath.Dir(macRepo), "--update", "--no-plugins") })
	got := mac.sessions(t, macRepo)
	if len(got) != 2 {
		t.Fatalf("want session + fork, got %v", got)
	}
	mac.run(t, "push", "--no-html") // the sweep, 30 minutes later
	var fork string
	for _, s := range got {
		if s != forkSession+".jsonl" {
			fork = s
		}
	}
	os.Remove(filepath.Join(mac.claudeDir, "projects", filepath.Base(filepath.Dir(mac.session(macRepo))), fork))

	away(t, winRepo, func() { mac.run(t, "import", "--search-root", filepath.Dir(macRepo), "--update", "--no-plugins") })
	if after := mac.sessions(t, macRepo); len(after) != 1 {
		t.Errorf("deleted fork came back with nothing new on the other machine: %v", after)
	}
}

// R4-3: import --update from an unzipped backup replaces a rule file, keeping the
// local one "in the archive" - which here is the backup folder, as profile/replaced/.
// That profile/ folder then makes the backup look like an archive with an empty
// profile, so a later import from the same backup restores no rules at all.
func TestFourthReviewBackupUpdateWritesIntoBackupAndBreaksNextImport(t *testing.T) {
	src := newFixture(t)
	c := src.claudeDir
	mustWrite(t, filepath.Join(c, "CLAUDE.md"), "# new rules\n")
	mustWrite(t, filepath.Join(c, "skills", "mine", "SKILL.md"), "---\nname: mine\n---\n")
	out := t.TempDir()
	if o, err := src.run(t, "backup", "--out", out); err != nil {
		t.Fatalf("backup failed: %v\n%s", err, o)
	}
	zips, _ := filepath.Glob(filepath.Join(out, "*.zip"))
	unzipped := t.TempDir()
	unzipTo(t, zips[0], unzipped)

	f := newImportFixture(t)
	f.makeRepo(t, "web-app", "")
	// An older, different CLAUDE.md here.
	mustWrite(t, filepath.Join(f.claudeDir, "CLAUDE.md"), "# old rules\n")
	old := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(f.claudeDir, "CLAUDE.md"), old, old)

	importFrom := func(extra ...string) string {
		args := append([]string{"import", "--claude-dir", f.claudeDir, "--archive", unzipped, "--search-root", f.dev, "--no-plugins"}, extra...)
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=")
		o, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("import %v failed: %v\n%s", extra, err, o)
		}
		return string(o)
	}
	o1 := importFrom("--update")
	t.Logf("first:\n%s", o1)
	if _, err := os.Stat(filepath.Join(unzipped, "profile")); err == nil {
		t.Errorf("import wrote profile/ into the backup folder")
	}
	// The skill is lost here, then imported again from the same backup.
	os.RemoveAll(filepath.Join(f.claudeDir, "skills"))
	o2 := importFrom("--update")
	if _, err := os.Stat(filepath.Join(f.claudeDir, "skills", "mine", "SKILL.md")); err != nil {
		t.Errorf("second import from the same backup restored no skill:\n%s", o2)
	}
}

// R4-6: a backup records its source profile only in MANIFEST.txt; import from it has
// no shard, so ~/.claude paths (saved tool output) are not repointed.
func TestFourthReviewBackupImportRepointsClaudeRoot(t *testing.T) {
	oldRoot := filepath.Join(t.TempDir(), "old-home", ".claude")
	repo := filepath.Join(t.TempDir(), "dev", "api")
	mustMkdir(t, repo)
	tool := filepath.Join(oldRoot, "projects", "x", "s", "tool-results", "r.txt")
	line := `{"type":"user","uuid":"u1","cwd":` + quoted(repo) + `,"sessionId":"` + forkSession + `","message":{"role":"user","content":` + quoted("see "+tool) + `}}` + "\n"
	mustWrite(t, filepath.Join(oldRoot, "projects", "-old-bucket", forkSession+".jsonl"), line)
	out := t.TempDir()
	cmd := exec.Command(binary, "backup", "--claude-dir", oldRoot, "--out", out)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=")
	if o, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, o)
	}
	zips, _ := filepath.Glob(filepath.Join(out, "*.zip"))
	unz := t.TempDir()
	unzipTo(t, zips[0], unz)

	newRoot := filepath.Join(t.TempDir(), ".claude")
	mustMkdir(t, filepath.Join(newRoot, "projects"))
	cmd = exec.Command(binary, "import", "--claude-dir", newRoot, "--archive", unz, "--search-root", filepath.Dir(repo), "--no-plugins")
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=")
	o, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, o)
	}
	files, _ := filepath.Glob(filepath.Join(newRoot, "projects", "*", forkSession+".jsonl"))
	if len(files) != 1 {
		t.Fatalf("not filed: %v\n%s", files, o)
	}
	if got := mustRead(t, files[0]); strings.Contains(got, oldRoot) {
		t.Errorf("saved tool output still points at the old profile after a backup import:\n%s", got)
	}
}

// R4-2: shared layout, a forked session, then B's name changes (set-machine, or a
// macOS hostname that drifted) and the README's advice is followed: push, then
// machines --forget <old>. B should not get its own conversation back.
func TestFourthReviewRenameFilesOwnForkBack(t *testing.T) {
	archive := t.TempDir()
	a, b := newMachine(t, "BOX-A", archive), newMachine(t, "BOX-B", archive)
	repo := gitRepo(t, filepath.Join(t.TempDir(), "api"))
	root := filepath.Dir(repo)

	mustWrite(t, a.session(repo), sessionLines(repo, "u1"))
	a.run(t, "push", "--no-html")
	b.run(t, "pull", "--no-plugins")

	mustWrite(t, a.session(repo), sessionLines(repo, "u1", "a1"))
	later := time.Now().Add(time.Minute)
	os.Chtimes(a.session(repo), later, later)
	a.run(t, "push", "--no-html")

	mustWrite(t, b.session(repo), sessionLines(repo, "u1", "b1"))
	later2 := time.Now().Add(2 * time.Minute)
	os.Chtimes(b.session(repo), later2, later2)
	b.run(t, "push", "--no-html") // forks/.../BOX-B.jsonl
	a.run(t, "pull", "--update", "--no-plugins")
	a.run(t, "push", "--no-html")
	b.run(t, "import", "--search-root", root, "--update", "--no-plugins")
	b.run(t, "push", "--no-html")
	before := b.sessions(t, repo)
	t.Logf("B before rename: %v", before)

	// B is renamed the documented way; from now on its name comes from config. It finds
	// its record under the old name in the archive it has saved.
	configAs(t, b.claudeDir, "BOX-B", "set-destination", archive)
	configAs(t, b.claudeDir, "BOX-B", "set-machine", "BOX-B2")
	b2 := machine{name: "", claudeDir: b.claudeDir, archive: archive}
	b2.run(t, "push", "--no-html")
	b2.run(t, "machines", "--forget", "BOX-B")
	out := b2.run(t, "import", "--search-root", root, "--update", "--no-plugins")
	after := b2.sessions(t, repo)
	if len(after) != len(before) {
		t.Errorf("after rename B holds %v (was %v): its own copy came back\n%s", after, before, out)
	}
	aBefore := a.sessions(t, repo)
	a.run(t, "pull", "--update", "--no-plugins")
	if got := a.sessions(t, repo); len(got) != len(aBefore) {
		t.Errorf("after B's rename A holds %v (was %v): B's fork filed again under its new name", got, aBefore)
	}
}

// A computed machine name is saved by the first push, so a hostname that later
// changes - macOS renames itself after a clash on the network - cannot split it.
func TestFourthReviewFirstPushSavesTheMachineName(t *testing.T) {
	m := newMachine(t, "", t.TempDir())
	mustWrite(t, filepath.Join(m.claudeDir, "projects", "x", forkSession+".jsonl"), uuidLines("u1"))
	m.run(t, "push", "--no-html")
	if got := mustRead(t, filepath.Join(m.claudeDir, "session-sync.json")); !strings.Contains(got, `"machine"`) {
		t.Errorf("the first push did not save the machine name:\n%s", got)
	}
}

// 6. A sync client's conflict copy is not a session: filed, it duplicated one and
// came back through every push.
func TestFourthReviewConflictCopyIsNotFiledAsASession(t *testing.T) {
	f := newImportFixture(t)
	repo := f.makeRepo(t, "api", "")
	f.archiveProject(t, "D--work-api", `D:\work\api`, mapSession, "hello")
	dir := filepath.Join(f.archive, "projects", "D--work-api")
	mustWrite(t, filepath.Join(dir, mapSession+" (conflicted copy 2026-11-20).jsonl"), mustRead(t, filepath.Join(dir, mapSession+".jsonl")))
	f.writeManifest(t, map[string]any{"D--work-api": map[string]any{"path": `D:\work\api`, "leaf": "api"}})
	if out, err := f.run(t, "import", "--search-root", f.dev, "--no-plugins"); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if got := f.localBucket(t, claude.Slug(repo)); len(got) != 1 {
		t.Errorf("a conflict copy was filed as a session: %v", got)
	}
}

// 8. A machine name read from a file name in the shared archive is printed without
// control characters: anyone who can write the folder could otherwise drive the
// terminal.
func TestFourthReviewMachineNamesFromTheArchiveArePrintedSafely(t *testing.T) {
	f := newImportFixture(t)
	repo := f.makeRepo(t, "api", "git@github.com:example/api.git")
	bucket := claude.Slug(repo)
	mustWrite(t, filepath.Join(f.archive, "projects", bucket, forkSession+".jsonl"), uuidLines("u1", "u2", "b1"))
	evil := filepath.Join(f.archive, "forks", bucket, forkSession, "\x1b[2JEVIL.jsonl")
	mustMkdir(t, filepath.Dir(evil))
	if err := os.WriteFile(evil, []byte(uuidLines("u1", "u2", "e1")), 0o644); err != nil {
		t.Skipf("this file system refuses control characters in a name, so the attack cannot happen here: %v", err)
	}
	f.writeShardAs(t, "OTHER-BOX", map[string]any{bucket: map[string]any{"path": repo, "leaf": "api"}})
	out, err := f.run(t, "import", "--search-root", f.dev, "--no-plugins")
	if err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("an escape sequence from the archive reached the terminal: %q", out)
	}
}

// A rule or skill file deleted here, once this machine has held it, is not brought
// back by the next import - as for memory.
func TestFourthReviewDeletedSkillStaysDeleted(t *testing.T) {
	f := profileImportFixture(t)
	mustWrite(t, filepath.Join(f.archive, "profile", "skills", "old", "SKILL.md"), "old skill\n")
	if out, err := f.run(t, "import", "--search-root", f.dev, "--no-plugins"); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	skill := filepath.Join(f.claudeDir, "skills", "old", "SKILL.md")
	if _, err := os.Stat(skill); err != nil {
		t.Fatalf("the skill was not imported in the first place: %v", err)
	}
	os.RemoveAll(filepath.Join(f.claudeDir, "skills", "old"))
	if out, err := f.run(t, "import", "--search-root", f.dev, "--no-plugins", "--update"); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if _, err := os.Stat(skill); err == nil {
		t.Error("a skill deleted here came back on the next import")
	}
}

// A plugin turned off here is not installed again because another machine has it on.
func TestFourthReviewPluginDisabledHereIsNotReinstalled(t *testing.T) {
	f := profileImportFixture(t)
	mustWrite(t, filepath.Join(f.archive, "profile", "plugins", "OLD-BOX.json"),
		`{"enabledPlugins":{"ponytail@ponytail":true},"extraKnownMarketplaces":{"ponytail":{"source":{"source":"github","repo":"DietrichGebert/ponytail"}}}}`)
	mustWrite(t, filepath.Join(f.claudeDir, "settings.json"), `{"enabledPlugins":{"ponytail@ponytail":false}}`)
	out, err := f.run(t, "import", "--search-root", f.dev, "--dry-run")
	if err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if strings.Contains(out, "ponytail@ponytail") {
		t.Errorf("a plugin turned off here was offered for installing:\n%s", out)
	}
}
