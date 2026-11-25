package main_test

// Four computers on one archive: a Mac, two Windows laptops and a Linux box, each with
// web-app at its own path and the same git remote, the two Windows laptops also
// sharing E:\api and E:\reports at identical paths. The cases no two-computer test
// reaches: every pair of layouts, three-way forks, a bucket two computers push to, and
// the archive-wide gates with four shards.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/convzip"
)

const webRemote = "git@github.com:example/web-app.git"

// box is one of the four computers. Its own folders exist only while it runs: one
// filesystem stands in for four, so another computer's checkout is never found at its
// recorded path, and each Windows laptop has its own E:\api and E:\reports.
type box struct {
	machine
	home   string   // its home folder, always there
	web    string   // its web-app checkout
	search string   // where its import looks for checkouts
	own    []string // its folders, kept in stash while another computer runs
	stash  string
}

type four struct {
	archive                string
	mac, winA, winB, linux box
	all                    []box
	api, reports           string // the paths both Windows laptops have
}

// newFour lays out the matrix under one temp folder, each computer's paths shaped as
// on its own system: the Mac at ~/dev/web-app, Windows A at E:\web-app, Windows B at
// D:\work\web-app, Linux at /home/you/src/web-app. They are real folders, so with this
// system's separators: TestImportMatchesByGitRemote files a real D:\ path on POSIX.
func newFour(t *testing.T) four {
	t.Helper()
	root := t.TempDir()
	at := func(parts ...string) string { return filepath.Join(append([]string{root}, parts...)...) }
	f := four{archive: at("archive"), api: at("E", "api"), reports: at("E", "reports")}
	mustMkdir(t, f.archive)
	mk := func(name, home, web, search string, more ...string) box {
		b := box{machine: newMachine(t, name, f.archive), home: home, web: web, search: search, stash: at("stash", name)}
		b.own = append([]string{web}, more...)
		mustMkdir(t, b.home)
		mustMkdir(t, b.stash)
		b.at(t, func() {
			mustWrite(t, filepath.Join(web, ".git", "config"), "[remote \"origin\"]\n\turl = "+webRemote+"\n")
			for _, p := range more {
				if p == f.api {
					gitRepo(t, p)
				}
			}
		})
		return b
	}
	f.mac = mk("MAC", at("Users", "you"), at("Users", "you", "dev", "web-app"), at("Users", "you", "dev"))
	f.winA = mk("WIN-A", at("A", "Users", "you"), at("E", "web-app"), at("E"), f.api, f.reports)
	f.winB = mk("WIN-B", at("B", "Users", "you"), at("D", "work", "web-app"), at("D", "work"), f.api, f.reports)
	f.linux = mk("LINUX", at("home", "you"), at("home", "you", "src", "web-app"), at("home", "you", "src"))
	f.all = []box{f.mac, f.winA, f.winB, f.linux}
	return f
}

func (b box) stashed(i int) string { return filepath.Join(b.stash, strconv.Itoa(i)) }

// at runs fn with this computer's folders in place.
func (b box) at(t *testing.T, fn func()) {
	t.Helper()
	for i, p := range b.own {
		mustMkdir(t, filepath.Dir(p))
		if err := os.Rename(b.stashed(i), p); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		mustMkdir(t, p)
	}
	defer func() {
		for i, p := range b.own {
			if err := os.Rename(p, b.stashed(i)); err != nil {
				t.Error(err)
			}
		}
	}()
	fn()
}

// exec runs the binary as this computer, with its folders in place.
func (b box) exec(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out []byte
	var err error
	b.at(t, func() {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=", "CLAUDE_SESSIONS_MACHINE="+b.name, "HOME="+b.home, "USERPROFILE="+b.home,
			"CLAUDE_SESSIONS_TEMP_DIRS="+filepath.Join(b.home, "no-temp"))
		out, err = cmd.CombinedOutput()
	})
	return string(out), err
}

// cmd runs a command that takes --claude-dir and --archive.
func (b box) cmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return b.exec(t, append([]string{args[0], "--claude-dir", b.claudeDir, "--archive", b.archive}, args[1:]...)...)
}

func (b box) run(t *testing.T, args ...string) string {
	t.Helper()
	out, err := b.cmd(t, args...)
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", b.name, args, err, out)
	}
	return out
}

func (b box) config(t *testing.T, args ...string) string {
	t.Helper()
	out, err := b.exec(t, append([]string{"config", "--claude-dir", b.claudeDir}, args...)...)
	if err != nil {
		t.Fatalf("%s config %v: %v\n%s", b.name, args, err, out)
	}
	return out
}

func (f four) pushAll(t *testing.T) {
	t.Helper()
	for _, b := range f.all {
		b.run(t, "push", "--no-html")
	}
}

