package main_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
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
	n := 0
	// Turn ids of this conversation's own, as real ones are: push takes a transcript
	// whose last turn another holds for a copy of that one.
	msg := func(role string, content any) map[string]any {
		n++
		return map[string]any{"type": role, "cwd": cwd, "sessionId": forkSession, "timestamp": "2026-11-21T05:00:00.000Z",
			"uuid": fmt.Sprintf("w%d-%s", n, claude.Slug(cwd)), "message": map[string]any{"role": role, "content": content}}
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
	if wf := workShard(t, m); len(wf) != 1 || wf[0].Path != po || wf[0].Files != 2 || wf[0].Skipped != "" || wf[0].Bytes != 33+8 {
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

// sparseParts writes n files of 9.9 MB in dir: sparse, so they count toward the cap
// without using the disk. 26 of them pass 250 MB.
func sparseParts(t *testing.T, dir string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		p := filepath.Join(dir, fmt.Sprintf("part%02d.csv", i))
		mustWrite(t, p, "")
		if err := os.Truncate(p, (99<<20)/10); err != nil {
			t.Fatal(err)
		}
	}
}

// The cap holds per first-level subfolder: an over-cap subfolder is skipped and named,
// and the rest of the folder is backed up.
func TestPushWorkSubfolderOverCap(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home := t.TempDir()
	w := filepath.Join(home, "SQL")
	sparseParts(t, filepath.Join(w, "big"), 26)
	mustWrite(t, filepath.Join(w, "small", "a.md"), "aa\n")
	mustWrite(t, filepath.Join(w, "small", "deep", "b.sql"), "select 1;\n")
	mustWrite(t, filepath.Join(w, "top.md"), "top\n")
	workSession(t, m, home, []string{filepath.Join(w, "top.md")})

	if out := m.workRun(t, home, "push", "--no-html", "--dry-run"); strings.Contains(out, "part00") || !strings.Contains(out, "would copy "+filepath.Join(w, "small", "a.md")) {
		t.Fatalf("dry run: %s", out)
	}
	m.workRun(t, home, "push", "--no-html")
	enc := "MAC/" + work.Encode(w) + "/"
	if got, want := workFiles(m.archive), []string{enc + "small/a.md", enc + "small/deep/b.sql", enc + "top.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("archived %v, want %v", got, want)
	}
	if wf := workShard(t, m); len(wf) != 1 || wf[0].Path != w || wf[0].Skipped != "over 250 MB: big" || wf[0].Files != 3 || wf[0].Bytes != 3+10+4 {
		t.Fatalf("shard: %+v", wf)
	}
	if log := mustRead(t, filepath.Join(m.claudeDir, "session-sync.log")); !strings.Contains(log,
		"WARN: work folder "+w+": subfolder big is over 250 MB after filtering; not backed up (exclude parts with config exclude-folder)") {
		t.Fatalf("log: %s", log)
	}
}

// Loose files over the cap are skipped as one unit; the subfolders are still copied.
func TestPushWorkOverCap(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home := t.TempDir()
	big := filepath.Join(home, "data")
	sparseParts(t, big, 26)
	mustWrite(t, filepath.Join(big, "notes", "n.md"), "n\n")
	workSession(t, m, home, []string{filepath.Join(big, "part00.csv")})

	m.workRun(t, home, "push", "--no-html")
	if got, want := workFiles(m.archive), []string{"MAC/" + work.Encode(big) + "/notes/n.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("archived %v, want %v", got, want)
	}
	if wf := workShard(t, m); len(wf) != 1 || wf[0].Path != big || wf[0].Skipped != "over 250 MB: (files in the folder itself)" || wf[0].Files != 1 || wf[0].Bytes != 2 {
		t.Fatalf("shard: %+v", wf)
	}
	if log := mustRead(t, filepath.Join(m.claudeDir, "session-sync.log")); !strings.Contains(log, "WARN: work folder "+big+": subfolder (files in the folder itself) is over 250 MB") {
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

// Push reads nothing restore refuses to write: a session in VS Code's settings folder
// must not carry its state databases to the archive.
func TestPushWorkSkipsLibrary(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, po, _ := poSession(t, m)
	code := filepath.Join(home, "Library", "Application Support", "Code")
	mustWrite(t, filepath.Join(code, "User", "settings.json"), "{}\n")
	mustWrite(t, filepath.Join(code, "state.md"), "token\n")
	workSession(t, m, home, []string{filepath.Join(code, "User", "settings.json"), filepath.Join(po, "r.md")},
		`cd "`+code+`" && echo token > state.md`)

	m.workRun(t, home, "push", "--no-html", "--scan-history")
	want := []string{"MAC/" + work.Encode(po) + "/out.csv", "MAC/" + work.Encode(po) + "/r.md"}
	if got := workFiles(m.archive); !reflect.DeepEqual(got, want) {
		t.Errorf("archived %v, want %v", got, want)
	}
	if wf := workShard(t, m); len(wf) != 1 || wf[0].Path != po {
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
	p := workPairAt(t, t.TempDir())
	p.a.workRun(t, p.homeA, "push", "--no-html")
	return p
}

// workPairAt is newWorkPair with A's home at homeA, before A pushes.
func workPairAt(t *testing.T, homeA string) workPair {
	t.Helper()
	arch := t.TempDir()
	p := workPair{a: newMachine(t, "WIN", arch), b: newMachine(t, "MAC", arch), homeA: homeA, homeB: t.TempDir()}
	p.repo = gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	p.folder = filepath.Join(p.homeA, "My Work", "run 1")
	mustWrite(t, filepath.Join(p.folder, "রিপোর্ট.md"), "report v1\n")
	mustWrite(t, filepath.Join(p.homeA, "Desktop", "todo.md"), "todo\n") // a broad folder: this one file only
	workSession(t, p.a, p.repo, []string{filepath.Join(p.folder, "রিপোর্ট.md"), filepath.Join(p.homeA, "Desktop", "todo.md")},
		`cd "`+p.folder+`"`)
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
	// Worked on here: A's conversation filed here never makes it B's (C2).
	workSession(t, p.b, gitRepo(t, filepath.Join(t.TempDir(), "dev", "web")), []string{filepath.Join(p.placed(), "b.md")})
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

	// Placed elsewhere later, the pulled path is never one to move conversations off:
	// on one layout it is where B's own conversations are too (I1).
	dir := filepath.Join(home, "elsewhere")
	mustMkdir(t, dir)
	configAs(t, b.claudeDir, "MAC", "map-folder", po, dir)
	b.workRun(t, home, "import", "--no-plugins")
	if h := work.LoadHeld(filepath.Join(b.claudeDir, "session-sync-base", "work-held.json")); len(h.Prev) != 0 || h.Placed["WIN|"+po] != dir {
		t.Fatalf("placed %v, earlier targets %v", h.Placed, h.Prev)
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
	// One file system stands in for two: A's folder is not on B.
	if err := os.RemoveAll(po); err != nil {
		t.Fatal(err)
	}
	out := b.workRun(t, homeB, "import", "--no-plugins")

	if _, err := os.Stat(b.session(filepath.Join(homeB, "SQL", "po"))); err != nil {
		t.Fatalf("not filed for the placed folder: %v", err)
	}
	if !strings.Contains(out, "(matched by work folder)") {
		t.Fatalf("output: %s", out)
	}
	if _, err := os.Stat(b.session(po)); !os.IsNotExist(err) {
		t.Fatalf("filed for A's path: %v", err)
	}
}

// A work folder above a repository - loose files in ~/Developer - must not pull the
// repository in with it: a project with a git remote is found by that remote, where
// this machine's own sessions for it are, never filed under the placed folder.
func TestImportRepoInsideWorkFolderMatchesByRemote(t *testing.T) {
	arch := t.TempDir()
	a, b := newMachine(t, "WIN", arch), newMachine(t, "MAC", arch)
	homeA, homeB := t.TempDir(), t.TempDir()
	dev := filepath.Join(homeA, "Developer")
	repo := gitRepo(t, filepath.Join(dev, "apps", "api"))
	mustWrite(t, filepath.Join(dev, "notes.md"), "x\n")
	workSession(t, a, repo, []string{filepath.Join(dev, "notes.md")})
	a.workRun(t, homeA, "push", "--no-html")
	if wf := workShard(t, a); len(wf) != 1 || wf[0].Path != dev {
		t.Fatalf("work folders %+v, want %s", wf, dev)
	}
	// One file system stands in for two: A's clone is not on B.
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	clone := gitRepo(t, filepath.Join(homeB, "code", "api"))

	out := b.workRun(t, homeB, "import", "--no-plugins")
	if _, err := os.Stat(filepath.Join(homeB, "Developer", "notes.md")); err != nil {
		t.Fatalf("work folder not placed: %v", err)
	}
	if _, err := os.Stat(b.session(filepath.Join(homeB, "Developer", "apps", "api"))); !os.IsNotExist(err) {
		t.Fatalf("filed under the placed work folder: %v\n%s", err, out)
	}
	if got := mustRead(t, b.session(clone)); !strings.Contains(got, inJSON(clone)) {
		t.Fatalf("not repointed to the clone:\n%s", got)
	}
	if !strings.Contains(out, "(matched by git remote)") {
		t.Fatalf("output: %s", out)
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
	tr := workSession(t, c, repoC, []string{filepath.Join(folderC, "রিপোর্ট.md")})
	// A session id of its own: WIN's, the same id, reads as one LNX filed from WIN.
	if err := os.Rename(tr, filepath.Join(filepath.Dir(tr), "77777777-7777-7777-7777-777777777777.jsonl")); err != nil {
		t.Fatal(err)
	}
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

// A folder a runtime loads code from is never recorded as a work folder: as one of this
// machine's own, an origin naming it would let another machine write there.
func TestPushWorkSkipsCodeFolder(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home := t.TempDir()
	lib := filepath.Join(home, "p", "node_modules", "lodash")
	mustWrite(t, filepath.Join(lib, "index.js"), "x\n")
	workSession(t, m, home, []string{filepath.Join(lib, "index.js")}, `cd "`+lib+`"`)

	m.workRun(t, home, "push", "--no-html")
	if wf := workShard(t, m); len(wf) != 0 {
		t.Fatalf("shard: %+v", wf)
	}
	if got := workFiles(m.archive); len(got) != 0 {
		t.Fatalf("archived %v", got)
	}
	if rec := mustRead(t, filepath.Join(m.claudeDir, "session-sync-base", "work-folders.json")); strings.Contains(rec, "node_modules") {
		t.Fatalf("recorded: %s", rec)
	}
}

// A's folder is placed on B under claude-work/ (not B's home layout), B adds a file
// there, and A gets it back in its original folder: only an origin (rule 0) does that.
func TestWorkRoundTrip(t *testing.T) {
	p := newWorkPair(t)
	configAs(t, p.b.claudeDir, "MAC", "work-placement", "machine")
	p.b.workRun(t, p.homeB, "import", "--no-plugins")
	var placed string
	_ = filepath.WalkDir(filepath.Join(p.homeB, "claude-work"), func(q string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == "রিপোর্ট.md" {
			placed = filepath.Dir(q)
		}
		return nil
	})
	if placed == "" {
		t.Fatal("A's folder was not placed under claude-work")
	}
	repoB := gitRepo(t, filepath.Join(t.TempDir(), "dev", "web"))
	newFile := filepath.Join(placed, "from-b.md")
	mustWrite(t, newFile, "written on B\n")
	workSession(t, p.b, repoB, []string{newFile})
	p.b.workRun(t, p.homeB, "push", "--no-html")

	var found bool
	for _, f := range workShard(t, p.b) {
		if f.Path == placed && f.Origin != nil && f.Origin.Machine == "WIN" && f.Origin.Path == p.folder {
			found = true
		}
	}
	if !found {
		t.Fatalf("no origin on %s: %+v", placed, workShard(t, p.b))
	}
	p.a.workRun(t, p.homeA, "import", "--no-plugins")
	if got := mustRead(t, filepath.Join(p.folder, "from-b.md")); got != "written on B\n" {
		t.Fatalf("A's original folder has %q", got)
	}
	// B's conversation, filed on A, names A's original folder, not B's placement (M8).
	if s := mustRead(t, p.a.session(repoB)); !strings.Contains(s, inJSON(filepath.Join(p.folder, "from-b.md"))) || strings.Contains(s, inJSON(placed)) {
		t.Fatalf("A's copy of B's conversation:\n%s", s)
	}
	if _, err := os.Stat(filepath.Join(p.homeA, "claude-work")); !os.IsNotExist(err) {
		t.Fatalf("A placed it under claude-work: %v", err)
	}
}

// aged makes a file two hours old: --repoint leaves anything newer alone.
func aged(t *testing.T, path string) {
	t.Helper()
	at := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// stalePair is B after an import from before A's folder was in the archive: B's copy of
// A's two-hour-old session still names A's folder. A has pushed the folder since.
func stalePair(t *testing.T, homeA string) workPair {
	t.Helper()
	p := workPairAt(t, homeA)
	aged(t, p.a.session(p.repo))
	configAs(t, p.a.claudeDir, "WIN", "work", "off")
	p.a.workRun(t, p.homeA, "push", "--no-html")
	p.b.workRun(t, p.homeB, "import", "--no-plugins")
	configAs(t, p.a.claudeDir, "WIN", "work", "on")
	p.a.workRun(t, p.homeA, "push", "--no-html")
	if s := mustRead(t, p.b.session(p.repo)); !strings.Contains(s, inJSON(p.folder)) {
		t.Fatalf("B's copy does not name A's folder:\n%s", s)
	}
	return p
}

// repointBackups is every backed-up copy of rel in B's session bucket, one per
// --repoint run, each in a <yyyymmdd-hhmm>[-n] folder.
func (p workPair) repointBackups(t *testing.T, rel string) []string {
	t.Helper()
	base := filepath.Join(p.b.claudeDir, "repoint-backup")
	found, _ := filepath.Glob(filepath.Join(base, "*", claude.Slug(p.repo), rel))
	for _, f := range found {
		r, _ := filepath.Rel(base, f)
		stamp := strings.Split(filepath.ToSlash(r), "/")[0]
		if _, err := time.Parse("20060102-1504", stamp[:min(13, len(stamp))]); err != nil {
			t.Fatalf("backup folder %q is not <yyyymmdd-hhmm>", stamp)
		}
	}
	return found
}

func TestRepointExisting(t *testing.T) {
	p := stalePair(t, t.TempDir())
	session := p.b.session(p.repo)
	before := mustRead(t, session)

	// A dry run writes nothing, so it speaks of the state the import would leave.
	out := p.b.workRun(t, p.homeB, "import", "--no-plugins", "--dry-run")
	if !strings.Contains(out, "after this import, 1 conversation(s) would still point at other machines' work folders; run claude-sessions import --repoint then") ||
		strings.Contains(out, "here still point") {
		t.Fatalf("dry run hint: %s", out)
	}

	out = p.b.workRun(t, p.homeB, "import", "--no-plugins")
	if !strings.Contains(out, "1 conversation(s) here still point at other machines' work folders: run claude-sessions import --repoint") {
		t.Fatalf("no hint: %s", out)
	}
	if mustRead(t, session) != before {
		t.Fatal("a plain import rewrote a conversation already here")
	}

	out = p.b.workRun(t, p.homeB, "import", "--no-plugins", "--repoint")
	s := mustRead(t, session)
	if !strings.Contains(s, inJSON(filepath.Join(p.placed(), "রিপোর্ট.md"))) || !strings.Contains(s, inJSON(`cd "`+p.placed()+`"`)) ||
		!strings.Contains(s, inJSON(filepath.Join(p.homeB, "Desktop", "todo.md"))) || strings.Contains(s, inJSON(p.homeA)) {
		t.Fatalf("not repointed:\n%s\n%s", s, out)
	}
	if !strings.Contains(out, "repointed 1 conversation(s)") {
		t.Fatalf("output: %s", out)
	}
	if st, err := os.Stat(session); err != nil || time.Since(st.ModTime()) < time.Hour {
		t.Fatalf("time not kept: %v %v", st, err)
	}
	backups := p.repointBackups(t, forkSession+".jsonl")
	if len(backups) != 1 || mustRead(t, backups[0]) != before {
		t.Fatalf("backup: %v", backups)
	}
	if out := p.b.workRun(t, p.homeB, "import", "--no-plugins"); strings.Contains(out, "--repoint") {
		t.Fatalf("still hinted after --repoint: %s", out)
	}
}

func TestRepointSkipsRecent(t *testing.T) {
	p := stalePair(t, t.TempDir())
	session := p.b.session(p.repo)
	at := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(session, at, at); err != nil {
		t.Fatal(err)
	}
	before := mustRead(t, session)

	out := p.b.workRun(t, p.homeB, "import", "--no-plugins", "--repoint")
	if !strings.Contains(out, "1 conversation(s) used in the last hour left alone") {
		t.Fatalf("output: %s", out)
	}
	if mustRead(t, session) != before {
		t.Fatal("a recent conversation was rewritten")
	}
	if _, err := os.Stat(filepath.Join(p.b.claudeDir, "repoint-backup")); !os.IsNotExist(err) {
		t.Fatalf("backup folder made: %v", err)
	}
}

func TestRepointDryRun(t *testing.T) {
	p := stalePair(t, t.TempDir())
	session := p.b.session(p.repo)
	before := mustRead(t, session)

	out := p.b.workRun(t, p.homeB, "import", "--no-plugins", "--repoint", "--dry-run")
	if !strings.Contains(out, "would repoint 1 conversation(s)") {
		t.Fatalf("output: %s", out)
	}
	if mustRead(t, session) != before {
		t.Fatal("dry run rewrote a conversation")
	}
	for _, f := range []string{filepath.Join(p.b.claudeDir, "repoint-backup"), filepath.Join(p.b.claudeDir, "session-sync-base", "repointed.json")} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Fatalf("dry run wrote %s: %v", f, err)
		}
	}
}

// A placement changed after a repoint: the next one moves the old target's paths, a
// subagent's too, to the new folder. B has pushed in between, so the old target is one
// of B's own work folders by then: a remap move is trusted all the same, both its ends
// being B's own record (R23). Two runs in one minute keep both backups.
func TestRepointRemap(t *testing.T) {
	p := stalePair(t, t.TempDir())
	session := p.b.session(p.repo)
	agentRel := filepath.Join(forkSession, "subagents", "agent-1.jsonl")
	agent := filepath.Join(filepath.Dir(session), agentRel)
	l, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "read " + filepath.Join(p.folder, "রিপোর্ট.md")}})
	mustWrite(t, agent, string(l)+"\n")
	aged(t, agent)
	original := mustRead(t, session)

	p.b.workRun(t, p.homeB, "import", "--no-plugins", "--repoint")
	first := mustRead(t, session)
	if !strings.Contains(first, inJSON(p.placed())) || !strings.Contains(mustRead(t, agent), inJSON(p.placed())) {
		t.Fatalf("first repoint:\n%s", first)
	}
	// B works in it, which makes it one of B's own; A's conversation filed here does not (C2).
	mustWrite(t, filepath.Join(p.placed(), "b.md"), "B's own\n")
	aged(t, workSession(t, p.b, gitRepo(t, filepath.Join(t.TempDir(), "dev", "web")), []string{filepath.Join(p.placed(), "b.md")}))
	p.b.workRun(t, p.homeB, "push", "--no-html")
	if own := mustRead(t, filepath.Join(p.b.claudeDir, "session-sync-base", "work-folders.json")); !strings.Contains(own, quoted(p.placed())) {
		t.Fatalf("the placed folder is not one of B's own after a push: %s", own)
	}
	dir := filepath.Join(p.homeB, "mapped")
	mustMkdir(t, dir)
	configAs(t, p.b.claudeDir, "MAC", "map-folder", p.folder, dir)
	out := p.b.workRun(t, p.homeB, "import", "--no-plugins", "--repoint")

	s, a := mustRead(t, session), mustRead(t, agent)
	if !strings.Contains(s, inJSON(filepath.Join(dir, "রিপোর্ট.md"))) || strings.Contains(s, inJSON(p.placed())) ||
		!strings.Contains(a, inJSON(filepath.Join(dir, "রিপোর্ট.md"))) {
		t.Fatalf("not moved to the new folder:\n%s\n%s\n%s", s, a, out)
	}
	if got := mustRead(t, filepath.Join(dir, "রিপোর্ট.md")); got != "report v1\n" {
		t.Fatalf("not placed at the new folder: %q", got)
	}
	if _, err := os.Stat(filepath.Join(p.placed(), "রিপোর্ট.md")); err != nil {
		t.Fatalf("old copy moved: %v", err)
	}
	var got []string
	for _, b := range p.repointBackups(t, forkSession+".jsonl") {
		got = append(got, mustRead(t, b))
	}
	if len(got) != 2 || !slices.Contains(got, original) || !slices.Contains(got, first) {
		t.Fatalf("backups: %q", got)
	}
	if n := len(p.repointBackups(t, agentRel)); n != 2 {
		t.Fatalf("%d subagent backup(s)", n)
	}
	_ = filepath.WalkDir(filepath.Dir(session), func(q string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), ".tmp") {
			t.Errorf("temp file left: %s", q)
		}
		return nil
	})
}

// --repoint made the copy shorter (A's home is longer than B's): push replaces the
// archived copy with it once, without the truncation warning, then leaves it be.
func TestRepointedAcceptedByPush(t *testing.T) {
	for _, viaHook := range []bool{false, true} {
		p := stalePair(t, filepath.Join(t.TempDir(), "a-much-longer-home-folder-for-the-first-machine"))
		p.b.workRun(t, p.homeB, "import", "--no-plugins", "--repoint")
		session := p.b.session(p.repo)
		archived := filepath.Join(p.b.archive, "projects", claude.Slug(p.repo), forkSession+".jsonl")
		local, _ := os.Stat(session)
		if arch, _ := os.Stat(archived); local.Size() >= arch.Size() {
			t.Fatalf("repoint did not shorten it: %d >= %d", local.Size(), arch.Size())
		}
		push := func() {
			if viaHook {
				p.b.hook(t, session)
			} else {
				p.b.workRun(t, p.homeB, "push", "--no-html")
			}
		}

		push()
		if mustRead(t, archived) != mustRead(t, session) {
			t.Fatalf("hook %v: archived copy not replaced", viaHook)
		}
		if log, _ := os.ReadFile(filepath.Join(p.b.claudeDir, "session-sync.log")); strings.Contains(string(log), "shorter") {
			t.Fatalf("hook %v: log: %s", viaHook, log)
		}
		if rec, _ := os.ReadFile(filepath.Join(p.b.claudeDir, "session-sync-base", "repointed.json")); strings.Contains(string(rec), forkSession) {
			t.Fatalf("hook %v: entry kept: %s", viaHook, rec)
		}
		was, _ := os.Stat(archived)
		push()
		if now, _ := os.Stat(archived); now.Size() != was.Size() || !now.ModTime().Equal(was.ModTime()) {
			t.Fatalf("hook %v: copied again", viaHook)
		}
	}
}

// turn appends one entry to m's copy of the pair's session and dates it at.
func (p workPair) turn(t *testing.T, m machine, uuid string, at time.Time) {
	t.Helper()
	l, _ := json.Marshal(map[string]any{"type": "user", "uuid": uuid, "sessionId": forkSession,
		"message": map[string]any{"role": "user", "content": "go on, " + uuid}})
	f, err := os.OpenFile(m.session(p.repo), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(append(l, '\n'))
	f.Close()
	if err := os.Chtimes(m.session(p.repo), at, at); err != nil {
		t.Fatal(err)
	}
}

// A session continued on both machines is a fork. Repointed, B's copy still holds
// none of A's later turns: push must keep the archived copy and the fork, not put B's
// copy over A's turns.
func TestRepointedForkKeepsOtherTurns(t *testing.T) {
	p := stalePair(t, t.TempDir())
	now := time.Now()
	p.turn(t, p.a, "a1", now.Add(-100*time.Minute))
	p.a.workRun(t, p.homeA, "push", "--no-html")
	p.turn(t, p.b, "b1", now.Add(-90*time.Minute))
	// One disk stands in for two: A's folder is not on B's, or B's push takes it for B's own.
	away(t, p.homeA, func() { p.b.workRun(t, p.homeB, "push", "--no-html") })
	fork := filepath.Join(p.b.archive, "forks", claude.Slug(p.repo), forkSession, "MAC.jsonl")
	if !strings.Contains(mustRead(t, fork), `"b1"`) {
		t.Fatal("B's push kept no fork")
	}

	p.b.workRun(t, p.homeB, "import", "--no-plugins", "--repoint")
	if !strings.Contains(mustRead(t, p.b.session(p.repo)), inJSON(p.placed())) {
		t.Fatal("not repointed")
	}
	p.b.workRun(t, p.homeB, "push", "--no-html")
	p.a.workRun(t, p.homeA, "push", "--no-html")
	archived := filepath.Join(p.b.archive, "projects", claude.Slug(p.repo), forkSession+".jsonl")
	if s := mustRead(t, archived); !strings.Contains(s, `"a1"`) {
		t.Fatalf("A's turn is gone from the archive:\n%s", s)
	}
	if s := mustRead(t, fork); !strings.Contains(s, `"b1"`) {
		t.Fatalf("B's turn is gone from the fork:\n%s", s)
	}
}

// A placement changes in one import and conversations are repointed by a later one,
// as the hint says - straight after a plain import, or once a conversation used in the
// last hour has aged. The earlier target is remembered until a --repoint has moved
// everything off it, then forgotten.
func TestRepointRemapLater(t *testing.T) {
	for _, recent := range []bool{false, true} {
		p := stalePair(t, t.TempDir())
		session := p.b.session(p.repo)
		held := filepath.Join(p.b.claudeDir, "session-sync-base", "work-held.json")
		p.b.workRun(t, p.homeB, "import", "--no-plugins", "--repoint")
		dir := filepath.Join(p.homeB, "mapped")
		mustMkdir(t, dir)
		configAs(t, p.b.claudeDir, "MAC", "map-folder", p.folder, dir)
		if out := p.b.workRun(t, p.homeB, "import", "--no-plugins"); !strings.Contains(out, "1 conversation(s) here still point") {
			t.Fatalf("recent %v: no hint after the placement changed: %s", recent, out)
		}
		if recent {
			at := time.Now().Add(-10 * time.Minute)
			if err := os.Chtimes(session, at, at); err != nil {
				t.Fatal(err)
			}
			if out := p.b.workRun(t, p.homeB, "import", "--no-plugins", "--repoint"); !strings.Contains(out, "1 conversation(s) used in the last hour left alone") {
				t.Fatalf("not left for later: %s", out)
			}
			if !strings.Contains(mustRead(t, held), `"prev"`) {
				t.Fatal("old target forgotten while a conversation still names it")
			}
			aged(t, session)
		}

		out := p.b.workRun(t, p.homeB, "import", "--no-plugins", "--repoint")
		if s := mustRead(t, session); !strings.Contains(out, "repointed 1 conversation(s)") ||
			!strings.Contains(s, inJSON(filepath.Join(dir, "রিপোর্ট.md"))) || strings.Contains(s, inJSON(p.placed())) {
			t.Fatalf("recent %v: not repointed to the new folder:\n%s\n%s", recent, s, out)
		}
		if h := mustRead(t, held); strings.Contains(h, `"prev"`) {
			t.Fatalf("recent %v: old target kept after a full repoint: %s", recent, h)
		}
		if out := p.b.workRun(t, p.homeB, "import", "--no-plugins"); strings.Contains(out, "--repoint") {
			t.Fatalf("recent %v: still hinted: %s", recent, out)
		}
	}
}

// doctorWorkLine returns the doctor lines about work folders, from --json.
func doctorWorkLine(t *testing.T, m machine) (level, detail, advice string) {
	t.Helper()
	var rep struct {
		Checks []struct{ Name, Level, Detail, Advice string }
	}
	if err := json.Unmarshal([]byte(m.run(t, "doctor", "--json", "--no-write-probe")), &rep); err != nil {
		t.Fatal(err)
	}
	for _, c := range rep.Checks {
		if c.Name == "work folders" {
			return c.Level, c.Detail, c.Advice
		}
	}
	t.Fatal("no work folders check")
	return
}

func TestDoctorWork(t *testing.T) {
	arch := t.TempDir()
	m := newMachine(t, "MAC", arch)
	shard := func(wf ...map[string]any) {
		b, _ := json.Marshal(map[string]any{"schemaVersion": 1, "machine": "MAC", "projects": map[string]any{}, "workFolders": wf})
		mustWrite(t, filepath.Join(arch, "manifest", "MAC.json"), string(b))
	}

	configAs(t, m.claudeDir, "MAC", "work", "off")
	if l, d, _ := doctorWorkLine(t, m); l != "info" || !strings.Contains(d, "off") {
		t.Fatalf("off: %s %s", l, d)
	}

	configAs(t, m.claudeDir, "MAC", "work", "on")
	shard(map[string]any{"path": "/w/a", "files": 3, "bytes": 100})
	if l, d, _ := doctorWorkLine(t, m); l != "ok" || !strings.Contains(d, "1 folder(s), 3 file(s)") {
		t.Fatalf("on: %s %s", l, d)
	}
	shard(map[string]any{"path": "/w/a", "files": 3, "bytes": 100, "packed": 2})
	if l, d, _ := doctorWorkLine(t, m); l != "ok" || !strings.Contains(d, "1 folder(s), 3 file(s), 2 packed unit(s)") {
		t.Fatalf("packed: %s %s", l, d)
	}

	shard(map[string]any{"path": "/w/a", "files": 3, "bytes": work.WarnBytes + 1})
	if l, _, a := doctorWorkLine(t, m); l != "warn" || !strings.Contains(a, "config exclude-folder") {
		t.Fatalf("big: %s %s", l, a)
	}

	shard(map[string]any{"path": "/w/huge", "files": 4, "bytes": 100, "skipped": "over 250 MB: big, logs"})
	if l, d, a := doctorWorkLine(t, m); l != "warn" || !strings.Contains(d, "/w/huge (over 250 MB: big, logs)") || !strings.Contains(a, "subfolder") {
		t.Fatalf("skipped: %s %s %s", l, d, a)
	}
}

// A folder listed with no copy in the archive (over the cap, or not pushed yet) is
// still placed for the conversations: they name where its files will land (I4).
func TestImportWorkNoCopyStillRepoints(t *testing.T) {
	p := newWorkPair(t)
	if err := os.RemoveAll(workCopy(p.a, p.folder)); err != nil {
		t.Fatal(err)
	}
	out := p.b.workRun(t, p.homeB, "import", "--no-plugins")
	s := mustRead(t, p.b.session(p.repo))
	if !strings.Contains(s, inJSON(filepath.Join(p.placed(), "রিপোর্ট.md"))) || strings.Contains(s, inJSON(p.folder)) {
		t.Fatalf("not repointed:\n%s\n%s", s, out)
	}
	if _, err := os.Stat(p.placed()); !os.IsNotExist(err) {
		t.Fatalf("files restored from nowhere: %v", err)
	}
}

// A work folder holding the Claude folder or a temp folder never archives them (M2):
// a Claude folder with no dot is not skipped as a dot folder.
func TestPushSkipsClaudeRootAndTemp(t *testing.T) {
	home := t.TempDir()
	w := filepath.Join(home, "W")
	m := machine{name: "MAC", claudeDir: filepath.Join(w, "claude"), archive: t.TempDir()}
	mustWrite(t, filepath.Join(m.claudeDir, "session-sync-base", "imported.json"), "{}\n") // as newMachine
	mustMkdir(t, filepath.Join(m.claudeDir, "projects"))
	mustWrite(t, filepath.Join(w, "a.md"), "mine\n")
	mustWrite(t, filepath.Join(w, "tmp", "t.md"), "scratch\n")
	workSession(t, m, w, []string{filepath.Join(w, "a.md")})
	cmd := m.workCmd(home, "push", "--no-html")
	cmd.Env = append(cmd.Env, "CLAUDE_SESSIONS_TEMP_DIRS="+filepath.Join(w, "tmp"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	files := workFiles(m.archive)
	if !slices.ContainsFunc(files, func(f string) bool { return strings.HasSuffix(f, "W/a.md") }) {
		t.Fatalf("folder not backed up: %v", files)
	}
	for _, f := range files {
		if strings.Contains(f, "W/claude/") || strings.Contains(f, "W/tmp/") {
			t.Errorf("archived: %s", f)
		}
	}
}

// exclude-folder removes the folder's copy where no entry lists it, under this
// machine's name and the names it went by before; another machine's stays (M6).
func TestExcludeFolderPurgesFormerNames(t *testing.T) {
	arch := t.TempDir()
	m := newMachine(t, "MAC", arch)
	configAs(t, m.claudeDir, "MAC", "set-destination", arch)
	configAs(t, m.claudeDir, "OLD", "set-machine", "MAC")
	dir := t.TempDir()
	enc := filepath.FromSlash(work.Encode(dir))
	for _, n := range []string{"MAC", "OLD", "OTHER"} {
		mustWrite(t, filepath.Join(arch, "work", n, enc, "r.md"), "x")
	}
	out := configAs(t, m.claudeDir, "MAC", "exclude-folder", dir)
	for _, n := range []string{"MAC", "OLD"} {
		if _, err := os.Stat(filepath.Join(arch, "work", n, enc)); !os.IsNotExist(err) {
			t.Errorf("%s's copy remains: %v\n%s", n, err, out)
		}
	}
	if _, err := os.Stat(filepath.Join(arch, "work", "OTHER", enc, "r.md")); err != nil {
		t.Errorf("another machine's copy removed: %v", err)
	}
	if !strings.Contains(out, "removed "+dir) {
		t.Errorf("output: %s", out)
	}
}

// The first run marks every conversation already here as read: none names a folder
// to back up, whoever wrote it. push --scan-history reads them all once, after a
// warning on stderr (R26).
func TestPushScanHistory(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, po, _ := poSession(t, m)
	// From before this version: no record yet.
	if err := os.Remove(filepath.Join(m.claudeDir, "session-sync-base", "imported.json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	m.workRun(t, home, "push", "--no-html")
	if _, err := os.Stat(filepath.Join(workCopy(m, po), "r.md")); !os.IsNotExist(err) {
		t.Fatalf("an earlier conversation's folder backed up without --scan-history: %v", err)
	}
	cmd := m.workCmd(home, "push", "--no-html", "--scan-history")
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("push --scan-history: %v\n%s%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "imported from other computers") || !strings.Contains(stderr.String(), "trust") {
		t.Errorf("no warning on stderr: %q", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(workCopy(m, po), "r.md")); err != nil {
		t.Errorf("not backed up with --scan-history: %v", err)
	}
}

// An excluded folder is not restored into: neither a placed folder inside it nor the
// files below an excluded subfolder arrive, and the import says so.
func TestImportWorkExcludedFolderNotRestored(t *testing.T) {
	p := newWorkPair(t)
	configAs(t, p.b.claudeDir, "MAC", "exclude-folder", p.placed())
	out := p.b.workRun(t, p.homeB, "import", "--no-plugins")

	if _, err := os.Stat(p.placed()); !os.IsNotExist(err) {
		t.Fatalf("restored into an excluded folder: %v", err)
	}
	if s := mustRead(t, p.b.session(p.repo)); strings.Contains(s, inJSON(p.placed())) {
		t.Fatalf("transcript moved to an excluded folder:\n%s", s)
	}
	if !strings.Contains(out, "excluded here") || !strings.Contains(out, p.placed()) {
		t.Fatalf("output: %s", out)
	}
}

func TestImportWorkExcludedSubfolderSkipped(t *testing.T) {
	homeA := t.TempDir()
	p := workPairAt(t, homeA)
	mustWrite(t, filepath.Join(p.folder, "private", "secret.md"), "s\n")
	p.a.workRun(t, p.homeA, "push", "--no-html")
	configAs(t, p.b.claudeDir, "MAC", "exclude-folder", filepath.Join(p.placed(), "private"))
	out := p.b.workRun(t, p.homeB, "import", "--no-plugins")

	if got := mustRead(t, filepath.Join(p.placed(), "রিপোর্ট.md")); got != "report v1\n" {
		t.Fatalf("restored %q", got)
	}
	if _, err := os.Stat(filepath.Join(p.placed(), "private")); !os.IsNotExist(err) {
		t.Fatalf("restored into an excluded subfolder: %v", err)
	}
	if !strings.Contains(out, "excluded here") {
		t.Fatalf("output: %s", out)
	}
}

func TestImportNoWork(t *testing.T) {
	p := newWorkPair(t)
	out := p.b.workRun(t, p.homeB, "import", "--no-plugins", "--no-work")
	if _, err := os.Stat(filepath.Join(p.homeB, "My Work")); !os.IsNotExist(err) {
		t.Fatalf("work folder placed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.homeB, "Desktop", "todo.md")); !os.IsNotExist(err) {
		t.Fatalf("exact file placed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.b.claudeDir, "session-sync-base", "work-held.json")); !os.IsNotExist(err) {
		t.Fatalf("held record written: %v", err)
	}
	s := mustRead(t, p.b.session(p.repo))
	if !strings.Contains(s, inJSON(p.homeA)) || strings.Contains(s, inJSON(p.homeB)) {
		t.Fatalf("transcript rewritten by work moves:\n%s", s)
	}
	if !strings.Contains(out, "work folders skipped (--no-work)") || strings.Contains(out, "--repoint") {
		t.Fatalf("output: %s", out)
	}
}

func TestPullNoWork(t *testing.T) {
	p := newWorkPair(t)
	if err := os.RemoveAll(p.folder); err != nil { // one file system stands in for two
		t.Fatal(err)
	}
	out := p.b.workRun(t, p.homeB, "pull", "--no-plugins", "--no-work")
	if _, err := os.Stat(p.folder); !os.IsNotExist(err) {
		t.Fatalf("work folder restored: %v", err)
	}
	if !strings.Contains(out, "work folders skipped (--no-work)") {
		t.Fatalf("output: %s", out)
	}
}

func TestNoWorkRefusesRepoint(t *testing.T) {
	p := newWorkPair(t)
	out, err := p.b.workCmd(p.homeB, "import", "--no-plugins", "--no-work", "--repoint").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "--no-work") || !strings.Contains(string(out), "--repoint") {
		t.Fatalf("err=%v out=%s", err, out)
	}
}

// packHome makes <home>/SQL/run1 holding n files 8 days old, and a session that works
// in SQL. It returns home, SQL and where run1's pack goes in the archive.
func packHome(t *testing.T, m machine, n int) (home, w, pack string) {
	t.Helper()
	home = t.TempDir()
	w = filepath.Join(home, "SQL")
	for i := 0; i < n; i++ {
		mustWrite(t, filepath.Join(w, "run1", fmt.Sprintf("f%02d.md", i)), fmt.Sprintf("file %d\n", i))
	}
	ageFiles(t, w)
	workSession(t, m, home, nil, `cd "`+w+`" && ls`)
	return home, w, filepath.Join(workCopy(m, w), "run1.cspack.zip")
}

// ageFiles makes every file under dir 8 days old.
func ageFiles(t *testing.T, dir string) {
	t.Helper()
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			err = os.Chtimes(p, old, old)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// packIndex lists a pack's entries, sorted, with their sizes.
func packIndex(t *testing.T, pack string) map[string]int64 {
	t.Helper()
	idx, err := work.ReadPackIndex(pack)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for n, e := range idx {
		out[n] = e.Size
	}
	return out
}

func sortedWorkFiles(arch string) []string {
	got := workFiles(arch)
	slices.Sort(got)
	return got
}

// backdate sets the pack's time an hour back, so a rewrite shows, and returns its info.
func backdate(t *testing.T, pack string) os.FileInfo {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(pack, old, old); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(pack)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

func samePack(t *testing.T, pack string, before os.FileInfo) {
	t.Helper()
	fi, err := os.Stat(pack)
	if err != nil || fi.Size() != before.Size() || !fi.ModTime().Equal(before.ModTime()) {
		t.Fatalf("pack rewritten: %v %v", fi, err)
	}
}

func TestPushPacksOldUnit(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, w, pack := packHome(t, m, 6)
	m.workRun(t, home, "push", "--no-html")

	enc := "MAC/" + work.Encode(w) + "/"
	if got := sortedWorkFiles(m.archive); !reflect.DeepEqual(got, []string{enc + "run1.cspack.zip"}) {
		t.Fatalf("archived %v", got)
	}
	if idx := packIndex(t, pack); len(idx) != 6 || idx["f00.md"] != 7 || idx["f05.md"] != 7 {
		t.Fatalf("pack: %v", idx)
	}
	if wf := workShard(t, m); len(wf) != 1 || wf[0].Packed != 1 || wf[0].Files != 6 || wf[0].Bytes != 42 {
		t.Fatalf("shard: %+v", wf)
	}
	if log := mustRead(t, filepath.Join(m.claudeDir, "session-sync.log")); !strings.Contains(log, "WORK: packed "+filepath.Join(w, "run1")+" (6 files)") {
		t.Fatalf("log: %s", log)
	}
}

func TestPushSkipsYoungOrSmallUnit(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, w, _ := packHome(t, m, 6)
	day := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(filepath.Join(w, "run1", "f03.md"), day, day); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		mustWrite(t, filepath.Join(w, "run2", fmt.Sprintf("g%d.md", i)), "g\n")
	}
	ageFiles(t, filepath.Join(w, "run2"))
	m.workRun(t, home, "push", "--no-html")

	got := sortedWorkFiles(m.archive)
	if len(got) != 10 || slices.ContainsFunc(got, func(s string) bool { return strings.HasSuffix(s, ".zip") }) {
		t.Fatalf("archived %v", got)
	}
	if wf := workShard(t, m); len(wf) != 1 || wf[0].Packed != 0 || wf[0].Files != 10 {
		t.Fatalf("shard: %+v", wf)
	}
}

// The first pack is built from local files. Only archive copies of files in it are
// removed: a file deleted here but still in the archive stays.
func TestPushKeepsArchiveOnlyFiles(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, w, pack := packHome(t, m, 6)
	mustWrite(t, filepath.Join(workCopy(m, w), "run1", "gone.md"), "only in the archive\n")
	mustWrite(t, filepath.Join(workCopy(m, w), "run1", "f00.md"), "file 0\n")
	ageFiles(t, workCopy(m, w)) // a leftover of an earlier push carries the local time
	m.workRun(t, home, "push", "--no-html")

	enc := "MAC/" + work.Encode(w) + "/"
	if got := sortedWorkFiles(m.archive); !reflect.DeepEqual(got, []string{enc + "run1.cspack.zip", enc + "run1/gone.md"}) {
		t.Fatalf("archived %v", got)
	}
	if idx := packIndex(t, pack); len(idx) != 6 {
		t.Fatalf("pack: %v", idx)
	}
}

// After packing, an edit and a new file are copied loose beside the pack, which is
// left alone.
func TestPushOverlayOnly(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, w, pack := packHome(t, m, 6)
	m.workRun(t, home, "push", "--no-html")
	before := backdate(t, pack)

	mustWrite(t, filepath.Join(w, "run1", "f02.md"), "e\n") // 4 of 42 bytes, under a quarter
	mustWrite(t, filepath.Join(w, "run1", "new.md"), "n\n")
	m.workRun(t, home, "push", "--no-html")
	enc := "MAC/" + work.Encode(w) + "/"
	if got := sortedWorkFiles(m.archive); !reflect.DeepEqual(got, []string{enc + "run1.cspack.zip", enc + "run1/f02.md", enc + "run1/new.md"}) {
		t.Fatalf("archived %v", got)
	}
	if got := mustRead(t, filepath.Join(workCopy(m, w), "run1", "f02.md")); got != "e\n" {
		t.Fatalf("overlay f02.md = %q", got)
	}
	samePack(t, pack, before)

	out := m.workRun(t, home, "push", "--no-html")
	samePack(t, pack, before)
	if !strings.Contains(out, "copied 0 file(s)") {
		t.Fatalf("copied again: %s", out)
	}
}

// Changes past 50 files, or past a quarter of the pack's bytes, are folded into a new
// pack once settled, and the loose copies go.
func TestPushCompacts(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, w, pack := packHome(t, m, 6)
	m.workRun(t, home, "push", "--no-html")
	mustWrite(t, filepath.Join(w, "run1", "f00.md"), "edited 0\n") // 9 of 42 bytes: loose
	m.workRun(t, home, "push", "--no-html")
	if _, err := os.Stat(filepath.Join(workCopy(m, w), "run1", "f00.md")); err != nil {
		t.Fatalf("no overlay: %v", err)
	}

	names := []string{"f00.md"}
	for i := 0; i < 51; i++ {
		names = append(names, fmt.Sprintf("n%02d.md", i))
		mustWrite(t, filepath.Join(w, "run1", names[i+1]), "n\n")
	}
	settle(t, filepath.Join(w, "run1"), filepath.Join(workCopy(m, w), "run1"), names...)
	m.workRun(t, home, "push", "--no-html")
	enc := "MAC/" + work.Encode(w) + "/"
	if got := sortedWorkFiles(m.archive); !reflect.DeepEqual(got, []string{enc + "run1.cspack.zip"}) {
		t.Fatalf("archived %v", got)
	}
	if idx := packIndex(t, pack); len(idx) != 57 || idx["f00.md"] != 9 || idx["f01.md"] != 7 || idx["n50.md"] != 2 {
		t.Fatalf("pack: %v", idx)
	}
	if log := mustRead(t, filepath.Join(m.claudeDir, "session-sync.log")); !strings.Contains(log, "WORK: compacted "+filepath.Join(w, "run1")) {
		t.Fatalf("log: %s", log)
	}

	// One file growing past a quarter of the pack's bytes.
	m2 := newMachine(t, "MAC", t.TempDir())
	home2, w2, pack2 := packHome(t, m2, 6)
	m2.workRun(t, home2, "push", "--no-html")
	mustWrite(t, filepath.Join(w2, "run1", "f00.md"), strings.Repeat("x", 20))
	m2.workRun(t, home2, "push", "--no-html")
	settle(t, filepath.Join(w2, "run1"), filepath.Join(workCopy(m2, w2), "run1"), "f00.md")
	m2.workRun(t, home2, "push", "--no-html")
	if got := sortedWorkFiles(m2.archive); len(got) != 1 {
		t.Fatalf("archived %v", got)
	}
	if idx := packIndex(t, pack2); len(idx) != 6 || idx["f00.md"] != 20 {
		t.Fatalf("pack: %v", idx)
	}
}

// Without the local record, push reads the pack's own index; without the pack too,
// the unit is backed up loose again.
func TestPushPackRecordLost(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, w, pack := packHome(t, m, 6)
	m.workRun(t, home, "push", "--no-html")
	rec := filepath.Join(m.claudeDir, "session-sync-base", "work-packs.json")
	before := backdate(t, pack)

	if err := os.Remove(rec); err != nil {
		t.Fatal(err)
	}
	m.workRun(t, home, "push", "--no-html")
	samePack(t, pack, before)
	enc := "MAC/" + work.Encode(w) + "/"
	if got := sortedWorkFiles(m.archive); !reflect.DeepEqual(got, []string{enc + "run1.cspack.zip"}) {
		t.Fatalf("archived %v", got)
	}
	if log := mustRead(t, filepath.Join(m.claudeDir, "session-sync.log")); strings.Count(log, "WORK: packed") != 1 {
		t.Fatalf("packed again: %s", log)
	}
	if _, err := os.Stat(rec); err != nil {
		t.Fatalf("record not rebuilt: %v", err)
	}

	mustWrite(t, filepath.Join(w, "run1", "f01.md"), "edited\n")
	if err := os.Remove(rec); err != nil {
		t.Fatal(err)
	}
	m.workRun(t, home, "push", "--no-html")
	samePack(t, pack, before)
	if got := sortedWorkFiles(m.archive); !reflect.DeepEqual(got, []string{enc + "run1.cspack.zip", enc + "run1/f01.md"}) {
		t.Fatalf("archived %v", got)
	}

	if err := os.Remove(pack); err != nil {
		t.Fatal(err)
	}
	m.workRun(t, home, "push", "--no-html")
	got := sortedWorkFiles(m.archive)
	if len(got) != 6 || got[0] != enc+"run1/f00.md" {
		t.Fatalf("not backed up loose: %v", got)
	}
}

func TestPushPackDryRun(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, w, _ := packHome(t, m, 6)
	out := m.workRun(t, home, "push", "--no-html", "--dry-run")
	if !strings.Contains(out, "would pack "+filepath.Join(w, "run1")+" (6 files, ") || strings.Contains(out, "would copy "+filepath.Join(w, "run1")) {
		t.Fatalf("output: %s", out)
	}
	if _, err := os.Stat(filepath.Join(m.archive, "work")); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote work/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.claudeDir, "session-sync-base", "work-packs.json")); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote the pack record: %v", err)
	}
}

// Excluding a path inside a packed unit takes its entries out of the pack and its
// loose copies out of the archive. The rest stays: other entries, a file only the
// pack still holds, and the overlay. Excluding the whole unit removes its pack.
func TestExcludeInsideUnitPrunesPack(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	configAs(t, m.claudeDir, "MAC", "set-destination", m.archive)
	home, w, pack := packHome(t, m, 7)
	sub := filepath.Join(w, "run1", "sub")
	mustWrite(t, filepath.Join(sub, "s.md"), "private\n")
	ageFiles(t, w)
	m.workRun(t, home, "push", "--no-html")
	if idx := packIndex(t, pack); idx["sub/s.md"] != 8 {
		t.Fatalf("pack: %v", idx)
	}
	if err := os.Remove(filepath.Join(w, "run1", "f06.md")); err != nil { // only in the pack now
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(w, "run1", "f00.md"), "e\n")
	m.workRun(t, home, "push", "--no-html")

	if out := configAs(t, m.claudeDir, "MAC", "exclude-folder", sub); !strings.Contains(out, "removed "+sub) {
		t.Fatalf("output: %s", out)
	}
	if idx := packIndex(t, pack); len(idx) != 7 || idx["f06.md"] != 7 || idx["sub/s.md"] != 0 {
		t.Fatalf("pack: %v", idx)
	}
	enc := "MAC/" + work.Encode(w) + "/"
	if got := sortedWorkFiles(m.archive); !reflect.DeepEqual(got, []string{enc + "run1.cspack.zip", enc + "run1/f00.md"}) {
		t.Fatalf("archived %v", got)
	}
	if rec := mustRead(t, filepath.Join(m.claudeDir, "session-sync-base", "work-packs.json")); strings.Contains(rec, "sub/s.md") {
		t.Fatalf("record still lists the excluded file: %s", rec)
	}

	before := backdate(t, pack)
	m.workRun(t, home, "push", "--no-html")
	m.workRun(t, home, "push", "--no-html")
	samePack(t, pack, before)

	if out := configAs(t, m.claudeDir, "MAC", "exclude-folder", filepath.Join(w, "run1")); !strings.Contains(out, "removed "+filepath.Join(w, "run1")) {
		t.Fatalf("output: %s", out)
	}
	if got := workFiles(m.archive); len(got) != 0 {
		t.Fatalf("left in the archive: %v", got)
	}
}

func setTime(t *testing.T, p string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

// A pack push cannot read is never deleted by an exclusion inside its unit, nor is
// anything beside it: the next run tries again.
func TestPushUnreadablePackKept(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a mode cannot make a file unreadable on Windows")
	}
	m := newMachine(t, "MAC", t.TempDir())
	configAs(t, m.claudeDir, "MAC", "set-destination", m.archive)
	home, w, pack := packHome(t, m, 7)
	sub := filepath.Join(w, "run1", "sub")
	mustWrite(t, filepath.Join(sub, "s.md"), "private\n")
	ageFiles(t, w)
	m.workRun(t, home, "push", "--no-html")
	if err := os.Remove(filepath.Join(w, "run1", "f06.md")); err != nil { // only in the pack now
		t.Fatal(err)
	}
	gone := filepath.Join(workCopy(m, w), "run1", "gone.md")
	mustWrite(t, gone, "archive only\n")

	if err := os.Chmod(pack, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(pack, 0o644) })
	if f, err := os.Open(pack); err == nil {
		f.Close()
		t.Skip("files stay readable here")
	}
	// Excluded while the pack holds sub and cannot be read: nothing is taken out yet.
	if out, err := configExpectErr(t, m.claudeDir, "MAC", "exclude-folder", sub); err == nil {
		t.Fatalf("exclude-folder: %s", out)
	}
	m.workRun(t, home, "push", "--no-html")
	if err := os.Chmod(pack, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(gone); err != nil {
		t.Fatalf("archive-only file deleted: %v", err)
	}
	if idx := packIndex(t, pack); len(idx) != 8 || idx["f06.md"] != 7 || idx["sub/s.md"] != 8 {
		t.Fatalf("pack: %v", idx)
	}
	if log := mustRead(t, filepath.Join(m.claudeDir, "session-sync.log")); !strings.Contains(log, "WARN: excluded folder "+sub) {
		t.Fatalf("log: %s", log)
	}
}

// Loose copies of packed files left beside the pack go on a routine push, unless
// newer than the pack's entry: then they may be the only copy of newer content.
func TestPushRemovesStaleLooseCopies(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, w, _ := packHome(t, m, 6)
	m.workRun(t, home, "push", "--no-html")
	loose := filepath.Join(workCopy(m, w), "run1")
	stale, newer := filepath.Join(loose, "f00.md"), filepath.Join(loose, "f01.md")
	mustWrite(t, stale, "stale\n")
	setTime(t, stale, time.Now().Add(-30*24*time.Hour))
	mustWrite(t, newer, "newer\n")
	setTime(t, newer, time.Now().Add(-time.Hour))
	m.workRun(t, home, "push", "--no-html")
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale loose copy kept: %v", err)
	}
	if _, err := os.Stat(newer); err != nil {
		t.Fatalf("newer loose copy deleted: %v", err)
	}

	// An overlay copy of a file deleted here since, with the record lost.
	mustWrite(t, filepath.Join(w, "run1", "f02.md"), "e\n")
	m.workRun(t, home, "push", "--no-html")
	if err := os.Remove(filepath.Join(w, "run1", "f02.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(m.claudeDir, "session-sync-base", "work-packs.json")); err != nil {
		t.Fatal(err)
	}
	m.workRun(t, home, "push", "--no-html")
	if got := mustRead(t, filepath.Join(loose, "f02.md")); got != "e\n" {
		t.Fatalf("overlay copy = %q", got)
	}
}

// An overlay copy gone from the archive is copied again.
func TestPushHealsMissingOverlayCopy(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, w, _ := packHome(t, m, 6)
	m.workRun(t, home, "push", "--no-html")
	mustWrite(t, filepath.Join(w, "run1", "f02.md"), "e\n")
	m.workRun(t, home, "push", "--no-html")
	ov := filepath.Join(workCopy(m, w), "run1", "f02.md")
	if err := os.Remove(ov); err != nil {
		t.Fatal(err)
	}
	m.workRun(t, home, "push", "--no-html")
	if got := mustRead(t, ov); got != "e\n" {
		t.Fatalf("overlay copy = %q", got)
	}
}

// A loose copy newer than the local file - whose time went back - is not removed when
// the unit is packed.
func TestPushPackKeepsNewerLooseCopy(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, w, pack := packHome(t, m, 6)
	lc := filepath.Join(workCopy(m, w), "run1", "f00.md")
	mustWrite(t, lc, "newer content in the archive\n")
	setTime(t, lc, time.Now().Add(-time.Hour))
	m.workRun(t, home, "push", "--no-html")
	if idx := packIndex(t, pack); idx["f00.md"] != 7 {
		t.Fatalf("pack: %v", idx)
	}
	m.workRun(t, home, "push", "--no-html")
	if got := mustRead(t, lc); got != "newer content in the archive\n" {
		t.Fatalf("loose copy = %q", got)
	}
}

// The shard counts the packs in the archive, not the records of them.
func TestPushPackedCountsPacks(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, w, pack := packHome(t, m, 6)
	m.workRun(t, home, "push", "--no-html")
	if err := os.RemoveAll(filepath.Join(w, "run1")); err != nil {
		t.Fatal(err)
	}
	m.workRun(t, home, "push", "--no-html")
	if wf := workShard(t, m); len(wf) != 1 || wf[0].Packed != 1 {
		t.Fatalf("shard: %+v", wf)
	}
	if err := os.Remove(pack); err != nil {
		t.Fatal(err)
	}
	m.workRun(t, home, "push", "--no-html")
	if wf := workShard(t, m); len(wf) != 1 || wf[0].Packed != 0 {
		t.Fatalf("shard: %+v", wf)
	}
}

// Held files - restored from another machine and unchanged - stay out of the pack.
func TestPushPackLeavesHeldFiles(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, w, pack := packHome(t, m, 7)
	held := filepath.Join(w, "run1", "f06.md")
	st, err := os.Stat(held)
	if err != nil {
		t.Fatal(err)
	}
	hp := filepath.Join(m.claudeDir, "session-sync-base", "work-held.json")
	h := work.LoadHeld(hp)
	h.Files[held] = work.HeldFile{Machine: "WIN", From: `E:\SQL\run1\f06.md`, Size: st.Size(), MTime: st.ModTime()}
	if err := h.Save(hp); err != nil {
		t.Fatal(err)
	}
	m.workRun(t, home, "push", "--no-html")
	if idx := packIndex(t, pack); len(idx) != 6 || idx["f06.md"] != 0 {
		t.Fatalf("pack: %v", idx)
	}
	if got := workFiles(m.archive); len(got) != 1 {
		t.Fatalf("archived %v", got)
	}
}

// growPack adds an entry declaring size bytes and holding none, so a merge with it
// passes the cap without the test writing 250 MB.
func growPack(t *testing.T, pack string, size uint64) {
	t.Helper()
	r, err := zip.OpenReader(pack)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range r.File {
		if err := zw.Copy(f); err != nil {
			t.Fatal(err)
		}
	}
	r.Close()
	if _, err := zw.CreateRaw(&zip.FileHeader{Name: "big.bin", Method: zip.Store, UncompressedSize64: size}); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, pack, buf.String())
}

// A compaction whose result restore would refuse is not made: the pack stays, the
// changes are copied loose, and push warns once, then again only when the unit changes.
func TestPushCompactionTooLarge(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, w, pack := packHome(t, m, 6)
	m.workRun(t, home, "push", "--no-html")
	growPack(t, pack, work.MaxFolderBytes)
	var names []string
	for i := 0; i < 51; i++ {
		names = append(names, fmt.Sprintf("n%02d.md", i))
		mustWrite(t, filepath.Join(w, "run1", names[i]), "")
	}
	settle(t, filepath.Join(w, "run1"), "", names...)
	before := backdate(t, pack)
	m.workRun(t, home, "push", "--no-html")
	samePack(t, pack, before)
	if got := workFiles(m.archive); len(got) != 52 {
		t.Fatalf("archived %d files: %v", len(got), got)
	}
	warn := "WARN: work folder " + w + ": subfolder run1 is too large to pack again"
	warned := func(want int) {
		t.Helper()
		if log := mustRead(t, filepath.Join(m.claudeDir, "session-sync.log")); strings.Count(log, warn) != want {
			t.Fatalf("want %d warning(s); log: %s", want, log)
		}
	}
	warned(1)
	m.workRun(t, home, "push", "--no-html")
	m.workRun(t, home, "push", "--no-html")
	warned(1)
	mustWrite(t, filepath.Join(w, "run1", "n51.md"), "")
	settle(t, filepath.Join(w, "run1"), "", "n51.md")
	m.workRun(t, home, "push", "--no-html")
	warned(2)
	samePack(t, pack, before)
}

// 50 settled changed files stay loose; the 51st folds them into the pack.
func TestPushCompactBoundary(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, w, pack := packHome(t, m, 6)
	m.workRun(t, home, "push", "--no-html")
	var names []string
	for i := 0; i < 50; i++ { // empty, so only the count can trigger it
		names = append(names, fmt.Sprintf("n%02d.md", i))
		mustWrite(t, filepath.Join(w, "run1", names[i]), "")
	}
	settle(t, filepath.Join(w, "run1"), "", names...)
	before := backdate(t, pack)
	m.workRun(t, home, "push", "--no-html")
	samePack(t, pack, before)
	if got := workFiles(m.archive); len(got) != 51 {
		t.Fatalf("archived %d files", len(got))
	}
	mustWrite(t, filepath.Join(w, "run1", "n50.md"), "")
	settle(t, filepath.Join(w, "run1"), "", "n50.md")
	m.workRun(t, home, "push", "--no-html")
	if got := workFiles(m.archive); len(got) != 1 {
		t.Fatalf("archived %v", got)
	}
	if idx := packIndex(t, pack); len(idx) != 57 {
		t.Fatalf("pack: %d entries", len(idx))
	}
}

// settle dates the named files of unit 8 days back, as a burst of changes is once it
// has settled, and their loose copies under loose ("" for none) alike.
func settle(t *testing.T, unit, loose string, names ...string) {
	t.Helper()
	old := time.Now().Add(-8 * 24 * time.Hour)
	for _, n := range names {
		setTime(t, filepath.Join(unit, n), old)
		if lc := filepath.Join(loose, n); loose != "" {
			if _, err := os.Stat(lc); err == nil {
				setTime(t, lc, old)
			}
		}
	}
}

// packPair is WIN with <homeA>/SQL/run1 packed - six files 8 days old - and f02.md
// edited since, so it sits loose beside the pack; and MAC, with its own home, on the
// same archive.
func packPair(t *testing.T) (a, b machine, homeA, homeB, w string) {
	t.Helper()
	arch := t.TempDir()
	a, b = newMachine(t, "WIN", arch), newMachine(t, "MAC", arch)
	homeA, w, _ = packHome(t, a, 6)
	a.workRun(t, homeA, "push", "--no-html")
	mustWrite(t, filepath.Join(w, "run1", "f02.md"), "edited\n") // under a quarter: no compaction
	a.workRun(t, homeA, "push", "--no-html")
	enc := "WIN/" + work.Encode(w) + "/"
	if got := sortedWorkFiles(arch); !reflect.DeepEqual(got, []string{enc + "run1.cspack.zip", enc + "run1/f02.md"}) {
		t.Fatalf("archived %v", got)
	}
	return a, b, homeA, t.TempDir(), w
}

// tmpRun runs as m with its temp folder at tmp, so a test sees what is left there.
func (m machine) tmpRun(t *testing.T, home, tmp string, args ...string) string {
	t.Helper()
	cmd := m.workCmd(home, args...)
	cmd.Env = append(cmd.Env, "TMPDIR="+tmp, "TMP="+tmp, "TEMP="+tmp)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return string(out)
}

// unpacked checks run1 as restored at dir: the six files, f02.md the edited one, each
// with the time it had on A.
func unpacked(t *testing.T, dir, w string) {
	t.Helper()
	for i := 0; i < 6; i++ {
		name := fmt.Sprintf("f%02d.md", i)
		want := fmt.Sprintf("file %d\n", i)
		if i == 2 {
			want = "edited\n"
		}
		if got := mustRead(t, filepath.Join(dir, name)); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
		st, err := os.Stat(filepath.Join(dir, name))
		src, serr := os.Stat(filepath.Join(w, "run1", name))
		if err != nil || serr != nil || !archive.SameMTime(st.ModTime(), src.ModTime()) {
			t.Errorf("%s: time %v, on A %v (%v %v)", name, st, src, err, serr)
		}
	}
}

// A packed unit plus its overlay unpacks to plain files: the overlay's edit wins, a
// leftover loose copy older than its pack entry loses to it (R33), and times come from
// the pack. A dry run unpacks nothing; the temp folder is cleared after.
func TestImportPackWithOverlay(t *testing.T) {
	a, b, _, homeB, w := packPair(t)
	stale := filepath.Join(workCopy(a, w), "run1", "f03.md")
	mustWrite(t, stale, "stale\n")
	setTime(t, stale, time.Now().Add(-30*24*time.Hour))
	tmp := t.TempDir()

	dry := b.tmpRun(t, homeB, tmp, "import", "--no-plugins", "--dry-run")
	if !strings.Contains(dry, "would unpack 5 file(s) from "+filepath.Join(w, "run1")) || !strings.Contains(dry, "(1 file(s) would be filed") {
		t.Fatalf("dry run output: %s", dry)
	}
	if _, err := os.Stat(filepath.Join(homeB, "SQL")); !os.IsNotExist(err) {
		t.Fatalf("dry run placed files: %v", err)
	}
	out := b.tmpRun(t, homeB, tmp, "import", "--no-plugins")
	placed := filepath.Join(homeB, "SQL", "run1")
	unpacked(t, placed, w)
	if got := filesUnderDir(t, placed); len(got) != 6 {
		t.Fatalf("restored %v", got)
	}
	if !strings.Contains(out, "(6 file(s) filed, 0 kept, 0 beside, 0 refused") {
		t.Fatalf("output: %s", out)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Fatalf("left in the temp folder: %v", left)
	}
	h := work.LoadHeld(filepath.Join(b.claudeDir, "session-sync-base", "work-held.json"))
	if hf, ok := h.Files[filepath.Join(placed, "f00.md")]; !ok || hf.Machine != "WIN" || hf.From != filepath.Join(w, "run1", "f00.md") {
		t.Fatalf("not held: %+v", h.Files)
	}
}

// filesUnderDir lists the files below dir, "/"-separated and sorted.
func filesUnderDir(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	slices.Sort(out)
	return out
}

// Unpacked files are held: B's push sends none of them back, loose or packed.
func TestImportPackHeldNotPushedBack(t *testing.T) {
	_, b, _, homeB, _ := packPair(t)
	b.workRun(t, homeB, "import", "--no-plugins")
	sqlB := filepath.Join(homeB, "SQL")
	mustWrite(t, filepath.Join(sqlB, "run1", "b.md"), "B's own\n")
	workSession(t, b, gitRepo(t, filepath.Join(t.TempDir(), "dev", "web")), nil, `cd "`+sqlB+`" && ls`)
	b.workRun(t, homeB, "push", "--no-html")
	var mine []string
	for _, f := range sortedWorkFiles(b.archive) {
		if strings.HasPrefix(f, "MAC/") {
			mine = append(mine, f)
		}
	}
	if want := []string{"MAC/" + work.Encode(sqlB) + "/run1/b.md"}; !reflect.DeepEqual(mine, want) {
		t.Fatalf("B archived %v, want %v", mine, want)
	}
}

// pull unpacks at the recorded paths.
func TestPullRestoresPack(t *testing.T) {
	_, b, homeA, _, w := packPair(t)
	if err := os.RemoveAll(w); err != nil { // one file system stands in for two
		t.Fatal(err)
	}
	b.workRun(t, homeA, "pull", "--no-plugins")
	if got := filesUnderDir(t, w); !reflect.DeepEqual(got, []string{"run1/f00.md", "run1/f01.md", "run1/f02.md", "run1/f03.md", "run1/f04.md", "run1/f05.md"}) {
		t.Fatalf("restored %v", got)
	}
	for i := 0; i < 6; i++ {
		want := fmt.Sprintf("file %d\n", i)
		if i == 2 {
			want = "edited\n"
		}
		if got := mustRead(t, filepath.Join(w, "run1", fmt.Sprintf("f%02d.md", i))); got != want {
			t.Errorf("f%02d.md = %q", i, got)
		}
	}
}

// A truncated pack - a partial sync - is refused with a note; the unit's overlay and
// the folder's other units are restored all the same.
func TestImportCorruptPack(t *testing.T) {
	arch := t.TempDir()
	a, b := newMachine(t, "WIN", arch), newMachine(t, "MAC", arch)
	homeA, w, pack := packHome(t, a, 6)
	for i := 0; i < 5; i++ {
		mustWrite(t, filepath.Join(w, "run2", fmt.Sprintf("g%d.md", i)), "g\n")
	}
	ageFiles(t, w)
	a.workRun(t, homeA, "push", "--no-html")
	mustWrite(t, filepath.Join(w, "run1", "f02.md"), "edited\n")
	a.workRun(t, homeA, "push", "--no-html")
	if _, err := os.Stat(filepath.Join(workCopy(a, w), "run2.cspack.zip")); err != nil {
		t.Fatalf("run2 not packed: %v", err)
	}
	st, err := os.Stat(pack)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(pack, st.Size()/2); err != nil {
		t.Fatal(err)
	}
	homeB := t.TempDir()
	out := b.workRun(t, homeB, "import", "--no-plugins")
	want := []string{"run1/f02.md", "run2/g0.md", "run2/g1.md", "run2/g2.md", "run2/g3.md", "run2/g4.md"}
	if got := filesUnderDir(t, filepath.Join(homeB, "SQL")); !reflect.DeepEqual(got, want) {
		t.Fatalf("restored %v\n%s", got, out)
	}
	if !strings.Contains(out, pack+": refused: ") || !strings.Contains(out, "1 refused") {
		t.Fatalf("output: %s", out)
	}
}

// Two machines packing the same unit name, placed onto one folder here: each pack is
// under its own machine's folder, and restore keeps to add-only and the copy beside.
func TestImportPacksFromTwoMachines(t *testing.T) {
	arch := t.TempDir()
	lin, win, b := newMachine(t, "LIN", arch), newMachine(t, "WIN", arch), newMachine(t, "MAC", arch)
	homeL, _, packL := packHome(t, lin, 6)
	homeW, ww, packW := packHome(t, win, 6)
	mustWrite(t, filepath.Join(ww, "run1", "f00.md"), "WIN's file 0, longer\n")
	ageFiles(t, ww)
	lin.workRun(t, homeL, "push", "--no-html")
	win.workRun(t, homeW, "push", "--no-html")
	if filepath.Dir(packL) == filepath.Dir(packW) {
		t.Fatalf("one folder for both packs: %s", packL)
	}
	for _, p := range []string{packL, packW} {
		if idx := packIndex(t, p); len(idx) != 6 {
			t.Fatalf("%s: %v", p, idx)
		}
	}
	homeB := t.TempDir()
	b.workRun(t, homeB, "import", "--no-plugins", "--update")
	placed := filepath.Join(homeB, "SQL", "run1")
	want := []string{"f00.WIN.md", "f00.md", "f01.md", "f02.md", "f03.md", "f04.md", "f05.md"}
	if got := filesUnderDir(t, placed); !reflect.DeepEqual(got, want) {
		t.Fatalf("restored %v", got)
	}
	if got := mustRead(t, filepath.Join(placed, "f00.md")); got != "file 0\n" {
		t.Fatalf("f00.md = %q", got)
	}
	if got := mustRead(t, filepath.Join(placed, "f00.WIN.md")); got != "WIN's file 0, longer\n" {
		t.Fatalf("f00.WIN.md = %q", got)
	}
}

// hostileSetup is MAC with "/evil" mapped to <homeB>/evil, on an archive where WIN
// pushed a session and EVIL's shard lists the folder /evil/proj. It returns MAC, its
// home and where EVIL's pack of the unit "run" goes.
func hostileSetup(t *testing.T) (b machine, homeB, pack string) {
	t.Helper()
	arch := t.TempDir()
	a := newMachine(t, "WIN", arch)
	b = newMachine(t, "MAC", arch)
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	mustWrite(t, a.session(repo), sessionLines(repo, "u1"))
	a.workRun(t, t.TempDir(), "push", "--no-html")
	homeB = t.TempDir()
	mapped := filepath.Join(homeB, "evil")
	mustMkdir(t, mapped)
	configAs(t, b.claudeDir, "MAC", "map-folder", "/evil", mapped)
	raw, _ := json.Marshal(map[string]any{"schemaVersion": 1, "machine": "EVIL", "home": "/", "projects": map[string]any{},
		"workFolders": []any{map[string]any{"path": "/evil/proj"}}})
	mustWrite(t, filepath.Join(arch, "manifest", "EVIL.json"), string(raw))
	return b, homeB, filepath.Join(workCopy(machine{name: "EVIL", archive: arch}, "/evil/proj"), "run.cspack.zip")
}

// packAt writes a zip at path holding entries dated at, each name with its body and
// mode ("x": executable).
func packAt(t *testing.T, path string, at time.Time, entries ...[3]string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e[0], Method: zip.Deflate, Modified: at}
		h.SetMode(0o644)
		if e[2] == "x" {
			h.SetMode(0o755)
		}
		w, err := zw.CreateHeader(h)
		if err == nil {
			_, err = w.Write([]byte(e[1]))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, path, buf.String())
}

// A hostile pack writes nothing a loose file could not: of a .pth file, a module in
// node_modules, a git config, a name climbing out and a script, only the script lands,
// not executable. The rest are counted refused.
func TestImportHostilePack(t *testing.T) {
	b, homeB, pack := hostileSetup(t)
	packAt(t, pack, time.Now().Add(-8*24*time.Hour),
		[3]string{"evil.pth", "import os\n"},
		[3]string{"node_modules/x.js", "evil()\n"},
		[3]string{".git/config", "[core]\n\tfsmonitor = evil\n"},
		[3]string{"../../x", "escaped\n"},
		[3]string{"a.sh", "#!/bin/sh\n", "x"},
	)
	out := b.workRun(t, homeB, "import", "--no-plugins", "--update")
	if got := filesUnderDir(t, homeB); !reflect.DeepEqual(got, []string{"evil/proj/run/a.sh"}) {
		t.Fatalf("wrote %v\n%s", got, out)
	}
	if fi, err := os.Stat(filepath.Join(homeB, "evil", "proj", "run", "a.sh")); runtime.GOOS != "windows" && (err != nil || fi.Mode().Perm() != 0o644) {
		t.Fatalf("a.sh mode: %v %v", fi, err)
	}
	if !strings.Contains(out, "work folders from EVIL: ") || !strings.Contains(out, "(1 file(s) filed, 0 kept, 0 beside, 4 refused") ||
		!strings.Contains(out, `"../../x"`) || !strings.Contains(out, "evil.pth: refused: type") {
		t.Fatalf("output: %s", out)
	}
}

// A name only Windows refuses (8.3 short names alias another) is refused alone on a
// Windows host; the rest of the pack restores (Review Focus 1).
func TestImportPackWindowsName(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only Windows reads a ~ name as another")
	}
	b, homeB, pack := hostileSetup(t)
	packAt(t, pack, time.Now().Add(-8*24*time.Hour), [3]string{"ok.md", "ok\n"}, [3]string{"PROGRA~1.md", "x\n"})
	out := b.workRun(t, homeB, "import", "--no-plugins")
	if got := filesUnderDir(t, homeB); !reflect.DeepEqual(got, []string{"evil/proj/run/ok.md"}) {
		t.Fatalf("wrote %v\n%s", got, out)
	}
	if !strings.Contains(out, "1 refused") || !strings.Contains(out, "a name Windows reads as another") {
		t.Fatalf("output: %s", out)
	}
}

// Excluding a path the pack holds nothing under still drops its overlay entries from
// the record, and leaves the pack alone.
func TestExcludeOverlayOnlyPathPrunesRecord(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	configAs(t, m.claudeDir, "MAC", "set-destination", m.archive)
	home, w, pack := packHome(t, m, 6)
	m.workRun(t, home, "push", "--no-html")
	sub := filepath.Join(w, "run1", "sub")
	mustWrite(t, filepath.Join(sub, "new.md"), "n\n")
	m.workRun(t, home, "push", "--no-html")
	rec := filepath.Join(m.claudeDir, "session-sync-base", "work-packs.json")
	if !strings.Contains(mustRead(t, rec), "sub/new.md") {
		t.Fatalf("overlay not recorded: %s", mustRead(t, rec))
	}
	before := backdate(t, pack)
	configAs(t, m.claudeDir, "MAC", "exclude-folder", sub)
	if r := mustRead(t, rec); strings.Contains(r, "sub/new.md") {
		t.Fatalf("record still lists the excluded file: %s", r)
	}
	samePack(t, pack, before)
	if _, err := os.Stat(filepath.Join(workCopy(m, w), "run1", "sub")); !os.IsNotExist(err) {
		t.Fatalf("excluded overlay kept: %v", err)
	}
}

// A record whose pack changed size on disk is rebuilt from the pack's index with its
// overlay kept: an overlay copy of a file deleted here, dated before its pack entry,
// is the only copy of that edit and must stay.
func TestPushPackRecordStaleKeepsOverlay(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, w, pack := packHome(t, m, 6)
	m.workRun(t, home, "push", "--no-html")
	f := filepath.Join(w, "run1", "f02.md")
	mustWrite(t, f, "edited\n")
	setTime(t, f, time.Now().Add(-30*24*time.Hour))
	m.workRun(t, home, "push", "--no-html")
	ov := filepath.Join(workCopy(m, w), "run1", "f02.md")
	if got := mustRead(t, ov); got != "edited\n" {
		t.Fatalf("overlay copy = %q", got)
	}
	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}
	growPack(t, pack, 0)
	m.workRun(t, home, "push", "--no-html")
	if _, err := os.Stat(ov); err != nil {
		t.Fatalf("overlay copy deleted: %v", err)
	}
}

// A link planted where this machine's pack goes is never read through by an exclusion
// inside the unit: the file it points to never reaches the archive.
func TestExcludeInsideUnitIgnoresLinkedPack(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	configAs(t, m.claudeDir, "MAC", "set-destination", m.archive)
	home, w, pack := packHome(t, m, 6)
	m.workRun(t, home, "push", "--no-html")
	private := filepath.Join(t.TempDir(), "private.zip")
	packAt(t, private, time.Now(), [3]string{"sub/a.md", "a\n"}, [3]string{"secret.md", "private\n"})
	if err := os.Remove(pack); err != nil {
		t.Fatal(err)
	}
	symlink(t, private, pack)
	configAs(t, m.claudeDir, "MAC", "exclude-folder", filepath.Join(w, "run1", "sub"))
	if fi, err := os.Lstat(pack); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("link replaced by what it pointed to: %v %v", fi, err)
	}
}

// R33 at its edges: an overlay file as old as its pack entry, or older by up to 2 s,
// wins; one older by more loses; a newer one wins.
func TestImportPackOverlayTie(t *testing.T) {
	b, homeB, pack := hostileSetup(t)
	at := time.Now().Add(-8 * 24 * time.Hour).Truncate(time.Second)
	names := map[string]time.Duration{"eq.md": 0, "older1.md": -time.Second, "older3.md": -3 * time.Second, "newer3.md": 3 * time.Second}
	var entries [][3]string
	for name, d := range names {
		entries = append(entries, [3]string{name, "pack\n"})
		p := filepath.Join(filepath.Dir(pack), "run", name)
		mustWrite(t, p, "overlay\n")
		setTime(t, p, at.Add(d))
	}
	packAt(t, pack, at, entries...)
	out := b.workRun(t, homeB, "import", "--no-plugins")
	want := map[string]string{"eq.md": "overlay\n", "older1.md": "overlay\n", "older3.md": "pack\n", "newer3.md": "overlay\n"}
	for name, w := range want {
		if got := mustRead(t, filepath.Join(homeB, "evil", "proj", "run", name)); got != w {
			t.Errorf("%s = %q, want %q\n%s", name, got, w, out)
		}
	}
}

// Names from the archive never reach the terminal raw: a refusal naming a pack entry
// or a loose file with control characters prints neither.
func TestImportNotesDropControlChars(t *testing.T) {
	b, homeB, pack := hostileSetup(t)
	packAt(t, pack, time.Now().Add(-8*24*time.Hour), [3]string{"a\x1b]0;pwned\x07.pem", "k"}, [3]string{"ok.md", "ok\n"})
	if runtime.GOOS != "windows" { // no control characters in Windows file names
		mustWrite(t, filepath.Join(filepath.Dir(pack), "run", "b\x1b[2J.pem"), "k")
	}
	out := b.workRun(t, homeB, "import", "--no-plugins")
	if strings.ContainsAny(out, "\x1b\x07") || !strings.Contains(out, "]0;pwned.pem: refused: secret") {
		t.Fatalf("output: %q", out)
	}
}

// An entry the push filter refuses is refused by its name before anything is written
// (R39): one no disk can create - a name over 255 bytes here, control characters on
// Windows - reads "refused: secret", not the disk's error, and its content never lands.
func TestImportPackRefusesBeforeWriting(t *testing.T) {
	b, homeB, pack := hostileSetup(t)
	long := strings.Repeat("k", 300) + ".pem"
	packAt(t, pack, time.Now().Add(-8*24*time.Hour), [3]string{long, "k"}, [3]string{"ok.md", "ok\n"})
	out := b.workRun(t, homeB, "import", "--no-plugins")
	if !strings.Contains(out, long+": refused: secret") || !strings.Contains(out, "(1 file(s) filed, 0 kept, 0 beside, 1 refused") {
		t.Fatalf("output: %s", out)
	}
}

// A repeat import with nothing new unpacks nothing: with the temp folder gone it still
// runs clean. Entries already here - add-only without --update, restored from that
// machine and unchanged with it - and entries excluded here are settled unread.
func TestImportRepeatSkipsUnpacking(t *testing.T) {
	_, b, _, homeB, _ := packPair(t)
	b.workRun(t, homeB, "import", "--no-plugins")
	missing := filepath.Join(t.TempDir(), "no-such-dir")
	for _, args := range [][]string{{"import", "--no-plugins"}, {"import", "--no-plugins", "--update"}} {
		out := b.tmpRun(t, homeB, missing, args...)
		if strings.Contains(out, "  x ") || !strings.Contains(out, "(0 file(s) filed, 6 kept, 0 beside, 0 refused, 0 excluded here)") {
			t.Fatalf("%v: %s", args, out)
		}
	}
	configAs(t, b.claudeDir, "MAC", "set-destination", b.archive)
	configAs(t, b.claudeDir, "MAC", "exclude-folder", filepath.Join(homeB, "SQL", "run1"))
	if out := b.tmpRun(t, homeB, missing, "import", "--no-plugins"); strings.Contains(out, "  x ") ||
		!strings.Contains(out, "(0 file(s) filed, 0 kept, 0 beside, 0 refused, 6 excluded here)") {
		t.Fatalf("excluded: %s", out)
	}
}

// A temp folder that an import killed while unpacking left behind is cleared by the
// next one once it is an hour old; a newer one, maybe another run's, stays.
func TestImportClearsStaleTempDirs(t *testing.T) {
	_, b, _, homeB, _ := packPair(t)
	tmp := t.TempDir()
	stale, fresh := filepath.Join(tmp, "cs-pack-1"), filepath.Join(tmp, "cs-pack-2")
	mustWrite(t, filepath.Join(stale, "run1", "f00.md"), "x")
	mustWrite(t, filepath.Join(fresh, "f00.md"), "x")
	setTime(t, stale, time.Now().Add(-2*time.Hour))
	b.tmpRun(t, homeB, tmp, "import", "--no-plugins")
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale temp folder kept: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh temp folder removed: %v", err)
	}
}

