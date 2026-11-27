package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/convzip"
)

// syncBuffer is a bytes.Buffer the hint's timer may write while the test reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// import, pull and restore, which the panel runs from concurrent requests, print only
// on their own streams, the slow-read hint included, and return what they did.
func TestImportPullRestoreWriteOnlyToTheirStreams(t *testing.T) {
	t.Setenv("CLAUDE_SESSIONS_MACHINE", "LAP-2")
	root := t.TempDir()
	claudeDir, dest := filepath.Join(root, ".claude"), filepath.Join(root, "archive")
	writeFile(t, filepath.Join(claudeDir, "settings.json"), "{}")
	proj := filepath.Join(root, "web-app")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "11111111-1111-1111-1111-111111111111"
	bucket := claude.Slug(proj)
	cwd, _ := json.Marshal(proj)
	writeFile(t, filepath.Join(archive.ProjectsDir(dest), bucket, id+".jsonl"),
		`{"type":"user","cwd":`+string(cwd)+`,"sessionId":"`+id+`","message":{"role":"user","content":"hi"}}`+"\n")
	manifest, _ := json.Marshal(map[string]any{"schemaVersion": 1, "machine": "LAP-1",
		"projects": map[string]any{bucket: map[string]string{"path": proj, "leaf": "web-app"}}})
	writeFile(t, filepath.Join(dest, "manifest", "LAP-1.json"), string(manifest))

	// The hint is due before the first archive read ends, so it prints, on s.Err.
	hook(t, &hintLimit, time.Millisecond)
	var importErr syncBuffer
	hook(t, &beforeArchive, func(step string) {
		if step != "first read" {
			return
		}
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end) && !strings.Contains(importErr.String(), streamingText); {
			time.Sleep(time.Millisecond)
		}
	})

	var out bytes.Buffer
	var imp importResult
	var pulled []importRow
	var restored restoreResult
	stdout, stderr := captureBoth(t, func() error {
		var err error
		if imp, err = runImport(streams{&out, &importErr}, importOpts{ClaudeDir: claudeDir, ArchiveDir: dest, SearchRoot: root, Depth: 1}); err != nil {
			return err
		}
		if pulled, err = runPull(streams{&out, &out}, pullOpts{ClaudeDir: claudeDir, ArchiveDir: dest}); err != nil {
			return err
		}
		restored, err = runRestore(streams{&out, &out}, restoreCmdOpts{ClaudeDir: claudeDir,
			Source: filepath.Join(archive.ProjectsDir(dest), bucket), ProjectPath: filepath.Join(root, "elsewhere")})
		return err
	})
	if stdout != "" || stderr != "" {
		t.Errorf("printed outside their streams:\nstdout: %q\nstderr: %q", stdout, stderr)
	}
	if !strings.Contains(importErr.String(), streamingText) {
		t.Errorf("the slow-read hint is not on import's error stream: %q", importErr.String())
	}
	for _, want := range []string{"archive      ", "pull files by bucket name only", "filed 1 transcript(s)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the text has no %q:\n%s", want, out.String())
		}
	}
	if len(imp.Rows) != 1 || imp.Rows[0].Filed != 1 || imp.Rows[0].Machine != "LAP-1" {
		t.Errorf("import rows = %+v, want web-app's, filed once, from LAP-1", imp.Rows)
	}
	if len(pulled) != 1 || pulled[0].Skipped != 1 || pulled[0].How != string(byBucketName) {
		t.Errorf("pull rows = %+v, want web-app's, already present", pulled)
	}
	if restored.Copied != 1 {
		t.Errorf("restore = %+v, want one filed", restored)
	}
	// The panel encodes these results itself: an empty list is [], never null.
	for name, v := range map[string]any{"import": imp, "pull": pulled} {
		if b, _ := json.Marshal(v); bytes.Contains(b, []byte("null")) {
			t.Errorf("%s's result encodes a null: %s", name, b)
		}
	}
}

