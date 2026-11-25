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
	for _, p := range []string{"", `\\host\share`, "//host/share", `\\?\C:\x`} {
		if l := ListFolders(p, false); l.Error != cantOpen || len(l.Entries) != 0 {
			t.Fatalf("%q: %+v", p, l)
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
