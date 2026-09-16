package hostagent

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"
)

// These run on every platform, which is the point: the macOS and Linux scheduling
// used to be unreachable from the machine this is developed on, so "untested on
// macOS and Linux" was a permanent caveat instead of a gap that could be closed.

func TestLaunchdPlistIsWellFormed(t *testing.T) {
	got := LaunchdPlist(Job{Binary: "/Users/me/.claude/bin/claude-sessions"}, 30)

	// launchd rejects a malformed plist silently from the user's point of view: the
	// agent simply never runs.
	var doc any
	if err := xml.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("plist is not well-formed XML: %v\n%s", err, got)
	}

	for _, want := range []string{
		"<key>Label</key><string>" + Label + "</string>",
		"<string>/Users/me/.claude/bin/claude-sessions</string>",
		"<string>push</string>",
		"<string>--quiet</string>",
		"<key>StartInterval</key><integer>1800</integer>", // minutes -> seconds
		"<key>RunAtLoad</key><false/>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plist missing %q", want)
		}
	}
}

// A path with an ampersand or quotes would otherwise produce invalid XML, and the
// agent would never load.
func TestLaunchdPlistEscapesPaths(t *testing.T) {
	got := LaunchdPlist(Job{Binary: `/Users/me/R&D "work"/claude-sessions`}, 15)

	var doc any
	if err := xml.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("a path with & and quotes broke the plist: %v\n%s", err, got)
	}
	if strings.Contains(got, `R&D`) {
		t.Error("the ampersand was not escaped")
	}
	if !strings.Contains(got, "R&amp;D") {
		t.Error("expected an escaped ampersand")
	}
	if !strings.Contains(got, "<integer>900</integer>") {
		t.Error("15 minutes should be 900 seconds")
	}
}

func TestSystemdUnits(t *testing.T) {
	service := SystemdService(Job{Binary: "/home/me/.claude/bin/claude-sessions"})
	if !strings.Contains(service, `ExecStart="/home/me/.claude/bin/claude-sessions" "push" "--quiet"`) {
		t.Errorf("service unit wrong:\n%s", service)
	}
	if !strings.Contains(service, "Type=oneshot") {
		t.Error("a push is a oneshot, not a daemon")
	}

	// A space splits ExecStart into two words; % is a systemd specifier.
	odd := SystemdService(Job{Binary: "/home/me/my tools/100%/claude-sessions"})
	if !strings.Contains(odd, `ExecStart="/home/me/my tools/100%%/claude-sessions"`) {
		t.Errorf("binary path not quoted for systemd:\n%s", odd)
	}

	timer := SystemdTimer(30)
	if !strings.Contains(timer, "OnUnitActiveSec=30min") {
		t.Errorf("timer interval wrong:\n%s", timer)
	}
	if !strings.Contains(timer, "[Install]\nWantedBy=timers.target") {
		t.Error("timer cannot be enabled without an [Install] section")
	}
}

