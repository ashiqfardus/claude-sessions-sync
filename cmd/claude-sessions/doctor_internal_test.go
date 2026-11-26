package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
)

func writeLog(t *testing.T, lines ...string) string {
	t.Helper()
	root := t.TempDir()
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(root, "session-sync.log"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// A sweep refused by macOS privacy settings fails while the terminal - where doctor
// runs - can still read the archive, so doctor has to learn it from the log (#6).
func TestDeniedSincePushFindsAPrivacyRefusal(t *testing.T) {
	root := writeLog(t,
		"2026-11-20 09:50:18  PUSH: 4 copied, 2668 unchanged, 161 session(s), 7 project(s) -> /a",
		"2026-11-20 10:20:42  SKIP: open /Users/me/Library/CloudStorage/GoogleDrive-me/My Drive/claude-sessions: operation not permitted",
	)
	line, ok := deniedSincePush(root)
	if !ok || !strings.Contains(line, "operation not permitted") {
		t.Fatalf("deniedSincePush = %q, %v; want the refused line", line, ok)
	}
}

// A refusal that a later sweep got past is history, not a problem.
func TestDeniedSincePushIgnoresARefusalBeforeTheLastPush(t *testing.T) {
	root := writeLog(t,
		"2026-11-20 10:20:42  SKIP: open /a: operation not permitted",
		"2026-11-20 10:50:49  PUSH: 1 copied, 2689 unchanged, 161 session(s), 7 project(s) -> /a"+sweepMark,
	)
	if line, ok := deniedSincePush(root); ok {
		t.Fatalf("deniedSincePush = %q; want nothing after a successful push", line)
	}
}

// A release build says "0.7.0" and a `go install` build says "v0.7.0": one release,
// so doctor must not warn. A different release still warns.
func TestOtherVersionsIgnoresALeadingV(t *testing.T) {
	dest := t.TempDir()
	t.Setenv("CLAUDE_SESSIONS_MACHINE", "LAP-1")
	old := version
	t.Cleanup(func() { version = old })
	for _, s := range []archive.Shard{
		{Machine: "LAP-1", ToolVersion: "0.7.0"},
		{Machine: "LAP-2", ToolVersion: "v0.7.0"},
		{Machine: "LAP-3", ToolVersion: "0.7.0"},
		{Machine: "LAP-4", ToolVersion: "0.7.1"},
	} {
		writeShard(t, dest, s)
	}
	for _, v := range []string{"0.7.0", "v0.7.0"} {
		version = v
		if got, want := otherVersions(dest), []string{"LAP-4 (0.7.1)"}; !reflect.DeepEqual(got, want) {
			t.Errorf("version %s: otherVersions = %v, want %v", v, got, want)
		}
	}
}
