package main

// doctor on a sync folder that fetches each file only when it is opened: it prints as
// it goes, gives up on a check that takes too long, reads no zip it need not, and says
// how to make the folder available offline.

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/convzip"
	"github.com/ashiqfardus/claude-sessions-sync/internal/work"
)

const (
	docLooseID  = "aaaaaaaa-1111-2222-3333-444444444444"
	docZippedID = "bbbbbbbb-1111-2222-3333-444444444444"
	docOtherID  = "cccccccc-1111-2222-3333-444444444444"
	docBucket   = "-work-web-app"
)

const streamingText = "Your archive folder downloads files only when they're opened, which makes backups and imports slow. In Google Drive, right-click the claude-sessions folder and choose Offline access, then Available offline. OneDrive calls it Always keep on this device."

// doctorFixture is a healthy profile and archive: two conversations that ended two
// hours ago, one archived loose and one zipped as push leaves it, its comparison
// recorded; plus another computer's zipped conversation and a work-folder pack.
func doctorFixture(t *testing.T) (claudeDir, dest string) {
	t.Helper()
	root := t.TempDir()
	claudeDir, dest = filepath.Join(root, ".claude"), filepath.Join(root, "archive")
	writeFile(t, filepath.Join(claudeDir, "settings.json"), `{"cleanupPeriodDays":3650}`)

	ended := time.Now().Add(-2 * time.Hour)
	local := filepath.Join(claudeDir, "projects", docBucket)
	archived := filepath.Join(dest, "projects", docBucket)
	for _, id := range []string{docLooseID, docZippedID} {
		body := `{"type":"user","cwd":"/work/web-app","sessionId":"` + id + `","timestamp":"2026-11-20T09:00:00.000Z","message":{"role":"user","content":"hi"}}` + "\n"
		for _, p := range []string{filepath.Join(local, id+".jsonl"), filepath.Join(archived, id+".jsonl")} {
			writeFile(t, p, body)
			if err := os.Chtimes(p, ended, ended); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := convzip.Zip(archived, docZippedID); err != nil {
		t.Fatal(err)
	}
	src, err := os.Stat(filepath.Join(local, docZippedID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	zst, err := os.Stat(convzip.Path(archived, docZippedID))
	if err != nil {
		t.Fatal(err)
	}
	z := &zipRecord{dirty: true, m: map[string]zipCompared{docBucket + "/" + docZippedID: {
		Size: src.Size(), MTime: src.ModTime(), ZipSize: zst.Size(), ZipMTime: zst.ModTime()}}}
	if err := z.save(claudeDir); err != nil {
		t.Fatal(err)
	}

	other := filepath.Join(dest, "projects", "-work-api")
	writeFile(t, filepath.Join(other, docOtherID+".jsonl"), `{"type":"user","cwd":"/work/api","message":{"role":"user","content":"hi"}}`+"\n")
	if _, err := convzip.Zip(other, docOtherID); err != nil {
		t.Fatal(err)
	}
	pack := filepath.Join(dest, "work", "LAP-2", "notes", "pack.zip")
	writeFile(t, filepath.Join(root, "loose.txt"), "a note\n")
	fi, _ := os.Stat(filepath.Join(root, "loose.txt"))
	if err := os.MkdirAll(filepath.Dir(pack), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := work.WritePack(pack, []work.PackFile{{Rel: "loose.txt", Path: filepath.Join(root, "loose.txt"), Size: fi.Size(), ModTime: fi.ModTime()}}); err != nil {
		t.Fatal(err)
	}
	return claudeDir, dest
}

// startDoctor runs doctor in this process, its output arriving line by line as it is
// printed. wait returns once doctor has, with every line printed after those read.
func startDoctor(t *testing.T, claudeDir, dest string) (lines <-chan string, wait func() (string, error)) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	out, done := make(chan string, 1024), make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			out <- sc.Text()
		}
		close(out)
	}()
	go func() {
		err := cmdDoctor([]string{"--claude-dir", claudeDir, "--archive", dest, "--no-write-probe"})
		w.Close()
		done <- err
	}()
	waited := false
	wait = func() (string, error) {
		err := <-done
		waited = true
		os.Stdout = stdout
		var rest []string
		for l := range out {
			rest = append(rest, l)
		}
		return strings.Join(rest, "\n"), err
	}
	t.Cleanup(func() {
		if !waited {
			wait()
		}
	})
	return out, wait
}

func runDoctorHere(t *testing.T, claudeDir, dest string) string {
	t.Helper()
	_, wait := startDoctor(t, claudeDir, dest)
	out, _ := wait()
	return out
}

// hook sets a test hook for the length of the test.
func hook[T any](t *testing.T, v *T, to T) {
	old := *v
	*v = to
	t.Cleanup(func() { *v = old })
}

// The header and everything on this computer print before the archive is touched, so
// a stalled sync folder shows a doctor at work, not a hung one.
func TestDoctorPrintsHeaderBeforeReadingTheArchive(t *testing.T) {
	claudeDir, dest := doctorFixture(t)
	release := make(chan struct{})
	hook(t, &beforeArchive, func(string) { <-release })

	lines, wait := startDoctor(t, claudeDir, dest)
	var seen []string
	timeout := time.After(10 * time.Second)
	for len(seen) == 0 || !strings.Contains(seen[len(seen)-1], "retention") {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("doctor ended while the archive was blocked:\n%s", strings.Join(seen, "\n"))
			}
			seen = append(seen, l)
		case <-timeout:
			close(release)
			t.Fatalf("nothing printed while the archive was blocked; got:\n%s", strings.Join(seen, "\n"))
		}
	}
	before := strings.Join(seen, "\n")
	if !strings.HasPrefix(seen[0], "claude-sessions ") {
		t.Errorf("the first line is not the header:\n%s", before)
	}
	for _, want := range []string{"+ claude root", "+ projects", "+ retention"} {
		if !strings.Contains(before, want) {
			t.Errorf("%q did not print before the archive was read:\n%s", want, before)
		}
	}
	if strings.Contains(before, "+ archive ") {
		t.Errorf("the archive check finished while it was blocked:\n%s", before)
	}

	close(release)
	rest, _ := wait()
	if !strings.Contains(rest, "+ archive ") || !strings.Contains(rest, "+ archived") {
		t.Errorf("the archive checks did not finish once released:\n%s", rest)
	}
}

