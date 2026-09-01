package hostagent

import (
	"strings"
	"testing"
)

// Deleting a scheduled task by name alone removed a live task belonging to the
// PowerShell predecessor during testing on a real machine. Ownership is now checked
// against the command the task runs, and "sync-claude-sessions.ps1" contains
// "claude-sessions" - the same substring trap that made doctor miscount archivers.
func TestOwnsAction(t *testing.T) {
	cases := []struct {
		action string
		want   bool
	}{
		{`"C:\Users\me\.claude\bin\claude-sessions.exe" push --quiet`, true},
		{`/home/me/.claude/bin/claude-sessions push --quiet`, true},
		{`powershell.exe -File "C:\Users\me\.claude\tools\sync-claude-sessions.ps1" -Quiet`, false},
		{`powershell.exe -File C:\tools\SYNC-CLAUDE-SESSIONS.PS1 -Quiet`, false},
		{`C:\Windows\System32\backup.exe /all`, false},
		{``, false},
	}
	for _, tc := range cases {
		if got := ownsAction(tc.action); got != tc.want {
			t.Errorf("ownsAction(%q) = %v, want %v", tc.action, got, tc.want)
		}
	}
}

// The two names must differ, or uninstall reaches a task this tool never created.
func TestTaskNameDoesNotCollideWithPredecessor(t *testing.T) {
	if Name == LegacyName {
		t.Fatalf("Name and LegacyName are both %q: uninstall would delete the predecessor's task", Name)
	}
}

// A freshly-registered task reports the sentinel date 30/11/1999 and result 267011.
// Printed verbatim it reads as a failure from the last century.
func TestNeverRunSentinel(t *testing.T) {
	cases := []struct {
		lastRun, lastResult string
		want                bool
	}{
		{"11/30/1999 12:00:00 AM", "267011", true},
		{"11/30/1999 12:00:00 AM", "0", true},
		{"", "267011", true},
		{"9/1/2026 1:31:40 PM", "0", false},
		{"9/1/2026 1:31:40 PM", "1", false},
	}
	for _, tc := range cases {
		if got := neverRun(tc.lastRun, tc.lastResult); got != tc.want {
			t.Errorf("neverRun(%q, %q) = %v, want %v", tc.lastRun, tc.lastResult, got, tc.want)
		}
	}
}

func TestDescribeResult(t *testing.T) {
	if got := describeResult("0"); got != "0 (success)" {
		t.Errorf("describeResult(0) = %q", got)
	}
	if got := describeResult("2147750687"); !strings.Contains(got, "already running") {
		t.Errorf("describeResult = %q", got)
	}
	// An unknown code passes through: a number the user can search for beats a guess.
	if got := describeResult("12345"); got != "12345" {
		t.Errorf("describeResult(12345) = %q", got)
	}
}
