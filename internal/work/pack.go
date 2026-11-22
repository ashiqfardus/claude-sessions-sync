package work

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"
)

const (
	// PackSuffix names a unit's pack, beside the unit's loose folder in the archive.
	PackSuffix = ".cspack.zip"
	// PackMaxEntries is the most entries a pack may hold; restore refuses more.
	PackMaxEntries = 20000
)

// PackFile is one file to put in a pack: Rel, "/"-separated and relative to the unit,
// names its entry, and Path is the file read.
type PackFile struct {
	Rel, Path string
	Size      int64
	ModTime   time.Time
}

// PackEntry is one entry of a pack's index.
type PackEntry struct {
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mtime"`
}

// WritePack writes files to the pack at dst, sorted by Rel. It writes a temp file
// beside dst and renames it into place, so a reader or a sync client sees the old
// pack or the new one, never half of one.
func WritePack(dst string, files []PackFile) error {
	tmp, err := buildPack(dst, nil, files)
	if err != nil {
		return err
	}
	return commitPack(tmp, dst)
}

// CompactPack writes to dst the entries of the pack at old, copied as they are, except
// those add replaces, plus add. old may be dst.
func CompactPack(old, dst string, add []PackFile) error {
	r, err := zip.OpenReader(old)
	if err != nil {
		return err
	}
	tmp, err := buildPack(dst, r.File, add)
	r.Close() // before the rename: Windows cannot replace an open file
	if err != nil {
		return err
	}
	return commitPack(tmp, dst)
}

// PrunePack rewrites the pack at path without the entries drop names, copied as they
// are through a temp file and a rename, and removes the pack when none are left. It
// returns how many entries it took out. A pack it cannot read is left as it is.
func PrunePack(path string, drop func(name string) bool) (int, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return 0, err
	}
	keep := slices.DeleteFunc(slices.Clone(r.File), func(f *zip.File) bool { return drop(f.Name) })
	n := len(r.File) - len(keep)
	if n == 0 || len(keep) == 0 {
		r.Close()
		if n > 0 {
			err = os.Remove(path)
		}
		return n, err
	}
	tmp, err := buildPack(path, keep, nil)
	r.Close() // before the rename: Windows cannot replace an open file
	if err != nil {
		return 0, err
	}
	return n, commitPack(tmp, path)
}

// ReadPackIndex reads a pack's entries from its central directory alone.
func ReadPackIndex(path string) (map[string]PackEntry, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	idx := make(map[string]PackEntry, len(r.File))
	for _, f := range r.File {
		idx[f.Name] = PackEntry{Size: int64(f.UncompressedSize64), ModTime: f.Modified}
	}
	return idx, nil
}

func commitPack(tmp, dst string) error {
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// buildPack writes keep and add, add winning on a name both hold, to a temp file
// beside dst, and returns its name. The ".tmp-" prefix lets RemoveOrphanTemps clear
// one a crash left.
func buildPack(dst string, keep []*zip.File, add []PackFile) (string, error) {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(dst)+"-*")
	if err != nil {
		return "", err
	}
	err = fillPack(tmp, keep, add)
	if err == nil {
		err = tmp.Sync() // a crash after the rename must not leave an empty pack
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

func fillPack(w io.Writer, keep []*zip.File, add []PackFile) error {
	old := map[string]*zip.File{}
	for _, f := range keep {
		old[f.Name] = f
	}
	added := map[string]PackFile{}
	for _, f := range add {
		added[f.Rel] = f
		delete(old, f.Rel)
	}
	names := make([]string, 0, len(old)+len(added))
	for n := range old {
		names = append(names, n)
	}
	for n := range added {
		names = append(names, n)
	}
	sort.Strings(names)

	zw := zip.NewWriter(w)
	for _, n := range names {
		var err error
		if f, ok := old[n]; ok {
			err = zw.Copy(f)
		} else {
			err = packOne(zw, added[n])
		}
		if err != nil {
			return err
		}
	}
	return zw.Close()
}

// packOne adds f as it was listed: at most f.Size bytes, so a file that grew since
// never passes the size Collect allowed, and stamped f.ModTime, so the next push sees
// the change and copies it loose.
func packOne(zw *zip.Writer, f PackFile) error {
	in, err := os.Open(f.Path)
	if err != nil {
		return err
	}
	defer in.Close()
	h := &zip.FileHeader{Name: f.Rel, Method: zip.Deflate, Modified: f.ModTime}
	h.SetMode(0o644)
	w, err := zw.CreateHeader(h)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, io.LimitReader(in, f.Size))
	return err
}