func TestCronLine(t *testing.T) {
	j := Job{Binary: "/home/me/.claude/bin/claude-sessions"}
	got := CronLine(j, 15)
	if got != "*/15 * * * * '/home/me/.claude/bin/claude-sessions' 'push' '--quiet' # claude-sessions-sync" {
		t.Errorf("cron line = %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Error("a cron entry must be a single line")
	}
	// */120 is not "every two hours": the minute field only goes to 59.
	if got := CronLine(j, 120); !strings.HasPrefix(got, "0 */2 * * * ") {
		t.Errorf("120 minutes = %q", got)
	}
	// A space would split the command; a bare % is a newline to cron.
	odd := CronLine(Job{Binary: "/home/me/my tools/100%/claude-sessions"}, 30)
	if !strings.Contains(odd, `'/home/me/my tools/100\%/claude-sessions'`) {
		t.Errorf("path not quoted for cron: %q", odd)
	}
}

// Uninstall once removed every crontab line mentioning "claude-sessions", including
// the user's own.
func TestOwnsCronLine(t *testing.T) {
	def := Job{}
	other := Job{Profile: "/tmp/scratch"}
	cases := []struct {
		j    Job
		line string
		want bool
	}{
		{def, CronLine(Job{Binary: "/b/claude-sessions"}, 30), true},
		{def, "*/30 * * * * /b/claude-sessions push --quiet", true}, // pre-marker form
		{def, "0 3 * * * /b/claude-sessions render --force", false},
		{def, "0 * * * * ~/dev/claude-sessions-sync/build.sh", false},
		{def, CronLine(Job{Binary: "/b/claude-sessions", Profile: "/tmp/scratch"}, 30), false},
		{other, CronLine(Job{Binary: "/b/claude-sessions"}, 30), false},
		{other, "*/30 * * * * /b/claude-sessions push --quiet", false},
		{other, CronLine(Job{Binary: "/b/claude-sessions", Profile: "/tmp/scratch"}, 30), true},
	}
	for _, tc := range cases {
		if got := ownsCronLine(tc.j, tc.line); got != tc.want {
			t.Errorf("ownsCronLine(%q, %q) = %v, want %v", tc.j.Profile, tc.line, got, tc.want)
		}
	}
}

// The sweep disappeared from a real machine because `install --claude-dir <scratch>`
// and `uninstall` - the CI job, run locally - used the same machine-wide name as the
// real install.
func TestJobNamesArePerProfile(t *testing.T) {
	def := Job{}
	a := Job{Profile: `C:\Temp\claude`}
	b := Job{Profile: `C:\Temp\other`}

	if def.TaskName() != Name || def.LaunchdLabel() != Label || def.SystemdTimerName() != Unit {
		t.Error("the default profile must keep the names existing installs registered")
	}
	for _, j := range []Job{a, b} {
		if j.TaskName() == Name || j.LaunchdLabel() == Label || j.SystemdTimerName() == Unit || j.CronMarker() == def.CronMarker() {
			t.Errorf("profile %q reuses the default job's names", j.Profile)
		}
	}
	if a.TaskName() == b.TaskName() {
		t.Error("two profiles share a task name")
	}
	if (Job{Profile: `c:\temp\CLAUDE`}).TaskName() != a.TaskName() {
		t.Error("the same path in a different case must name the same job")
	}
	if strings.TrimSuffix(a.SystemdServiceName(), ".service") != strings.TrimSuffix(a.SystemdTimerName(), ".timer") {
		t.Errorf("timer %q does not start service %q", a.SystemdTimerName(), a.SystemdServiceName())
	}

	// The scheduler does not inherit CLAUDE_CONFIG_DIR, so the profile is passed.
	if got := strings.Join(a.Args(), " "); got != `push --quiet --claude-dir C:\Temp\claude` {
		t.Errorf("args = %q", got)
	}
	if got := strings.Join(def.Args(), " "); got != "push --quiet" {
		t.Errorf("default args = %q", got)
	}
}

func TestTaskXML(t *testing.T) {
	j := Job{Binary: `C:\Users\First Last\.claude\bin\claude-sessions.exe`, Profile: `D:\claude profiles\work`}
	got := TaskXML(j, 30, time.Date(2026, 9, 16, 10, 0, 0, 0, time.Local))

	def, err := parseTaskXML([]byte(got))
	if err != nil {
		t.Fatalf("task XML does not parse: %v\n%s", err, got)
	}
	if def.Command != j.Binary {
		t.Errorf("command = %q", def.Command)
	}
	// A path with a space must stay one argument.
	if def.Arguments != `push --quiet --claude-dir "D:\claude profiles\work"` {
		t.Errorf("arguments = %q", def.Arguments)
	}
	if def.Enabled != "true" || !ownsAction(def.Command+" "+def.Arguments) {
		t.Errorf("parsed definition = %+v", def)
	}
	for _, want := range []string{
		"<Interval>PT30M</Interval>",
		"<StartBoundary>2026-09-16T10:00:00</StartBoundary>",
		// Task Scheduler's defaults skip every run on battery - a laptop's sweep
		// silently did nothing while unplugged.
		"<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>",
		"<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("task XML missing %s", want)
		}
	}
}

// What `schtasks /query /xml` actually prints on Windows 11: an encoding declaration
// that does not match the bytes, and CR CR LF line endings.
func TestParseTaskXMLFromSchtasks(t *testing.T) {
	out := "<?xml version=\"1.0\" encoding=\"UTF-16\"?>\r\r\n" +
		"<Task version=\"1.2\" xmlns=\"http://schemas.microsoft.com/windows/2004/02/mit/task\">\r\r\n" +
		"  <Settings>\r\r\n    <Enabled>false</Enabled>\r\r\n  </Settings>\r\r\n" +
		"  <Actions Context=\"Author\">\r\r\n    <Exec>\r\r\n" +
		"      <Command>C:\\x\\claude-sessions.exe</Command>\r\r\n" +
		"      <Arguments>push --quiet</Arguments>\r\r\n" +
		"    </Exec>\r\r\n  </Actions>\r\r\n</Task>\r\r\n"
	def, err := parseTaskXML([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if def.Command != `C:\x\claude-sessions.exe` || def.Arguments != "push --quiet" || def.Enabled != "false" {
		t.Errorf("parsed %+v", def)
	}
}

func TestResultIsHealthy(t *testing.T) {
	for _, ok := range []string{"0", "267009", "267011", "2147750687", ""} {
		if !resultIsHealthy(ok) {
			t.Errorf("%s reported as a failure", ok)
		}
	}
	// 0x80070002: the scheduled program no longer exists.
	if resultIsHealthy("2147942402") {
		t.Error("a missing program reported as healthy")
	}
}

func TestDefaultInterval(t *testing.T) {
	for _, in := range []int{0, -5} {
		if got := normalMinutes(in); got != 30 {
			t.Errorf("normalMinutes(%d) = %d, want 30", in, got)
		}
	}
	if got := normalMinutes(15); got != 15 {
		t.Errorf("normalMinutes(15) = %d", got)
	}
}

// Every platform must run the same command, or a session archived on one machine
// behaves differently from another for no reason a user could discover.
func TestEveryPlatformRunsPushQuiet(t *testing.T) {
	j := Job{Binary: "/opt/claude-sessions", Profile: "/p"}
	for name, content := range map[string]string{
		"launchd": LaunchdPlist(j, 30),
		"systemd": SystemdService(j),
		"cron":    CronLine(j, 30),
		"windows": TaskXML(j, 30, time.Now()),
	} {
		for _, want := range []string{"push", "--quiet", "--claude-dir", "/p"} {
			if !strings.Contains(content, want) {
				t.Errorf("%s does not run `push --quiet --claude-dir`:\n%s", name, content)
			}
		}
	}
}
