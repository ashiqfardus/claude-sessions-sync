package main_test

// A conversation idle long enough is zipped in the archive, <id>.conv.zip. Push reads
// the zip as it reads a loose copy: one unchanged is left zipped, one continued is
// unzipped before the usual rules judge it, and another machine's zipped copy still
// holds a session imported here.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/convzip"
)

// zipArchived zips the archive's copy of the session in repo's bucket.
func zipArchived(t *testing.T, archive, repo string) string {
	t.Helper()
	bucket := filepath.Join(archive, "projects", claude.Slug(repo))
	if _, err := convzip.Zip(bucket, forkSession); err != nil {
		t.Fatal(err)
	}
	return bucket
}

var pushCounts = regexp.MustCompile(`PUSH: \d+ copied, \d+ unchanged`)

// pushedSession is a session with a subagent on one machine, pushed.
func pushedSession(t *testing.T) (archive string, m machine, repo string) {
	t.Helper()
	archive = t.TempDir()
	m = newMachine(t, "MAC", archive)
	repo = gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	mustWrite(t, m.session(repo), sessionLines(repo, "u1", "m1"))
	mustWrite(t, filepath.Join(filepath.Dir(m.session(repo)), forkSession, "subagents", "agent-a1.jsonl"), sessionLines(repo, "s1"))
	m.run(t, "push", "--no-html")
	return
}

func TestPushLeavesAnUnchangedZippedCopy(t *testing.T) {
	archive, mac, repo := pushedSession(t)
	loose := pushCounts.FindString(mac.run(t, "push", "--no-html"))
	bucket := zipArchived(t, archive, repo)
	zipped := pushCounts.FindString(mac.run(t, "push", "--no-html"))
	if zipped == "" || zipped != loose {
		t.Errorf("the zipped copy is not counted as the loose one was: %q, loose %q", zipped, loose)
	}
	ents, _ := os.ReadDir(bucket)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != forkSession+convzip.Suffix {
		t.Fatalf("the archive holds %v, want only the zip", names)
	}
}

// zippedAndContinued is a pushed session whose archived copy also holds a tool output
// the local folder lacks, zipped, then continued here by two turns.
func zippedAndContinued(t *testing.T) (archive string, m machine, repo string) {
	t.Helper()
	archive, m, repo = pushedSession(t)
	mustWrite(t, filepath.Join(archive, "projects", claude.Slug(repo), forkSession, "tool-results", "r0.txt"), "archived only\n")
	zipArchived(t, archive, repo)
	f, err := os.OpenFile(m.session(repo), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(sessionLines(repo, "w1", "w2"))
	f.Close()
	later(t, m.session(repo))
	return
}

// unzipped checks the archive holds the continued session loose, every turn and every
// file of its folder, and no zip.
func unzipped(t *testing.T, archive, repo string) {
	t.Helper()
	got := archived(t, archive, repo)
	for _, turn := range []string{"u1", "m1", "w1", "w2"} {
		if !strings.Contains(got, `"`+turn+`"`) {
			t.Errorf("the archived transcript lacks %s:\n%s", turn, got)
		}
	}
	bucket := filepath.Join(archive, "projects", claude.Slug(repo))
	for _, rel := range []string{"subagents/agent-a1.jsonl", "tool-results/r0.txt"} {
		if _, err := os.Stat(filepath.Join(bucket, forkSession, filepath.FromSlash(rel))); err != nil {
			t.Errorf("the archive has no %s: %v", rel, err)
		}
	}
	if _, err := os.Lstat(convzip.Path(bucket, forkSession)); err == nil {
		t.Error("the zip is still beside the loose copy")
	}
}

func TestPushContinuesAZippedConversation(t *testing.T) {
	archive, mac, repo := zippedAndContinued(t)
	mac.run(t, "push", "--no-html")
	unzipped(t, archive, repo)
}

func TestHookPushContinuesAZippedConversation(t *testing.T) {
	archive, mac, repo := zippedAndContinued(t)
	mac.hook(t, mac.session(repo))
	unzipped(t, archive, repo)
}

// MAC's copy, which holds the session WIN imported, is zipped: WIN still leaves its
// copy to it, and reads the zip in a temp folder it removes.
func TestHeldCheckReadsAZippedCopy(t *testing.T) {
	archive, _, win, macRepo, winRepo := importedOnWin(t)
	zipArchived(t, archive, macRepo)
	tmp := t.TempDir()
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, tmp)
	}
	win.run(t, "push", "--no-html")
	// Ended an hour ago, and pushed again from what it remembered: doctor must not call
	// it unarchived.
	ago := time.Now().Add(-time.Hour)
	os.Chtimes(win.session(winRepo), ago, ago)
	win.run(t, "push", "--no-html")
	// From what it remembered, by the zip's own size and time: one that read the zip
	// would now fail on it, and WIN would archive its own copy.
	garble(t, convzip.Path(filepath.Join(archive, "projects", claude.Slug(macRepo)), forkSession))
	if out := win.run(t, "push", "--no-html"); strings.Contains(out, "WARN") {
		t.Errorf("the held check read the zip:\n%s", out)
	}

	if got := archived(t, archive, winRepo); got != "" {
		t.Fatalf("WIN archived the session it imported a second time, under its own folder:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(archive, "projects", claude.Slug(winRepo), forkSession)); err == nil {
		t.Error("WIN archived the imported session's own folder a second time")
	}
	if held := mustRead(t, filepath.Join(win.claudeDir, "session-sync-base", "held.json")); !strings.Contains(held, claude.Slug(macRepo)) {
		t.Errorf("held.json does not record MAC's zipped copy:\n%s", held)
	}
	if out := win.doctor(t); strings.Contains(out, "not in the archive") {
		t.Errorf("doctor reports the held session as unarchived:\n%s", out)
	}
	if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
		t.Errorf("push left %d temporary item(s) behind", len(ents))
	}
}

