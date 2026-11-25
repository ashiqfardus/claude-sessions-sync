// Package convzip packs one archived conversation, its transcript and everything under
// its folder, into <id>.conv.zip, and reads or unpacks it again. The archive is shared
// with other machines, so every zip is read through the hardened work.ReadPackIndex and
// work.ExtractPack, and Zip deletes the loose files only after reading the zip back.
package convzip

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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

// Test hooks: the cap, and how Zip reads its zip back.
var (
	maxBytes int64 = work.MaxFolderBytes
	readBack       = work.ReadPackIndex
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
// deletes the loose files, the transcript first. Any failed check leaves them and
// removes the zip. A conversation over the cap is left alone with ErrTooLarge.
func Zip(bucketDir, id string) (Result, error) {
	dst := Path(bucketDir, id)
	if exists(dst) {
		return Result{}, errors.New("a zip already exists; unzip it first")
	}
	files, err := loose(bucketDir, id)
	if err != nil {
		return Result{}, err
	}
	if len(files) == 0 {
		return Result{}, os.ErrNotExist
	}
	var res Result
	for _, f := range files {
		res.Raw += f.Size
	}
	if res.Raw > maxBytes {
		return Result{}, ErrTooLarge
	}
	res.Files = len(files)
	if err := work.WritePack(dst, files); err != nil {
		return Result{}, err
	}
	if err := verify(dst, files); err != nil {
		os.Remove(dst)
		return Result{}, fmt.Errorf("zip not trusted, files kept: %w", err)
	}
	if st, err := os.Stat(dst); err == nil {
		res.Packed = st.Size()
	}
	// Transcript first: if the run dies between the two, the folder still holds the rest.
	if files[0].Rel == id+".jsonl" {
		if err := os.Remove(files[0].Path); err != nil {
			return res, err
		}
	}
	return res, os.RemoveAll(filepath.Join(bucketDir, id))
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
		if st, err := os.Lstat(f.Path); err != nil || st.Size() != f.Size || !st.ModTime().Equal(f.ModTime) {
			return fmt.Errorf("%s changed while zipping", f.Rel)
		}
	}
	dir, err := os.MkdirTemp("", "convzip-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	out, notes, err := work.ExtractPack(path, func() (string, error) { return dir, nil }, nil)
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

// extract unpacks id's zip into a new dir under parent. A refused entry fails the lot.
func extract(zipPath, parent string, skip func(string, int64, time.Time) bool) (dir string, out []work.PackFile, err error) {
	dir, err = os.MkdirTemp(parent, ".tmp-convzip-")
	if err != nil {
		return "", nil, err
	}
	out, notes, err := work.ExtractPack(zipPath, func() (string, error) { return dir, nil }, skip)
	if err == nil && len(notes) > 0 {
		err = fmt.Errorf("zip refused: %s", notes[0])
	}
	if err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	return dir, out, nil
}

// mine reports whether a zip entry is id's transcript or inside id's folder.
func mine(id, name string) bool { return name == id+".jsonl" || strings.HasPrefix(name, id+"/") }

// Materialize returns a transcript path with its <id>/ folder beside it. With only
// loose files that is the archive's own path. With a zip, it is the zip extracted under
// tmpDir, any loose file laid over its entry: a conversation continued on another
// machine before the zip reached it is a loose file next to a zip, and the loose file
// is newer. cleanup removes the extraction.
func Materialize(bucketDir, id, tmpDir string) (transcript string, cleanup func(), err error) {
	zp := Path(bucketDir, id)
	lt := filepath.Join(bucketDir, id+".jsonl")
	if !exists(zp) {
		if !exists(lt) {
			return "", nil, os.ErrNotExist
		}
		return lt, func() {}, nil
	}
	dir, _, err := extract(zp, tmpDir, func(n string, _ int64, _ time.Time) bool { return !mine(id, n) })
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { os.RemoveAll(dir) }
	files, err := loose(bucketDir, id)
	if err == nil {
		for _, f := range files {
			if err = archive.CopyFile(f.Path, filepath.Join(dir, filepath.FromSlash(f.Rel))); err != nil {
				break
			}
		}
	}
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return filepath.Join(dir, id+".jsonl"), cleanup, nil
}

// Unzip writes each entry into the bucket unless a loose file is already there with a
// modification time at or after the entry's, then deletes the zip. A loose transcript
// the zip's copy does not hold is an error, and so is any refused entry: the zip stays.
func Unzip(bucketDir, id string) error {
	zp := Path(bucketDir, id)
	skip := func(n string, _ int64, mt time.Time) bool {
		if !mine(id, n) {
			return true
		}
		st, err := os.Lstat(filepath.Join(bucketDir, filepath.FromSlash(n)))
		return err == nil && st.Mode().IsRegular() && !st.ModTime().Before(mt)
	}
	dir, files, err := extract(zp, bucketDir, skip)
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	for _, f := range files {
		dst := filepath.Join(bucketDir, filepath.FromSlash(f.Rel))
		if exists(dst) && strings.HasSuffix(f.Rel, ".jsonl") && !claude.HoldsAll(f.Path, dst) {
			return fmt.Errorf("%s has turns the zip lacks; zip kept", f.Rel)
		}
	}
	for _, f := range files {
		dst := filepath.Join(bucketDir, filepath.FromSlash(f.Rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.Rename(f.Path, dst); err != nil { // atomic, and keeps the entry's time
			return err
		}
	}
	return os.Remove(zp)
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }
