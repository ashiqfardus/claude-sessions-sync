package main_test

// A conversation idle long enough is zipped in the archive, <id>.conv.zip. Push reads
// the zip as it reads a loose copy: one unchanged is left zipped, one continued is
// unzipped before the usual rules judge it, and another machine's zipped copy still
// holds a session imported here.

import (
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
	win.run(t, "push", "--no-html")

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

	out := mac.run(t, "push", "--no-html")
	if n := strings.Count(out, "WARN: "+forkSession); n != 1 {
		t.Errorf("%d WARN line(s) for the session, want 1:\n%s", n, out)
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
