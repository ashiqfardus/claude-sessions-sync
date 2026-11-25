package archive

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// CopyFile copies src to dst, preserving the modification time.
//
// Preserving mtime is not cosmetic: change detection compares the destination's
// timestamp against the source's, so stamping the copy with "now" would make every
// comparison meaningless and the archive's own listing misleading about when a
// session actually happened.
//
// The copy goes to a temporary file and is renamed into place, so a sync client
// watching the folder never uploads a half-written transcript.
func CopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(dst)+"-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, info.Mode().Perm()); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chtimes(tmpName, info.ModTime(), info.ModTime()); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// StatOrNil returns the FileInfo for path, or nil when it does not exist.
//
// NeedsCopy takes a nil destination to mean "not there yet", so this keeps callers
// free of os.IsNotExist branching.
func StatOrNil(path string) os.FileInfo {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	return info
}

// IsRealDir reports whether path is a directory itself, not a link or junction to
// one.
//
// Folders read from, or written into, the archive are checked with it. The archive is
// shared, and a symlink planted there makes restore copy - or render delete - files
// wherever the link points.
func IsRealDir(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().Type() == fs.ModeDir
}

// ThroughRealDirs reports whether every folder below root on the way to path that
// exists is a real one. CopyFile's MkdirAll and Rename follow links, so a write into
// the shared archive checks this first: a planted link would take it outside.
func ThroughRealDirs(root, path string) bool {
	root = filepath.Clean(root)
	for d := filepath.Dir(path); d != root && d != filepath.Dir(d); d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err == nil && !IsRealDir(d) {
			return false
		}
	}
	return true
}
