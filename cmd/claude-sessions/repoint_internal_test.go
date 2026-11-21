package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/rewrite"
)

// noTemps fails when dir holds a temp file a rewrite left.
func noTemps(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp") {
			t.Fatalf("temp file left: %s", e.Name())
		}
	}
}

// A session that goes on between the choice and the rename keeps its new line: the
// original is left as it is, and no temp file stays.
func TestRepointFileChangedMeanwhile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(p, []byte(`{"p":"/old/x"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"p":"/old/y"}` + "\n")
	f.Close()
	want, _ := os.ReadFile(p)

	err = repointFile(p, dir, filepath.Join(dir, "backup", "s.jsonl"), rewrite.RulesForMoves([]rewrite.Move{{From: "/old", To: "/new"}}), st)
	if !errors.Is(err, errLive) {
		t.Fatalf("err = %v, want errLive", err)
	}
	if got, _ := os.ReadFile(p); string(got) != string(want) {
		t.Fatalf("original changed: %q", got)
	}
	noTemps(t, dir)
}

// A rewrite that would break a line's JSON is not kept: the original stays, with no
// temp file and no backup.
func TestRepointFileInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	want := `{"p":"/old/x"}` + "\n"
	if err := os.WriteFile(p, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Lstat(p)
	backup := filepath.Join(dir, "backup", "s.jsonl")
	if err := repointFile(p, dir, backup, rewrite.RulesForMoves([]rewrite.Move{{From: "/old", To: `/n"ew`}}), st); err == nil {
		t.Fatal("no error")
	}
	if got, _ := os.ReadFile(p); string(got) != want {
		t.Fatalf("original changed: %q", got)
	}
	noTemps(t, dir)
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("backup written: %v", err)
	}
}

// A folder placed inside, or around, where it was before gets no move: rules apply in
// sequence, so old -> old/sub would rewrite old/sub/x to old/sub/sub/x. It is named
// instead, to repoint by hand.
func TestRemapMovesNested(t *testing.T) {
	moves, notes := remapMoves(
		map[string][]string{"WIN|/w/a": {"/u/b/w", "/u/b/x"}, "WIN|/w/c": {"/u/b/c/d"}, "WIN|/w/gone": {"/u/b/g"}},
		map[string]string{"WIN|/w/a": "/u/b/w/sub", "WIN|/w/c": "/u/b/c"})
	if want := []rewrite.Move{{From: "/u/b/x", To: "/u/b/w/sub"}}; !reflect.DeepEqual(moves, want) {
		t.Fatalf("moves %+v, want %+v", moves, want)
	}
	if len(notes) != 2 || !strings.Contains(notes[0], "/u/b/w") || !strings.Contains(notes[1], "/u/b/c/d") || !strings.Contains(notes[0], "by hand") {
		t.Fatalf("notes %q", notes)
	}
}

// A filed transcript --repoint rewrites longer is filed in whole: its old size would
// leave archive bytes past the mark, where push reads folders.
func TestRepointMovesFiledMark(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "projects", "-b", "11111111-1111-1111-1111-111111111111.jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"cwd":"/old/x"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(p, at, at)
	imported := loadImported(root, false) // a first run: marks p
	before := imported.offset(p)
	res := repointLocal(root, []rewrite.Move{{From: "/old", To: "/a/much/longer/new"}}, time.Now(), false, imported)
	st, _ := os.Stat(p)
	if res.Rewritten != 1 || imported.offset(p) != st.Size() || st.Size() <= before {
		t.Fatalf("rewritten %d, mark %d, size %d (was %d)", res.Rewritten, imported.offset(p), st.Size(), before)
	}
}

// Two machines placed at one target: the origin is the same on every run (M4).
func TestOriginOfTieBreak(t *testing.T) {
	placed := map[string]string{`WIN|E:\W`: "/h/W", "LNX|/w": "/h/W", "BOX|/b": "/h/W"}
	for i := 0; i < 50; i++ {
		if o := originOf("/h/W/sub", placed, "MAC", false); o == nil || o.Machine != "BOX" || o.Path != "/b/sub" {
			t.Fatalf("run %d: %+v", i, o)
		}
	}
}

// A mark only grows: a transcript noted again smaller - or another whose key is the
// same - never lowers it (R25).
func TestRecordMarkOnlyGrows(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "projects", "-b", "11111111-1111-1111-1111-111111111111.jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Repeat("x", 100)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := loadImported(root, false)
	r.note(p)
	if err := os.WriteFile(p, []byte("y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.note(p)
	if got := r.offset(p); got != 101 {
		t.Fatalf("mark %d, want 101", got)
	}
}

// Each transcript noted is on disk at once: a run killed after it leaves nothing it
// filed unrecorded (R26).
func TestRecordSavedOnEachNote(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "projects", "-b", "11111111-1111-1111-1111-111111111111.jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	r := loadImported(root, true)
	if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.note(p)
	raw, _ := os.ReadFile(importedPath(root))
	if !strings.Contains(string(raw), `"-b/11111111-1111-1111-1111-111111111111.jsonl": 2`) {
		t.Fatalf("not saved: %s", raw)
	}
}
