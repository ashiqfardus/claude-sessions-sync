package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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
