package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
