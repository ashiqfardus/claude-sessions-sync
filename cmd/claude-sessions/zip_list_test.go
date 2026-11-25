package main_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestCleanLeavesSessionsWithAZippedCopy(t *testing.T) {
	archive := t.TempDir()
	m := newMachine(t, "MAC", archive)
	a := filepath.Join(archive, "projects", "-work-web-app")
	b := filepath.Join(archive, "projects", "-work-web-app2")
	body := sessionLines("/work/web-app", "u1", "u2")
	mustWrite(t, filepath.Join(a, forkSession+".jsonl"), body)
	mustWrite(t, filepath.Join(b, forkSession+".jsonl"), body)
	if _, err := convzip.Zip(b, forkSession); err != nil {
		t.Fatal(err)
	}
	m.run(t, "clean", "--yes")
	if _, err := os.Stat(filepath.Join(a, forkSession+".jsonl")); err != nil {
		t.Fatalf("clean deleted the loose copy: %v", err)
	}
	if _, err := os.Stat(convzip.Path(b, forkSession)); err != nil {
		t.Fatalf("clean deleted the zip: %v", err)
	}
}
