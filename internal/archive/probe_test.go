package archive

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	for _, p := range []string{"/run/user/1000/gvfs/google-drive:host=x/My Drive", "/home/u/.gvfs/x", "/mnt/google-drive:foo/x"} {
		if !isGvfs(p) {
			t.Errorf("%s should be gvfs", p)
		}
	}
	if isGvfs("/home/u/Insync/a@b/Google Drive") {
		t.Error("Insync folder is not gvfs")
	}
	err := Writable("/run/user/1000/gvfs/google-drive:host=x")
	if err == nil || !strings.HasPrefix(err.Error(), "This is GNOME's Google Drive view, which is not a normal folder.") {
		t.Errorf("got %v", err)
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
