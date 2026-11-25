package main_test

// Push zips an archived conversation idle for 30 days into <id>.conv.zip, once every
// machine in the archive reads zips, never from the per-reply hook or a dry run.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/convzip"
	"github.com/ashiqfardus/claude-sessions-sync/internal/work"
)

const day = 24 * time.Hour

// agePaths stamps every file at or under each path age ago.
func agePaths(t *testing.T, age time.Duration, paths ...string) {
	t.Helper()
	at := time.Now().Add(-age)
	for _, p := range paths {
		filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				if err := os.Chtimes(path, at, at); err != nil {
					t.Fatal(err)
				}
			}
			return nil
		})
	}
}

// sessionFiles is the session's transcript and folder in a bucket.
func sessionFiles(bucket string) []string {
	return []string{filepath.Join(bucket, forkSession+".jsonl"), filepath.Join(bucket, forkSession)}
}

func archiveBucket(archive, repo string) string {
	return filepath.Join(archive, "projects", claude.Slug(repo))
}

// ageSession stamps the session's local and archived files age ago.
func ageSession(t *testing.T, m machine, repo string, age time.Duration) {
	t.Helper()
	agePaths(t, age, append(sessionFiles(filepath.Dir(m.session(repo))), sessionFiles(archiveBucket(m.archive, repo))...)...)
}

// stillLoose fails unless the archive holds the session loose, and no zip of it.
func stillLoose(t *testing.T, archive, repo string) {
	t.Helper()
	if archived(t, archive, repo) == "" {
		t.Fatal("the loose transcript is gone")
	}
	if fileExists(convzip.Path(archiveBucket(archive, repo), forkSession)) {
		t.Fatal("the session was zipped")
	}
}

func pushLog(t *testing.T, m machine) string {
	t.Helper()
	return mustRead(t, filepath.Join(m.claudeDir, "session-sync.log"))
}

func TestPushZipsIdleConversations(t *testing.T) {
	archive, mac, repo := pushedSession(t)
	ageSession(t, mac, repo, 31*day)
	mac.run(t, "push", "--no-html")

	bucket := archiveBucket(archive, repo)
	onlyZip(t, bucket)
	ents, err := convzip.Entries(convzip.Path(bucket, forkSession))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{forkSession + ".jsonl", forkSession + "/subagents/agent-a1.jsonl"} {
		if _, ok := ents[name]; !ok {
			t.Errorf("the zip lacks %s: %v", name, ents)
		}
	}
	if log := pushLog(t, mac); !strings.Contains(log, "ZIP: 1 conversation(s), ") {
		t.Errorf("no ZIP line in the log:\n%s", log)
	}
	// The next push reads the zip as the archived copy and leaves it.
	mac.run(t, "push", "--no-html")
	onlyZip(t, bucket)
}

func TestPushKeepsRecentLoose(t *testing.T) {
	archive, mac, repo := pushedSession(t)
	ageSession(t, mac, repo, 29*day)
	mac.run(t, "push", "--no-html")
	stillLoose(t, archive, repo)

	// The archived copy idle, but the session used here a day ago: still loose.
	ageSession(t, mac, repo, 31*day)
	agePaths(t, day, mac.session(repo))
	mac.run(t, "push", "--no-html")
	stillLoose(t, archive, repo)
}

// A file dated in the future is not idle, however wrong the clock that wrote it.
func TestIdleIgnoresFutureMTime(t *testing.T) {
	archive, mac, repo := pushedSession(t)
	future := filepath.Join(archiveBucket(archive, repo), forkSession, "tool-results", "r0.txt")
	mustWrite(t, future, "archived only\n")
	ageSession(t, mac, repo, 31*day)
	agePaths(t, -2*day, future)
	mac.run(t, "push", "--no-html")
	stillLoose(t, archive, repo)
}

func TestZipWaitsForOlderShard(t *testing.T) {
	archive, mac, repo := pushedSession(t)
	mustWrite(t, filepath.Join(archive, "manifest", "LAP-2.json"), `{"schemaVersion":1,"machine":"LAP-2","projects":{}}`)
	ageSession(t, mac, repo, 31*day)
	mac.run(t, "push", "--no-html")
	stillLoose(t, archive, repo)

	out := mac.doctor(t)
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "zips") && strings.Contains(l, "LAP-2") {
			line = l
		}
	}
	if line == "" || !strings.Contains(out, "machines --forget") || !strings.Contains(out, "Update") {
		t.Errorf("doctor does not say zipping waits for LAP-2, or how to end the wait:\n%s", out)
	}

	// Forgotten, it holds nothing back.
	mac.run(t, "machines", "--forget", "LAP-2")
	mac.run(t, "push", "--no-html")
	onlyZip(t, archiveBucket(archive, repo))
}

func TestZipSessionsOff(t *testing.T) {
	archive, mac, repo := pushedSession(t)
	configAs(t, mac.claudeDir, mac.name, "zip-sessions", "off")
	ageSession(t, mac, repo, 31*day)
	mac.run(t, "push", "--no-html")
	stillLoose(t, archive, repo)

	// A zip already there is still read as the archived copy: nothing goes beside it.
	bucket := zipArchived(t, archive, repo)
	mac.run(t, "push", "--no-html")
	onlyZip(t, bucket)
}

