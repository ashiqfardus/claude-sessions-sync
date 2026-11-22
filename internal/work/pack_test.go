package work

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// packFile writes content at dir/rel with mtime at and returns it as a PackFile.
func packFile(t *testing.T, dir, rel, content string, at time.Time) PackFile {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
	return PackFile{Rel: rel, Path: p, Size: int64(len(content)), ModTime: at}
}

// packContents reads every entry of the pack at path.
func packContents(t *testing.T, path string) map[string]string {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	out := map[string]string{}
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = string(b)
	}
	return out
}

func TestWritePackRoundTrip(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "run.cspack.zip")
	base := time.Now().Add(-8 * 24 * time.Hour)
	files := []PackFile{
		packFile(t, src, "r.md", "report\n", base),
		packFile(t, src, "deep/q.sql", "select 1;\n", base.Add(-time.Hour)),
		packFile(t, src, "a.csv", "a,b\n1,2\n", base.Add(-48*time.Hour)),
	}
	if err := WritePack(dst, files); err != nil {
		t.Fatal(err)
	}
	idx, err := ReadPackIndex(dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx) != 3 {
		t.Fatalf("index: %+v", idx)
	}
	for _, f := range files {
		e, ok := idx[f.Rel]
		if d := e.ModTime.Sub(f.ModTime).Abs(); !ok || e.Size != f.Size || d > 2*time.Second {
			t.Errorf("%s: %+v, want size %d mtime %v", f.Rel, e, f.Size, f.ModTime)
		}
	}
	want := map[string]string{"r.md": "report\n", "deep/q.sql": "select 1;\n", "a.csv": "a,b\n1,2\n"}
	if got := packContents(t, dst); !reflect.DeepEqual(got, want) {
		t.Fatalf("contents %v", got)
	}
}

// A file that cannot be read leaves no pack and no temp file, and a pack already
// there stays as it was: an interrupted compaction keeps the old pack valid.
func TestWritePackAtomic(t *testing.T) {
	src, dir := t.TempDir(), t.TempDir()
	dst := filepath.Join(dir, "run.cspack.zip")
	at := time.Now().Add(-8 * 24 * time.Hour)
	good := packFile(t, src, "a.md", "a\n", at)
	bad := []PackFile{good, {Rel: "gone.md", Path: filepath.Join(src, "gone.md"), Size: 3, ModTime: at}}

	if err := WritePack(dst, bad); err == nil {
		t.Fatal("pack written with a missing file")
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}

	if err := WritePack(dst, []PackFile{good}); err != nil {
		t.Fatal(err)
	}
	if err := WritePack(dst, bad); err == nil {
		t.Fatal("pack rewritten with a missing file")
	}
	if err := CompactPack(dst, dst, bad[1:]); err == nil {
		t.Fatal("pack compacted with a missing file")
	}
	if left, _ := os.ReadDir(dir); len(left) != 1 {
		t.Fatalf("left behind: %v", left)
	}
	if got := packContents(t, dst); !reflect.DeepEqual(got, map[string]string{"a.md": "a\n"}) {
		t.Fatalf("old pack changed: %v", got)
	}
}

// A file that grew after it was listed is packed as listed: never past the size
// Collect allowed, and with the listed time, so the next push sees it changed.
func TestWritePackKeepsListedSize(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "run.cspack.zip")
	f := packFile(t, src, "a.md", "abc", time.Now())
	f.Size = 2
	if err := WritePack(dst, []PackFile{f}); err != nil {
		t.Fatal(err)
	}
	if got := packContents(t, dst); got["a.md"] != "ab" {
		t.Fatalf("contents %q", got)
	}
}

func TestCompactPackOverlayWins(t *testing.T) {
	src, dir := t.TempDir(), t.TempDir()
	pack := filepath.Join(dir, "run.cspack.zip")
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := WritePack(pack, []PackFile{
		packFile(t, src, "a.md", "old a\n", old),
		packFile(t, src, "sub/b.md", "old b\n", old),
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	add := []PackFile{
		packFile(t, src, "a.md", "new a, longer\n", now),
		packFile(t, src, "c.md", "c\n", now),
	}
	if err := CompactPack(pack, pack, add); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"a.md": "new a, longer\n", "sub/b.md": "old b\n", "c.md": "c\n"}
	if got := packContents(t, pack); !reflect.DeepEqual(got, want) {
		t.Fatalf("contents %v, want %v", got, want)
	}
	idx, err := ReadPackIndex(pack)
	if err != nil {
		t.Fatal(err)
	}
	if e := idx["a.md"]; e.Size != 14 || e.ModTime.Sub(now).Abs() > 2*time.Second {
		t.Fatalf("a.md: %+v", e)
	}
	if e := idx["sub/b.md"]; e.ModTime.Sub(old).Abs() > 2*time.Second {
		t.Fatalf("b.md lost its time: %+v", e)
	}
	if left, _ := os.ReadDir(dir); len(left) != 1 {
		t.Fatalf("left behind: %v", left)
	}
}
