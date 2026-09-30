package identity

import (
	"runtime"
	"testing"
)

// Paths from the archive are untrusted. On Windows, merely stat-ing \\host\share
// connects to that host over SMB and hands it the user's NTLM hash - and push used to
// do that on every run for a cwd planted in a transcript.
func TestLocalRejectsNetworkAndDevicePaths(t *testing.T) {
	never := []string{``, `relative\dir`, `relative/dir`}
	if runtime.GOOS == "windows" {
		never = append(never, `\\evil.example\share\app`, `//evil.example/share/app`,
			`\\?\C:\work\app`, `\\.\pipe\x`, `\work\app`)
		if !Local(`C:\work\app`) {
			t.Error(`C:\work\app should be local`)
		}
	} else if !Local("/home/me/app") {
		t.Error("/home/me/app should be local")
	}
	for _, p := range never {
		if Local(p) {
			t.Errorf("Local(%q) = true", p)
		}
		if Remote(p) != "" || RepoRoot(p) != "" {
			t.Errorf("%q was looked at", p)
		}
	}
}