// Claude Code deletes old transcripts locally; one whose archived copy is zipped keeps
// its row in this machine's index, as a loose one does.
func TestPushKeepsTheIndexRowOfAZippedCopy(t *testing.T) {
	archive, mac, repo := pushedSession(t)
	zipArchived(t, archive, repo)
	os.Remove(mac.session(repo))
	os.RemoveAll(filepath.Join(filepath.Dir(mac.session(repo)), forkSession))
	mac.run(t, "push", "--no-html")
	if idx := mustRead(t, filepath.Join(archive, "index", "MAC.json")); !strings.Contains(idx, forkSession) {
		t.Errorf("the index lost the zipped session:\n%s", idx)
	}
}

// appendTurns adds turns to a local transcript, as replies written after the last push.
func appendTurns(t *testing.T, path, repo string, turns ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(sessionLines(repo, turns...))
	f.Close()
	later(t, path)
}

// Another machine that had not seen the zip yet pushed its own continuation loose
// beside it, and this one went on too: the zip, that loose copy and this machine's
// turns are all kept, and the log says why once.
func TestPushKeepsADivergedZip(t *testing.T) {
	archive, mac, repo := pushedSession(t)
	appendTurns(t, mac.session(repo), repo, "m2")
	mac.run(t, "push", "--no-html")
	bucket := zipArchived(t, archive, repo)
	loose := filepath.Join(bucket, forkSession+".jsonl")
	mustWrite(t, loose, sessionLines(repo, "u1", "m1", "x1"))
	later(t, loose)
	appendTurns(t, mac.session(repo), repo, "m3")

	// Remembered: the WARN is said once, not on every run.
	out := mac.run(t, "push", "--no-html") + mac.run(t, "push", "--no-html")
	if n := strings.Count(out, "WARN: "+forkSession); n != 1 {
		t.Errorf("%d WARN line(s) for the session across two pushes, want 1:\n%s", n, out)
	}
	if _, err := os.Lstat(convzip.Path(bucket, forkSession)); err != nil {
		t.Fatalf("the zip, the only copy of m2 in the archive but the fork, is gone: %v", err)
	}
	if !strings.Contains(mustRead(t, loose), `"x1"`) {
		t.Error("the other machine's loose continuation was overwritten")
	}
	fork := mustRead(t, filepath.Join(archive, "forks", claude.Slug(repo), forkSession, "MAC.jsonl"))
	if !strings.Contains(fork, `"m3"`) {
		t.Errorf("this machine's turns were not kept as a fork:\n%s", fork)
	}
}

