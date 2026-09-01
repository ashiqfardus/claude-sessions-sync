package hostagent

import (
	"fmt"
	"strconv"
	"strings"
)

// This file holds the exact content and arguments each platform's scheduler needs,
// with no OS-specific build tag and no system calls.
//
// It exists so that the macOS and Linux scheduling can be tested from any machine,
// including the Windows one this is developed on. Previously the plist and the
// systemd units were built inline inside //go:build darwin and //go:build linux
// files, which meant they could not be compiled - let alone asserted on - anywhere
// else, and "untested on macOS and Linux" was a permanent caveat rather than a
// fixable gap.
//
// What still cannot be checked this way is whether launchd or systemd accept and run
// what we generate. That needs the real OS, and CI does it.

// LaunchdPlist is the macOS user agent definition.
func LaunchdPlist(label, binary string, everyMinutes int) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + xmlEscape(label) + `</string>
  <key>ProgramArguments</key>
  <array>
    <string>` + xmlEscape(binary) + `</string>
    <string>push</string>
    <string>--quiet</string>
  </array>
  <key>StartInterval</key><integer>` + strconv.Itoa(everyMinutes*60) + `</integer>
  <key>RunAtLoad</key><false/>
  <key>ProcessType</key><string>Background</string>
</dict>
</plist>
`
}

// SystemdService is the oneshot unit that runs a push.
func SystemdService(binary string) string {
	return "[Unit]\nDescription=Archive Claude Code sessions\n\n" +
		"[Service]\nType=oneshot\n" +
		"ExecStart=" + binary + " push --quiet\n"
}

// SystemdTimer schedules the service.
//
// Persistent=true matters: without it a machine that was asleep at the scheduled
// moment simply skips that run, which for a backup is the wrong answer.
func SystemdTimer(everyMinutes int) string {
	n := strconv.Itoa(everyMinutes)
	return "[Unit]\nDescription=Archive Claude Code sessions every " + n + " minutes\n\n" +
		"[Timer]\nOnBootSec=5min\nOnUnitActiveSec=" + n + "min\n" +
		"Persistent=true\n\n[Install]\nWantedBy=timers.target\n"
}

// CronLine is the fallback for systems without systemd.
func CronLine(binary string, everyMinutes int) string {
	return fmt.Sprintf("*/%d * * * * %s push --quiet", everyMinutes, binary)
}

// SchtasksCreateArgs is the Windows Task Scheduler invocation.
//
// The command is one quoted string because the binary path routinely contains
// spaces, and /SC MINUTE /MO n repeats indefinitely - the PowerShell predecessor
// could not express that and needed a daily trigger with a repetition window.
func SchtasksCreateArgs(name, binary string, everyMinutes int) []string {
	return []string{
		"/Create",
		"/TN", name,
		"/TR", fmt.Sprintf(`"%s" push --quiet`, binary),
		"/SC", "MINUTE",
		"/MO", strconv.Itoa(everyMinutes),
		"/F",
	}
}

// normalMinutes applies the shared default so every platform agrees.
func normalMinutes(everyMinutes int) int {
	if everyMinutes <= 0 {
		return 30
	}
	return everyMinutes
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// ownsAction reports whether a scheduled task's command line is this tool's binary.
//
// "sync-claude-sessions.ps1" contains "claude-sessions", so the PowerShell script has
// to be excluded explicitly - the same substring trap that made doctor report two
// archivers when only one was installed, and the reason uninstall once deleted a task
// this program never registered.
//
// Kept here, with no build tag, so it can be tested from any machine.
func ownsAction(action string) bool {
	a := strings.ToLower(action)
	if strings.Contains(a, "sync-claude-sessions") {
		return false
	}
	return strings.Contains(a, "claude-sessions")
}

// neverRun reports Task Scheduler's "has not run yet" state.
//
// A freshly registered task reports the sentinel date 30/11/1999 and result 267011
// (0x41303). Printed verbatim, a correct install looks like a failure from the last
// century.
func neverRun(lastRun, lastResult string) bool {
	return strings.Contains(lastRun, "1999") || strings.TrimSpace(lastResult) == "267011"
}

// describeResult translates the Task Scheduler codes worth naming. Anything else is
// passed through: an unexplained number the user can search for beats a wrong guess.
func describeResult(code string) string {
	switch strings.TrimSpace(code) {
	case "0":
		return "0 (success)"
	case "267009":
		return "currently running"
	case "267010", "267011":
		return "not yet run"
	case "267014":
		return "terminated by user"
	case "2147750687":
		return "an instance was already running"
	case "2147943645":
		return "the service is not available (is the user logged on?)"
	default:
		return code
	}
}
