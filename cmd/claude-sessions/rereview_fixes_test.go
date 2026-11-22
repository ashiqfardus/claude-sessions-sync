package main_test

// Regressions from the second review.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
)

// 3. A projects.json entry from the PowerShell tool keeps the machine it names after
// pushes rewrite the file.
func TestLegacyMachineSurvivesPushes(t *testing.T) {
	f := newFixture(t)
	mustWrite(t, filepath.Join(f.archive, "projects.json"),
		`{"e--work-old":{"path":"E:\\work\\old","leaf":"old","machine":"LAP-2-admin"}}`)
	mustMkdir(t, filepath.Join(f.archive, "projects", "e--work-old"))
	for i := 0; i < 2; i++ {
		if out, err := f.push(t, "--no-html"); err != nil {
			t.Fatalf("push: %v\n%s", err, out)
		}
	}
	var view map[string]struct {
		Machine string `json:"machine"`
	}
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(f.archive, "projects.json"))), &view); err != nil {
		t.Fatal(err)
	}
	if got := view["e--work-old"].Machine; got != "LAP-2-admin" {
		t.Errorf("after two pushes the legacy entry names %q", got)
	}
}

// 4. Only the sweep marks its PUSH line: it is the run that may lack the privacy grant.
func TestOnlyTheSweepMarksItsPush(t *testing.T) {
	f := newFixture(t)
	if out, err := f.push(t, "--no-html", "--sweep"); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if out, err := f.push(t, "--no-html"); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	var marks []bool
	for _, line := range strings.Split(mustRead(t, filepath.Join(f.claudeDir, "session-sync.log")), "\n") {
		if strings.Contains(line, "PUSH:") {
			marks = append(marks, strings.HasSuffix(line, "[sweep]"))
		}
	}
	if len(marks) != 2 || !marks[0] || marks[1] {
		t.Errorf("want the sweep's PUSH marked and the other not, got %v", marks)
	}
}

// 5. A forked session is logged as forked once, not after every push.
func TestForkIsLoggedOnce(t *testing.T) {
	f := newFixture(t)
	l, _ := f.seedSession(t, uuidLines("u1", "u2", "a1", "a2", "a3"), uuidLines("u1", "u2", "b1"), true)
	f.push(t, "--no-html")
	mustWrite(t, l, mustRead(t, l)+uuidLines("a4"))
	f.push(t, "--no-html")
	f.push(t, "--no-html")
	if n := strings.Count(mustRead(t, filepath.Join(f.claudeDir, "session-sync.log")), "FORK:"); n != 1 {
		t.Errorf("FORK logged %d times, want once", n)
	}
	forks, _ := filepath.Glob(filepath.Join(f.archive, "forks", forkBucket, forkSession, "*.jsonl"))
	if len(forks) != 1 || !strings.Contains(mustRead(t, forks[0]), `"a4"`) {
		t.Errorf("the fork copy was not kept up to date: %v", forks)
	}
}

// 7. A mapping wins over a related repository with the same old path, on every run.
func TestMappingBeatsARelatedRepoEveryTime(t *testing.T) {
	for i := 0; i < 6; i++ {
		f := newImportFixture(t)
		mapped := f.makeRepo(t, "new-api", "git@github.com:example/api-v2.git")
		f.makeRepo(t, filepath.Join("old", "api"), "git@github.com:example/api.git")
		web := f.makeRepo(t, "web", "git@github.com:example/web.git")
		mustWrite(t, filepath.Join(f.archive, "projects", "D--work-web", forkSession+".jsonl"),
			`{"type":"user","uuid":"u1","cwd":"D:\\work\\web","message":{"role":"user","content":"see D:\\work\\api\\main.go"}}`+"\n")
		f.archiveProject(t, "D--work-api", `D:\work\api`, mapSession, "hello")
		f.writeManifest(t, map[string]any{
			"D--work-api": map[string]any{"path": `D:\work\api`, "leaf": "api"},
			"D--work-web": map[string]any{"path": `D:\work\web`, "leaf": "web", "remote": "git@github.com:example/web.git",
				"related": []map[string]string{{"path": `D:\work\api`, "remote": "git@github.com:example/api.git"}}},
		})
		if out, err := f.config(t, "map", "D--work-api", mapped); err != nil {
			t.Fatalf("map: %v\n%s", err, out)
		}
		if out, err := f.run(t, "import", "--search-root", f.dev, "--depth", "4"); err != nil {
			t.Fatalf("import: %v\n%s", err, out)
		}
		body := mustRead(t, filepath.Join(f.claudeDir, "projects", claude.Slug(web), forkSession+".jsonl"))
		if !strings.Contains(body, "new-api") {
			t.Fatalf("run %d: the mapped project lost to a related repository:\n%s", i, body)
		}
	}
}

