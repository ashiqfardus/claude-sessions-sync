package hostagent

import (
	"encoding/xml"
	"strings"
	"testing"
)

// These run on every platform, which is the point: the macOS and Linux scheduling
// used to be unreachable from the machine this is developed on, so "untested on
// macOS and Linux" was a permanent caveat instead of a gap that could be closed.

func TestLaunchdPlistIsWellFormed(t *testing.T) {
	got := LaunchdPlist(Label, "/Users/me/.claude/bin/claude-sessions", 30)

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
	got := LaunchdPlist(Label, `/Users/me/R&D "work"/claude-sessions`, 15)

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
	service := SystemdService("/home/me/.claude/bin/claude-sessions")
	if !strings.Contains(service, "ExecStart=/home/me/.claude/bin/claude-sessions push --quiet") {
		t.Errorf("service unit wrong:\n%s", service)
	}
	if !strings.Contains(service, "Type=oneshot") {
		t.Error("a push is a oneshot, not a daemon")
	}

	timer := SystemdTimer(30)
	if !strings.Contains(timer, "OnUnitActiveSec=30min") {
		t.Errorf("timer interval wrong:\n%s", timer)
	}
	// Without Persistent a machine asleep at the scheduled moment skips the run
	// entirely, which for a backup is the wrong answer.
	if !strings.Contains(timer, "Persistent=true") {
		t.Error("timer must catch up after the machine was asleep")
	}
	if !strings.Contains(timer, "[Install]\nWantedBy=timers.target") {
		t.Error("timer cannot be enabled without an [Install] section")
	}
}

func TestCronLine(t *testing.T) {
	got := CronLine("/home/me/.claude/bin/claude-sessions", 15)
	if got != "*/15 * * * * /home/me/.claude/bin/claude-sessions push --quiet" {
		t.Errorf("cron line = %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Error("a cron entry must be a single line")
	}
}

func TestSchtasksCreateArgs(t *testing.T) {
	got := SchtasksCreateArgs(Name, `C:\Users\First Last\.claude\bin\claude-sessions.exe`, 30)

	joined := strings.Join(got, " ")
	// The path contains a space; without quoting, schtasks treats the tail as
	// arguments to a program that does not exist.
	if !strings.Contains(joined, `"C:\Users\First Last\.claude\bin\claude-sessions.exe" push --quiet`) {
		t.Errorf("binary path is not quoted as one command: %v", got)
	}
	if !strings.Contains(joined, "/SC MINUTE /MO 30") {
		t.Errorf("schedule wrong: %v", got)
	}
	if !strings.Contains(joined, "/TN "+Name) {
		t.Errorf("task name wrong: %v", got)
	}
	// Replaces our own task rather than failing; the name is ours alone.
	if !strings.Contains(joined, "/F") {
		t.Error("missing /F")
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
	bin := "/opt/claude-sessions"
	for name, content := range map[string]string{
		"launchd": LaunchdPlist(Label, bin, 30),
		"systemd": SystemdService(bin),
		"cron":    CronLine(bin, 30),
		"windows": strings.Join(SchtasksCreateArgs(Name, bin, 30), " "),
	} {
		if !strings.Contains(content, "push") || !strings.Contains(content, "--quiet") {
			t.Errorf("%s does not run `push --quiet`:\n%s", name, content)
		}
	}
}