// A burst of changes stays loose while it is fresh: three pushes in a row, each with 51
// new files, rebuild nothing. Once every change is a week old, one push compacts them.
func TestPushCompactionWaitsToSettle(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, w, pack := packHome(t, m, 6)
	m.workRun(t, home, "push", "--no-html")
	before := backdate(t, pack)
	var names []string
	for run := 0; run < 3; run++ {
		for i := 0; i < 51; i++ {
			names = append(names, fmt.Sprintf("r%d-%02d.md", run, i))
			mustWrite(t, filepath.Join(w, "run1", names[len(names)-1]), "x\n")
		}
		m.workRun(t, home, "push", "--no-html")
		samePack(t, pack, before)
	}
	if got := workFiles(m.archive); len(got) != 154 {
		t.Fatalf("archived %d files", len(got))
	}
	settle(t, filepath.Join(w, "run1"), filepath.Join(workCopy(m, w), "run1"), names...)
	m.workRun(t, home, "push", "--no-html")
	m.workRun(t, home, "push", "--no-html")
	if got := workFiles(m.archive); len(got) != 1 {
		t.Fatalf("archived %v", got)
	}
	if idx := packIndex(t, pack); len(idx) != 159 {
		t.Fatalf("pack: %d entries", len(idx))
	}
	if log := mustRead(t, filepath.Join(m.claudeDir, "session-sync.log")); strings.Count(log, "WORK: compacted") != 1 {
		t.Fatalf("log: %s", log)
	}
}