const skipLine = "skipped: the archive folder is not responding (see the hint above)"

const notRespondingText = "If this folder is in Google Drive, OneDrive, Dropbox or iCloud Drive, make it available offline: right-click it and choose Offline access, then Available offline (Google Drive), Always keep on this device (OneDrive), Make available offline (Dropbox) or Keep Downloaded (iCloud Drive). Otherwise the folder is not responding: check that its drive or network share is connected."

// sanity bounds a doctor run with a step blocked: far over the 1 s limit the tests set,
// as Windows runs schtasks for the sweep check, which a CI runner can make slow. Each
// test proves the single wait by the steps started, not by the clock.
const sanity = 10 * time.Second

// blockSteps blocks the named archive steps forever and returns the steps started, in
// order. Never released: an abandoned step stays blocked here, touching no file while
// the test's folders are removed.
func blockSteps(t *testing.T, names ...string) (started func() []string) {
	var mu sync.Mutex
	var got []string
	block := make(chan struct{})
	hook(t, &beforeArchive, func(name string) {
		mu.Lock()
		got = append(got, name)
		mu.Unlock()
		for _, n := range names {
			if n == name {
				<-block
			}
		}
	})
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

// A check over its limit is abandoned and named. The archive checks after it are
// skipped in one line below the hint; the checks of this computer still run.
func TestDoctorTimesOutASlowCheck(t *testing.T) {
	claudeDir, dest := doctorFixture(t)
	hook(t, &archiveLimit, time.Second)
	started := blockSteps(t, "manifest")

	start := time.Now()
	out := runDoctorHere(t, claudeDir, dest)
	if took := time.Since(start); took > sanity {
		t.Errorf("doctor took %s with one check blocked and a 1s limit", took)
	}
	want := "manifest: not finished after 1s - the archive folder may be downloading files on demand"
	if !strings.Contains(out, want) || !strings.Contains(out, "  ! manifest ") {
		t.Errorf("no warning for the slow check, want %q in:\n%s", want, out)
	}
	// The one read streamingHint makes was fast: no sign the folder streams files, so the
	// hint is the conditional one.
	if hint, skip := strings.Index(out, notRespondingText), strings.Index(out, skipLine); hint < 0 || skip < hint ||
		strings.Count(out, notRespondingText) != 1 || strings.Count(out, skipLine) != 1 || strings.Contains(out, streamingText) {
		t.Errorf("want the conditional hint once, then the skip line once:\n%s", out)
	}
	if got := strings.Join(started(), ","); got != "archive,versions,name,last backup,zips,prune,profile,manifest" {
		t.Errorf("archive steps started: %s; want none after manifest", got)
	}
	for _, later := range []string{"session hook", "sweep", "last push", "warning(s),"} {
		if !strings.Contains(out, later) {
			t.Errorf("%q missing: the checks of this computer did not run:\n%s", later, out)
		}
	}
	for _, skipped := range []string{"work folders", "+ archived"} {
		if strings.Contains(out, skipped) {
			t.Errorf("%q ran after the archive stopped answering:\n%s", skipped, out)
		}
	}
}

// R20: with two archive steps blocked, doctor waits out the first only, and never starts
// the second, so a stuck folder costs one time limit.
func TestDoctorSkipsTheArchiveOnceAStepTimesOut(t *testing.T) {
	claudeDir, dest := doctorFixture(t)
	hook(t, &archiveLimit, time.Second)
	started := blockSteps(t, "versions", "manifest")

	start := time.Now()
	out := runDoctorHere(t, claudeDir, dest)
	if took := time.Since(start); took > sanity {
		t.Errorf("doctor took %s with two steps blocked and a 1s limit", took)
	}
	if !strings.Contains(out, "versions: not finished after 1s") || !strings.Contains(out, skipLine) {
		t.Errorf("want the first warning and the skip line:\n%s", out)
	}
	if strings.Contains(out, "manifest: not finished") {
		t.Errorf("the second blocked step was waited out too:\n%s", out)
	}
	if got := started(); strings.Join(got, ",") != "archive,versions" {
		t.Errorf("archive steps started: %v; want archive, versions and nothing after", got)
	}
}

// The archive's own folder not answering in time is the same: no archive step after it
// runs, and the checks of this computer do.
func TestDoctorRunsLocalChecksWhenTheArchiveDoesNotAnswer(t *testing.T) {
	claudeDir, dest := doctorFixture(t)
	hook(t, &archiveLimit, time.Second)
	started := blockSteps(t, "archive")

	out := runDoctorHere(t, claudeDir, dest)
	for _, want := range []string{"archive: not finished after 1s", notRespondingText, skipLine, "session hook", "last push"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q missing:\n%s", want, out)
		}
	}
	if strings.Contains(out, streamingText) {
		t.Errorf("the streaming hint, with no slow read seen:\n%s", out)
	}
	if got := started(); strings.Join(got, ",") != "archive" {
		t.Errorf("archive steps started: %v; want only archive", got)
	}
}

