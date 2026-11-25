package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/golden"
	"github.com/ashiqfardus/claude-sessions-sync/internal/hostagent"
)

// captureStdout runs fn with os.Stdout swapped for a pipe, and returns what it printed.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = stdout }()
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	ferr := fn()
	w.Close()
	out := <-done
	r.Close()
	return out, ferr
}

// install and uninstall in-process, the sweep faked: a real one would register a job
// on this computer.
func TestGoldenInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	root := filepath.Join(t.TempDir(), ".claude")
	dest := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := archive.SaveConfig(root, archive.Config{Destination: dest}); err != nil {
		t.Fatal(err)
	}
	hook(t, &installSweep, func(hostagent.Job, int) (hostagent.Status, error) {
		return hostagent.Status{Installed: true, Mechanism: "<MECH>", Detail: "every 30 minutes"}, nil
	})
	hook(t, &uninstallSweep, func(hostagent.Job) (bool, error) { return true, nil })
	hook(t, &legacySweep, func() (string, bool) { return "", false })

	roots := map[string]string{"<CLAUDE>": root, "<ARCHIVE>": dest, "<HOME>": home}
	for _, c := range []struct {
		name string
		cmd  func([]string) error
	}{{"install", cmdInstall}, {"uninstall", cmdUninstall}} {
		out, err := captureStdout(t, func() error { return c.cmd([]string{"--claude-dir", root}) })
		got := golden.Normalize(fmt.Sprintf("$ claude-sessions %s --claude-dir %s\nerr %v\n--- stdout\n%s", c.name, root, err, out), roots)
		// The OS's name.
		got = regexp.MustCompile(`Installing on \w+`).ReplaceAllString(got, "Installing on <OS>")
		// Windows names the binary claude-sessions.exe.
		got = regexp.MustCompile(`(<CLAUDE>/bin/claude-sessions)\.exe\b`).ReplaceAllString(got, "$1")
		golden.Check(t, c.name, got)
	}
}
