package main_test

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/work"
)

// configExpectErr runs config and returns its output and error instead of failing.
func configExpectErr(t *testing.T, claudeDir, name string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(binary, append([]string{"config", "--claude-dir", claudeDir}, args...)...)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=", "CLAUDE_SESSIONS_MACHINE="+name)
	o, err := cmd.CombinedOutput()
	return string(o), err
}

func TestConfigWorkCommands(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	target := t.TempDir()

	configAs(t, m.claudeDir, "MAC", "work", "off")
	if out := configAs(t, m.claudeDir, "MAC", "show"); !strings.Contains(out, "work backup  off") {
		t.Fatalf("show: %s", out)
	}
	configAs(t, m.claudeDir, "MAC", "work", "on")
	if out := configAs(t, m.claudeDir, "MAC", "show"); !strings.Contains(out, "work backup  on") {
		t.Fatalf("show: %s", out)
	}

	configAs(t, m.claudeDir, "MAC", "work-placement", "machine")
	if out := configAs(t, m.claudeDir, "MAC", "show"); !strings.Contains(out, "placement    machine") {
		t.Fatalf("show: %s", out)
	}
	if _, err := configExpectErr(t, m.claudeDir, "MAC", "work-placement", "sideways"); err == nil {
		t.Fatal("sideways accepted")
	}

	configAs(t, m.claudeDir, "MAC", "map-folder", `E:\SQL`, target)
	if out := configAs(t, m.claudeDir, "MAC", "show"); !strings.Contains(out, `E:\SQL`) || !strings.Contains(out, target) {
		t.Fatalf("map not listed: %s", out)
	}
	if _, err := configExpectErr(t, m.claudeDir, "MAC", "map-folder", "relative/path", target); err == nil {
		t.Fatal("relative path accepted")
	}
	if _, err := configExpectErr(t, m.claudeDir, "MAC", "map-folder", "/x", filepath.Join(target, "missing")); err == nil {
		t.Fatal("missing folder accepted")
	}
	configAs(t, m.claudeDir, "MAC", "unmap-folder", `E:\SQL`)
	if out := configAs(t, m.claudeDir, "MAC", "show"); strings.Contains(out, `E:\SQL`) {
		t.Fatalf("still mapped: %s", out)
	}
}

func TestExcludeFolderPurges(t *testing.T) {
	arch := t.TempDir()
	m := newMachine(t, "MAC", arch)
	dir := t.TempDir()
	configAs(t, m.claudeDir, "MAC", "set-destination", arch)

	enc := work.Encode(dir)
	mine := filepath.Join(arch, "work", "MAC", filepath.FromSlash(enc))
	other := filepath.Join(arch, "work", "OTHER", filepath.FromSlash(enc))
	mustWrite(t, filepath.Join(mine, "r.md"), "x")
	mustWrite(t, filepath.Join(other, "r.md"), "x")
	shard, _ := json.Marshal(map[string]any{
		"schemaVersion": 1, "machine": "MAC", "projects": map[string]any{},
		"workFolders": []map[string]any{{"path": dir}},
	})
	mustWrite(t, filepath.Join(arch, "manifest", "MAC.json"), string(shard))

	out := configAs(t, m.claudeDir, "MAC", "exclude-folder", dir)
	if !strings.Contains(out, "removed "+dir) || !strings.Contains(out, "trash or version history") {
		t.Fatalf("output: %s", out)
	}
	if _, err := os.Stat(mine); !os.IsNotExist(err) {
		t.Fatalf("own copy remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "r.md")); err != nil {
		t.Fatalf("other machine touched: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(arch, "manifest", "MAC.json"))
	if strings.Contains(string(raw), dir) {
		t.Fatalf("shard entry remains: %s", raw)
	}
}

func seedShard(t *testing.T, arch string, paths ...string) {
	t.Helper()
	var wf []map[string]any
	for _, p := range paths {
		wf = append(wf, map[string]any{"path": p})
	}
	b, _ := json.Marshal(map[string]any{"schemaVersion": 1, "machine": "MAC", "projects": map[string]any{}, "workFolders": wf})
	mustWrite(t, filepath.Join(arch, "manifest", "MAC.json"), string(b))
}

func TestExcludeFolderIgnoresTraversal(t *testing.T) {
	arch := t.TempDir()
	m := newMachine(t, "MAC", arch)
	configAs(t, m.claudeDir, "MAC", "set-destination", arch)
	sentinel := filepath.Join(arch, "work", "OTHER", "keep.md")
	mustWrite(t, sentinel, "x")
	mine := filepath.Join(arch, "work", "MAC", "keep.md")
	mustWrite(t, mine, "x")
	// Windows-form, so it behaves the same on every OS. Unguarded, the first entry
	// resolves to <arch>/work/OTHER and the second to <arch>/work: both inside the temp tree.
	seedShard(t, arch, `E:\zz\a\..\..\..\..\OTHER`, `E:\zz\a\..\..\..`)

	configAs(t, m.claudeDir, "MAC", "exclude-folder", `E:\zz\a`)
	for _, f := range []string{sentinel, mine} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("deleted outside its folder: %v", err)
		}
	}
}

func TestExcludeSubfolderOfArchivedFolder(t *testing.T) {
	arch := t.TempDir()
	m := newMachine(t, "MAC", arch)
	dir := t.TempDir()
	sub := filepath.Join(dir, "private")
	configAs(t, m.claudeDir, "MAC", "set-destination", arch)
	root := filepath.Join(arch, "work", "MAC", filepath.FromSlash(work.Encode(dir)))
	mustWrite(t, filepath.Join(root, "private", "s.md"), "x")
	mustWrite(t, filepath.Join(root, "a.md"), "x")
	seedShard(t, arch, dir)

	out := configAs(t, m.claudeDir, "MAC", "exclude-folder", sub)
	if !strings.Contains(out, "removed "+sub) {
		t.Fatalf("output: %s", out)
	}
	if _, err := os.Stat(filepath.Join(root, "private")); !os.IsNotExist(err) {
		t.Fatal("subfolder remains")
	}
	if _, err := os.Stat(filepath.Join(root, "a.md")); err != nil {
		t.Fatal("parent content lost")
	}
	raw, _ := os.ReadFile(filepath.Join(arch, "manifest", "MAC.json"))
	var got struct{ WorkFolders []struct{ Path string } }
	if json.Unmarshal(raw, &got) != nil || len(got.WorkFolders) != 1 || got.WorkFolders[0].Path != dir {
		t.Fatalf("parent entry dropped: %s", raw)
	}
}

// workCmd runs as m with home as the home folder. Every test folder sits under the
// system temp folder, which push never backs up, so the temp list is replaced by a
// missing folder - plus a trailing empty entry, which must not match every path.
func (m machine) workCmd(home string, args ...string) *exec.Cmd {
	cmd := exec.Command(binary, append([]string{args[0], "--claude-dir", m.claudeDir, "--archive", m.archive}, args[1:]...)...)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=", "CLAUDE_SESSIONS_MACHINE="+m.name, "HOME="+home, "USERPROFILE="+home,
		"CLAUDE_SESSIONS_TEMP_DIRS="+filepath.Join(home, "no-temp")+string(os.PathListSeparator))
	return cmd
}

