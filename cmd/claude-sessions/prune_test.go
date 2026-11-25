package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/convzip"
)

const (
	keptID  = "11111111-2222-3333-4444-555555555555"
	otherID = "66666666-7777-8888-9999-000000000000"
)

// twoMachines is one project on a Mac and on a Windows laptop, plus an unrelated one.
func twoMachines() map[string]archive.Project {
	return map[string]archive.Project{
		"-Users-you-dev-web-app": {Path: "/Users/you/dev/web-app", Leaf: "web-app", Machine: "you-mac"},
		"e--web-app":             {Path: `E:\web-app`, Leaf: "web-app", Machine: "LAP-2"},
		"-Users-you-dev-api":     {Path: "/Users/you/dev/api", Leaf: "api", Machine: "you-mac"},
	}
}

func TestMatchProjectAcrossMachines(t *testing.T) {
	projects := twoMachines()
	for _, arg := range []string{"web-app", "WEB-APP", "/Users/you/dev/web-app", `E:\web-app`, "E:/web-app"} {
		id, buckets, err := matchProject(projects, arg)
		if err != nil {
			t.Fatalf("%s: %v", arg, err)
		}
		if id != (archive.KeepEntry{Leaf: "web-app"}) {
			t.Errorf("%s: identity %+v, want the folder name web-app", arg, id)
		}
		if want := []string{"-Users-you-dev-web-app", "e--web-app"}; !reflect.DeepEqual(buckets, want) {
			t.Errorf("%s: buckets %v, want %v", arg, buckets, want)
		}
	}
}

func TestMatchProjectByRemote(t *testing.T) {
	projects := map[string]archive.Project{
		"-Users-you-dev-web-app": {Path: "/Users/you/dev/web-app", Leaf: "web-app", Remote: "git@github.com:example/web-app.git"},
		"d--work-site":           {Path: `D:\work\site`, Leaf: "site", Remote: "https://github.com/example/web-app"},
		"d--work-other":          {Path: `D:\work\other`, Leaf: "other", Remote: "https://github.com/example/other"},
	}
	for _, arg := range []string{"/Users/you/dev/web-app", `D:\work\site`, "site"} {
		id, buckets, err := matchProject(projects, arg)
		if err != nil {
			t.Fatalf("%s: %v", arg, err)
		}
		if id.Remote == "" || id.Leaf != "" || id.Session != "" {
			t.Errorf("%s: identity %+v, want the remote", arg, id)
		}
		if want := []string{"-Users-you-dev-web-app", "d--work-site"}; !reflect.DeepEqual(buckets, want) {
			t.Errorf("%s: buckets %v, want %v", arg, buckets, want)
		}
	}
}

func TestMatchProjectAmbiguous(t *testing.T) {
	projects := map[string]archive.Project{
		"-Users-you-dev-api":  {Path: "/Users/you/dev/api", Leaf: "api", Remote: "git@github.com:example/api.git"},
		"e--clients-acme-api": {Path: `E:\clients\acme\api`, Leaf: "api", Remote: "git@github.com:acme/api.git"},
	}
	_, _, err := matchProject(projects, "api")
	if err == nil {
		t.Fatal("two unrelated repositories named api matched as one")
	}
	for _, want := range []string{"-Users-you-dev-api", "/Users/you/dev/api", "e--clients-acme-api", `E:\clients\acme\api`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %s:\n%v", want, err)
		}
	}
	// The path settles it.
	id, buckets, err := matchProject(projects, `E:\clients\acme\api`)
	if err != nil || id.Remote != "git@github.com:acme/api.git" || !reflect.DeepEqual(buckets, []string{"e--clients-acme-api"}) {
		t.Errorf("by path: %+v %v %v", id, buckets, err)
	}

	// A path whose bucket has no remote is the folder name of both: the refusal says
	// to keep each by its own path.
	projects["d--api"] = archive.Project{Path: `D:\api`, Leaf: "api"}
	if _, _, err := matchProject(projects, `D:\api`); err == nil || !strings.Contains(err.Error(), "by its own path") {
		t.Errorf("by a path with no remote: %v", err)
	}
}

func TestMatchProjectIsNotSubstring(t *testing.T) {
	projects := map[string]archive.Project{"e--web-app": {Path: `E:\web-app`, Leaf: "web-app"}}
	for _, arg := range []string{"app", "web", "web-ap", `E:\web`} {
		if id, buckets, err := matchProject(projects, arg); err == nil {
			t.Errorf("%q matched %+v %v", arg, id, buckets)
		}
	}
}

func TestKeptCoversEveryMachinesBucket(t *testing.T) {
	projects := twoMachines()
	id, _, err := matchProject(projects, "/Users/you/dev/web-app") // kept from the Mac
	if err != nil {
		t.Fatal(err)
	}
	r := archive.Retention{Keep: []archive.KeepEntry{id, {Session: keptID}}}

	if !kept(r, otherID, []string{"e--web-app"}, projects) {
		t.Error("a conversation only in the Windows bucket of a kept project is not kept")
	}
	if kept(r, otherID, []string{"-Users-you-dev-api"}, projects) {
		t.Error("a conversation in an unrelated project is kept")
	}
	if !kept(r, keptID, []string{"-Users-you-dev-api"}, projects) {
		t.Error("a kept conversation is not kept")
	}
	if !kept(r, otherID, []string{"-Users-you-dev-api", "e--web-app"}, projects) {
		t.Error("a conversation with one copy in a kept project is not kept")
	}

	// Kept by remote: a bucket whose machine could not read the remote is still the
	// project when its folder name matches a bucket that has it.
	projects["-Users-you-dev-web-app"] = archive.Project{Path: "/Users/you/dev/web-app", Leaf: "web-app", Remote: "git@github.com:example/web-app.git"}
	if id, _, err = matchProject(projects, "web-app"); err != nil || id.Remote == "" {
		t.Fatalf("%+v %v", id, err)
	}
	r = archive.Retention{Keep: []archive.KeepEntry{id}}
	if !kept(r, otherID, []string{"e--web-app"}, projects) {
		t.Error("a bucket with no remote is not kept with its project")
	}
}

// A bucket no manifest lists - its machine forgotten - has no known identity, so while
// any project is kept it cannot be shown not to be that project.
func TestKeptCountsAnUnlistedBucket(t *testing.T) {
	projects := twoMachines()
	byProject := archive.Retention{Keep: []archive.KeepEntry{{Leaf: "web-app"}}}
	if !kept(byProject, otherID, []string{"-Users-you-dev-gone"}, projects) {
		t.Error("a conversation in an unlisted bucket is not kept while a project is")
	}
	if kept(archive.Retention{Keep: []archive.KeepEntry{{Session: keptID}}}, otherID, []string{"-Users-you-dev-gone"}, projects) {
		t.Error("an unlisted bucket is kept when only conversations are")
	}
	if kept(archive.Retention{}, otherID, []string{"-Users-you-dev-gone"}, projects) {
		t.Error("an unlisted bucket is kept with an empty keep list")
	}
}

