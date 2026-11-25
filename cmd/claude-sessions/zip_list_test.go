package main_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/convzip"
)

// zippedArchive makes an archive with one conversation, zipped, whose prompt holds word.
func zippedArchive(t *testing.T, word string) (m machine, bucketDir string) {
	t.Helper()
	archive := t.TempDir()
	m = newMachine(t, "MAC", archive)
	bucketDir = filepath.Join(archive, "projects", "-work-web-app")
	mustWrite(t, filepath.Join(bucketDir, forkSession+".jsonl"), sessionLines("/work/web-app", word))
	if _, err := convzip.Zip(bucketDir, forkSession); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(bucketDir, forkSession+".jsonl")); err == nil {
		t.Fatal("fixture: transcript was not zipped away")
	}
	return m, bucketDir
}

func TestLsArchivedListsZipped(t *testing.T) {
	m, _ := zippedArchive(t, "zippedprompt")
	out := m.run(t, "ls", "--archived")
	if !strings.Contains(out, forkSession) || !strings.Contains(out, "zippedprompt") || !strings.Contains(out, "/work/web-app") {
		t.Fatalf("ls --archived missed the zipped conversation:\n%s", out)
	}
}

func TestSearchArchivedFindsTextInZipped(t *testing.T) {
	m, _ := zippedArchive(t, "needleinzip")
	if out := m.run(t, "search", "--archived", "needleinzip"); !strings.Contains(out, forkSession) {
		t.Fatalf("search --archived did not find the zipped text:\n%s", out)
	}
}

func TestStatsArchivedCountsZipped(t *testing.T) {
	m, _ := zippedArchive(t, "x1")
	if out := m.run(t, "stats", "--archived"); !strings.Contains(out, "1 session(s) across 1 project(s)") || !strings.Contains(out, "web-app") {
		t.Fatalf("stats --archived did not count the zipped conversation:\n%s", out)
	}
}

func TestArchivedSkipsUnreadableZip(t *testing.T) {
	m, bucketDir := zippedArchive(t, "x1")
	zp := convzip.Path(bucketDir, forkSession)
	mustWrite(t, zp, "not a zip")
	// run merges stderr: one note, no row.
	if out := m.run(t, "ls", "--archived"); !strings.Contains(out, "skipped") || strings.Contains(out, "1 session(s)") {
		t.Fatalf("bad zip not skipped with a note:\n%s", out)
	}
}

// WIN's bucket holds MAC's copy in whole, loose, and also a zip of it: without the zip,
// clean would delete MAC's. With one, the session is left alone.
func TestCleanLeavesSessionsWithAZippedCopy(t *testing.T) {
	archive, _, win, macRepo, winRepo := continuedOnWin(t)
	wb := filepath.Join(archive, "projects", claude.Slug(winRepo))
	snap := map[string]string{}
	filepath.WalkDir(wb, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			snap[p] = mustRead(t, p)
		}
		return nil
	})
	if _, err := convzip.Zip(wb, forkSession); err != nil {
		t.Fatal(err)
	}
	for p, body := range snap { // the loose files are back beside the zip
		mustWrite(t, p, body)
	}
	if out, err := win.clean(t, "--yes"); err != nil {
		t.Fatalf("clean: %v\n%s", err, out)
	}
	if archived(t, archive, macRepo) == "" {
		t.Fatal("clean deleted a copy of a session that has a zipped copy")
	}
	if _, err := os.Stat(convzip.Path(wb, forkSession)); err != nil {
		t.Fatalf("clean deleted the zip: %v", err)
	}
}

func TestSearchArchivedSkipsUnreadableZip(t *testing.T) {
	m, bucketDir := zippedArchive(t, "needleinzip")
	other := "77777777-7777-7777-7777-777777777777"
	mustWrite(t, filepath.Join(bucketDir, other+".jsonl"), sessionLines("/work/web-app", "needleinzip"))
	mustWrite(t, convzip.Path(bucketDir, forkSession), "not a zip")
	out := m.run(t, "search", "--archived", "needleinzip")
	if !strings.Contains(out, other) || strings.Count(out, "skipped") != 1 {
		t.Fatalf("search did not continue past a bad zip with one note:\n%s", out)
	}
}

func TestStatsArchivedSkipsUnreadableZip(t *testing.T) {
	m, bucketDir := zippedArchive(t, "x1")
	mustWrite(t, convzip.Path(bucketDir, forkSession), "not a zip")
	out := m.run(t, "stats", "--archived")
	if !strings.Contains(out, "1 session(s)") || strings.Count(out, "skipped") > 1 {
		t.Fatalf("stats failed on a bad zip:\n%s", out)
	}
}
