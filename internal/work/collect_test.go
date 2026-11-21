package work

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func put(t *testing.T, root, rel string, size int64) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if size > 0 {
		if err := f.Truncate(size); err != nil {
			t.Fatal(err)
		}
	}
}

func rels(t *testing.T, root string, c Collected) []string {
	t.Helper()
	var out []string
	for _, f := range c.Files {
		r, err := filepath.Rel(root, f)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, filepath.ToSlash(r))
	}
	sort.Strings(out)
	return out
}

func TestCollectFilters(t *testing.T) {
	root := t.TempDir()
	for _, r := range []string{"r.md", "q.sql", "m.csv", "n.ipynb", "shot.png", "app.apk", ".env",
		"prod.key", "db-credentials.json", "node_modules/x.js", ".next/a.js", "inner/.git/HEAD",
		"inner/code.go", "sub/deep.md"} {
		put(t, root, r, 0)
	}
	put(t, root, "big.csv", 10<<20+1)
	c, err := Collect(Folder{Path: root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"m.csv", "n.ipynb", "q.sql", "r.md", "sub/deep.md"}
	if got := rels(t, root, c); !reflect.DeepEqual(got, want) {
		t.Fatalf("files %v want %v", got, want)
	}
	if !sort.StringsAreSorted(c.Files) {
		t.Fatalf("not sorted: %v", c.Files)
	}
	if ws := map[string]int{"type": 2, "large": 1, "dot": 1, "secret": 2}; !reflect.DeepEqual(c.Skipped, ws) {
		t.Fatalf("skipped %v want %v", c.Skipped, ws)
	}
}

func TestCollectSymlinks(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	put(t, out, "leak.md", 0)
	put(t, root, "ok.md", 0)
	if err := os.Symlink(out, filepath.Join(root, "link")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if err := os.Symlink(filepath.Join(out, "leak.md"), filepath.Join(root, "l.md")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	c, err := Collect(Folder{Path: root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := rels(t, root, c); !reflect.DeepEqual(got, []string{"ok.md"}) {
		t.Fatalf("got %v", got)
	}
}

func TestCollectExact(t *testing.T) {
	root := t.TempDir()
	put(t, root, "a.md", 0)
	put(t, root, "b.md", 0)
	c, err := Collect(Folder{Path: filepath.Join(root, "a.md"), Exact: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := rels(t, root, c); !reflect.DeepEqual(got, []string{"a.md"}) {
		t.Fatalf("got %v", got)
	}
	put(t, root, "x.png", 0)
	c, err = Collect(Folder{Path: filepath.Join(root, "x.png"), Exact: true}, nil)
	if err != nil || len(c.Files) != 0 || c.Skipped["type"] != 1 {
		t.Fatalf("denied exact: %v %v", c, err)
	}
	if _, err = Collect(Folder{Path: filepath.Join(root, "none.md"), Exact: true}, nil); err == nil {
		t.Fatal("missing exact file should error")
	}
	if _, err = Collect(Folder{Path: filepath.Join(root, "nodir")}, nil); err == nil {
		t.Fatal("missing folder should error")
	}
}

func TestCollectExclude(t *testing.T) {
	root := t.TempDir()
	put(t, root, "r.md", 0)
	put(t, root, "sub/deep.md", 0)
	c, err := Collect(Folder{Path: root}, []string{filepath.Join(root, "sub")})
	if err != nil {
		t.Fatal(err)
	}
	if got := rels(t, root, c); !reflect.DeepEqual(got, []string{"r.md"}) || c.Skipped["excluded"] != 1 {
		t.Fatalf("got %v %v", got, c.Skipped)
	}
}

func TestCollectNonASCII(t *testing.T) {
	root := t.TempDir()
	put(t, root, "রিপোর্ট.md", 0)
	put(t, root, "résumé.csv", 0)
	c, err := Collect(Folder{Path: root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"résumé.csv", "রিপোর্ট.md"}
	if got := rels(t, root, c); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestEncode(t *testing.T) {
	for in, want := range map[string]string{
		"/Users/a/SQL":     "Users/a/SQL",
		`E:\SQL\po`:        "E/SQL/po",
		"/e/SQL":           "e/SQL",
		"/Users/a/My Work": "Users/a/My Work",
	} {
		if got := Encode(in); got != want {
			t.Errorf("Encode(%q)=%q want %q", in, got, want)
		}
	}
}