func (m machine) workRun(t *testing.T, home string, args ...string) string {
	t.Helper()
	out, err := m.workCmd(home, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return string(out)
}

// workSession writes a session started in cwd that wrote each file in writes and ran
// each command, and returns its path.
func workSession(t *testing.T, m machine, cwd string, writes []string, commands ...string) string {
	t.Helper()
	var b strings.Builder
	add := func(v any) {
		l, _ := json.Marshal(v)
		b.Write(append(l, '\n'))
	}
	msg := func(role string, content any) map[string]any {
		return map[string]any{"type": role, "cwd": cwd, "sessionId": forkSession, "timestamp": "2026-11-21T05:00:00.000Z",
			"message": map[string]any{"role": role, "content": content}}
	}
	use := func(name string, input map[string]string) {
		add(msg("assistant", []any{map[string]any{"type": "tool_use", "name": name, "input": input}}))
	}
	add(msg("user", "do the work"))
	for _, w := range writes {
		use("Write", map[string]string{"file_path": w, "content": "x"})
	}
	for _, c := range commands {
		use("Bash", map[string]string{"command": c})
	}
	p := filepath.Join(m.claudeDir, "projects", claude.Slug(cwd), forkSession+".jsonl")
	mustWrite(t, p, b.String())
	return p
}

// poSession makes <home>/SQL/po holding r.md, out.csv and shot.png, and a session
// started in home that writes r.md and runs a script there.
func poSession(t *testing.T, m machine) (home, po, transcript string) {
	t.Helper()
	home = t.TempDir()
	po = filepath.Join(home, "SQL", "po")
	mustWrite(t, filepath.Join(po, "r.md"), "report, first and longer version\n")
	mustWrite(t, filepath.Join(po, "out.csv"), "a,b\n1,2\n")
	mustWrite(t, filepath.Join(po, "shot.png"), "png")
	transcript = workSession(t, m, home, []string{filepath.Join(po, "r.md")}, `cd "`+po+`" && python q.py`)
	return home, po, transcript
}

func workCopy(m machine, folder string) string {
	return filepath.Join(m.archive, "work", m.name, filepath.FromSlash(work.Encode(folder)))
}

func workShard(t *testing.T, m machine) []archive.WorkFolder {
	t.Helper()
	var s archive.Shard
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(m.archive, "manifest", m.name+".json"))), &s); err != nil {
		t.Fatal(err)
	}
	return s.WorkFolders
}

// workFiles lists every file under the archive's work/ folder, "/"-separated.
func workFiles(arch string) []string {
	var out []string
	root := filepath.Join(arch, "work")
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}