func (f four) importAll(t *testing.T) {
	t.Helper()
	for _, b := range f.all {
		b.run(t, "import", "--search-root", b.search, "--update", "--no-plugins")
	}
}

// turnClock dates each turn a test writes a millisecond after the one before, from an
// hour ago.
var turnClock atomic.Int64

func turnAt() string {
	n := turnClock.Add(1)
	return time.Now().UTC().Add(-time.Hour).Add(time.Duration(n) * time.Millisecond).Format(time.RFC3339Nano)
}

// turnLines is one user turn per uuid of conversation id, worked in cwd.
func turnLines(id, cwd string, uuids ...string) string {
	var s strings.Builder
	for _, u := range uuids {
		l, _ := json.Marshal(map[string]any{"type": "user", "uuid": u, "cwd": cwd, "sessionId": id, "timestamp": turnAt(),
			"message": map[string]any{"role": "user", "content": "turn " + u}})
		s.Write(append(l, '\n'))
	}
	return s.String()
}

// transcript is b's local copy of conversation id, worked in dir.
func (b box) transcript(dir, id string) string {
	return filepath.Join(b.claudeDir, "projects", claude.Slug(dir), id+".jsonl")
}

// replyClock spaces the replies a test writes 3 seconds apart, after the last push:
// two computers' copies of one length written within the 2-second mtime tolerance
// would pass for the same copy.
var replyClock atomic.Int64

