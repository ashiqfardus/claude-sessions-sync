package archive

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func init() { probeRetryDelay = 0 }

func probeLeft(t *testing.T, dir string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, ".cs-probe-*"))
	return m
}

func TestProbeNormalFolderPassesAndLeavesNothing(t *testing.T) {
	d := t.TempDir()
	if err := Writable(d); err != nil {
		t.Fatal(err)
	}
	if got := probeLeft(t, d); len(got) != 0 {
		t.Errorf("left behind: %v", got)
	}
}

func TestProbeNamesTheFailedProperty(t *testing.T) {
	cases := []struct {
		name string
		seam func(*probeOps)
		want string
	}{
		{"times", func(o *probeOps) { o.chtimes = func(string, time.Time, time.Time) error { return nil } }, "this folder does not keep file times"},
		{"times error", func(o *probeOps) { o.chtimes = func(string, time.Time, time.Time) error { return errors.New("x") } }, "this folder does not keep file times"},
		{"rename", func(o *probeOps) { o.rename = func(string, string) error { return errors.New("x") } }, "this folder cannot replace a file by renaming"},
		{"rename lies", func(o *probeOps) { o.rename = func(string, string) error { return nil } }, "this folder cannot replace a file by renaming"},
		{"excl", func(o *probeOps) {
			o.open = func(p string, f int, m os.FileMode) (*os.File, error) { return os.OpenFile(p, f&^os.O_EXCL, m) }
		}, "this folder does not support creating a file only if it is new"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ops := realProbeOps
			c.seam(&ops)
			d := t.TempDir()
			err := probeFolder(d, ops)
			if err == nil || err.Error() != c.want {
				t.Fatalf("got %v, want %q", err, c.want)
			}
			if got := probeLeft(t, d); len(got) != 0 {
				t.Errorf("left behind: %v", got)
			}
		})
	}
}

func TestProbeRefusesGvfs(t *testing.T) {
	for _, p := range []string{"/run/user/1000/gvfs/google-drive:host=x/My Drive", "/home/u/.gvfs/x", "/mnt/google-drive:foo/x", "/run/user/1000/gvfs"} {
		if !isGvfs(p) {
			t.Errorf("%s should be gvfs", p)
		}
	}
	for _, p := range []string{"/home/u/Insync/a@b/Google Drive", "/home/u/work/gvfs/archive", "/.gvfs-old/x", "/home/u/my google-drive: notes/x", "/run/user/abc/gvfs/x"} {
		if isGvfs(p) {
			t.Errorf("%s is not gvfs", p)
		}
	}
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, ".gvfs", "x"), 0o755)
	link := filepath.Join(d, "link")
	if err := os.Symlink(filepath.Join(d, ".gvfs", "x"), link); err == nil {
		if !isGvfs(link) {
			t.Error("a link to a gvfs path must be refused")
		}
	}
	err := Writable("/run/user/1000/gvfs/google-drive:host=x")
	if err == nil || !strings.HasPrefix(err.Error(), "This is GNOME's Google Drive view, which is not a normal folder.") {
		t.Errorf("got %v", err)
	}
}

func TestProbeRetriesTransientFailure(t *testing.T) {
	ops := realProbeOps
	n := 0
	ops.rename = func(a, b string) error {
		if n++; n < 3 {
			return errors.New("locked")
		}
		return os.Rename(a, b)
	}
	if err := probeFolder(t.TempDir(), ops); err != nil {
		t.Fatal(err)
	}
}

// A sync client or antivirus can hold the file for longer than three tries.
func TestProbeWaitsForAHeldRename(t *testing.T) {
	ops := realProbeOps
	n := 0
	ops.rename = func(a, b string) error {
		if n++; n < 5 {
			return errors.New("locked")
		}
		return os.Rename(a, b)
	}
	if err := probeFolder(t.TempDir(), ops); err != nil {
		t.Fatal(err)
	}
}

// A FUSE or File Provider cache can serve the old bytes just after a rename that
// worked: read again, never rename a file that is gone.
func TestProbeRereadsAStaleReadAfterRename(t *testing.T) {
	ops := realProbeOps
	renames, reads := 0, 0
	ops.rename = func(a, b string) error { renames++; return os.Rename(a, b) }
	ops.read = func(p string) ([]byte, error) {
		if reads++; reads < 3 {
			return []byte("first"), nil
		}
		return os.ReadFile(p)
	}
	if err := probeFolder(t.TempDir(), ops); err != nil {
		t.Fatal(err)
	}
	if renames != 1 {
		t.Errorf("renamed %d times, want once", renames)
	}
}

func TestProbeStaleCleanup(t *testing.T) {
	d := t.TempDir()
	old := filepath.Join(d, ".cs-probe-old")
	fresh := filepath.Join(d, ".cs-probe-fresh")
	other := filepath.Join(d, ".other-old")
	target := t.TempDir()
	link := filepath.Join(d, ".cs-probe-link")
	for _, p := range []string{old, fresh, other} {
		os.Mkdir(p, 0o755)
	}
	hour2 := time.Now().Add(-2 * time.Hour)
	for _, p := range []string{old, other, target} {
		os.Chtimes(p, hour2, hour2)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skip("no symlinks")
	}
	os.Chtimes(link, hour2, hour2)
	if err := Writable(d); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(old); err == nil {
		t.Error("stale probe kept")
	}
	for _, p := range []string{fresh, other, link, target} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s was removed", p)
		}
	}
}

func TestDetectListsLinuxSyncApps(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, d := range []string{"Insync/a@b/Google Drive", "Insync/a@b/My Drive"} {
		os.MkdirAll(filepath.Join(home, d), 0o755)
	}
	c, _, _ := detectLists()
	has := func(rel string) bool {
		for _, x := range c {
			if x == filepath.Join(home, rel, archiveFolderName) {
				return true
			}
		}
		return false
	}
	for _, r := range []string{"Insync/a@b/Google Drive", "Insync/a@b/My Drive", "Nextcloud", "ownCloud"} {
		if !has(r) {
			t.Errorf("missing %s", r)
		}
	}
}
