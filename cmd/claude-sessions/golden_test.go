package main_test

// Golden files pin the exact output of commands, so a refactor can prove it changed
// nothing. `go test ./cmd/claude-sessions/ -run TestGolden -update` rewrites them;
// read every changed file before committing it.

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/golden"
)

type goldenEnv struct {
	m     machine
	home  string
	roots map[string]string
}

// mask replaces what a fixture cannot hold still, applied after golden.Normalize.
type mask struct{ re, with string }

// versionMasks hide the build's version: "dev" from Go 1.21, a VCS pseudo-version
// from later toolchains.
var versionMasks = []mask{
	{`(?m)^claude-sessions \S+( doctor)?$`, "claude-sessions <VERSION>$1"},
	{`"version": "[^"]*"`, `"version": "<VERSION>"`},
}

// lastPushMasks hide the shard's lastPush: the hour, in UTC, of the push the fixture
// just made. A shard from an older build prints "-" (zero time in JSON), left as is.
var lastPushMasks = []mask{
	{`(?m)^(LAP-1 +\d+ +\d+ +)\d{4}-\d\d-\d\d \d\d:\d\d`, "${1}<LAST-PUSH-HOUR>"},
	{`("LastPush": ")[1-9][^"]*"`, `${1}<LAST-PUSH-HOUR>"`},
	{`(last backup +LAP-1 |"detail": "LAP-1 )\d{4}-\d\d-\d\d \d\d:\d\d`, "${1}<LAST-PUSH-HOUR>"},
}

var doctorMasks = append([]mask{
	// The sweep is launchd, systemd or cron, or Task Scheduler, and its job name holds
	// a hash of the temp profile's path.
	{`(?m)^  . sweep .*$`, "  ? sweep <SWEEP>"},
	{`(?m)^  sweep: .*$`, "  sweep: <SWEEP-ADVICE>"},
	{`(?s)"name": "sweep",.*?\n    \}`, "\"name\": \"sweep\" <SWEEP>\n    }"},
	{`\d+[hms][\dhms.]* ago`, "<AGO> ago"},
	// The last push is the one the fixture just made, timed by the wall clock.
	{`(last push +)\d{4}-\d\d-\d\d \d\d:\d\d`, "${1}<PUSHED-AT>"},
	{`("name": "last push",\s+"level": "ok",\s+"detail": ")\d{4}-\d\d-\d\d \d\d:\d\d`, "${1}<PUSHED-AT>"},
}, lastPushMasks...)

const (
	goldenWeb1  = "11111111-1111-1111-1111-111111111111"
	goldenWeb2  = "22222222-2222-2222-2222-222222222222"
	goldenNotes = "33333333-3333-3333-3333-333333333333"
)

// goldenFixture is LAP-1 with a git repo web-app (two conversations, the first with a
// subagent and saved tool output) and a plain folder notes (one), its destination
// saved and zipping off, every file dated 2026-08-30 09:00 UTC, pushed once.
func goldenFixture(t *testing.T) goldenEnv {
	t.Helper()
	m := newMachine(t, "LAP-1", t.TempDir())
	// Deep enough that a path ls (34 columns) or stats (48) shortens keeps only the
	// part below the temp folder.
	home := filepath.Join(t.TempDir(), "Users", "you")
	repo := gitRepo(t, filepath.Join(home, "Documents", "GitHub", "example-org", "web-app"))
	notes := filepath.Join(home, "Documents", "planning-notes", "example-org", "notes")
	mustMkdir(t, notes)
	e := goldenEnv{m: m, home: home, roots: map[string]string{
		"<CLAUDE>": m.claudeDir, "<ARCHIVE>": m.archive, "<HOME>": home,
		"<HOME-SLUG>": claude.Slug(home), // the start of every bucket name
	}}

	transcript := func(dir, id string, uuids ...string) string {
		p := filepath.Join(m.claudeDir, "projects", claude.Slug(dir), id+".jsonl")
		mustWrite(t, p, padLines(t, strings.ReplaceAll(sessionLines(dir, uuids...), forkSession, id)))
		return p
	}
	web1 := transcript(repo, goldenWeb1, "u1", "migration", "u3")
	transcript(repo, goldenWeb2, "u4", "u5")
	transcript(notes, goldenNotes, "u6")
	sessionDir := strings.TrimSuffix(web1, ".jsonl")
	mustWrite(t, filepath.Join(sessionDir, "subagents", "agent-a1.jsonl"), padLines(t, sessionLines(repo, "s1")))
	mustWrite(t, filepath.Join(sessionDir, "tool-results", "r1.txt"), "output\n")

	// config takes no --archive, so the destination is saved, as a user's is.
	for _, args := range [][]string{
		{"config", "set-destination", m.archive},
		{"config", "zip-sessions", "off"},
		{"push"},
	} {
		if args[0] == "push" {
			pinTimes(t, m.claudeDir, home)
		}
		if out := runGolden(t, e, args...); !strings.Contains(out, "\nexit 0\n") {
			t.Fatalf("fixture: %s", out)
		}
	}
	return e
}