func TestPushWorkFolder(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, po, _ := poSession(t, m)

	out := m.workRun(t, home, "push", "--no-html")
	for _, f := range []string{"r.md", "out.csv"} {
		if _, err := os.Stat(filepath.Join(workCopy(m, po), f)); err != nil {
			t.Errorf("%s not archived: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(workCopy(m, po), "shot.png")); !os.IsNotExist(err) {
		t.Errorf("image archived: %v", err)
	}
	if wf := workShard(t, m); len(wf) != 1 || wf[0].Path != po || wf[0].Files != 2 {
		t.Fatalf("shard: %+v", wf)
	}
	if log := mustRead(t, filepath.Join(m.claudeDir, "session-sync.log")); !strings.Contains(log, "WORK: now backing up "+po+" (2 files") {
		t.Fatalf("log: %s", log)
	}
	if !strings.Contains(out, "now backing up "+po) {
		t.Fatalf("output: %s", out)
	}
}

func TestPushWorkIgnoresRepoAndTemp(t *testing.T) {
	home := t.TempDir()
	repo := gitRepo(t, filepath.Join(home, "dev", "api"))
	keep := filepath.Join(home, "notes")
	writes := []string{filepath.Join(repo, "n.md"), filepath.Join(keep, "k.md")}
	for _, w := range writes {
		mustWrite(t, w, "x")
	}
	for _, c := range []struct {
		defaultTemp bool // the real list: os.TempDir() holds every test folder
		want        []string
	}{
		{true, nil},
		{false, []string{"MAC/" + work.Encode(keep) + "/k.md"}},
	} {
		m := newMachine(t, "MAC", t.TempDir())
		workSession(t, m, home, writes)
		cmd := m.workCmd(home, "push", "--no-html")
		if c.defaultTemp {
			cmd.Env = append(cmd.Env, "CLAUDE_SESSIONS_TEMP_DIRS=")
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("push: %v\n%s", err, out)
		}
		if got := workFiles(m.archive); !reflect.DeepEqual(got, c.want) {
			t.Errorf("default temp %v: archived %v, want %v", c.defaultTemp, got, c.want)
		}
	}
}

func TestPushWorkOverCap(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home := t.TempDir()
	big := filepath.Join(home, "data")
	for i := 0; i < 26; i++ {
		p := filepath.Join(big, fmt.Sprintf("part%02d.csv", i))
		mustWrite(t, p, "")
		// Sparse: it counts toward the cap without using the disk.
		if err := os.Truncate(p, (99<<20)/10); err != nil {
			t.Fatal(err)
		}
	}
	workSession(t, m, home, []string{filepath.Join(big, "part00.csv")})

	m.workRun(t, home, "push", "--no-html")
	if got := workFiles(m.archive); len(got) != 0 {
		t.Fatalf("copied %d file(s)", len(got))
	}
	if wf := workShard(t, m); len(wf) != 1 || wf[0].Path != big || wf[0].Skipped != "over 250 MB" {
		t.Fatalf("shard: %+v", wf)
	}
	if log := mustRead(t, filepath.Join(m.claudeDir, "session-sync.log")); !strings.Contains(log, "WARN: work folder "+big+" is over 250 MB") {
		t.Fatalf("log: %s", log)
	}
}

func TestPushWorkOff(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, po, _ := poSession(t, m)
	m.workRun(t, home, "push", "--no-html")
	before, files := workShard(t, m), workFiles(m.archive)

	configAs(t, m.claudeDir, "MAC", "work", "off")
	mustWrite(t, filepath.Join(po, "new.md"), "x")
	other := filepath.Join(home, "other")
	mustWrite(t, filepath.Join(other, "o.md"), "x")
	workSession(t, m, other, []string{filepath.Join(other, "o.md")})
	m.workRun(t, home, "push", "--no-html")

	if after := workShard(t, m); len(before) != 1 || !reflect.DeepEqual(before, after) {
		t.Fatalf("shard changed: %+v -> %+v", before, after)
	}
	if got := workFiles(m.archive); !reflect.DeepEqual(got, files) {
		t.Fatalf("copied while off: %v -> %v", files, got)
	}
}

func TestPushWorkDeletedFolderKept(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, po, _ := poSession(t, m)
	m.workRun(t, home, "push", "--no-html")
	before := workShard(t, m)

	if err := os.RemoveAll(po); err != nil {
		t.Fatal(err)
	}
	m.workRun(t, home, "push", "--no-html")
	if after := workShard(t, m); len(before) != 1 || !reflect.DeepEqual(before, after) {
		t.Fatalf("shard changed: %+v -> %+v", before, after)
	}
	if _, err := os.Stat(filepath.Join(workCopy(m, po), "r.md")); err != nil {
		t.Fatalf("archive copy gone: %v", err)
	}
}

func TestPushWorkDryRun(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, po, _ := poSession(t, m)

	out := m.workRun(t, home, "push", "--no-html", "--dry-run")
	if _, err := os.Stat(filepath.Join(m.archive, "work")); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote work/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.claudeDir, "session-sync-base", "work-folders.json")); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote the local record: %v", err)
	}
	for _, f := range []string{"r.md", "out.csv"} {
		if !strings.Contains(out, "would copy "+filepath.Join(po, f)) {
			t.Errorf("%s not listed: %s", f, out)
		}
	}
	if strings.Contains(out, "shot.png") {
		t.Errorf("image listed: %s", out)
	}
}

func TestPushWorkSmallerFileReplaces(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, po, _ := poSession(t, m)
	m.workRun(t, home, "push", "--no-html")

	mustWrite(t, filepath.Join(po, "r.md"), "v2\n")
	m.workRun(t, home, "push", "--no-html")
	if got := mustRead(t, filepath.Join(workCopy(m, po), "r.md")); got != "v2\n" {
		t.Fatalf("archived r.md = %q", got)
	}
}

func TestStopHookCopiesNoWork(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, _, transcript := poSession(t, m)

	cmd := m.workCmd(home, "push", "--quiet", "--from-hook")
	cmd.Stdin = strings.NewReader(`{"transcript_path":` + quoted(transcript) + `}`)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hook: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(m.archive, "projects", claude.Slug(home), forkSession+".jsonl")); err != nil {
		t.Fatalf("hook archived nothing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.archive, "work")); !os.IsNotExist(err) {
		t.Fatalf("hook wrote work/: %v", err)
	}
}

