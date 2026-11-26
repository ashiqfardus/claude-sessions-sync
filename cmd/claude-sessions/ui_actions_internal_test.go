package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/golden"
	"github.com/ashiqfardus/claude-sessions-sync/internal/hostagent"
)

const (
	pWeb1  = "b1000000-0000-4000-8000-000000000001" // web-app here; a copy also in a folder no computer lists
	pWeb2  = "b1000000-0000-4000-8000-000000000002" // web-app here
	pNotes = "b1000000-0000-4000-8000-000000000003" // notes here
	pAPI   = "b1000000-0000-4000-8000-000000000004" // api on LAP-2
	pOld   = "b1000000-0000-4000-8000-000000000005" // in a folder of transcripts, for restore
)

const lap2Bucket, oldBucket, oldProject = "e--dev-api", "-old-web-app", "/old/you/proj"

// parityEnv is one twin: everything it holds is under base.
type parityEnv struct{ base, root, dest string }

func (e parityEnv) path(elem ...string) string {
	return filepath.Join(append([]string{e.base}, elem...)...)
}

// parityFixture is this computer, LAP-1, with web-app and notes pushed to its archive
// (pages made, zipping off), and in the archive LAP-2's api and an older copy of pWeb1
// that clean removes. A folder of transcripts waits for restore, and an empty api
// folder for import to find. Every conversation is dated 2026-08-30.
func parityFixture(t *testing.T) parityEnv {
	t.Helper()
	base := t.TempDir()
	e := parityEnv{base: base, root: filepath.Join(base, ".claude"), dest: filepath.Join(base, "archive")}
	at := time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC)
	for _, d := range []string{e.dest, e.path("dev", "web-app"), e.path("dev", "notes"), e.path("dev", "api"), e.path("dev", "restored")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	localConv(t, e.root, e.path("dev", "web-app"), pWeb1, at, "w1", "w2")
	localConv(t, e.root, e.path("dev", "web-app"), pWeb2, at, "w3")
	localConv(t, e.root, e.path("dev", "notes"), pNotes, at, "n1")
	off := false
	if err := archive.SaveConfig(e.root, archive.Config{Destination: e.dest, ZipSessions: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err := runPush(quietStreams(), e.root, "", pushOpts{}); err != nil {
		t.Fatal(err)
	}

	writeShard(t, e.dest, archive.Shard{Machine: "LAP-2", SkipsHeld: true, ReadsZips: true, HonorsDeleted: true,
		Projects: map[string]archive.Project{lap2Bucket: {Path: `E:\dev\api`, Leaf: "api"}}})
	dated := func(p, body string) {
		writeFile(t, p, body)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	dated(filepath.Join(archive.ProjectsDir(e.dest), lap2Bucket, pAPI+".jsonl"),
		strings.ReplaceAll(strings.ReplaceAll(turns(at, "a1", "a2"), "/Users/you/dev/web-app", `E:\\dev\\api`), "prompt a1", "prompt a1 for api"))
	archived := filepath.Join(archive.ProjectsDir(e.dest), claude.Slug(e.path("dev", "web-app")), pWeb1+".jsonl")
	first, _, _ := strings.Cut(mustReadFile(t, archived), "\n") // the archived copy holds it in whole, and more
	dated(filepath.Join(archive.ProjectsDir(e.dest), oldBucket, pWeb1+".jsonl"), first+"\n")
	dated(e.path("restore-src", pOld+".jsonl"), strings.ReplaceAll(strings.ReplaceAll(turns(at, "o1"), "/Users/you/dev/web-app", oldProject), "prompt o1", "prompt o1 for proj"))
	return e
}

var (
	rfc3339Time = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?(Z|[+-]\d\d:\d\d)`)
	minuteTime  = regexp.MustCompile(`\d{4}-\d\d-\d\d \d\d:\d\d(:\d\d)?`)
	fileStamp   = regexp.MustCompile(`\b\d{8}-\d{6}\b`) // a backup's name, a folder of replaced files
)

// normal is a JSON result made comparable between twins: indented with sorted keys,
// and as normalText says.
func (e parityEnv) normal(t *testing.T, raw []byte, start time.Time) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%v in %s", err, raw)
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return e.normalText(string(b), start)
}

// layout is every file under the twin's folder, by its normal name, and each
// transcript's size: what a run left, which a result does not always say (a dry run's
// and a real run's can be the same).
// The log, the lock and the panel's own file are not the command's.
func (e parityEnv) layout(t *testing.T, start time.Time) string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(e.base, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(e.base, p)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(d.Name(), "session-sync.log") || d.Name() == "session-sync.lock" || rel == ".claude/session-sync-base/panel.json" {
			return nil
		}
		if strings.HasSuffix(rel, ".jsonl") { // what a rewrite or an update changes; other files hold times
			info, err := d.Info()
			if err != nil {
				return err
			}
			rel += fmt.Sprintf(" %d", info.Size())
		}
		names = append(names, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	return e.normalText(strings.Join(names, "\n"), start)
}

// normalText takes out what differs between twins: times at or after the start of the
// test's hour become <NOW> (Ruling P14), file stamps <STAMP>, and e's paths placeholders.
func (e parityEnv) normalText(s string, start time.Time) string {
	hour := start.Truncate(time.Hour)
	s = rfc3339Time.ReplaceAllStringFunc(s, func(m string) string {
		if at, err := time.Parse(time.RFC3339Nano, m); err == nil && !at.Before(hour) {
			return "<NOW>"
		}
		return m
	})
	s = minuteTime.ReplaceAllStringFunc(s, func(m string) string {
		for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04"} {
			if at, err := time.ParseInLocation(layout, m, time.Local); err == nil && !at.Before(hour) {
				return "<NOW>"
			}
		}
		return m
	})
	s = fileStamp.ReplaceAllString(s, "<STAMP>")
	roots := map[string]string{"<BASE>": e.base, "<BASE-SLUG>": claude.Slug(e.base)}
	if real, err := filepath.EvalSymlinks(e.base); err == nil && real != e.base {
		roots["<BASE-SLUG-REAL>"] = claude.Slug(real)
	}
	return golden.Normalize(s, roots)
}

type parityCase struct {
	name, action string
	setup        func(t *testing.T, e parityEnv)  // on both twins, before either runs
	cli          func(e parityEnv) []string       // the command and its flags; --claude-dir and --json are added
	body         func(e parityEnv) map[string]any // the panel's
	says         string                           // text the job's lines hold, for a flag neither the result nor the files show
}

func parityCases() []parityCase {
	args := func(a ...string) func(parityEnv) []string { return func(parityEnv) []string { return a } }
	body := func(b map[string]any) func(parityEnv) map[string]any {
		return func(parityEnv) map[string]any { return b }
	}
	config := func(t *testing.T, e parityEnv, action string, a ...string) {
		t.Helper()
		if _, err := runConfig(quietStreams(), configOpts{ClaudeDir: e.root, Action: action, Args: a}); err != nil {
			t.Fatal(err)
		}
	}
	install := func(t *testing.T, e parityEnv) {
		t.Helper()
		if _, err := runInstall(quietStreams(), installOpts{ClaudeDir: e.root, Every: 30}); err != nil {
			t.Fatal(err)
		}
	}
	// Rules here, and LAP-2's newer ones in the archive, which only --update-profile takes.
	newerRules := func(t *testing.T, e parityEnv) {
		for p, at := range map[string]time.Time{filepath.Join(e.root, "CLAUDE.md"): time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC),
			filepath.Join(e.dest, "profile", "CLAUDE.md"): time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)} {
			writeFile(t, p, "# rules written "+at.Format("2 Jan")+"\n")
			if err := os.Chtimes(p, at, at); err != nil {
				t.Fatal(err)
			}
		}
	}
	restoreArgs := func(e parityEnv) []string {
		return []string{"restore", "--source", e.path("restore-src"), "--project-path", e.path("dev", "restored"), "--rewrite-from", oldProject}
	}
	restoreBody := func(e parityEnv) map[string]any {
		return map[string]any{"source": e.path("restore-src"), "projectPath": e.path("dev", "restored"), "rewriteFrom": oldProject}
	}
	cases := []parityCase{
		{name: "push", action: "push", cli: args("push"), body: body(nil)},
		{name: "push-dry-run", action: "push", cli: args("push", "--dry-run"), body: body(map[string]any{"dryRun": true})},
		{name: "push-scan-history", action: "push", cli: args("push", "--scan-history"), body: body(map[string]any{"scanHistory": true}),
			says: "WARNING: --scan-history reads every conversation"},

		{name: "import", action: "import", cli: args("import"), body: body(nil)},
		{name: "import-dry-run", action: "import", cli: args("import", "--dry-run"), body: body(map[string]any{"dryRun": true})},
		// One level under base is too shallow for dev/api: not found.
		{name: "import-search-root-depth", action: "import",
			cli:  func(e parityEnv) []string { return []string{"import", "--search-root", e.base, "--depth", "1"} },
			body: func(e parityEnv) map[string]any { return map[string]any{"searchRoot": e.base, "depth": 1} }},
		// Mapped to a folder of another name, whose bare name replaces "api" in the text.
		{name: "import-bare-name", action: "import",
			setup: func(t *testing.T, e parityEnv) {
				mustMkdirAll(t, e.path("dev", "api-here"))
				config(t, e, "map", lap2Bucket, e.path("dev", "api-here"))
			},
			cli: args("import", "--include-bare-name"), body: body(map[string]any{"includeBareName": true})},
		// LAP-2 went on with a conversation brought in before.
		{name: "import-update", action: "import",
			setup: lap2WentOn(func(e parityEnv) error {
				_, err := runImport(quietStreams(), importOpts{ClaudeDir: e.root})
				return err
			}),
			cli: args("import", "--update"), body: body(map[string]any{"update": true})},
		{name: "import-no-work-no-plugins", action: "import", cli: args("import", "--no-work", "--no-plugins"),
			body: body(map[string]any{"noWork": true, "noPlugins": true}), says: noWorkNote},
		{name: "import-update-profile-repoint", action: "import", setup: newerRules, cli: args("import", "--update-profile", "--repoint"),
			body: body(map[string]any{"updateProfile": true, "repoint": true}), says: "repointed 0 conversation(s)"},
		{name: "import-profile-only", action: "import", cli: args("import", "--profile-only"), body: body(map[string]any{"profileOnly": true})},

		{name: "pull", action: "pull", cli: args("pull"), body: body(nil)},
		{name: "pull-dry-run", action: "pull", cli: args("pull", "--dry-run"), body: body(map[string]any{"dryRun": true})},
		{name: "pull-options", action: "pull", setup: newerRules, cli: args("pull", "--update-profile", "--no-plugins", "--no-work"),
			body: body(map[string]any{"updateProfile": true, "noPlugins": true, "noWork": true}), says: noWorkNote},
		{name: "pull-update", action: "pull", setup: lap2WentOn(func(e parityEnv) error { _, err := runPull(quietStreams(), pullOpts{ClaudeDir: e.root}); return err }),
			cli: args("pull", "--update"), body: body(map[string]any{"update": true})},

		{name: "restore", action: "restore", cli: restoreArgs, body: restoreBody},
		{name: "restore-bare-name-update", action: "restore",
			cli: func(e parityEnv) []string { return append(restoreArgs(e), "--include-bare-name", "--update") },
			body: func(e parityEnv) map[string]any {
				b := restoreBody(e)
				b["includeBareName"], b["update"] = true, true
				return b
			}},
		{name: "restore-dry-run", action: "restore",
			cli: func(e parityEnv) []string { return append(restoreArgs(e), "--dry-run") },
			body: func(e parityEnv) map[string]any {
				b := restoreBody(e)
				b["dryRun"] = true
				return b
			}},

		{name: "render", action: "render", cli: args("render"), body: body(nil)},
		{name: "render-force-exclude", action: "render", cli: args("render", "--force", "--exclude", "notes"),
			body: body(map[string]any{"force": true, "exclude": []string{"notes"}})},
		{name: "render-include", action: "render",
			setup: func(t *testing.T, e parityEnv) {
				if _, err := runRender(quietStreams(), renderOpts{ClaudeDir: e.root, Exclude: []string{"notes"}}); err != nil {
					t.Fatal(err)
				}
			},
			cli: args("render", "--include", "notes"), body: body(map[string]any{"include": []string{"notes"}})},

		{name: "clean", action: "clean", cli: args("clean"), body: body(nil)},
		{name: "clean-yes", action: "clean", cli: args("clean", "--yes"), body: body(map[string]any{"yes": true})},

		{name: "prune-older-than", action: "prune", cli: args("prune", "--older-than", "30d"), body: body(map[string]any{"olderThanDays": 30})},
		{name: "prune-project", action: "prune", cli: args("prune", "--project", "notes"), body: body(map[string]any{"projects": []string{"notes"}})},
		{name: "prune-yes", action: "prune", cli: args("prune", pWeb2, "--yes"), body: body(map[string]any{"ids": []string{pWeb2}, "yes": true})},
		{name: "prune-undo", action: "prune-undo",
			setup: func(t *testing.T, e parityEnv) {
				if _, err := runPrune(quietStreams(), pruneOpts{ClaudeDir: e.root, IDs: []string{pWeb2}, Yes: true}); err != nil {
					t.Fatal(err)
				}
			},
			cli: args("prune", "--undo", pWeb2), body: body(map[string]any{"ids": []string{pWeb2}})},

		{name: "backup", action: "backup", cli: func(e parityEnv) []string { return []string{"backup", "--out", e.path("backups")} },
			body: func(e parityEnv) map[string]any { return map[string]any{"out": e.path("backups")} }},
		{name: "backup-dry-run", action: "backup", cli: func(e parityEnv) []string { return []string{"backup", "--dry-run", "--out", e.path("backups")} },
			body: func(e parityEnv) map[string]any { return map[string]any{"dryRun": true, "out": e.path("backups")} }},

		{name: "install", action: "install", cli: args("install"), body: body(nil)},
		{name: "install-every", action: "install", cli: args("install", "--every", "60"), body: body(map[string]any{"every": 60})},
		{name: "install-no-sweep", action: "install", setup: install, cli: args("install", "--no-sweep"), body: body(map[string]any{"noSweep": true})},
		// A PowerShell hook too, which uninstall leaves alone as the CLI does (Ruling P41).
		{name: "uninstall", action: "uninstall",
			setup: func(t *testing.T, e parityEnv) {
				install(t, e)
				p := claude.SettingsPath(e.root)
				var doc map[string]any
				if err := json.Unmarshal([]byte(mustReadFile(t, p)), &doc); err != nil {
					t.Fatal(err)
				}
				hooks := doc["hooks"].(map[string]any)
				hooks["SessionEnd"] = append(hooks["SessionEnd"].([]any), map[string]any{"hooks": []any{
					map[string]any{"type": "command", "command": `powershell -File C:\tools\sync-claude-sessions.ps1`}}})
				writeJSON(t, p, doc)
			},
			cli: args("uninstall"), body: body(nil)},

		{name: "machines-forget", action: "machines-forget", cli: args("machines", "--forget", "LAP-2"), body: body(map[string]any{"name": "LAP-2"})},
		{name: "doctor", action: "doctor", cli: args("doctor"), body: body(nil)},
	}

	// One case per config action, each with valid arguments.
	type cfgCase struct {
		action string
		setup  func(t *testing.T, e parityEnv)
		args   func(e parityEnv) []string
	}
	fixed := func(a ...string) func(parityEnv) []string { return func(parityEnv) []string { return a } }
	for _, c := range []cfgCase{
		{"show", nil, fixed()},
		{"set-destination", func(t *testing.T, e parityEnv) { mustMkdirAll(t, e.path("archive-new")) },
			func(e parityEnv) []string { return []string{e.path("archive-new")} }},
		{"map", nil, func(e parityEnv) []string { return []string{lap2Bucket, e.path("dev", "api")} }},
		{"unmap", func(t *testing.T, e parityEnv) { config(t, e, "map", lap2Bucket, e.path("dev", "api")) }, fixed(lap2Bucket)},
		{"set-machine", nil, fixed("LAP-9")},
		{"work", nil, fixed("off")},
		{"work-placement", nil, fixed("machine")},
		{"map-folder", nil, func(e parityEnv) []string { return []string{`E:\work\reports`, e.path("dev", "notes")} }},
		{"unmap-folder", func(t *testing.T, e parityEnv) { config(t, e, "map-folder", `E:\work\reports`, e.path("dev", "notes")) }, fixed(`E:\work\reports`)},
		{"exclude-folder", nil, func(e parityEnv) []string { return []string{e.path("dev", "notes")} }},
		{"unexclude-folder", func(t *testing.T, e parityEnv) { config(t, e, "exclude-folder", e.path("dev", "notes")) },
			func(e parityEnv) []string { return []string{e.path("dev", "notes")} }},
		{"keep", nil, fixed("notes")},
		{"unkeep", func(t *testing.T, e parityEnv) { config(t, e, "keep", "notes") }, fixed("notes")},
		{"keep-days", nil, fixed("180")},
		{"zip-sessions", nil, fixed("on")},
	} {
		c := c
		cases = append(cases, parityCase{name: "config-" + c.action, action: "config", setup: c.setup,
			cli:  func(e parityEnv) []string { return append([]string{"config", c.action}, c.args(e)...) },
			body: func(e parityEnv) map[string]any { return map[string]any{"action": c.action, "args": nonNil(c.args(e))} }})
	}
	return cases
}

// lap2WentOn brings LAP-2's conversation in with bring, then gives it a turn more in
// the archive, which only --update brings in.
func lap2WentOn(bring func(e parityEnv) error) func(t *testing.T, e parityEnv) {
	return func(t *testing.T, e parityEnv) {
		t.Helper()
		if err := bring(e); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(archive.ProjectsDir(e.dest), lap2Bucket, pAPI+".jsonl")
		at := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
		writeFile(t, p, mustReadFile(t, p)+strings.ReplaceAll(turns(at, "a3"), "/Users/you/dev/web-app", `E:\\dev\\api`))
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
}

func mustMkdirAll(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

// fakeSweep stands in for the scheduled sweep and the panel's shortcut, which a test
// must not register or create, and says the interval it was given.
func fakeSweep(t *testing.T) {
	hook(t, &installSweep, func(_ hostagent.Job, every int) (hostagent.Status, error) {
		return hostagent.Status{Installed: true, Mechanism: "fake", Detail: fmt.Sprintf("every %d minutes", every)}, nil
	})
	hook(t, &uninstallSweep, func(hostagent.Job) (bool, error) { return true, nil })
	hook(t, &installShortcut, func(hostagent.Job) (string, error) { return "/x/claude-sessions panel.app", nil })
	hook(t, &uninstallShortcut, func(hostagent.Job) (bool, error) { return true, nil })
	hook(t, &legacySweep, func() (string, bool) { return "", false })
}

// actionPanel serves a panel on root for the test, and gives it with its cookie.
func actionPanel(t *testing.T, root string) (*uiServer, string) {
	t.Helper()
	u, err := newUIServer(uiOpts{ClaudeDir: root, NoOpen: true, IdleAfter: time.Hour, Tick: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	runUI(t, u)
	return u, login(t, u)
}

// postRun sends POST /api/run/<path> with body, as the page does.
func postRun(t *testing.T, u *uiServer, cookie, path, body string) (*http.Response, string) {
	t.Helper()
	resp, text, err := tryRun(u, cookie, path, body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, text
}

// tryRun is postRun for a goroutine, which must not stop the test itself.
func tryRun(u *uiServer, cookie, path, body string) (*http.Response, string, error) {
	req, err := http.NewRequest(http.MethodPost, u.url("/api/run/"+path), strings.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Origin", "http://"+req.URL.Host)
	req.Header.Set("X-CS-Token", u.panel.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp, string(b), err
}

// lowerCamel is a flag's name as a body field: no-write-probe is noWriteProbe.
func lowerCamel(flag string) string {
	parts := strings.Split(flag, "-")
	for i := 1; i < len(parts); i++ {
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

type finalState struct {
	OK     bool            `json:"ok"`
	Error  string          `json:"error"`
	Lines  []string        `json:"lines"`
	Result json.RawMessage `json:"result"`
}

// runAction runs name with body to its end and gives the job's final state.
func runAction(t *testing.T, u *uiServer, cookie, name string, body any) finalState {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, text := postRun(t, u, cookie, name+"?wait=1", string(b))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %d %s", name, resp.StatusCode, text)
	}
	var st finalState
	if err := json.Unmarshal([]byte(text), &st); err != nil {
		t.Fatalf("%s: %v in %s", name, err, text)
	}
	return st
}

// The one-code-path rule: an action's result is the CLI's --json for the same flags,
// on twin profiles.
func TestPanelMatchesCLIJSON(t *testing.T) {
	fakeSweep(t)
	for _, c := range parityCases() {
		c := c
		t.Run(c.name, func(t *testing.T) {
			start := time.Now()
			isolateUI(t)
			t.Setenv("CLAUDE_SESSIONS_MACHINE", "LAP-1")
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			a, b := parityFixture(t), parityFixture(t)
			if c.setup != nil {
				c.setup(t, a)
				c.setup(t, b)
			}

			args := append(c.cli(a), "--claude-dir", a.root, "--json")
			var out string
			stderr, err := captureFile(t, &os.Stderr, func() (err error) {
				out, err = captureStdout(t, func() error { return cliCommands[args[0]](args[1:]) })
				return err
			})
			if err != nil {
				t.Fatalf("claude-sessions %s: %v\n%s", strings.Join(args, " "), err, stderr)
			}

			u, cookie := actionPanel(t, b.root)
			st := runAction(t, u, cookie, c.action, c.body(b))
			if !st.OK {
				t.Fatalf("the panel's %s failed: %s\n%s", c.action, st.Error, strings.Join(st.Lines, "\n"))
			}
			if want, got := a.normal(t, []byte(out), start), b.normal(t, st.Result, start); got != want {
				t.Errorf("the panel's result differs from claude-sessions %s:\n--- CLI\n%s\n--- panel\n%s",
					strings.Join(c.cli(a), " "), want, got)
			}
			if c.says != "" && !strings.Contains(strings.Join(st.Lines, "\n"), c.says) {
				t.Errorf("the job did not say %q:\n%s", c.says, strings.Join(st.Lines, "\n"))
			}
			if want, got := a.layout(t, start), b.layout(t, start); got != want {
				t.Errorf("the panel's run left other files than claude-sessions %s:\n--- CLI\n%s\n--- panel\n%s",
					strings.Join(c.cli(a), " "), want, got)
			}
		})
	}
}

func TestEveryActionHasAParityCase(t *testing.T) {
	var want, got, configs []string
	for name := range uiActions {
		if name != "open-log" { // it opens a file in another program: there is no CLI for it
			want = append(want, name)
		}
	}
	seen := map[string]bool{}
	for _, c := range parityCases() {
		if !seen[c.action] {
			seen[c.action] = true
			got = append(got, c.action)
		}
		if a, ok := strings.CutPrefix(c.name, "config-"); ok && c.action == "config" {
			configs = append(configs, a)
		}
	}
	sort.Strings(want)
	sort.Strings(got)
	if !slices.Equal(got, want) {
		t.Errorf("parity cases are for %v; the actions are %v", got, want)
	}
	if !slices.Equal(configs, panelConfigActions) {
		t.Errorf("config parity cases are %v; panelConfigActions is %v", configs, panelConfigActions)
	}
}

// No body field reaches a flag the panel leaves out: --quiet's watchdog would exit the
// panel, and the others belong to the hooks, the sweep and diagnostics.
func TestRunRefusesUnknownFields(t *testing.T) {
	isolateUI(t)
	fakeSweep(t) // should a refusal fail, no sweep is registered and no log opened
	recordOpens(t)
	root, _ := queryFixture(t)
	u, cookie := actionPanel(t, root)
	if resp, body := postRun(t, u, cookie, "push", `{"quiet": true}`); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, badRun) {
		t.Errorf(`{"quiet": true} to push gave %d %s, want 400`, resp.StatusCode, body)
	}

	// Every exempt flag, by its Go and its JSON names, to every action.
	names := []string{"noHTML", "noProbe", "keepLegacy", "archiveDir", "replaceLegacySweep", "claudeDir", "json"}
	for _, x := range mapExempt {
		if i := strings.Index(x, "--"); i >= 0 { // not a command
			names = append(names, lowerCamel(x[i+2:]))
		}
	}
	for name := range uiActions {
		for _, f := range names {
			if name == "render" && f == "exclude" {
				continue // render --exclude is a real field; doctor's is not
			}
			if resp, body := postRun(t, u, cookie, name, fmt.Sprintf(`{%q: true}`, f)); resp.StatusCode != http.StatusBadRequest {
				t.Errorf("%s with %q gave %d %s, want 400", name, f, resp.StatusCode, body)
			}
		}
	}
	for _, c := range []struct{ name, body string }{
		{"push", `{"dryRun": true} {}`},                                     // two values
		{"push", `[]`},                                                      // not an object
		{"config", `{"action": "rm", "args": []}`},                          // not a config action
		{"config", `{"action": "", "args": []}`},                            // show is named
		{"push", `{"dryRun": true` + strings.Repeat(" ", maxRunBody) + `}`}, // over the limit
	} {
		if resp, body := postRun(t, u, cookie, c.name, c.body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s with %.60s gave %d %s, want 400", c.name, c.body, resp.StatusCode, body)
		}
	}
	if resp, _ := postRun(t, u, cookie, "rm", `{}`); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown action gave %d, want 404", resp.StatusCode)
	}
	if j := u.jobs.Current(); j != nil || u.jobs.Get("1") != nil {
		t.Error("a refused request started a job")
	}
}

// The scheduled sweep holds the lock when the person clicks Back up now: the job fails
// with the plain sentence, and neither the archive nor the sweep's lock is touched.
func TestRunWhileSweepHoldsLock(t *testing.T) {
	isolateUI(t)
	t.Setenv("CLAUDE_SESSIONS_MACHINE", "LAP-1")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	e := parityFixture(t)
	lock, err := archive.AcquireLock(e.root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	lockPath := filepath.Join(e.root, "session-sync.lock")
	held, before := mustReadFile(t, lockPath), tree(t, e.dest)

	u, cookie := actionPanel(t, e.root)
	st := runAction(t, u, cookie, "push", map[string]any{})
	if st.OK || st.Error != "Another backup or import is running on this computer. Try again in a minute." {
		t.Errorf("push under the sweep's lock: ok %v, error %q", st.OK, st.Error)
	}
	if after := tree(t, e.dest); !mapsEqual(before, after) {
		t.Error("push changed the archive while the sweep held the lock")
	}
	if now := mustReadFile(t, lockPath); now != held {
		t.Errorf("the lock file changed: %q, was %q", now, held)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// Two tabs, or a double click: a second POST while a job runs gets 409 and starts
// nothing, so a conversation is deleted, and recorded, once.
func TestDoubleSubmitStartsOneJob(t *testing.T) {
	isolateUI(t)
	t.Setenv("CLAUDE_SESSIONS_MACHINE", "LAP-1")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	e := parityFixture(t)
	u, cookie := actionPanel(t, e.root)
	// prune waits for the lock, so the first job is still running when the second POST comes.
	lock, err := archive.AcquireLock(e.root)
	if err != nil {
		t.Fatal(err)
	}
	type answer struct {
		code int
		body string
	}
	answers := make(chan answer, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, body, err := tryRun(u, cookie, "prune", `{"ids": ["`+pWeb2+`"], "yes": true}`)
			if err != nil {
				t.Error(err)
				return
			}
			answers <- answer{resp.StatusCode, body}
		}()
	}
	wg.Wait()
	close(answers)
	var codes []int
	var id string
	for a := range answers {
		codes = append(codes, a.code)
		switch a.code {
		case http.StatusOK:
			var started struct{ ID string }
			if err := json.Unmarshal([]byte(a.body), &started); err != nil {
				t.Fatal(err)
			}
			id = started.ID
		case http.StatusConflict:
			if want := `{"error":"Another action is running: Delete old conversations. It will finish first."}`; strings.TrimSpace(a.body) != want {
				t.Errorf("409 said %s, want %s", a.body, want)
			}
		}
	}
	sort.Ints(codes)
	if !slices.Equal(codes, []int{http.StatusOK, http.StatusConflict}) {
		t.Fatalf("two POSTs gave %v, want one 200 and one 409", codes)
	}
	lock.Release()
	if st := u.jobs.Get(id).Wait(); !st.OK {
		t.Fatalf("the prune failed: %s\n%s", st.Error, strings.Join(st.Lines, "\n"))
	}
	var list struct{ Entries []archive.DeletedEntry }
	if err := json.Unmarshal([]byte(mustReadFile(t, filepath.Join(archive.DeletedDir(e.dest), "LAP-1.json"))), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Entries) != 1 || list.Entries[0].ID != pWeb2 {
		t.Errorf("the deleted list holds %+v, want %s once", list.Entries, pWeb2)
	}
	if u.jobs.Get("2") != nil {
		t.Error("the second POST started a job")
	}
}

// Every GET the panel serves answers 200 with JSON, on a real archive. A route added
// later is asked for with no parameters.
func TestEveryGetRouteAnswers(t *testing.T) {
	isolateUI(t)
	root, _ := queryFixture(t)
	u, cookie := actionPanel(t, root)
	st := runAction(t, u, cookie, "config", map[string]any{"action": "show"}) // a job for the events stream
	if !st.OK {
		t.Fatalf("config show: %s", st.Error)
	}
	for _, r := range u.routes() {
		if r.Method != http.MethodGet {
			continue
		}
		path := r.Path
		switch path {
		case "/read/":
			path += qbWeb + "/" + qWeb
		case "/api/jobs/events":
			path += "?id=1"
		}
		resp, body := send(t, u, http.MethodGet, u.url(path), cookie)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s gave %d %s", path, resp.StatusCode, body)
			continue
		}
		switch r.Path {
		case "/read/":
			if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
				t.Errorf("GET %s is %s", path, resp.Header.Get("Content-Type"))
			}
			continue
		case "/api/jobs/events":
			_, data, ok := strings.Cut(body, "event: done\ndata: ")
			if !ok {
				t.Errorf("GET %s has no done event:\n%s", path, body)
				continue
			}
			body, _, _ = strings.Cut(data, "\n")
		}
		var v any
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			t.Errorf("GET %s: %v in %.200s", path, err, body)
		}
	}
}

// A changing action that changed something says so in Activity once; a preview or a
// dry run says nothing. clean keeps its check for Storage.
func TestActionsReportToActivity(t *testing.T) {
	isolateUI(t)
	t.Setenv("CLAUDE_SESSIONS_MACHINE", "LAP-1")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	e := parityFixture(t)
	u, cookie := actionPanel(t, e.root)
	uiLines := func() []string {
		var out []string
		for _, l := range strings.Split(mustReadFile(t, filepath.Join(e.root, "session-sync.log")), "\n") {
			if _, rest, ok := strings.Cut(l, "  UI: "); ok {
				out = append(out, rest)
			}
		}
		return out
	}
	for _, c := range []struct {
		action string
		body   map[string]any
		want   []string
	}{
		{"clean", nil, nil},
		{"import", map[string]any{"dryRun": true}, nil}, // it would bring one in
		{"config", map[string]any{"action": "keep-days"}, nil},
		{"prune", map[string]any{"ids": []string{pWeb2}}, nil},
		{"clean", map[string]any{"yes": true}, []string{"Removed 1 duplicate copy and freed "}},
		{"prune", map[string]any{"ids": []string{pWeb2}, "yes": true}, []string{"Deleted 1 conversation from the archive"}},
		{"config", map[string]any{"action": "zip-sessions", "args": []string{"on"}}, []string{"Changed Compress old conversations"}},
	} {
		before := uiLines()
		if st := runAction(t, u, cookie, c.action, c.body); !st.OK {
			t.Fatalf("%s %v: %s", c.action, c.body, st.Error)
		}
		added := uiLines()[len(before):]
		if len(added) != len(c.want) || len(c.want) == 1 && !strings.HasPrefix(added[0], c.want[0]) {
			t.Errorf("%s %v logged %q, want %q", c.action, c.body, added, c.want)
		}
		if c.action == "clean" {
			u.mu.Lock()
			last := u.lastClean
			u.mu.Unlock()
			wantCopies := 1 // found by the check
			if c.body != nil {
				wantCopies = 0 // left after removing it
			}
			if last == nil || last.Copies != wantCopies || (last.Bytes > 0) != (wantCopies > 0) || time.Since(last.At) > time.Minute {
				t.Errorf("after clean %v, lastClean is %+v, want %d copies", c.body, last, wantCopies)
			}
		}
	}
}

func TestOpenLogOpensTheLog(t *testing.T) {
	isolateUI(t)
	rec := recordOpens(t)
	u, cookie := actionPanel(t, t.TempDir())
	if st := runAction(t, u, cookie, "open-log", map[string]any{}); !st.OK {
		t.Fatalf("open-log: %s", st.Error)
	}
	if got := rec.get(); len(got) != 1 || got[0] != filepath.Join(u.root, "session-sync.log") {
		t.Errorf("opened %v, want the log", got)
	}
}

// prune-undo without ids is refused as an undo: a nil Undo would make it a prune
// (Ruling P44).
func TestUndoWithoutIDsIsAnUndo(t *testing.T) {
	isolateUI(t)
	t.Setenv("CLAUDE_SESSIONS_MACHINE", "LAP-1")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	e := parityFixture(t)
	u, cookie := actionPanel(t, e.root)
	if st := runAction(t, u, cookie, "prune-undo", map[string]any{}); st.OK {
		t.Fatal("prune-undo with no ids succeeded")
	}
	if log := mustReadFile(t, filepath.Join(e.root, "session-sync.log")); !strings.Contains(log, "PANEL: prune-undo: usage: claude-sessions prune --undo") {
		t.Errorf("prune-undo with no ids did not fail as an undo:\n%s", log)
	}
}

// A refused action is no backup: its log line is PANEL:, which Activity never shows
// (Ruling P55), not ERROR:, which Activity reads as a backup that did not finish.
func TestRefusedActionIsNoFailedBackup(t *testing.T) {
	isolateUI(t)
	t.Setenv("CLAUDE_SESSIONS_MACHINE", "LAP-1")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	e := parityFixture(t)
	u, cookie := actionPanel(t, e.root)
	if st := runAction(t, u, cookie, "config", map[string]any{"action": "keep", "args": []string{"no-such-project"}}); st.OK {
		t.Fatal("keeping a project not archived succeeded")
	}
	log := mustReadFile(t, filepath.Join(e.root, "session-sync.log"))
	if !strings.Contains(log, "PANEL: config: ") {
		t.Errorf("the refusal is not logged as PANEL:\n%s", log)
	}
	for _, it := range queryActivity(e.root) {
		if strings.Contains(it.Text, "did not finish") {
			t.Errorf("Activity says %q for a refused config keep", it.Text)
		}
	}
}

// Once Stop is decided no job starts (Ruling P22), and the page is told why in plain words.
func TestRunAfterStopIsRefused(t *testing.T) {
	isolateUI(t)
	u, cookie := actionPanel(t, t.TempDir())
	u.jobs.Close()
	resp, body := postRun(t, u, cookie, "push", `{}`)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, "The panel is stopping, so it starts nothing new.") {
		t.Errorf("a run after Stop gave %d %s", resp.StatusCode, body)
	}
}

// A path the panel would resolve against its own working folder is refused with a
// plain sentence, before any job; a full path, ~, or a project's name is not.
func TestRunRefusesRelativePaths(t *testing.T) {
	isolateUI(t)
	fakeSweep(t)
	recordOpens(t)
	root, _ := queryFixture(t)
	u, cookie := actionPanel(t, root)
	abs := t.TempDir()
	for _, c := range []struct{ action, body string }{
		{"restore", `{"source": "transcripts", "projectPath": "` + jsonPath(abs) + `"}`},
		{"restore", `{"source": "` + jsonPath(abs) + `", "projectPath": "dev/web-app"}`},
		{"backup", `{"out": "backups"}`},
		{"import", `{"searchRoot": "` + jsonPath(abs) + `, dev"}`},
		{"config", `{"action": "set-destination", "args": ["archive"]}`},
		{"config", `{"action": "map", "args": ["-Users-you-dev-api", "dev/api"]}`},
		{"config", `{"action": "map-folder", "args": ["/Users/you/reports", "reports"]}`},
		{"config", `{"action": "unexclude-folder", "args": ["dev/notes"]}`},
		{"prune", `{"projects": ["."]}`},
		{"prune", `{"projects": ["notes", ".."]}`},
		{"prune", `{"projects": ["dev/web-app"]}`},
		{"config", `{"action": "keep", "args": ["./web-app"]}`},
		{"config", `{"action": "unkeep", "args": ["dev\\web-app"]}`},
	} {
		resp, body := postRun(t, u, cookie, c.action, c.body)
		if want := `{"error":"` + string(notFull) + `"}`; resp.StatusCode != http.StatusBadRequest || strings.TrimSpace(body) != want {
			t.Errorf("%s %s gave %d %s, want 400 %s", c.action, c.body, resp.StatusCode, body, want)
		}
	}
	if u.jobs.Current() != nil || u.jobs.Get("1") != nil {
		t.Error("a refused path started a job")
	}

	for _, c := range []struct{ action, body string }{
		{"restore", `{"source": "` + jsonPath(abs) + `", "projectPath": "~/dev/web-app"}`},
		{"backup", `{"out": "~/claude-backups"}`},
		{"backup", `{}`},
		{"import", `{"searchRoot": "` + jsonPath(abs) + `, ~/dev"}`},
		{"config", `{"action": "set-destination", "args": ["` + jsonPath(abs) + `"]}`},
		{"config", `{"action": "map-folder", "args": ["E:\\work\\reports", "` + jsonPath(abs) + `"]}`},
		{"config", `{"action": "unexclude-folder", "args": ["E:\\work\\reports"]}`},
		{"prune", `{"projects": ["web-app", "/Users/you/dev/api", "E:\\web-app"]}`},
		{"config", `{"action": "keep", "args": ["web-app"]}`},
		{"config", `{"action": "unkeep", "args": ["` + qWeb + `"]}`},
	} {
		if err := uiActions[c.action].decode(json.RawMessage(c.body)); err != nil {
			t.Errorf("%s %s was refused: %v", c.action, c.body, err)
		}
	}
}

// jsonPath is p as a JSON string's content.
func jsonPath(p string) string { return strings.ReplaceAll(p, `\`, `\\`) }

// A changing run's activity line says what changed, and nothing for a run that changed
// nothing; a sweep that could not be changed is said too.
func TestActivityTextSaysWhatChanged(t *testing.T) {
	for _, c := range []struct {
		name   string
		result any
		want   string
	}{
		{"import", []importRow{{Filed: 2}, {Kind: "work", Machine: "LAP-2"}}, "Brought in 2 conversations"},
		{"import", []importRow{{Skipped: 3}, {Kind: "work", Machine: "LAP-2"}}, ""},
		{"pull", []importRow{{Updated: 1}, {Kind: "work", Filed: 1}}, "Brought in 1 conversation and 1 work folder"},
		{"install", installResult{Sweep: "launchd: every 30 minutes"}, "Set up automatic backup"},
		{"install", installResult{SweepFailed: "access denied"}, "Set up automatic backup after each reply; the scheduled backup could not be changed"},
		{"uninstall", uninstallResult{Sweep: "removed"}, "Turned off automatic backup"},
		{"uninstall", uninstallResult{Sweep: "FAILED: access denied"}, "Turned off automatic backup after each reply; the scheduled backup could not be removed"},
	} {
		if got := activityText(c.name, c.result); got != c.want {
			t.Errorf("activityText(%s, %+v) = %q, want %q", c.name, c.result, got, c.want)
		}
	}
}

// Storage is read again straight after a job, not kept from before it: clean --yes's
// removal shows at once.
func TestStorageShowsCleanAtOnce(t *testing.T) {
	isolateUI(t)
	t.Setenv("CLAUDE_SESSIONS_MACHINE", "LAP-1")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	e := parityFixture(t)
	u, cookie := actionPanel(t, e.root)
	var before, after storageView
	getJSON(t, u, cookie, "/api/storage", &before) // now kept for minutes
	if st := runAction(t, u, cookie, "clean", map[string]any{"yes": true}); !st.OK {
		t.Fatalf("clean --yes: %s", st.Error)
	}
	getJSON(t, u, cookie, "/api/storage", &after)
	if after.Sizes.Conversations >= before.Sizes.Conversations {
		t.Errorf("after clean --yes, Storage still shows %d bytes of conversations (was %d)", after.Sizes.Conversations, before.Sizes.Conversations)
	}
	if after.Duplicates == nil || after.Duplicates.Copies != 0 {
		t.Errorf("after clean --yes, duplicates are %+v", after.Duplicates)
	}
}

// A new name for this computer shows in the panel at once, not at its next start.
func TestSetMachineShowsAtOnce(t *testing.T) {
	isolateUI(t)
	t.Setenv("CLAUDE_SESSIONS_MACHINE", "") // the name saved, not the tests' own
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	machineMu.RLock()
	name := savedMachine
	machineMu.RUnlock()
	former := savedFormer()
	t.Cleanup(func() { setSavedMachine(name, former) })
	u, cookie := actionPanel(t, t.TempDir())
	if st := runAction(t, u, cookie, "config", map[string]any{"action": "set-machine", "args": []string{"LAP-7"}}); !st.OK {
		t.Fatalf("set-machine: %s", st.Error)
	}
	var state struct{ Machine string }
	if getJSON(t, u, cookie, "/api/state", &state); state.Machine != "LAP-7" {
		t.Errorf("/api/state says %q after set-machine LAP-7", state.Machine)
	}
}