// padLines pads every line to 600 bytes with a "pad" field. Each holds the cwd, as long
// as the temp folder, which differs between runs and OSes, and ls and stats print sizes.
func padLines(t *testing.T, lines string) string {
	t.Helper()
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimSuffix(lines, "\n"), "\n") {
		n := 600 - len(l) - len(`,"pad":""`)
		if n < 0 {
			t.Fatalf("a line is over 600 bytes: %s", l)
		}
		b.WriteString(strings.TrimSuffix(l, "}") + `,"pad":"` + strings.Repeat(" ", n) + `"}` + "\n")
	}
	return b.String()
}

// pinTimes dates every file and folder under dirs 2026-08-30 09:00 UTC.
func pinTimes(t *testing.T, dirs ...string) {
	t.Helper()
	at := time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC)
	for _, d := range dirs {
		if err := filepath.WalkDir(d, func(p string, _ fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			return os.Chtimes(p, at, at)
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// runGolden runs the binary as LAP-1 with e's profile and home, and returns the
// command, its exit code, stdout and stderr, normalized.
func runGolden(t *testing.T, e goldenEnv, args ...string) string {
	t.Helper()
	full := args
	if len(args) > 0 {
		full = append([]string{args[0], "--claude-dir", e.m.claudeDir}, args[1:]...)
	}
	cmd := exec.Command(binary, full...)
	// TZ pins the local times printed on Linux and macOS; Windows ignores it, and its
	// CI runners keep UTC.
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=", "CLAUDE_CODE_PROJECT_DIR_NAME=",
		"CLAUDE_SESSIONS_MACHINE="+e.m.name, "HOME="+e.home, "USERPROFILE="+e.home, "TZ=UTC")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	n := func(s string) string { return golden.Normalize(s, e.roots) }
	out := fmt.Sprintf("$ %s\nexit %d\n--- stdout\n%s--- stderr\n%s",
		n(strings.Join(append([]string{"claude-sessions"}, full...), " ")), code, n(stdout.String()), n(stderr.String()))
	return applyMasks(out, versionMasks)
}

func applyMasks(s string, masks []mask) string {
	for _, m := range masks {
		s = regexp.MustCompile(m.re).ReplaceAllString(s, m.with)
	}
	return s
}

func TestGoldenReadOnly(t *testing.T) {
	for _, c := range []struct {
		name  string
		args  []string
		masks []mask
	}{
		{"ls", []string{"ls"}, nil},
		{"ls-json", []string{"ls", "--json"}, nil},
		{"ls-archived", []string{"ls", "--archived"}, nil},
		{"ls-bad-date", []string{"ls", "--since", "2026-13-01"}, nil},
		{"search", []string{"search", "migration"}, nil},
		{"search-json", []string{"search", "--json", "migration"}, nil},
		{"search-none", []string{"search", "nothing-matches-this"}, nil},
		{"stats", []string{"stats"}, nil},
		{"stats-json", []string{"stats", "--json"}, nil},
		{"machines", []string{"machines"}, lastPushMasks},
		{"machines-json", []string{"machines", "--json"}, lastPushMasks},
		{"doctor", []string{"doctor"}, doctorMasks},
		{"doctor-json", []string{"doctor", "--json"}, doctorMasks},
		{"config-show", []string{"config", "show"}, nil},
		{"config-map-list", []string{"config", "map"}, nil},
		{"config-keep-list", []string{"config", "keep"}, nil},
		{"version", []string{"version"}, nil},
		{"help", nil, nil},
		{"unknown-command", []string{"bogus"}, nil},
	} {
		c := c
		t.Run(c.name, func(t *testing.T) {
			e := goldenFixture(t)
			golden.Check(t, c.name, applyMasks(runGolden(t, e, c.args...), c.masks))
		})
	}
}