// The Mac's bucket has the remote and the Windows one has none. Once the Mac is
// forgotten, the Windows bucket must stay kept: keep stores the folder name too.
func TestKeepSurvivesForgettingTheMachineWithTheRemote(t *testing.T) {
	const remote = "git@github.com:example/web-app.git"
	projects := twoMachines()
	mac := projects["-Users-you-dev-web-app"]
	mac.Remote = remote
	projects["-Users-you-dev-web-app"] = mac
	claudeDir, dest := keepFixture(t, projects)
	config := func(args ...string) error {
		return cmdConfig(append([]string{"--claude-dir", claudeDir}, args...))
	}
	both := []archive.KeepEntry{{Remote: remote}, {Leaf: "web-app"}}

	if err := config("keep", "web-app"); err != nil {
		t.Fatal(err)
	}
	if r, _ := archive.LoadRetention(dest); !reflect.DeepEqual(r.Keep, both) {
		t.Fatalf("keep list %+v, want %+v", r.Keep, both)
	}
	if err := config("unkeep", "web-app"); err != nil {
		t.Fatal(err)
	}
	if r, _ := archive.LoadRetention(dest); len(r.Keep) != 0 {
		t.Fatalf("unkeep left %+v", r.Keep)
	}

	if err := config("keep", "/Users/you/dev/web-app"); err != nil {
		t.Fatal(err)
	}
	if err := archive.ForgetMachine(dest, "you-mac"); err != nil {
		t.Fatal(err)
	}
	r, err := archive.LoadRetention(dest)
	if err != nil {
		t.Fatal(err)
	}
	after, err := projectsOf(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !kept(r, otherID, []string{"e--web-app"}, after) {
		t.Error("the Windows bucket is not kept once the Mac is forgotten")
	}
	if !kept(r, otherID, []string{"-Users-you-dev-web-app"}, after) {
		t.Error("the forgotten Mac's bucket is not kept")
	}
}

// `config keep .` in the project's folder keeps it.
func TestConfigKeepRelativePath(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	claudeDir, dest := keepFixture(t, map[string]archive.Project{
		"here": {Path: cwd, Leaf: filepath.Base(cwd), Machine: "LAP-2"},
	})
	if err := cmdConfig([]string{"--claude-dir", claudeDir, "keep", "."}); err != nil {
		t.Fatal(err)
	}
	want := []archive.KeepEntry{{Leaf: strings.ToLower(filepath.Base(cwd))}}
	if r, _ := archive.LoadRetention(dest); !reflect.DeepEqual(r.Keep, want) {
		t.Errorf("keep list %+v, want %+v", r.Keep, want)
	}
}

// A retention.json that cannot be read is never overwritten: it may hold the keep list.
func TestConfigRefusesAnInvalidRetentionFile(t *testing.T) {
	claudeDir, dest := keepFixture(t)
	const bad = `{"keep": [`
	if err := os.WriteFile(archive.RetentionPath(dest), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"keep"}, {"keep", "web-app"}, {"keep", keptID}, {"unkeep", "web-app"}, {"keep-days"}, {"keep-days", "365"}, {"keep-days", "off"}} {
		if err := cmdConfig(append([]string{"--claude-dir", claudeDir}, args...)); err == nil {
			t.Errorf("config %v succeeded with an invalid retention.json", args)
		}
	}
	if got, _ := os.ReadFile(archive.RetentionPath(dest)); string(got) != bad {
		t.Errorf("retention.json was rewritten: %q", got)
	}
}

func TestConfigKeepDaysMinimum(t *testing.T) {
	claudeDir, dest := keepFixture(t)
	if err := cmdConfig([]string{"--claude-dir", claudeDir, "keep-days", "30"}); err == nil || err.Error() != "keep-days must be at least 90" {
		t.Errorf("keep-days 30: %v", err)
	}
	if err := cmdConfig([]string{"--claude-dir", claudeDir, "keep-days", "200000"}); err == nil {
		t.Error("keep-days 200000 saved")
	}
	for arg, want := range map[string]int{"365": 365, "off": 0} {
		if err := cmdConfig([]string{"--claude-dir", claudeDir, "keep-days", arg}); err != nil {
			t.Fatalf("keep-days %s: %v", arg, err)
		}
		if r, err := archive.LoadRetention(dest); err != nil || r.KeepDays != want {
			t.Errorf("keep-days %s saved %+v, %v", arg, r, err)
		}
	}
}

func TestConfigKeepAndUnkeep(t *testing.T) {
	claudeDir, dest := keepFixture(t)
	config := func(args ...string) error {
		return cmdConfig(append([]string{"--claude-dir", claudeDir}, args...))
	}
	keep := func() []archive.KeepEntry {
		r, err := archive.LoadRetention(dest)
		if err != nil {
			t.Fatal(err)
		}
		return r.Keep
	}

	for _, arg := range []string{"web-app", `E:\web-app`, keptID, strings.ToUpper(keptID)} {
		if err := config("keep", arg); err != nil {
			t.Fatalf("keep %s: %v", arg, err)
		}
	}
	if want := []archive.KeepEntry{{Leaf: "web-app"}, {Session: keptID}}; !reflect.DeepEqual(keep(), want) {
		t.Errorf("keep list %+v, want %+v: one entry each", keep(), want)
	}
	if err := config("keep", "app"); err == nil {
		t.Error("keep app matched web-app")
	}
	if err := config("keep"); err != nil {
		t.Errorf("listing: %v", err)
	}
	if err := config("unkeep", "/Users/you/dev/web-app"); err != nil {
		t.Fatal(err)
	}
	if err := config("unkeep", keptID); err != nil {
		t.Fatal(err)
	}
	if got := keep(); len(got) != 0 {
		t.Errorf("after unkeep: %+v", got)
	}
	if err := config("unkeep", "web-app"); err == nil {
		t.Error("unkeep of something not kept succeeded")
	}
}

// keepFixture is a profile whose archive holds the manifests of the projects given,
// twoMachines' by default.
func keepFixture(t *testing.T, given ...map[string]archive.Project) (claudeDir, dest string) {
	t.Helper()
	root := t.TempDir()
	claudeDir, dest = filepath.Join(root, ".claude"), filepath.Join(root, "archive")
	all := twoMachines()
	if len(given) > 0 {
		all = given[0]
	}
	byMachine := map[string]map[string]archive.Project{}
	for bucket, p := range all {
		if byMachine[p.Machine] == nil {
			byMachine[p.Machine] = map[string]archive.Project{}
		}
		byMachine[p.Machine][bucket] = p
	}
	for machine, projects := range byMachine {
		writeJSON(t, filepath.Join(archive.ManifestDir(dest), machine+".json"),
			archive.Shard{SchemaVersion: archive.SchemaVersion, Machine: machine, Projects: projects})
	}
	writeJSON(t, archive.ConfigPath(claudeDir), map[string]string{"destination": dest})
	return claudeDir, dest
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0o755)
	}
	if err == nil {
		err = os.WriteFile(path, data, 0o644)
	}
	if err != nil {
		t.Fatal(err)
	}
}

const (
	oldWeb = "aaaaaaaa-1111-4111-8111-000000000001" // web-app on the Mac, 200 days idle
	newWeb = "aaaaaaaa-1111-4111-8111-000000000002" // web-app on LAP-2, 10 days
	oldAPI = "aaaaaaaa-1111-4111-8111-000000000003" // api on the Mac, 200 days
)

// pruneFixture is keepFixture's archive with three conversations, every shard
// honouring the deleted list, run as the Mac.
func pruneFixture(t *testing.T) (claudeDir, dest string) {
	t.Helper()
	claudeDir, dest = keepFixture(t)
	honour(t, dest, "you-mac", true)
	honour(t, dest, "LAP-2", true)
	t.Setenv("CLAUDE_SESSIONS_MACHINE", "you-mac")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	now := time.Now()
	conv(t, dest, "-Users-you-dev-web-app", oldWeb, now.AddDate(0, 0, -200), "w1", "w2")
	conv(t, dest, "e--web-app", newWeb, now.AddDate(0, 0, -10), "n1")
	conv(t, dest, "-Users-you-dev-api", oldAPI, now.AddDate(0, 0, -200), "p1")
	return claudeDir, dest
}

// honour sets or clears machine's honorsDeleted.
func honour(t *testing.T, dest, machine string, on bool) {
	t.Helper()
	shards, _, err := archive.ReadShards(dest)
	if err != nil {
		t.Fatal(err)
	}
	s := shards[machine]
	s.HonorsDeleted = on
	writeJSON(t, filepath.Join(archive.ManifestDir(dest), machine+".json"), s)
}

// conv archives a transcript of id in bucket, its turns a minute apart from at, its
// modification time its last turn's.
func conv(t *testing.T, dest, bucket, id string, at time.Time, uuids ...string) string {
	t.Helper()
	p := filepath.Join(archive.ProjectsDir(dest), bucket, id+".jsonl")
	writeFile(t, p, turns(at, uuids...))
	last := at.Add(time.Duration(len(uuids)-1) * time.Minute)
	if err := os.Chtimes(p, last, last); err != nil {
		t.Fatal(err)
	}
	return p
}

func turns(at time.Time, uuids ...string) string {
	var b strings.Builder
	for i, u := range uuids {
		fmt.Fprintf(&b, `{"type":"user","uuid":%q,"timestamp":%q,"cwd":"/Users/you/dev/web-app","message":{"role":"user","content":"prompt %s"}}`+"\n",
			u, at.Add(time.Duration(i)*time.Minute).UTC().Format(time.RFC3339Nano), u)
	}
	return b.String()
}

// tree is every file under dir and its content.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = string(raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// captured runs f with stdout and stderr captured.
func captured(t *testing.T, f func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	ferr := f()
	os.Stdout, os.Stderr = stdout, stderr
	w.Close()
	return <-done, ferr
}

