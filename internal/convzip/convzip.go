// Package convzip packs one archived conversation, its transcript and everything under
// its folder, into <id>.conv.zip, and reads or unpacks it again. The archive is shared
// with other machines, so every zip is read through the hardened work.ReadPackIndex and
// work.ExtractPackLimit, and Zip deletes the loose files only after reading the zip back.
package convzip

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/work"
)

// Suffix ends the name of a conversation's zip.
const Suffix = ".conv.zip"

// ErrTooLarge is Zip's refusal of a conversation over the cap; it stays loose.
var ErrTooLarge = errors.New("conversation too large to zip")

// ErrZipExists is Zip's refusal to write over a zip that is already there.
var ErrZipExists = errors.New("a zip already exists; unzip it first")

// ErrNotHeld is DropHeld's answer when a loose file holds what the zip beside it lacks.
// It changes nothing.
var ErrNotHeld = errors.New("a loose file holds what the zip lacks")

// ErrNotRemoved is DropHeld's answer when the zip holds every loose file but removing
// them stopped part way: the zip stays, and so do the files not yet removed.
var ErrNotRemoved = errors.New("the zip holds every loose file beside it, but not all could be removed")

// ErrDiverged is Unzip's refusal when the loose and zipped copies of a transcript each
// hold turns the other lacks. It changes nothing.
var ErrDiverged = errors.New("loose and zipped transcripts have each turns the other lacks")

// Test hooks: the cap, how Zip reads its zip back, and how Unzip writes each entry.
var (
	maxBytes int64 = work.MaxFolderBytes
	readBack       = work.ReadPackIndex
	copyFile       = archive.CopyFile
)

// Result is what Zip wrote: files packed, their bytes, and the zip's size.
type Result struct {
	Files  int
	Raw    int64
	Packed int64
}

// Path is where id's zip lives in bucketDir.
func Path(bucketDir, id string) string { return filepath.Join(bucketDir, id+Suffix) }

// Find maps each conversation id (lower-cased) with a zip in bucketDir to its path.
// Only exact names count: a sync client's conflict copy is not read, zipped over or
// deleted. Links and folders are skipped.
func Find(bucketDir string) map[string]string {
	out := map[string]string{}
	ents, _ := os.ReadDir(bucketDir)
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), Suffix)
		if ok && e.Type().IsRegular() && claude.IsSessionID(id) {
			out[strings.ToLower(id)] = filepath.Join(bucketDir, e.Name())
		}
	}
	return out
}

// Entries lists a zip's entries from its central directory alone.
func Entries(zipPath string) (map[string]work.PackEntry, error) {
	return work.ReadPackIndex(zipPath)
}

// loose lists the conversation's files in bucketDir: the transcript and every regular
// file under <id>/, named as their zip entries are.
func loose(bucketDir, id string) ([]work.PackFile, error) {
	var files []work.PackFile
	add := func(rel, path string, info fs.FileInfo) {
		files = append(files, work.PackFile{Rel: rel, Path: path, Size: info.Size(), ModTime: info.ModTime()})
	}
	tp := filepath.Join(bucketDir, id+".jsonl")
	if st, err := os.Lstat(tp); err == nil && st.Mode().IsRegular() {
		add(id+".jsonl", tp, st)
	}
	root := filepath.Join(bucketDir, id)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(bucketDir, p)
		if err != nil {
			return err
		}
		add(filepath.ToSlash(rel), p, info)
		return nil
	})
	return files, err
}

