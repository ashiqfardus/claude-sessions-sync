package render

import (
	"bytes"
	"os"
	"path/filepath"
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

// A state that has stayed unreadable, untouched, for stateSettle is broken, not being
// replaced: every page is rendered again and a readable state saved, with no one
// having to run render --force.
func TestLongBrokenStateRendersEveryPageAgain(t *testing.T) {
	wait := stateWait
	stateWait = time.Millisecond
	t.Cleanup(func() { stateWait = wait })
	dest := stateArchive(t)
	state := statePath(filepath.Join(dest, "html"))
	if err := os.WriteFile(state, []byte(`{"version":5,"pa`), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-stateSettle - time.Minute)
	if err := os.Chtimes(state, old, old); err != nil {
		t.Fatal(err)
	}
	if res, err := Archive(dest, Options{}); err != nil || res.Rendered == 0 {
		t.Fatalf("render = %+v, %v; want every page rendered again", res, err)
	}
	if _, err := loadState(filepath.Join(dest, "html")); err != nil {
		t.Errorf("the state saved after cannot be read: %v", err)
	}
}
