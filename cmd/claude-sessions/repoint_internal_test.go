package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
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
	imported := loadImported(root, true) // a first run: marks p
	before := imported.offset(p)
	res := repointLocal(root, []rewrite.Move{{From: "/old", To: "/a/much/longer/new"}}, time.Now(), false, imported, startProgress(io.Discard))
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

// writeTranscript writes content to the transcript at projects/-b/<id>.jsonl in root.
func writeTranscript(t *testing.T, root, id, content string) string {
	t.Helper()
	p := filepath.Join(root, "projects", "-b", id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A mark only grows: a transcript settled again smaller never lowers it (R25).
func TestRecordMarkOnlyGrows(t *testing.T) {
	root := t.TempDir()
	p := writeTranscript(t, root, "11111111-1111-1111-1111-111111111111", strings.Repeat("x", 100)+"\n")
	r := loadImported(root, true) // a first run: marks p at 101
	writeTranscript(t, root, "11111111-1111-1111-1111-111111111111", "y\n")
	if err := r.settle(p, true); err != nil {
		t.Fatal(err)
	}
	if got := r.offset(p); got != 101 {
		t.Fatalf("mark %d, want 101", got)
	}
}

// The journal after a run killed part way: a transcript pended and never settled
// counts whole, at its size now; one settled, at the size journaled, so lines added
// here since are read (R28).
func TestJournalPendingCountsWhole(t *testing.T) {
	root := t.TempDir()
	r := loadImported(root, true) // a first run, with nothing here yet
	a := filepath.Join(root, "projects", "-b", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa.jsonl")
	b := filepath.Join(root, "projects", "-b", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb.jsonl")
	if err := r.pend(a, b); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, root, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", strings.Repeat("a", 99)+"\n")
	writeTranscript(t, root, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", strings.Repeat("b", 49)+"\n")
	if err := r.settle(b, true); err != nil {
		t.Fatal(err)
	}
	// Killed here: a's size never journaled, nothing folded into imported.json.
	f, _ := os.OpenFile(b, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("continued here\n")
	f.Close()
	again := loadImported(root, false)
	if got := again.offset(a); got != 100 {
		t.Errorf("pending a: mark %d, want 100", got)
	}
	if got := again.offset(b); got != 50 {
		t.Errorf("settled b: mark %d, want 50", got)
	}
	// Folded at the next load that saves: the journal goes, the marks stay.
	loadImported(root, true)
	if _, err := os.Stat(importedLog(root)); !os.IsNotExist(err) {
		t.Errorf("journal left: %v", err)
	}
	if got := loadImported(root, false).offset(a); got != 100 {
		t.Errorf("after folding, a: mark %d, want 100", got)
	}
}

// openFiles counts this process's open files, or -1 where /dev/fd cannot be listed
// (Windows, where removing the journal below fails instead).
func openFiles() int {
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}

// Journaling leaves nothing open: runs that stop early never reach finish, and Windows
// cannot remove an open journal, at finish or at the next load.
func TestJournalNotHeldOpen(t *testing.T) {
	root := t.TempDir()
	p := writeTranscript(t, root, "11111111-1111-1111-1111-111111111111", "x\n")
	r := loadImported(root, true)
	before := openFiles()
	if err := r.pend(p); err != nil {
		t.Fatal(err)
	}
	if err := r.settle(p, true); err != nil {
		t.Fatal(err)
	}
	if after := openFiles(); after != before {
		t.Fatalf("%d files open after journaling, %d before", after, before)
	}
	if err := os.Remove(importedLog(root)); err != nil {
		t.Fatalf("journal: %v", err)
	}
}

// A lookup takes the larger of the exact key and the case-folded one, on every OS;
// a nil record reads everything (R28, N6).
func TestRecordFoldedLookup(t *testing.T) {
	root := t.TempDir()
	r := loadImported(root, false)
	r.set("-b/A.jsonl", 300)
	r.set("-b/a.jsonl", 10)
	for _, name := range []string{"A.jsonl", "a.jsonl"} {
		if got := r.offset(filepath.Join(root, "projects", "-b", name)); got != 300 {
			t.Errorf("%s: mark %d, want 300", name, got)
		}
	}
	if got := r.offset(filepath.Join(root, "projects", "-b", "x.jsonl")); got != 0 {
		t.Errorf("unrecorded: mark %d", got)
	}
	var none *importRecord
	if none.offset(filepath.Join(root, "x.jsonl")) != 0 || none.has(filepath.Join(root, "x.jsonl")) {
		t.Error("a nil record holds something")
	}
	p := writeTranscript(t, root, "11111111-1111-1111-1111-111111111111", `{"cwd":"/old/x"}`+"\n")
	at := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(p, at, at)
	if res := repointLocal(root, []rewrite.Move{{From: "/old", To: "/new"}}, time.Now(), false, nil, startProgress(io.Discard)); res.Rewritten != 1 {
		t.Errorf("repoint with no record: %+v", res)
	}
}

// Only a name with a dot or a path separator - one of this tool's own files - is
// refused: "_", non-ASCII letters and every name claude.Slug makes are filed (R32).
func TestSafeBucketName(t *testing.T) {
	for _, n := range []string{"imported.log", "work-held.json", "imported.json", "", "a/b", `a\b`, ".."} {
		if safeBucketName(n) {
			t.Errorf("%q accepted", n)
		}
	}
	for _, n := range []string{"my_app", "-Users-a-রিপোর্ট", "C--Users-a-proj"} {
		if !safeBucketName(n) {
			t.Errorf("%q refused", n)
		}
	}
	for _, p := range []string{"/Users/a/dev/api", `C:\Users\a\My Work\run 1`, `E:\SQL\po`, "/home/a/.config/x",
		"/Users/a/রিপোর্ট/résumé", "/w/" + strings.Repeat("long name. ", 40)} {
		if s := claude.Slug(p); !safeBucketName(s) {
			t.Errorf("Slug(%q) = %q refused", p, s)
		}
	}
}

// The quick pass over each line changes no answer of pointing: a line it passes over
// is one that no find matches as strings.ToLower lower-cases it. It relies on A-Z, İ
// and the Kelvin sign being the only runes that lower-case to ASCII.
func TestPointingQuickPassKeepsTheAnswer(t *testing.T) {
	for r := rune(utf8.RuneSelf); r <= unicode.MaxRune; r++ {
		if l := unicode.ToLower(r); l < utf8.RuneSelf && r != '\u0130' && r != '\u212a' {
			t.Fatalf("%U lower-cases to ASCII %q: the quick pass would miss it", r, l)
		}
	}
	moves := []rewrite.Move{{From: `C:\Users\other\SQL`, To: "/Users/me/SQL"}, {From: "/Users/other/web-app", To: "/Users/me/web-app"}}
	rules := rewrite.RulesForMoves(moves)
	finds := make([]string, len(rules))
	for i, r := range rules {
		finds[i] = strings.ToLower(r.Find)
	}
	quick := findsOf(finds)
	if quick == nil {
		t.Fatal("ASCII finds have no quick pass")
	}
	dir := t.TempDir()
	for i, line := range []string{
		`{"cwd":"C:\\Users\\other\\SQL\\x"}`,       // json-escaped
		`{"cwd":"c:\\USERS\\Other\\sql"}`,          // any case
		`{"text":"see /users/OTHER/web-app/a.go"}`, // forward slash
		`{"text":"/c/Users/other/SQL"}`,            // Git Bash
		`{"text":"C--Users-other-SQL"}`,            // its bucket's name
		"{\"text\":\"/USERS/OTHER/WEB-APP \u2014 \u00e9t\u00e9\"}",
		"{\"text\":\"/users/other/web-app\u0130\"}", // İ: matched in full
		"{\"text\":\"/users/other/web-\u212aapp\"}", // the Kelvin sign
		"{\"text\":\"/u\u017fers/other/web-app\"}",  // ſ: no find holds it, as before
		`{"cwd":"C:\\Users\\me\\SQL"}`,              // this machine's own
		`{"cwd":"/users/other/web-application"}`,    // a longer name: no whole path
		`{"text":"other web-app SQL"}`,
		"",
	} {
		p := filepath.Join(dir, fmt.Sprintf("%d.jsonl", i))
		writeFile(t, p, `{"type":"user"}`+"\n"+line+"\n"+`{"type":"assistant"}`)
		if got, want := pointing(p, rules, finds, quick), pointing(p, rules, finds, nil); got != want {
			t.Errorf("%q: pointing %v with the quick pass, %v without", line, got, want)
		}
	}
	if !pointing(filepath.Join(dir, "1.jsonl"), rules, finds, quick) || pointing(filepath.Join(dir, "9.jsonl"), rules, finds, quick) {
		t.Error("fixture: the cases do not both point and not")
	}
	if quick.may(`{"cwd":"C:\\Users\\me\\SQL","text":"other web-app"}`, new([]byte)) {
		t.Error("the quick pass matches in full a line that names no moved folder")
	}
	if findsOf([]string{"/users/\u00e9t\u00e9/x"}) != nil {
		t.Error("a find that is not ASCII has a quick pass")
	}
}