// A healthy archive's zips and packs are counted by name and stat: doctor opens none.
// It reads a zip's index only for a session that would otherwise be pending.
func TestDoctorDoesNotOpenPacks(t *testing.T) {
	claudeDir, dest := doctorFixture(t)
	opened := 0
	hook(t, &readZipIndex, func(p string) (map[string]work.PackEntry, error) {
		opened++
		return convzip.Entries(p)
	})

	out := runDoctorHere(t, claudeDir, dest)
	if opened != 0 {
		t.Errorf("doctor read %d zip index(es) of a healthy archive:\n%s", opened, out)
	}
	if !strings.Contains(out, "+ archived") {
		t.Errorf("doctor did not count the zipped conversations as archived:\n%s", out)
	}
}

// One read of one archived file over the limit says how to make the folder available
// offline, once; a fast one says nothing.
func TestStreamingHint(t *testing.T) {
	claudeDir, dest := doctorFixture(t)
	hook(t, &hintLimit, 50*time.Millisecond)
	read := readHead
	block := make(chan struct{}) // never released, as in TestDoctorTimesOutASlowCheck
	hook(t, &readHead, func(string) { <-block })

	if out := runDoctorHere(t, claudeDir, dest); strings.Count(out, streamingText) != 1 {
		t.Errorf("want the hint once on a slow read, got:\n%s", out)
	}

	readHead = read
	hintLimit = 3 * time.Second
	if out := runDoctorHere(t, claudeDir, dest); strings.Contains(out, streamingText) {
		t.Errorf("hint on a fast read:\n%s", out)
	}
}

