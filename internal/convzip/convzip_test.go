package convzip

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
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
	before, _ := os.ReadFile(filepath.Join(dir, testID+".jsonl"))
	defer func(old func(string) (map[string]work.PackEntry, error)) { readBack = old }(readBack)
	readBack = func(string) (map[string]work.PackEntry, error) { return nil, errors.New("drive offline") }
	if _, err := Zip(dir, testID); err == nil {
		t.Fatal("want error")
	}
	after, _ := os.ReadFile(filepath.Join(dir, testID+".jsonl"))
	if !bytes.Equal(before, after) || !exists(filepath.Join(dir, testID, "subagents", "agent-a.jsonl")) {
		t.Fatal("loose changed")
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
				n = work.MaxFileBytes + 1
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