func TestHookPushNeverZips(t *testing.T) {
	archive, mac, repo := pushedSession(t)
	ageSession(t, mac, repo, 31*day)
	mac.hook(t, mac.session(repo))
	stillLoose(t, archive, repo)
}

// A conversation over the cap stays loose, and says so once until its size changes.
func TestPushLeavesOverCapLooseQuietly(t *testing.T) {
	archive, mac, repo := pushedSession(t)
	big := filepath.Join(archiveBucket(archive, repo), forkSession, "tool-results", "big.bin")
	mustWrite(t, big, "")
	if err := os.Truncate(big, work.MaxFolderBytes); err != nil { // sparse: nothing is written
		t.Fatal(err)
	}
	ageSession(t, mac, repo, 31*day)
	count := func() int { return strings.Count(pushLog(t, mac), forkSession+" is over") }

	mac.run(t, "push", "--no-html")
	mac.run(t, "push", "--no-html")
	stillLoose(t, archive, repo)
	if n := count(); n != 1 {
		t.Fatalf("%d over-cap line(s) across two pushes, want 1:\n%s", n, pushLog(t, mac))
	}
	if rec := mustRead(t, filepath.Join(mac.claudeDir, "session-sync-base", "zip-skipped.json")); !strings.Contains(rec, forkSession) {
		t.Errorf("zip-skipped.json does not remember it:\n%s", rec)
	}

	// A new size is tried again.
	if err := os.Truncate(big, work.MaxFolderBytes+1); err != nil {
		t.Fatal(err)
	}
	agePaths(t, 31*day, big)
	mac.run(t, "push", "--no-html")
	stillLoose(t, archive, repo)
	if n := count(); n != 2 {
		t.Errorf("%d over-cap line(s) after its size changed, want 2", n)
	}
}

// tree lists every file under dir with its size and time.
func tree(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			st, _ := os.Stat(p)
			fmt.Fprintf(&b, "%s %d %d\n", p, st.Size(), st.ModTime().UnixNano())
		}
		return nil
	})
	return b.String()
}

func TestDryRunZipsNothing(t *testing.T) {
	archive, mac, repo := pushedSession(t)
	ageSession(t, mac, repo, 31*day)
	before := tree(t, filepath.Join(archive, "projects"))
	out := mac.run(t, "push", "--no-html", "--dry-run")
	if !strings.Contains(out, "would zip "+claude.Slug(repo)+"/"+forkSession) {
		t.Errorf("the dry run does not list the session:\n%s", out)
	}
	if after := tree(t, filepath.Join(archive, "projects")); after != before {
		t.Errorf("the dry run changed the archive:\n%s\nwas:\n%s", after, before)
	}
	if strings.Contains(pushLog(t, mac), "ZIP: ") {
		t.Error("the dry run logged a ZIP line")
	}
}

// A run killed while Zip deleted the loose files left a folder file beside the zip: the
// next push unzips and zips again, so the bucket holds the zip alone, with that file.
func TestPushZipsALeftoverBesideItsZip(t *testing.T) {
	archive, mac, repo := pushedSession(t)
	ageSession(t, mac, repo, 31*day)
	bucket := archiveBucket(archive, repo)
	left := filepath.Join(bucket, forkSession, "subagents", "agent-a1.jsonl")
	data := mustRead(t, left)
	zipArchived(t, archive, repo)
	mustWrite(t, left, data)
	agePaths(t, 31*day, left)

	mac.run(t, "push", "--no-html")
	onlyZip(t, bucket)
	ents, err := convzip.Entries(convzip.Path(bucket, forkSession))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ents[forkSession+"/subagents/agent-a1.jsonl"]; !ok {
		t.Errorf("the zip lacks the subagent: %v", ents)
	}
}

// A loose transcript beside the zip, each with turns the other lacks: both are kept,
// and the log says so once.
func TestPushLeavesADivergedLeftoverOnce(t *testing.T) {
	archive, mac, repo := pushedSession(t)
	ageSession(t, mac, repo, 31*day)
	bucket := zipArchived(t, archive, repo)
	loose := filepath.Join(bucket, forkSession+".jsonl")
	mustWrite(t, loose, sessionLines(repo, "u1", "x1"))
	agePaths(t, 31*day, loose)

	mac.run(t, "push", "--no-html")
	mac.run(t, "push", "--no-html")
	if n := strings.Count(pushLog(t, mac), "WARN: "+forkSession); n != 1 {
		t.Errorf("%d WARN line(s) for the session across two pushes, want 1:\n%s", n, pushLog(t, mac))
	}
	if !fileExists(convzip.Path(bucket, forkSession)) || !strings.Contains(mustRead(t, loose), `"x1"`) {
		t.Error("the zip or the loose transcript beside it was not kept")
	}
}

// Unattended, only the sweep zips: the SessionEnd hook's quiet push has a 120-second
// limit to keep.
func TestQuietPushZipsOnlyInTheSweep(t *testing.T) {
	archive, mac, repo := pushedSession(t)
	ageSession(t, mac, repo, 31*day)
	mac.run(t, "push", "--no-html", "--quiet")
	stillLoose(t, archive, repo)

	mac.run(t, "push", "--no-html", "--quiet", "--sweep")
	onlyZip(t, archiveBucket(archive, repo))
}
