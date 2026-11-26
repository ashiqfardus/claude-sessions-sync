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
	h := startHeartbeat(&out)
	time.Sleep(70 * time.Millisecond)
	h.reading("work folders", 0)
	time.Sleep(70 * time.Millisecond)
	h.reading("conversations", 3657)
	start := time.Now()
	steps := 0
	for time.Since(start) < 150*time.Millisecond {
		h.step()
		steps++
	}
	h.stop()
	elapsed := time.Since(start) + 140*time.Millisecond
	got := out.String()
	time.Sleep(60 * time.Millisecond)
	if out.String() != got {
		t.Errorf("the heartbeat wrote after stop:\n%s", out.String())
	}

	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if max := int(elapsed/heartbeatEvery) + 1; len(lines) < 3 || len(lines) > max {
		t.Fatalf("%d line(s) in %v at one per %v, %d steps:\n%s", len(lines), elapsed, heartbeatEvery, steps, got)
	}
	re := regexp.MustCompile(`^still reading the archive(: work folders|: \d+ of 3657 conversations)? \(\d+s\)$`)
	for _, l := range lines {
		if !re.MatchString(l) {
			t.Errorf("line %q is not a heartbeat", l)
		}
	}
	if !strings.Contains(got, "still reading the archive (0s)") || !strings.Contains(got, ": work folders (0s)") ||
		!regexp.MustCompile(`: [1-9]\d* of 3657 conversations`).MatchString(got) {
		t.Errorf("a stage is missing:\n%s", got)
	}
}

// Not on a terminal - a pipe, a file, the panel's buffer - import prints no heartbeat,
// so scripted output and the goldens stay as they were.
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

// import counts each archived conversation, loose or zipped, against all of them.
func TestImportHeartbeatCountsConversations(t *testing.T) {
	hook(t, &isTerminal, func(io.Writer) bool { return true })
	hook(t, &heartbeatEvery, time.Microsecond)
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
	if _, err := runImport(streams{&out, &errs}, importOpts{ClaudeDir: claudeDir, ArchiveDir: dest, SearchRoot: search, Depth: 1, DryRun: true}); err != nil {
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