// say adds turns to b's conversation id in dir, starting it if it is new, as a reply
// after the last push.
func (b box) say(t *testing.T, dir, id string, uuids ...string) {
	t.Helper()
	p := b.transcript(dir, id)
	old, _ := os.ReadFile(p)
	mustWrite(t, p, string(old)+turnLines(id, dir, uuids...))
	at := time.Now().Add(5*time.Minute + time.Duration(replyClock.Add(1))*3*time.Second)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

// holdsAll fails unless b has every turn among its conversations in dir.
func (b box) holdsAll(t *testing.T, dir string, turns ...string) {
	t.Helper()
	for _, turn := range turns {
		if !b.holds(t, dir, turn) {
			t.Errorf("%s lost turn %s: %v", b.name, turn, b.sessions(t, dir))
		}
	}
}

// copies lists every archived copy of conversation id: loose or zipped in any bucket,
// and forks.
func copies(t *testing.T, archive, id string) []string {
	t.Helper()
	var out []string
	for _, pat := range []string{
		filepath.Join(archive, "projects", "*", id+".jsonl"),
		filepath.Join(archive, "projects", "*", id+convzip.Suffix),
		filepath.Join(archive, "forks", "*", id, "*.jsonl"),
	} {
		m, _ := filepath.Glob(pat)
		out = append(out, m...)
	}
	return out
}

// olderBuild rewrites name's shard as a build without field would have left it.
func olderBuild(t *testing.T, archive, name, field string) {
	t.Helper()
	p := filepath.Join(archive, "manifest", name+".json")
	var s map[string]any
	if err := json.Unmarshal([]byte(mustRead(t, p)), &s); err != nil {
		t.Fatal(err)
	}
	delete(s, field)
	raw, _ := json.Marshal(s)
	mustWrite(t, p, string(raw))
}

func convID(group, n int) string {
	return fmt.Sprintf("%08x-0000-4000-8000-%012d", 0xaaaaaaaa+group, n)
}

// own gives each computer a conversation of its own in web-app, by name.
func (f four) own(t *testing.T) map[string]string {
	t.Helper()
	ids := map[string]string{}
	for i, b := range f.all {
		ids[b.name] = convID(0, i+1)
		b.say(t, b.web, ids[b.name], "t-"+strings.ToLower(b.name))
	}
	return ids
}

// Every computer files every other's conversation under its own web-app, its cwd
// rewritten to its own path for each pair: Windows A's to Windows B's (E: and D: are
// folders here, not drives), Windows to Linux and back, Mac to Linux.
func TestFourComputersFileEachOthersWork(t *testing.T) {
	f := newFour(t)
	ids := f.own(t)
	f.pushAll(t)
	f.importAll(t)
	for _, to := range f.all {
		for _, from := range f.all {
			if from.name == to.name {
				continue
			}
			p := to.transcript(to.web, ids[from.name])
			if _, err := os.Stat(p); err != nil {
				t.Errorf("%s -> %s: not filed under %s: %v", from.name, to.name, to.web, to.sessions(t, to.web))
				continue
			}
			if body := mustRead(t, p); !strings.Contains(body, `"cwd":`+quoted(to.web)) || strings.Contains(body, inJSON(from.web)) {
				t.Errorf("%s -> %s: cwd not rewritten to %s:\n%s", from.name, to.name, to.web, body)
			}
		}
	}
}

// Each computer imports and pushes twice: every conversation is archived once, in the
// bucket of the computer it began on.
func TestFourComputersArchiveEachConversationOnce(t *testing.T) {
	f := newFour(t)
	ids := f.own(t)
	f.winA.say(t, f.api, convID(1, 1), "api-a")
	f.winB.say(t, f.api, convID(1, 2), "api-b")
	for round := 0; round < 2; round++ {
		f.pushAll(t)
		f.importAll(t)
	}
	f.pushAll(t)
	all := []string{convID(1, 1), convID(1, 2)}
	for _, id := range ids {
		all = append(all, id)
	}
	for _, id := range all {
		if got := copies(t, f.archive, id); len(got) != 1 {
			t.Errorf("%s has %d archived copies, want 1: %v", id, len(got), got)
		}
	}
	for _, b := range f.all {
		want := filepath.Join(f.archive, "projects", claude.Slug(b.web), ids[b.name]+".jsonl")
		if _, err := os.Stat(want); err != nil {
			t.Errorf("%s's conversation is not in its own bucket: %v", b.name, err)
		}
		f.holdsOwn(t, b)
	}
	for _, b := range []box{f.winA, f.winB} {
		b.holdsAll(t, f.api, "api-a", "api-b")
	}
}

// holdsOwn fails unless b holds the turn own wrote for each computer.
func (f four) holdsOwn(t *testing.T, b box) {
	t.Helper()
	for _, o := range f.all {
		b.holdsAll(t, b.web, "t-"+strings.ToLower(o.name))
	}
}

// A conversation begun on the Mac goes on differently on three computers: every turn
// reaches every computer, the branches are kept as forks, and they stop multiplying.
func TestConversationContinuedOnThreeComputers(t *testing.T) {
	f := newFour(t)
	id := convID(0, 9)
	f.mac.say(t, f.mac.web, id, "u1", "m1")
	f.pushAll(t)
	f.importAll(t)
	f.winA.say(t, f.winA.web, id, "a1")
	f.winB.say(t, f.winB.web, id, "b1")
	f.linux.say(t, f.linux.web, id, "l1")
	f.pushAll(t)
	f.importAll(t)
	f.pushAll(t)
	f.importAll(t)
	counts := map[string]int{}
	for _, b := range f.all {
		b.holdsAll(t, b.web, "u1", "m1", "a1", "b1", "l1")
		counts[b.name] = len(b.sessions(t, b.web))
	}
	for _, turn := range []string{"a1", "b1", "l1"} {
		found := false
		for _, c := range copies(t, f.archive, id) {
			found = found || strings.Contains(mustRead(t, c), `"`+turn+`"`)
		}
		if !found {
			t.Errorf("no archived copy holds %s", turn)
		}
	}
	// The conversation and a fork of each other branch. One that took a branch by
	// --update may also get another computer's fork of that branch: a session of its own.
	f.pushAll(t)
	f.importAll(t)
	for _, b := range f.all {
		if got := b.sessions(t, b.web); len(got) != counts[b.name] || len(got) < 3 {
			t.Errorf("%s holds %v after another round (had %d), want the conversation and two forks", b.name, got, counts[b.name])
		}
	}
}

// E:\api on both Windows laptops is one bucket: their conversations sit side by side,
// and one continued on both becomes a fork, not an overwrite.
func TestTwoWindowsShareABucket(t *testing.T) {
	f := newFour(t)
	p, q := convID(1, 1), convID(1, 2)
	f.winA.say(t, f.api, p, "p1")
	f.winB.say(t, f.api, q, "q1")
	f.winA.run(t, "push", "--no-html")
	f.winB.run(t, "push", "--no-html")
	bucket := filepath.Join(f.archive, "projects", claude.Slug(f.api))
	for _, id := range []string{p, q} {
		if _, err := os.Stat(filepath.Join(bucket, id+".jsonl")); err != nil {
			t.Fatalf("%s is not in the shared bucket: %v", id, err)
		}
	}
	for _, b := range []box{f.winA, f.winB} {
		b.run(t, "import", "--search-root", b.search, "--update", "--no-plugins")
		b.holdsAll(t, f.api, "p1", "q1")
	}

	f.winA.say(t, f.api, p, "a2")
	f.winB.say(t, f.api, p, "b2")
	f.winA.run(t, "push", "--no-html")
	f.winB.run(t, "push", "--no-html")
	if got := mustRead(t, filepath.Join(bucket, p+".jsonl")); !strings.Contains(got, `"a2"`) || strings.Contains(got, `"b2"`) {
		t.Errorf("the archived copy is not A's:\n%s", got)
	}
	fork := filepath.Join(f.archive, "forks", claude.Slug(f.api), p, "WIN-B.jsonl")
	if _, err := os.Stat(fork); err != nil || !strings.Contains(mustRead(t, fork), `"b2"`) {
		t.Fatalf("B's branch is not kept as a fork: %v", err)
	}
	if got := copies(t, f.archive, q); len(got) != 1 {
		t.Errorf("the other conversation has copies %v", got)
	}
	for _, b := range []box{f.winA, f.winB, f.winA} {
		b.run(t, "import", "--search-root", b.search, "--update", "--no-plugins")
		b.holdsAll(t, f.api, "p1", "q1", "a2", "b2")
	}
}

// workLines is a turn of conversation id in cwd that writes file.
func workLines(id, cwd, uuid, file string) string {
	l, _ := json.Marshal(map[string]any{"type": "assistant", "uuid": uuid, "cwd": cwd, "sessionId": id, "timestamp": turnAt(),
		"message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "name": "Write",
			"input": map[string]string{"file_path": file, "content": "x"}}}}})
	return turnLines(id, cwd, uuid+"-ask") + string(l) + "\n"
}

