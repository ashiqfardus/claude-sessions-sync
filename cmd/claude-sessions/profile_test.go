package main_test

// The profile - global rules, skills, plugin list - and the ~/.claude paths inside
// transcripts: what another machine needs to resume a session with the same context.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPushArchivesProfileButNotAccountSkillsTrashOrSecrets(t *testing.T) {
	f := newFixture(t)
	c := f.claudeDir
	mustWrite(t, filepath.Join(c, "CLAUDE.md"), "# rules\n@RTK.md\n")
	mustWrite(t, filepath.Join(c, "RTK.md"), "rtk\n")
	mustWrite(t, filepath.Join(c, "skills", "mine", "SKILL.md"), "---\nname: mine\n---\n")
	mustWrite(t, filepath.Join(c, "skills", "mine", "scripts", "run.sh"), "echo hi\n")
	mustWrite(t, filepath.Join(c, "skills", "mine", ".env"), "TOKEN=secret\n")
	mustWrite(t, filepath.Join(c, "skills", "mine", "node_modules", "dep", "index.js"), "x\n")
	mustWrite(t, filepath.Join(c, "skills", "synced", "acct", "docs", "SKILL.md"), "account skill\n")
	mustWrite(t, filepath.Join(c, "skills", ".trash", "old", "SKILL.md"), "deleted\n")
	mustWrite(t, filepath.Join(c, "agents", "reviewer.md"), "agent\n")
	mustWrite(t, filepath.Join(c, "settings.json"), `{
  "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "local-only-tool"}]}]},
  "enabledPlugins": {"ponytail@ponytail": true, "off@ponytail": false},
  "extraKnownMarketplaces": {"ponytail": {"source": {"source": "github", "repo": "DietrichGebert/ponytail"}}}
}`)
	outside := filepath.Join(t.TempDir(), "elsewhere")
	mustWrite(t, filepath.Join(outside, "SKILL.md"), "not mine\n")
	linked := os.Symlink(outside, filepath.Join(c, "skills", "linked")) == nil &&
		os.Symlink(filepath.Join(outside, "SKILL.md"), filepath.Join(c, "LINKED.md")) == nil

	if out, err := f.push(t, "--no-html"); err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}

	p := filepath.Join(f.archive, "profile")
	for _, want := range []string{"CLAUDE.md", "RTK.md", "skills/mine/SKILL.md", "skills/mine/scripts/run.sh", "agents/reviewer.md"} {
		if _, err := os.Stat(filepath.Join(p, filepath.FromSlash(want))); err != nil {
			t.Errorf("%s was not archived: %v", want, err)
		}
	}
	for _, never := range []string{"skills/mine/.env", "skills/mine/node_modules", "skills/synced", "skills/.trash", "skills/linked", "LINKED.md", "settings.json"} {
		if _, err := os.Lstat(filepath.Join(p, filepath.FromSlash(never))); err == nil {
			t.Errorf("%s must not be archived", never)
		}
	}
	if !linked {
		t.Log("symlinks unavailable here; the linked-skill case was not exercised")
	}

	lists, _ := filepath.Glob(filepath.Join(p, "plugins", "*.json"))
	if len(lists) != 1 {
		t.Fatalf("want one plugin list, got %v", lists)
	}
	body := mustRead(t, lists[0])
	if !strings.Contains(body, "ponytail@ponytail") || !strings.Contains(body, "DietrichGebert/ponytail") {
		t.Errorf("plugin list lacks the enabled plugin or its source:\n%s", body)
	}
	if strings.Contains(body, "local-only-tool") {
		t.Errorf("hooks leaked into the plugin list:\n%s", body)
	}
}

// profileImportFixture is a second machine about to import: one archived project to
// import, and a profile left by OLD-BOX.
func profileImportFixture(t *testing.T) importFixture {
	t.Helper()
	f := newImportFixture(t)
	f.makeRepo(t, "api", "git@github.com:example/api.git")
	f.archiveProject(t, "D--work-api", `D:\work\api`, "11111111-1111-1111-1111-111111111111", "hello")
	f.writeManifest(t, map[string]any{
		"D--work-api": map[string]any{"path": `D:\work\api`, "leaf": "api", "remote": "git@github.com:example/api.git"},
	})
	return f
}

func TestImportRestoresProfileAndKeepsLocalRulesUnlessUpdateProfile(t *testing.T) {
	f := profileImportFixture(t)
	p := filepath.Join(f.archive, "profile")
	mustWrite(t, filepath.Join(p, "CLAUDE.md"), "archived rules\n")
	mustWrite(t, filepath.Join(p, "REVIEW.md"), "archived review, dated in the future\n")
	mustWrite(t, filepath.Join(p, "skills", "graphify", "SKILL.md"), "graphify\n")
	mustWrite(t, filepath.Join(f.claudeDir, "CLAUDE.md"), "local rules\n")
	mustWrite(t, filepath.Join(f.claudeDir, "REVIEW.md"), "local review\n")
	old := time.Now().Add(-48 * time.Hour)
	future := time.Now().Add(48 * time.Hour)
	for path, at := range map[string]time.Time{
		filepath.Join(f.claudeDir, "CLAUDE.md"): old,
		filepath.Join(f.claudeDir, "REVIEW.md"): old,
		filepath.Join(p, "REVIEW.md"):           future,
	} {
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}

	out, err := f.run(t, "import", "--search-root", f.dev, "--no-plugins")
	if err != nil {
		t.Fatalf("import failed: %v\n%s", err, out)
	}
	if got := mustRead(t, filepath.Join(f.claudeDir, "skills", "graphify", "SKILL.md")); got != "graphify\n" {
		t.Errorf("missing skill not restored: %q\n%s", got, out)
	}
	if got := mustRead(t, filepath.Join(f.claudeDir, "CLAUDE.md")); got != "local rules\n" {
		t.Errorf("local CLAUDE.md replaced without --update: %q", got)
	}

	out, err = f.run(t, "import", "--search-root", f.dev, "--no-plugins", "--update-profile")
	if err != nil {
		t.Fatalf("import --update-profile failed: %v\n%s", err, out)
	}
	if got := mustRead(t, filepath.Join(f.claudeDir, "CLAUDE.md")); got != "archived rules\n" {
		t.Errorf("--update-profile did not take the newer archived CLAUDE.md: %q\n%s", got, out)
	}
	if got := mustRead(t, filepath.Join(f.claudeDir, "REVIEW.md")); got != "local review\n" {
		t.Errorf("a future-dated archive file replaced a local one: %q", got)
	}
}