// Once an exclusion inside a packed unit is out of the pack, later pushes trust the
// record and leave the pack unread: one zeroed since, at its size, draws no warning.
func TestExcludeInsideUnitReadsPackOnce(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	configAs(t, m.claudeDir, "MAC", "set-destination", m.archive)
	home, w, pack := packHome(t, m, 7)
	sub := filepath.Join(w, "run1", "sub")
	mustWrite(t, filepath.Join(sub, "s.md"), "private\n")
	ageFiles(t, w)
	m.workRun(t, home, "push", "--no-html")
	configAs(t, m.claudeDir, "MAC", "exclude-folder", sub)
	st, err := os.Stat(pack)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, pack, strings.Repeat("\x00", int(st.Size())))
	m.workRun(t, home, "push", "--no-html")
	m.workRun(t, home, "push", "--no-html")
	if log := mustRead(t, filepath.Join(m.claudeDir, "session-sync.log")); strings.Contains(log, "WARN: excluded folder") {
		t.Fatalf("pack read again: %s", log)
	}
}

// An exclusion prunes the pack under a name this machine went by before even when
// that name's shard is gone: the pack is found from the excluded path's own folders.
func TestExcludeFolderPrunesFormerNamePack(t *testing.T) {
	arch := t.TempDir()
	m := newMachine(t, "MAC", arch)
	configAs(t, m.claudeDir, "MAC", "set-destination", arch)
	configAs(t, m.claudeDir, "OLD", "set-machine", "MAC")
	dir := t.TempDir()
	pack := filepath.Join(workCopy(machine{name: "OLD", archive: arch}, dir), "run1.cspack.zip")
	packAt(t, pack, time.Now().Add(-8*24*time.Hour), [3]string{"keep.md", "k\n"}, [3]string{"sub/s.md", "private\n"})
	if _, err := os.Stat(filepath.Join(arch, "manifest", "OLD.json")); !os.IsNotExist(err) {
		t.Fatalf("OLD has a shard: %v", err)
	}
	configAs(t, m.claudeDir, "MAC", "exclude-folder", filepath.Join(dir, "run1", "sub"))
	if idx := packIndex(t, pack); !reflect.DeepEqual(idx, map[string]int64{"keep.md": 2}) {
		t.Fatalf("pack: %v", idx)
	}
}