// onlyZip fails unless the bucket holds the session as its zip alone.
func onlyZip(t *testing.T, bucket string) {
	t.Helper()
	ents, _ := os.ReadDir(bucket)
	if len(ents) != 1 || ents[0].Name() != forkSession+convzip.Suffix {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Fatalf("the archive holds %v, want only the zip", names)
	}
}

// The zipped copy went on elsewhere, on the same layout: this machine's copy has no turn
// it lacks, so the zip stays and nothing is written. The pair is remembered, and the next
// push reads nothing of the zip: one garbled at the same size and time goes unnoticed.
func TestPushLeavesAZipAheadOfTheLocalCopy(t *testing.T) {
	archive, mac, repo := pushedSession(t)
	bucket := filepath.Join(archive, "projects", claude.Slug(repo))
	ahead := filepath.Join(bucket, forkSession+".jsonl")
	mustWrite(t, ahead, sessionLines(repo, "u1", "m1", "x1"))
	later(t, ahead)
	zipArchived(t, archive, repo)
	zp := convzip.Path(bucket, forkSession)

	for i := 0; i < 2; i++ {
		out := mac.run(t, "push", "--no-html")
		if strings.Contains(out, "WARN") || !strings.Contains(out, "PUSH: 0 copied") {
			t.Errorf("push %d unzipped, copied or failed:\n%s", i+1, out)
		}
		onlyZip(t, bucket)
		if i == 0 {
			garble(t, zp)
		}
	}
}

// A pair remembered as held never hides a continuation: the turn after it is archived.
func TestARememberedZipStillTakesAContinuation(t *testing.T) {
	archive, mac, repo := pushedSession(t)
	bucket := zipArchived(t, archive, repo)
	later(t, mac.session(repo)) // the same turns at a new time: compared, and remembered
	mac.run(t, "push", "--no-html")
	onlyZip(t, bucket)
	if rec := mustRead(t, filepath.Join(mac.claudeDir, "session-sync-base", "zip-compared.json")); !strings.Contains(rec, forkSession) {
		t.Fatalf("setup: the pair is not remembered:\n%s", rec)
	}
	appendTurns(t, mac.session(repo), repo, "w1")
	mac.run(t, "push", "--no-html")
	got := archived(t, archive, repo)
	for _, turn := range []string{"u1", "m1", "w1"} {
		if !strings.Contains(got, `"`+turn+`"`) {
			t.Errorf("the archived transcript lacks %s:\n%s", turn, got)
		}
	}
	if _, err := os.Lstat(convzip.Path(bucket, forkSession)); err == nil {
		t.Error("the zip is still beside the loose copy")
	}
}

// A record that cannot be read is no record: push compares again, and writes it anew.
func TestPushComparesAgainWithACorruptZipRecord(t *testing.T) {
	archive, mac, repo := pushedSession(t)
	bucket := zipArchived(t, archive, repo)
	later(t, mac.session(repo))
	rec := filepath.Join(mac.claudeDir, "session-sync-base", "zip-compared.json")
	mustWrite(t, rec, `{"`+claude.Slug(repo)+"/"+forkSession+`": {"size": "garbled`)
	mac.run(t, "push", "--no-html")
	onlyZip(t, bucket)
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(mustRead(t, rec)), &m); err != nil || m[claude.Slug(repo)+"/"+forkSession] == nil {
		t.Errorf("the record was not compared again and rewritten (%v):\n%s", err, mustRead(t, rec))
	}
}