// The per-reply hook copies a transcript before the full push sees it. The folders it
// names must still be found: the push scans what changed since it last ran, not only
// what it copies itself.
func TestPushWorkAfterHook(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, _, transcript := poSession(t, m)
	m.workRun(t, home, "push", "--no-html")

	notes := filepath.Join(home, "notes")
	mustWrite(t, filepath.Join(notes, "n.md"), "x")
	l, _ := json.Marshal(map[string]any{"type": "assistant", "cwd": home, "sessionId": forkSession, "message": map[string]any{
		"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "name": "Write", "input": map[string]string{"file_path": filepath.Join(notes, "n.md")}}}}})
	f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(append(l, '\n'))
	f.Close()
	cmd := m.workCmd(home, "push", "--quiet", "--from-hook")
	cmd.Stdin = strings.NewReader(`{"transcript_path":` + quoted(transcript) + `}`)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hook: %v\n%s", err, out)
	}

	m.workRun(t, home, "push", "--no-html")
	if _, err := os.Stat(filepath.Join(workCopy(m, notes), "n.md")); err != nil {
		t.Fatalf("folder named after the hook ran not archived: %v", err)
	}
}

// A folder excluded while the archive was away, or a part of one: the next push
// removes its copy.
func TestPushWorkPurgesExcluded(t *testing.T) {
	for _, sub := range []string{"", "private"} {
		m := newMachine(t, "MAC", t.TempDir())
		home, po, _ := poSession(t, m)
		mustWrite(t, filepath.Join(po, "private", "s.md"), "x")
		m.workRun(t, home, "push", "--no-html")
		x := filepath.Join(po, sub)
		if _, err := os.Stat(workCopy(m, x)); err != nil {
			t.Fatal(err)
		}

		cfgPath := filepath.Join(m.claudeDir, "session-sync.json")
		cfg := map[string]any{}
		if raw, err := os.ReadFile(cfgPath); err == nil {
			json.Unmarshal(raw, &cfg)
		}
		cfg["workExclude"] = []string{x}
		b, _ := json.Marshal(cfg)
		mustWrite(t, cfgPath, string(b))

		m.workRun(t, home, "push", "--no-html")
		if _, err := os.Stat(workCopy(m, x)); !os.IsNotExist(err) {
			t.Fatalf("%s: excluded copy remains: %v", x, err)
		}
		wf := workShard(t, m)
		if sub == "" && len(wf) != 0 {
			t.Fatalf("shard: %+v", wf)
		}
		if sub != "" {
			if len(wf) != 1 || wf[0].Path != po {
				t.Fatalf("parent entry dropped: %+v", wf)
			}
			if _, err := os.Stat(filepath.Join(workCopy(m, po), "r.md")); err != nil {
				t.Fatalf("parent content lost: %v", err)
			}
		}
	}
}

// A dot folder is never a backup root: an edit of ~/.ssh/config must not carry the
// keys beside it to the archive.
func TestPushWorkSkipsDotFolder(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home := t.TempDir()
	mustWrite(t, filepath.Join(home, ".ssh", "config"), "Host x\n")
	mustWrite(t, filepath.Join(home, ".ssh", "deploy"), "PRIVATE KEY\n")
	workSession(t, m, home, []string{filepath.Join(home, ".ssh", "config")})

	m.workRun(t, home, "push", "--no-html")
	if got := workFiles(m.archive); len(got) != 0 {
		t.Fatalf("archived %v", got)
	}
	if wf := workShard(t, m); len(wf) != 0 {
		t.Fatalf("shard: %+v", wf)
	}
}

// The shard is shared archive data. A folder added to it by hand is not walked: only
// folders this machine's sessions named are.
func TestPushWorkIgnoresShardEntries(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, _, _ := poSession(t, m)
	docs := filepath.Join(home, "private-docs")
	mustWrite(t, filepath.Join(docs, "tax-2025.txt"), "x")
	m.workRun(t, home, "push", "--no-html")

	shardPath := filepath.Join(m.archive, "manifest", "MAC.json")
	var shard map[string]any
	if err := json.Unmarshal([]byte(mustRead(t, shardPath)), &shard); err != nil {
		t.Fatal(err)
	}
	shard["workFolders"] = append(shard["workFolders"].([]any), map[string]any{"path": docs})
	b, _ := json.Marshal(shard)
	mustWrite(t, shardPath, string(b))

	m.workRun(t, home, "push", "--no-html")
	if _, err := os.Stat(workCopy(m, docs)); !os.IsNotExist(err) {
		t.Fatalf("hand-added folder copied: %v", err)
	}
}

// workPair is two machines with different homes sharing one archive. WIN pushed a
// session, started in a repository outside both homes, that wrote a file in
// "<homeA>/My Work/run 1" and ran a cd into it. The repository is not a home folder,
// so no project move can repoint those paths: only a work-folder move can.
type workPair struct {
	a, b         machine
	homeA, homeB string
	repo, folder string // folder is A's work folder
}

func newWorkPair(t *testing.T) workPair {
	t.Helper()
	arch := t.TempDir()
	p := workPair{a: newMachine(t, "WIN", arch), b: newMachine(t, "MAC", arch), homeA: t.TempDir(), homeB: t.TempDir()}
	p.repo = gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	p.folder = filepath.Join(p.homeA, "My Work", "run 1")
	mustWrite(t, filepath.Join(p.folder, "রিপোর্ট.md"), "report v1\n")
	mustWrite(t, filepath.Join(p.homeA, "Desktop", "todo.md"), "todo\n") // a broad folder: this one file only
	workSession(t, p.a, p.repo, []string{filepath.Join(p.folder, "রিপোর্ট.md"), filepath.Join(p.homeA, "Desktop", "todo.md")},
		`cd "`+p.folder+`"`)
	p.a.workRun(t, p.homeA, "push", "--no-html")
	return p
}

// placed is where A's work folder lands on B by default.
func (p workPair) placed() string { return filepath.Join(p.homeB, "My Work", "run 1") }

// inJSON is s as it appears inside a JSON string.
func inJSON(s string) string { return strings.Trim(quoted(s), `"`) }

func TestImportWorkAcrossHomes(t *testing.T) {
	p := newWorkPair(t)
	p.b.workRun(t, p.homeB, "import", "--no-plugins", "--dry-run")
	if _, err := os.Stat(filepath.Join(p.b.claudeDir, "session-sync-base", "work-held.json")); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote the record: %v", err)
	}
	if _, err := os.Stat(p.placed()); !os.IsNotExist(err) {
		t.Fatalf("dry run placed files: %v", err)
	}
	out := p.b.workRun(t, p.homeB, "import", "--no-plugins")

	if got := mustRead(t, filepath.Join(p.placed(), "রিপোর্ট.md")); got != "report v1\n" {
		t.Fatalf("restored %q", got)
	}
	todo := filepath.Join(p.homeB, "Desktop", "todo.md")
	if got := mustRead(t, todo); got != "todo\n" {
		t.Fatalf("exact file restored %q", got)
	}
	s := mustRead(t, p.b.session(p.repo))
	if !strings.Contains(s, inJSON(filepath.Join(p.placed(), "রিপোর্ট.md"))) || !strings.Contains(s, inJSON(`cd "`+p.placed()+`"`)) ||
		!strings.Contains(s, inJSON(todo)) || strings.Contains(s, inJSON(p.homeA)) {
		t.Fatalf("transcript not repointed:\n%s", s)
	}
	if !strings.Contains(out, "work folders from WIN: ") || !strings.Contains(out, p.folder+" -> "+p.placed()) ||
		!strings.Contains(out, "config map-folder") || !strings.Contains(out, "config work-placement") {
		t.Fatalf("output: %s", out)
	}
}

func TestImportWorkMapFolder(t *testing.T) {
	p := newWorkPair(t)
	dir := filepath.Join(p.homeB, "mapped")
	mustMkdir(t, dir)
	configAs(t, p.b.claudeDir, "MAC", "map-folder", p.folder, dir)
	p.b.workRun(t, p.homeB, "import", "--no-plugins")

	if got := mustRead(t, filepath.Join(dir, "রিপোর্ট.md")); got != "report v1\n" {
		t.Fatalf("restored %q", got)
	}
	if _, err := os.Stat(filepath.Join(p.homeB, "My Work")); !os.IsNotExist(err) {
		t.Fatalf("also placed under the home: %v", err)
	}
	if s := mustRead(t, p.b.session(p.repo)); !strings.Contains(s, inJSON(filepath.Join(dir, "রিপোর্ট.md"))) {
		t.Fatalf("transcript not repointed:\n%s", s)
	}
}

func TestImportWorkAddOnly(t *testing.T) {
	p := newWorkPair(t)
	mine := filepath.Join(p.placed(), "রিপোর্ট.md")
	mustWrite(t, mine, "B's own\n")
	p.b.workRun(t, p.homeB, "import", "--no-plugins")
	if got := mustRead(t, mine); got != "B's own\n" {
		t.Fatalf("local file replaced: %q", got)
	}
	if _, err := os.Stat(filepath.Join(p.placed(), "রিপোর্ট.WIN.md")); !os.IsNotExist(err) {
		t.Fatalf("copy beside written without --update: %v", err)
	}
}

func TestImportWorkUpdate(t *testing.T) {
	p := newWorkPair(t)
	mustWrite(t, filepath.Join(p.folder, "notes.md"), "A's notes\n")
	mustWrite(t, filepath.Join(p.placed(), "notes.md"), "B's notes\n")
	p.a.workRun(t, p.homeA, "push", "--no-html")
	future := filepath.Join(workCopy(p.a, p.folder), "future.md")
	mustWrite(t, future, "from the future\n")
	later := time.Now().Add(3 * time.Hour)
	if err := os.Chtimes(future, later, later); err != nil {
		t.Fatal(err)
	}
	p.b.workRun(t, p.homeB, "import", "--no-plugins")

	report := filepath.Join(p.folder, "রিপোর্ট.md")
	mustWrite(t, report, "report v2, longer\n")
	soon := time.Now().Add(5 * time.Minute)
	if err := os.Chtimes(report, soon, soon); err != nil {
		t.Fatal(err)
	}
	p.a.workRun(t, p.homeA, "push", "--no-html")
	dry := p.b.workRun(t, p.homeB, "import", "--no-plugins", "--update", "--dry-run")
	if !strings.Contains(dry, "(1 file(s) would be filed") || !strings.Contains(dry, "copy would be written beside it as notes.WIN.md") ||
		strings.Contains(dry, "copy written") {
		t.Errorf("dry run output: %s", dry)
	}
	if _, err := os.Stat(filepath.Join(p.placed(), "notes.WIN.md")); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote the copy beside: %v", err)
	}
	if got := mustRead(t, filepath.Join(p.placed(), "রিপোর্ট.md")); got != "report v1\n" {
		t.Fatalf("dry run updated: %q", got)
	}
	out := p.b.workRun(t, p.homeB, "import", "--no-plugins", "--update")

	if got := mustRead(t, filepath.Join(p.placed(), "রিপোর্ট.md")); got != "report v2, longer\n" {
		t.Errorf("held copy not updated: %q", got)
	}
	if got := mustRead(t, filepath.Join(p.placed(), "notes.md")); got != "B's notes\n" {
		t.Errorf("B's own file replaced: %q", got)
	}
	if got := mustRead(t, filepath.Join(p.placed(), "notes.WIN.md")); got != "A's notes\n" {
		t.Errorf("copy beside: %q", got)
	}
	if !strings.Contains(out, "notes.WIN.md") {
		t.Errorf("output does not name the copy beside: %s", out)
	}
	if _, err := os.Stat(filepath.Join(p.placed(), "future.md")); !os.IsNotExist(err) {
		t.Errorf("future-dated file restored: %v", err)
	}
}

