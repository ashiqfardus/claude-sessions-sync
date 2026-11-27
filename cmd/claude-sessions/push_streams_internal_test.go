package main

import (
	"bytes"
	"encoding/json"
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

// progressFixture is LAP-1 with three conversations of web-app, one 40 days idle, one
// of which wrote to a work folder, notes. A push copies the three, then the folder,
// zips the idle one, and builds the three pages.
func progressFixture(t *testing.T) (claudeDir, dest string) {
	t.Helper()
	t.Setenv("CLAUDE_SESSIONS_MACHINE", "LAP-1")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_SESSIONS_TEMP_DIRS", filepath.Join(home, "no-temp")+string(os.PathListSeparator))
	claudeDir, dest = t.TempDir(), t.TempDir()
	note := filepath.Join(home, "notes", "n.md")
	writeFile(t, note, "a note\n")
	cwd, _ := json.Marshal(filepath.Join(home, "web-app"))
	bucket := filepath.Join(claudeDir, "projects", "-x-web-app")
	for i, id := range []string{"11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222", cleanID} {
		body := `{"type":"user","cwd":` + string(cwd) + `,"sessionId":"` + id + `","message":{"role":"user","content":"hello"}}` + "\n"
		if i == 0 {
			file, _ := json.Marshal(note)
			body += `{"type":"assistant","cwd":` + string(cwd) + `,"sessionId":"` + id + `","message":{"role":"assistant","content":[{"type":"tool_use","name":"Write","input":{"file_path":` + string(file) + `,"content":"x"}}]}}` + "\n"
		}
		writeFile(t, filepath.Join(bucket, id+".jsonl"), body)
		if id == cleanID {
			old := time.Now().Add(-40 * 24 * time.Hour)
			os.Chtimes(filepath.Join(bucket, id+".jsonl"), old, old)
		}
	}
	return claudeDir, dest
}

// E2E H1: a backup says how far it has got - copying the conversations, looking
// through them for work folders, copying those, compressing old conversations and
// building the pages - as \r-ended text on the error stream when it is not a terminal:
// each stage's start and its last count, in that order. A preview says it checks. None
// of it is in the log, in --json output, from a quiet run or on a terminal.
func TestPushSaysHowFarItHasGot(t *testing.T) {
	claudeDir, dest := progressFixture(t)
	var out, errs bytes.Buffer
	if _, err := runPush(streams{&out, &errs}, claudeDir, dest, pushOpts{DryRun: true, ScanHistory: true}); err != nil {
		t.Fatal(err)
	}
	inOrder := func(got string, want ...string) {
		t.Helper()
		at := 0
		for _, w := range want {
			i := strings.Index(got[at:], w)
			if i < 0 {
				t.Errorf("no %q after byte %d of:\n%q", w, at, got)
				return
			}
			at += i + len(w)
		}
	}
	inOrder(errs.String(), "checking conversations: 0/3\r", "checking conversations: 3/3\r", "looking for work folders: 0/3\r",
		"looking for work folders: 3/3\r", "checking work folders: 0/1\r", "checking work folders: 1/1\r")
	if strings.Contains(errs.String(), "copying") || strings.Contains(errs.String(), "compressing") || strings.Contains(errs.String(), "building") {
		t.Errorf("a preview said it copies, compresses or builds:\n%q", errs.String())
	}

	errs.Reset()
	if _, err := runPush(streams{&out, &errs}, claudeDir, dest, pushOpts{ScanHistory: true}); err != nil {
		t.Fatal(err)
	}
	inOrder(errs.String(), "copying conversations: 0/3\r", "copying conversations: 3/3\r", "looking for work folders: 0/3\r",
		"looking for work folders: 3/3\r", "copying work folders: 0/1\r", "copying work folders: 1/1\r",
		"compressing old conversations: 0/1\r", "compressing old conversations: 1/1\r",
		"building readable pages: 0/3\r", "building readable pages: 3/3\r")
	if log, err := os.ReadFile(filepath.Join(claudeDir, "session-sync.log")); err != nil || !strings.Contains(string(log), "PUSH: ") ||
		strings.Contains(string(log), "\r") || strings.Contains(string(log), "conversations: ") {
		t.Errorf("the log (%v) holds progress, or no PUSH line:\n%q", err, log)
	}

	// --json: the result alone on stdout, the progress on stderr.
	stdout, stderr := captureBoth(t, func() error { return cmdPush([]string{"--claude-dir", claudeDir, "--archive", dest, "--json"}) })
	var res pushResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil || res.Sessions != 3 {
		t.Errorf("--json stdout is not the result alone (%v):\n%q", err, stdout)
	}
	if !strings.Contains(stderr, "copying conversations: 3/3\r") {
		t.Errorf("--json: no progress on stderr:\n%q", stderr)
	}

	// Quiet, as the hooks and the sweep run it, and on a terminal: none.
	errs.Reset()
	if _, err := runPush(streams{&out, &errs}, claudeDir, dest, pushOpts{Quiet: true}); err != nil {
		t.Fatal(err)
	}
	hook(t, &isTerminal, func(io.Writer) bool { return true })
	if _, err := runPush(streams{&out, &errs}, claudeDir, dest, pushOpts{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(errs.String(), "\r") || !strings.Contains(errs.String(), "PUSH: ") {
		t.Errorf("progress from a quiet run or on a terminal, or no lines:\n%q", errs.String())
	}
}