// captureBoth runs fn with os.Stdout and os.Stderr each swapped for a pipe.
func captureBoth(t *testing.T, fn func() error) (stdout, stderr string) {
	t.Helper()
	var err error
	stderr, _ = captureFile(t, &os.Stderr, func() error {
		stdout, err = captureFile(t, &os.Stdout, fn)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return stdout, stderr
}

// ~/.claude.json can give a memory-only bucket C:/x where a transcript says C:\x: one
// folder, compared from any computer.
func TestSamePathIgnoresSlashDirection(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{`C:/Users/you/dev/web-app`, `C:\Users\you\dev\web-app`, true},
		{`c:\users\you\dev\web-app\`, `C:/Users/you/dev/web-app`, true},
		{`C:\Users\you\dev\web-app`, `C:\Users\you\dev\api`, false},
		{"/Users/you/dev/web-app", "/Users/you/dev/web-app/", true},
	} {
		if got := samePath(c.a, c.b); got != c.want {
			t.Errorf("samePath(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// On a terminal, the heartbeat says how far the read has got at most once per
// interval, however often it steps, and nothing once stopped.
func TestHeartbeatSaysHowFarAtMostOncePerInterval(t *testing.T) {
	hook(t, &isTerminal, func(io.Writer) bool { return true })
	hook(t, &heartbeatEvery, 25*time.Millisecond)
	var out syncBuffer
	start := time.Now()
	h := startHeartbeat(&out)
	waitForText(t, &out, "still reading the archive (")
	h.reading("work folders", 0)
	waitForText(t, &out, ": work folders (")
	h.reading("conversations", 3657)
	steps := 0
	for spin := time.Now(); time.Since(spin) < 100*time.Millisecond || !strings.Contains(out.String(), " of 3657 conversations"); steps++ {
		h.step()
		if time.Since(spin) > 5*time.Second {
			break
		}
	}
	h.stop()
	elapsed := time.Since(start)
	got := out.String()
	time.Sleep(60 * time.Millisecond)
	if out.String() != got {
		t.Errorf("the heartbeat wrote after stop:\n%s", out.String())
	}

	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if max := int(elapsed / heartbeatEvery); len(lines) < 3 || len(lines) > max {
		t.Fatalf("%d line(s) in %v at one per %v, %d steps:\n%s", len(lines), elapsed, heartbeatEvery, steps, got)
	}
	re := regexp.MustCompile(`^still reading the archive(: work folders|: \d+ of 3657 conversations)? \(\d+s\)$`)
	for _, l := range lines {
		if !re.MatchString(l) {
			t.Errorf("line %q is not a heartbeat", l)
		}
	}
	if !regexp.MustCompile(`: [1-9]\d* of 3657 conversations`).MatchString(got) {
		t.Errorf("no count past 0:\n%s", got)
	}
}

// waitForText waits up to 5 seconds for w to hold want.
func waitForText(t *testing.T, w *syncBuffer, want string) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); !strings.Contains(w.String(), want); time.Sleep(time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("no %q in:\n%s", want, w.String())
		}
	}
}

// Not on a terminal - a pipe, a file, the panel's buffer - import prints no heartbeat
// line: its progress there is \r-ended text, which the panel shows in place.
func TestHeartbeatOnlyOnATerminal(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "stderr.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	for name, x := range map[string]io.Writer{"file": f, "pipe": w, "buffer": &bytes.Buffer{}} {
		if isTerminal(x) {
			t.Errorf("a %s counts as a terminal", name)
		}
	}

	hook(t, &heartbeatEvery, time.Microsecond)
	claudeDir, dest, search := heartbeatArchive(t, 30)
	var out bytes.Buffer
	if _, err := runImport(streams{&out, f}, importOpts{ClaudeDir: claudeDir, ArchiveDir: dest, SearchRoot: search, Depth: 1, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(f.Name()); strings.Contains(string(b)+out.String(), "still reading") {
		t.Errorf("a heartbeat with no terminal:\n%s%s", b, out.String())
	}
}

// import counts each archived conversation, loose or zipped, against all of them. The
// import is held inside the count, at its first zip, until a line is due.
func TestImportHeartbeatCountsConversations(t *testing.T) {
	hook(t, &isTerminal, func(io.Writer) bool { return true })
	hook(t, &heartbeatEvery, time.Millisecond)
	claudeDir, dest, search := heartbeatArchive(t, 40)
	buckets, _ := archive.BucketNames(dest)
	for i := 0; i < 5; i++ { // zipped ones count once each, as loose ones do
		if _, err := convzip.Zip(filepath.Join(archive.ProjectsDir(dest), buckets[0]), fmt.Sprintf("11111111-1111-1111-1111-%012d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if len(convzip.Find(filepath.Join(archive.ProjectsDir(dest), buckets[0]))) != 5 {
		t.Fatal("fixture: not zipped")
	}
	var out bytes.Buffer
	var errs syncBuffer
	realM := materialize
	hook(t, &materialize, func(bucketDir, id, tmp string) (string, func(), error) {
		waitForText(t, &errs, " of 40 conversations (")
		return realM(bucketDir, id, tmp)
	})
	if _, err := runImport(streams{&out, &errs}, importOpts{ClaudeDir: claudeDir, ArchiveDir: dest, SearchRoot: search, Depth: 1}); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`still reading the archive: \d+ of 40 conversations \(\d+s\)\n`).MatchString(errs.String()) ||
		regexp.MustCompile(`: (4[1-9]|[5-9]\d|\d{3,}) of 40`).MatchString(errs.String()) {
		t.Errorf("no count of 40 conversations, or one past it:\n%.2000s", errs.String())
	}
	if strings.Contains(out.String(), "still reading") {
		t.Errorf("a heartbeat on the output:\n%s", out.String())
	}
}

// heartbeatArchive is an archive of n conversations of one project, here at search.
func heartbeatArchive(t *testing.T, n int) (claudeDir, dest, search string) {
	t.Helper()
	t.Setenv("CLAUDE_SESSIONS_MACHINE", "LAP-2")
	root := t.TempDir()
	claudeDir, dest, search = filepath.Join(root, ".claude"), filepath.Join(root, "archive"), filepath.Join(root, "dev")
	proj := filepath.Join(search, "web-app")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	bucket := claude.Slug(proj)
	cwd, _ := json.Marshal(proj)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("11111111-1111-1111-1111-%012d", i)
		writeFile(t, filepath.Join(archive.ProjectsDir(dest), bucket, id+".jsonl"),
			`{"type":"user","cwd":`+string(cwd)+`,"sessionId":"`+id+`","message":{"role":"user","content":"hi"}}`+"\n")
	}
	manifest, _ := json.Marshal(map[string]any{"schemaVersion": 1, "machine": "LAP-1",
		"projects": map[string]any{bucket: map[string]string{"path": proj, "leaf": "web-app"}}})
	writeFile(t, filepath.Join(dest, "manifest", "LAP-1.json"), string(manifest))
	return claudeDir, dest, search
}

// A dry run opens no conversation's zip and lists no conversation's folder: on a drive
// that downloads each file as it is opened, those reads were most of a dry run's time,
// and nothing it prints comes from them. A real run still reads both, and files what
// the dry run said it would.
func TestImportDryRunOpensNoConversation(t *testing.T) {
	claudeDir, dest, search := heartbeatArchive(t, 6)
	buckets, _ := archive.BucketNames(dest)
	dir := filepath.Join(archive.ProjectsDir(dest), buckets[0])
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("11111111-1111-1111-1111-%012d", i)
		writeFile(t, filepath.Join(dir, id, "subagents", "agent-1.jsonl"), "{}\n")
		if i < 3 {
			if _, err := convzip.Zip(dir, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	indexes, unpacked := zipReads(t)
	var listed []string
	realList := listSessionFiles
	hook(t, &listSessionFiles, func(d string) []string {
		listed = append(listed, d)
		return realList(d)
	})
	run := func(dry bool) string {
		var out bytes.Buffer
		if _, err := runImport(streams{&out, &out}, importOpts{ClaudeDir: claudeDir, ArchiveDir: dest, SearchRoot: search, Depth: 1, DryRun: dry}); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}

	dry := run(true)
	if len(*indexes)+len(*unpacked)+len(listed) > 0 {
		t.Errorf("a dry run read conversations: zip indexes %v, unpacked %v, folders listed %v", *indexes, *unpacked, listed)
	}
	if !strings.Contains(dry, ": 6 filed, 0 already present") || !strings.Contains(dry, "6 transcript(s) filed.") {
		t.Errorf("the dry run's counts changed:\n%s", dry)
	}

	real := run(false)
	if len(*indexes) < 3 || len(*unpacked) < 3 || len(listed) < 6 {
		t.Errorf("a real run read less: zip indexes %v, unpacked %v, folders listed %v", *indexes, *unpacked, listed)
	}
	if !strings.Contains(real, ": 6 filed, 0 already present") {
		t.Errorf("the real run did not file what the dry run said:\n%s", real)
	}
	here := filepath.Join(claude.ProjectsDir(claudeDir), filepath.Base(dir))
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("11111111-1111-1111-1111-%012d", i)
		for _, rel := range []string{id + ".jsonl", filepath.Join(id, "subagents", "agent-1.jsonl")} {
			if _, err := os.Stat(filepath.Join(here, rel)); err != nil {
				t.Errorf("the real run did not file %s: %v", rel, err)
			}
		}
	}
}