// E:\reports, a work folder on both Windows laptops, is archived under each. A's comes
// to B, where that path is B's own, under B's home (today's rule): B's files there are
// never replaced, nor is B's copy of A's once B has a newer one.
func TestTwoWindowsSameWorkFolder(t *testing.T) {
	f := newFour(t)
	report := filepath.Join(f.reports, "report.md")
	for i, b := range []box{f.winA, f.winB} {
		id := convID(2, i+1)
		mustWrite(t, b.transcript(f.reports, id), workLines(id, f.reports, "r-"+b.name, report))
		b.at(t, func() { mustWrite(t, report, b.name+"'s report\n") })
		b.run(t, "push", "--no-html")
	}
	for _, b := range []box{f.winA, f.winB} {
		if got := mustRead(t, filepath.Join(workCopy(b.machine, f.reports), "report.md")); got != b.name+"'s report\n" {
			t.Errorf("%s's report is archived as %q", b.name, got)
		}
	}
	b := f.winB
	b.run(t, "import", "--search-root", b.search, "--no-plugins")
	placed := fileUnder(t, filepath.Join(b.home, "claude-work"), "report.md")
	if got := mustRead(t, placed); got != "WIN-A's report\n" {
		t.Fatalf("A's report reached B as %q", got)
	}

	// B edits its copy of A's report; A's next version is newer than A's first, older
	// than B's edit.
	mustWrite(t, placed, "B's edit\n")
	agePaths(t, -10*time.Minute, placed)
	f.winA.at(t, func() {
		mustWrite(t, report, "WIN-A's report, second version\n")
		agePaths(t, -5*time.Minute, report)
	})
	f.winA.run(t, "push", "--no-html")
	if got := mustRead(t, filepath.Join(workCopy(f.winA.machine, f.reports), "report.md")); got != "WIN-A's report, second version\n" {
		t.Fatalf("A's second version is not archived: %q", got)
	}
	for _, args := range [][]string{{}, {"--update"}} {
		b.run(t, append([]string{"import", "--search-root", b.search, "--no-plugins"}, args...)...)
		if got := mustRead(t, placed); got != "B's edit\n" {
			t.Errorf("import %v replaced B's newer copy: %q", args, got)
		}
		b.at(t, func() {
			if got := mustRead(t, report); got != "WIN-B's report\n" {
				t.Errorf("import %v replaced B's own report: %q", args, got)
			}
		})
	}
}

// fileUnder is the one file named name under dir.
func fileUnder(t *testing.T, dir, name string) string {
	t.Helper()
	var found []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == name {
			found = append(found, p)
		}
		return nil
	})
	if len(found) != 1 {
		t.Fatalf("%s under %s: %v", name, dir, found)
	}
	return found[0]
}

// idle ages b's own conversation, here and archived, past the zip threshold.
func (f four) idle(t *testing.T, b box, id string) {
	t.Helper()
	bucket := filepath.Join(f.archive, "projects", claude.Slug(b.web))
	agePaths(t, 31*day, b.transcript(b.web, id), filepath.Join(bucket, id+".jsonl"), filepath.Join(bucket, id))
}

func (f four) zipped(b box, id string) bool {
	bucket := filepath.Join(f.archive, "projects", claude.Slug(b.web))
	return fileExists(convzip.Path(bucket, id)) && !fileExists(filepath.Join(bucket, id+".jsonl"))
}

