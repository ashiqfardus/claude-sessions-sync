package archive

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Change detection compares the destination's timestamp against the source's, so a
// copy stamped with "now" would make every later comparison meaningless and the
// archive's own listing wrong about when a session happened.
func TestCopyFilePreservesModTime(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "session.jsonl")
	dst := filepath.Join(dir, "out", "session.jsonl")

	if err := os.WriteFile(src, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(src, want, want); err != nil {
		t.Fatal(err)
	}

	if err := CopyFile(src, dst); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if diff := info.ModTime().Sub(want); diff > time.Second || diff < -time.Second {
		t.Errorf("mtime = %v, want %v (off by %v)", info.ModTime(), want, diff)
	}

	// And the copy must therefore be recognised as unchanged.
	srcInfo, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	if NeedsCopy(srcInfo, info) {
		t.Error("a fresh copy must not immediately need copying again")
	}
}

func TestCopyFileLeavesNoTempOnSuccess(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.jsonl")
	if err := os.WriteFile(src, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	if err := CopyFile(src, filepath.Join(out, "a.jsonl")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("expected only the copied file, got %d entries", len(entries))
	}
}

func TestStatOrNil(t *testing.T) {
	if StatOrNil(filepath.Join(t.TempDir(), "nope")) != nil {
		t.Error("a missing file should yield nil")
	}
}
