package render

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// stateArchive is an archive of two conversations, e--app/s1 and e--app/s2, rendered
// once.
func stateArchive(t *testing.T) (dest string) {
	t.Helper()
	dest = t.TempDir()
	bucket := filepath.Join(dest, "projects", "e--app")
	if err := os.MkdirAll(bucket, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"s1", "s2"} {
		if err := os.WriteFile(filepath.Join(bucket, id+".jsonl"), []byte(`{"type":"user","message":{"role":"user","content":"hi `+id+`"}}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if res, err := Archive(dest, Options{}); err != nil || res.Rendered != 2 {
		t.Fatalf("first render: %+v, %v", res, err)
	}
	return dest
}

// A state that cannot be parsed - a sync client half way through replacing it - renders
// nothing and is left as it is. Read as empty, every page counted as stale, and the
// state saved after held this run's pages alone: every computer started over.
func TestUnparsableStateRendersNothing(t *testing.T) {
	wait := stateWait
	stateWait = time.Millisecond
	t.Cleanup(func() { stateWait = wait })
	dest := stateArchive(t)
	added := filepath.Join(dest, "projects", "e--app", "s3.jsonl")
	if err := os.WriteFile(added, []byte(`{"type":"user","message":{"role":"user","content":"new"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := statePath(filepath.Join(dest, "html"))
	garbled := []byte(`{"version":5,"pages":{"e--app/s1":{"si`)
	if err := os.WriteFile(state, garbled, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Archive(dest, Options{})
	if err == nil || !strings.Contains(err.Error(), "no page is rendered this run") || res.Rendered != 0 {
		t.Fatalf("render = %+v, %v; want nothing rendered, and why", res, err)
	}
	if got := mustRead(t, state); !bytes.Equal(got, garbled) {
		t.Errorf("the state was replaced: %s", got)
	}
	if _, err := os.Stat(filepath.Join(dest, "html", "e--app", "s3.html")); err == nil {
		t.Error("a page was rendered")
	}

	// --force renders every page again, and saves a state that can be read.
	if res, err := Archive(dest, Options{Force: true}); err != nil || res.Rendered != 3 {
		t.Fatalf("render --force = %+v, %v", res, err)
	}
	if res, err := Archive(dest, Options{}); err != nil || res.UpToDate != 3 {
		t.Errorf("after --force: %+v, %v", res, err)
	}
}

// Every computer renders into html/. One that saves its state while this run goes on
// keeps its pages in it, and this run's own entries win for the pages it rendered.
func TestTwoWritersKeepEachOthersPages(t *testing.T) {
	dest := stateArchive(t)
	htmlDir := filepath.Join(dest, "html")
	// The other computer saves after this run has read the state.
	other := renderState{Version: templateVersion, Pages: map[string]pageState{
		"e--app/s1": {Size: 999, Prompt: "stale"},
		"e--web/x":  {Size: 7, Prompt: "theirs"},
	}}
	res, err := Archive(dest, Options{Progress: func(n, of int) {
		if n == 0 {
			if err := writeState(htmlDir, other); err != nil {
				t.Error(err)
			}
		}
	}})
	if err != nil || res.UpToDate != 2 {
		t.Fatalf("render = %+v, %v", res, err)
	}
	s, err := loadState(htmlDir)
	if err != nil {
		t.Fatal(err)
	}
	if p := s.Pages["e--web/x"]; p.Prompt != "theirs" {
		t.Errorf("the other computer's page is gone: %+v", s.Pages)
	}
	for _, k := range []string{"e--app/s1", "e--app/s2"} {
		if p := s.Pages[k]; p.Size == 999 || !strings.HasPrefix(p.Prompt, "hi ") {
			t.Errorf("%s: %+v, want this run's", k, p)
		}
	}

	// An older template's state is not taken for current.
	other.Version = templateVersion - 1
	if _, err := Archive(dest, Options{Progress: func(n, of int) {
		if n == 0 {
			_ = writeState(htmlDir, other)
		}
	}}); err != nil {
		t.Fatal(err)
	}
	if s, _ := loadState(htmlDir); s.Version != templateVersion || len(s.Pages) != 2 {
		t.Errorf("an older template's pages were kept: %+v", s)
	}
}

// fastStateRetries makes loadState's retries quick.
func fastStateRetries(t *testing.T) {
	wait := stateWait
	stateWait = time.Millisecond
	t.Cleanup(func() { stateWait = wait })
}

// A state that cannot be read - a placeholder the sync client cannot fetch, a sharing
// violation - is never taken for broken, however old its mtime: nothing is rendered,
// run after run, and the other computers' entries stay. Its mtime is the writer's.
func TestUnreadableStateNeverStartsOver(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 000 does not stop reads on Windows")
	}
	fastStateRetries(t)
	dest := stateArchive(t)
	htmlDir := filepath.Join(dest, "html")
	s, err := loadState(htmlDir)
	if err != nil {
		t.Fatal(err)
	}
	s.Pages["e--web/x"] = pageState{Size: 7, Prompt: "theirs"}
	if err := writeState(htmlDir, s); err != nil {
		t.Fatal(err)
	}
	state := statePath(htmlDir)
	before := mustRead(t, state)
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(state, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(state, 0o644) })
	if _, err := os.ReadFile(state); err == nil {
		t.Skip("chmod 000 does not stop reads here (root?)")
	}
	note := filepath.Join(t.TempDir(), "render-broken")
	for run := 1; run <= 2; run++ {
		res, err := Archive(dest, Options{BrokenNote: note})
		if err == nil || !strings.Contains(err.Error(), "cannot be read") || res.Rendered != 0 {
			t.Fatalf("run %d: render = %+v, %v; want nothing rendered, and why", run, res, err)
		}
	}
	if _, err := os.Stat(note); err == nil {
		t.Error("a read error was noted as garbled bytes")
	}
	if err := os.Chmod(state, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, state); !bytes.Equal(got, before) {
		t.Errorf("the state was replaced: %s", got)
	}
}

// Garbled bytes render nothing the first time this computer finds them; found again
// on its next run, unchanged, they are broken for good and every page is rendered
// again. Bytes garbled another way are a new first sight, and a state that loads
// clears the note.
func TestGarbledTwiceRendersEveryPageAgain(t *testing.T) {
	fastStateRetries(t)
	dest := stateArchive(t)
	state := statePath(filepath.Join(dest, "html"))
	note := filepath.Join(t.TempDir(), "base", "render-broken")
	garble := func(b string) {
		t.Helper()
		if err := os.WriteFile(state, []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	nothing := func(when string) {
		t.Helper()
		res, err := Archive(dest, Options{BrokenNote: note})
		if err == nil || !strings.Contains(err.Error(), "next run") || res.Rendered != 0 || res.Note != "" {
			t.Fatalf("%s: render = %+v, %v; want nothing rendered, and why", when, res, err)
		}
	}

	garble(`{"version":5,"pa`)
	nothing("first sight")
	garble(`{"version":5,"pages":{"e--`)
	nothing("garbled another way")
	res, err := Archive(dest, Options{BrokenNote: note})
	if err != nil || res.Rendered != 2 || !strings.Contains(res.Note, "every page is rendered again") {
		t.Fatalf("the same bytes again: render = %+v, %v; want every page rendered, and a note why", res, err)
	}
	if _, err := loadState(filepath.Join(dest, "html")); err != nil {
		t.Errorf("the state saved after cannot be read: %v", err)
	}
	if _, err := os.Stat(note); err != nil {
		t.Fatalf("the note went before a state loaded: %v", err)
	}
	if res, err := Archive(dest, Options{BrokenNote: note}); err != nil || res.UpToDate != 2 {
		t.Fatalf("after: render = %+v, %v", res, err)
	}
	if _, err := os.Stat(note); !os.IsNotExist(err) {
		t.Errorf("a state that loads left the note: %v", err)
	}
}
