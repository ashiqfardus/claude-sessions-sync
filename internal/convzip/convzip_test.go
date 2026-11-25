package convzip

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/work"
)

const testID = "0b8e1c52-4f3a-4d27-9a61-7c2d5e8f9a10"

func turn(n int) string {
	return `{"type":"user","uuid":"u-` + strings.Repeat("x", n) + `","message":{"role":"user","content":"` + strings.Repeat("hello ", 50) + `"}}` + "\n"
}

func put(t *testing.T, path, body string, mt time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mt, mt); err != nil {
		t.Fatal(err)
	}
}

// bucket makes a conversation with a 3-turn transcript, a subagent and a tool result.
func bucket(t *testing.T) (dir string, mt time.Time) {
	t.Helper()
	dir = t.TempDir()
	mt = time.Now().Add(-40 * 24 * time.Hour).Truncate(time.Second)
	put(t, filepath.Join(dir, testID+".jsonl"), turn(1)+turn(2)+turn(3), mt)
	put(t, filepath.Join(dir, testID, "subagents", "agent-a.jsonl"), turn(4), mt)
	put(t, filepath.Join(dir, testID, "tool-results", "x.txt"), strings.Repeat("out\n", 100), mt)
	return dir, mt
}

func near(a, b time.Time) bool { d := a.Sub(b); return d > -2*time.Second && d < 2*time.Second }

func TestZipRoundTrip(t *testing.T) {
	dir, mt := bucket(t)
	orig := filepath.Join(t.TempDir(), "orig.jsonl")
	b, _ := os.ReadFile(filepath.Join(dir, testID+".jsonl"))
	os.WriteFile(orig, b, 0o644)

	res, err := Zip(dir, testID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 3 || res.Packed >= res.Raw {
		t.Fatalf("result %+v", res)
	}
	if exists(filepath.Join(dir, testID+".jsonl")) || exists(filepath.Join(dir, testID)) {
		t.Fatal("loose files remain")
	}
	if Find(dir)[testID] != Path(dir, testID) {
		t.Fatalf("Find = %v", Find(dir))
	}
	got, cleanup, err := Materialize(dir, testID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !claude.HoldsAll(got, orig) {
		t.Fatal("transcript lost turns")
	}
	x := filepath.Join(filepath.Dir(got), testID, "tool-results", "x.txt")
	xb, err := os.ReadFile(x)
	if err != nil || string(xb) != strings.Repeat("out\n", 100) {
		t.Fatalf("x.txt: %v", err)
	}
	if st, _ := os.Stat(x); !near(st.ModTime(), mt) {
		t.Fatalf("mtime %v want %v", st.ModTime(), mt)
	}
}

func TestZipRefusesOverCap(t *testing.T) {
	dir, _ := bucket(t)
	defer func(old int64) { maxBytes = old }(maxBytes)
	maxBytes = 100
	if _, err := Zip(dir, testID); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
	if !exists(filepath.Join(dir, testID+".jsonl")) || !exists(filepath.Join(dir, testID, "tool-results", "x.txt")) || exists(Path(dir, testID)) {
		t.Fatal("touched")
	}
}

func TestZipReadErrorKeepsLoose(t *testing.T) {
	dir, _ := bucket(t)
	before := snapshot(t, dir)
	defer func(old func(string) (map[string]work.PackEntry, error)) { readBack = old }(readBack)
	readBack = func(string) (map[string]work.PackEntry, error) { return nil, errors.New("drive offline") }
	if _, err := Zip(dir, testID); err == nil {
		t.Fatal("want error")
	}
	if after := snapshot(t, dir); len(after) != 3 || !reflect.DeepEqual(before, after) {
		t.Fatalf("loose changed: %d files", len(after))
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".zip") || strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("left %s", e.Name())
		}
	}
}