func prune(t *testing.T, claudeDir string, args ...string) (string, error) {
	t.Helper()
	return captured(t, func() error { return cmdPrune(append([]string{"--claude-dir", claudeDir}, args...)) })
}

func planned(t *testing.T, claudeDir, dest string, sel pruneSel, r archive.Retention) []string {
	t.Helper()
	plan, err := planPrune(claudeDir, dest, sel, r, time.Now())
	if err != nil {
		t.Fatalf("%+v: %v", sel, err)
	}
	ids := []string{}
	for _, it := range plan.Remove {
		ids = append(ids, it.ID)
	}
	sort.Strings(ids)
	return ids
}

func TestPruneNeedsASelector(t *testing.T) {
	claudeDir, dest := pruneFixture(t)
	before := tree(t, dest)
	for _, args := range [][]string{nil, {"--yes"}} {
		if _, err := prune(t, claudeDir, args...); err == nil || err.Error() != "prune needs --older-than, --project or a conversation id" {
			t.Errorf("prune %v: %v", args, err)
		}
	}
	if !reflect.DeepEqual(tree(t, dest), before) {
		t.Error("prune with no selector changed the archive")
	}
}

func TestPruneListsWithoutYes(t *testing.T) {
	claudeDir, dest := pruneFixture(t)
	before := tree(t, dest)
	out, err := prune(t, claudeDir, "--older-than", "100d")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/Users/you/dev/web-app", "/Users/you/dev/api", "prompt w1", "prompt p1",
		time.Now().AddDate(0, 0, -200).Format("2006-01-02"), "2 conversation(s)", "0 kept (on the keep list)", "Run with --yes to delete them"} {
		if !strings.Contains(out, want) {
			t.Errorf("the listing has no %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "prompt n1") {
		t.Errorf("the listing has the 10-day-old conversation:\n%s", out)
	}
	if !reflect.DeepEqual(tree(t, dest), before) {
		t.Error("prune without --yes changed the archive")
	}
}

func TestPruneSelectorsAnd(t *testing.T) {
	claudeDir, dest := pruneFixture(t)
	for _, c := range []struct {
		sel  pruneSel
		want []string
	}{
		{pruneSel{OlderThan: 100, Projects: []string{"web-app"}}, []string{oldWeb}},
		{pruneSel{OlderThan: 100}, []string{oldWeb, oldAPI}},
		{pruneSel{Projects: []string{"web-app"}}, []string{oldWeb, newWeb}},
		{pruneSel{Projects: []string{`E:\web-app`}}, []string{oldWeb, newWeb}},
		{pruneSel{OlderThan: 100, Projects: []string{"web-app", "api"}}, []string{oldWeb, oldAPI}},
		{pruneSel{IDs: []string{newWeb, oldAPI}, Projects: []string{"web-app"}}, []string{newWeb}},
		{pruneSel{IDs: []string{newWeb}, OlderThan: 100}, []string{}},
		{pruneSel{OlderThan: 5}, []string{oldWeb, newWeb, oldAPI}},
	} {
		if got := planned(t, claudeDir, dest, c.sel, archive.Retention{}); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%+v: %v, want %v", c.sel, got, c.want)
		}
	}
	if _, err := planPrune(claudeDir, dest, pruneSel{Projects: []string{"app"}}, archive.Retention{}, time.Now()); err == nil {
		t.Error("--project app matched web-app")
	}

	// This machine's own copy counts: continued here, it is not old.
	local := filepath.Join(claudeDir, "projects", "-Users-you-dev-web-app", oldWeb+".jsonl")
	writeFile(t, local, turns(time.Now().AddDate(0, 0, -200), "w1", "w2")+turns(time.Now().AddDate(0, 0, -3), "w3"))
	if got := planned(t, claudeDir, dest, pruneSel{OlderThan: 100}, archive.Retention{}); !reflect.DeepEqual(got, []string{oldAPI}) {
		t.Errorf("with a newer local copy: %v, want only %s", got, oldAPI)
	}
}

func TestPruneHonoursKeepList(t *testing.T) {
	claudeDir, dest := pruneFixture(t)
	if _, err := captured(t, func() error { return cmdConfig([]string{"--claude-dir", claudeDir, "keep", "web-app"}) }); err != nil {
		t.Fatal(err)
	}
	out, err := prune(t, claudeDir, "--older-than", "100d")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1 kept (on the keep list)") || strings.Contains(out, "prompt w1") || !strings.Contains(out, "prompt p1") {
		t.Errorf("listing:\n%s", out)
	}
	if out, err := prune(t, claudeDir, "--older-than", "100d", "--yes"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if archivedCopy(dest, "-Users-you-dev-web-app", oldWeb) == "" {
		t.Error("the kept project's old conversation was deleted")
	}
	if archivedCopy(dest, "-Users-you-dev-api", oldAPI) != "" {
		t.Error("the old conversation of a project not kept was not deleted")
	}
}

func TestPruneDeletesEveryCopy(t *testing.T) {
	claudeDir, dest := pruneFixture(t)
	const x = "aaaaaaaa-1111-4111-8111-00000000000a"
	now := time.Now()
	macDir := filepath.Join(archive.ProjectsDir(dest), "-Users-you-dev-web-app")
	winDir := filepath.Join(archive.ProjectsDir(dest), "e--web-app")
	conv(t, dest, "-Users-you-dev-web-app", x, now.AddDate(0, 0, -50), "x1", "x2", "x3")
	writeFile(t, filepath.Join(macDir, x, "subagents", "agent-a1.jsonl"), turns(now.AddDate(0, 0, -50), "s1"))
	conv(t, dest, "e--web-app", x, now.AddDate(0, 0, -60), "x1", "x2")
	writeFile(t, filepath.Join(winDir, x, "tool-results", "r1.txt"), "output\n")
	if _, err := convzip.Zip(winDir, x); err != nil {
		t.Fatal(err)
	}
	writeFile(t, pagePath(dest, "-Users-you-dev-web-app", x), "<html></html>")
	writeFile(t, pagePath(dest, "e--web-app", x), "<html></html>")
	writeFile(t, pagePath(dest, "-Users-you-dev-web-app", oldWeb), "<html></html>")
	writeFile(t, filepath.Join(dest, "forks", "-Users-you-dev-web-app", x, "LAP-2.jsonl"), turns(now.AddDate(0, 0, -55), "x1", "x2", "f1"))
	if zippedCopy(dest, "e--web-app", x) == "" {
		t.Fatal("no zipped copy to delete")
	}
	before := tree(t, dest)

	if out, err := prune(t, claudeDir, x, "--yes"); err != nil || !strings.Contains(out, "Deleted 1 conversation") || !strings.Contains(out, "trash") {
		t.Fatalf("%v\n%s", err, out)
	}
	after := tree(t, dest)
	for rel, content := range before {
		if strings.Contains(rel, x) {
			if _, ok := after[rel]; ok {
				t.Errorf("%s is still there", rel)
			}
		} else if after[rel] != content {
			t.Errorf("%s changed", rel)
		}
	}
	for rel := range after {
		if _, ok := before[rel]; !ok && rel != "deleted/you-mac.json" {
			t.Errorf("%s appeared", rel)
		}
	}
	for _, d := range []string{filepath.Join(macDir, x), filepath.Join(winDir, x), filepath.Join(dest, "forks", "-Users-you-dev-web-app", x)} {
		if _, err := os.Lstat(d); err == nil {
			t.Errorf("%s is still there", d)
		}
	}

	d, err := archive.LoadDeleted(dest)
	if err != nil {
		t.Fatal(err)
	}
	e := d[x]
	lastTurn := now.AddDate(0, 0, -50).Add(2 * time.Minute)
	if e.LastUUID != "x3" || !e.LastAt.Equal(lastTurn) || e.Undone ||
		!reflect.DeepEqual(e.Buckets, []string{"-Users-you-dev-web-app", "e--web-app"}) || time.Since(e.DeletedAt) > time.Minute {
		t.Errorf("deleted entry %+v, want x3 at %v in both buckets", e, lastTurn)
	}
}

