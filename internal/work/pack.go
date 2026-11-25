package work

import (
	"archive/zip"
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	// PackSuffix names a unit's pack, beside the unit's loose folder in the archive.
	PackSuffix = ".cspack.zip"
	// PackMaxEntries is the most entries a pack may hold; restore refuses more.
	PackMaxEntries = 20000
	// PackMaxBytes is the largest pack file read: MaxFolderBytes of content plus room
	// for the headers of PackMaxEntries entries. Push never writes a larger one.
	PackMaxBytes = MaxFolderBytes + 16<<20
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
	r, f, err := openPack(old)
	if err != nil {
		return err
	}
	tmp, err := buildPack(dst, r.File, add)
	f.Close() // before the rename: Windows cannot replace an open file
	if err != nil {
		return err
	}
	return commitPack(tmp, dst)
}

// PrunePack rewrites the pack at path without the entries drop names, copied as they
// are through a temp file and a rename, and removes the pack when none are left. It
// returns how many entries it took out. A pack it cannot read is left as it is.
func PrunePack(path string, drop func(name string) bool) (int, error) {
	r, f, err := openPack(path)
	if err != nil {
		return 0, err
	}
	keep := slices.DeleteFunc(slices.Clone(r.File), func(f *zip.File) bool { return drop(f.Name) })
	n := len(r.File) - len(keep)
	if n == 0 || len(keep) == 0 {
		f.Close()
		if n > 0 {
			err = os.Remove(path)
		}
		return n, err
	}
	tmp, err := buildPack(path, keep, nil)
	f.Close() // before the rename: Windows cannot replace an open file
	if err != nil {
		return 0, err
	}
	return n, commitPack(tmp, path)
}

