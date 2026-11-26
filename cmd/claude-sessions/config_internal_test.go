package main

import (
	"bytes"
	"slices"
	"testing"
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