func TestPruneWaitsForOlderShard(t *testing.T) {
	claudeDir, dest := pruneFixture(t)
	honour(t, dest, "LAP-2", false)
	before := tree(t, dest)
	out, err := prune(t, claudeDir, "--older-than", "100d", "--yes")
	if err == nil || !strings.Contains(out+err.Error(), "LAP-2 runs a claude-sessions too old") || strings.Contains(out+err.Error(), "you-mac") {
		t.Errorf("an older build's shard did not stop prune, by name: %v\n%s", err, out)
	}
	if !reflect.DeepEqual(tree(t, dest), before) {
		t.Error("prune deleted while a shard lacks honorsDeleted")
	}

	// A manifest that cannot be read stops it too.
	honour(t, dest, "LAP-2", true)
	writeFile(t, filepath.Join(archive.ManifestDir(dest), "LAP-3.json"), "{")
	before = tree(t, dest)
	out, err = prune(t, claudeDir, "--older-than", "100d", "--yes")
	if err == nil || !strings.Contains(out+err.Error(), "LAP-3.json") {
		t.Errorf("an unreadable manifest did not stop prune: %v\n%s", err, out)
	}
	if !reflect.DeepEqual(tree(t, dest), before) {
		t.Error("prune deleted with a manifest it cannot read")
	}
}

func TestPruneSkipsUndatable(t *testing.T) {
	claudeDir, dest := pruneFixture(t)
	const u = "aaaaaaaa-1111-4111-8111-00000000000b"
	p := filepath.Join(archive.ProjectsDir(dest), "-Users-you-dev-api", u+".jsonl")
	// No turn time, and a modification time still to come - a wrong clock - to fall
	// back to: it cannot be dated.
	writeFile(t, p, `{"type":"user","uuid":"q1","message":{"role":"user","content":"no time"}}`+"\n")
	future := time.Now().Add(48 * time.Hour)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}
	if got := planned(t, claudeDir, dest, pruneSel{OlderThan: 1}, archive.Retention{}); !reflect.DeepEqual(got, []string{oldWeb, newWeb, oldAPI}) {
		t.Errorf("%v: want only the dated ones", got)
	}
	// Named, it can be deleted - recorded with no time: a LastAt still to come would make
	// push skip the turns a computer adds before then.
	plan, err := planPrune(claudeDir, dest, pruneSel{IDs: []string{u}}, archive.Retention{}, time.Now())
	if err != nil || len(plan.Remove) != 1 || plan.Remove[0].ID != u || !plan.Remove[0].LastAt.IsZero() {
		t.Errorf("by id: %+v %v", plan.Remove, err)
	}

	// Nor is a conversation whose every copy sits in a bucket no manifest lists: no keep
	// entry could name it.
	conv(t, dest, "-Users-you-dev-gone", "aaaaaaaa-1111-4111-8111-00000000000c", time.Now().AddDate(0, 0, -400), "g1")
	if got := planned(t, claudeDir, dest, pruneSel{OlderThan: 100}, archive.Retention{}); !reflect.DeepEqual(got, []string{oldWeb, oldAPI}) {
		t.Errorf("with an unlisted bucket: %v", got)
	}

	// One copy that cannot be dated may hold the newest turn: the old copy beside it
	// does not date the conversation.
	second := filepath.Join(archive.ProjectsDir(dest), "-Users-you-dev-web-app", oldAPI+".jsonl")
	writeFile(t, second, `{"type":"user","uuid":"p2","message":{"role":"user","content":"no time"}}`+"\n")
	if err := os.Chtimes(second, future, future); err != nil {
		t.Fatal(err)
	}
	if got := planned(t, claudeDir, dest, pruneSel{OlderThan: 100}, archive.Retention{}); !reflect.DeepEqual(got, []string{oldWeb}) {
		t.Errorf("with an undatable second copy: %v", got)
	}
}

