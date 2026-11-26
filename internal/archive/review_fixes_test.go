package archive

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 12. A machine already using the old ~/Google Drive folder keeps using it when a
// CloudStorage mount appears beside it: switching would split the archive in two.
func TestDetectKeepsAnOldGoogleDriveFolder(t *testing.T) {
	home := fakeHome(t)
	want := filepath.Join(home, "Google Drive", "My Drive", archiveFolderName)
	mkdirs(t, want, filepath.Join(home, "Library", "CloudStorage", "GoogleDrive-me@example.com", "My Drive", archiveFolderName))
	if got := detected(t); got != want {
		t.Fatalf("detected %q, want the folder already in use %q", got, want)
	}
}

// Third review 9: an archive in two accounts is a guess even when only one of them is
// Google Drive.
func TestDetectRefusesAnArchiveInTwoKindsOfAccount(t *testing.T) {
	home := fakeHome(t)
	cs := filepath.Join(home, "Library", "CloudStorage")
	mkdirs(t,
		filepath.Join(cs, "GoogleDrive-me@example.com", "My Drive", archiveFolderName),
		filepath.Join(cs, "OneDrive-Personal", archiveFolderName))
	if got := detected(t); got != "" {
		t.Fatalf("detected %q, want none: two accounts hold an archive", got)
	}
}

// Third review 7: a copy interrupted under forks/ or profile/ is cleaned up like any
// other, not uploaded and kept by the sync client for ever.
func TestRemoveOrphanTempsCoversForksAndProfile(t *testing.T) {
	dest := t.TempDir()
	var paths []string
	for _, p := range []string{
		filepath.Join(dest, "forks", "e--api", "s1", ".tmp-MAC.jsonl-1"),
		filepath.Join(dest, "profile", "machines", "MAC", ".tmp-CLAUDE.md-1"),
		filepath.Join(dest, "memory", "MAC", "e--api", ".tmp-MEMORY.md-1"),
	} {
		mustWriteFile(t, p, "half")
		old := time.Now().Add(-2 * OrphanTempAge)
		os.Chtimes(p, old, old)
		paths = append(paths, p)
	}
	if n := len(RemoveOrphanTemps(dest)); n != len(paths) {
		t.Errorf("removed %d, want %d", n, len(paths))
	}
}

// A run reading the lock file while the holder releases it - routine for a waiting
// run - left the file behind on Windows, where an open file cannot be deleted: the
// waiter then timed out on a lock nobody held (TestAcquireLockWait, flaky there).
// On macOS and Linux an open file can be deleted, so this only bites on Windows.
func TestReleaseRemovesTheLockWhileAnotherRunHasItOpen(t *testing.T) {
	root := t.TempDir()
	l, err := AcquireLock(root)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(root, "session-sync.lock"))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		f.Close()
	}()
	l.Release()
	if _, err := os.Stat(filepath.Join(root, "session-sync.lock")); err == nil {
		t.Fatal("the lock file outlived its release")
	}
}

// 11. An entry from the PowerShell tool's projects.json keeps the machine it names,
// which is who wrote its sessions.
func TestLegacyEntryKeepsItsMachine(t *testing.T) {
	dest := t.TempDir()
	os.WriteFile(filepath.Join(dest, "projects.json"),
		[]byte(`{"e--work-api":{"path":"E:\\work\\api","leaf":"api","machine":"LAP-1-me"}}`), 0o644)
	merged, _, err := Merged(dest)
	if err != nil {
		t.Fatal(err)
	}
	if got := merged["e--work-api"].Owner(); got != "LAP-1-me" {
		t.Fatalf("Owner() = %q, want the machine the entry names", got)
	}
}