// One computer on a build without readsZips, then without honorsDeleted: nothing is
// zipped, and nothing pruned, by any of the other three until it is forgotten.
func TestFourComputersGateAndForget(t *testing.T) {
	f := newFour(t)
	ids := f.own(t)
	f.pushAll(t)
	f.importAll(t)
	f.pushAll(t)
	olderBuild(t, f.archive, "LINUX", "readsZips")
	for _, b := range f.all {
		f.idle(t, b, ids[b.name])
	}
	for _, b := range f.all[:3] {
		b.run(t, "push", "--no-html")
		if f.zipped(b, ids[b.name]) {
			t.Fatalf("%s zipped while LINUX does not read zips", b.name)
		}
	}
	out, err := f.mac.cmd(t, "doctor")
	if detail, advice := doctorZips(out); !strings.HasSuffix(detail, "zips             idle conversations are not zipped until these machines read zips: LINUX") ||
		!strings.Contains(advice, "machines --forget") {
		t.Errorf("doctor does not name LINUX as the one zipping waits for:\n%s", out)
	}
	f.mac.run(t, "machines", "--forget", "LINUX")
	f.pushAll(t) // LINUX's push brings its shard back, reading zips
	for _, b := range f.all {
		if !f.zipped(b, ids[b.name]) {
			t.Errorf("%s did not zip once LINUX was forgotten", b.name)
		}
	}
	f.importAll(t)
	for _, b := range f.all {
		f.holdsOwn(t, b)
	}

	olderBuild(t, f.archive, "LINUX", "honorsDeleted")
	out, err = f.mac.cmd(t, "prune", "--yes", ids["MAC"])
	if err == nil || !strings.Contains(out, "LINUX") || !strings.Contains(out, "machines --forget") {
		t.Errorf("prune --yes did not refuse for LINUX (%v):\n%s", err, out)
	}
	if len(copies(t, f.archive, ids["MAC"])) == 0 {
		t.Fatal("prune deleted while LINUX would upload it again")
	}
	f.mac.run(t, "machines", "--forget", "LINUX")
	f.mac.run(t, "prune", "--yes", ids["MAC"])
	if got := copies(t, f.archive, ids["MAC"]); len(got) != 0 {
		t.Fatalf("prune left %v", got)
	}
	f.pushAll(t)
	f.pushAll(t)
	if got := copies(t, f.archive, ids["MAC"]); len(got) != 0 {
		t.Errorf("a computer uploaded the pruned conversation again: %v", got)
	}
	f.importAll(t)
	for _, b := range f.all { // the pruned one stays where it was
		f.holdsOwn(t, b)
	}
}

// A project kept by its remote is kept in all four computers' buckets, the one a
// computer adds after the keep included.
func TestKeepByRemoteCoversFourBuckets(t *testing.T) {
	f := newFour(t)
	ids := f.own(t)
	for _, b := range f.all[:3] {
		b.run(t, "push", "--no-html")
	}
	f.mac.config(t, "set-destination", f.archive)
	out := f.mac.config(t, "keep", "web-app")
	for _, b := range f.all[:3] {
		if !strings.Contains(out, claude.Slug(b.web)) {
			t.Errorf("keep does not list %s's bucket:\n%s", b.name, out)
		}
	}
	f.linux.run(t, "push", "--no-html")
	if out := f.mac.run(t, "prune", "--yes", "--project", "web-app"); !strings.Contains(out, "No archived conversation to delete") ||
		!strings.Contains(out, "4 kept") {
		t.Errorf("prune --project web-app does not keep all four:\n%s", out)
	}
	if out := f.mac.run(t, "prune", "--yes", ids["LINUX"]); !strings.Contains(out, "1 kept") {
		t.Errorf("LINUX's conversation, added after the keep, is not kept:\n%s", out)
	}
	for _, id := range ids {
		if got := copies(t, f.archive, id); len(got) != 1 {
			t.Errorf("%s: %v", id, got)
		}
	}
}

// A conversation from the Mac continued on both Windows laptops has a copy in three
// buckets. prune deletes all three, no computer uploads it again, and every
// computer's own copy stays.
func TestPruneRemovesEveryBucketsCopy(t *testing.T) {
	f := newFour(t)
	id := convID(0, 9)
	f.mac.say(t, f.mac.web, id, "u1")
	f.pushAll(t)
	f.importAll(t)
	f.winA.say(t, f.winA.web, id, "a1")
	f.winB.say(t, f.winB.web, id, "b1")
	f.pushAll(t)
	if got := copies(t, f.archive, id); len(got) != 3 {
		t.Fatalf("setup: copies %v, want the Mac's and each laptop's", got)
	}
	f.linux.run(t, "prune", "--yes", id)
	if got := copies(t, f.archive, id); len(got) != 0 {
		t.Fatalf("prune left %v", got)
	}
	f.pushAll(t)
	f.importAll(t)
	f.pushAll(t)
	if got := copies(t, f.archive, id); len(got) != 0 {
		t.Errorf("uploaded again: %v", got)
	}
	for _, b := range f.all {
		if _, err := os.Stat(b.transcript(b.web, id)); err != nil {
			t.Errorf("%s's own copy went: %v", b.name, err)
		}
	}
}