func TestRestoredNotPushedBack(t *testing.T) {
	p := newWorkPair(t)
	p.b.workRun(t, p.homeB, "import", "--no-plugins")
	mustWrite(t, filepath.Join(p.placed(), "b.md"), "B's own\n")
	p.b.workRun(t, p.homeB, "push", "--no-html")

	mine := func() (out []string) {
		for _, f := range workFiles(p.b.archive) {
			if strings.HasPrefix(f, "MAC/") {
				out = append(out, f)
			}
		}
		return out
	}
	enc := "MAC/" + work.Encode(p.placed())
	if got, want := mine(), []string{enc + "/b.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("B archived %v, want %v", got, want)
	}
	for _, f := range workShard(t, p.b) {
		if f.Path == p.placed() && f.Files != 1 {
			t.Fatalf("restored files counted as B's: %+v", f)
		}
	}

	// Edited here, it is B's now.
	mustWrite(t, filepath.Join(p.placed(), "রিপোর্ট.md"), "report, edited on B\n")
	p.b.workRun(t, p.homeB, "push", "--no-html")
	if got, want := mine(), []string{enc + "/b.md", enc + "/রিপোর্ট.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after the edit B archived %v, want %v", got, want)
	}
}

// Hostile shards: a root home, a folder in ~/.ssh, a ".." path, archive files a push
// never sends, an origin aimed into node_modules in one of B's own folders, folders
// that Python and Node load code from (R21, R22), an ancestor of the home, and a machine
// name that makes a copy beside an .exe. Only the plain files of the one folder
// B mapped land, and not executable.
func TestImportWorkHostileShard(t *testing.T) {
	arch := t.TempDir()
	a, b := newMachine(t, "WIN", arch), newMachine(t, "MAC", arch)
	homeA, homeB := t.TempDir(), t.TempDir()
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	mustWrite(t, a.session(repo), sessionLines(repo, "u1"))
	a.workRun(t, homeA, "push", "--no-html")

	mapped := filepath.Join(homeB, "evil")
	mustMkdir(t, mapped)
	configAs(t, b.claudeDir, "MAC", "map-folder", "/evil", mapped)
	mustWrite(t, filepath.Join(mapped, "proj", "tool"), "B's own tool\n")
	own := filepath.Join(homeB, "mine")
	mustMkdir(t, own)
	ownRecord, _ := json.Marshal([]map[string]any{{"path": own}})
	mustWrite(t, filepath.Join(b.claudeDir, "session-sync-base", "work-folders.json"), string(ownRecord))

	evil, evil2, evil3, evil4, exe := machine{name: "EVIL", archive: arch}, machine{name: "EVIL2", archive: arch},
		machine{name: "EVIL3", archive: arch}, machine{name: "EVIL4", archive: arch}, machine{name: "exe", archive: arch}
	code := []string{"/zz/proj2/venv/lib/python3.12/site-packages", "/zz/proj/node_modules/lodash",
		"/zz/miniconda3/lib/python3.12/site-packages", "/zz/env/etc/conda/activate.d"}
	ssh := filepath.Join(homeB, ".ssh")
	put := func(m machine, folder, rel, body string) string {
		f := filepath.Join(workCopy(m, folder), filepath.FromSlash(rel))
		mustWrite(t, f, body)
		return f
	}
	put(evil, ssh, "authorized_keys", "ssh-ed25519 AAAA attacker\n")
	put(evil, "/evil/proj", "ok.md", "fine\n")
	if err := os.Chmod(put(evil, "/evil/proj", "run.sh", "#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	put(evil, "/evil/proj", ".git/config", "[core]\n\tfsmonitor = evil\n")
	put(evil, "/evil/proj", "node_modules/x/index.js", "evil()\n")
	put(evil, "/evil/proj", "tool.exe", "MZ")
	put(evil, "/evil/proj", "deploy.key", "k")
	put(evil2, "/evil/proj", "evil2.md", "x")
	put(evil3, filepath.Dir(homeB), "x.md", "x")
	put(evil, "/x/y", "index.js", "evil()\n")
	put(exe, "/evil/proj", "tool", "MZ")
	evil4Folders := []any{"/zz/ok"}
	put(evil4, "/zz/ok", "zz.pth", "import os\n")
	for _, f := range code {
		put(evil4, f, "sitecustomize.py", "import os\n")
		evil4Folders = append(evil4Folders, f)
	}
	// Links in the archive are never read through, as a file or on the way to a folder
	// (no symlinks on some Windows runners).
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "sub", "private.md"), "private\n")
	_ = os.Symlink(filepath.Join(outside, "sub", "private.md"), filepath.Join(workCopy(evil, "/evil/proj"), "link.md"))
	_ = os.Symlink(outside, workCopy(evil, "/evil/lnk"))

	shard := func(name, home string, folders ...any) {
		var wf []any
		for _, f := range folders {
			if p, ok := f.(string); ok {
				f = map[string]any{"path": p}
			}
			wf = append(wf, f)
		}
		raw, _ := json.Marshal(map[string]any{"schemaVersion": 1, "machine": name, "home": home, "projects": map[string]any{}, "workFolders": wf})
		mustWrite(t, filepath.Join(arch, "manifest", name+".json"), string(raw))
	}
	sep := string(filepath.Separator) // not filepath.Join, which would clean the ".." away
	shard("EVIL", "/", ssh, homeB+sep+"notes"+sep+".."+sep+".ssh", "/evil/proj", "/evil/lnk/sub",
		map[string]any{"path": "/x/y", "origin": map[string]any{"machine": "MAC", "path": filepath.Join(own, "node_modules", "lodash")}})
	shard("EVIL2", "x", "/evil/proj") // a relative home: the whole shard is ignored
	shard("EVIL3", homeB, filepath.Dir(homeB))
	shard("exe", "/", "/evil/proj")
	shard("EVIL4", "/zz", evil4Folders...)

	out := b.workRun(t, homeB, "import", "--no-plugins", "--update")

	var got []string
	_ = filepath.WalkDir(homeB, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(homeB, p)
			got = append(got, filepath.ToSlash(rel))
		}
		return nil
	})
	if want := []string{"evil/proj/ok.md", "evil/proj/run.sh", "evil/proj/tool"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("wrote %v, want %v\n%s", got, want, out)
	}
	for _, f := range append(code, "/x/y") {
		if !strings.Contains(out, f+": refused: ") {
			t.Errorf("%s not refused: %s", f, out)
		}
	}
	if fi, err := os.Stat(filepath.Join(mapped, "proj", "run.sh")); runtime.GOOS != "windows" && (err != nil || fi.Mode().Perm() != 0o644) {
		t.Fatalf("run.sh mode: %v %v", fi, err)
	}
	if !strings.Contains(out, "work folders from EVIL: ") || !strings.Contains(out, "7 refused") || !strings.Contains(out, "contains ..") ||
		!strings.Contains(out, "in a folder code is loaded from") || !strings.Contains(out, "/zz/ok/zz.pth: refused: type") ||
		!strings.Contains(out, "a broad folder") ||
		!strings.Contains(out, "tool: refused: type") || strings.Contains(out, "EVIL2") {
		t.Fatalf("output: %s", out)
	}
}