func TestImportRepointsOtherMachinesClaudeDir(t *testing.T) {
	f := newImportFixture(t)
	local := f.makeRepo(t, "api", "git@github.com:example/api.git")
	id := "22222222-2222-2222-2222-222222222222"
	saved := `C:\Users\other\.claude\projects\D--work-api\` + id + `\tool-results\big.txt`
	esc, _ := json.Marshal(saved)
	mustWrite(t, filepath.Join(f.archive, "projects", "D--work-api", id+".jsonl"),
		line(`{"type":"user","cwd":"D:\\work\\api","sessionId":"`+id+`","message":{"role":"user","content":"go"}}`)+
			line(`{"type":"user","toolUseResult":{"persistedOutputPath":`+string(esc)+`},"message":{"role":"user","content":"see C:\\Users\\other\\.claude\\CLAUDE.md"}}`))
	mustWrite(t, filepath.Join(f.archive, "projects", "D--work-api", id, "tool-results", "big.txt"), "large output\n")
	shard, _ := json.Marshal(map[string]any{
		"schemaVersion": 1, "machine": "OLD-BOX", "claudeRoot": `C:\Users\other\.claude`,
		"projects": map[string]any{"D--work-api": map[string]any{"path": `D:\work\api`, "leaf": "api", "remote": "git@github.com:example/api.git"}},
	})
	mustWrite(t, filepath.Join(f.archive, "manifest", "OLD-BOX.json"), string(shard))

	out, err := f.run(t, "import", "--search-root", f.dev, "--no-plugins")
	if err != nil {
		t.Fatalf("import failed: %v\n%s", err, out)
	}
	body := mustRead(t, filepath.Join(f.claudeDir, "projects", slugOf(local), id+".jsonl"))
	if strings.Contains(strings.ToLower(body), `users\\other`) {
		t.Fatalf("the other machine's ~/.claude survived the import:\n%s", body)
	}
	rootEsc, _ := json.Marshal(f.claudeDir)
	if !strings.Contains(body, strings.Trim(string(rootEsc), `"`)) {
		t.Fatalf("transcript does not point at this machine's ~/.claude:\n%s", body)
	}

	// On Windows the repointed path is a real file: the tool output came along too.
	if runtime.GOOS == "windows" {
		var rec struct {
			ToolUseResult struct{ PersistedOutputPath string } `json:"toolUseResult"`
		}
		if err := json.Unmarshal([]byte(strings.Split(body, "\n")[1]), &rec); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(rec.ToolUseResult.PersistedOutputPath); err != nil {
			t.Errorf("saved tool output not readable at the repointed path: %v", err)
		}
	}
}

func TestImportPluginsFallBackToPrintedCommandsAndHonourNoPlugins(t *testing.T) {
	f := profileImportFixture(t)
	mustWrite(t, filepath.Join(f.archive, "profile", "plugins", "OLD-BOX.json"), `{
  "enabledPlugins": {"ponytail@ponytail": true, "here@ponytail": true, "evil@bad": true, "-rf@ponytail": true},
  "extraKnownMarketplaces": {
    "ponytail": {"source": {"source": "github", "repo": "DietrichGebert/ponytail"}},
    "bad": {"source": {"source": "git", "url": "ext::sh -c touch pwned"}}
  }
}`)
	mustWrite(t, filepath.Join(f.claudeDir, "settings.json"), `{"enabledPlugins": {"here@ponytail": true}}`)

	// No `claude` on PATH, in both runs: nothing can install for real even if a
	// regression ignores --no-plugins, and the command is printed instead.
	run := func(extra ...string) (string, error) {
		full := append([]string{"import", "--claude-dir", f.claudeDir, "--archive", f.archive, "--search-root", f.dev}, extra...)
		cmd := exec.Command(binary, full...)
		cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=", "PATH=", "Path=")
		b, err := cmd.CombinedOutput()
		return string(b), err
	}
	out, err := run()
	if err != nil {
		t.Fatalf("import failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "ponytail@ponytail: claude is not on PATH") ||
		!strings.Contains(out, "claude plugin install ponytail --marketplace DietrichGebert/ponytail -y") {
		t.Errorf("missing plugin not reported with its command:\n%s", out)
	}
	for _, never := range []string{"here@ponytail", "evil@bad", "ext::", "-rf"} {
		if strings.Contains(out, never) {
			t.Errorf("%q should not be offered:\n%s", never, out)
		}
	}

	out, err = run("--no-plugins")
	if err != nil {
		t.Fatalf("import --no-plugins failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "plugin ") {
		t.Errorf("--no-plugins still handled plugins:\n%s", out)
	}
}