// With four shards clean deletes only copies another holds in whole: the Mac's of a
// conversation continued on Windows A, and of one continued on Windows B and Linux.
// The two branches of that one, and both of one continued on both laptops in their
// shared bucket, stay.
func TestCleanWithFourShards(t *testing.T) {
	f := newFour(t)
	one, two, shared := convID(0, 1), convID(0, 2), convID(1, 1)
	f.mac.say(t, f.mac.web, one, "u1")
	f.mac.say(t, f.mac.web, two, "v1")
	f.winA.say(t, f.api, shared, "s1")
	f.pushAll(t)
	f.importAll(t)
	f.winA.say(t, f.winA.web, one, "a1")
	f.winB.say(t, f.winB.web, two, "b1")
	f.linux.say(t, f.linux.web, two, "l1")
	f.winA.say(t, f.api, shared, "a2")
	f.winB.say(t, f.api, shared, "b2")
	f.pushAll(t)
	before := map[string][]string{one: copies(t, f.archive, one), two: copies(t, f.archive, two), shared: copies(t, f.archive, shared)}
	if len(before[one]) != 2 || len(before[two]) != 3 || len(before[shared]) != 2 {
		t.Fatalf("setup: %v", before)
	}

	f.linux.run(t, "clean", "--yes")
	macBucket := filepath.Join(f.archive, "projects", claude.Slug(f.mac.web))
	for id, n := range map[string]int{one: 1, two: 2, shared: 2} {
		got := copies(t, f.archive, id)
		if len(got) != n || slices.ContainsFunc(got, func(c string) bool { return strings.HasPrefix(c, macBucket) && id != shared }) {
			t.Errorf("%s: clean left %v, want %d without the Mac's", id, got, n)
		}
	}
	f.pushAll(t)
	f.importAll(t)
	for _, b := range f.all {
		b.holdsAll(t, b.web, "u1", "a1", "v1", "b1", "l1")
	}
	for _, b := range []box{f.winA, f.winB} {
		b.holdsAll(t, f.api, "s1", "a2", "b2")
	}
	if got := copies(t, f.archive, one); len(got) != 1 {
		t.Errorf("the Mac's copy came back: %v", got)
	}
}

// shardOf is the fields of a computer's shard these tests read.
type shardOf struct {
	LastPush  time.Time `json:"lastPush"`
	InstallID string    `json:"installId"`
}

func readShardOf(t *testing.T, archive, name string) (shardOf, string) {
	t.Helper()
	raw := mustRead(t, filepath.Join(archive, "manifest", name+".json"))
	var s shardOf
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	return s, raw
}

// The shard records the hour of the last full push, so it changes at most once an
// hour, and `machines` shows it.
func TestLastPushWrittenHourly(t *testing.T) {
	archive := t.TempDir()
	m := newMachine(t, "LAP-2", archive)
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	mustWrite(t, m.session(repo), sessionLines(repo, "u1"))

	var first string
	var s shardOf
	for try := 0; ; try++ { // two pushes in one hour, unless the clock crossed one
		hour := time.Now().UTC().Truncate(time.Hour)
		m.run(t, "push", "--no-html")
		s, first = readShardOf(t, archive, "LAP-2")
		m.run(t, "push", "--no-html")
		_, second := readShardOf(t, archive, "LAP-2")
		if now := time.Now().UTC().Truncate(time.Hour); now != hour && try == 0 {
			continue
		}
		if !s.LastPush.Equal(hour) || s.LastPush.Location() != time.UTC {
			t.Fatalf("lastPush = %v, want %v in UTC", s.LastPush, hour)
		}
		if second != first {
			t.Fatalf("a second push in the same hour changed the shard:\n%s\n---\n%s", first, second)
		}
		break
	}

	// Written three hours ago: the next push records this hour.
	old := s.LastPush.Add(-3 * time.Hour).Format(time.RFC3339)
	shard := filepath.Join(archive, "manifest", "LAP-2.json")
	mustWrite(t, shard, strings.Replace(first, `"lastPush": "`+s.LastPush.Format(time.RFC3339)+`"`, `"lastPush": "`+old+`"`, 1))
	m.hook(t, m.session(repo))
	if now, _ := readShardOf(t, archive, "LAP-2"); now.LastPush.Format(time.RFC3339) != old {
		t.Fatalf("the per-reply hook wrote lastPush: %v", now.LastPush)
	}
	m.run(t, "push", "--no-html")
	now, _ := readShardOf(t, archive, "LAP-2")
	if now.LastPush.Before(s.LastPush) {
		t.Fatalf("lastPush not updated: %v", now.LastPush)
	}
	if out := m.run(t, "machines"); !strings.Contains(out, "LAST BACKUP") || !strings.Contains(out, now.LastPush.Local().Format("2006-01-02 15:04")) {
		t.Errorf("machines does not show the last backup %v:\n%s", now.LastPush.Local(), out)
	}
}

