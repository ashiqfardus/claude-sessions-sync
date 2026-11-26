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
	m           machine
	home        string
	repo, notes string
	roots       map[string]string
	env         []string // added to runGolden's environment
}

// mask replaces what a fixture cannot hold still, applied after golden.Normalize.
type mask struct{ re, with string }

// versionMasks hide the build's version: "dev" from Go 1.21, a VCS pseudo-version
// from later toolchains.
var versionMasks = []mask{
	{`(?m)^claude-sessions \S+( doctor)?$`, "claude-sessions <VERSION>$1"},
	{`"version": "[^"]*"`, `"version": "<VERSION>"`},
}

// lastPushMasks hide the date and hour of the shard's lastPush, from the push the
// fixture just made. It is cut to the hour, so ":00" stays and is checked. A shard from
// an older build prints "-" (zero time, year 0001, in JSON), left as is.
var lastPushMasks = []mask{
	{`(?m)^(LAP-1 +\d+ +\d+ +)\d{4}-\d\d-\d\d \d\d(:00)`, "${1}<LASTPUSH-HR>$2"},
	{`("LastPush": ")[1-9]\d{3}-\d\d-\d\dT\d\d(:00:00Z")`, "${1}<LASTPUSH-HR>$2"},
	{`(last backup +LAP-1 |"detail": "LAP-1 )\d{4}-\d\d-\d\d \d\d(:00)`, "${1}<LASTPUSH-HR>$2"},
}

