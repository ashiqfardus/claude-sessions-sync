package main_test

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

// archived is the archive's copy of the session in repo's bucket, "" if there is none.
func archived(t *testing.T, archive, repo string) string {
	t.Helper()
	p := filepath.Join(archive, "projects", claude.Slug(repo), forkSession+".jsonl")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return mustRead(t, p)
}

// doctor runs doctor, which exits non-zero on any failed check.
func (m machine) doctor(t *testing.T) string {
	t.Helper()
	cmd := exec.Command(binary, "doctor", "--claude-dir", m.claudeDir, "--archive", m.archive)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=", "CLAUDE_SESSIONS_MACHINE="+m.name)
	out, _ := cmd.CombinedOutput()
	return string(out)
}

// later stamps a file minutes ahead, as a reply written after the last push would be.
func later(t *testing.T, p string) {
	t.Helper()
	at := time.Now().Add(5 * time.Minute)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

// Two machines with different paths: the session imported from MAC is not archived
// again under WIN's folder - until WIN continues it. clean then deletes MAC's shorter
// copy, MAC does not upload it again, and the turn still reaches MAC.
func TestImportedSessionIsArchivedOnce(t *testing.T) {
	archive := t.TempDir()
	mac, win := newMachine(t, "MAC", archive), newMachine(t, "WIN", archive)
	macRepo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	winRepo := gitRepo(t, filepath.Join(t.TempDir(), "work", "api"))
	mustWrite(t, mac.session(macRepo), sessionLines(macRepo, "u1", "m1"))
	// A subagent and saved tool output travel with it.
	mustWrite(t, filepath.Join(filepath.Dir(mac.session(macRepo)), forkSession, "subagents", "agent-a1.jsonl"), sessionLines(macRepo, "s1"))
	mustWrite(t, filepath.Join(filepath.Dir(mac.session(macRepo)), forkSession, "tool-results", "r1.txt"), "output\n")

	mac.run(t, "push", "--no-html")
	away(t, macRepo, func() { win.run(t, "import", "--search-root", filepath.Dir(winRepo), "--no-plugins") })
	if len(win.sessions(t, winRepo)) != 1 {
		t.Fatalf("WIN did not receive the session: %v", win.sessions(t, winRepo))
	}
	win.run(t, "push", "--no-html")
	win.run(t, "push", "--no-html") // and again, from what it remembered
	if got := archived(t, archive, winRepo); got != "" {
		t.Fatalf("WIN archived the session it imported a second time, under its own folder:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(archive, "projects", claude.Slug(winRepo), forkSession)); err == nil {
		t.Error("WIN archived the imported session's own folder a second time")
	}
	if strings.Contains(mustRead(t, filepath.Join(archive, "index", "WIN.json")), forkSession) {
		t.Error("WIN's index lists a session whose archived copy is MAC's")
	}
	if out := win.run(t, "clean"); !strings.Contains(out, "No duplicate copies") {
		t.Errorf("clean found duplicates where there are none:\n%s", out)
	}
	// Ended an hour ago: doctor must not call it unarchived.
	ago := time.Now().Add(-time.Hour)
	os.Chtimes(win.session(winRepo), ago, ago)
	win.run(t, "push", "--no-html")
	if out := win.doctor(t); strings.Contains(out, "not in the archive") {
		t.Errorf("doctor reports the held session as unarchived:\n%s", out)
	}

	// WIN continues it: now WIN's copy holds more, and is archived.
	local := win.session(winRepo)
	f, _ := os.OpenFile(local, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(sessionLines(winRepo, "w1"))
	f.Close()
	later(t, local)
	win.run(t, "push", "--no-html")
	if !strings.Contains(archived(t, archive, winRepo), `"w1"`) {
		t.Fatal("WIN's continuation was not archived")
	}

	out := win.run(t, "clean")
	if !strings.Contains(out, "Would delete 1 duplicate") || !strings.Contains(out, claude.Slug(macRepo)) {
		t.Fatalf("clean does not offer MAC's shorter copy:\n%s", out)
	}
	if archived(t, archive, macRepo) == "" {
		t.Fatal("clean deleted without --yes")
	}
	win.run(t, "clean", "--yes")
	if archived(t, archive, macRepo) != "" {
		t.Fatal("clean --yes left MAC's shorter copy")
	}
	if _, err := os.Stat(filepath.Join(archive, "projects", claude.Slug(macRepo), forkSession)); err == nil {
		t.Error("clean left the deleted copy's own folder")
	}
	if !strings.Contains(archived(t, archive, winRepo), `"m1"`) {
		t.Fatal("the copy kept lost a turn")
	}
	for _, rel := range []string{"subagents/agent-a1.jsonl", "tool-results/r1.txt"} {
		if _, err := os.Stat(filepath.Join(archive, "projects", claude.Slug(winRepo), forkSession, filepath.FromSlash(rel))); err != nil {
			t.Errorf("the copy kept has no %s: %v", rel, err)
		}
	}

	mac.run(t, "push", "--no-html")
	if got := archived(t, archive, macRepo); got != "" {
		t.Fatalf("MAC uploaded its shorter copy again:\n%s", got)
	}
	away(t, winRepo, func() { mac.run(t, "import", "--search-root", filepath.Dir(macRepo), "--update", "--no-plugins") })
	if !mac.holds(t, macRepo, "w1") {
		t.Error("WIN's turn did not reach MAC")
	}
	mac.run(t, "push", "--no-html")
	if got := archived(t, archive, macRepo); got != "" {
		t.Fatalf("MAC archived WIN's continuation again under its own folder:\n%s", got)
	}
}

// A copy is never deleted for one that lacks turns of it, nor while a machine that
// would upload it again runs an older build.
func TestCleanKeepsForksAndWaitsForOlderBuilds(t *testing.T) {
	archive := t.TempDir()
	mac, win := newMachine(t, "MAC", archive), newMachine(t, "WIN", archive)
	macRepo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	winRepo := gitRepo(t, filepath.Join(t.TempDir(), "work", "api"))
	mustWrite(t, mac.session(macRepo), sessionLines(macRepo, "u1", "m1"))
	mustWrite(t, win.session(winRepo), sessionLines(winRepo, "u1", "w1"))
	mac.run(t, "push", "--no-html")
	win.run(t, "push", "--no-html")
	if out := win.run(t, "clean", "--yes"); !strings.Contains(out, "No duplicate copies") {
		t.Errorf("clean offered a copy of a forked session:\n%s", out)
	}
	if archived(t, archive, macRepo) == "" || archived(t, archive, winRepo) == "" {
		t.Fatal("clean deleted a copy of a forked session")
	}

	// MAC's copy becomes one WIN's holds in whole - but MAC's shard is from a build that
	// would upload it again.
	mustWrite(t, filepath.Join(archive, "projects", claude.Slug(macRepo), forkSession+".jsonl"), sessionLines(macRepo, "u1"))
	shard := filepath.Join(archive, "manifest", "MAC.json")
	mustWrite(t, shard, strings.Replace(mustRead(t, shard), `"skipsHeld": true`, `"skipsHeld": false`, 1))
	out := win.run(t, "clean", "--yes")
	if !strings.Contains(out, "Kept for now") || !strings.Contains(out, "MAC") {
		t.Errorf("clean did not say it waits for MAC:\n%s", out)
	}
	if archived(t, archive, macRepo) == "" {
		t.Fatal("clean deleted a copy that MAC's older build would upload again")
	}
}

// clean runs clean and returns its output and whether it failed.
func (m machine) clean(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(binary, append([]string{"clean", "--claude-dir", m.claudeDir, "--archive", m.archive}, args...)...)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=", "CLAUDE_SESSIONS_MACHINE="+m.name)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// continuedOnWin is a session from MAC, with a subagent, imported and continued on WIN:
// MAC's archived copy is the one clean offers to delete.
func continuedOnWin(t *testing.T) (archive string, mac, win machine, macRepo, winRepo string) {
	t.Helper()
	archive = t.TempDir()
	mac, win = newMachine(t, "MAC", archive), newMachine(t, "WIN", archive)
	macRepo = gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	winRepo = gitRepo(t, filepath.Join(t.TempDir(), "work", "api"))
	mustWrite(t, mac.session(macRepo), sessionLines(macRepo, "u1", "m1"))
	mustWrite(t, filepath.Join(filepath.Dir(mac.session(macRepo)), forkSession, "subagents", "agent-a1.jsonl"), sessionLines(macRepo, "s1"))
	mac.run(t, "push", "--no-html")
	away(t, macRepo, func() { win.run(t, "import", "--search-root", filepath.Dir(winRepo), "--no-plugins") })
	local := win.session(winRepo)
	f, _ := os.OpenFile(local, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(sessionLines(winRepo, "w1"))
	f.Close()
	later(t, local)
	win.run(t, "push", "--no-html")
	if out := win.run(t, "clean"); !strings.Contains(out, "Would delete 1 duplicate") {
		t.Fatalf("setup: clean does not offer MAC's copy:\n%s", out)
	}
	return
}

// A subagent that went on in MAC's copy after WIN imported it: deleting MAC's copy
// would lose those turns, though the name is in WIN's folder.
func TestCleanKeepsACopyWhoseSubagentHasMore(t *testing.T) {
	archive, _, win, macRepo, _ := continuedOnWin(t)
	agent := filepath.Join(archive, "projects", claude.Slug(macRepo), forkSession, "subagents", "agent-a1.jsonl")
	mustWrite(t, agent, sessionLines(macRepo, "s1", "s2"))
	out, err := win.clean(t, "--yes")
	if err == nil || !strings.Contains(out, "lacks turns of subagents/agent-a1.jsonl") {
		t.Errorf("clean did not say why it kept the copy (err %v):\n%s", err, out)
	}
	if archived(t, archive, macRepo) == "" || !strings.Contains(mustRead(t, agent), `"s2"`) {
		t.Fatal("clean deleted the only copy of a subagent's turns")
	}
}

// A file naming every turn, in a bucket no machine pushes to, is never a reason to
// delete the real copies: its content can be anything.
func TestCleanIgnoresAPlantedCopy(t *testing.T) {
	archive, _, win, macRepo, winRepo := continuedOnWin(t)
	evil := filepath.Join(archive, "projects", "evil", forkSession+".jsonl")
	mustWrite(t, evil, sessionLines("/x", "u1", "m1", "w1", "zz"))
	later(t, evil)
	if _, err := win.clean(t, "--yes"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(archived(t, archive, winRepo), `"w1"`) {
		t.Fatal("clean deleted WIN's real copy for the planted one")
	}
	if archived(t, archive, macRepo) != "" {
		t.Error("MAC's copy, which WIN's holds, was not deleted")
	}
}

// A manifest caught half synced may be a machine that would upload them again.
func TestCleanDeletesNothingWithAnUnreadableManifest(t *testing.T) {
	archive, _, win, macRepo, _ := continuedOnWin(t)
	mustWrite(t, filepath.Join(archive, "manifest", "OTHER.json"), "{\"machine\": \"OTH")
	if out, _ := win.clean(t); !strings.Contains(out, "Nothing can be deleted") || strings.Contains(out, "clean --yes") {
		t.Errorf("the preview offers what --yes will refuse:\n%s", out)
	}
	out, err := win.clean(t, "--yes")
	if err == nil || !strings.Contains(out, "nothing deleted") || strings.Contains(out, "Deleting") {
		t.Errorf("clean went ahead, or said it would (err %v):\n%s", err, out)
	}
	if archived(t, archive, macRepo) == "" {
		t.Fatal("clean deleted with a manifest it could not read")
	}
}

// importedOnWin is MAC's session, with a subagent, imported on WIN and left as it was.
func importedOnWin(t *testing.T) (archive string, mac, win machine, macRepo, winRepo string) {
	t.Helper()
	archive = t.TempDir()
	mac, win = newMachine(t, "MAC", archive), newMachine(t, "WIN", archive)
	macRepo = gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	winRepo = gitRepo(t, filepath.Join(t.TempDir(), "work", "api"))
	mustWrite(t, mac.session(macRepo), sessionLines(macRepo, "u1", "m1"))
	mustWrite(t, filepath.Join(filepath.Dir(mac.session(macRepo)), forkSession, "subagents", "agent-a1.jsonl"), sessionLines(macRepo, "s1"))
	mac.run(t, "push", "--no-html")
	away(t, macRepo, func() { win.run(t, "import", "--search-root", filepath.Dir(winRepo), "--no-plugins") })
	return
}

// A copy archived by a build that left subagents out does not hold WIN's: WIN archives
// its own, folder and all.
func TestHeldTakesTheSessionFolderToo(t *testing.T) {
	archive, _, win, macRepo, winRepo := importedOnWin(t)
	if err := os.RemoveAll(filepath.Join(archive, "projects", claude.Slug(macRepo), forkSession)); err != nil {
		t.Fatal(err)
	}
	win.run(t, "push", "--no-html")
	if archived(t, archive, winRepo) == "" {
		t.Fatal("WIN left its session to a copy that lacks its subagent")
	}
	if _, err := os.Stat(filepath.Join(archive, "projects", claude.Slug(winRepo), forkSession, "subagents", "agent-a1.jsonl")); err != nil {
		t.Errorf("WIN's subagent is not archived: %v", err)
	}
}

// MAC goes on with the session: the copy WIN left it to grows, and still holds WIN's.
func TestDoctorTrustsAHeldCopyThatGrew(t *testing.T) {
	archive, _, win, macRepo, winRepo := importedOnWin(t)
	ago := time.Now().Add(-time.Hour)
	os.Chtimes(win.session(winRepo), ago, ago)
	win.run(t, "push", "--no-html")
	if archived(t, archive, winRepo) != "" {
		t.Fatal("setup: WIN archived the session it imported")
	}
	macCopy := filepath.Join(archive, "projects", claude.Slug(macRepo), forkSession+".jsonl")
	f, err := os.OpenFile(macCopy, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(sessionLines(macRepo, "m2"))
	f.Close()
	later(t, macCopy)
	if out := win.doctor(t); strings.Contains(out, "not in the archive") {
		t.Errorf("doctor reports the session as unarchived because MAC's copy grew:\n%s", out)
	}
}

// archivedTemps lists the ".tmp-" files anywhere in the archive.
func archivedTemps(t *testing.T, archive string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(archive, func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), ".tmp-") {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// A power cut during import left a half-written subagent in the session's folder: the
// copy WIN imported from still holds the session, which is not archived again - nor
// that file with it.
func TestALeftoverTempDoesNotUnholdAnImportedSession(t *testing.T) {
	archive, _, win, _, winRepo := importedOnWin(t)
	mustWrite(t, filepath.Join(filepath.Dir(win.session(winRepo)), forkSession, "subagents", ".tmp-agent-a1.jsonl-4159126922"), "{\"half")
	win.run(t, "push", "--no-html")
	if got := archived(t, archive, winRepo); got != "" {
		t.Fatalf("WIN archived the session it imported again, for a temp file:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(archive, "projects", claude.Slug(winRepo), forkSession)); err == nil {
		t.Error("WIN archived the imported session's folder again")
	}
	if tmps := archivedTemps(t, archive); len(tmps) > 0 {
		t.Errorf("a temp file reached the archive: %v", tmps)
	}
}

// Nor does one travel with a session push does archive, from a full push or the hook.
func TestPushNeverArchivesATempFile(t *testing.T) {
	archive := t.TempDir()
	mac := newMachine(t, "MAC", archive)
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	mustWrite(t, mac.session(repo), sessionLines(repo, "u1"))
	sub := filepath.Join(filepath.Dir(mac.session(repo)), forkSession, "subagents")
	mustWrite(t, filepath.Join(sub, "agent-a1.jsonl"), sessionLines(repo, "s1"))
	mustWrite(t, filepath.Join(sub, ".tmp-agent-a2.jsonl-77"), "{\"half")
	mac.run(t, "push", "--no-html")
	if _, err := os.Stat(filepath.Join(archive, "projects", claude.Slug(repo), forkSession, "subagents", "agent-a1.jsonl")); err != nil {
		t.Fatalf("the subagent was not archived: %v", err)
	}
	f, _ := os.OpenFile(mac.session(repo), os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(sessionLines(repo, "u2"))
	f.Close()
	later(t, mac.session(repo))
	mac.hook(t, mac.session(repo))
	if !strings.Contains(archived(t, archive, repo), `"u2"`) {
		t.Fatal("setup: the hook did not archive the new turn")
	}
	if tmps := archivedTemps(t, archive); len(tmps) > 0 {
		t.Errorf("a temp file reached the archive: %v", tmps)
	}
}

// Push and import remove the temp files an interrupted run left in the projects folder
// over an hour ago. Younger ones may be mid-write, and other names are not ours: both
// stay, as does everything with a dry run.
func TestPushAndImportRemoveLeftoverTemps(t *testing.T) {
	archive := t.TempDir()
	mac := newMachine(t, "MAC", archive)
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	mustWrite(t, mac.session(repo), sessionLines(repo, "u1"))
	sub := filepath.Join(filepath.Dir(mac.session(repo)), forkSession, "subagents")
	old, young, foreign := filepath.Join(sub, ".tmp-agent-a1.jsonl-1"), filepath.Join(sub, ".tmp-agent-a2.jsonl-2"), filepath.Join(sub, ".tmp-notes")
	for _, p := range []string{old, young, foreign} {
		mustWrite(t, p, "{\"half")
	}
	aged(t, old)
	aged(t, foreign)
	mac.run(t, "push", "--no-html", "--dry-run")
	if _, err := os.Stat(old); err != nil {
		t.Fatal("a dry run removed a file")
	}
	mac.run(t, "push", "--no-html")
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("push left the old temp file")
	}
	for _, p := range []string{young, foreign} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("push removed %s", filepath.Base(p))
		}
	}

	again := filepath.Join(filepath.Dir(mac.session(repo)), ".tmp-"+forkSession+".jsonl-3")
	mustWrite(t, again, "{\"half")
	aged(t, again)
	mac.run(t, "import", "--search-root", filepath.Dir(repo), "--no-plugins")
	if _, err := os.Stat(again); !os.IsNotExist(err) {
		t.Error("import left the old temp file")
	}
	if _, err := os.Stat(young); err != nil {
		t.Error("import removed the young temp file")
	}
}

// An archived copy holding a temp file an interrupted run left there is still deleted
// for one that holds the rest of it: the temp file is no part of the session.
func TestCleanIgnoresATempFileInACopy(t *testing.T) {
	archive, _, win, macRepo, _ := continuedOnWin(t)
	mustWrite(t, filepath.Join(archive, "projects", claude.Slug(macRepo), forkSession, "subagents", ".tmp-agent-a2.jsonl-5"), "{\"half")
	if out := win.run(t, "clean"); !strings.Contains(out, "Would delete 1 duplicate") {
		t.Fatalf("clean keeps MAC's copy for a temp file:\n%s", out)
	}
	if out, err := win.clean(t, "--yes"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if archived(t, archive, macRepo) != "" {
		t.Error("clean --yes kept MAC's copy for a temp file")
	}
}

// A session continued on MAC and on WIN, each with a subagent of its own, and imported
// on NEW from both: NEW's folder holds both subagents, and neither copy holds them all.
// NEW archives none of it, and clean deletes neither copy. Once the copy holding a part
// changes, NEW compares again, and archives its own copy if that part is gone.
func TestImportedFromTwoCopiesIsNotArchivedAgain(t *testing.T) {
	archive := t.TempDir()
	mac, win, lap := newMachine(t, "MAC", archive), newMachine(t, "WIN", archive), newMachine(t, "NEW", archive)
	macRepo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	winRepo := gitRepo(t, filepath.Join(t.TempDir(), "work", "api"))
	newRepo := gitRepo(t, filepath.Join(t.TempDir(), "code", "api"))
	mustWrite(t, mac.session(macRepo), sessionLines(macRepo, "u1", "m1"))
	mustWrite(t, filepath.Join(filepath.Dir(mac.session(macRepo)), forkSession, "subagents", "agent-a1.jsonl"), sessionLines(macRepo, "s1"))
	mustWrite(t, win.session(winRepo), sessionLines(winRepo, "u1", "m1"))
	mustWrite(t, filepath.Join(filepath.Dir(win.session(winRepo)), forkSession, "subagents", "agent-b1.jsonl"), sessionLines(winRepo, "t1"))
	mac.run(t, "push", "--no-html")
	win.run(t, "push", "--no-html")
	if archived(t, archive, macRepo) == "" || archived(t, archive, winRepo) == "" {
		t.Fatal("setup: MAC's and WIN's copies are not both archived")
	}
	away(t, macRepo, func() {
		away(t, winRepo, func() { lap.run(t, "import", "--search-root", filepath.Dir(newRepo), "--no-plugins") })
	})
	local := filepath.Join(filepath.Dir(lap.session(newRepo)), forkSession, "subagents")
	for _, a := range []string{"agent-a1.jsonl", "agent-b1.jsonl"} {
		if _, err := os.Stat(filepath.Join(local, a)); err != nil {
			t.Fatalf("setup: NEW's import did not bring %s: %v", a, err)
		}
	}

	lap.run(t, "push", "--no-html")
	lap.run(t, "push", "--no-html") // and again, from what it remembered
	if got := archived(t, archive, newRepo); got != "" {
		t.Fatalf("NEW archived the session it imported from two copies again:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(archive, "projects", claude.Slug(newRepo), forkSession)); err == nil {
		t.Error("NEW archived the session's folder again")
	}
	if out := lap.run(t, "clean", "--yes"); !strings.Contains(out, "No duplicate copies") {
		t.Errorf("clean found a duplicate where each copy holds a part:\n%s", out)
	}
	if archived(t, archive, macRepo) == "" || archived(t, archive, winRepo) == "" {
		t.Fatal("clean deleted a copy that holds a part of the session")
	}

	var held map[string]struct {
		In   string `json:"in"`
		Also []struct {
			In string `json:"in"`
		} `json:"also"`
	}
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(lap.claudeDir, "session-sync-base", "held.json"))), &held); err != nil {
		t.Fatal(err)
	}
	h := held[claude.Slug(newRepo)+"/"+forkSession]
	if h.In == "" || len(h.Also) != 1 {
		t.Fatalf("held.json does not record both copies: %+v", h)
	}
	// The copy that holds only a part loses it, and is replaced: NEW must not trust what
	// it found before.
	part := filepath.Join(archive, "projects", h.Also[0].In, forkSession)
	if err := os.RemoveAll(part); err != nil {
		t.Fatal(err)
	}
	later(t, filepath.Join(archive, "projects", h.Also[0].In, forkSession+".jsonl"))
	lap.run(t, "push", "--no-html")
	for _, a := range []string{"agent-a1.jsonl", "agent-b1.jsonl"} {
		if _, err := os.Stat(filepath.Join(archive, "projects", claude.Slug(newRepo), forkSession, "subagents", a)); err != nil {
			t.Errorf("NEW did not archive %s once the copy holding it changed: %v", a, err)
		}
	}
}
