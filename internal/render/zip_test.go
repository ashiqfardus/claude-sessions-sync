package render

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/convzip"
)

const zipID = "11111111-2222-3333-4444-555555555555"

// zippedArchive is an archive whose one conversation was rendered, then zipped.
func zippedArchive(t *testing.T) (dest, bucket, pagePath string) {
	t.Helper()
	dest = t.TempDir()
	bucket = filepath.Join(dest, "projects", "e--work-api")
	if err := os.MkdirAll(bucket, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"user","timestamp":"2026-08-30T09:00:00.000Z","message":{"role":"user","content":"a zipped question"}}` + "\n"
	if err := os.WriteFile(filepath.Join(bucket, zipID+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if res, err := Archive(dest, Options{}); err != nil || res.Rendered != 1 {
		t.Fatalf("first render: %+v, %v", res, err)
	}
	if _, err := convzip.Zip(bucket, zipID); err != nil {
		t.Fatal(err)
	}
	return dest, bucket, filepath.Join(dest, "html", "e--work-api", zipID+".html")
}

// countUnpacks counts the zips render unpacks.
func countUnpacks(t *testing.T) *int {
	t.Helper()
	n := new(int)
	real := materialize
	t.Cleanup(func() { materialize = real })
	materialize = func(bucketDir, id, tmpDir string) (string, func(), error) {
		*n++
		return real(bucketDir, id, tmpDir)
	}
	return n
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Zipping a conversation must not take its page away or out of the listings, and a
// quiet run reads nothing of the zip but its index.
func TestRenderKeepsAndListsZippedPages(t *testing.T) {
	dest, _, pagePath := zippedArchive(t)
	before := mustRead(t, pagePath)
	unpacks := countUnpacks(t)

	res, err := Archive(dest, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.UpToDate != 1 || res.Rendered != 0 || res.Unpublished != 0 || res.Failed != 0 {
		t.Errorf("the zipped conversation's page is not reported up to date: %+v", res)
	}
	if *unpacks != 0 {
		t.Errorf("a quiet run unpacked the zip %d time(s)", *unpacks)
	}
	if after, err := os.ReadFile(pagePath); err != nil || !bytes.Equal(after, before) {
		t.Errorf("the page was removed or rewritten: %v", err)
	}
	if idx := string(mustRead(t, filepath.Join(dest, "html", "e--work-api", "index.html"))); !strings.Contains(idx, `href="./`+zipID+`.html"`) {
		t.Errorf("the project index no longer links the zipped conversation:\n%s", idx)
	}
	if idx := string(mustRead(t, filepath.Join(dest, "html", "index.html"))); !strings.Contains(idx, pageHref("e--work-api", zipID)) || !strings.Contains(idx, "a zipped question") {
		t.Errorf("the main index no longer lists the zipped conversation:\n%s", idx)
	}
}

// A template change still reaches a zipped conversation: its page is rendered again
// from the zip, within the budget like any page, and the zip is left as it was.
func TestRenderRerendersZippedOnTemplateBump(t *testing.T) {
	dest, bucket, pagePath := zippedArchive(t)
	zp := convzip.Path(bucket, zipID)
	zipBefore, zst := mustRead(t, zp), mustStat(t, zp)

	// The last pass was an older template, and so is the page, at its transcript's time.
	htmlDir := filepath.Join(dest, "html")
	s := loadState(htmlDir)
	s.Version = templateVersion - 1
	if err := saveState(htmlDir, s); err != nil {
		t.Fatal(err)
	}
	pst := mustStat(t, pagePath)
	if err := os.WriteFile(pagePath, []byte("an older template's page"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(pagePath, pst.ModTime(), pst.ModTime())

	tmp := t.TempDir()
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, tmp)
	}
	unpacks := countUnpacks(t)

	// A spent budget leaves it for the next pass, still listed.
	real := now
	at := real()
	now = func() time.Time { at = at.Add(time.Hour); return at }
	res, err := Archive(dest, Options{Budget: time.Second})
	now = real
	if err != nil {
		t.Fatal(err)
	}
	if res.Deferred != 1 || *unpacks != 0 {
		t.Errorf("a spent budget did not defer the zipped page: %+v, %d unpack(s)", res, *unpacks)
	}
	if idx := string(mustRead(t, filepath.Join(htmlDir, "index.html"))); !strings.Contains(idx, pageHref("e--work-api", zipID)) {
		t.Errorf("a deferred zipped page fell out of the index:\n%s", idx)
	}

	res, err = Archive(dest, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Rendered != 1 || *unpacks != 1 {
		t.Errorf("the template bump did not render the zipped page from its zip: %+v, %d unpack(s)", res, *unpacks)
	}
	if got := string(mustRead(t, pagePath)); !strings.Contains(got, "a zipped question") {
		t.Errorf("the page was not rewritten:\n%s", got)
	}
	if !bytes.Equal(mustRead(t, zp), zipBefore) || !mustStat(t, zp).ModTime().Equal(zst.ModTime()) {
		t.Error("rendering changed the zip")
	}
	if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
		t.Errorf("the unpacked copy was left behind: %v", ents)
	}
	if res, _ := Archive(dest, Options{}); res.UpToDate != 1 || *unpacks != 1 {
		t.Errorf("the re-rendered page is not current: %+v, %d unpack(s)", res, *unpacks)
	}
}

func mustStat(t *testing.T, p string) os.FileInfo {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st
}
