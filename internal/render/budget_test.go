package render

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The SessionEnd hook is killed after 120 seconds. A first render of a large archive
// took longer, and a killed run kept none of its work, so every session end started
// over and never finished. A budget stops rendering in time, and the next pass
// carries on from there.
func TestRenderBudgetDefersAndResumes(t *testing.T) {
	dest := t.TempDir()
	bucket := filepath.Join(dest, "projects", "e--app")
	if err := os.MkdirAll(bucket, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		body := fmt.Sprintf(`{"type":"user","message":{"role":"user","content":"prompt %d"}}`+"\n", i)
		if err := os.WriteFile(filepath.Join(bucket, fmt.Sprintf("s%d.jsonl", i)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A budget that is already spent: nothing new is rendered.
	res, err := Archive(dest, Options{Budget: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Deferred != 3 || res.Rendered != 0 {
		t.Fatalf("expected every page deferred, got %+v", res)
	}

	res, err = Archive(dest, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Rendered != 3 || res.Deferred != 0 {
		t.Fatalf("the next pass did not render what was deferred: %+v", res)
	}
	if res, _ := Archive(dest, Options{Budget: 1}); res.UpToDate != 3 || res.Deferred != 0 {
		t.Errorf("current pages were deferred instead of listed: %+v", res)
	}
}

// Progress saved mid-run must never carry pages from an older template forward as
// current, or a template change would stop halfway and stay there.
func TestProgressDropsAnOlderTemplate(t *testing.T) {
	prev := renderState{Version: templateVersion - 1, Pages: map[string]pageState{"a/old": {Size: 1}}}
	next := renderState{Version: templateVersion, Pages: map[string]pageState{"a/new": {Size: 2}}}
	got := progress(prev, next)
	if _, ok := got.Pages["a/old"]; ok || len(got.Pages) != 1 {
		t.Errorf("progress = %+v", got.Pages)
	}
	prev.Version = templateVersion
	if got := progress(prev, next); len(got.Pages) != 2 {
		t.Errorf("progress lost this template's earlier pages: %+v", got.Pages)
	}
}

// A symlinked html/<bucket> planted in the shared archive, together with a shared
// exclusion, made the next push delete every .html file wherever the link pointed.
func TestRemovePagesDoesNotFollowLinks(t *testing.T) {
	html := t.TempDir()
	victim := t.TempDir()
	keep := filepath.Join(victim, "keep.html")
	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(html, "e--app")); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	if _, err := removePages(html, "e--app"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Error("removePages deleted a file through a symlink")
	}
}
