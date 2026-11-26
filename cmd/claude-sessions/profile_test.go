package main_test

// The profile - global rules, skills, plugin list - and the ~/.claude paths inside
// transcripts: what another machine needs to resume a session with the same context.

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
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

	// The repointed path is a real file: the tool output came along too. On a Unix
	// machine that takes the rest of the Windows path converted (R36).
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

// runHome runs the binary with HOME at dev, and the system temp folder not counted as
// one, which work-folder placement would otherwise refuse.
func (f importFixture) runHome(t *testing.T, args ...string) (string, error) {
	t.Helper()
	full := append([]string{args[0], "--claude-dir", f.claudeDir, "--archive", f.archive}, args[1:]...)
	cmd := exec.Command(binary, full...)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=", "HOME="+f.dev, "USERPROFILE="+f.dev,
		"CLAUDE_SESSIONS_TEMP_DIRS="+filepath.Join(f.dev, "no-temp")+string(os.PathListSeparator))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// profileOnlyFixture is findable: a normal import with HOME at dev files the transcript.
func profileOnlyFixture(t *testing.T) importFixture {
	t.Helper()
	f := profileImportFixture(t)
	mustWrite(t, filepath.Join(f.archive, "profile", "CLAUDE.md"), "archived rules\n")
	mustWrite(t, filepath.Join(f.claudeDir, "CLAUDE.md"), "local rules\n")
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(f.claudeDir, "CLAUDE.md"), old, old); err != nil {
		t.Fatal(err)
	}
	// A work folder archived by another computer, for a normal import to place.
	m := newMachine(t, "WORK-BOX", f.archive)
	home, _, _ := poSession(t, m)
	m.workRun(t, home, "push", "--no-html")
	return f
}

// placed lists where the archived work folder's r.md landed under the home folder.
func placed(f importFixture) (found []string) {
	filepath.WalkDir(f.dev, func(p string, d fs.DirEntry, _ error) error {
		if d != nil && d.Name() == "r.md" {
			found = append(found, p)
		}
		return nil
	})
	return found
}

func transcripts(f importFixture) []string {
	m, _ := filepath.Glob(filepath.Join(f.claudeDir, "projects", "*", "*.jsonl"))
	return m
}

func TestProfileOnlyTakesNewerRulesOnly(t *testing.T) {
	// control: the same fixture, imported normally, files the transcript.
	c := profileOnlyFixture(t)
	if out, err := c.runHome(t, "import", "--no-plugins"); err != nil || len(transcripts(c)) == 0 || len(placed(c)) == 0 {
		t.Fatalf("control import filed no transcript or placed no work folder (err %v):\n%s", err, out)
	}

	f := profileOnlyFixture(t)
	out, err := f.runHome(t, "import", "--profile-only")
	if err != nil {
		t.Fatalf("import --profile-only: %v\n%s", err, out)
	}
	if got := mustRead(t, filepath.Join(f.claudeDir, "CLAUDE.md")); got != "archived rules\n" {
		t.Errorf("newer archived CLAUDE.md not taken: %q\n%s", got, out)
	}
	if kept, _ := filepath.Glob(filepath.Join(f.archive, "profile", "replaced", "*", "*", "CLAUDE.md")); len(kept) == 0 {
		t.Errorf("local CLAUDE.md not kept in profile/replaced/:\n%s", out)
	}
	if m := transcripts(f); len(m) != 0 {
		t.Errorf("a transcript was filed: %v", m)
	}
	if got := placed(f); len(got) != 0 {
		t.Errorf("a work folder was placed: %v", got)
	}
}

func TestProfileOnlyInstallsNoPlugins(t *testing.T) {
	f := profileOnlyFixture(t)
	mustWrite(t, filepath.Join(f.archive, "profile", "plugins", "OLD-BOX.json"),
		`{"enabledPlugins":{"ponytail@ponytail":true},"extraKnownMarketplaces":{"ponytail":{"source":{"source":"github","repo":"DietrichGebert/ponytail"}}}}`)
	out, err := f.runHome(t, "import", "--profile-only", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "ponytail") || !strings.Contains(out, `"plugins": []`) {
		t.Errorf("plugins were offered or installed:\n%s", out)
	}
}

func TestProfileOnlyDryRunChangesNothing(t *testing.T) {
	f := profileOnlyFixture(t)
	snap := func() string {
		var b strings.Builder
		filepath.Walk(f.claudeDir, func(p string, i os.FileInfo, _ error) error { b.WriteString(p + "\n"); return nil })
		filepath.Walk(f.archive, func(p string, i os.FileInfo, _ error) error { b.WriteString(p + "\n"); return nil })
		return b.String()
	}
	before := snap()
	out, err := f.runHome(t, "import", "--profile-only", "--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := mustRead(t, filepath.Join(f.claudeDir, "CLAUDE.md")); got != "local rules\n" {
		t.Errorf("dry run replaced CLAUDE.md: %q", got)
	}
	if after := snap(); after != before {
		t.Errorf("dry run wrote files (profile/replaced, held list...):\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if !strings.Contains(out, "would") {
		t.Errorf("dry run output does not say what would happen:\n%s", out)
	}
}

func TestProfileOnlyNothingNewer(t *testing.T) {
	f := profileOnlyFixture(t)
	fresh := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(f.claudeDir, "CLAUDE.md"), fresh, fresh); err != nil {
		t.Fatal(err)
	}
	out, err := f.runHome(t, "import", "--profile-only")
	if err != nil || !strings.Contains(out, "No rules or skills here are older than the archive's.") {
		t.Errorf("want the nothing-newer line, got err=%v\n%s", err, out)
	}
}

func TestProfileOnlyUnreachableArchiveAndHeldLock(t *testing.T) {
	f := profileOnlyFixture(t)
	missing := filepath.Join(f.archive, "gone")
	out, err := exec.Command(binary, "import", "--profile-only", "--claude-dir", f.claudeDir, "--archive", missing).CombinedOutput()
	if err == nil || !strings.Contains(string(out), missing+" is not reachable - is the sync client mounted?") {
		t.Errorf("unreachable archive: err=%v\n%s", err, out)
	}
	if got := mustRead(t, filepath.Join(f.claudeDir, "CLAUDE.md")); got != "local rules\n" {
		t.Errorf("CLAUDE.md changed: %q", got)
	}

	mustWrite(t, filepath.Join(f.claudeDir, "session-sync.lock"), fmt.Sprintf("%d token 2026-09-01T00:00:00Z\n", os.Getpid()))
	o, err := f.runHome(t, "import", "--profile-only")
	if err == nil || !strings.Contains(o, "lock") {
		t.Errorf("held lock not refused: err=%v\n%s", err, o)
	}
	if got := mustRead(t, filepath.Join(f.claudeDir, "CLAUDE.md")); got != "local rules\n" {
		t.Errorf("CLAUDE.md changed under a held lock: %q", got)
	}
}

func TestProfileOnlyRefusesCombinations(t *testing.T) {
	f := profileImportFixture(t)
	for _, fl := range [][]string{{"--update"}, {"--repoint"}, {"--no-work"}, {"--search-root", f.dev}, {"--depth", "3"}, {"--include-bare-name"}} {
		out, err := f.run(t, append([]string{"import", "--profile-only"}, fl...)...)
		want := "--profile-only brings in rules and skills only; it cannot be combined with " + fl[0]
		if err == nil || !strings.Contains(out, want) {
			t.Errorf("%v: want refusal %q, got err=%v\n%s", fl, want, err, out)
		}
	}
}
