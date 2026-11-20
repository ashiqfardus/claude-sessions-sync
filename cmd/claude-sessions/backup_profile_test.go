package main_test

// A backup zip mirrors ~/.claude: rules and skills sit at its top, not in profile/.
// Importing it brings them back too - and only from a backup, so an archive's own
// INDEX.md and README.md are never taken for rules.

import (
	"archive/zip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func unzipTo(t *testing.T, zipPath, dir string) {
	t.Helper()
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	for _, zf := range zr.File {
		rc, err := zf.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		mustWrite(t, filepath.Join(dir, filepath.FromSlash(zf.Name)), string(data))
	}
}

func TestImportFromABackupRestoresRulesSkillsAndPlugins(t *testing.T) {
	src := newFixture(t)
	c := src.claudeDir
	mustWrite(t, filepath.Join(c, "CLAUDE.md"), "# rules\n@RTK.md\n")
	mustWrite(t, filepath.Join(c, "RTK.md"), "rtk\n")
	mustWrite(t, filepath.Join(c, "skills", "mine", "SKILL.md"), "---\nname: mine\n---\n")
	mustWrite(t, filepath.Join(c, "agents", "reviewer.md"), "agent\n")
	mustWrite(t, filepath.Join(c, "settings.json"), `{"cleanupPeriodDays":3650,
  "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "/old/machine/only-tool"}]}]},
  "enabledPlugins": {"ponytail@ponytail": true},
  "extraKnownMarketplaces": {"ponytail": {"source": {"source": "github", "repo": "DietrichGebert/ponytail"}}}}`)
	out := t.TempDir()
	if o, err := src.run(t, "backup", "--out", out); err != nil {
		t.Fatalf("backup failed: %v\n%s", err, o)
	}
	zips, _ := filepath.Glob(filepath.Join(out, "*.zip"))
	if len(zips) != 1 {
		t.Fatalf("want one zip, got %v", zips)
	}
	unzipped := t.TempDir()
	unzipTo(t, zips[0], unzipped)

	f := newImportFixture(t)
	f.makeRepo(t, "airos-frontend", "")
	importFrom := func(extra ...string) string {
		args := append([]string{"import", "--claude-dir", f.claudeDir, "--archive", unzipped, "--search-root", f.dev}, extra...)
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=")
		o, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("import %v failed: %v\n%s", extra, err, o)
		}
		return string(o)
	}

	// Plugins first, as a dry run: installing one needs a real `claude`.
	if o := importFrom("--dry-run"); !strings.Contains(o, "ponytail@ponytail") || !strings.Contains(o, "would install") {
		t.Errorf("the backup's enabled plugin was not offered:\n%s", o)
	}
	o := importFrom("--no-plugins")
	for _, rel := range []string{"CLAUDE.md", "RTK.md", "skills/mine/SKILL.md", "agents/reviewer.md"} {
		if _, err := os.Stat(filepath.Join(f.claudeDir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s was not restored from the backup: %v\n%s", rel, err, o)
		}
	}
	// settings.json stays behind on purpose: its hooks point at the old machine.
	if raw, err := os.ReadFile(filepath.Join(f.claudeDir, "settings.json")); err == nil && strings.Contains(string(raw), "only-tool") {
		t.Errorf("the backup's settings.json, with the old machine's hooks, was restored:\n%s", raw)
	}
}

func TestImportDoesNotTakeAnArchivesOwnFilesForRules(t *testing.T) {
	f := newImportFixture(t)
	f.makeRepo(t, "api", "")
	f.archiveProject(t, "D--work-api", `D:\work\api`, mapSession, "hello")
	f.writeManifest(t, map[string]any{"D--work-api": map[string]any{"path": `D:\work\api`, "leaf": "api"}})
	mustWrite(t, filepath.Join(f.archive, "INDEX.md"), "# every session\n")
	mustWrite(t, filepath.Join(f.archive, "README.md"), "# how to use this folder\n")

	if out, err := f.run(t, "import", "--search-root", f.dev, "--no-plugins"); err != nil {
		t.Fatalf("import failed: %v\n%s", err, out)
	}
	for _, name := range []string{"INDEX.md", "README.md"} {
		if _, err := os.Stat(filepath.Join(f.claudeDir, name)); err == nil {
			t.Errorf("the archive's own %s was installed as a rule file", name)
		}
	}
}
