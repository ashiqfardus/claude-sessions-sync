package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
