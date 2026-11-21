package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

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