var copiedRe = regexp.MustCompile(`(?m)^copied (\d+) file\(s\)`)

// The two-machine loop: A packs, B unpacks, B edits a packed file and pushes, then A
// and B each import --update and push, twice over. Each keeps its own copy with the
// other's beside it; after that no push copies anything and nothing new appears.
func TestTwoMachinePackLoop(t *testing.T) {
	a, b, homeA, homeB, w := packPair(t)
	sqlB := filepath.Join(homeB, "SQL")
	b.workRun(t, homeB, "import", "--no-plugins")
	workSession(t, b, gitRepo(t, filepath.Join(t.TempDir(), "dev", "web")), nil, `cd "`+sqlB+`" && ls`)
	mustWrite(t, filepath.Join(sqlB, "run1", "f03.md"), "edited on B\n")
	b.workRun(t, homeB, "push", "--no-html")

	var arch []string
	for round := 1; round <= 2; round++ {
		for _, s := range []struct {
			m    machine
			home string
		}{{a, homeA}, {b, homeB}} {
			out := s.m.workRun(t, s.home, "import", "--no-plugins", "--update")
			if strings.Contains(out, "  x ") || round == 2 && (!strings.Contains(out, "(0 file(s) filed, ") || !strings.Contains(out, ", 0 beside, ")) {
				t.Fatalf("round %d, %s import: %s", round, s.m.name, out)
			}
			out = s.m.workRun(t, s.home, "push", "--no-html")
			if c := copiedRe.FindStringSubmatch(out); c == nil || c[1] != "0" {
				t.Fatalf("round %d, %s push: %s", round, s.m.name, out)
			}
		}
		if round == 1 {
			arch = sortedWorkFiles(a.archive)
		} else if got := sortedWorkFiles(a.archive); !reflect.DeepEqual(got, arch) {
			t.Fatalf("archive changed: %v, was %v", got, arch)
		}
	}
	runFiles := func(beside string) []string {
		return []string{"f00.md", "f01.md", "f02.md", beside, "f03.md", "f04.md", "f05.md"}
	}
	if got := filesUnderDir(t, filepath.Join(w, "run1")); !reflect.DeepEqual(got, runFiles("f03.MAC.md")) {
		t.Fatalf("A has %v", got)
	}
	if got := filesUnderDir(t, filepath.Join(sqlB, "run1")); !reflect.DeepEqual(got, runFiles("f03.WIN.md")) {
		t.Fatalf("B has %v", got)
	}
	if a3, b3 := mustRead(t, filepath.Join(w, "run1", "f03.md")), mustRead(t, filepath.Join(sqlB, "run1", "f03.md")); a3 != "file 3\n" || b3 != "edited on B\n" {
		t.Fatalf("f03: A %q, B %q", a3, b3)
	}
}

