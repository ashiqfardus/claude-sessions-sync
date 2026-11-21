package main_test

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

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

// A folder excluded while the archive was away: the next push removes its copy.
func TestPushWorkPurgesExcluded(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, po, _ := poSession(t, m)
	m.workRun(t, home, "push", "--no-html")
	if _, err := os.Stat(workCopy(m, po)); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(m.claudeDir, "session-sync.json")
	cfg := map[string]any{}
	if raw, err := os.ReadFile(cfgPath); err == nil {
		json.Unmarshal(raw, &cfg)
	}
	cfg["workExclude"] = []string{po}
	b, _ := json.Marshal(cfg)
	mustWrite(t, cfgPath, string(b))

	m.workRun(t, home, "push", "--no-html")
	if _, err := os.Stat(workCopy(m, po)); !os.IsNotExist(err) {
		t.Fatalf("excluded copy remains: %v", err)
	}
	if wf := workShard(t, m); len(wf) != 0 {
		t.Fatalf("shard: %+v", wf)
	}
}
