package hostagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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

// suffix distinguishes a non-default profile's job. Stable for a given path.
func (j Job) suffix() string {
	if j.Profile == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(j.Profile))))
	return "-" + hex.EncodeToString(sum[:])[:8]
}

// TaskName is the Windows Task Scheduler name.
func (j Job) TaskName() string { return Name + j.suffix() }

// LaunchdLabel is the macOS job label.
func (j Job) LaunchdLabel() string { return Label + j.suffix() }

// SystemdTimerName is the Linux timer unit; the service shares its base name.
func (j Job) SystemdTimerName() string {
	return strings.TrimSuffix(Unit, ".timer") + j.suffix() + ".timer"
}

// SystemdServiceName is the oneshot unit the timer starts.
func (j Job) SystemdServiceName() string {
	return strings.TrimSuffix(j.SystemdTimerName(), ".timer") + ".service"
}

// CronMarker tags our crontab line so uninstall removes that line and nothing else.
func (j Job) CronMarker() string { return "# " + Name + j.suffix() }

// Args is what every platform runs after the binary. --sweep marks the run in the log:
// doctor trusts only the sweep's own success to show the sweep can reach the archive.
func (j Job) Args() []string {
	args := []string{"push", "--quiet", "--sweep"}
	if j.Profile != "" {
		args = append(args, "--claude-dir", j.Profile)
	}
	return args
}

// LaunchdPlist is the macOS user agent definition.
//
// stderr goes beside session-sync.log (the binary lives in <claude-root>/bin): a
// crash or a kill before push opens its own log would otherwise leave no trace.
func LaunchdPlist(j Job, everyMinutes int) string {
	var args strings.Builder
	for _, a := range append([]string{j.Binary}, j.Args()...) {
		args.WriteString("    <string>" + xmlEscape(a) + "</string>\n")
	}
	errLog := path.Join(path.Dir(path.Dir(filepath.ToSlash(j.Binary))), "session-sync.err.log")
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + xmlEscape(j.LaunchdLabel()) + `</string>
  <key>ProgramArguments</key>
  <array>
` + args.String() + `  </array>
  <key>StartInterval</key><integer>` + strconv.Itoa(everyMinutes*60) + `</integer>
  <key>RunAtLoad</key><false/>
  <key>ProcessType</key><string>Background</string>
  <key>StandardErrorPath</key><string>` + xmlEscape(errLog) + `</string>
</dict>
</plist>
`
}

// SystemdService is the oneshot unit that runs a push.
//
// A oneshot has no start timeout by default, and the timer never starts a unit that
// is still active - so one push hung on a dead network mount stopped every later
// sweep until reboot. Task Scheduler's one-hour limit is matched here.
func SystemdService(j Job) string {
	return "[Unit]\nDescription=Archive Claude Code sessions\n\n" +
		"[Service]\nType=oneshot\nTimeoutStartSec=1h\n" +
		"ExecStart=" + systemdCommand(append([]string{j.Binary}, j.Args()...)) + "\n"
}

// SystemdTimer schedules the service.
//
// A monotonic timer: it counts from boot and from the last run. Persistent= only
// applies to OnCalendar timers and was a no-op here, so it is not emitted.
func SystemdTimer(everyMinutes int) string {
	n := strconv.Itoa(everyMinutes)
	return "[Unit]\nDescription=Archive Claude Code sessions every " + n + " minutes\n\n" +
		"[Timer]\nOnBootSec=5min\nOnUnitActiveSec=" + n + "min\n\n" +
		"[Install]\nWantedBy=timers.target\n"
}

// CronLine is the fallback for systems without systemd.
//
// ponytail: intervals of an hour or more round down to whole hours; cron has no
// "every 90 minutes".
func CronLine(j Job, everyMinutes int) string {
	schedule := fmt.Sprintf("*/%d * * * *", everyMinutes)
	if everyMinutes >= 60 {
		schedule = fmt.Sprintf("0 */%d * * *", everyMinutes/60)
	}
	var cmd []string
	for _, a := range append([]string{j.Binary}, j.Args()...) {
		cmd = append(cmd, shellQuote(a))
	}
	// A bare % in a crontab command is a newline.
	line := strings.ReplaceAll(strings.Join(cmd, " "), "%", `\%`)
	return schedule + " " + line + " " + j.CronMarker()
}

// ownsCronLine reports whether a crontab line is this job's.
//
// Matching "claude-sessions" anywhere deleted the user's own lines. The marker is
// exact; the unmarked form is what versions before the marker wrote for the default
// profile.
func ownsCronLine(j Job, line string) bool {
	line = strings.TrimSpace(line)
	if strings.HasSuffix(line, " "+j.CronMarker()) {
		return true
	}
	return j.Profile == "" && !strings.Contains(line, "#") &&
		strings.HasSuffix(line, " push --quiet") && ownsAction(line)
}

