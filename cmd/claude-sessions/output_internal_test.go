package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/convzip"
)

// The panel runs command code from concurrent requests, and every command loads the
// pinned machine name through resolveRoot. Run with -race.
func TestMachineNameIsRaceFree(t *testing.T) {
	t.Setenv("CLAUDE_SESSIONS_MACHINE", "") // read the saved name, not the variable
	machineMu.RLock()
	name, former := savedMachine, formerMachines
	machineMu.RUnlock()
	t.Cleanup(func() { setSavedMachine(name, former) })

	dir := t.TempDir()
	if err := archive.SaveConfig(dir, archive.Config{Machine: "LAP-1", FormerMachines: []string{"LAP-2"}}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if _, _, err := resolveRoot(dir); err != nil {
					t.Error(err)
					return
				}
				if got := machineName(); got != "LAP-1" {
					t.Errorf("machineName() = %q, want LAP-1", got)
					return
				}
				if !isMe("LAP-2") {
					t.Error("isMe(LAP-2) = false: the former name was lost")
					return
				}
			}
		}()
	}
	wg.Wait()
}

// A command the panel calls must print nothing of its own: two requests at once would
// interleave their output on the server's terminal.
func TestRunLsWritesOnlyToItsStreams(t *testing.T) {
	claudeDir := t.TempDir()
	const id = "11111111-1111-1111-1111-111111111111"
	writeFile(t, filepath.Join(claudeDir, "projects", "-x-web-app", id+".jsonl"),
		`{"type":"user","cwd":"/x/web-app","sessionId":"`+id+`","message":{"role":"user","content":"hello there"}}`+"\n")

	var buf bytes.Buffer
	var rows []sessionRow
	out, err := captureStdout(t, func() (err error) {
		rows, err = runLs(streams{&buf, &buf}, lsOpts{ClaudeDir: claudeDir})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Errorf("runLs printed to stdout: %q", out)
	}
	if len(rows) != 1 || rows[0].SessionID != id {
		t.Errorf("rows = %+v, want the one session", rows)
	}
	if got := buf.String(); !strings.Contains(got, id) || !strings.Contains(got, "1 session(s).") {
		t.Errorf("the listing is not in its stream:\n%s", got)
	}
}

// A zip that cannot be read is noted on the command's own error stream, never on the
// process's: the panel's queries pass streams that drop it.
func TestUnreadableZipIsNotedOnItsStream(t *testing.T) {
	claudeDir, dest := t.TempDir(), t.TempDir()
	const id = "11111111-1111-1111-1111-111111111111"
	writeFile(t, convzip.Path(filepath.Join(archive.ProjectsDir(dest), "-x-web-app"), id), "not a zip")

	for name, run := range map[string]func(streams) error{
		"ls": func(s streams) error {
			_, err := runLs(s, lsOpts{ClaudeDir: claudeDir, ArchiveDir: dest})
			return err
		},
		"search": func(s streams) error {
			_, err := runSearch(s, searchOpts{ClaudeDir: claudeDir, ArchiveDir: dest, Query: "x"})
			return err
		},
		"stats": func(s streams) error {
			_, err := runStats(s, statsOpts{ClaudeDir: claudeDir, ArchiveDir: dest})
			return err
		},
	} {
		var out, errs bytes.Buffer
		var stdout string
		stderr, err := captureFile(t, &os.Stderr, func() (err error) {
			stdout, err = captureStdout(t, func() error { return run(streams{&out, &errs}) })
			return err
		})
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if stdout != "" || stderr != "" {
			t.Errorf("%s wrote to the process: stdout %q, stderr %q", name, stdout, stderr)
		}
		if !strings.Contains(errs.String(), "skipped "+convzip.Path(filepath.Join(archive.ProjectsDir(dest), "-x-web-app"), id)) {
			t.Errorf("%s: the skipped note is not on s.Err: %q (s.Out %q)", name, errs.String(), out.String())
		}
	}
}

// Progress text is held back to one write every progressEvery, so a script reading
// stdout first never finds stderr's pipe full. A stage's first text and its last count
// are always written: the last on the step that completes the count, or, for a stage
// that ends short of it, when the next stage starts or the heartbeat stops.
func TestProgressIsThrottled(t *testing.T) {
	var buf bytes.Buffer
	hook(t, &progressEvery, time.Hour)
	h := startProgress(&buf)
	h.reading("copying conversations", 3657)
	for i := 0; i < 3657; i++ {
		h.step()
	}
	h.reading("copying work folders", 10)
	h.step()
	h.step()
	h.reading("building readable pages", 0)
	h.reading("compressing old conversations", 5)
	h.step()
	h.stop()
	h.step()
	h.reading("after stop", 1)
	want := "copying conversations: 0/3657\rcopying conversations: 3657/3657\r" +
		"copying work folders: 0/10\rcopying work folders: 2/10\r" +
		"building readable pages\r" +
		"compressing old conversations: 0/5\rcompressing old conversations: 1/5\r"
	if buf.String() != want {
		t.Errorf("got %q\nwant %q", buf.String(), want)
	}

	// Once progressEvery has passed, a step is written.
	buf.Reset()
	h = startProgress(&buf)
	h.reading("copying conversations", 3)
	h.step()
	hook(t, &progressEvery, 0) // passed, without waiting on the clock
	h.step()
	h.stop()
	if want := "copying conversations: 0/3\rcopying conversations: 2/3\r"; buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}

	// To a pipe or a file, however much time passes: each stage's first text and its
	// last count, a few dozen bytes a stage, which no pipe fills with, each a line: a
	// script that reads stderr with stdout found the next line glued on after a \r.
	f, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h = startProgress(f)
	h.reading("reading conversations", 3657)
	for i := 0; i < 3657; i++ {
		h.step()
	}
	h.reading("reading work folders", 10)
	h.step()
	h.stop()
	got, _ := os.ReadFile(f.Name())
	if want := "reading conversations: 0/3657\nreading conversations: 3657/3657\nreading work folders: 0/10\nreading work folders: 1/10\n"; string(got) != want {
		t.Errorf("to a file %q, want %q", got, want)
	}

	// On a terminal push's progress writes nothing: its own lines are what it shows.
	buf.Reset()
	hook(t, &isTerminal, func(io.Writer) bool { return true })
	h = startProgress(&buf)
	h.reading("copying conversations", 1)
	h.step()
	h.stop()
	if buf.Len() != 0 || h.on() {
		t.Errorf("on a terminal: %q", buf.String())
	}
}

// A stage that reads no archive is named in full, on a terminal too: import's "reading "
// is for its reads of the archive alone.
func TestHeartbeatCheckingStage(t *testing.T) {
	var buf bytes.Buffer
	hook(t, &progressEvery, time.Hour)
	h := startHeartbeat(&buf)
	h.checking("checking file paths in conversations", 2)
	h.step()
	h.step()
	h.stop()
	if want := "checking file paths in conversations: 0/2\rchecking file paths in conversations: 2/2\r"; buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}

	// A terminal's line, said without the ticker that says it every heartbeatEvery.
	buf.Reset()
	h = &heartbeat{w: &buf, start: time.Now(), done: make(chan struct{})}
	h.checking("checking file paths in conversations", 3)
	h.step()
	h.say()
	h.stop()
	if !regexp.MustCompile(`^still checking file paths in conversations: 1 of 3 \(\d+s\)\n$`).MatchString(buf.String()) {
		t.Errorf("on a terminal: %q", buf.String())
	}
}