// 9. An import with nothing new says nothing about forks, and a new fork is counted.
func TestImportReportsForksOnlyWhenTheyChange(t *testing.T) {
	f, _ := forkFixture(t, uuidLines("u1", "u2", "w1"), uuidLines("u1", "u2", "m1"))
	out, err := f.run(t, "import", "--search-root", f.dev, "--update")
	if err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if !strings.Contains(out, "1 filed") {
		t.Errorf("the new fork was not counted as filed:\n%s", out)
	}
	out, err = f.run(t, "import", "--search-root", f.dev, "--update")
	if err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if strings.Contains(out, "continued here and elsewhere") {
		t.Errorf("an import with nothing new reported the fork again:\n%s", out)
	}
}

// 11. A profile named .claude outside the home folder does not make its parent a home.
func TestImportUsesTheRecordedHomeNotAnyDotClaudeParent(t *testing.T) {
	f := newImportFixture(t)
	home := filepath.Dir(f.claudeDir)
	f.archiveProject(t, "D--work", `D:\work`, homeSession, "hi")
	data, _ := json.Marshal(map[string]any{"schemaVersion": 1, "machine": "WIN-BOX",
		"claudeRoot": `D:\work\.claude`, "home": `C:\Users\alice`,
		"projects": map[string]any{"D--work": map[string]any{"path": `D:\work`, "leaf": "work"}}})
	mustWrite(t, filepath.Join(f.archive, "manifest", "WIN-BOX.json"), string(data))

	out, err := f.runAsHome(t, home, "import")
	if err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if strings.Contains(out, "home folder") {
		t.Errorf("D:\\work was taken for a home folder:\n%s", out)
	}
}

// 11. push records this machine's home, which import then trusts.
func TestPushRecordsTheHomeFolder(t *testing.T) {
	f := newFixture(t)
	if out, err := f.push(t, "--no-html"); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	shards, _ := filepath.Glob(filepath.Join(f.archive, "manifest", "*.json"))
	if len(shards) != 1 || !strings.Contains(mustRead(t, shards[0]), `"home"`) {
		t.Errorf("the shard does not record the home folder: %v", shards)
	}
}

// 12. A session renamed on the other machine, with no new turn, brings the new title.
func TestImportUpdateBringsATitleChange(t *testing.T) {
	title := `{"type":"custom-title","customTitle":"renamed","sessionId":"` + forkSession + `"}` + "\n"
	f, repo := forkFixture(t, uuidLines("u1", "u2"), uuidLines("u1", "u2")+title)
	out, err := f.run(t, "import", "--search-root", f.dev, "--update")
	if err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	got := mustRead(t, filepath.Join(f.claudeDir, "projects", claude.Slug(repo), forkSession+".jsonl"))
	if !strings.Contains(got, "renamed") {
		t.Errorf("the other machine's title was not brought across:\n%s\n%s", got, out)
	}
	if n := len(f.localBucket(t, claude.Slug(repo))); n != 1 {
		t.Errorf("a title change was filed as a fork: %v", f.localBucket(t, claude.Slug(repo)))
	}
	_ = os.Stat
}
