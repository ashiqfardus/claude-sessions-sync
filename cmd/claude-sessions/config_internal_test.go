package main

import "testing"

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
