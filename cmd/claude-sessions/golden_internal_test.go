package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/golden"
	"github.com/ashiqfardus/claude-sessions-sync/internal/hostagent"
)

// captureStdout runs fn with os.Stdout swapped for a pipe, and returns what it printed.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	return captureFile(t, &os.Stdout, fn)
}

// captureFile runs fn with *f, os.Stdout or os.Stderr, swapped for a pipe, and returns
// what it printed there.
func captureFile(t *testing.T, f **os.File, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	was := *f
	*f = w
	defer func() { *f = was }()
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
		name, cmdName string
		cmd           func([]string) error
		flags         []string
	}{
		{"install", "install", cmdInstall, nil},
		{"uninstall", "uninstall", cmdUninstall, nil},
		{"install-json", "install", cmdInstall, []string{"--json"}},
	} {
		args := append([]string{"--claude-dir", root}, c.flags...)
		out, err := captureStdout(t, func() error { return c.cmd(args) })
		got := golden.Normalize(fmt.Sprintf("$ claude-sessions %s %s\nerr %v\n--- stdout\n%s", c.cmdName, strings.Join(args, " "), err, out), roots)
		// The OS's name.
		got = regexp.MustCompile(`Installing on \w+`).ReplaceAllString(got, "Installing on <OS>")
		// Windows names the binary claude-sessions.exe.
		got = regexp.MustCompile(`(<CLAUDE>/bin/claude-sessions)\.exe\b`).ReplaceAllString(got, "$1")
		golden.Check(t, c.name, got)
	}

	// The interval install used is saved, -1 for none, for the panel to show.
	sweepEvery := func() int {
		cfg, err := archive.LoadConfig(root)
		if err != nil {
			t.Fatal(err)
		}
		return cfg.SweepEvery
	}
	if got := sweepEvery(); got != 30 {
		t.Errorf("after install, sweepEvery = %d, want 30", got)
	}
	out, err := captureStdout(t, func() error {
		_, err := runInstall(quietStreams(), installOpts{ClaudeDir: root, NoSweep: true})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Errorf("runInstall printed to stdout: %q", out)
	}
	if got := sweepEvery(); got != -1 {
		t.Errorf("after install --no-sweep, sweepEvery = %d, want -1", got)
	}
}