// garble overwrites a file with junk of the same size and time: a push that reads it fails.
func garble(t *testing.T, p string) {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, p, strings.Repeat("x", int(st.Size())))
	os.Chtimes(p, st.ModTime(), st.ModTime())
}

// A dry run writes nothing: neither the unzip nor the record of what it compared.
func TestDryRunLeavesAZippedCopyAndNoRecord(t *testing.T) {
	archive, mac, repo := pushedSession(t)
	bucket := zipArchived(t, archive, repo)
	later(t, mac.session(repo)) // the same turns: compared, and remembered by a real push
	mac.run(t, "push", "--no-html", "--dry-run")
	appendTurns(t, mac.session(repo), repo, "w1") // continued: a real push unzips
	mac.run(t, "push", "--no-html", "--dry-run")
	onlyZip(t, bucket)
	if rec := filepath.Join(mac.claudeDir, "session-sync-base", "zip-compared.json"); fileExists(rec) {
		t.Errorf("a dry run saved what it compared:\n%s", mustRead(t, rec))
	}
}

// A zip of this machine's own copy that cannot be read does not stop the session being
// archived: the loose copy goes beside it, and the WARN is said once for that zip.
func TestPushArchivesBesideAnUnreadableZip(t *testing.T) {
	archive, mac, repo := pushedSession(t)
	bucket := zipArchived(t, archive, repo)
	zp := convzip.Path(bucket, forkSession)
	mustWrite(t, zp, "not a zip")
	appendTurns(t, mac.session(repo), repo, "w1")
	out := mac.run(t, "push", "--no-html")
	appendTurns(t, mac.session(repo), repo, "w2")
	out += mac.run(t, "push", "--no-html")
	if n := strings.Count(out, "WARN: "+forkSession); n != 1 {
		t.Errorf("%d WARN line(s) for the session across two pushes, want 1:\n%s", n, out)
	}
	got := archived(t, archive, repo)
	for _, turn := range []string{"u1", "m1", "w1", "w2"} {
		if !strings.Contains(got, `"`+turn+`"`) {
			t.Errorf("the archived transcript lacks %s:\n%s", turn, got)
		}
	}
	if !fileExists(filepath.Join(bucket, forkSession, "subagents", "agent-a1.jsonl")) {
		t.Error("the session's subagent is not archived beside the zip")
	}
	if mustRead(t, zp) != "not a zip" {
		t.Error("the unreadable zip was changed")
	}
}

// The transcript is as zipped, but a subagent written here since is not in the zip: it
// is unzipped, and the subagent archived.
func TestPushUnzipsForAFolderFileTheZipLacks(t *testing.T) {
	archive, mac, repo := pushedSession(t)
	bucket := zipArchived(t, archive, repo)
	mustWrite(t, filepath.Join(filepath.Dir(mac.session(repo)), forkSession, "subagents", "agent-a2.jsonl"), sessionLines(repo, "s2"))
	mac.run(t, "push", "--no-html")
	if !fileExists(filepath.Join(bucket, forkSession, "subagents", "agent-a2.jsonl")) {
		t.Error("the subagent the zip lacked is not archived")
	}
	if fileExists(convzip.Path(bucket, forkSession)) {
		t.Error("the zip stayed though it lacks a file of the session's folder")
	}
	if got := archived(t, archive, repo); !strings.Contains(got, `"m1"`) {
		t.Errorf("the transcript is not archived loose:\n%s", got)
	}
}

func fileExists(p string) bool { _, err := os.Lstat(p); return err == nil }

