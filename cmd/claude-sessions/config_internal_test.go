package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
)

// From Git Bash, `config map-folder /Users/me/SQL ...` arrives as the Git install's
// path: MSYS rewrites Unix-looking arguments before Windows programs see them.
func TestGitBashRewritten(t *testing.T) {
	for p, want := range map[string]bool{
		`C:/Program Files/Git/Users/me/SQL`: true,
		`C:\Program Files\Git\Users\me\SQL`: true,
		`c:/program files/git/home/me`:      true,
		`/Users/me/SQL`:                     false,
		`E:\SQL`:                            false,
		`C:\Program Files\Gitea\data`:       false,
		`/opt/Program Files/Git/x`:          false, // no drive: not what MSYS makes
	} {
		if got := gitBashRewritten(p); got != want {
			t.Errorf("gitBashRewritten(%q) = %v, want %v", p, got, want)
		}
	}
}

// runConfig prints on its own streams only, and its Lines are what it printed there.
func TestRunConfigRecordsItsLines(t *testing.T) {
	root := t.TempDir()
	var buf bytes.Buffer
	var res configResult
	out, err := captureStdout(t, func() (err error) {
		res, err = runConfig(streams{&buf, &buf}, configOpts{ClaudeDir: root, Action: "work", Args: []string{"off"}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Errorf("runConfig printed to stdout: %q", out)
	}
	if buf.String() != "Work folder backup is off\n" || !slices.Equal(res.Lines, []string{"Work folder backup is off"}) || res.View != nil {
		t.Errorf("stream %q, result %+v", buf.String(), res)
	}

	// No action is show, with the defaults resolved.
	res, err = runConfig(quietStreams(), configOpts{ClaudeDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if v := res.View; res.Action != "show" || v == nil || v.WorkOn || !v.ZipSessions || v.SweepEvery != 30 || v.Placement != "home" {
		t.Errorf("show = %+v, view %+v", res, res.View)
	}
}

// After the action, a bucket of a folder at the top of the disk, /json, is -json: only
// the double-dash --json is the flag there, as the docs and hints write it.
func TestPositionalAfterActionKeepsDashJSON(t *testing.T) {
	for _, c := range []struct{ in, want []string }{
		{[]string{"map", "-json", "/x"}, []string{"--", "map", "-json", "/x"}},
		{[]string{"keep", "-json"}, []string{"--", "keep", "-json"}},
		{[]string{"unkeep", "-json", "--json"}, []string{"--json", "--", "unkeep", "-json"}},
		{[]string{"keep", "web-app", "--json"}, []string{"--json", "--", "keep", "web-app"}},
		{[]string{"keep", "web-app", "--json=true"}, []string{"--json=true", "--", "keep", "web-app"}},
		{[]string{"--json", "keep", "web-app"}, []string{"--json", "--", "keep", "web-app"}},
	} {
		if got := positionalAfterAction(c.in); !slices.Equal(got, c.want) {
			t.Errorf("%q: %q, want %q", c.in, got, c.want)
		}
	}
}

// A remote that is not kept is answered as one, not as a folder that does not exist.
func TestUnkeepARemoteNotKeptSaysSo(t *testing.T) {
	claudeDir, _ := keepFixture(t)
	for _, rm := range []string{"git@github.com:example/none.git", "https://github.com/example/none", "ssh://git@host:22/o/r.git"} {
		_, err := captured(t, func() error { return cmdConfig([]string{"--claude-dir", claudeDir, "unkeep", rm}) })
		if err == nil || err.Error() != rm+" is not on the keep list" {
			t.Errorf("unkeep %s: %v; want it is not on the keep list", rm, err)
		}
	}
}

// E2E M7: set-destination given the home folder, or a folder of other files - a sync
// app's root - makes the archive a claude-sessions folder inside it, and says so. An
// archive folder, an empty one and a new one are taken as they are, and a destination
// saved before is never moved.
func TestSetDestinationGivesTheArchiveAFolderOfItsOwn(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	writeFile(t, filepath.Join(home, "notes.txt"), "mine")
	if err := os.MkdirAll(filepath.Join(home, "projects"), 0o755); err != nil { // a home the archive spilled into
		t.Fatal(err)
	}
	drive := filepath.Join(home, "My Drive")
	writeFile(t, filepath.Join(drive, "taxes.pdf"), "x")
	held := filepath.Join(home, "held")
	writeFile(t, filepath.Join(held, "manifest", "LAP-1.json"), "{}")
	writeFile(t, filepath.Join(held, "README.txt"), "x")
	empty := filepath.Join(home, "empty")
	writeFile(t, filepath.Join(empty, ".DS_Store"), "")
	for _, c := range []struct{ chosen, want string }{
		{home, filepath.Join(home, "claude-sessions")},
		{drive + string(filepath.Separator), filepath.Join(drive, "claude-sessions")},
		{held, held},
		{empty, empty},
		{filepath.Join(home, "new"), filepath.Join(home, "new")},
	} {
		var out bytes.Buffer
		if err := configAction(&out, root, []string{"set-destination", c.chosen}); err != nil {
			t.Fatalf("%s: %v", c.chosen, err)
		}
		cfg, err := archive.LoadConfig(root)
		if err != nil || cfg.Destination != c.want || !archive.IsRealDir(c.want) {
			t.Errorf("%s: saved %q (%v), want %q, made", c.chosen, cfg.Destination, err, c.want)
		}
		using := "Using " + c.want + ": a folder of its own inside the one you chose.\n"
		if said := strings.Contains(out.String(), using); said != (c.want != filepath.Clean(c.chosen)) {
			t.Errorf("%s: said %v:\n%s", c.chosen, said, out.String())
		}
	}
	if _, err := os.Stat(filepath.Join(home, "manifest")); err == nil {
		t.Error("the archive was made in the home folder")
	}

	// Saved before, by an older version: used where it is.
	if err := archive.SaveConfig(root, archive.Config{Destination: drive}); err != nil {
		t.Fatal(err)
	}
	if dest, _, err := archive.ResolveDestination(root, ""); err != nil || dest != drive {
		t.Errorf("a saved destination resolves to %q (%v)", dest, err)
	}
}