// Zip packs id's transcript and folder into its zip, reads the zip back, and only then
// deletes the loose files it packed, the transcript first. Any failed check leaves them
// and removes only the temp it wrote. A conversation over the cap is left alone with
// ErrTooLarge, and one with no transcript with os.ErrNotExist.
func Zip(bucketDir, id string) (Result, error) {
	dst := Path(bucketDir, id)
	if exists(dst) {
		return Result{}, ErrZipExists
	}
	// Unzip refuses a linked folder, so such a zip could never be unpacked again.
	if d := filepath.Join(bucketDir, id); exists(d) && !archive.IsRealDir(d) {
		return Result{}, fmt.Errorf("%s is not a plain folder; left loose", id)
	}
	// Nor one holding a link or anything else that is neither a file nor a folder: it is
	// never packed, so the folder would outlive the zip, and each sweep take it for a run
	// killed half way.
	if odd := oddEntry(filepath.Join(bucketDir, id)); odd != "" {
		return Result{}, fmt.Errorf("%s is neither a file nor a folder; left loose", work.Printable(odd))
	}
	files, err := loose(bucketDir, id)
	if err != nil {
		return Result{}, err
	}
	// loose lists the transcript first. A folder without one has nothing to show or
	// resume, and readers find a conversation by its transcript: it stays loose.
	if len(files) == 0 || files[0].Rel != id+".jsonl" {
		return Result{}, os.ErrNotExist
	}
	var res Result
	for _, f := range files {
		res.Raw += f.Size
	}
	if res.Raw > maxBytes { // the total bounds every single file too
		return Result{}, ErrTooLarge
	}
	res.Files = len(files)
	// Checked under its temp name, and only then moved into place: on a shared bucket
	// another machine's zip may have synced in at dst meanwhile, and a failed check
	// removes only what this run wrote.
	tmp, err := work.WritePackTemp(dst, files)
	if err != nil {
		return Result{}, err
	}
	if err := verify(tmp, files); err != nil {
		os.Remove(tmp)
		return Result{}, fmt.Errorf("zip not trusted, files kept: %w", err)
	}
	if exists(dst) { // zipped elsewhere since: that zip is not written over
		os.Remove(tmp)
		return Result{}, ErrZipExists
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return Result{}, err
	}
	// The zip's own time is the conversation's last activity, not the day it was zipped:
	// readers that date it by the file alone never take it for older than its last turn.
	// Failing that it keeps the time now, which only makes it look younger.
	newest := files[0].ModTime
	for _, f := range files {
		if f.ModTime.After(newest) {
			newest = f.ModTime
		}
	}
	_ = os.Chtimes(dst, newest, newest)
	if st, err := os.Stat(dst); err == nil {
		res.Packed = st.Size()
	}
	return res, removeLoose(bucketDir, id, files)
}

// removeLoose deletes the loose files a zip was proved to hold, then id's emptied
// folders. Transcript first: if the run dies between the two, the folder still holds
// the rest. Each file is checked again just before its removal: one a sync client
// replaced since the listing is not in the zip, and stays.
func removeLoose(bucketDir, id string, files []work.PackFile) error {
	for _, f := range files {
		if !unchanged(f) {
			return fmt.Errorf("%s changed meanwhile; kept", f.Rel)
		}
		if err := os.Remove(f.Path); err != nil {
			return err
		}
	}
	removeEmptyDirs(filepath.Join(bucketDir, id))
	return nil
}

// oddEntry names the first entry under root that is neither a regular file nor a
// folder, "" for none.
func oddEntry(root string) (odd string) {
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !d.Type().IsRegular() {
			odd = p
			return fs.SkipAll
		}
		return nil
	})
	return odd
}

// unchanged reports whether f is still as it was listed.
func unchanged(f work.PackFile) bool {
	st, err := os.Lstat(f.Path)
	return err == nil && st.Mode().IsRegular() && st.Size() == f.Size && st.ModTime().Equal(f.ModTime)
}

// removeEmptyDirs removes root and the folders under it that hold nothing, deepest
// first. A folder with anything left in it stays.
func removeEmptyDirs(root string) {
	var dirs []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, d := range dirs {
		os.Remove(d)
	}
}