func TestZipAndLooseBothPresentLooseWins(t *testing.T) {
	dir, mt := bucket(t)
	if _, err := Zip(dir, testID); err != nil {
		t.Fatal(err)
	}
	longer := turn(1) + turn(2) + turn(3) + turn(9)
	put(t, filepath.Join(dir, testID+".jsonl"), longer, time.Now())

	got, cleanup, err := Materialize(dir, testID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if b, _ := os.ReadFile(got); string(b) != longer {
		t.Fatal("loose transcript did not win")
	}
	if !exists(filepath.Join(filepath.Dir(got), testID, "tool-results", "x.txt")) {
		t.Fatal("x.txt from the zip missing")
	}

	if err := Unzip(dir, testID); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, testID+".jsonl")); string(b) != longer {
		t.Fatal("unzip overwrote the longer transcript")
	}
	st, err := os.Stat(filepath.Join(dir, testID, "tool-results", "x.txt"))
	if err != nil || !near(st.ModTime(), mt) {
		t.Fatalf("x.txt not restored: %v", err)
	}
	if exists(Path(dir, testID)) {
		t.Fatal("zip remains")
	}
}

func TestFindIgnoresConflictCopies(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{testID + ".conv (1).zip", testID + " (conflicted copy).conv.zip", "notanid.conv.zip"} {
		put(t, filepath.Join(dir, n), "x", time.Now())
	}
	os.Mkdir(filepath.Join(dir, testID+Suffix), 0o755)
	if got := Find(dir); len(got) != 0 {
		t.Fatalf("Find = %v", got)
	}
}

func TestUnzipRestoresMTimes(t *testing.T) {
	dir, mt := bucket(t)
	if _, err := Zip(dir, testID); err != nil {
		t.Fatal(err)
	}
	if err := Unzip(dir, testID); err != nil {
		t.Fatal(err)
	}
	for rel, size := range map[string]int64{
		testID + ".jsonl": int64(len(turn(1) + turn(2) + turn(3))),
		filepath.Join(testID, "subagents", "agent-a.jsonl"): int64(len(turn(4))),
		filepath.Join(testID, "tool-results", "x.txt"):      400,
	} {
		st, err := os.Stat(filepath.Join(dir, rel))
		if err != nil || st.Size() != size || !near(st.ModTime(), mt) {
			t.Fatalf("%s: %v %v", rel, st, err)
		}
	}
	if exists(Path(dir, testID)) {
		t.Fatal("zip remains")
	}
}

func TestMaterializeRejectsCraftedZip(t *testing.T) {
	for name, entry := range map[string]string{"traversal": "../evil", "over cap": testID + "/big.bin"} {
		t.Run(name, func(t *testing.T) {
			dir, tmp := t.TempDir(), t.TempDir()
			var buf bytes.Buffer
			zw := zip.NewWriter(&buf)
			w, _ := zw.Create(entry)
			n := 10
			if name == "over cap" {
				defer func(old int64) { maxBytes = old }(maxBytes)
				maxBytes = 1 << 20
				n = 2 << 20
			}
			w.Write(make([]byte, n))
			zw.Close()
			os.WriteFile(Path(dir, testID), buf.Bytes(), 0o644)
			if _, _, err := Materialize(dir, testID, tmp); err == nil {
				t.Fatal("want error")
			}
			if exists(filepath.Join(tmp, "..", "evil")) || exists(filepath.Join(filepath.Dir(tmp), "evil")) {
				t.Fatal("wrote outside tmp")
			}
			if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
				t.Fatalf("left %d in tmp", len(ents))
			}
			if err := Unzip(dir, testID); err == nil || !exists(Path(dir, testID)) {
				t.Fatalf("Unzip must refuse and keep the zip: %v", err)
			}
		})
	}
}

// snapshot maps every file under dir to its bytes.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			b, _ := os.ReadFile(p)
			m[p] = string(b)
		}
		return nil
	})
	return m
}

func TestZipTakesFilesOverTheWorkPackCap(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 0; b.Len() <= work.MaxFileBytes; i++ {
		fmt.Fprintf(&b, `{"type":"user","uuid":"u%d","message":{"content":"%s"}}`+"\n", i, strings.Repeat("z", 1000))
	}
	put(t, filepath.Join(dir, testID+".jsonl"), b.String(), time.Now().Add(-40*24*time.Hour))
	if _, err := Zip(dir, testID); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(dir, testID+".jsonl")) {
		t.Fatal("not zipped")
	}
	got, cleanup, err := Materialize(dir, testID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if st, _ := os.Stat(got); st.Size() <= work.MaxFileBytes {
		t.Fatal("short")
	}
}

