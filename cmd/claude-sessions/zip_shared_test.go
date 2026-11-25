package main_test

// Two computers push to one bucket - the Windows laptops' shared E:\api - and the sync
// client carries each one's writes to the other in its own order. One computer's zip
// can reach the other before its deletions of the loose files do: nothing either does
// then may take the last copy of a turn, nor rewrite the zip on every push.

import (
	"archive/zip"
	"bytes"
	"encoding/json"
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

// Nothing in a conversation's folder that Zip cannot pack makes a sweep zip it again on
// every push: a link keeps the conversation loose, said once; a folder left empty
// beside the zip - one the system would not remove - is not taken for a killed run.
func TestNothingUnpackableRewritesTheZip(t *testing.T) {
	t.Run("a link", func(t *testing.T) {
		archive, mac, repo := pushedSession(t)
		bucket := archiveBucket(archive, repo)
		if err := os.Symlink(filepath.Join(bucket, forkSession+".jsonl"), filepath.Join(bucket, forkSession, "subagents", "odd")); err != nil {
			t.Skipf("no symlinks here: %v", err)
		}
		ageSession(t, mac, repo, 31*day)
		for i := 0; i < 3; i++ {
			mac.run(t, "push", "--no-html")
		}
		stillLoose(t, archive, repo)
		if n := strings.Count(pushLog(t, mac), "WARN: "+forkSession); n != 1 {
			t.Errorf("%d WARN line(s) for the session across three pushes, want 1:\n%s", n, pushLog(t, mac))
		}
	})
	t.Run("an empty folder", func(t *testing.T) {
		archive, mac, repo := pushedSession(t)
		bucket := archiveBucket(archive, repo)
		ageSession(t, mac, repo, 31*day)
		mac.run(t, "push", "--no-html")
		onlyZip(t, bucket)
		mustMkdir(t, filepath.Join(bucket, forkSession, "subagents"))
		prev, _ := os.Stat(convzip.Path(bucket, forkSession))
		for i := 0; i < 2; i++ {
			mac.run(t, "push", "--no-html")
			cur, err := os.Stat(convzip.Path(bucket, forkSession))
			if err != nil || !os.SameFile(prev, cur) {
				t.Fatalf("push %d unzipped the zip, or wrote it again: %v", i+2, err)
			}
		}
	})
}

// A push removes what killed runs left in the OS temp folder: its own extracts, older
// than an hour. Nothing else there - a newer extract, another name, a file, a link and
// what it points to - is touched.
func TestPushRemovesStaleExtractsFromTheTempFolder(t *testing.T) {
	archive, mac, _ := pushedSession(t)
	tmp := t.TempDir()
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, tmp)
	}
	old := time.Now().Add(-2 * time.Hour)
	at := func(name string, dir bool) string {
		p := filepath.Join(tmp, name)
		if dir {
			mustWrite(t, filepath.Join(p, "x.jsonl"), "extracted\n")
		} else {
			mustWrite(t, p, "a file\n")
		}
		os.Chtimes(p, old, old)
		return p
	}
	stale := []string{at("convzip-123", true), at("claude-sessions-push-456", true), at("cs-zip789", true)}
	kept := []string{at("convzip-file", false), at("other-123", true)}
	fresh := filepath.Join(tmp, "convzip-new")
	mustWrite(t, filepath.Join(fresh, "x.jsonl"), "in use\n")
	kept = append(kept, fresh)
	target := filepath.Join(t.TempDir(), "elsewhere")
	mustWrite(t, filepath.Join(target, "x.jsonl"), "not ours\n")
	if os.Symlink(target, filepath.Join(tmp, "convzip-link")) == nil {
		kept = append(kept, filepath.Join(tmp, "convzip-link"), filepath.Join(target, "x.jsonl"))
	}
	os.Chtimes(target, old, old)

	mac.run(t, "push", "--no-html")
	for _, p := range stale {
		if fileExists(p) {
			t.Errorf("%s was left", filepath.Base(p))
		}
	}
	for _, p := range kept {
		if !fileExists(p) {
			t.Errorf("%s was removed", p)
		}
	}
	_ = archive
}

// A loose copy beside a zip that holds more of the conversation - written by a computer
// the zip had not reached yet - does not hide the zip's turns from search, nor list the
// conversation twice.
func TestArchivedReadersReadTheZipBesideALooseCopy(t *testing.T) {
	archive, mac, repo := pushedSession(t)
	bucket := archiveBucket(archive, repo)
	loose := filepath.Join(bucket, forkSession+".jsonl")
	behind := mustRead(t, loose)
	mustWrite(t, loose, behind+sessionLines(repo, "zipneedle"))
	zipArchived(t, archive, repo)
	mustWrite(t, loose, behind)

	if out := mac.run(t, "search", "--archived", "zipneedle"); !strings.Contains(out, forkSession) {
		t.Errorf("search --archived missed the turn only the zip holds:\n%s", out)
	}
	if out := mac.run(t, "ls", "--archived"); strings.Count(out, forkSession) != 1 {
		t.Errorf("ls --archived does not list the conversation once:\n%s", out)
	}
	mustWrite(t, convzip.Path(bucket, forkSession), "not a zip") // the loose copy is still read
	if out := mac.run(t, "search", "--archived", "turn m1"); !strings.Contains(out, forkSession) {
		t.Errorf("search --archived lost the loose copy beside an unreadable zip:\n%s", out)
	}
}

// ls --archived reads a zip's transcript alone: a folder entry it cannot read - its
// bytes do not match their checksum - does not hide the conversation's row.
func TestLsArchivedReadsOnlyAZipsTranscript(t *testing.T) {
	m, bucketDir := zippedArchive(t, "headonly")
	tr := sessionLines("/work/web-app", "headonly")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{forkSession + ".jsonl": tr, forkSession + "/tool-results/r0.txt": "PAYLOAD-0123456789"} {
		h := &zip.FileHeader{Name: name, Method: zip.Store, Modified: time.Now().Add(-time.Hour)}
		h.SetMode(0o644)
		w, _ := zw.CreateHeader(h)
		w.Write([]byte(body))
	}
	zw.Close()
	mustWrite(t, convzip.Path(bucketDir, forkSession), string(bytes.Replace(buf.Bytes(), []byte("PAYLOAD-0123456789"), []byte("PAYLOAD-0123456780"), 1)))

	var rows []struct {
		SessionID   string `json:"session"`
		FirstPrompt string `json:"firstPrompt"`
	}
	out := m.run(t, "ls", "--archived", "--json")
	if err := json.Unmarshal([]byte(out[strings.Index(out, "["):]), &rows); err != nil || len(rows) != 1 || !strings.Contains(rows[0].FirstPrompt, "headonly") {
		t.Fatalf("ls --archived did not list the conversation from its transcript (%v):\n%s", err, out)
	}
}
