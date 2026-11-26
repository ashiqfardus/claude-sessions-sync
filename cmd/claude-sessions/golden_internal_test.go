package main

import (
	"bytes"
	"errors"
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

// installEnv is a profile with its destination saved, HOME set to a temp folder and
// the sweep faked: a real one would register a job on this computer.
func installEnv(t *testing.T) (root, dest, home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	root = filepath.Join(t.TempDir(), ".claude")
	dest = t.TempDir()
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
	return root, dest, home
}

// install and uninstall in-process, the sweep faked.
func TestGoldenInstall(t *testing.T) {
	root, dest, home := installEnv(t)
	roots := map[string]string{"<CLAUDE>": root, "<ARCHIVE>": dest, "<HOME>": home}
	for _, c := range []struct {
		name, cmdName string
		cmd           func([]string) error
		flags         []string
	}{
		{"install", "install", cmdInstall, nil},
		{"uninstall", "uninstall", cmdUninstall, nil},
		{"install-json", "install", cmdInstall, []string{"--json"}},
		// Over the install just made, so its sweep goes.
		{"install-no-sweep", "install", cmdInstall, []string{"--no-sweep"}},
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
}

// install saves its sweep's interval for the panel, -1 for none. --no-sweep takes away
// a sweep installed earlier, or the setting would say none while one still ran; one it
// cannot take away keeps the interval saved before.
func TestInstallNoSweepRemovesAnEarlierSweep(t *testing.T) {
	root, _, _ := installEnv(t)
	sweeps := 0
	hook(t, &installSweep, func(hostagent.Job, int) (hostagent.Status, error) {
		sweeps++
		return hostagent.Status{Installed: true, Mechanism: "<MECH>", Detail: "every 60 minutes"}, nil
	})
	var registered bool
	var removeErr error
	hook(t, &uninstallSweep, func(hostagent.Job) (bool, error) { return registered, removeErr })
	sweepEvery := func() int {
		cfg, err := archive.LoadConfig(root)
		if err != nil {
			t.Fatal(err)
		}
		return cfg.SweepEvery
	}

	for _, c := range []struct {
		name       string
		registered bool
		err        error
		line       string
		every      int
	}{
		{"one registered", true, nil, "  sweep     removed (only after replies)\n", -1},
		{"none registered", false, nil, "  sweep     skipped (--no-sweep)\n", -1},
		{"removal fails", false, errors.New("access denied"), "  sweep     FAILED to remove the sweep installed earlier: access denied\n", 60},
	} {
		if _, err := runInstall(quietStreams(), installOpts{ClaudeDir: root, Every: 60}); err != nil {
			t.Fatal(err)
		}
		if got := sweepEvery(); got != 60 {
			t.Fatalf("%s: after install --every 60, sweepEvery = %d", c.name, got)
		}
		registered, removeErr = c.registered, c.err
		before := sweeps
		var buf bytes.Buffer
		var res installResult
		out, err := captureStdout(t, func() (err error) {
			res, err = runInstall(streams{&buf, &buf}, installOpts{ClaudeDir: root, NoSweep: true})
			return err
		})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if out != "" {
			t.Errorf("%s: runInstall printed to stdout: %q", c.name, out)
		}
		if !strings.Contains(buf.String(), "\n"+c.line) {
			t.Errorf("%s: want the line %q in\n%s", c.name, c.line, buf.String())
		}
		if sweeps != before {
			t.Errorf("%s: --no-sweep registered a sweep", c.name)
		}
		if got := sweepEvery(); got != c.every {
			t.Errorf("%s: sweepEvery = %d, want %d", c.name, got, c.every)
		}
		if (res.SweepFailed != "") != (c.err != nil) {
			t.Errorf("%s: sweepFailed = %q", c.name, res.SweepFailed)
		}
	}
}