func TestZipRefusesSingleFileOverCap(t *testing.T) {
	dir, _ := bucket(t)
	defer func(old int64) { maxBytes = old }(maxBytes)
	maxBytes = 1000
	put(t, filepath.Join(dir, testID, "tool-results", "x.txt"), strings.Repeat("a", 1001), time.Now())
	if _, err := Zip(dir, testID); !errors.Is(err, ErrTooLarge) || exists(Path(dir, testID)) {
		t.Fatalf("err = %v", err)
	}
}

// A conversation whose transcript is gone has nothing to show or resume: it is not
// zipped, and its folder is left as it is.
func TestZipRefusesWithoutTranscript(t *testing.T) {
	dir, _ := bucket(t)
	os.Remove(filepath.Join(dir, testID+".jsonl"))
	if _, err := Zip(dir, testID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
	if exists(Path(dir, testID)) || !exists(filepath.Join(dir, testID, "tool-results", "x.txt")) || !exists(filepath.Join(dir, testID, "subagents", "agent-a.jsonl")) {
		t.Fatal("touched")
	}
}

func TestZipRefusesWhenZipExists(t *testing.T) {
	dir, _ := bucket(t)
	os.WriteFile(Path(dir, testID), []byte("x"), 0o644)
	if _, err := Zip(dir, testID); !errors.Is(err, ErrZipExists) {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(Path(dir, testID)); string(b) != "x" || !exists(filepath.Join(dir, testID+".jsonl")) {
		t.Fatal("touched")
	}
}

func TestZipKeepsFileChangedWhileZipping(t *testing.T) {
	dir, _ := bucket(t)
	x := filepath.Join(dir, testID, "tool-results", "x.txt")
	defer func(old func(string) (map[string]work.PackEntry, error)) { readBack = old }(readBack)
	readBack = func(p string) (map[string]work.PackEntry, error) {
		put(t, x, "replaced by a sync client", time.Now())
		return work.ReadPackIndex(p)
	}
	if _, err := Zip(dir, testID); err == nil {
		t.Fatal("want error")
	}
	if b, _ := os.ReadFile(x); string(b) != "replaced by a sync client" {
		t.Fatal("lost the new file")
	}
	if exists(Path(dir, testID)) {
		t.Fatal("zip left")
	}
}

func TestZipLeavesFilesItDidNotPack(t *testing.T) {
	dir, _ := bucket(t)
	late := filepath.Join(dir, testID, "tool-results", "late.txt")
	defer func(old func(string) (map[string]work.PackEntry, error)) { readBack = old }(readBack)
	readBack = func(p string) (map[string]work.PackEntry, error) {
		put(t, late, "arrived mid-zip", time.Now())
		return work.ReadPackIndex(p)
	}
	Zip(dir, testID)
	if b, _ := os.ReadFile(late); string(b) != "arrived mid-zip" {
		t.Fatal("deleted a file that was never packed")
	}
}

func TestUnzipDivergedChangesNothing(t *testing.T) {
	dir, _ := bucket(t)
	if _, err := Zip(dir, testID); err != nil {
		t.Fatal(err)
	}
	forked := turn(1) + turn(2) + turn(9)
	put(t, filepath.Join(dir, testID+".jsonl"), forked, time.Now())
	if err := Unzip(dir, testID); !errors.Is(err, ErrDiverged) {
		t.Fatalf("err = %v", err)
	}
	if !exists(Path(dir, testID)) || exists(filepath.Join(dir, testID, "tool-results", "x.txt")) {
		t.Fatal("changed something")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, testID+".jsonl")); string(b) != forked {
		t.Fatal("loose changed")
	}
	got, cleanup, err := Materialize(dir, testID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if b, _ := os.ReadFile(got); string(b) != forked {
		t.Fatal("diverged: loose should be read")
	}
}

func TestUnzipReplacesShorterLoose(t *testing.T) {
	dir, _ := bucket(t)
	if _, err := Zip(dir, testID); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(dir, testID+".jsonl"), turn(1), time.Now())
	if err := Unzip(dir, testID); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, testID+".jsonl")); string(b) != turn(1)+turn(2)+turn(3) {
		t.Fatal("zipped transcript did not replace the shorter loose one")
	}
}

func TestUnzipRefusesForeignEntryAndKeepsZip(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range []string{testID + ".jsonl", "other/file"} {
		w, _ := zw.Create(n)
		w.Write([]byte(turn(1)))
	}
	zw.Close()
	os.WriteFile(Path(dir, testID), buf.Bytes(), 0o644)
	if err := Unzip(dir, testID); err == nil || !exists(Path(dir, testID)) || exists(filepath.Join(dir, testID+".jsonl")) {
		t.Fatalf("err = %v", err)
	}
}

