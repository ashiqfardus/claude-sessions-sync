package archive

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A sync client keeps both sides of a conflict, and the copy carries the same machine
// name inside. Read after the real file, it replaced it: INDEX.md fell from 3,000
// sessions to 5 and stayed there.
func TestConflictCopiesAreIgnored(t *testing.T) {
	dest := t.TempDir()
	writeShard(t, dest, "LAP-1", Shard{Machine: "LAP-1", Projects: map[string]Project{"e--api": {Path: `E:\api`}}})
	writeShard(t, dest, "LAP-1.sync-conflict-20260930-101500-ABCDEFG", Shard{Machine: "LAP-1", Projects: map[string]Project{}})
	writeShard(t, dest, "LAP-1 (1)", Shard{Machine: "LAP-1", Projects: map[string]Project{}})

	shards, bad, err := ReadShards(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(shards["LAP-1"].Projects) != 1 {
		t.Errorf("a conflict copy replaced the machine's shard: %+v", shards["LAP-1"])
	}
	if len(bad) != 2 {
		t.Errorf("conflict copies should be reported for doctor, got %v", bad)
	}

	if err := WriteSessionIndex(dest, "LAP-1", []Session{{Bucket: "e--api", ID: "s1"}, {Bucket: "e--api", ID: "s2"}}); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(IndexDir(dest), "LAP-1 (1).json"), `{"machine":"LAP-1","sessions":[]}`)
	if rows, _ := AllSessions(dest); len(rows) != 2 {
		t.Errorf("a conflict copy of the index hid sessions: %d row(s)", len(rows))
	}
}

// An archive pointed at a whole drive had every other program's .tmp- files deleted.
func TestRemoveOrphanTempsStaysInItsOwnFolders(t *testing.T) {
	dest := t.TempDir()
	ours := filepath.Join(ProjectsDir(dest), "e--api", ".tmp-x.jsonl-1")
	root := filepath.Join(dest, ".tmp-INDEX.md-1")
	work := filepath.Join(dest, "work", "MAC", "Users", "a", "SQL", ".tmp-r.md-1")
	foreign := filepath.Join(dest, "Photos", ".tmp-upload")
	for _, p := range []string{ours, root, work, foreign} {
		mustWriteFile(t, p, "half")
		old := time.Now().Add(-2 * OrphanTempAge)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if n := RemoveOrphanTemps(dest); n != 3 {
		t.Errorf("removed %d, want 3", n)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Error("another program's temp file was deleted")
	}
}

// An unchanged file is not rewritten: every write is an upload.
func TestWriteFileAtomicLeavesIdenticalContentAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "INDEX.md")
	if err := WriteFileAtomic(path, []byte("same"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("same"), 0o644); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); !SameMTime(info.ModTime(), old) {
		t.Error("identical content was written again")
	}
}

// The hook used to skip whenever another run held the lock; it now waits.
func TestAcquireLockWait(t *testing.T) {
	root := t.TempDir()
	held, err := AcquireLock(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLockWait(root, 1500*time.Millisecond); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("want ErrLockHeld after waiting, got %v", err)
	}
	go func() {
		time.Sleep(time.Second)
		held.Release()
	}()
	l, err := AcquireLockWait(root, 10*time.Second)
	if err != nil {
		t.Fatalf("the lock was released but never taken: %v", err)
	}
	l.Release()
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
