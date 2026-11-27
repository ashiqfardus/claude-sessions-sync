package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/convzip"
)

func writeLog(t *testing.T, lines ...string) string {
	t.Helper()
	root := t.TempDir()
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(root, "session-sync.log"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// A sweep refused by macOS privacy settings fails while the terminal - where doctor
// runs - can still read the archive, so doctor has to learn it from the log (#6).
func TestDeniedSincePushFindsAPrivacyRefusal(t *testing.T) {
	root := writeLog(t,
		"2026-11-20 09:50:18  PUSH: 4 copied, 2668 unchanged, 161 session(s), 7 project(s) -> /a",
		"2026-11-20 10:20:42  SKIP: open /Users/me/Library/CloudStorage/GoogleDrive-me/My Drive/claude-sessions: operation not permitted",
	)
	line, ok := deniedSincePush(root)
	if !ok || !strings.Contains(line, "operation not permitted") {
		t.Fatalf("deniedSincePush = %q, %v; want the refused line", line, ok)
	}
}

// A refusal that a later sweep got past is history, not a problem.
func TestDeniedSincePushIgnoresARefusalBeforeTheLastPush(t *testing.T) {
	root := writeLog(t,
		"2026-11-20 10:20:42  SKIP: open /a: operation not permitted",
		"2026-11-20 10:50:49  PUSH: 1 copied, 2689 unchanged, 161 session(s), 7 project(s) -> /a"+sweepMark,
	)
	if line, ok := deniedSincePush(root); ok {
		t.Fatalf("deniedSincePush = %q; want nothing after a successful push", line)
	}
}

// A release build says "0.7.0" and a `go install` build says "v0.7.0": one release,
// so doctor must not warn. A different release still warns.
func TestOtherVersionsIgnoresALeadingV(t *testing.T) {
	dest := t.TempDir()
	t.Setenv("CLAUDE_SESSIONS_MACHINE", "LAP-1")
	old := version
	t.Cleanup(func() { version = old })
	for _, s := range []archive.Shard{
		{Machine: "LAP-1", ToolVersion: "0.7.0"},
		{Machine: "LAP-2", ToolVersion: "v0.7.0"},
		{Machine: "LAP-3", ToolVersion: "0.7.0"},
		{Machine: "LAP-4", ToolVersion: "0.7.1"},
	} {
		writeShard(t, dest, s)
	}
	for _, v := range []string{"0.7.0", "v0.7.0"} {
		version = v
		if got, want := otherVersions(dest), []string{"LAP-4 (0.7.1)"}; !reflect.DeepEqual(got, want) {
			t.Errorf("version %s: otherVersions = %v, want %v", v, got, want)
		}
	}
}

// push zips nothing while session-sync.json cannot be read, so doctor must not say it
// does.
func TestZipCheckSaysWhenTheConfigCannotBeRead(t *testing.T) {
	root, dest := t.TempDir(), t.TempDir()
	if err := os.WriteFile(archive.ConfigPath(root), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := zipCheck(root, dest)
	if c.Level != levelWarn || !strings.Contains(c.Detail, "not zipped") || !strings.Contains(c.Detail, archive.ConfigPath(root)) {
		t.Errorf("zipCheck = %+v; want a warning naming the unreadable config", c)
	}
}

// With no home folder there is no Claude folder to check: the report says so, rather
// than coming back empty, which the panel's Health read as all fine.
func TestDoctorReportsAMissingHomeFolder(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	t.Setenv("home", "") // plan9
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if _, err := os.UserHomeDir(); err == nil {
		t.Skip("this OS finds a home folder without HOME")
	}
	rep, err := runDoctor(quietStreams(), doctorOpts{NoProbe: true})
	if !errors.Is(err, errChecksFailed) {
		t.Errorf("err = %v, want errChecksFailed", err)
	}
	if len(rep.Checks) != 1 || rep.Checks[0].Name != "claude root" || rep.Checks[0].Level != levelFail || rep.OK {
		t.Errorf("report %+v; want one failed claude root check", rep)
	}
}

// E2E M6: a conversation just brought in from the archive sits in this computer's folder
// for its project, and the archive holds it in the other computer's. The archived check,
// light or full, counts it as archived, loose or zipped there, as push leaves it to that
// copy; once continued here it is not.
func TestArchivedCheckCountsACopyInAnotherBucket(t *testing.T) {
	t.Setenv("CLAUDE_SESSIONS_MACHINE", "LAP-2")
	root, dest := t.TempDir(), t.TempDir()
	const id = "44444444-4444-4444-4444-444444444444"
	theirs := filepath.Join(archive.ProjectsDir(dest), "-home-a-web-app", id+".jsonl")
	mine := filepath.Join(claude.ProjectsDir(root), "-Users-b-dev-web-app", id+".jsonl")
	writeFile(t, theirs, `{"cwd":"/home/a/web-app"}`+"\n")
	writeFile(t, mine, `{"cwd":"/Users/b/dev/web-app"}`+"\n") // repointed: another size
	at := time.Now().Add(-3 * time.Hour)
	os.Chtimes(theirs, at, at)
	brought := at.Add(-(archive.MTimeToleranceSeconds + 1) * time.Second) // olderThanArchive
	os.Chtimes(mine, brought, brought)
	got := func() level {
		t.Helper()
		bs, err := claude.ListBuckets(claude.ProjectsDir(root))
		if err != nil {
			t.Fatal(err)
		}
		light, full := archivedCheck(root, dest, bs, true), archivedCheck(root, dest, bs, false)
		if light.Level != full.Level {
			t.Errorf("light %+v, full %+v", light, full)
		}
		return light.Level
	}
	if l := got(); l != levelOK {
		t.Errorf("brought in, loose there: %s", l)
	}
	if _, err := convzip.Zip(filepath.Dir(theirs), id); err != nil {
		t.Fatal(err)
	}
	if l := got(); l != levelOK {
		t.Errorf("brought in, zipped there: %s", l)
	}
	later := time.Now().Add(-time.Hour) // continued here, an hour ago
	os.Chtimes(mine, later, later)
	if l := got(); l != levelFail {
		t.Errorf("continued here: %s", l)
	}
}