// ReadPackIndex reads a pack's entries from its central directory alone.
func ReadPackIndex(path string) (map[string]PackEntry, error) {
	r, f, err := openPack(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	idx := make(map[string]PackEntry, len(r.File))
	for _, f := range r.File {
		idx[f.Name] = PackEntry{Size: int64(f.UncompressedSize64), ModTime: f.Modified}
	}
	return idx, nil
}

// ExtractPack writes the entries of the pack at path that restore may take to
// <dir>/<name>, each stamped with its header's time, and returns them, with notes
// naming the entries refused. A pack is shared archive data, as untrusted as a loose
// file. Before anything is written, the whole pack is refused when openPack refuses
// it or for more than MaxFolderBytes declared in all. Each entry is
// refused alone for a name that is not a plain path inside the pack, a type other
// than a regular file, more than MaxFileBytes declared or read, or a CRC mismatch.
// skip, given each name, declared size and header time, drops an entry unwritten: the
// caller's own checks run there, before an entry touches the disk. tmpDir makes dir,
// once, at the first entry to write, so a pack with nothing to unpack makes none; with
// tmpDir nil nothing is read or written, and the entries are listed as they would be,
// for a dry run. An error refuses the whole pack; the caller removes dir.
func ExtractPack(path string, tmpDir func() (string, error), skip func(rel string, size int64, mtime time.Time) bool) ([]PackFile, []string, error) {
	return ExtractPackLimit(path, tmpDir, skip, MaxFileBytes)
}

// ExtractPackLimit is ExtractPack with the per-entry cap maxFile in place of
// MaxFileBytes. The total stays capped at MaxFolderBytes.
func ExtractPackLimit(path string, tmpDir func() (string, error), skip func(rel string, size int64, mtime time.Time) bool, maxFile uint64) ([]PackFile, []string, error) {
	r, in, err := openPack(path)
	if err != nil {
		return nil, nil, err
	}
	defer in.Close()
	var total uint64
	for _, f := range r.File {
		if total += min(f.UncompressedSize64, MaxFolderBytes+1); total > MaxFolderBytes { // capped: never wraps
			return nil, nil, fmt.Errorf("over %d MB in all", MaxFolderBytes>>20)
		}
	}
	var out []PackFile
	var notes []string
	dir := ""
	for _, f := range r.File {
		why := ""
		switch {
		case !packName(f.Name):
			why = "not a plain path inside the pack"
		case !f.Mode().IsRegular():
			why = "not a regular file"
		case f.UncompressedSize64 > maxFile:
			why = fmt.Sprintf("over %d MB", maxFile>>20)
		}
		if why == "" && skip != nil && skip(f.Name, int64(f.UncompressedSize64), f.Modified) {
			continue
		}
		pf := PackFile{Rel: f.Name, Size: int64(f.UncompressedSize64), ModTime: f.Modified}
		if why == "" && tmpDir != nil {
			if dir == "" {
				if dir, err = tmpDir(); err != nil {
					return nil, notes, err
				}
			}
			pf.Path = filepath.Join(dir, filepath.FromSlash(f.Name))
			if err := extractOne(f, pf.Path, maxFile); err != nil {
				// Not the path: it is built from the name, which the note quotes.
				var pe *fs.PathError
				if errors.As(err, &pe) {
					err = pe.Err
				}
				why = err.Error()
			}
		}
		if why != "" {
			notes = append(notes, fmt.Sprintf("entry \"%s\": %s", Printable(f.Name), why))
			continue
		}
		out = append(out, pf)
	}
	return out, notes, nil
}

// Printable drops control characters from text read out of the archive before it is
// printed, a tab becoming a space. Names and prompts there are written by other
// machines, and an escape sequence among them could retitle the terminal, rewrite what
// is on screen or, through OSC 52, set the clipboard.
func Printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// openPack opens the pack at path for zip to read, closing the file on an error;
// the caller closes f. Every reader goes through it. The pack must be a regular file,
// not a link, that stays the one checked while it is opened, no larger than
// PackMaxBytes, and whose directory checkDirectory accepts. zip's insecure-path error
// is ignored: ExtractPack checks every name itself.
func openPack(path string) (r *zip.Reader, f *os.File, err error) {
	l, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !l.Mode().IsRegular() { // checked before the open: a pipe would block it
		return nil, nil, errors.New("not a regular file")
	}
	if f, err = os.Open(path); err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	switch {
	case err != nil || !st.Mode().IsRegular() || !os.SameFile(l, st):
		err = errors.New("changed while being opened")
	case st.Size() > PackMaxBytes:
		err = fmt.Errorf("over %d MB", PackMaxBytes>>20)
	default:
		if err = checkDirectory(f, st.Size()); err == nil {
			if r, err = zip.NewReader(f, st.Size()); errors.Is(err, zip.ErrInsecurePath) {
				err = nil
			}
		}
		// Bytes rewritten in place since the walk would show here.
		if err == nil && len(r.File) > PackMaxEntries {
			err = fmt.Errorf("zip read %d entries", len(r.File))
		}
	}
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return r, f, nil
}

// checkDirectory walks a zip's central directory before zip parses it. zip reads
// directory headers until one fails and checks the end record's count only modulo
// 65,536, so a lying end record lets a small file hold millions of entries, each
// taking memory. A pack is refused unless, as push writes it, its end record closes
// the file with no comment, the directory runs from its offset to that record, no
// zip64 record is in play, and the headers tile it exactly, PackMaxEntries at most.
// zip then starts and stops where this walk did.
func checkDirectory(r io.ReaderAt, size int64) error {
	bad := errors.New("not a complete zip")
	le := binary.LittleEndian
	var end [22]byte
	if size < 22 {
		return bad
	}
	if _, err := r.ReadAt(end[:], size-22); err != nil {
		return bad
	}
	eocd := size - 22
	records, dirSize, dirOff := int64(le.Uint16(end[10:])), int64(le.Uint32(end[12:])), int64(le.Uint32(end[16:]))
	if le.Uint32(end[:]) != 0x06054b50 || le.Uint16(end[20:]) != 0 || dirOff+dirSize != eocd {
		return bad
	}
	if records > PackMaxEntries {
		return fmt.Errorf("over %d entries", PackMaxEntries)
	}
	// The other zip64 markers cannot pass the checks above; older Go also reads a
	// zip64 record when the size is 0xffff and a locator precedes the end record.
	if dirSize == 0xffff {
		var loc [4]byte
		if _, err := r.ReadAt(loc[:], eocd-20); err != nil || le.Uint32(loc[:]) == 0x07064b50 {
			return bad
		}
	}
	br := bufio.NewReader(io.NewSectionReader(r, dirOff, dirSize))
	var h [46]byte
	n := int64(0)
	for pos := dirOff; pos < eocd; n++ {
		if n == PackMaxEntries {
			return fmt.Errorf("over %d entries", PackMaxEntries)
		}
		if _, err := io.ReadFull(br, h[:]); err != nil || le.Uint32(h[:]) != 0x02014b50 {
			return bad
		}
		rest := int(le.Uint16(h[28:])) + int(le.Uint16(h[30:])) + int(le.Uint16(h[32:])) // name, extra, comment
		if _, err := br.Discard(rest); err != nil {
			return bad
		}
		pos += int64(len(h) + rest)
	}
	if n != records {
		return bad
	}
	return nil
}

// packName reports whether n names a file inside a pack: "/"-separated names, none
// empty, "." or "..", with no backslash or drive, and local on this OS.
func packName(n string) bool {
	return n != "" && !strings.Contains(n, `\`) && !(len(n) >= 2 && n[1] == ':') &&
		!slices.ContainsFunc(strings.Split(n, "/"), func(s string) bool { return s == "" || s == "." || s == ".." }) &&
		filepath.IsLocal(filepath.FromSlash(n))
}

// extractOne writes entry f to p, a new file, read through the zip's own size and
// CRC checks and never past maxFile, with f's time. A failed entry leaves no
// file.
func extractOne(f *zip.File, p string, maxFile uint64) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	// O_EXCL: a second entry of one name, or of one a case-blind disk folds into it,
	// never replaces the first.
	out, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(rc, int64(maxFile)+1))
	if err == nil && uint64(n) > maxFile {
		err = fmt.Errorf("over %d MB", maxFile>>20)
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chtimes(p, f.Modified, f.Modified)
	}
	if err != nil {
		os.Remove(p)
	}
	return err
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
	if err == nil {
		var st os.FileInfo
		if st, err = tmp.Stat(); err == nil && st.Size() > PackMaxBytes {
			err = fmt.Errorf("pack over %d MB", PackMaxBytes>>20) // restore would refuse it
		}
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