// verify proves the zip at path holds every file as it was read, and that no file
// changed on disk while it was written.
func verify(path string, files []work.PackFile) error {
	idx, err := readBack(path)
	if err != nil {
		return err
	}
	if len(idx) != len(files) {
		return fmt.Errorf("zip holds %d entries, want %d", len(idx), len(files))
	}
	for _, f := range files {
		if e, ok := idx[f.Rel]; !ok || e.Size != f.Size {
			return fmt.Errorf("entry %s missing or the wrong size", f.Rel)
		}
		if !unchanged(f) {
			return fmt.Errorf("%s changed while zipping", f.Rel)
		}
	}
	dir, err := os.MkdirTemp("", "convzip-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	out, notes, err := work.ExtractPackLimit(path, func() (string, error) { return dir, nil }, nil, uint64(maxBytes))
	if err != nil {
		return err
	}
	if len(notes) > 0 || len(out) != len(files) {
		return fmt.Errorf("zip does not extract cleanly: %v", notes)
	}
	for _, f := range files {
		if strings.HasSuffix(f.Rel, ".jsonl") && !claude.HoldsAll(filepath.Join(dir, filepath.FromSlash(f.Rel)), f.Path) {
			return fmt.Errorf("%s lost turns in the zip", f.Rel)
		}
	}
	return nil
}

// extract unpacks the zip into a new dir under parent ("" for the OS temp dir), taking
// only id's entries, or with only set its transcript alone. A refused entry fails the
// lot, and so does an entry that is not id's: the zip is not one Zip wrote, and is
// never deleted unread.
func extract(zipPath, parent, id string, only bool) (dir string, out []work.PackFile, err error) {
	dir, err = os.MkdirTemp(parent, "convzip-")
	if err != nil {
		return "", nil, err
	}
	foreign := ""
	skip := func(n string, _ int64, _ time.Time) bool {
		if !mine(id, n) {
			foreign = n
			return true
		}
		return only && lowerASCII(n) != lowerASCII(id)+".jsonl"
	}
	out, notes, err := work.ExtractPackLimit(zipPath, func() (string, error) { return dir, nil }, skip, uint64(maxBytes))
	if err == nil && len(notes) > 0 {
		err = fmt.Errorf("zip refused: %s", notes[0])
	}
	if err == nil && foreign != "" {
		err = fmt.Errorf("zip holds an entry that is not this conversation's: %s", work.Printable(foreign))
	}
	if err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	return dir, out, nil
}

// mine reports whether a zip entry is id's transcript or inside id's folder. ASCII case
// is folded: Find lower-cases ids, and a drive that ignores case opens the zip either way.
func mine(id, name string) bool {
	id, name = lowerASCII(id), lowerASCII(name)
	return name == id+".jsonl" || strings.HasPrefix(name, id+"/")
}

// lowerASCII folds A-Z only: Unicode folding takes <id>.j\u017fonl for <id>.jsonl.
func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// Materialize returns a transcript path with its <id>/ folder beside it. With only
// loose files that is the archive's own path. With a zip, it is the zip extracted under
// tmpDir, loose files laid over their entries: a conversation continued on another
// machine before the zip reached it is a loose file next to a zip. For a transcript the
// loose copy goes over the zipped one unless the zipped one holds all its turns and the
// loose one lacks some of the zipped one's. cleanup removes the extraction.
func Materialize(bucketDir, id, tmpDir string) (transcript string, cleanup func(), err error) {
	zp := Path(bucketDir, id)
	lt := filepath.Join(bucketDir, id+".jsonl")
	if !isRegular(zp) {
		if !isRegular(lt) {
			return "", nil, os.ErrNotExist
		}
		return lt, func() {}, nil
	}
	dir, out, err := extract(zp, tmpDir, id, false)
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { os.RemoveAll(dir) }
	files, err := loose(bucketDir, id)
	if err == nil {
		for _, f := range files {
			dst := filepath.Join(dir, filepath.FromSlash(f.Rel))
			if strings.HasSuffix(f.Rel, ".jsonl") && isRegular(dst) &&
				claude.HoldsAll(dst, f.Path) && !claude.HoldsAll(f.Path, dst) {
				continue
			}
			if err = archive.CopyFile(f.Path, dst); err != nil {
				break
			}
		}
	}
	if err != nil {
		cleanup()
		return "", nil, err
	}
	for _, f := range out {
		if strings.EqualFold(f.Rel, id+".jsonl") {
			return f.Path, cleanup, nil
		}
	}
	if p := filepath.Join(dir, id+".jsonl"); isRegular(p) {
		return p, cleanup, nil // from the loose copy
	}
	cleanup()
	return "", nil, os.ErrNotExist
}

// TranscriptOnly extracts id's transcript alone from its zip in bucketDir, under tmpDir,
// checked as Materialize checks the whole zip, for a reader that needs no folder file:
// a listing's opening lines. cleanup removes the extraction.
func TranscriptOnly(bucketDir, id, tmpDir string) (transcript string, cleanup func(), err error) {
	dir, out, err := extract(Path(bucketDir, id), tmpDir, id, true)
	if err != nil {
		return "", nil, err
	}
	if len(out) != 1 {
		os.RemoveAll(dir)
		return "", nil, os.ErrNotExist
	}
	return out[0].Path, func() { os.RemoveAll(dir) }, nil
}

// Unzip writes each entry into the bucket, then deletes the zip. A loose file is kept
// when it is at or after the entry's modification time; a transcript is judged by its
// turns instead, never by time: the loose copy stays when it holds all the zipped
// one's, is replaced when the zipped one holds all of it, and when neither does Unzip
// changes nothing and returns ErrDiverged. Any refused or foreign entry fails it, and
// then the zip stays. Entries are unpacked outside the bucket and moved in one file at
// a time through a temp beside the target. A zip replaced meanwhile - another
// machine's, synced in - is not the one read, and stays beside the loose files written.
func Unzip(bucketDir, id string) error {
	zp := Path(bucketDir, id)
	read, err := os.Lstat(zp)
	if err != nil || !read.Mode().IsRegular() {
		return os.ErrNotExist
	}
	dir, files, err := extract(zp, "", id, false)
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	var write []work.PackFile
	for _, f := range files {
		dst := filepath.Join(bucketDir, filepath.FromSlash(f.Rel))
		if !archive.ThroughRealDirs(bucketDir, dst) {
			return fmt.Errorf("%s: a folder on its way is a link; zip kept", f.Rel)
		}
		st, err := os.Lstat(dst)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil || !st.Mode().IsRegular():
			return fmt.Errorf("%s is not a regular file", f.Rel)
		case strings.HasSuffix(f.Rel, ".jsonl"):
			zipHolds, looseHolds := claude.HoldsAll(f.Path, dst), claude.HoldsAll(dst, f.Path)
			if !zipHolds && !looseHolds {
				return fmt.Errorf("%s: %w", f.Rel, ErrDiverged)
			}
			if looseHolds {
				continue
			}
		case !st.ModTime().Before(f.ModTime):
			continue
		}
		write = append(write, f)
	}
	for _, f := range write {
		os.Chmod(f.Path, 0o644) // extraction makes it private; archive files are not
		dst := filepath.Join(bucketDir, filepath.FromSlash(f.Rel))
		if err := copyFile(f.Path, dst); err != nil { // temp, mtime, rename
			return err
		}
	}
	if now, err := os.Lstat(zp); err != nil || !os.SameFile(read, now) || now.Size() != read.Size() || !now.ModTime().Equal(read.ModTime()) {
		return nil // the sweep settles the pair, once it has stood
	}
	return os.Remove(zp)
}

