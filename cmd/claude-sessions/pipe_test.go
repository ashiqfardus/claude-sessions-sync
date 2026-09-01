package main_test

// Pins down the behaviour behind the CI failure that hit macOS and Linux but not
// Windows.
//
// `claude-sessions doctor | grep -q archive` failed there because grep exits at its
// first match and closes the pipe; on Unix the resulting EPIPE on stdout becomes
// SIGPIPE and kills the writer with 141, which `set -o pipefail` then reports.
// Windows has no SIGPIPE, so it could never reproduce locally.
//
// Dying on SIGPIPE is correct Unix behaviour - coreutils does the same, and
// `set -o pipefail; ls | head -1` returns 141 for them too - so the fix belonged in
// the CI script, not in this program. What these tests guarantee is the part that
// actually matters to a user: a plain pipeline works, and the output is right.

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestPlainPipelinesWork(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX shell semantics to exercise here")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no shell available")
	}

	f := newFixture(t)

	cases := []struct {
		name   string
		script string
		expect string
	}{
		{"doctor into head", "doctor --claude-dir " + f.claudeDir + " --archive " + f.archive + " | head -3", "claude root"},
		{"doctor into grep", "doctor --claude-dir " + f.claudeDir + " --archive " + f.archive + " | grep 'claude root'", "claude root"},
		{"ls into head", "ls --claude-dir " + f.claudeDir + " | head -2", "PROJECT"},
		{"search into wc", "search --claude-dir " + f.claudeDir + " login | wc -l", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := exec.Command("sh", "-c", binary+" "+tc.script).CombinedOutput()
			if err != nil {
				t.Fatalf("pipeline failed: %v\n%s", err, out)
			}
			if tc.expect != "" && !strings.Contains(string(out), tc.expect) {
				t.Errorf("expected %q in output, got:\n%s", tc.expect, out)
			}
		})
	}
}

// push --json must be parseable straight out of a pipe, which means stdout carries
// only the result and progress goes to stderr.
func TestPushJSONPipesCleanly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX shell semantics to exercise here")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no shell available")
	}

	f := newFixture(t)
	script := binary + " push --claude-dir " + f.claudeDir + " --archive " + f.archive + " --json 2>/dev/null"
	out, err := exec.Command("sh", "-c", script).Output()
	if err != nil {
		t.Fatalf("push --json failed: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(out)), "{") {
		t.Errorf("stdout should be JSON alone, got:\n%s", out)
	}
}
