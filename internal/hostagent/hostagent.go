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

// Label is the launchd job label on macOS.
//
// Declared here rather than in install_darwin.go so that the plist it names can be
// generated and asserted on from any platform - see spec.go.
const Label = "com.github.ashiqfardus.claude-sessions-sync.sweep"

// Unit is the systemd user timer name on Linux.
const Unit = "claude-sessions-sync-sweep.timer"

// Job is the sweep for ONE Claude profile.
//
// The scheduler namespace is per machine, but a profile is not. When every install
// shared one job name, `install --claude-dir <scratch>` followed by `uninstall`
// silently replaced and then deleted the real profile's sweep - which is how the
// sweep on the machine this was developed on disappeared, found two weeks later.
// The default profile keeps the historical names so existing installs still match;
// any other profile gets its own names and passes --claude-dir, because a scheduler
// does not inherit CLAUDE_CONFIG_DIR from a shell profile.
type Job struct {
	Binary  string // may be empty when only querying or removing
	Profile string // the Claude root; "" means the default ~/.claude
}

// Status is what is currently registered on this machine.
type Status struct {
	Installed bool
	Detail    string // human-readable: schedule, last result, or why it is not running
	Mechanism string // "Task Scheduler", "launchd", "systemd", "cron"
	// Problem is set when the job is registered but will not archive anything:
	// disabled, stopped, failing, or running some other program.
	Problem string
}