var installIDForm = regexp.MustCompile(`^[0-9a-f]{32}$`)

const clashMessage = "Another computer also uses the name LAP-2. Give one of them its own name with `claude-sessions config set-machine`."

// Two installs under one name overwrite each other's shard: the second to push warns,
// still pushes, and the first's doctor fails. One install pushing again never warns.
func TestSameNameWarns(t *testing.T) {
	archive := t.TempDir()
	first, second := newMachine(t, "LAP-2", archive), newMachine(t, "LAP-2", archive)
	for _, m := range []machine{first, second} {
		repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
		mustWrite(t, m.session(repo), sessionLines(repo, "u1"))
	}
	for i := 0; i < 2; i++ {
		if out := first.run(t, "push", "--no-html"); strings.Contains(out, "Another computer") {
			t.Fatalf("one install pushing again warned:\n%s", out)
		}
	}
	idOf := func(m machine) string {
		var c struct {
			InstallID string `json:"installId"`
		}
		json.Unmarshal([]byte(mustRead(t, filepath.Join(m.claudeDir, "session-sync.json"))), &c)
		return c.InstallID
	}
	if s, _ := readShardOf(t, archive, "LAP-2"); !installIDForm.MatchString(idOf(first)) || s.InstallID != idOf(first) {
		t.Fatalf("install id %q, shard has %q", idOf(first), s.InstallID)
	}
	if out := first.doctor(t); strings.Contains(out, "Another computer") {
		t.Errorf("doctor warned with no clash:\n%s", out)
	}

	out := second.run(t, "push", "--no-html")
	if !strings.Contains(out, "WARN: "+clashMessage) {
		t.Errorf("the second install did not warn:\n%s", out)
	}
	if s, _ := readShardOf(t, archive, "LAP-2"); idOf(second) == idOf(first) || s.InstallID != idOf(second) {
		t.Errorf("the second install did not push its own id: %q, shard %q", idOf(second), s.InstallID)
	}
	cmd := exec.Command(binary, "doctor", "--claude-dir", first.claudeDir, "--archive", archive)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=", "CLAUDE_SESSIONS_MACHINE=LAP-2")
	raw, err := cmd.CombinedOutput()
	if err == nil || !regexp.MustCompile(`(?m)^\s+x \S.*`+regexp.QuoteMeta(clashMessage)).Match(raw) {
		t.Errorf("doctor did not fail on the clash (%v):\n%s", err, raw)
	}
}

// A dry run makes no install ID and writes no shard, so no lastPush.
func TestDryRunMakesNoInstallID(t *testing.T) {
	archive := t.TempDir()
	m := newMachine(t, "LAP-2", archive)
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	mustWrite(t, m.session(repo), sessionLines(repo, "u1"))
	m.run(t, "push", "--no-html", "--dry-run")
	if raw, err := os.ReadFile(filepath.Join(m.claudeDir, "session-sync.json")); err == nil && strings.Contains(string(raw), "installId") {
		t.Errorf("a dry run made an install id:\n%s", raw)
	}
	if fileExists(filepath.Join(archive, "manifest", "LAP-2.json")) {
		t.Error("a dry run wrote the shard")
	}
}

// otherInstall leaves a shard for LAP-2 written by another install.
func otherInstall(t *testing.T, archive string) {
	t.Helper()
	mustWrite(t, filepath.Join(archive, "manifest", "LAP-2.json"),
		`{"schemaVersion":1,"machine":"LAP-2","projects":{},"installId":"ffffffffffffffffffffffffffffffff"}`)
}

// A session-sync.json that cannot be read gives no ID to compare: no warning, from push
// or doctor, and it is not overwritten.
func TestUnreadableConfigNeverWarns(t *testing.T) {
	archive := t.TempDir()
	m := newMachine(t, "LAP-2", archive)
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	mustWrite(t, m.session(repo), sessionLines(repo, "u1"))
	cfg := filepath.Join(m.claudeDir, "session-sync.json")
	mustWrite(t, cfg, "{not json")
	otherInstall(t, archive)
	if out := m.run(t, "push", "--no-html"); strings.Contains(out, "Another computer") {
		t.Errorf("push warned with no install id of its own:\n%s", out)
	}
	if out := m.doctor(t); strings.Contains(out, "Another computer") {
		t.Errorf("doctor warned with no install id of its own:\n%s", out)
	}
	if got := mustRead(t, cfg); got != "{not json" {
		t.Errorf("the unreadable config was replaced: %q", got)
	}
}

