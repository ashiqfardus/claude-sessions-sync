// Package hostagent owns the per-OS scheduling that runs the periodic sweep, and
// reports on what is currently installed.
//
// The sweep exists to catch sessions that ended without the SessionEnd hook firing -
// a killed terminal, a crash, a machine put to sleep mid-session.
package hostagent

// Name is the scheduled job's identifier on every platform. It matches the task the
// PowerShell installer registers, so the Go installer replaces it rather than
// running a second copy alongside it.
const Name = "Claude Session Sync"

// Status is what is currently registered on this machine.
type Status struct {
	Installed bool
	Detail    string // human-readable: schedule, last result, or why it is not running
	Mechanism string // "Task Scheduler", "launchd", "systemd", "cron"
}