// pull keeps its same-layout contract: files at their recorded paths, no rewriting.
func TestPullWorkSameLayout(t *testing.T) {
	arch := t.TempDir()
	a, b := newMachine(t, "WIN", arch), newMachine(t, "MAC", arch)
	home, po, transcript := poSession(t, a)
	a.workRun(t, home, "push", "--no-html")
	// One file system stands in for two: A's folder would be there already.
	if err := os.RemoveAll(po); err != nil {
		t.Fatal(err)
	}
	a.workRun(t, home, "pull", "--no-plugins") // its own shard: what A deleted stays deleted
	if _, err := os.Stat(po); !os.IsNotExist(err) {
		t.Fatalf("own folder restored: %v", err)
	}
	b.workRun(t, home, "pull", "--no-plugins")

	if got := mustRead(t, filepath.Join(po, "r.md")); got != "report, first and longer version\n" {
		t.Fatalf("r.md = %q", got)
	}
	if _, err := os.Stat(filepath.Join(po, "shot.png")); !os.IsNotExist(err) {
		t.Fatalf("image restored: %v", err)
	}
	if got := mustRead(t, b.session(home)); got != mustRead(t, transcript) {
		t.Fatalf("transcript rewritten:\n%s", got)
	}
}

func TestImportProjectInsideWorkFolder(t *testing.T) {
	arch := t.TempDir()
	a, b := newMachine(t, "WIN", arch), newMachine(t, "MAC", arch)
	homeA, homeB := t.TempDir(), t.TempDir()
	po := filepath.Join(homeA, "SQL", "po")
	mustWrite(t, filepath.Join(po, "r.md"), "x\n")
	workSession(t, a, po, []string{filepath.Join(po, "r.md")})
	a.workRun(t, homeA, "push", "--no-html")
	b.workRun(t, homeB, "import", "--no-plugins")

	if _, err := os.Stat(b.session(filepath.Join(homeB, "SQL", "po"))); err != nil {
		t.Fatalf("not filed for the placed folder: %v", err)
	}
	if _, err := os.Stat(b.session(po)); !os.IsNotExist(err) {
		t.Fatalf("filed for A's path: %v", err)
	}
}