// A slow read seen, then a timeout: the streaming hint, not the conditional one, and
// once.
func TestDoctorTimeoutAfterASlowReadShowsTheStreamingHint(t *testing.T) {
	claudeDir, dest := doctorFixture(t)
	hook(t, &archiveLimit, time.Second)
	hook(t, &hintLimit, 50*time.Millisecond)
	block := make(chan struct{}) // never released, as in blockSteps
	hook(t, &readHead, func(string) { <-block })
	blockSteps(t, "manifest")

	out := runDoctorHere(t, claudeDir, dest)
	if hint, skip := strings.Index(out, streamingText), strings.Index(out, skipLine); hint < 0 || skip < hint ||
		strings.Count(out, streamingText) != 1 || strings.Contains(out, notRespondingText) {
		t.Errorf("want the streaming hint once, then the skip line:\n%s", out)
	}
}

// captureStderr pipes os.Stderr to lines; done puts it back and returns the lines not
// read yet.
func captureStderr(t *testing.T) (lines <-chan string, done func() []string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = w
	out := make(chan string, 1024)
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			out <- sc.Text()
		}
		close(out)
	}()
	restored := false
	done = func() []string {
		if !restored {
			restored = true
			os.Stderr = stderr
			w.Close()
		}
		var rest []string
		for l := range out {
			rest = append(rest, l)
		}
		return rest
	}
	t.Cleanup(func() { done() })
	return out, done
}

// waitFor reads lines until one holds text, giving up after five seconds.
func waitFor(lines <-chan string, text string) bool {
	timeout := time.After(5 * time.Second)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				return false
			}
			if strings.Contains(l, text) {
				return true
			}
		case <-timeout:
			return false
		}
	}
}

// hintIfSlow prints the hint once while a read is still going, never for one stopped in
// time, and never when it is not to show.
func TestStreamingHintOnASlowFirstRead(t *testing.T) {
	lines, done := captureStderr(t)
	hook(t, &hintLimit, 10*time.Millisecond)

	stop := hintIfSlow(true) // a read still going: the hint arrives while it is
	if !waitFor(lines, streamingText) {
		t.Fatal("no hint for a slow read")
	}
	stop()
	hintLimit = time.Hour // a fast read, stopped long before the limit
	hintIfSlow(true)()
	hintIfSlow(false)
	if rest := strings.Join(done(), "\n"); strings.Contains(rest, streamingText) {
		t.Errorf("the hint printed again:\n%s", rest)
	}
}

// import and push time their first archive read: delayed until the hint has printed, it
// prints, once.
func TestImportAndPushHintOnASlowFirstRead(t *testing.T) {
	root := t.TempDir()
	claudeDir, dest := filepath.Join(root, ".claude"), filepath.Join(root, "archive")
	writeFile(t, filepath.Join(claudeDir, "settings.json"), "{}")
	for _, d := range []string{filepath.Join(claudeDir, "projects"), filepath.Join(dest, "projects")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devNull.Close()
	hook(t, &hintLimit, 10*time.Millisecond)

	for _, c := range []struct {
		name string
		run  func([]string) error
		args []string
	}{
		{"import", cmdImport, []string{"--dry-run"}},
		{"push", cmdPush, []string{"--dry-run", "--no-html"}},
	} {
		lines, done := captureStderr(t)
		reached := make(chan bool, 1)
		hook(t, &beforeArchive, func(step string) {
			if step == "first read" {
				reached <- waitFor(lines, streamingText)
			}
		})
		stdout := os.Stdout
		os.Stdout = devNull
		_ = c.run(append([]string{"--claude-dir", claudeDir, "--archive", dest}, c.args...))
		os.Stdout = stdout
		rest := strings.Join(done(), "\n")

		select {
		case ok := <-reached:
			if !ok {
				t.Errorf("%s: no hint while its first archive read was slow:\n%s", c.name, rest)
			}
		default:
			t.Errorf("%s: its first archive read is not timed:\n%s", c.name, rest)
		}
		if strings.Contains(rest, streamingText) {
			t.Errorf("%s: the hint printed more than once:\n%s", c.name, rest)
		}
	}
}
