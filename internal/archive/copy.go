package archive

import (
	"io"
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
