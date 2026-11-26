package archive

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	probePrefix = ".cs-probe-"
	probeStale  = time.Hour
)

// probeOps are the filesystem calls the probe depends on, so tests can simulate a
// folder that lacks one of them.
type probeOps struct {
	open    func(string, int, os.FileMode) (*os.File, error)
	rename  func(string, string) error
	chtimes func(string, time.Time, time.Time) error
}

var realProbeOps = probeOps{os.OpenFile, os.Rename, os.Chtimes}

// isGvfs reports whether p is GNOME Online Accounts' Drive view (a gvfs mount), which
// is not a normal folder: no rename-over, no kept times.
func isGvfs(p string) bool {
	p = filepath.ToSlash(p)
	return strings.Contains(p, "/gvfs/") || strings.Contains(p, "/.gvfs") || strings.Contains(p, "google-drive:")
}

const gvfsMessage = "This is GNOME's Google Drive view, which is not a normal folder. Use a sync app that keeps a real folder: see Linux and other sync apps in the README."

// Writable reports whether dir behaves like a synced folder: it can be written to,
// creates files exclusively, replaces a file by renaming over it and keeps file times.
// The tool depends on all four (WriteFileAtomic, R23 zip times, size+mtime checks).
//
// NOTE: this writes, in a hidden .cs-probe-* subfolder it always removes. doctor
// exposes --no-write-probe to skip it.
func Writable(dir string) error {
	if isGvfs(dir) {
		return errors.New(gvfsMessage)
	}
	return probeFolder(dir, realProbeOps)
}

func probeFolder(dir string, ops probeOps) error {
	removeStaleProbes(dir)
	sub, err := os.MkdirTemp(dir, probePrefix+"*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(sub)

	a, b := filepath.Join(sub, "a"), filepath.Join(sub, "b")
	f, err := ops.open(a, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.WriteString("first")
	if serr := f.Sync(); werr == nil {
		werr = serr
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	if g, err := ops.open(a, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
		g.Close()
		return errors.New("this folder does not support creating a file only if it is new")
	}

	if err := os.WriteFile(b, []byte("second"), 0o600); err != nil {
		return err
	}
	if err := ops.rename(b, a); err != nil {
		return errors.New("this folder cannot replace a file by renaming")
	}
	if got, err := os.ReadFile(a); err != nil || !bytes.Equal(got, []byte("second")) {
		return errors.New("this folder cannot replace a file by renaming")
	}

	want := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := ops.chtimes(a, want, want); err != nil {
		return errors.New("this folder does not keep file times")
	}
	if st, err := os.Lstat(a); err != nil || !SameMTime(st.ModTime(), want) {
		return errors.New("this folder does not keep file times")
	}
	return nil
}

// removeStaleProbes removes probe folders a crashed run left behind. Our prefix only,
// real directories only (never through a link), and only when older than an hour so a
// probe running on another computer is left alone.
func removeStaleProbes(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), probePrefix) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		st, err := os.Lstat(p)
		if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || time.Since(st.ModTime()) < probeStale {
			continue
		}
		os.RemoveAll(p)
	}
}
