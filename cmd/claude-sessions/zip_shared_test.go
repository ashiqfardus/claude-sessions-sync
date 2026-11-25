package main_test

// Two computers push to one bucket - the Windows laptops' shared E:\api - and the sync
// client carries each one's writes to the other in its own order. One computer's zip
// can reach the other before its deletions of the loose files do: nothing either does
// then may take the last copy of a turn, nor rewrite the zip on every push.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/convzip"
)

// zippedFile is a loose file of the archive as computer A zipped it.
type zippedFile struct {
	path, data string
	mt         time.Time
}

func zippedFiles(t *testing.T, paths ...string) []zippedFile {
	t.Helper()
	var out []zippedFile
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, zippedFile{p, mustRead(t, p), st.ModTime()})
	}
	return out
}

// notYetDeleted puts A's loose files back, as B sees the bucket while A's deletions of
// them are still on their way.
func notYetDeleted(t *testing.T, files []zippedFile) {
	t.Helper()
	for _, f := range files {
		mustWrite(t, f.path, f.data)
		if err := os.Chtimes(f.path, f.mt, f.mt); err != nil {
			t.Fatal(err)
		}
	}
}

// deletionsLand applies A's deletions on B: each file A zipped, where it is still as A
// left it.
func deletionsLand(files []zippedFile) {
	for _, f := range files {
		if b, err := os.ReadFile(f.path); err == nil && string(b) == f.data {
			os.Remove(f.path)
		}
	}
}

// holdsConversation fails unless the bucket holds the session: each of turns in its
// transcript, and each folder file with its content.
func holdsConversation(t *testing.T, bucket string, turns []string, folder map[string]string) {
	t.Helper()
	p, done, err := convzip.Materialize(bucket, forkSession, t.TempDir())
	if err != nil {
		t.Fatalf("the conversation is gone from the archive: %v", err)
	}
	defer done()
	got := mustRead(t, p)
	for _, turn := range turns {
		if !strings.Contains(got, `"`+turn+`"`) {
			t.Errorf("the archive lost turn %s:\n%s", turn, got)
		}
	}
	for rel, want := range folder {
		if b, err := os.ReadFile(filepath.Join(filepath.Dir(p), forkSession, filepath.FromSlash(rel))); err != nil || !strings.Contains(string(b), want) {
			t.Errorf("the archive lost %s: %v", rel, err)
		}
	}
}

// A zipped the conversation; on B the zip has arrived and A's deletions have not, and
// one of them lands while B's sweep runs - a file B can no longer read. B's push leaves
// A's zip, which holds every loose file, and once the deletions land it still holds the
// conversation: the tool output only the archive had, and every turn. Also when A's
// deletion of the transcript came first.
func TestSharedBucketKeepsAZipWhoseDeletionsAreOnTheWay(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a file its owner cannot read")
	}
	for name, transcript := range map[string]bool{"every loose file": true, "folder files only": false} {
		t.Run(name, func(t *testing.T) {
			archive, b, repo := pushedSession(t)
			bucket := archiveBucket(archive, repo)
			r0 := filepath.Join(bucket, forkSession, "tool-results", "r0.txt")
			mustWrite(t, r0, "archived only\n")
			ageSession(t, b, repo, 31*day)
			paths := []string{filepath.Join(bucket, forkSession, "subagents", "agent-a1.jsonl"), r0}
			if transcript {
				paths = append(paths, filepath.Join(bucket, forkSession+".jsonl"))
			}
			files := zippedFiles(t, paths...)
			zipArchived(t, archive, repo) // computer A
			notYetDeleted(t, files)
			zst, err := os.Stat(convzip.Path(bucket, forkSession))
			if err != nil {
				t.Fatal(err)
			}

			os.Chmod(r0, 0)
			b.run(t, "push", "--no-html")
			os.Chmod(r0, 0o644)
			deletionsLand(files)

			if st, err := os.Stat(convzip.Path(bucket, forkSession)); err != nil || !os.SameFile(zst, st) {
				t.Errorf("B's push removed A's zip or wrote it again (%v): between the two, only files A is deleting held the conversation", err)
			}
			holdsConversation(t, bucket, []string{"u1", "m1"}, map[string]string{"subagents/agent-a1.jsonl": `"s1"`, "tool-results/r0.txt": "archived only"})
		})
	}
}

// The same, B having gone on with the conversation: its push archives its turns beside
// A's zip rather than unzipping it, so the tool output only the zip will hold is still
// there once A's deletions land.
func TestPushLeavesAZipBesideLooseFilesOnTheirWayOut(t *testing.T) {
	archive, b, repo := pushedSession(t)
	bucket := archiveBucket(archive, repo)
	mustWrite(t, filepath.Join(bucket, forkSession, "tool-results", "r0.txt"), "archived only\n")
	files := zippedFiles(t, filepath.Join(bucket, forkSession+".jsonl"),
		filepath.Join(bucket, forkSession, "subagents", "agent-a1.jsonl"), filepath.Join(bucket, forkSession, "tool-results", "r0.txt"))
	zipArchived(t, archive, repo) // computer A
	notYetDeleted(t, files)
	appendTurns(t, b.session(repo), repo, "w1", "w2")

	b.run(t, "push", "--no-html")
	deletionsLand(files)

	if !fileExists(convzip.Path(bucket, forkSession)) {
		t.Error("B's push unzipped A's zip beside loose files on their way out")
	}
	holdsConversation(t, bucket, []string{"u1", "m1", "w1", "w2"}, map[string]string{"subagents/agent-a1.jsonl": `"s1"`, "tool-results/r0.txt": "archived only"})
}

// Continued on both Windows laptops - A's branch archived in the shared bucket, B's kept
// as its fork - then idle, and zipped by A. B's fork is archived and unchanged, so its
// pushes leave the zip as it is: written once, not unzipped and zipped again each time.
func TestAnArchivedForkLeavesItsZipAlone(t *testing.T) {
	f := newFour(t)
	p := convID(1, 1)
	f.winA.say(t, f.api, p, "p1")
	f.winA.run(t, "push", "--no-html")
	f.winB.run(t, "import", "--search-root", f.winB.search, "--update", "--no-plugins")
	f.winA.say(t, f.api, p, "a2")
	f.winB.say(t, f.api, p, "b2")
	f.winA.run(t, "push", "--no-html")
	f.winB.run(t, "push", "--no-html")
	bucket := filepath.Join(f.archive, "projects", claude.Slug(f.api))
	fork := filepath.Join(f.archive, "forks", claude.Slug(f.api), p, "WIN-B.jsonl")
	if !fileExists(fork) {
		t.Fatal("no fork")
	}
	agePaths(t, 33*day, f.winB.transcript(f.api, p), fork) // the fork is B's copy, time and all
	agePaths(t, 31*day, f.winA.transcript(f.api, p), filepath.Join(bucket, p+".jsonl"), filepath.Join(bucket, p))
	f.winA.run(t, "push", "--no-html") // A zips the shared copy
	zp := convzip.Path(bucket, p)
	prev, err := os.Stat(zp)
	if err != nil {
		t.Fatalf("A did not zip: %v", err)
	}
	for i := 0; i < 3; i++ {
		out := f.winB.run(t, "push", "--no-html")
		cur, err := os.Stat(zp)
		if err != nil || !os.SameFile(prev, cur) {
			t.Fatalf("B's push %d unzipped the zip, or wrote it again (%v):\n%s", i+1, err, out)
		}
	}
	if got := mustRead(t, fork); !strings.Contains(got, `"b2"`) {
		t.Errorf("B's branch is not in its fork:\n%s", got)
	}
	f.winB.holdsAll(t, f.api, "p1", "b2")
}
