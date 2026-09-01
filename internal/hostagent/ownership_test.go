package hostagent

import "testing"

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