// DropHeld removes id's loose files in bucketDir when the zip beside them holds every
// one: a transcript by its turns, any other file at its size and time, the zip read
// back in full first. Loose files beside a zip are most often another machine's: it
// zipped them in this shared bucket, and its deletions have not arrived yet. Unzipping
// would remove that zip on the strength of files on their way out, so the zip is never
// removed or rewritten here. A loose file the zip does not hold changes nothing:
// ErrNotHeld.
func DropHeld(bucketDir, id string) error {
	zp := Path(bucketDir, id)
	if !isRegular(zp) {
		return os.ErrNotExist
	}
	files, err := loose(bucketDir, id)
	if err != nil || len(files) == 0 {
		return err
	}
	dir, out, err := extract(zp, "", id, false)
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	zipped := map[string]work.PackFile{}
	for _, e := range out {
		zipped[e.Rel] = e
	}
	for _, f := range files {
		e, ok := zipped[f.Rel]
		switch {
		case !ok:
			return ErrNotHeld
		case strings.HasSuffix(f.Rel, ".jsonl"):
			if !claude.HoldsAll(e.Path, f.Path) {
				return ErrNotHeld
			}
		case e.Size != f.Size || !archive.SameMTime(e.ModTime, f.ModTime):
			return ErrNotHeld
		}
	}
	if !isRegular(zp) { // removed meanwhile: the loose files may be all there is
		return os.ErrNotExist
	}
	if err := removeLoose(bucketDir, id, files); err != nil {
		return fmt.Errorf("%w: %v", ErrNotRemoved, err)
	}
	return nil
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// isRegular is the test Find applies: a link or folder is not a file of ours.
func isRegular(p string) bool { st, err := os.Lstat(p); return err == nil && st.Mode().IsRegular() }