// An edit on A after B unpacked reaches B under --update through the overlay, and again
// once A has compacted it into the pack: replaced each time, never written beside.
func TestImportUpdateThroughOverlayAndCompaction(t *testing.T) {
	a, b, homeA, homeB, w := packPair(t)
	placed := filepath.Join(homeB, "SQL", "run1")
	b.workRun(t, homeB, "import", "--no-plugins")
	mustWrite(t, filepath.Join(w, "run1", "f04.md"), "A edits f04\n")
	a.workRun(t, homeA, "push", "--no-html")
	out := b.workRun(t, homeB, "import", "--no-plugins", "--update")
	if got := mustRead(t, filepath.Join(placed, "f04.md")); got != "A edits f04\n" || len(filesUnderDir(t, placed)) != 6 {
		t.Fatalf("after the overlay: f04 %q, files %v\n%s", got, filesUnderDir(t, placed), out)
	}

	names := []string{"f02.md", "f04.md", "f05.md"}
	for i := 0; i < 51; i++ {
		names = append(names, fmt.Sprintf("n%02d.md", i))
		mustWrite(t, filepath.Join(w, "run1", names[len(names)-1]), "n\n")
	}
	mustWrite(t, filepath.Join(w, "run1", "f05.md"), "A edits f05\n")
	settle(t, filepath.Join(w, "run1"), filepath.Join(workCopy(a, w), "run1"), names...)
	a.workRun(t, homeA, "push", "--no-html")
	if got := sortedWorkFiles(a.archive); len(got) != 1 || !strings.HasSuffix(got[0], "run1.cspack.zip") {
		t.Fatalf("not compacted: %v", got)
	}
	out = b.workRun(t, homeB, "import", "--no-plugins", "--update")
	f04, f05 := mustRead(t, filepath.Join(placed, "f04.md")), mustRead(t, filepath.Join(placed, "f05.md"))
	if got := filesUnderDir(t, placed); f04 != "A edits f04\n" || f05 != "A edits f05\n" || len(got) != 57 || strings.Contains(out, " beside it as ") {
		t.Fatalf("after compaction: f04 %q, f05 %q, files %v\n%s", f04, f05, got, out)
	}
}