func TestImportWorkOff(t *testing.T) {
	p := newWorkPair(t)
	configAs(t, p.b.claudeDir, "MAC", "work", "off")
	p.b.workRun(t, p.homeB, "import", "--no-plugins")
	if _, err := os.Stat(filepath.Join(p.homeB, "My Work")); !os.IsNotExist(err) {
		t.Fatalf("restored while off: %v", err)
	}
	if s := mustRead(t, p.b.session(p.repo)); !strings.Contains(s, inJSON(p.folder)) {
		t.Fatalf("work move applied while off:\n%s", s)
	}
}

// R17: a shard can name any folder. One of this machine's own - a project (from its
// shard or its .claude.json) or a folder above one, a work folder, the Claude folder's
// parent, the home as an "exact file" - still has its files placed, but never becomes
// a move: that would repoint this machine's sessions.
func TestImportWorkNeverMovesOwnPaths(t *testing.T) {
	const other = "88888888-8888-8888-8888-888888888888"
	for _, viaState := range []bool{false, true} {
		arch := t.TempDir()
		a, b := newMachine(t, "WIN", arch), newMachine(t, "MAC", arch)
		homeA, homeB := t.TempDir(), t.TempDir()
		repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
		deep := filepath.Join(t.TempDir(), "w", "x", "proj") // a project here; its parent is named below
		state := map[string]any{deep: map[string]any{}}
		if viaState {
			state[repo] = map[string]any{}
		}
		raw, _ := json.Marshal(map[string]any{"projects": state})
		mustWrite(t, filepath.Join(b.claudeDir, ".claude.json"), string(raw))
		if !viaState {
			mustWrite(t, b.session(repo), sessionLines(repo, "u1"))
			b.workRun(t, homeB, "push", "--no-html")
		}
		theirs := filepath.Join(a.claudeDir, "projects", claude.Slug(repo), other+".jsonl")
		mustWrite(t, theirs, strings.ReplaceAll(sessionLines(repo, "a1"), forkSession, other))
		a.workRun(t, homeA, "push", "--no-html")

		own := filepath.Join(t.TempDir(), "mine")
		rec, _ := json.Marshal([]map[string]any{{"path": own}})
		mustWrite(t, filepath.Join(b.claudeDir, "session-sync-base", "work-folders.json"), string(rec))
		upper := filepath.Dir(b.claudeDir)
		hostile := func(name, home string, folders ...string) {
			var wf []map[string]any
			for _, f := range folders {
				if f == homeB {
					mustWrite(t, workCopy(machine{name: name, archive: arch}, f), "x")
					wf = append(wf, map[string]any{"path": f, "exactFile": true})
					continue
				}
				mustWrite(t, filepath.Join(workCopy(machine{name: name, archive: arch}, f), "x.md"), "x")
				wf = append(wf, map[string]any{"path": f})
			}
			raw, _ := json.Marshal(map[string]any{"schemaVersion": 1, "machine": name, "home": home, "projects": map[string]any{}, "workFolders": wf})
			mustWrite(t, filepath.Join(arch, "manifest", name+".json"), string(raw))
		}
		// The repository's parent for a home: the repository lands at <homeB>/api.
		hostile("EVIL", filepath.Dir(repo), repo)
		hostile("EVIL2", "/zz", own, upper, filepath.Dir(deep), homeB)

		out := b.workRun(t, homeB, "import", "--no-plugins")
		filed := mustRead(t, filepath.Join(b.claudeDir, "projects", claude.Slug(repo), other+".jsonl"))
		if !strings.Contains(filed, inJSON(repo)) || strings.Contains(filed, inJSON(filepath.Join(homeB, "api"))) {
			t.Fatalf("state %v: transcript repointed:\n%s", viaState, filed)
		}
		if _, err := os.Stat(b.session(filepath.Join(homeB, "api"))); !os.IsNotExist(err) {
			t.Fatalf("state %v: filed for the placed folder: %v", viaState, err)
		}
		if _, err := os.Stat(filepath.Join(homeB, "api", "x.md")); err != nil {
			t.Fatalf("state %v: files not placed: %v", viaState, err)
		}
		for _, f := range []string{repo, own, upper, filepath.Dir(deep), homeB} {
			if !strings.Contains(out, f+": refused: a path of this machine's own") {
				t.Fatalf("state %v: %s not reported: %s", viaState, f, out)
			}
		}
	}
}

