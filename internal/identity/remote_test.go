package identity

import (
	"os"
	"path/filepath"
	"testing"
)

func writeGitConfig(t *testing.T, dir, body string) string {
	t.Helper()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRemoteReadsOrigin(t *testing.T) {
	dir := writeGitConfig(t, t.TempDir(), `[core]
	repositoryformatversion = 0
[remote "upstream"]
	url = https://github.com/someone/else.git
[remote "origin"]
	url = https://github.com/example/api.git
	fetch = +refs/heads/*:refs/remotes/origin/*
[branch "main"]
	remote = origin
`)
	if got := Remote(dir); got != "https://github.com/example/api.git" {
		t.Errorf("Remote() = %q", got)
	}
}

// The whole reason this package parses instead of shelling out: a repository's own
// config can tell git to execute things. Reading the file can never do that, so a
// hostile config is inert - we still just get the URL, or nothing.
func TestRemoteIgnoresExecutableConfigKeys(t *testing.T) {
	dir := writeGitConfig(t, t.TempDir(), `[core]
	fsmonitor = "/bin/sh -c 'touch /tmp/pwned'"
	pager = "/bin/sh -c 'touch /tmp/pwned'"
	sshCommand = "/bin/sh -c 'touch /tmp/pwned'"
[alias]
	remote = "!sh -c 'touch /tmp/pwned'"
[remote "origin"]
	url = git@github.com:example/api.git
`)
	if got := Remote(dir); got != "git@github.com:example/api.git" {
		t.Errorf("Remote() = %q", got)
	}
}

func TestRemoteMissing(t *testing.T) {
	if got := Remote(t.TempDir()); got != "" {
		t.Errorf("a folder with no .git should yield %q, got %q", "", got)
	}
	dir := writeGitConfig(t, t.TempDir(), "[core]\n\tbare = false\n")
	if got := Remote(dir); got != "" {
		t.Errorf("a repo with no origin should yield %q, got %q", "", got)
	}
}

func TestSameRemote(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"git@github.com:savoyit/airos-frontend.git", "https://github.com/savoyit/airos-frontend.git", true},
		{"https://github.com/savoyit/airos-frontend", "git@github.com:savoyit/airos-frontend", true},
		{"https://user:token@github.com/o/r.git", "git@github.com:o/r", true},
		{"HTTPS://GitHub.com/O/R.git", "git@github.com:o/r", true},
		{"ssh://git@github.com:22/o/r.git", "https://github.com/o/r", true},
		{"git@github.com:o/r.git", "git@github.com:o/other.git", false},
		{"git@gitlab.com:o/r.git", "git@github.com:o/r.git", false},
		// An unknown identity is not evidence of anything.
		{"", "", false},
		{"", "git@github.com:o/r.git", false},
	}
	for _, tc := range cases {
		if got := SameRemote(tc.a, tc.b); got != tc.want {
			t.Errorf("SameRemote(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
