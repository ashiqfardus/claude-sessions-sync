package archive

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
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

var gvfsRun = regexp.MustCompile(`^/run/user/[0-9]+/gvfs(/|$)`)

// FolderProblem is a folder that cannot act as a synced folder (as opposed to a raw
// OS error such as read-only or out of quota). doctor gives sync-app advice for it.
type FolderProblem struct{ msg string }

func (e *FolderProblem) Error() string { return e.msg }

func problem(msg string) error { return &FolderProblem{msg} }

// IsFolderProblem reports whether err is a FolderProblem.
func IsFolderProblem(err error) bool {
	var fp *FolderProblem
	return errors.As(err, &fp)
}

// isGvfs reports whether p is GNOME Online Accounts' Drive view (a gvfs mount), which
// is not a normal folder: no rename-over, no kept times. Links are resolved first,
// best effort, so a link to the mount is caught.
func isGvfs(p string) bool {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	p = filepath.ToSlash(p)
	if gvfsRun.MatchString(p) {
		return true
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".gvfs" || strings.HasPrefix(seg, "google-drive:") {
			return true
		}
	}
	return false
}

const gvfsMessage = "This is GNOME's Google Drive view, which is not a normal folder. Use a sync app that keeps a real folder: see Linux and other sync apps in the README."

// Antivirus and indexers briefly lock a file just written; retry before failing.
var probeRetryDelay = 150 * time.Millisecond

func retry3(ok func() bool) bool {
	for i := 0; i < 3; i++ {
		if i > 0 {
			time.Sleep(probeRetryDelay)
		}
		if ok() {
			return true
		}
	}
	return false
}

// Writable reports whether dir behaves like a synced folder: it can be written to,
// creates files exclusively, replaces a file by renaming over it and keeps file times.
// The tool depends on all four (WriteFileAtomic, R23 zip times, size+mtime checks).
//
// NOTE: this writes, in a hidden .cs-probe-* subfolder it always removes. doctor
// exposes --no-write-probe to skip it.
func Writable(dir string) error {
	if isGvfs(dir) {
		return problem(gvfsMessage)
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
		return problem("this folder does not support creating a file only if it is new")
	}

	if err := os.WriteFile(b, []byte("second"), 0o600); err != nil {
		return err
	}
	if !retry3(func() bool {
		if ops.rename(b, a) != nil {
			return false
		}
		got, err := os.ReadFile(a)
		return err == nil && bytes.Equal(got, []byte("second"))
	}) {
		return problem("this folder cannot replace a file by renaming")
	}

	// An even second: FAT truncates to 2 s, so an odd one would sit on the tolerance edge.
	want := time.Date(2020, 1, 2, 3, 4, 6, 0, time.UTC)
	if !retry3(func() bool {
		if ops.chtimes(a, want, want) != nil {
			return false
		}
		st, err := os.Lstat(a)
		return err == nil && SameMTime(st.ModTime(), want)
	}) {
		return problem("this folder does not keep file times")
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