// A link planted in the shared archive at <bucket>/<id> must not take the session's
// folder files outside it: none is copied, the WARN says why, and the transcript is
// archived as ever.
func TestPushWritesNoFolderFileThroughAPlantedLink(t *testing.T) {
	archive := t.TempDir()
	mac := newMachine(t, "MAC", archive)
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	mustWrite(t, mac.session(repo), sessionLines(repo, "u1", "m1"))
	mustWrite(t, filepath.Join(filepath.Dir(mac.session(repo)), forkSession, "subagents", "agent-a1.jsonl"), sessionLines(repo, "s1"))
	bucket := filepath.Join(archive, "projects", claude.Slug(repo))
	mustMkdir(t, bucket)
	outside := t.TempDir()
	symlink(t, outside, filepath.Join(bucket, forkSession))

	out := mac.run(t, "push", "--no-html")
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatalf("push wrote %d item(s) through the link", len(ents))
	}
	if !strings.Contains(out, "WARN: "+forkSession) {
		t.Errorf("push did not say why the folder was not archived:\n%s", out)
	}
	if got := archived(t, archive, repo); !strings.Contains(got, `"m1"`) {
		t.Errorf("the transcript is not archived:\n%s", got)
	}
}

// zippedOnMac is MAC's session, with a subagent and a tool output, pushed and zipped in
// the archive. WIN keeps the project at another path.
func zippedOnMac(t *testing.T) (archive string, mac, win machine, macRepo, winRepo string) {
	t.Helper()
	archive, mac, macRepo = pushedSession(t)
	mustWrite(t, filepath.Join(archive, "projects", claude.Slug(macRepo), forkSession, "tool-results", "r0.txt"), "archived only\n")
	zipArchived(t, archive, macRepo)
	win = newMachine(t, "WIN", archive)
	winRepo = gitRepo(t, filepath.Join(t.TempDir(), "work", "api"))
	return
}

// importOn runs import on m, searching beside here, with gone, the other machine's
// folder, moved away.
func importOn(t *testing.T, m machine, gone, here string, args ...string) string {
	t.Helper()
	var out string
	away(t, gone, func() {
		out = m.run(t, append([]string{"import", "--search-root", filepath.Dir(here), "--no-plugins"}, args...)...)
	})
	return out
}

// A zipped conversation is filed as a loose one is: every turn, its folder, paths
// rewritten. The archive keeps the zip, and the extraction is removed.
func TestImportRestoresAZippedConversation(t *testing.T) {
	archive, _, win, macRepo, winRepo := zippedOnMac(t)
	tmp := t.TempDir()
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, tmp)
	}
	out := importOn(t, win, macRepo, winRepo)

	got := mustRead(t, win.session(winRepo))
	for _, turn := range []string{"u1", "m1"} {
		if !strings.Contains(got, `"`+turn+`"`) {
			t.Errorf("the filed transcript lacks %s:\n%s\n%s", turn, got, out)
		}
	}
	if !strings.Contains(got, `"cwd":`+quoted(winRepo)) {
		t.Errorf("the cwd is not rewritten to %s:\n%s", winRepo, got)
	}
	dir := filepath.Join(filepath.Dir(win.session(winRepo)), forkSession)
	if sub := mustRead(t, filepath.Join(dir, "subagents", "agent-a1.jsonl")); !strings.Contains(sub, `"cwd":`+quoted(winRepo)) {
		t.Errorf("the subagent is not filed with its paths rewritten:\n%s", sub)
	}
	if r := mustRead(t, filepath.Join(dir, "tool-results", "r0.txt")); r != "archived only\n" {
		t.Errorf("the tool output is %q", r)
	}
	onlyZip(t, filepath.Join(archive, "projects", claude.Slug(macRepo)))
	if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
		t.Errorf("import left %d temporary item(s) behind", len(ents))
	}
}

// A bucket of zips alone is a project to import, placed from the manifest or, with
// none, from the zipped transcript's cwd.
func TestImportFindsABucketOfOnlyZips(t *testing.T) {
	archive, _, win, macRepo, winRepo := zippedOnMac(t)
	placed := claude.Slug(macRepo) + " -> " + winRepo
	if out := importOn(t, win, macRepo, winRepo, "--dry-run"); !strings.Contains(out, placed) || !strings.Contains(out, "1 filed") {
		t.Errorf("the bucket of zips is not listed with its placement:\n%s", out)
	}
	for _, f := range []string{filepath.Join("manifest", "MAC.json"), "projects.json"} {
		if err := os.Remove(filepath.Join(archive, f)); err != nil {
			t.Fatal(err)
		}
	}
	if out := importOn(t, win, macRepo, winRepo, "--dry-run"); !strings.Contains(out, placed) {
		t.Errorf("with no manifest the bucket is not placed from its zipped transcript:\n%s", out)
	}
}

// MAC went on with the session imported on WIN, and its copy was zipped since: --update
// brings the new turns, with paths rewritten.
func TestImportUpdateFromAZippedCopy(t *testing.T) {
	archive, mac, win, macRepo, winRepo := importedOnWin(t)
	appendTurns(t, mac.session(macRepo), macRepo, "m2")
	mac.run(t, "push", "--no-html")
	zipArchived(t, archive, macRepo)
	out := importOn(t, win, macRepo, winRepo, "--update")
	got := mustRead(t, win.session(winRepo))
	if !strings.Contains(got, `"m2"`) || !strings.Contains(got, `"cwd":`+quoted(winRepo)) {
		t.Errorf("the new turn did not arrive rewritten:\n%s\n%s", got, out)
	}
	if s := win.sessions(t, winRepo); len(s) != 1 {
		t.Errorf("WIN holds %v, want the one session", s)
	}
}

func TestPullReadsZips(t *testing.T) {
	archive, _, repo := pushedSession(t)
	zipArchived(t, archive, repo)
	lap := newMachine(t, "LAP-2", archive)
	out := lap.run(t, "pull", "--no-plugins")
	if got := mustRead(t, lap.session(repo)); !strings.Contains(got, `"m1"`) {
		t.Errorf("pull did not file the zipped conversation:\n%s\n%s", got, out)
	}
	if !fileExists(filepath.Join(filepath.Dir(lap.session(repo)), forkSession, "subagents", "agent-a1.jsonl")) {
		t.Error("pull did not file the zipped subagent")
	}
}

// Another machine continued the conversation loose beside a zip it could not see yet:
// the loose turns win, the subagent comes from the zip, and the id is filed once.
func TestPullReadsALooseCopyBesideItsZip(t *testing.T) {
	archive, _, repo := pushedSession(t)
	bucket := zipArchived(t, archive, repo)
	mustWrite(t, filepath.Join(bucket, forkSession+".jsonl"), sessionLines(repo, "u1", "m1", "x1"))
	lap := newMachine(t, "LAP-2", archive)
	out := lap.run(t, "pull", "--no-plugins")
	if !strings.Contains(out, "filed 1 transcript(s)") {
		t.Errorf("not filed once:\n%s", out)
	}
	if got := mustRead(t, lap.session(repo)); !strings.Contains(got, `"x1"`) {
		t.Errorf("the loose continuation is not what was filed:\n%s", got)
	}
	if !fileExists(filepath.Join(filepath.Dir(lap.session(repo)), forkSession, "subagents", "agent-a1.jsonl")) {
		t.Error("the subagent only the zip holds was not filed")
	}
}

// A zip that cannot be read is named and skipped; the bucket's other conversations are
// filed.
func TestPullSkipsAnUnreadableZip(t *testing.T) {
	archive, mac, repo := pushedSession(t)
	const other = "77777777-7777-7777-7777-777777777777"
	mustWrite(t, filepath.Join(filepath.Dir(mac.session(repo)), other+".jsonl"), sessionLines(repo, "o1"))
	mac.run(t, "push", "--no-html")
	bucket := zipArchived(t, archive, repo)
	mustWrite(t, convzip.Path(bucket, forkSession), "not a zip")
	lap := newMachine(t, "LAP-2", archive)
	out := lap.run(t, "pull", "--no-plugins")
	if !strings.Contains(out, forkSession+convzip.Suffix) {
		t.Errorf("the unreadable zip is not named:\n%s", out)
	}
	if fileExists(lap.session(repo)) {
		t.Error("a conversation was filed from a zip that cannot be read")
	}
	if !fileExists(filepath.Join(filepath.Dir(lap.session(repo)), other+".jsonl")) {
		t.Errorf("the bucket's other conversation was not filed:\n%s", out)
	}
}
