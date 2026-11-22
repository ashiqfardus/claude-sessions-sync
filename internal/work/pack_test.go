package work

import (
	"archive/zip"
	"bytes"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
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

// PrunePack takes entries out and keeps the rest with their times; with none left it
// removes the pack, and one it cannot read stays as it is.
func TestPrunePack(t *testing.T) {
	src, dir := t.TempDir(), t.TempDir()
	pack := filepath.Join(dir, "run.cspack.zip")
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := WritePack(pack, []PackFile{
		packFile(t, src, "a.md", "a\n", old),
		packFile(t, src, "sub/s.md", "s\n", old),
		packFile(t, src, "sub/t.md", "t\n", old),
	}); err != nil {
		t.Fatal(err)
	}
	inSub := func(n string) bool { return Within(n, "sub", false) }
	if n, err := PrunePack(pack, inSub); err != nil || n != 2 {
		t.Fatalf("pruned %d: %v", n, err)
	}
	if got := packContents(t, pack); !reflect.DeepEqual(got, map[string]string{"a.md": "a\n"}) {
		t.Fatalf("contents %v", got)
	}
	if idx, _ := ReadPackIndex(pack); idx["a.md"].ModTime.Sub(old).Abs() > 2*time.Second {
		t.Fatalf("time lost: %+v", idx)
	}
	if n, err := PrunePack(pack, inSub); err != nil || n != 0 {
		t.Fatalf("pruned again %d: %v", n, err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 1 {
		t.Fatalf("left behind: %v", left)
	}

	if err := os.WriteFile(filepath.Join(dir, "bad.cspack.zip"), []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PrunePack(filepath.Join(dir, "bad.cspack.zip"), func(string) bool { return true }); err == nil {
		t.Fatal("unreadable pack pruned")
	}
	if _, err := os.Stat(filepath.Join(dir, "bad.cspack.zip")); err != nil {
		t.Fatalf("unreadable pack removed: %v", err)
	}

	if n, err := PrunePack(pack, func(string) bool { return true }); err != nil || n != 1 {
		t.Fatalf("pruned all %d: %v", n, err)
	}
	if _, err := os.Stat(pack); !os.IsNotExist(err) {
		t.Fatalf("empty pack kept: %v", err)
	}
}

// zipEntry is one entry of a test pack. A raw entry is written as it is, with its
// declared size and CRC; the others are compressed with their real ones.
type zipEntry struct {
	name, body string
	mode       fs.FileMode
	raw        bool
	size       uint64 // raw: the declared uncompressed size
	crc        uint32 // raw: the declared CRC
	at         time.Time
}

// zipAt writes a zip holding entries at path, as a hostile machine could.
func zipAt(t *testing.T, path string, entries ...zipEntry) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate, Modified: e.at}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		var w io.Writer
		var err error
		if e.raw {
			h.Method, h.UncompressedSize64, h.CompressedSize64, h.CRC32 = zip.Store, e.size, uint64(len(e.body)), e.crc
			w, err = zw.CreateRaw(h)
		} else {
			w, err = zw.CreateHeader(h)
		}
		if err == nil {
			_, err = io.WriteString(w, e.body)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// filesUnder lists every file and link below dir, "/"-separated.
func filesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return err
	}); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func packRels(files []PackFile) []string {
	var out []string
	for _, f := range files {
		out = append(out, f.Rel)
	}
	sort.Strings(out)
	return out
}

// Names that climb out, are absolute or carry a drive are refused one by one, and
// nothing lands outside tmpDir. Valid entries come out with their header times;
// skip drops an entry unread.
func TestExtractPackRefusesTraversal(t *testing.T) {
	base := t.TempDir()
	pack, tmp := filepath.Join(base, "in", "run.cspack.zip"), filepath.Join(base, "out", "tmp")
	at := time.Now().Add(-8 * 24 * time.Hour).Truncate(time.Second)
	zipAt(t, pack,
		zipEntry{name: "ok.md", body: "ok\n", at: at},
		zipEntry{name: "sub/deep.md", body: "deep\n", at: at},
		zipEntry{name: "skipped.md", body: "skipped\n", at: at},
		zipEntry{name: "../x", body: "x"},
		zipEntry{name: "/abs", body: "x"},
		zipEntry{name: `C:\x`, body: "x"},
		zipEntry{name: "a/../../b", body: "x"},
		zipEntry{name: "", body: "x"},
		zipEntry{name: "./c", body: "x"},
	)
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	var asked []string
	files, notes, err := ExtractPack(pack, tmp, func(rel string, mtime time.Time) bool {
		asked = append(asked, rel)
		return rel == "skipped.md" && mtime.Equal(at)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := packRels(files); !reflect.DeepEqual(got, []string{"ok.md", "sub/deep.md"}) {
		t.Fatalf("extracted %v (notes %q)", got, notes)
	}
	if len(notes) != 6 {
		t.Fatalf("notes %q", notes)
	}
	if !reflect.DeepEqual(asked, []string{"ok.md", "sub/deep.md", "skipped.md"}) {
		t.Fatalf("skip asked about %v", asked)
	}
	for _, f := range files {
		st, err := os.Stat(f.Path)
		if err != nil || !st.ModTime().Equal(at) || !f.ModTime.Equal(at) || st.Size() != f.Size {
			t.Fatalf("%s: %v %v, want time %v", f.Rel, st, err, at)
		}
		if rel, err := filepath.Rel(tmp, f.Path); err != nil || filepath.ToSlash(rel) != f.Rel {
			t.Fatalf("%s written at %s", f.Rel, f.Path)
		}
	}
	if got := filesUnder(t, base); !reflect.DeepEqual(got, []string{"in/run.cspack.zip", "out/tmp/ok.md", "out/tmp/sub/deep.md"}) {
		t.Fatalf("wrote %v", got)
	}
}

func TestExtractPackRefusesSymlinkAndDirEntries(t *testing.T) {
	dir := t.TempDir()
	pack, tmp := filepath.Join(dir, "run.cspack.zip"), filepath.Join(dir, "tmp")
	zipAt(t, pack,
		zipEntry{name: "ok.md", body: "ok\n"},
		zipEntry{name: "link", body: "/etc/passwd", mode: fs.ModeSymlink | 0o777},
		zipEntry{name: "dir/"},
		zipEntry{name: "d2", mode: fs.ModeDir | 0o755},
		zipEntry{name: "fifo", mode: fs.ModeNamedPipe | 0o644},
	)
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	files, notes, err := ExtractPack(pack, tmp, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := packRels(files); !reflect.DeepEqual(got, []string{"ok.md"}) || len(notes) != 4 {
		t.Fatalf("extracted %v, notes %q", got, notes)
	}
	if got := filesUnder(t, tmp); !reflect.DeepEqual(got, []string{"ok.md"}) {
		t.Fatalf("wrote %v", got)
	}
	if ents, _ := os.ReadDir(tmp); len(ents) != 1 {
		t.Fatalf("tmp holds %v", ents)
	}
}

func TestExtractPackLimits(t *testing.T) {
	dir := t.TempDir()
	extract := func(name string, entries ...zipEntry) ([]PackFile, []string, error, string) {
		t.Helper()
		pack, tmp := filepath.Join(dir, name+".cspack.zip"), filepath.Join(dir, name)
		zipAt(t, pack, entries...)
		if err := os.Mkdir(tmp, 0o700); err != nil {
			t.Fatal(err)
		}
		files, notes, err := ExtractPack(pack, tmp, nil)
		return files, notes, err, tmp
	}

	// One entry declares 1 KB and holds 11 MB; another declares over 10 MB.
	bomb := strings.Repeat("b", 11<<20)
	files, notes, err, tmp := extract("bomb",
		zipEntry{name: "ok.md", body: "ok\n"},
		zipEntry{name: "bomb.md", body: bomb, raw: true, size: 1 << 10, crc: crc32.ChecksumIEEE([]byte(bomb))},
		zipEntry{name: "declared.md", raw: true, size: MaxFileBytes + 1},
	)
	if err != nil || !reflect.DeepEqual(packRels(files), []string{"ok.md"}) || len(notes) != 2 {
		t.Fatalf("extracted %v, notes %q: %v", packRels(files), notes, err)
	}
	if got := filesUnder(t, tmp); !reflect.DeepEqual(got, []string{"ok.md"}) {
		t.Fatalf("wrote %v", got)
	}

	// A CRC that does not match refuses that entry alone.
	files, notes, err, tmp = extract("crc",
		zipEntry{name: "ok.md", body: "ok\n"},
		zipEntry{name: "bad.md", body: "hello\n", raw: true, size: 6, crc: crc32.ChecksumIEEE([]byte("hello\n")) + 1},
	)
	if err != nil || !reflect.DeepEqual(packRels(files), []string{"ok.md"}) || len(notes) != 1 || !strings.Contains(notes[0], "bad.md") {
		t.Fatalf("extracted %v, notes %q: %v", packRels(files), notes, err)
	}
	if got := filesUnder(t, tmp); !reflect.DeepEqual(got, []string{"ok.md"}) {
		t.Fatalf("wrote %v", got)
	}

	// Past 20,000 entries, or past 250 MB declared in all, the whole pack is refused
	// before anything is written.
	many := []zipEntry{{name: "first.md", body: "x"}}
	for i := 0; i < PackMaxEntries; i++ {
		many = append(many, zipEntry{name: fmt.Sprintf("e%05d.md", i)})
	}
	if files, _, err, tmp = extract("many", many...); err == nil || len(files) != 0 {
		t.Fatalf("%d entries extracted: %v", len(files), err)
	}
	if got := filesUnder(t, tmp); len(got) != 0 {
		t.Fatalf("wrote %v", got)
	}
	large := []zipEntry{{name: "first.md", body: "x"}}
	for i := 0; i < 26; i++ {
		large = append(large, zipEntry{name: fmt.Sprintf("l%02d.md", i), raw: true, size: MaxFileBytes})
	}
	if files, _, err, tmp = extract("large", large...); err == nil || len(files) != 0 {
		t.Fatalf("260 MB extracted: %v", err)
	}
	if got := filesUnder(t, tmp); len(got) != 0 {
		t.Fatalf("wrote %v", got)
	}
}

// A truncated pack, or no zip at all, is refused whole with nothing written.
func TestExtractPackCorrupt(t *testing.T) {
	src, dir := t.TempDir(), t.TempDir()
	pack, tmp := filepath.Join(dir, "run.cspack.zip"), filepath.Join(dir, "tmp")
	at := time.Now().Add(-8 * 24 * time.Hour)
	var in []PackFile
	for i := 0; i < 6; i++ {
		in = append(in, packFile(t, src, fmt.Sprintf("f%d.md", i), strings.Repeat("x", 100), at))
	}
	if err := WritePack(pack, in); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(pack)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(pack, st.Size()/2); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	if files, _, err := ExtractPack(pack, tmp, nil); err == nil || len(files) != 0 {
		t.Fatalf("truncated pack extracted %v: %v", packRels(files), err)
	}
	if err := os.WriteFile(pack, []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ExtractPack(pack, tmp, nil); err == nil {
		t.Fatal("not a zip, extracted")
	}
	if got := filesUnder(t, tmp); len(got) != 0 {
		t.Fatalf("wrote %v", got)
	}
}

// With no tmpDir, ExtractPack checks the pack and lists what it would extract, for a
// dry run, writing nothing.
func TestExtractPackDryRun(t *testing.T) {
	dir := t.TempDir()
	pack := filepath.Join(dir, "run.cspack.zip")
	zipAt(t, pack, zipEntry{name: "ok.md", body: "ok\n"}, zipEntry{name: "../x", body: "x"})
	files, notes, err := ExtractPack(pack, "", nil)
	if err != nil || !reflect.DeepEqual(packRels(files), []string{"ok.md"}) || files[0].Path != "" || len(notes) != 1 {
		t.Fatalf("listed %+v, notes %q: %v", files, notes, err)
	}
	if got := filesUnder(t, dir); !reflect.DeepEqual(got, []string{"run.cspack.zip"}) {
		t.Fatalf("wrote %v", got)
	}
}
