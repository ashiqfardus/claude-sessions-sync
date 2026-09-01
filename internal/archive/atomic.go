package archive

import (
	"os"
	"path/filepath"
	"time"
)

// MTimeToleranceSeconds is the slack allowed when comparing modification times.
//
// Google Drive's virtual filesystem stores coarser timestamps than NTFS: a file
// copied there reads back rounded (.2718814Z -> .2710000Z), so it is a fraction of a
// millisecond OLDER than its source. An exact comparison therefore fails for every
// file on every run and re-uploads the entire archive - this shipped once, and cost
// 19 redundant uploads every 30 minutes.
//
// Two seconds is the same idea as rsync --modify-window, and also covers FAT/exFAT
// on a USB stick, whose timestamps are only accurate to 2 seconds.
const MTimeToleranceSeconds = 2

// NeedsCopy reports whether src must be copied to dst.
//
// Size differences are decisive. Timestamps are compared with tolerance, never
// exactly. Any future size/mtime comparison against a synced or removable filesystem
// needs the same treatment.
func NeedsCopy(src, dst os.FileInfo) bool {
	if dst == nil {
		return true
	}
	if src.Size() != dst.Size() {
		return true
	}
	cutoff := src.ModTime().Add(-MTimeToleranceSeconds * time.Second)
	return dst.ModTime().Before(cutoff)
}

// WriteFileAtomic writes data to path via a temporary file in the same directory,
// then renames it into place.
//
// A sync client watches this folder and uploads whatever it sees. Writing in place
// means it can upload a half-written index or manifest; a rename is atomic on every
// filesystem this tool targets, so a reader sees either the old file or the new one.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}

	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	// Flush to disk before the rename: a crash between the two would otherwise leave
	// a correctly-named file with no contents, which is worse than no file.
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		os.Remove(tmpName)
		return err
	}

	// os.Rename replaces an existing file on every supported platform - on Windows Go
	// uses MoveFileEx with MOVEFILE_REPLACE_EXISTING. An earlier version of this
	// function "worked around" a non-existent Windows limitation by deleting the
	// destination first and retrying; that turned a failed write into the loss of the
	// file that was already there. Never delete the destination to make room.
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// Writable reports whether dir can actually be written to.
//
// A read-only mount, an exhausted quota or a permissions problem passes every other
// check while backing up precisely nothing, so probe rather than assume.
func Writable(dir string) error {
	f, err := os.CreateTemp(dir, ".write-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_, werr := f.Write([]byte("probe"))
	cerr := f.Close()
	os.Remove(name)
	if werr != nil {
		return werr
	}
	return cerr
}
