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
	if !strings.Contains(out, "removed") || !strings.Contains(out, "trash or version history") {
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