func TestUnzipLeavesNoTempInBucketAndFolderDoesNotBreakLoose(t *testing.T) {
	dir, _ := bucket(t)
	if _, err := Zip(dir, testID); err != nil {
		t.Fatal(err)
	}
	if err := Unzip(dir, testID); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), "tmp") {
			t.Fatalf("left %s", e.Name())
		}
	}
	// A folder named like the zip is not a zip: the loose copy is read as it is.
	os.Mkdir(Path(dir, testID), 0o755)
	got, cleanup, err := Materialize(dir, testID, t.TempDir())
	if err != nil || got != filepath.Join(dir, testID+".jsonl") {
		t.Fatalf("%v %v", got, err)
	}
	cleanup()
}

func TestUnzipUpperCaseIDWithLowerCaseKey(t *testing.T) {
	dir, _ := bucket(t)
	up := strings.ToUpper(testID)
	// An upper-case id on disk: Find keys it lower-case.
	os.Rename(filepath.Join(dir, testID+".jsonl"), filepath.Join(dir, up+".jsonl"))
	os.Rename(filepath.Join(dir, testID), filepath.Join(dir, up))
	if _, err := Zip(dir, up); err != nil {
		t.Fatal(err)
	}
	if Find(dir)[testID] == "" {
		t.Fatal("not found by lower-case key")
	}
	if err := Unzip(dir, up); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(dir, up+".jsonl")) || exists(Path(dir, up)) {
		t.Fatal("not restored")
	}
}

// symlink plants a link, or skips where links need rights the runner lacks.
func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
}

// A link planted at <id> or under it in the shared archive must not take a write
// outside the bucket: Unzip refuses before writing anything, and the zip stays.
func TestUnzipRefusesALinkedFolder(t *testing.T) {
	for name, at := range map[string]string{"id": testID, "subfolder": filepath.Join(testID, "subagents")} {
		t.Run(name, func(t *testing.T) {
			dir, _ := bucket(t)
			if _, err := Zip(dir, testID); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			os.MkdirAll(filepath.Dir(filepath.Join(dir, at)), 0o755)
			symlink(t, outside, filepath.Join(dir, at))
			if err := Unzip(dir, testID); err == nil {
				t.Fatal("unzipped through a link")
			}
			if ents, _ := os.ReadDir(outside); len(ents) != 0 {
				t.Fatalf("wrote %d file(s) through the link", len(ents))
			}
			if !exists(Path(dir, testID)) || exists(filepath.Join(dir, testID+".jsonl")) {
				t.Fatal("the zip went, or the transcript was written before the refusal")
			}
		})
	}
}

// A conversation whose folder is a link is not zipped: its zip could never be unzipped.
func TestZipRefusesALinkedFolder(t *testing.T) {
	dir := t.TempDir()
	put(t, filepath.Join(dir, testID+".jsonl"), turn(1), time.Now())
	outside := t.TempDir()
	put(t, filepath.Join(outside, "x.txt"), "not ours", time.Now())
	symlink(t, outside, filepath.Join(dir, testID))
	if _, err := Zip(dir, testID); err == nil {
		t.Fatal("zipped a conversation whose folder is a link")
	}
	if exists(Path(dir, testID)) || !exists(filepath.Join(dir, testID+".jsonl")) || !exists(filepath.Join(outside, "x.txt")) {
		t.Fatal("touched")
	}
}

// Only ASCII case is folded: Unicode folding takes <id>.jſonl for <id>.jsonl, and
// Unzip then wrote it and deleted the zip.
func TestUnzipFoldsASCIICaseOnly(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create(testID + ".jſonl")
	w.Write([]byte(turn(1)))
	zw.Close()
	os.WriteFile(Path(dir, testID), buf.Bytes(), 0o644)
	if err := Unzip(dir, testID); err == nil || !exists(Path(dir, testID)) || exists(filepath.Join(dir, testID+".jſonl")) {
		t.Fatalf("err = %v", err)
	}
	if !mine(strings.ToUpper(testID), testID+".jsonl") || !mine(testID, strings.ToUpper(testID)+"/subagents/a.jsonl") {
		t.Fatal("ASCII case is no longer folded")
	}
}