// R18: the skipped names apply inside a work folder, as push applies them below a root.
// A folder that only lives under one restores normally.
func TestImportWorkUnderSkippedName(t *testing.T) {
	arch := t.TempDir()
	a, b := newMachine(t, "WIN", arch), newMachine(t, "MAC", arch)
	homeA, homeB := t.TempDir(), t.TempDir()
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	dir := filepath.Join(homeA, "build", "report")
	mustWrite(t, filepath.Join(dir, "r.md"), "r\n")
	workSession(t, a, repo, []string{filepath.Join(dir, "r.md")})
	a.workRun(t, homeA, "push", "--no-html")
	mustWrite(t, filepath.Join(workCopy(a, dir), "node_modules", "x.js"), "x") // one push never sends
	b.workRun(t, homeB, "import", "--no-plugins")

	here := filepath.Join(homeB, "build", "report")
	if got := mustRead(t, filepath.Join(here, "r.md")); got != "r\n" {
		t.Fatalf("restored %q", got)
	}
	if _, err := os.Stat(filepath.Join(here, "node_modules")); !os.IsNotExist(err) {
		t.Fatalf("skipped folder restored: %v", err)
	}
}

// R19: Claude lists the home itself as a project. That alone must not stop a folder in
// the home from moving: here another machine with the same home path maps one away.
func TestImportWorkHomeProjectKeepsMoves(t *testing.T) {
	arch := t.TempDir()
	a, b := newMachine(t, "WIN", arch), newMachine(t, "MAC", arch)
	home := t.TempDir()
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	po := filepath.Join(home, "SQL", "po")
	mustWrite(t, filepath.Join(po, "r.md"), "r\n")
	workSession(t, a, repo, []string{filepath.Join(po, "r.md")})
	a.workRun(t, home, "push", "--no-html")

	raw, _ := json.Marshal(map[string]any{"projects": map[string]any{home: map[string]any{}}})
	mustWrite(t, filepath.Join(b.claudeDir, ".claude.json"), string(raw))
	dir := filepath.Join(t.TempDir(), "mapped")
	mustMkdir(t, dir)
	configAs(t, b.claudeDir, "MAC", "map-folder", po, dir)
	out := b.workRun(t, home, "import", "--no-plugins")

	if got := mustRead(t, filepath.Join(dir, "r.md")); got != "r\n" {
		t.Fatalf("restored %q", got)
	}
	if s := mustRead(t, b.session(repo)); !strings.Contains(s, inJSON(filepath.Join(dir, "r.md"))) || strings.Contains(s, inJSON(po)) {
		t.Fatalf("move dropped:\n%s\n%s", s, out)
	}
}

// --update replaces only a file held from the same machine. One held from another
// machine is that machine's: the newcomer is written beside it.
func TestImportWorkUpdateOtherMachine(t *testing.T) {
	p := newWorkPair(t)
	c := newMachine(t, "LNX", p.a.archive)
	homeC := t.TempDir()
	repoC := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	folderC := filepath.Join(homeC, "w")
	mustWrite(t, filepath.Join(folderC, "রিপোর্ট.md"), "LNX's report\n")
	workSession(t, c, repoC, []string{filepath.Join(folderC, "রিপোর্ট.md")})
	c.workRun(t, homeC, "push", "--no-html")

	dir := filepath.Join(p.homeB, "shared")
	mustMkdir(t, dir)
	configAs(t, p.b.claudeDir, "MAC", "map-folder", p.folder, dir)
	configAs(t, p.b.claudeDir, "MAC", "map-folder", folderC, dir)
	p.b.workRun(t, p.homeB, "import", "--no-plugins") // LNX sorts first: its copy is filed
	out := p.b.workRun(t, p.homeB, "import", "--no-plugins", "--update")

	if got := mustRead(t, filepath.Join(dir, "রিপোর্ট.md")); got != "LNX's report\n" {
		t.Fatalf("replaced by another machine's copy: %q\n%s", got, out)
	}
	if got := mustRead(t, filepath.Join(dir, "রিপোর্ট.WIN.md")); got != "report v1\n" {
		t.Fatalf("copy beside: %q\n%s", got, out)
	}
}