// TaskXML is the Windows Task Scheduler definition.
//
// Registered from XML rather than `schtasks /SC MINUTE` because the flags cannot turn
// off Task Scheduler's defaults of not starting on battery and stopping when the
// charger is pulled - on a laptop, the sweep silently skipped every unplugged hour.
//
// j.Binary is the windowless copy (see WindowlessName), so no terminal opens per run.
func TaskXML(j Job, everyMinutes int, now time.Time) string {
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>Archive Claude Code sessions (claude-sessions)</Description></RegistrationInfo>
  <Triggers>
    <TimeTrigger>
      <Repetition><Interval>PT` + strconv.Itoa(everyMinutes) + `M</Interval><StopAtDurationEnd>false</StopAtDurationEnd></Repetition>
      <StartBoundary>` + now.Format("2006-01-02T15:04:05") + `</StartBoundary>
      <Enabled>true</Enabled>
    </TimeTrigger>
  </Triggers>
  <Principals><Principal id="Author"><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <StartWhenAvailable>true</StartWhenAvailable>
    <ExecutionTimeLimit>PT1H</ExecutionTimeLimit>
    <Enabled>true</Enabled>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + xmlEscape(j.Binary) + `</Command>
      <Arguments>` + xmlEscape(windowsArgs(j.Args())) + `</Arguments>
    </Exec>
  </Actions>
</Task>
`
}

// taskProgram is the file name a task runs, whatever OS parses the definition.
func taskProgram(def taskDefinition) string {
	cmd := strings.Trim(strings.TrimSpace(def.Command), `"`)
	return cmd[strings.LastIndexAny(cmd, `\/`)+1:]
}

// ownsTask reports whether a scheduled task runs this tool.
//
// The program's file name must be ours exactly. A substring match on the whole
// command line would claim a foreign task that merely passes a path containing
// "claude-sessions" as an argument, and uninstall deletes what it claims.
func ownsTask(def taskDefinition) bool {
	p := taskProgram(def)
	return strings.EqualFold(p, "claude-sessions.exe") || strings.EqualFold(p, WindowlessName)
}

// taskDefinition is the part of `schtasks /query /xml` this tool reads. XML rather
// than the LIST or CSV forms because their field names and states are translated on
// non-English Windows.
type taskDefinition struct {
	Command   string `xml:"Actions>Exec>Command"`
	Arguments string `xml:"Actions>Exec>Arguments"`
	Enabled   string `xml:"Settings>Enabled"`
}

func parseTaskXML(data []byte) (taskDefinition, error) {
	var t taskDefinition
	// schtasks declares UTF-16 but writes the console encoding; the content that
	// matters is ASCII-compatible either way.
	text := strings.Replace(string(data), `encoding="UTF-16"`, `encoding="UTF-8"`, 1)
	err := xml.Unmarshal([]byte(text), &t)
	return t, err
}

// windowsArgs joins arguments the way CommandLineToArgvW splits them. Paths cannot
// contain '"', so quoting anything with whitespace is enough - except that
// backslashes before the closing quote escape it, so trailing ones are doubled
// (`\\srv\my share\` would otherwise swallow the quote and every later argument).
func windowsArgs(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t") {
			a = `"` + a + a[len(strings.TrimRight(a, `\`)):] + `"`
		}
		out[i] = a
	}
	return strings.Join(out, " ")
}

// systemdCommand quotes an ExecStart line: '%' is a specifier and '$' an
// environment expansion, and a path with a space is otherwise two words.
func systemdCommand(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		a = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$").Replace(a)
		out[i] = `"` + a + `"`
	}
	return strings.Join(out, " ")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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

// ownsAction reports whether an unmarked crontab command line is this tool's binary.
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

// resultIsHealthy reports whether a Task Scheduler last-result code means the sweep
// works. push --quiet always exits 0, so anything else is the scheduler failing to
// start it - typically 2147942402, the binary is gone - or 267014, a run killed
// before it finished, which is what the one-hour limit does to a hung push.
func resultIsHealthy(code string) bool {
	switch strings.TrimSpace(code) {
	case "", "0", "267009", "267010", "267011", "2147750687":
		return true
	}
	return false
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
		return "terminated before finishing (stopped by hand, or hung past the 1-hour limit)"
	case "2147750687":
		return "an instance was already running"
	case "2147943645":
		return "the service is not available (is the user logged on?)"
	case "2147942402":
		return "2147942402 (the program was not found)"
	default:
		return code
	}
}
