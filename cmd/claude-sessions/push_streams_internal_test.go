package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hintSignal closes hint when the streaming hint is written through it, and panics on a
// second one.
type hintSignal struct {
	w    io.Writer
	hint chan struct{}
}

func (h hintSignal) Write(p []byte) (int, error) {
	if strings.Contains(string(p), streamingAdvice) {
		close(h.hint)
	}
	return h.w.Write(p)
}

// push and render, run by the panel, print only to the streams they are given: every
// log line, the --scan-history warning and the streaming hint on s.Err, the summary on
// s.Out. Run with -race.
func TestRunPushProgressGoesToItsStream(t *testing.T) {
	t.Setenv("CLAUDE_SESSIONS_MACHINE", "LAP-1")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	claudeDir, dest := t.TempDir(), t.TempDir()
	const id = "11111111-1111-1111-1111-111111111111"
	writeFile(t, filepath.Join(claudeDir, "projects", "-x-web-app", id+".jsonl"),
		`{"type":"user","cwd":"/x/web-app","sessionId":"`+id+`","message":{"role":"user","content":"hello there"}}`+"\n")

	// The first archive read is held until the hint has been written.
	hint := make(chan struct{})
	hook(t, &hintLimit, time.Millisecond)
	hook(t, &beforeArchive, func(step string) {
		if step == "first read" {
			select {
			case <-hint:
			case <-time.After(10 * time.Second):
			}
		}
	})

	var out, errs bytes.Buffer
	var res pushResult
	var stdout string
	stderr, err := captureFile(t, &os.Stderr, func() (err error) {
		stdout, err = captureStdout(t, func() (err error) {
			res, err = runPush(streams{&out, hintSignal{&errs, hint}}, claudeDir, dest, pushOpts{ScanHistory: true})
			return err
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "" || stderr != "" {
		t.Errorf("runPush wrote to the process: stdout %q, stderr %q", stdout, stderr)
	}
	if res.Sessions != 1 || res.Archive != dest {
		t.Errorf("result = %+v, want the one session pushed to %s", res, dest)
	}
	for _, want := range []string{"PUSH: ", "HTML: ", "WARNING: --scan-history", streamingAdvice} {
		if !strings.Contains(errs.String(), want) {
			t.Errorf("s.Err lacks %q:\n%s", want, errs.String())
		}
	}
	if !strings.Contains(out.String(), "1 session(s) across 1 project(s) -> "+dest) {
		t.Errorf("the summary is not on s.Out:\n%s", out.String())
	}

	out.Reset()
	var rendered int
	stdout, err = captureStdout(t, func() error {
		r, err := runRender(streams{&out, &errs}, renderOpts{ClaudeDir: claudeDir, ArchiveDir: dest, Force: true})
		rendered = r.Rendered
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "" {
		t.Errorf("runRender wrote to stdout: %q", stdout)
	}
	if rendered != 1 || !strings.Contains(out.String(), "Rendered 1, 0 already up to date") {
		t.Errorf("render: %d rendered, s.Out:\n%s", rendered, out.String())
	}
}

// The hint's stop waits for a hint being written, so none is written after the command
// that started it returns.
func TestHintStopWaitsForItsWrite(t *testing.T) {
	hook(t, &hintLimit, time.Millisecond)
	entered, release := make(chan struct{}), make(chan struct{})
	stop := hintIfSlowTo(blockingWriter{entered, release})
	<-entered
	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("stop returned while the hint was being written")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-stopped
}

type blockingWriter struct{ entered, release chan struct{} }

func (b blockingWriter) Write(p []byte) (int, error) {
	close(b.entered)
	<-b.release
	return len(p), nil
}
