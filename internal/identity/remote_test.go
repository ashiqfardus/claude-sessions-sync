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
		{"git@github.com:example/web-app.git", "https://github.com/example/web-app.git", true},
		{"https://github.com/example/web-app", "git@github.com:example/web-app", true},
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

// A quoted value or a trailing comment used to stay in the URL, so it never matched.
func TestRemoteReadsQuotedAndCommentedValues(t *testing.T) {
	for body, want := range map[string]string{
		"[remote \"origin\"]\n\turl = \"https://github.com/o/r.git\"\n":              "https://github.com/o/r.git",
		"[remote \"origin\"]\n\turl = git@github.com:o/r.git # mirror\n":             "git@github.com:o/r.git",
		"[remote \"origin\"]\n\turl = https://github.com/o/r.git ; old\n":            "https://github.com/o/r.git",
		"[remote \"origin\"]\n\turl = \"https://host/a#b.git\" # quoted hash kept\n": "https://host/a#b.git",
	} {
		dir := writeGitConfig(t, t.TempDir(), body)
		if got := Remote(dir); got != want {
			t.Errorf("Remote() = %q, want %q for:\n%s", got, want, body)
		}
	}
}

func TestSameRemoteEdgeCases(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		// A trailing slash after .git.
		{"https://github.com/o/r.git/", "git@github.com:o/r.git", true},
		// In scp form a numeric owner is not a port.
		{"git@github.com:1234/repo.git", "https://github.com/1234/repo", true},
		{"git@github.com:1234/repo.git", "https://github.com/repo", false},
	}
	for _, tc := range cases {
		if got := SameRemote(tc.a, tc.b); got != tc.want {
			t.Errorf("SameRemote(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// A token in a remote must never reach what NormalRemote gives, which the panel shows.
func TestNormalRemoteDropsCredentials(t *testing.T) {
	for in, want := range map[string]string{
		"https://user:tok@x@github.com/o/r.git":       "github.com/o/r",
		"https://user:tok/x@github.com/o/r.git":       "github.com/o/r",
		"https://github.com/o/r.git?access_token=tok": "github.com/o/r",
		"ssh://user:tok@github.com:22/o/r.git":        "github.com/o/r",
		"user:tok@x@github.com:o/r.git":               "github.com/o/r",
		"user:tok/x@github.com:o/r.git":               "github.com/o/r",
		"git@github.com:o/r@v2.git":                   "github.com/o/r@v2",
		"https://host/a#b.git":                        "host/a#b",
		"/srv/git/repo.git":                           "/srv/git/repo",
	} {
		if got := NormalRemote(in); got != want {
			t.Errorf("NormalRemote(%q) = %q, want %q", in, got, want)
		}
	}
	if !SameRemote("https://user:tok/x@github.com/o/r.git", "git@github.com:o/r") {
		t.Error("a remote with a token is not the same as without")
	}
}
