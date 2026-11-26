package panel

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func names(l FolderList) []string {
	var n []string
	for _, e := range l.Entries {
		n = append(n, e.Name)
	}
	return n
}

func TestListFoldersNamesOnly(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"web-app", "api", ".cfg", "Notes"} {
		os.Mkdir(filepath.Join(root, d), 0o755)
	}
	os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o644)
	linked := true
	if os.Symlink(filepath.Join(root, "api"), filepath.Join(root, "dirlink")) != nil ||
		os.Symlink(filepath.Join(root, "file.txt"), filepath.Join(root, "filelink")) != nil {
		linked = false
	}
	want := []string{"api", "Notes", "web-app"}
	if linked {
		want = []string{"api", "dirlink", "Notes", "web-app"}
	}
	l := ListFolders(root, false)
	if l.Error != "" || !reflect.DeepEqual(names(l), want) {
		t.Fatalf("got %v err %q, want %v", names(l), l.Error, want)
	}
	if l.Parent != filepath.Dir(root) || l.Path != root {
		t.Fatalf("path/parent %q %q", l.Path, l.Parent)
	}
	if l.Entries[0].Path != filepath.Join(root, "api") {
		t.Fatalf("entry path %q", l.Entries[0].Path)
	}
	h := ListFolders(root, true)
	if h.Entries[0].Name != ".cfg" || len(h.Entries) != len(want)+1 {
		t.Fatalf("hidden: %v", names(h))
	}
}

func TestListFoldersRelativeRefused(t *testing.T) {
	if l := ListFolders("rel/path", false); l.Error == "" || len(l.Entries) != 0 {
		t.Fatalf("%+v", l)
	}
}

func TestListFoldersUnreadable(t *testing.T) {
	l := ListFolders(filepath.Join(t.TempDir(), "missing"), false)
	if l.Error != "You can't open this folder." {
		t.Fatalf("%+v", l)
	}
	f := filepath.Join(t.TempDir(), "f")
	os.WriteFile(f, nil, 0o644)
	if l := ListFolders(f, false); l.Error == "" {
		t.Fatal("a file is not a folder")
	}
}

func TestListFoldersCap(t *testing.T) {
	old := maxEntries
	maxEntries = 3
	t.Cleanup(func() { maxEntries = old })
	root := t.TempDir()
	for _, d := range []string{"a", "b", "c", "d", "e"} {
		os.Mkdir(filepath.Join(root, d), 0o755)
	}
	l := ListFolders(root, false)
	if len(l.Entries) != 3 || !l.Truncated {
		t.Fatalf("%+v", l)
	}
}

func TestRoots(t *testing.T) {
	r := Roots("/h", nil)
	if len(r) < 1 || r[0] != (Folder{"Home folder", "/h"}) {
		t.Fatalf("%v", r)
	}
	r = Roots("/h", []string{`C:\`, `D:\`})
	if len(r) != 3 || r[1].Path != `C:\` {
		t.Fatalf("%v", r)
	}
}

func TestListFoldersRefusesUNCAndEmpty(t *testing.T) {
	paths := []string{"", `\\host\share`, "//host/share", `\\?\C:\x`}
	if runtime.GOOS == "windows" { // Go reads any two leading separators, and \??\, as a network or device path
		paths = append(paths, `/\h\s`, `\/h/s`, `\??\UNC\h\s`, `\\.\C:\x`)
	}
	for _, p := range paths {
		// Refused as given: one cleaned first, /\h\s into \\h\s, was opened.
		if l := ListFolders(p, false); l.Error != cantOpen || len(l.Entries) != 0 || l.Path != p || l.Parent != "" {
			t.Fatalf("%q: %+v", p, l)
		}
	}
}

// On Windows only a drive-letter path stays on this computer's disks: the rule Go's
// own parsing gives as IsAbs plus a two-byte volume, checked against it there.
func TestLocalPath(t *testing.T) {
	for p, want := range map[string]bool{
		`C:\`: true, `c:/Users/you`: true, `D:\a\..\b`: true, `Z:\x`: true,
		`\\host\share`: false, `//host/share`: false, `/\host\share`: false, `\/host/share`: false,
		`\??\UNC\host\share`: false, `\\?\C:\x`: false, `\\.\C:\x`: false, `\\?\UNC\host\share`: false,
		`C:x`: false, `C:`: false, `\x`: false, `/x`: false, "": false,
	} {
		if got := localPath("windows", p); got != want {
			t.Errorf("windows %q: %v, want %v", p, got, want)
		}
		if runtime.GOOS == "windows" {
			if got := filepath.IsAbs(p) && len(filepath.VolumeName(p)) == 2; got != want {
				t.Errorf("Go on Windows reads %q as local %v, the classifier %v", p, got, want)
			}
		}
	}
	for p, want := range map[string]bool{"/Users/you": true, "/": true, "//host/share": false, `\\host\share`: false} {
		if got := localPath("linux", p); got != want {
			t.Errorf("linux %q: %v, want %v", p, got, want)
		}
	}
}

func TestListFoldersSkipsBrokenAndLoopLinks(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "real"), 0o755)
	if os.Symlink(filepath.Join(root, "gone"), filepath.Join(root, "broken")) != nil {
		t.Skip("symlinks unavailable")
	}
	os.Symlink(filepath.Join(root, "loop2"), filepath.Join(root, "loop1"))
	os.Symlink(filepath.Join(root, "loop1"), filepath.Join(root, "loop2"))
	if l := ListFolders(root, false); !reflect.DeepEqual(names(l), []string{"real"}) {
		t.Fatalf("%v", names(l))
	}
}

func TestListFoldersPermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permissions not enforced")
	}
	d := filepath.Join(t.TempDir(), "locked")
	os.Mkdir(d, 0o755)
	os.Chmod(d, 0)
	t.Cleanup(func() { os.Chmod(d, 0o755) })
	if l := ListFolders(d, false); l.Error != cantOpen {
		t.Fatalf("%+v", l)
	}
}
