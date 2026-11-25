package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
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
