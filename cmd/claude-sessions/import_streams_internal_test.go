package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
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