// Another computer's Desktop is this home's Desktop. A real import makes that folder
// when it places a work file there, then files the Desktop conversations into it; the
// dry run, which places nothing, said "no folder named Desktop" for the same import.
func TestDryRunPlacesAHomeFolderAsTheRealRunDoes(t *testing.T) {
	p := workPairAt(t, t.TempDir()) // A's conversation wrote Desktop/todo.md
	desk := filepath.Join(p.homeA, "Desktop")
	const id = "77777777-7777-7777-7777-777777777777"
	mustWrite(t, filepath.Join(p.a.claudeDir, "projects", claude.Slug(desk), id+".jsonl"),
		line(`{"type":"user","cwd":`+quoted(desk)+`,"sessionId":"`+id+`","message":{"role":"user","content":"tidy the desktop"}}`))
	p.a.workRun(t, p.homeA, "push", "--no-html")
	if err := os.RemoveAll(desk); err != nil { // A's own disk: not on this computer
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	placement := func(out string) string {
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, " "+claude.Slug(desk)+":") || strings.Contains(l, " "+claude.Slug(desk)+" ->") {
				return l
			}
		}
		return ""
	}

	dry := placement(p.b.workRun(t, p.homeB, "import", "--no-plugins", "--search-root", elsewhere, "--dry-run"))
	if _, err := os.Stat(filepath.Join(p.homeB, "Desktop")); !os.IsNotExist(err) {
		t.Fatalf("the dry run made Desktop, or the fixture has one: %v", err)
	}
	real := placement(p.b.workRun(t, p.homeB, "import", "--no-plugins", "--search-root", elsewhere))
	if want := "+ " + claude.Slug(desk) + " -> " + filepath.Join(p.homeB, "Desktop") + " (matched by home folder): 1 filed, 0 already present"; real != want {
		t.Fatalf("real run: %q, want %q", real, want)
	}
	if dry != real {
		t.Errorf("dry run: %q\nreal run: %q", dry, real)
	}

	// With Desktop here already, the two agree as they always did.
	q := workPairAt(t, t.TempDir())
	mustWrite(t, filepath.Join(q.a.claudeDir, "projects", claude.Slug(filepath.Join(q.homeA, "Desktop")), id+".jsonl"),
		line(`{"type":"user","cwd":`+quoted(filepath.Join(q.homeA, "Desktop"))+`,"sessionId":"`+id+`","message":{"role":"user","content":"tidy"}}`))
	q.a.workRun(t, q.homeA, "push", "--no-html")
	desk = filepath.Join(q.homeA, "Desktop")
	if err := os.RemoveAll(desk); err != nil {
		t.Fatal(err)
	}
	mustMkdir(t, filepath.Join(q.homeB, "Desktop"))
	dry = placement(q.b.workRun(t, q.homeB, "import", "--no-plugins", "--search-root", elsewhere, "--dry-run"))
	real = placement(q.b.workRun(t, q.homeB, "import", "--no-plugins", "--search-root", elsewhere))
	if dry != real || !strings.Contains(real, "(matched by home folder)") {
		t.Errorf("with Desktop here, dry run: %q\nreal run: %q", dry, real)
	}
}