// A shard for this name that does not parse holds no ID: no warning, and push replaces
// it with its own.
func TestCorruptShardNeverWarns(t *testing.T) {
	archive := t.TempDir()
	m := newMachine(t, "LAP-2", archive)
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	mustWrite(t, m.session(repo), sessionLines(repo, "u1"))
	mustWrite(t, filepath.Join(archive, "manifest", "LAP-2.json"), "{garbage")
	if out := m.run(t, "push", "--no-html"); strings.Contains(out, "Another computer") {
		t.Errorf("push warned over a corrupt shard:\n%s", out)
	}
	if s, _ := readShardOf(t, archive, "LAP-2"); !installIDForm.MatchString(s.InstallID) {
		t.Errorf("push did not write its own id over the corrupt shard: %q", s.InstallID)
	}
	if out := m.doctor(t); strings.Contains(out, "Another computer") {
		t.Errorf("doctor warned:\n%s", out)
	}
}

// A shard from an older build, or one with a zero time, shows "-" as its last backup,
// in machines and in doctor.
func TestOlderShardShowsNoLastBackup(t *testing.T) {
	archive := t.TempDir()
	m := newMachine(t, "LAP-2", archive)
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	mustWrite(t, m.session(repo), sessionLines(repo, "u1"))
	m.run(t, "push", "--no-html")
	mustWrite(t, filepath.Join(archive, "manifest", "OLD-BOX.json"), `{"schemaVersion":1,"machine":"OLD-BOX","projects":{}}`)
	mustWrite(t, filepath.Join(archive, "manifest", "ZERO-BOX.json"),
		`{"schemaVersion":1,"machine":"ZERO-BOX","projects":{},"lastPush":"0001-01-01T00:00:00Z"}`)
	s, _ := readShardOf(t, archive, "LAP-2")
	here := s.LastPush.Local().Format("2006-01-02 15:04")

	rows := map[string][]string{}
	for _, l := range strings.Split(m.run(t, "machines"), "\n") {
		if f := strings.Fields(l); len(f) >= 4 {
			rows[f[0]] = f
		}
	}
	for name, want := range map[string]string{"OLD-BOX": "-", "ZERO-BOX": "-", "LAP-2": strings.Fields(here)[0]} {
		if r := rows[name]; len(r) < 4 || r[3] != want {
			t.Errorf("machines shows %s as %v, want last backup %s", name, r, want)
		}
	}
	out := m.doctor(t)
	for _, want := range []string{"last backup      LAP-2 " + here, "last backup      OLD-BOX -", "last backup      ZERO-BOX -"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor lacks %q:\n%s", want, out)
		}
	}
}

// backup leaves the install ID out, so a restored backup makes a new one. Restored on
// the same computer, its next push warns once about its own old ID, then no more.
func TestBackupLeavesInstallIDOut(t *testing.T) {
	archive := t.TempDir()
	m := newMachine(t, "LAP-2", archive)
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	mustWrite(t, m.session(repo), sessionLines(repo, "u1"))
	m.run(t, "push", "--no-html")
	cfg := filepath.Join(m.claudeDir, "session-sync.json")
	if !strings.Contains(mustRead(t, cfg), `"installId"`) {
		t.Fatalf("setup: no install id:\n%s", mustRead(t, cfg))
	}

	out := t.TempDir()
	cmd := exec.Command(binary, "backup", "--claude-dir", m.claudeDir, "--out", out)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=", "CLAUDE_SESSIONS_MACHINE=LAP-2")
	if o, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("backup: %v\n%s", err, o)
	}
	zips, _ := filepath.Glob(filepath.Join(out, "*.zip"))
	if len(zips) != 1 {
		t.Fatalf("want one zip, got %v", zips)
	}
	unzipped := t.TempDir()
	unzipTo(t, zips[0], unzipped)
	saved := mustRead(t, filepath.Join(unzipped, "session-sync.json"))
	if strings.Contains(saved, "installId") || !strings.Contains(saved, `"destination"`) {
		t.Fatalf("the backup's session-sync.json:\n%s", saved)
	}
	if !strings.Contains(mustRead(t, cfg), `"installId"`) {
		t.Fatal("backup changed this computer's session-sync.json")
	}

	mustWrite(t, cfg, saved) // restored on the same computer
	if o := m.run(t, "push", "--no-html"); !strings.Contains(o, "WARN: "+clashMessage) {
		t.Errorf("the first push after the restore did not warn:\n%s", o)
	}
	if o := m.run(t, "push", "--no-html"); strings.Contains(o, "Another computer") {
		t.Errorf("the warning did not stop:\n%s", o)
	}
}
