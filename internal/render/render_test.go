package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeArchive(t *testing.T, lines ...string) string {
	t.Helper()
	dest := t.TempDir()
	bucket := filepath.Join(dest, "projects", "e--work-api")
	if err := os.MkdirAll(bucket, 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(bucket, "session-1.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dest
}

func page(t *testing.T, dest string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dest, "html", "e--work-api", "session-1.html"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// Transcripts contain arbitrary text, including HTML and script tags from pages that
// were being worked on. Everything is escaped before any formatting is applied.
func TestRenderEscapesContent(t *testing.T) {
	dest := writeArchive(t,
		`{"type":"user","cwd":"/work/api","timestamp":"2026-08-30T09:00:00.000Z","message":{"role":"user","content":"fix <script>alert('xss')</script> please"}}`)

	if _, err := Archive(dest, false); err != nil {
		t.Fatal(err)
	}
	got := page(t, dest)

	if strings.Contains(got, "<script>alert") {
		t.Error("script tag from transcript content was not escaped")
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("expected escaped content, got:\n%s", got)
	}
}

func TestRenderCollapsesToolsAndResults(t *testing.T) {
	dest := writeArchive(t,
		`{"type":"assistant","timestamp":"2026-08-30T09:00:01.000Z","message":{"role":"assistant","content":[`+
			`{"type":"text","text":"looking now"},`+
			`{"type":"tool_use","name":"Bash","input":{"command":"ls -la"}},`+
			`{"type":"thinking","thinking":"weighing the options"}]}}`,
		`{"type":"user","timestamp":"2026-08-30T09:00:02.000Z","message":{"role":"user","content":[`+
			`{"type":"tool_result","content":"total 4\ndrwxr-xr-x"}]}}`)

	if _, err := Archive(dest, false); err != nil {
		t.Fatal(err)
	}
	got := page(t, dest)

	for _, want := range []string{
		`looking now`,
		`<details class="tool"><summary>&#9881; Bash</summary>`,
		`ls -la`,
		`<details class="thinking"><summary>thinking</summary>`,
		`weighing the options`,
		`<details class="result"><summary>result</summary>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// A single tool result can carry an entire file; rendering all of it produces a page
// a phone cannot open.
func TestRenderTruncatesHugeBlocks(t *testing.T) {
	huge := strings.Repeat("x", maxBlockChars*2)
	dest := writeArchive(t,
		`{"type":"user","timestamp":"2026-08-30T09:00:00.000Z","message":{"role":"user","content":"`+huge+`"}}`)

	if _, err := Archive(dest, false); err != nil {
		t.Fatal(err)
	}
	got := page(t, dest)

	if !strings.Contains(got, "truncated") {
		t.Error("a huge block should be truncated with a pointer to the .jsonl")
	}
	if len(got) > maxBlockChars*2 {
		t.Errorf("page is %d bytes: truncation did not take effect", len(got))
	}
}

// A killed session leaves a half-written final line. The page must still render, and
// must say what it could not read rather than pretending to be complete.
func TestRenderSurvivesTruncatedTranscript(t *testing.T) {
	dest := writeArchive(t,
		`{"type":"user","timestamp":"2026-08-30T09:00:00.000Z","message":{"role":"user","content":"a real question"}}`,
		`{"type":"user","timestamp":"2026-08-30T09:0`)

	res, err := Archive(dest, false)
	if err != nil {
		t.Fatalf("a truncated line must not be fatal: %v", err)
	}
	if res.Rendered != 1 {
		t.Errorf("rendered = %d, want 1", res.Rendered)
	}
	if res.Skipped != 1 {
		t.Errorf("skipped = %d, want 1", res.Skipped)
	}

	got := page(t, dest)
	if !strings.Contains(got, "a real question") {
		t.Error("content before the bad line was lost")
	}
	if !strings.Contains(got, "could not be parsed") {
		t.Error("the page should admit what it skipped")
	}
}

// Re-rendering an unchanged archive should do nothing: this runs after every push,
// over a cloud filesystem.
func TestRenderSkipsUpToDatePages(t *testing.T) {
	dest := writeArchive(t,
		`{"type":"user","timestamp":"2026-08-30T09:00:00.000Z","message":{"role":"user","content":"hello"}}`)

	if _, err := Archive(dest, false); err != nil {
		t.Fatal(err)
	}
	res, err := Archive(dest, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rendered != 0 || res.UpToDate != 1 {
		t.Errorf("second pass rendered=%d upToDate=%d, want 0 and 1", res.Rendered, res.UpToDate)
	}

	// --force exists for when the template changes.
	res, err = Archive(dest, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rendered != 1 {
		t.Errorf("--force should re-render, got %d", res.Rendered)
	}
}

// A changed transcript must be re-rendered, with the same timestamp tolerance used
// everywhere else - a synced filesystem rounds mtimes.
func TestRenderPicksUpChangedTranscript(t *testing.T) {
	dest := writeArchive(t,
		`{"type":"user","timestamp":"2026-08-30T09:00:00.000Z","message":{"role":"user","content":"first"}}`)
	if _, err := Archive(dest, false); err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(dest, "projects", "e--work-api", "session-1.jsonl")
	body := `{"type":"user","timestamp":"2026-08-30T09:00:00.000Z","message":{"role":"user","content":"second"}}` + "\n"
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(src, later, later); err != nil {
		t.Fatal(err)
	}

	res, err := Archive(dest, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rendered != 1 {
		t.Errorf("a changed transcript must be re-rendered, got %d", res.Rendered)
	}
	if !strings.Contains(page(t, dest), "second") {
		t.Error("the page still shows the old content")
	}
}

func TestRenderWritesIndex(t *testing.T) {
	dest := writeArchive(t,
		`{"type":"user","cwd":"/work/api","timestamp":"2026-08-30T09:00:00.000Z","message":{"role":"user","content":"why is the build failing"}}`)

	if _, err := Archive(dest, false); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dest, "html", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)

	if !strings.Contains(got, "why is the build failing") {
		t.Error("the index should show each session's opening message")
	}
	if !strings.Contains(got, `href="e--work-api/session-1.html"`) {
		t.Errorf("the index should link to the page, got:\n%s", got)
	}
	// Pages are read from a cloud folder on a phone: one self-contained file, no
	// external stylesheet to fail to resolve and no script to be blocked.
	if strings.Contains(got, "<script") || strings.Contains(got, `rel="stylesheet"`) {
		t.Error("pages must be self-contained: no scripts, no external stylesheets")
	}
	if !strings.Contains(got, "viewport") {
		t.Error("missing the viewport meta tag that makes this readable on a phone")
	}
}

// A bucket with no transcripts must not leave an empty directory in a synced folder.
func TestRenderCreatesNoEmptyDirectories(t *testing.T) {
	dest := writeArchive(t,
		`{"type":"user","timestamp":"2026-08-30T09:00:00.000Z","message":{"role":"user","content":"hi"}}`)
	memoryOnly := filepath.Join(dest, "projects", "e--memory-only", "memory")
	if err := os.MkdirAll(memoryOnly, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := Archive(dest, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "html", "e--memory-only")); err == nil {
		t.Error("an empty html directory was created for a bucket with no transcripts")
	}
}
