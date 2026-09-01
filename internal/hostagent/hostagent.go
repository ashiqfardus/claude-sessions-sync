// Package hostagent owns the per-OS scheduling that runs the periodic sweep, and
// reports on what is currently installed.
//
// The sweep exists to catch sessions that ended without the SessionEnd hook firing -
// a killed terminal, a crash, a machine put to sleep mid-session.
package hostagent

// Name is this tool's scheduled job.
//
// It is deliberately NOT the name the PowerShell predecessor uses. Sharing a name
// looked like a tidy way to "replace" it, and in practice meant uninstall deleting a
// scheduled task this program never created - which is exactly what happened, on a
// live machine, during testing. A tool must never remove automation it did not
// install. The predecessor is detected and reported instead, and only removed when
// the user explicitly asks.
const Name = "claude-sessions-sync"

// LegacyName is the task registered by the PowerShell implementation this tool
// replaces. It is only ever read, or removed on explicit request.
const LegacyName = "Claude Session Sync"

// Status is what is currently registered on this machine.
type Status struct {
	Installed bool
	Detail    string // human-readable: schedule, last result, or why it is not running
	Mechanism string // "Task Scheduler", "launchd", "systemd", "cron"
}