var doctorMasks = append([]mask{
	// The sweep's detail and advice name launchd, systemd or cron, or Task Scheduler,
	// and the job name holds a hash of the temp profile's path. Its level stays: the job
	// is unregistered on every OS here, a warning (see TestGoldenReadOnly).
	{`(?m)^(  . sweep +).*$`, "${1}<SWEEP>"},
	{`(?m)^(  sweep: ).*$`, "${1}<SWEEP-ADVICE>"},
	{`("name": "sweep",\s+"level": "[a-z]+",\s+"detail": ")[^"]*"`, `${1}<SWEEP>"`},
	{`("name": "sweep",\s+"level": "[a-z]+",\s+"detail": "<SWEEP>",\s+"advice": ")[^"]*"`, `${1}<SWEEP-ADVICE>"`},
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
	e := goldenSetup(t)
	pinTimes(t, e.m.claudeDir, e.home)
	goldenStep(t, e, "push")
	return e
}

// goldenSetup is goldenFixture before its times are pinned and it is pushed.
func goldenSetup(t *testing.T) goldenEnv {
	t.Helper()
	m := newMachine(t, "LAP-1", t.TempDir())
	// Deep enough that a path ls (34 columns) or stats (48) shortens keeps only the
	// part below the temp folder.
	home := filepath.Join(t.TempDir(), "Users", "you")
	repo := gitRepo(t, filepath.Join(home, "Documents", "GitHub", "example-org", "web-app"))
	notes := filepath.Join(home, "Documents", "planning-notes", "example-org", "notes")
	mustMkdir(t, notes)
	e := goldenEnv{m: m, home: home, repo: repo, notes: notes, roots: map[string]string{
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
	goldenStep(t, e, "config", "set-destination", m.archive)
	goldenStep(t, e, "config", "zip-sessions", "off")
	return e
}

// goldenStep runs a fixture's setup step, which must succeed.
func goldenStep(t *testing.T, e goldenEnv, args ...string) {
	t.Helper()
	if out := runGolden(t, e, args...); !strings.Contains(out, "\nexit 0\n") {
		t.Fatalf("fixture: %s", out)
	}
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
	cmd.Env = append(cmd.Env, e.env...)
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
		// 3 warnings on every OS: retention, session hook and the sweep, which is not
		// registered for a temp profile. Only a Task Scheduler that cannot be queried
		// at all reports it differently (still a warning, with no advice).
		{"doctor", []string{"doctor"}, doctorMasks},
		{"doctor-json", []string{"doctor", "--json"}, doctorMasks},
		// The flag, not the saved destination; "<ARCHIVE>" is the fixture's archive.
		{"doctor-archive-flag", []string{"doctor", "--archive", "<ARCHIVE>"}, doctorMasks},
		{"machines-archive-flag", []string{"machines", "--archive", "<ARCHIVE>"}, lastPushMasks},
		{"ls-archived-archive-flag", []string{"ls", "--archive", "<ARCHIVE>"}, nil},
		{"search-archive-flag", []string{"search", "--archive", "<ARCHIVE>", "migration"}, nil},
		{"config-show", []string{"config", "show"}, nil},
		{"config-show-json", []string{"config", "show", "--json"}, nil},
		{"config-map-list", []string{"config", "map"}, nil},
		{"config-keep-list", []string{"config", "keep"}, nil},
		{"version", []string{"version"}, nil},
		{"help", nil, nil},
		{"unknown-command", []string{"bogus"}, nil},
	} {
		c := c
		t.Run(c.name, func(t *testing.T) {
			e := goldenFixture(t)
			args := append([]string(nil), c.args...)
			for i, a := range args {
				if a == "<ARCHIVE>" {
					args[i] = e.m.archive
				}
			}
			golden.Check(t, c.name, applyMasks(runGolden(t, e, args...), c.masks))
		})
	}
}

// goldenLAP2 is a second computer on e's archive, with its own profile and home and
// web-app cloned at <LAP2-HOME>/dev/web-app, its destination saved and zipping off.
func goldenLAP2(t *testing.T, e goldenEnv) goldenEnv {
	t.Helper()
	m := newMachine(t, "LAP-2", e.m.archive)
	home := filepath.Join(t.TempDir(), "Users", "you")
	roots := map[string]string{"<LAP2-CLAUDE>": m.claudeDir, "<LAP2-HOME>": home, "<LAP2-HOME-SLUG>": claude.Slug(home)}
	for k, v := range e.roots {
		roots[k] = v
	}
	// LAP-1's temp list, if goldenWork gave it one: import refuses to place a work folder there.
	l := goldenEnv{m: m, home: home, repo: gitRepo(t, filepath.Join(home, "dev", "web-app")), roots: roots, env: e.env}
	goldenStep(t, l, "config", "set-destination", e.m.archive)
	goldenStep(t, l, "config", "zip-sessions", "off")
	return l
}

// lap2Import runs import on l with LAP-1's home put away, as it would not exist there.
func lap2Import(t *testing.T, e, l goldenEnv, args ...string) (out string) {
	t.Helper()
	away(t, e.home, func() {
		out = runGolden(t, l, append([]string{"import", "--search-root", filepath.Join(l.home, "dev")}, args...)...)
	})
	return out
}

// goldenWork gives e one work folder, <HOME>/reports/q3, written by a conversation
// started in the home folder, and pushes it.
func goldenWork(t *testing.T, e goldenEnv) goldenEnv {
	t.Helper()
	q3 := filepath.Join(e.home, "reports", "q3")
	mustWrite(t, filepath.Join(q3, "r.md"), "report\n")
	mustWrite(t, filepath.Join(q3, "out.csv"), "a,b\n1,2\n")
	workSession(t, e.m, e.home, []string{filepath.Join(q3, "r.md")})
	// Every test folder is under the system temp folder, which push never backs up.
	e.env = []string{"CLAUDE_SESSIONS_TEMP_DIRS=" + filepath.Join(e.home, "no-temp") + string(os.PathListSeparator)}
	goldenStep(t, e, "push")
	return e
}

// addTurn appends a turn to a conversation in dir, dated after the last push.
func addTurn(t *testing.T, e goldenEnv, dir, id, uuid string) {
	t.Helper()
	p := filepath.Join(e.m.claudeDir, "projects", claude.Slug(dir), id+".jsonl")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(padLines(t, strings.ReplaceAll(sessionLines(dir, uuid), forkSession, id)))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	later(t, p)
}

// copyTree copies the files under from to to.
func copyTree(t *testing.T, from, to string) {
	t.Helper()
	if err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		mustWrite(t, filepath.Join(to, rel), mustRead(t, p))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

var backupMasks = []mask{
	// The zip's name holds the wall-clock time it was made.
	{`claude-backup-LAP-1-\d{8}-\d{6}\.zip`, "claude-backup-LAP-1-<STAMP>.zip"},
	// Its size depends on how well the temp paths inside it compress.
	{`(?m)^(Wrote .*\.zip \()[0-9.]+[BKM]\)$`, "${1}<SIZE>)"},
}

// sizeMasks hide a size in bytes other than 0: a copy's page holds the temp folder's
// path, whose length differs from run to run.
var sizeMasks = []mask{{`("(size|freed)": )[1-9]\d*`, "${1}<SIZE>"}}

func TestGoldenChanging(t *testing.T) {
	// pushed runs args on goldenFixture, unpushed on it before its push.
	pushed := func(args ...string) func(*testing.T) string {
		return func(t *testing.T) string { return runGolden(t, goldenFixture(t), args...) }
	}
	unpushed := func(args ...string) func(*testing.T) string {
		return func(t *testing.T) string {
			e := goldenSetup(t)
			pinTimes(t, e.m.claudeDir, e.home)
			return runGolden(t, e, args...)
		}
	}
	// imports runs import with args on LAP-2, after LAP-1 pushed a work folder too.
	imports := func(args ...string) func(*testing.T) string {
		return func(t *testing.T) string {
			e := goldenWork(t, goldenFixture(t))
			return lap2Import(t, e, goldenLAP2(t, e), args...)
		}
	}
	// pulls runs pull with args after this computer lost notes' conversation.
	pulls := func(args ...string) func(*testing.T) string {
		return func(t *testing.T) string {
			e := goldenFixture(t)
			mustRemove(t, filepath.Join(e.m.claudeDir, "projects", claude.Slug(e.notes), goldenNotes+".jsonl"))
			return runGolden(t, e, append([]string{"pull"}, args...)...)
		}
	}
	// forgets runs machines with args after a second computer, LAP-2, pushed.
	forgets := func(args ...string) func(*testing.T) string {
		return func(t *testing.T) string {
			e := goldenFixture(t)
			mustWrite(t, filepath.Join(e.m.archive, "manifest", "LAP-2.json"), `{"schemaVersion": 1, "machine": "LAP-2", "projects": {}}`+"\n")
			return runGolden(t, e, append([]string{"machines"}, args...)...)
		}
	}
	for _, c := range []struct {
		name  string
		run   func(t *testing.T) string
		masks []mask
	}{
		// push: the first on a fixture not pushed yet, then again with nothing new.
		{"push", unpushed("push"), nil},
		{"push-again", pushed("push"), nil},
		{"push-dry-run", unpushed("push", "--dry-run"), nil},
		{"push-json", unpushed("push", "--json"), nil},

		{"render", pushed("render"), nil},
		{"render-json", pushed("render", "--json"), nil},
		{"render-exclude", pushed("render", "--exclude", "notes"), nil},
		{"render-force", pushed("render", "--force"), nil},

		{"import-dry-run", imports("--dry-run"), nil},
		{"import", imports(), nil},
		{"import-json", imports("--json"), nil},
		{"import-profile-only-json", func(t *testing.T) string {
			e := goldenWork(t, goldenFixture(t))
			l := goldenLAP2(t, e)
			mustWrite(t, filepath.Join(l.m.archive, "profile", "skills", "demo", "SKILL.md"), "demo\n")
			var out string
			away(t, e.home, func() { out = runGolden(t, l, "import", "--profile-only", "--json") })
			return out
		}, []mask{{`/\d{8}-\d{6}"`, `/<STAMP>"`}}},
		{"import-update", func(t *testing.T) string {
			e := goldenWork(t, goldenFixture(t))
			l := goldenLAP2(t, e)
			if out := lap2Import(t, e, l); !strings.Contains(out, "\nexit 0\n") {
				t.Fatalf("fixture: %s", out)
			}
			addTurn(t, e, e.repo, goldenWeb1, "u7")
			goldenStep(t, e, "push")
			return lap2Import(t, e, l, "--update")
		}, nil},

		{"pull-dry-run", pulls("--dry-run"), nil},
		{"pull", pulls(), nil},
		{"pull-json", pulls("--json"), nil},

		// restore: web-app's archived folder, filed at <HOME>/dev/web-app.
		{"restore-dry-run", func(t *testing.T) string { return goldenRestore(t, "--dry-run") }, nil},
		{"restore", func(t *testing.T) string { return goldenRestore(t) }, nil},
		{"restore-json", func(t *testing.T) string { return goldenRestore(t, "--json") }, nil},

		// clean: LAP-2 imported web-app and went on with its first conversation, so
		// LAP-2's copy holds LAP-1's in whole.
		{"clean", func(t *testing.T) string { return runGolden(t, goldenTwoCopies(t), "clean") }, nil},
		{"clean-yes", func(t *testing.T) string { return runGolden(t, goldenTwoCopies(t), "clean", "--yes") }, nil},
		{"clean-json", func(t *testing.T) string { return runGolden(t, goldenTwoCopies(t), "clean", "--json") }, sizeMasks},
		{"clean-yes-json", func(t *testing.T) string { return runGolden(t, goldenTwoCopies(t), "clean", "--yes", "--json") }, sizeMasks},

		{"prune-list", func(t *testing.T) string { return runGolden(t, goldenAged(t), "prune", "--older-than", "30d") }, nil},
		{"prune-list-json", func(t *testing.T) string {
			return runGolden(t, goldenAged(t), "prune", "--older-than", "30d", "--json")
		}, sizeMasks},
		{"prune-yes", func(t *testing.T) string { return runGolden(t, goldenFixture(t), "prune", goldenNotes, "--yes") }, nil},
		{"prune-yes-json", func(t *testing.T) string {
			return runGolden(t, goldenFixture(t), "prune", goldenNotes, "--yes", "--json")
		}, sizeMasks},
		{"prune-undo", func(t *testing.T) string {
			e := goldenFixture(t)
			goldenStep(t, e, "prune", goldenNotes, "--yes")
			return runGolden(t, e, "prune", "--undo", goldenNotes)
		}, nil},
		{"prune-no-selector", pushed("prune"), nil},

		{"backup-dry-run", func(t *testing.T) string {
			e := goldenFixture(t)
			return runGolden(t, e, "backup", "--out", filepath.Join(e.home, "backups"), "--dry-run")
		}, backupMasks},
		{"backup-dry-run-json", func(t *testing.T) string {
			e := goldenFixture(t)
			return runGolden(t, e, "backup", "--out", filepath.Join(e.home, "backups"), "--dry-run", "--json")
		}, backupMasks},
		{"backup", func(t *testing.T) string {
			e := goldenFixture(t)
			return runGolden(t, e, "backup", "--out", filepath.Join(e.home, "backups"))
		}, backupMasks},

		{"machines-forget", forgets("--forget", "LAP-2"), nil},
		{"machines-forget-json", forgets("--forget", "LAP-2", "--json"), nil},

		{"config-set-destination", func(t *testing.T) string {
			e := goldenFixture(t)
			return runGolden(t, e, "config", "set-destination", e.m.archive)
		}, nil},
		{"config-map", func(t *testing.T) string {
			e := goldenFixture(t)
			return runGolden(t, e, "config", "map", claude.Slug(e.repo), mkdir(t, e.home, "dev", "web-app"))
		}, nil},
		{"config-unmap", func(t *testing.T) string {
			e := goldenFixture(t)
			goldenStep(t, e, "config", "map", claude.Slug(e.repo), mkdir(t, e.home, "dev", "web-app"))
			return runGolden(t, e, "config", "unmap", claude.Slug(e.repo))
		}, nil},
		{"config-set-machine", pushed("config", "set-machine", "LAP-2"), nil},
		{"config-set-machine-bad", pushed("config", "set-machine", ".."), nil},
		{"config-work-off", pushed("config", "work", "off"), nil},
		{"config-work-placement", pushed("config", "work-placement", "machine"), nil},
		{"config-map-folder", func(t *testing.T) string {
			e := goldenFixture(t)
			return runGolden(t, e, "config", "map-folder", "/Users/you/reports", mkdir(t, e.home, "reports"))
		}, nil},
		{"config-unmap-folder", func(t *testing.T) string {
			e := goldenFixture(t)
			goldenStep(t, e, "config", "map-folder", "/Users/you/reports", mkdir(t, e.home, "reports"))
			return runGolden(t, e, "config", "unmap-folder", "/Users/you/reports")
		}, nil},
		{"config-exclude-folder", func(t *testing.T) string {
			e := goldenFixture(t)
			return runGolden(t, e, "config", "exclude-folder", mkdir(t, e.home, "reports"))
		}, nil},
		{"config-unexclude-folder", func(t *testing.T) string {
			e := goldenFixture(t)
			goldenStep(t, e, "config", "exclude-folder", mkdir(t, e.home, "reports"))
			return runGolden(t, e, "config", "unexclude-folder", filepath.Join(e.home, "reports"))
		}, nil},
		{"config-zip-sessions-off", pushed("config", "zip-sessions", "off"), nil},
		{"config-keep", pushed("config", "keep", "web-app"), nil},
		{"config-keep-json", pushed("config", "keep", "web-app", "--json"), nil},
		{"config-unkeep", func(t *testing.T) string {
			e := goldenFixture(t)
			goldenStep(t, e, "config", "keep", "web-app")
			return runGolden(t, e, "config", "unkeep", "web-app")
		}, nil},
		{"config-keep-days", pushed("config", "keep-days", "365"), nil},
		{"config-keep-days-too-low", pushed("config", "keep-days", "30"), nil},
	} {
		c := c
		t.Run(c.name, func(t *testing.T) {
			golden.Check(t, c.name, applyMasks(c.run(t), c.masks))
		})
	}
}

// goldenRestore files a copy of web-app's archived folder at <HOME>/dev/web-app.
func goldenRestore(t *testing.T, args ...string) string {
	t.Helper()
	e := goldenFixture(t)
	src := filepath.Join(e.home, "restore-from")
	copyTree(t, filepath.Join(e.m.archive, "projects", claude.Slug(e.repo)), src)
	return runGolden(t, e, append([]string{"restore", "--source", src,
		"--project-path", mkdir(t, e.home, "dev", "web-app"), "--rewrite-from", e.repo}, args...)...)
}

// goldenTwoCopies is LAP-2 after it imported web-app, went on with its first
// conversation and pushed: LAP-2's copy holds LAP-1's in whole.
func goldenTwoCopies(t *testing.T) goldenEnv {
	t.Helper()
	e := goldenFixture(t)
	l := goldenLAP2(t, e)
	if out := lap2Import(t, e, l); !strings.Contains(out, "\nexit 0\n") {
		t.Fatalf("fixture: %s", out)
	}
	addTurn(t, l, l.repo, goldenWeb1, "w1")
	goldenStep(t, l, "push")
	return l
}

// goldenAged is goldenFixture with notes and web-app's second conversation last used
// on 2026-08-30 and its first yesterday, so --older-than 30d picks the same two on any
// day.
func goldenAged(t *testing.T) goldenEnv {
	t.Helper()
	e := goldenSetup(t)
	yesterday := time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	for p, at := range map[string]string{
		filepath.Join(claude.Slug(e.repo), goldenWeb1):   yesterday,
		filepath.Join(claude.Slug(e.repo), goldenWeb2):   "2026-08-30T09:00:00.000Z",
		filepath.Join(claude.Slug(e.notes), goldenNotes): "2026-08-30T09:00:00.000Z",
	} {
		p = filepath.Join(e.m.claudeDir, "projects", p+".jsonl")
		mustWrite(t, p, strings.ReplaceAll(mustRead(t, p), "2026-11-20T05:00:00.000Z", at))
	}
	pinTimes(t, e.m.claudeDir, e.home)
	goldenStep(t, e, "push")
	return e
}

func mkdir(t *testing.T, elem ...string) string {
	t.Helper()
	p := filepath.Join(elem...)
	mustMkdir(t, p)
	return p
}

func mustRemove(t *testing.T, p string) {
	t.Helper()
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
}
