package main

import (
	"encoding/json"
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
	if err == nil || !strings.Contains(out+err.Error(), "LAP-2") || strings.Contains(out+err.Error(), "you-mac") {
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
	writeFile(t, p, `{"type":"user","uuid":"q1","message":{"role":"user","content":"no time"}}`+"\n")
	for _, mtime := range []time.Time{time.Now().Add(48 * time.Hour), time.Now().AddDate(-1, 0, 0)} {
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		if got := planned(t, claudeDir, dest, pruneSel{OlderThan: 1}, archive.Retention{}); !reflect.DeepEqual(got, []string{oldWeb, newWeb, oldAPI}) {
			t.Errorf("modified %v: %v, want only the dated ones", mtime, got)
		}
	}
	// Named, it can be deleted.
	if got := planned(t, claudeDir, dest, pruneSel{IDs: []string{u}}, archive.Retention{}); !reflect.DeepEqual(got, []string{u}) {
		t.Errorf("by id: %v", got)
	}

	// Nor is a conversation whose every copy sits in a bucket no manifest lists: no keep
	// entry could name it.
	conv(t, dest, "-Users-you-dev-gone", "aaaaaaaa-1111-4111-8111-00000000000c", time.Now().AddDate(0, 0, -400), "g1")
	if got := planned(t, claudeDir, dest, pruneSel{OlderThan: 100}, archive.Retention{}); !reflect.DeepEqual(got, []string{oldWeb, oldAPI}) {
		t.Errorf("with an unlisted bucket: %v", got)
	}

	// One copy that cannot be dated may hold the newest turn: the old copy beside it
	// does not date the conversation.
	writeFile(t, filepath.Join(archive.ProjectsDir(dest), "-Users-you-dev-web-app", oldAPI+".jsonl"),
		`{"type":"user","uuid":"p2","message":{"role":"user","content":"no time"}}`+"\n")
	if got := planned(t, claudeDir, dest, pruneSel{OlderThan: 100}, archive.Retention{}); !reflect.DeepEqual(got, []string{oldWeb}) {
		t.Errorf("with an undatable second copy: %v", got)
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