// A transcript with no turn time is dated by its modification time.
func TestPruneDatesByModTimeWithoutTurnTimes(t *testing.T) {
	claudeDir, dest := pruneFixture(t)
	const u = "aaaaaaaa-1111-4111-8111-00000000000e"
	p := filepath.Join(archive.ProjectsDir(dest), "-Users-you-dev-api", u+".jsonl")
	writeFile(t, p, `{"type":"user","uuid":"q1","message":{"role":"user","content":"no time here"}}`+"\n")
	old := time.Now().AddDate(0, 0, -200)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	out, err := prune(t, claudeDir, "--older-than", "100d")
	if err != nil || !strings.Contains(out, "no time here") || !strings.Contains(out, "3 conversation(s)") {
		t.Errorf("the old transcript with no turn time is not listed: %v\n%s", err, out)
	}
	plan, err := planPrune(claudeDir, dest, pruneSel{OlderThan: 100}, archive.Retention{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	for _, it := range plan.Remove {
		if it.ID == u && (!it.LastAt.Equal(st.ModTime()) || it.LastUUID != "q1") {
			t.Errorf("dated %v (%s), want its modification time %v and q1", it.LastAt, it.LastUUID, st.ModTime())
		}
	}
}

func TestPruneUndo(t *testing.T) {
	claudeDir, dest := pruneFixture(t)
	if out, err := prune(t, claudeDir, oldAPI, "--yes"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if d, _ := archive.LoadDeleted(dest); d[oldAPI].Undone || d[oldAPI].ID == "" {
		t.Fatalf("not recorded: %+v", d)
	}
	out, err := prune(t, claudeDir, "--undo", oldAPI)
	if err != nil || !strings.Contains(out, "next push") {
		t.Fatalf("%v\n%s", err, out)
	}
	d, err := archive.LoadDeleted(dest)
	if err != nil || !d[oldAPI].Undone {
		t.Errorf("after --undo: %+v %v", d, err)
	}
	if _, err := prune(t, claudeDir, "--undo", "--older-than", "100d"); err == nil {
		t.Error("--undo took a selector other than ids")
	}
}

func TestIndexLeavesOutPruned(t *testing.T) {
	claudeDir, dest := pruneFixture(t)
	repo := filepath.Join(t.TempDir(), "web-app")
	bucket := filepath.Join(claudeDir, "projects", claude.Slug(repo))
	const x, y = "aaaaaaaa-1111-4111-8111-0000000000d1", "aaaaaaaa-1111-4111-8111-0000000000d2"
	for _, id := range []string{x, y} {
		writeFile(t, filepath.Join(bucket, id+".jsonl"), strings.ReplaceAll(turns(time.Now().Add(-time.Hour), id[len(id)-2:]), "/Users/you/dev/web-app", filepath.ToSlash(repo)))
	}
	push := func() {
		t.Helper()
		if out, err := captured(t, func() error { return runPush(claudeDir, "", pushOpts{NoHTML: true}) }); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	push()
	honour(t, dest, "you-mac", true)
	// Another machine's listing has it too.
	writeJSON(t, filepath.Join(archive.IndexDir(dest), "LAP-2.json"), archive.SessionIndex{SchemaVersion: archive.SchemaVersion, Machine: "LAP-2",
		Sessions: []archive.Session{{Bucket: "e--web-app", Project: `E:\web-app`, ID: x, Updated: time.Now(), Prompt: "from LAP-2"}}})
	if out, err := prune(t, claudeDir, x, "--yes"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	push()
	index, own := mustReadFile(t, filepath.Join(dest, "INDEX.md")), mustReadFile(t, filepath.Join(archive.IndexDir(dest), "you-mac.json"))
	for name, s := range map[string]string{"INDEX.md": index, "index/you-mac.json": own} {
		if strings.Contains(s, x) {
			t.Errorf("%s lists the pruned conversation:\n%s", name, s)
		}
		if !strings.Contains(s, y) {
			t.Errorf("%s does not list the other one:\n%s", name, s)
		}
	}
}

func mustReadFile(t *testing.T, p string) string {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// An undo from a computer whose clock is behind the deletion's still takes it off the
// list, and so does a deletion again over an undo from a clock ahead.
func TestPruneUndoBeatsAClockAhead(t *testing.T) {
	claudeDir, dest := pruneFixture(t)
	ahead := time.Now().Add(10 * time.Minute)
	if err := archive.RecordDeleted(dest, "LAP-2", []archive.DeletedEntry{{ID: oldAPI, DeletedAt: ahead}, {ID: oldWeb, DeletedAt: ahead, Undone: true}}); err != nil {
		t.Fatal(err)
	}
	if out, err := prune(t, claudeDir, "--undo", oldAPI); err != nil || !strings.Contains(out, "Off the deleted list") {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, err := prune(t, claudeDir, oldWeb, "--yes"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	d, err := archive.LoadDeleted(dest)
	if err != nil || !d[oldAPI].Undone || d[oldWeb].Undone {
		t.Errorf("undo %+v, delete again %+v (%v): the clock ahead won", d[oldAPI], d[oldWeb], err)
	}
}

// A conversation continued after it was pruned is listed again: a row is left out by
// the rule push skips a local copy by, applied to its archived copy, never by when it
// was deleted - a clock ahead there, or behind here, would hide one push archived again.
func TestIndexListsAConversationContinuedAfterPruning(t *testing.T) {
	_, dest := pruneFixture(t)
	old := time.Now().AddDate(0, 0, -200)
	ahead := time.Now().Add(48 * time.Hour) // the computer that pruned them runs ahead
	// oldAPI continued since by a computer whose clock is behind: its new turn is dated
	// before the last one recorded, but comes after it.
	conv(t, dest, "-Users-you-dev-api", oldAPI, old, "p0", "p1")
	if err := archive.RecordDeleted(dest, "you-mac", []archive.DeletedEntry{
		{ID: oldWeb, LastUUID: "w2", LastAt: old.Add(time.Minute), DeletedAt: ahead},
		{ID: strings.ToUpper(oldAPI), LastUUID: "p0", LastAt: old.Add(time.Hour), DeletedAt: ahead},
		{ID: newWeb, LastUUID: "n1", LastAt: time.Now(), DeletedAt: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}
	if err := archive.RecordDeleted(dest, "LAP-2", []archive.DeletedEntry{{ID: newWeb, DeletedAt: time.Now().Add(time.Minute), Undone: true}}); err != nil {
		t.Fatal(err)
	}
	rows := withoutPruned(dest, []archive.Session{
		{Bucket: "-Users-you-dev-web-app", ID: oldWeb}, // its copy as pruned, back
		{Bucket: "e--web-app", ID: oldWeb},             // pruned, no copy left there
		{Bucket: "-Users-you-dev-api", ID: oldAPI},     // continued since
		{Bucket: "e--web-app", ID: newWeb},             // undone
		{Bucket: "-Users-you-dev-api", ID: keptID},     // never pruned
	})
	var got []string
	for _, r := range rows {
		got = append(got, r.Bucket+"/"+r.ID)
	}
	if want := []string{"-Users-you-dev-api/" + oldAPI, "e--web-app/" + newWeb, "-Users-you-dev-api/" + keptID}; !reflect.DeepEqual(got, want) {
		t.Errorf("rows %v, want %v", got, want)
	}
}

// symlink makes link point at target, or skips the test where links cannot be made.
func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot make a link here: %v", err)
	}
}

// forks/<bucket> and html/<bucket> planted as links to folders outside the archive:
// those conversations are refused and not recorded, nothing outside is touched, and
// the rest are deleted.
func TestPruneRefusesLinkedForksAndPages(t *testing.T) {
	claudeDir, dest := pruneFixture(t)
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "forks", oldWeb, "LAP-2.jsonl"), turns(time.Now().AddDate(0, 0, -1), "w1"))
	writeFile(t, filepath.Join(outside, "html", newWeb+".html"), "<html>outside</html>")
	symlink(t, filepath.Join(outside, "forks"), filepath.Join(dest, "forks", "-Users-you-dev-web-app"))
	symlink(t, filepath.Join(outside, "html"), filepath.Join(dest, "html", "e--web-app"))
	before := tree(t, outside)
	// A fork read through the link would date it a day old.
	if got := planned(t, claudeDir, dest, pruneSel{OlderThan: 100}, archive.Retention{}); !reflect.DeepEqual(got, []string{oldWeb, oldAPI}) {
		t.Errorf("dated through a link: %v", got)
	}

	out, err := prune(t, claudeDir, oldWeb, newWeb, oldAPI, "--yes")
	if err == nil || !strings.Contains(out, "a link stands on the way") {
		t.Errorf("links were not refused: %v\n%s", err, out)
	}
	if !reflect.DeepEqual(tree(t, outside), before) {
		t.Error("a file outside the archive changed")
	}
	if archivedCopy(dest, "-Users-you-dev-web-app", oldWeb) == "" || archivedCopy(dest, "e--web-app", newWeb) == "" {
		t.Error("a refused conversation was deleted")
	}
	if archivedCopy(dest, "-Users-you-dev-api", oldAPI) != "" {
		t.Error("the conversation with no link on its way was not deleted")
	}
	d, _ := archive.LoadDeleted(dest)
	if _, ok := d[oldWeb]; ok || d[newWeb].ID != "" || d[oldAPI].ID == "" {
		t.Errorf("recorded %+v: want only %s", d, oldAPI)
	}
}

// This machine's deleted list cannot be read: nothing is deleted, and it is not
// written over.
func TestPruneUnreadableDeletedList(t *testing.T) {
	claudeDir, dest := pruneFixture(t)
	writeFile(t, filepath.Join(archive.DeletedDir(dest), "you-mac.json"), `{"entries": [`)
	before := tree(t, dest)
	out, err := prune(t, claudeDir, "--older-than", "100d", "--yes")
	if err == nil || !strings.Contains(err.Error(), "nothing deleted: the deleted list cannot be read") {
		t.Errorf("%v\n%s", err, out)
	}
	if !reflect.DeepEqual(tree(t, dest), before) {
		t.Error("prune changed the archive with a deleted list it cannot read")
	}
}

// The deleted list holds the entry before the first path is deleted.
func TestPruneRecordsBeforeDeleting(t *testing.T) {
	claudeDir, dest := pruneFixture(t)
	calls, recorded := 0, false
	real := removePath
	removePath = func(string, bool) error {
		calls++
		d, _ := archive.LoadDeleted(dest)
		recorded = d[oldAPI].ID == oldAPI && !d[oldAPI].Undone
		return errors.New("refused by the test")
	}
	defer func() { removePath = real }()
	out, err := prune(t, claudeDir, oldAPI, "--yes")
	if err == nil || calls != 1 || !recorded || !strings.Contains(out, "refused by the test") || strings.Contains(out, "Deleted") {
		t.Errorf("calls %d, recorded first %v, %v\n%s", calls, recorded, err, out)
	}
	if archivedCopy(dest, "-Users-you-dev-api", oldAPI) == "" {
		t.Error("the transcript went although its delete failed")
	}
}

// A conversation gone by the time it is deleted - or named in a case this file system
// does not find - is reported, not counted and not recorded.
func TestPruneReportsAConversationGone(t *testing.T) {
	claudeDir, dest := pruneFixture(t)
	var failed []string
	plan, err := planPrune(claudeDir, dest, pruneSel{IDs: []string{oldAPI}}, archive.Retention{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	plan.Remove = append(plan.Remove, pruneItem{ID: "aaaaaaaa-1111-4111-8111-0000000000ff", Buckets: []string{"-Users-you-dev-api"}})
	n, _, err := applyPrune(dest, plan, time.Now(), func(it pruneItem, err error) { failed = append(failed, it.ID+": "+err.Error()) })
	if n != 1 || err == nil || len(failed) != 1 || !strings.Contains(failed[0], "no copy of it is in the archive") {
		t.Errorf("deleted %d, %v, failed %v", n, err, failed)
	}
	if d, _ := archive.LoadDeleted(dest); len(d) != 1 || d[oldAPI].ID == "" {
		t.Errorf("recorded %+v", d)
	}
}

// A conversation given a newer turn between the listing and the deletion is skipped.
func TestPruneSkipsAConversationContinuedSinceListed(t *testing.T) {
	claudeDir, dest := pruneFixture(t)
	plan, err := planPrune(claudeDir, dest, pruneSel{OlderThan: 100}, archive.Retention{}, time.Now())
	if err != nil || len(plan.Remove) != 2 {
		t.Fatalf("%+v %v", plan.Remove, err)
	}
	p := archivedCopy(dest, "-Users-you-dev-web-app", oldWeb)
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(turns(time.Now().AddDate(0, 0, -1), "w3"))
	f.Close()
	var failed []string
	n, _, err := applyPrune(dest, plan, time.Now(), func(it pruneItem, err error) { failed = append(failed, it.ID+": "+err.Error()) })
	if n != 1 || err == nil || len(failed) != 1 || !strings.Contains(failed[0], oldWeb+": it changed since it was listed") {
		t.Errorf("deleted %d, %v, failed %v", n, err, failed)
	}
	if archivedCopy(dest, "-Users-you-dev-web-app", oldWeb) == "" {
		t.Error("the continued conversation was deleted")
	}
	if d, _ := archive.LoadDeleted(dest); d[oldWeb].ID != "" {
		t.Errorf("the continued conversation was recorded: %+v", d[oldWeb])
	}

	// Named by id, any turn newer than was listed stops it too, however old.
	plan, err = planPrune(claudeDir, dest, pruneSel{IDs: []string{oldWeb}}, archive.Retention{}, time.Now())
	if err != nil || len(plan.Remove) != 1 {
		t.Fatalf("%+v %v", plan.Remove, err)
	}
	f, err = os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(turns(time.Now(), "w4"))
	f.Close()
	if n, _, err := applyPrune(dest, plan, time.Now().Add(time.Minute), func(pruneItem, error) {}); n != 0 || err == nil {
		t.Errorf("by id: deleted %d, %v", n, err)
	}
	// ...and so does a turn from a clock ahead, which cannot be dated...
	if plan, err = planPrune(claudeDir, dest, pruneSel{IDs: []string{newWeb}}, archive.Retention{}, time.Now()); err != nil || len(plan.Remove) != 1 {
		t.Fatalf("%+v %v", plan.Remove, err)
	}
	q := archivedCopy(dest, "e--web-app", newWeb)
	writeFile(t, q, mustReadFile(t, q)+turns(time.Now().Add(time.Hour), "n9"))
	if n, _, err := applyPrune(dest, plan, time.Now(), func(pruneItem, error) {}); n != 0 || err == nil {
		t.Errorf("a turn from a clock ahead: deleted %d, %v", n, err)
	}
	// ...and a copy arriving in a bucket that was not listed, however old.
	if plan, err = planPrune(claudeDir, dest, pruneSel{IDs: []string{oldWeb}}, archive.Retention{}, time.Now()); err != nil || len(plan.Remove) != 1 {
		t.Fatalf("%+v %v", plan.Remove, err)
	}
	conv(t, dest, "-Users-you-dev-api", oldWeb, time.Now().AddDate(0, 0, -300), "w1")
	if n, _, err := applyPrune(dest, plan, time.Now(), func(pruneItem, error) {}); n != 0 || err == nil {
		t.Errorf("a copy in a new bucket: deleted %d, %v", n, err)
	}
}

func TestPruneRefusalMessages(t *testing.T) {
	claudeDir, dest := pruneFixture(t)
	before := tree(t, dest)
	for args, want := range map[string]string{
		"web-app":                        `"web-app" is not a conversation id (a project goes with --project)`,
		"--older-than 100":               "--older-than takes a number of days from 1 to 36500, such as 365d",
		"--older-than 0d":                "--older-than takes a number of days from 1 to 36500, such as 365d",
		"--older-than 36501d":            "--older-than takes a number of days from 1 to 36500, such as 365d",
		"--undo":                         "usage: claude-sessions prune --undo <conversation id>...",
		"--undo --project api " + oldAPI: "usage: claude-sessions prune --undo <conversation id>...",
	} {
		if _, err := prune(t, claudeDir, strings.Fields(args)...); err == nil || err.Error() != want {
			t.Errorf("prune %s: %v, want %q", args, err, want)
		}
	}
	if !reflect.DeepEqual(tree(t, dest), before) {
		t.Error("a refused prune changed the archive")
	}
}

// --- push honours the deleted list; the age rule ---

const prunedX = "aaaaaaaa-1111-4111-8111-0000000000e1"

// localConv writes this computer's transcript of id in a project at repo, its turns a
// minute apart from at, its modification time its last turn's.
func localConv(t *testing.T, claudeDir, repo, id string, at time.Time, uuids ...string) string {
	t.Helper()
	p := filepath.Join(claudeDir, "projects", claude.Slug(repo), id+".jsonl")
	writeFile(t, p, "")
	appendTurn(t, p, repo, at, uuids...)
	return p
}

// appendTurn adds turns to the transcript at p, a minute apart from at, and stamps it
// with the last one's time.
func appendTurn(t *testing.T, p, repo string, at time.Time, uuids ...string) {
	t.Helper()
	writeFile(t, p, mustReadFile(t, p)+strings.ReplaceAll(turns(at, uuids...), "/Users/you/dev/web-app", filepath.ToSlash(repo)))
	last := at.Add(time.Duration(len(uuids)-1) * time.Minute)
	if err := os.Chtimes(p, last, last); err != nil {
		t.Fatal(err)
	}
}

func pushHere(t *testing.T, claudeDir string, opts pushOpts) {
	t.Helper()
	opts.NoHTML = true
	if err := os.MkdirAll(filepath.Join(claudeDir, "projects"), 0o755); err != nil { // as Claude Code makes it
		t.Fatal(err)
	}
	if out, err := captured(t, func() error { return runPush(claudeDir, "", opts) }); err != nil {
		t.Fatalf("push %+v: %v\n%s", opts, err, out)
	}
}

// copiesOf is every file of id's in the archive's projects/ and forks/.
func copiesOf(t *testing.T, dest, id string) []string {
	t.Helper()
	var out []string
	for rel := range tree(t, dest) {
		if strings.Contains(rel, id) && (strings.HasPrefix(rel, "projects/") || strings.HasPrefix(rel, "forks/")) {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}

// prunedLocally is pruneFixture with prunedX on this computer, 200 days old, pushed,
// then pruned.
func prunedLocally(t *testing.T) (claudeDir, dest, repo, local string) {
	t.Helper()
	claudeDir, dest = pruneFixture(t)
	repo = filepath.Join(t.TempDir(), "web-app")
	local = localConv(t, claudeDir, repo, prunedX, time.Now().AddDate(0, 0, -200), "x1", "x2")
	pushHere(t, claudeDir, pushOpts{})
	if len(copiesOf(t, dest, prunedX)) == 0 {
		t.Fatal("push did not archive it to begin with")
	}
	if out, err := prune(t, claudeDir, prunedX, "--yes"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if c := copiesOf(t, dest, prunedX); len(c) > 0 {
		t.Fatalf("prune left %v", c)
	}
	return claudeDir, dest, repo, local
}

// doctorLine runs doctor and returns its line for the check name.
func doctorLine(t *testing.T, claudeDir, name string) string {
	t.Helper()
	out, _ := captured(t, func() error { return cmdDoctor([]string{"--claude-dir", claudeDir, "--no-write-probe"}) })
	for _, l := range strings.Split(out, "\n") {
		// "  + name  detail", the mark a space for information
		if f := strings.Fields(l); strings.HasPrefix(l, "  ") && len(f) > 1 && (f[0] == name || f[1] == name) {
			return strings.TrimSpace(l)
		}
	}
	t.Fatalf("doctor has no %s line:\n%s", name, out)
	return ""
}

func TestPushSkipsPruned(t *testing.T) {
	claudeDir, dest, _, local := prunedLocally(t)
	for _, opts := range []pushOpts{{}, {Quiet: true}, {Quiet: true, Sweep: true}, {Quiet: true, FromHook: true, Session: local}} {
		pushHere(t, claudeDir, opts)
		if c := copiesOf(t, dest, prunedX); len(c) > 0 {
			t.Fatalf("push %+v archived the pruned conversation again: %v", opts, c)
		}
	}
	for _, f := range []string{"INDEX.md", filepath.Join("index", "you-mac.json")} {
		if s := mustReadFile(t, filepath.Join(dest, f)); strings.Contains(s, prunedX) {
			t.Errorf("%s lists it:\n%s", f, s)
		}
	}
	if l := doctorLine(t, claudeDir, "archived"); !strings.HasPrefix(l, "+") {
		t.Errorf("doctor reports the pruned conversation unarchived: %s", l)
	}
}

func TestPushArchivesAPrunedConversationContinued(t *testing.T) {
	claudeDir, dest, repo, local := prunedLocally(t)
	appendTurn(t, local, repo, time.Now().Add(-time.Hour), "x3")
	pushHere(t, claudeDir, pushOpts{Quiet: true, FromHook: true, Session: local})
	if p := archivedCopy(dest, claude.Slug(repo), prunedX); p == "" || !strings.Contains(mustReadFile(t, p), `"x3"`) {
		t.Fatalf("the hook did not archive the continued conversation: %v", copiesOf(t, dest, prunedX))
	}
	pushHere(t, claudeDir, pushOpts{})
	if s := mustReadFile(t, filepath.Join(dest, "INDEX.md")); !strings.Contains(s, prunedX) {
		t.Errorf("INDEX.md does not list it again:\n%s", s)
	}
}

// Continued on a computer whose clock is behind the one that pruned it: its new turn is
// dated before the last turn recorded, but comes after it, so it is archived and listed.
func TestPushArchivesAContinuationFromAClockBehind(t *testing.T) {
	claudeDir, dest, repo, local := prunedLocally(t)
	d, err := archive.LoadDeleted(dest)
	if err != nil || d[prunedX].LastUUID != "x2" {
		t.Fatalf("%+v %v", d[prunedX], err)
	}
	appendTurn(t, local, repo, d[prunedX].LastAt.Add(-time.Hour), "x3")
	pushHere(t, claudeDir, pushOpts{})
	if p := archivedCopy(dest, claude.Slug(repo), prunedX); p == "" || !strings.Contains(mustReadFile(t, p), `"x3"`) {
		t.Fatalf("not archived: %v", copiesOf(t, dest, prunedX))
	}
	for _, f := range []string{"INDEX.md", filepath.Join("index", "you-mac.json")} {
		if s := mustReadFile(t, filepath.Join(dest, f)); !strings.Contains(s, prunedX) {
			t.Errorf("%s does not list what push archived again:\n%s", f, s)
		}
	}
}

func TestPushAfterUndoArchivesAgain(t *testing.T) {
	claudeDir, dest, _, _ := prunedLocally(t)
	pushHere(t, claudeDir, pushOpts{})
	if c := copiesOf(t, dest, prunedX); len(c) > 0 {
		t.Fatalf("archived before the undo: %v", c)
	}
	if out, err := prune(t, claudeDir, "--undo", prunedX); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	pushHere(t, claudeDir, pushOpts{})
	if len(copiesOf(t, dest, prunedX)) == 0 {
		t.Error("not archived again after --undo")
	}
}

func TestPrunedHere(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().AddDate(0, 0, -100)
	write := func(name, body string, mtime time.Time) string {
		p := filepath.Join(dir, name+".jsonl")
		writeFile(t, p, body)
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return p
	}
	noTime := `{"type":"user","uuid":"q1","message":{"role":"user","content":"no time"}}` + "\n"
	e := archive.DeletedEntry{ID: prunedX, LastUUID: "x2", LastAt: at.Add(time.Minute)}
	for _, c := range []struct {
		name string
		e    archive.DeletedEntry
		path string
		want bool
	}{
		{"as pruned", e, write("same", turns(at, "x1", "x2"), at), true},
		{"behind it", e, write("behind", turns(at, "x1"), at), true},
		{"continued", e, write("more", turns(at, "x1", "x2", "x3"), at), false},
		{"continued by a clock behind", e, write("skewed", turns(at, "x1", "x2")+turns(at.AddDate(0, 0, -5), "x3"), at), false},
		{"another branch, newer", e, write("branch", turns(at, "x1")+turns(at.Add(time.Hour), "y2"), at), false},
		{"no uuid recorded, older", archive.DeletedEntry{ID: prunedX, LastAt: at.Add(time.Minute)}, write("older", turns(at, "x1"), at), true},
		{"no uuid recorded, newer", archive.DeletedEntry{ID: prunedX, LastAt: at}, write("newer", turns(at, "x1", "x2"), at), false},
		{"no turn time, older file", e, write("untimed-old", noTime, at), true},
		{"no turn time, newer file", e, write("untimed-new", noTime, time.Now()), false},
		{"no turn time, file from a clock ahead", e, write("untimed-ahead", noTime, time.Now().Add(48*time.Hour)), false},
		{"undone", archive.DeletedEntry{ID: prunedX, LastUUID: "x2", LastAt: at.Add(time.Minute), Undone: true}, write("undone", turns(at, "x1", "x2"), at), false},
	} {
		if got := prunedHere(map[string]archive.DeletedEntry{prunedX: c.e}, strings.ToUpper(prunedX), c.path); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if prunedHere(map[string]archive.DeletedEntry{}, prunedX, write("unlisted", turns(at, "x1"), at)) {
		t.Error("a conversation not on the list was skipped")
	}
}

// A path that appears between the check and the deletion - here a continuation that
// another computer's push brought in beside a zip - is never deleted unseen.
func TestPruneDeletesOnlyWhatItChecked(t *testing.T) {
	claudeDir, dest := pruneFixture(t)
	dir := filepath.Join(archive.ProjectsDir(dest), "-Users-you-dev-api")
	if _, err := convzip.Zip(dir, oldAPI); err != nil {
		t.Fatal(err)
	}
	plan, err := planPrune(claudeDir, dest, pruneSel{IDs: []string{oldWeb, oldAPI}}, archive.Retention{}, time.Now())
	if err != nil || len(plan.Remove) != 2 || plan.Remove[0].ID != oldWeb {
		t.Fatalf("%+v %v", plan.Remove, err)
	}
	loose := filepath.Join(dir, oldAPI+".jsonl")
	real, arrived := removePath, false
	removePath = func(p string, isDir bool) error {
		if !arrived {
			arrived = true
			writeFile(t, loose, turns(time.Now().AddDate(0, 0, -200), "p1")+turns(time.Now(), "p2"))
		}
		return real(p, isDir)
	}
	defer func() { removePath = real }()
	if n, _, err := applyPrune(dest, plan, time.Now(), func(it pruneItem, err error) { t.Errorf("%s: %v", it.ID, err) }); n != 2 || err != nil {
		t.Errorf("deleted %d, %v", n, err)
	}
	if !arrived || zippedCopy(dest, "-Users-you-dev-api", oldAPI) != "" {
		t.Fatal("the zip checked was not deleted")
	}
	if s, err := os.ReadFile(loose); err != nil || !strings.Contains(string(s), `"p2"`) {
		t.Errorf("the transcript that arrived after the check was deleted: %v", err)
	}
}

// A file's path made a folder since the check is refused, not emptied.
func TestPruneNeverEmptiesAFolderInAFilesPlace(t *testing.T) {
	claudeDir, dest := pruneFixture(t)
	dir := filepath.Join(archive.ProjectsDir(dest), "-Users-you-dev-api")
	writeFile(t, filepath.Join(dir, oldAPI, "tool-results", "r1.txt"), "output\n")
	plan, err := planPrune(claudeDir, dest, pruneSel{IDs: []string{oldAPI}}, archive.Retention{}, time.Now())
	if err != nil || len(plan.Remove) != 1 {
		t.Fatalf("%+v %v", plan.Remove, err)
	}
	transcript, inside := filepath.Join(dir, oldAPI+".jsonl"), filepath.Join(dir, oldAPI+".jsonl", "keep.txt")
	real := removePath
	removePath = func(p string, isDir bool) error {
		if p == filepath.Join(dir, oldAPI) { // its folder goes first; the transcript then turns into one
			if err := os.Remove(transcript); err != nil {
				t.Fatal(err)
			}
			writeFile(t, inside, "not the transcript\n")
		}
		return real(p, isDir)
	}
	defer func() { removePath = real }()
	var failed []string
	if n, _, _ := applyPrune(dest, plan, time.Now(), func(it pruneItem, err error) { failed = append(failed, err.Error()) }); n != 0 || len(failed) != 1 {
		t.Errorf("deleted %d, failed %v", n, failed)
	}
	if _, err := os.Stat(inside); err != nil {
		t.Errorf("the folder in the transcript's place was emptied: %v", err)
	}
}

// Named by id and undated when listed, a conversation given a turn since is skipped: its
// date cannot show it.
func TestPruneSkipsAnUndatedConversationGivenATurn(t *testing.T) {
	claudeDir, dest := pruneFixture(t)
	const u = "aaaaaaaa-1111-4111-8111-00000000000b"
	p := filepath.Join(archive.ProjectsDir(dest), "-Users-you-dev-api", u+".jsonl")
	future := time.Now().Add(48 * time.Hour)
	stamp := func(body string) {
		writeFile(t, p, body)
		if err := os.Chtimes(p, future, future); err != nil {
			t.Fatal(err)
		}
	}
	untimed := func(uuid string) string {
		return `{"type":"user","uuid":"` + uuid + `","message":{"role":"user","content":"no time"}}` + "\n"
	}
	stamp(untimed("q1"))
	plan, err := planPrune(claudeDir, dest, pruneSel{IDs: []string{u}}, archive.Retention{}, time.Now())
	if err != nil || len(plan.Remove) != 1 || plan.Remove[0].dated {
		t.Fatalf("%+v %v", plan.Remove, err)
	}
	stamp(untimed("q1") + untimed("q2"))
	var failed []string
	if n, _, _ := applyPrune(dest, plan, time.Now(), func(it pruneItem, err error) { failed = append(failed, err.Error()) }); n != 0 ||
		len(failed) != 1 || !strings.Contains(failed[0], "it changed since it was listed") {
		t.Errorf("deleted %d, failed %v", n, failed)
	}
	if archivedCopy(dest, "-Users-you-dev-api", u) == "" {
		t.Error("the conversation given a turn was deleted")
	}
}

// ageFixture is pruneFixture with keep-days 90: oldWeb 120 days old, newWeb 10, and no
// oldAPI.
func ageFixture(t *testing.T) (claudeDir, dest string) {
	t.Helper()
	claudeDir, dest = pruneFixture(t)
	if err := os.Remove(archivedCopy(dest, "-Users-you-dev-api", oldAPI)); err != nil {
		t.Fatal(err)
	}
	conv(t, dest, "-Users-you-dev-web-app", oldWeb, time.Now().AddDate(0, 0, -120), "w1", "w2")
	if out, err := captured(t, func() error { return cmdConfig([]string{"--claude-dir", claudeDir, "keep-days", "90"}) }); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return claudeDir, dest
}

func pruneLines(t *testing.T, claudeDir string) []string {
	t.Helper()
	raw, _ := os.ReadFile(filepath.Join(claudeDir, "session-sync.log"))
	var out []string
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.Contains(l, "PRUNE: ") {
			out = append(out, l)
		}
	}
	return out
}

func TestAgeRuleDeletesOldOnce(t *testing.T) {
	claudeDir, dest := ageFixture(t)
	// The first push of this build: it writes its shard before it runs the rule, so it
	// does not wait for itself.
	honour(t, dest, "you-mac", false)
	pushHere(t, claudeDir, pushOpts{})
	if archivedCopy(dest, "-Users-you-dev-web-app", oldWeb) != "" {
		t.Error("the 120-day-old conversation was not deleted")
	}
	if archivedCopy(dest, "e--web-app", newWeb) == "" {
		t.Error("the 10-day-old conversation was deleted")
	}
	want := "PRUNE: " + oldWeb + " (/Users/you/dev/web-app, last message " + time.Now().AddDate(0, 0, -120).Format("2006-01-02") + ", "
	if lines := pruneLines(t, claudeDir); len(lines) != 1 || !strings.Contains(lines[0], want) {
		t.Errorf("want one line with %q, got %q", want, lines)
	}
	if d, _ := archive.LoadDeleted(dest); d[oldWeb].LastUUID != "w2" {
		t.Errorf("not recorded: %+v", d[oldWeb])
	}
	last := mustReadFile(t, pruneLastPath(claudeDir))

	// Within 24 hours it does not run again.
	conv(t, dest, "-Users-you-dev-api", oldAPI, time.Now().AddDate(0, 0, -200), "p1")
	pushHere(t, claudeDir, pushOpts{})
	if archivedCopy(dest, "-Users-you-dev-api", oldAPI) == "" || len(pruneLines(t, claudeDir)) != 1 {
		t.Error("the age rule ran twice within 24 hours")
	}
	if now := mustReadFile(t, pruneLastPath(claudeDir)); now != last {
		t.Errorf("prune-last.json changed without a run: %s -> %s", last, now)
	}

	// A day later it does; so it does when the last run is dated ahead, by a wrong clock.
	for _, at := range []time.Time{time.Now().Add(-25 * time.Hour), time.Now().AddDate(1, 0, 0)} {
		conv(t, dest, "-Users-you-dev-api", oldAPI, time.Now().AddDate(0, 0, -200), "p1")
		writeJSON(t, pruneLastPath(claudeDir), map[string]time.Time{"lastRun": at})
		pushHere(t, claudeDir, pushOpts{})
		if archivedCopy(dest, "-Users-you-dev-api", oldAPI) != "" {
			t.Errorf("last run %v: the age rule did not run again", at)
		}
	}
}

func TestAgeRuleOffByDefault(t *testing.T) {
	claudeDir, dest := pruneFixture(t)
	before := tree(t, archive.ProjectsDir(dest))
	pushHere(t, claudeDir, pushOpts{})
	pushHere(t, claudeDir, pushOpts{Quiet: true, Sweep: true})
	if !reflect.DeepEqual(tree(t, archive.ProjectsDir(dest)), before) || len(pruneLines(t, claudeDir)) > 0 {
		t.Error("push deleted with no keep-days set")
	}
	if _, err := os.Stat(pruneLastPath(claudeDir)); err == nil {
		t.Error("the age rule ran with no keep-days set")
	}
	if l := doctorLine(t, claudeDir, "prune"); !strings.Contains(l, "keep-days off") {
		t.Errorf("doctor: %s", l)
	}
}

func TestAgeRuleWaitsForOlderShard(t *testing.T) {
	claudeDir, dest := ageFixture(t)
	honour(t, dest, "LAP-2", false)
	pushHere(t, claudeDir, pushOpts{})
	if archivedCopy(dest, "-Users-you-dev-web-app", oldWeb) == "" || len(pruneLines(t, claudeDir)) > 0 {
		t.Error("the age rule deleted while LAP-2 lacks honorsDeleted")
	}
	if log := mustReadFile(t, filepath.Join(claudeDir, "session-sync.log")); !strings.Contains(log, "LAP-2 runs a claude-sessions too old") {
		t.Errorf("the log does not say whom it waits for:\n%s", log)
	}
	if l := doctorLine(t, claudeDir, "prune"); !strings.HasPrefix(l, "!") || !strings.Contains(l, "keep-days 90") ||
		!strings.Contains(l, "LAP-2") || strings.Contains(l, "you-mac") || !strings.Contains(l, "last run here") {
		t.Errorf("doctor does not name LAP-2 alone: %s", l)
	}
}

// Zipped conversations are dated by their index alone: the age rule unpacks none.
func TestAgeRuleReadsNoZipContent(t *testing.T) {
	claudeDir, dest := ageFixture(t)
	for _, c := range []struct{ bucket, id string }{{"-Users-you-dev-web-app", oldWeb}, {"e--web-app", newWeb}} {
		dir := filepath.Join(archive.ProjectsDir(dest), c.bucket)
		writeFile(t, filepath.Join(dir, c.id, "tool-results", "r1.txt"), "output\n")
		if _, err := convzip.Zip(dir, c.id); err != nil {
			t.Fatal(err)
		}
	}
	calls, real := 0, materialize
	materialize = func(bucketDir, id, tmp string) (string, func(), error) {
		calls++
		return real(bucketDir, id, tmp)
	}
	defer func() { materialize = real }()
	ret, err := archive.LoadRetention(dest)
	if err != nil {
		t.Fatal(err)
	}
	n, err := runAgeRule(claudeDir, dest, ret, time.Now(), func(string, ...any) {})
	if n != 1 || err != nil || calls != 0 {
		t.Errorf("deleted %d, %v, %d zip(s) unpacked", n, err, calls)
	}
	if zippedCopy(dest, "-Users-you-dev-web-app", oldWeb) != "" || zippedCopy(dest, "e--web-app", newWeb) == "" {
		t.Error("the age rule did not date the zipped copies by their index")
	}
}

// The hooks and a dry run never run the age rule; the sweep does.
func TestHookPushRunsNoAgeRule(t *testing.T) {
	claudeDir, dest := ageFixture(t)
	local := localConv(t, claudeDir, filepath.Join(t.TempDir(), "web-app"), prunedX, time.Now().Add(-time.Hour), "x1")
	for _, opts := range []pushOpts{{Quiet: true, FromHook: true, Session: local}, {Quiet: true}, {DryRun: true}} {
		pushHere(t, claudeDir, opts)
		if archivedCopy(dest, "-Users-you-dev-web-app", oldWeb) == "" {
			t.Fatalf("push %+v ran the age rule", opts)
		}
	}
	if _, err := os.Stat(pruneLastPath(claudeDir)); err == nil {
		t.Error("a hook or a dry run recorded an age-rule run")
	}
	pushHere(t, claudeDir, pushOpts{Quiet: true, Sweep: true})
	if archivedCopy(dest, "-Users-you-dev-web-app", oldWeb) != "" {
		t.Error("the sweep did not run the age rule")
	}
}
