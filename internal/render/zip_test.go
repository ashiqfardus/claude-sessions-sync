package render

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/convzip"
	"github.com/ashiqfardus/claude-sessions-sync/internal/work"
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

// countIndexReads counts the zip indexes render reads.
func countIndexReads(t *testing.T) *int {
	t.Helper()
	n := new(int)
	real := zipEntries
	t.Cleanup(func() { zipEntries = real })
	zipEntries = func(p string) (map[string]work.PackEntry, error) {
		*n++
		return real(p)
	}
	return n
}

// garble overwrites a file with junk of the same size and time.
func garble(t *testing.T, p string) {
	t.Helper()
	st := mustStat(t, p)
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), int(st.Size())), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(p, st.ModTime(), st.ModTime())
}

// listed fails unless both indexes link the zipped conversation's page.
func listed(t *testing.T, dest string) {
	t.Helper()
	if idx := string(mustRead(t, filepath.Join(dest, "html", "e--work-api", "index.html"))); !strings.Contains(idx, `href="./`+zipID+`.html"`) {
		t.Errorf("the project index does not link the zipped conversation:\n%s", idx)
	}
	if idx := string(mustRead(t, filepath.Join(dest, "html", "index.html"))); !strings.Contains(idx, pageHref("e--work-api", zipID)) || !strings.Contains(idx, "a zipped question") {
		t.Errorf("the main index does not list the zipped conversation:\n%s", idx)
	}
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
	listed(t, dest)

	// Its zip unchanged since, the next quiet run reads no zip at all, not even an index:
	// on a drive that streams files, each read is a download.
	reads := countIndexReads(t)
	res, err = Archive(dest, Options{Budget: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if *reads != 0 || *unpacks != 0 || res.UpToDate != 1 {
		t.Errorf("a quiet run over a current zipped page read %d index(es) and unpacked %d: %+v", *reads, *unpacks, res)
	}
	listed(t, dest)
}

// A loose copy beside a zip that holds more than the zip - a continuation - is the one
// rendered; the zip is unpacked to tell, as import does, its index not read.
func TestRenderPrefersLooseBesideZip(t *testing.T) {
	dest, bucket, pagePath := zippedArchive(t)
	loose := filepath.Join(bucket, zipID+".jsonl")
	body := `{"type":"user","timestamp":"2026-08-30T09:00:00.000Z","message":{"role":"user","content":"a zipped question"}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-08-30T09:05:00.000Z","message":{"role":"assistant","content":"continued elsewhere"}}` + "\n"
	if err := os.WriteFile(loose, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	reads, unpacks := countIndexReads(t), countUnpacks(t)

	res, err := Archive(dest, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Rendered != 1 || *reads != 0 || *unpacks != 1 {
		t.Errorf("the loose copy was not the one rendered: %+v, %d index read(s), %d unpack(s)", res, *reads, *unpacks)
	}
	if got := string(mustRead(t, pagePath)); !strings.Contains(got, "continued elsewhere") {
		t.Errorf("the page lacks the loose copy's turn:\n%s", got)
	}
}

// A loose copy beside a zip that holds more - written by a computer the zip had not
// reached yet - does not hide the zip's turns from the page, even when it is this
// machine's own copy byte for byte. A zip beside it that cannot be read leaves the page
// to the loose copy.
func TestRenderShowsTheZipBesideALooseCopyThatHoldsLess(t *testing.T) {
	dest := t.TempDir()
	bucket := filepath.Join(dest, "projects", "e--work-api")
	local := filepath.Join(t.TempDir(), "projects")
	q := `{"type":"user","uuid":"q1","timestamp":"2026-08-30T09:00:00.000Z","message":{"role":"user","content":"a zipped question"}}` + "\n"
	a := `{"type":"assistant","uuid":"a1","timestamp":"2026-08-30T09:05:00.000Z","message":{"role":"assistant","content":"only the zip has this"}}` + "\n"
	at := time.Now().Add(-time.Hour)
	for _, p := range []string{filepath.Join(bucket, zipID+".jsonl"), filepath.Join(local, "e--work-api", zipID+".jsonl")} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(p, []byte(q+a), 0o644)
	}
	if _, err := convzip.Zip(bucket, zipID); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(bucket, zipID+".jsonl"), filepath.Join(local, "e--work-api", zipID+".jsonl")} {
		os.WriteFile(p, []byte(q), 0o644)
		os.Chtimes(p, at, at)
	}
	page := filepath.Join(dest, "html", "e--work-api", zipID+".html")
	if _, err := Archive(dest, Options{LocalProjects: local}); err != nil {
		t.Fatal(err)
	}
	if got := string(mustRead(t, page)); !strings.Contains(got, "only the zip has this") {
		t.Errorf("the page lacks the turn only the zip holds:\n%s", got)
	}

	// The zip is replaced by one holding more, the loose copy unchanged: the page is
	// rendered again.
	other := filepath.Join(t.TempDir(), "e--work-api")
	a2 := `{"type":"assistant","uuid":"a2","timestamp":"2026-08-30T09:06:00.000Z","message":{"role":"assistant","content":"zipped again with more"}}` + "\n"
	os.MkdirAll(other, 0o755)
	os.WriteFile(filepath.Join(other, zipID+".jsonl"), []byte(q+a+a2), 0o644)
	if _, err := convzip.Zip(other, zipID); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(convzip.Path(other, zipID), convzip.Path(bucket, zipID)); err != nil {
		t.Fatal(err)
	}
	if _, err := Archive(dest, Options{LocalProjects: local}); err != nil {
		t.Fatal(err)
	}
	if got := string(mustRead(t, page)); !strings.Contains(got, "zipped again with more") {
		t.Errorf("the page was not rendered again from the new zip beside the loose copy:\n%s", got)
	}

	garble(t, convzip.Path(bucket, zipID))
	if res, err := Archive(dest, Options{Force: true}); err != nil || res.Failed != 0 {
		t.Fatalf("%+v, %v", res, err)
	}
	if got := string(mustRead(t, page)); !strings.Contains(got, "a zipped question") {
		t.Errorf("the loose copy beside an unreadable zip was not rendered:\n%s", got)
	}
}

// A zip that cannot be read - garbled, or a placeholder not downloaded yet - keeps its
// page, which stays listed from what the last pass knew, and is counted as failed.
func TestRenderKeepsPageOfUnreadableZip(t *testing.T) {
	dest, bucket, pagePath := zippedArchive(t)
	before := mustRead(t, pagePath)
	garble(t, convzip.Path(bucket, zipID))

	for i := 0; i < 2; i++ { // and the next pass too: its state is kept
		res, err := Archive(dest, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Failed != 1 || res.UpToDate != 0 || res.Rendered != 0 {
			t.Errorf("pass %d: an unreadable zip is not counted as failed: %+v", i+1, res)
		}
		if after, err := os.ReadFile(pagePath); err != nil || !bytes.Equal(after, before) {
			t.Errorf("pass %d: the page was removed or rewritten: %v", i+1, err)
		}
		listed(t, dest)
	}
}

// The listing of a page kept for an unreadable zip dates it by the zip, which Zip dates
// by the conversation's last activity, not by when the page was last written.
func TestRenderDatesAKeptPageByItsZip(t *testing.T) {
	dest := t.TempDir()
	bucket := filepath.Join(dest, "projects", "e--work-api")
	tp := filepath.Join(bucket, zipID+".jsonl")
	if err := os.MkdirAll(bucket, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"user","timestamp":"2026-08-30T09:00:00.000Z","message":{"role":"user","content":"a zipped question"}}` + "\n"
	if err := os.WriteFile(tp, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	last := time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC)
	os.Chtimes(tp, last, last)
	if _, err := Archive(dest, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := convzip.Zip(bucket, zipID); err != nil {
		t.Fatal(err)
	}
	garble(t, convzip.Path(bucket, zipID))
	written := time.Date(2030, 1, 2, 3, 4, 0, 0, time.UTC) // the page, written since
	os.Chtimes(filepath.Join(dest, "html", "e--work-api", zipID+".html"), written, written)

	if res, err := Archive(dest, Options{}); err != nil || res.Failed != 1 {
		t.Fatalf("render = %+v, %v; want the unreadable zip failed", res, err)
	}
	idx := string(mustRead(t, filepath.Join(dest, "html", "index.html")))
	if want := last.Local().Format("2006-01-02 15:04"); !strings.Contains(idx, want) || strings.Contains(idx, written.Local().Format("2006-01-02")) {
		t.Errorf("the kept page is not dated %s, its last activity:\n%s", want, idx)
	}
}

// A template change still reaches a zipped conversation: its page is rendered again
// from the zip, within the budget like any page, and the zip is left as it was.
func TestRenderRerendersZippedOnTemplateBump(t *testing.T) {
	dest, bucket, pagePath := zippedArchive(t)
	zp := convzip.Path(bucket, zipID)
	zipBefore, zst := mustRead(t, zp), mustStat(t, zp)
	// A pass after zipping records the zip: a remembered zip still takes the bump.
	if _, err := Archive(dest, Options{}); err != nil {
		t.Fatal(err)
	}

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
