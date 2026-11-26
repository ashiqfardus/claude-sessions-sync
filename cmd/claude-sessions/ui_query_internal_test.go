package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/convzip"
)

const (
	qWeb    = "a1000000-0000-4000-8000-000000000001" // web-app, indexed by LAP-1 and LAP-2; has a page; changed here a moment ago
	qZipped = "a1000000-0000-4000-8000-000000000002" // web-app on LAP-1, 200 days, compressed
	qForked = "a1000000-0000-4000-8000-000000000003" // api (example-org) on LAP-1, 40 days, forked
	qKept   = "a1000000-0000-4000-8000-000000000004" // notes on LAP-3, 400 days; notes is kept
	qGone   = "a1000000-0000-4000-8000-000000000005" // app on LAP-1, deleted
	qApp    = "a1000000-0000-4000-8000-000000000006" // app on LAP-1, 5 days; its deletion was undone
	qHome   = "a1000000-0000-4000-8000-000000000007" // the home folder on LAP-1, 10 days
	qAPI2   = "a1000000-0000-4000-8000-000000000008" // api (other-org) on LAP-2, 100 days
	qAmb    = "a1000000-0000-4000-8000-000000000009" // api with no remote on LAP-3, 20 days
	qLocal  = "a1000000-0000-4000-8000-00000000000a" // only on this computer, never archived
)

const (
	qbWeb, qbAPI, qbApp, qbHome = "-Users-you-dev-web-app", "-Users-you-dev-api", "-Users-you-dev-app", "-Users-you"
	qbWinWeb, qbWinAPI          = "e--web-app", "e--clients-api"
	qbNotes, qbAmbAPI           = "-home-you-notes", "-home-you-api"
)

// queryTranscript is a conversation whose first prompt is prompt, long enough that its
// zip is smaller than it.
func queryTranscript(cwd, prompt string) string {
	var b strings.Builder
	line := func(role, text string) {
		m, _ := json.Marshal(map[string]any{"type": role, "cwd": cwd, "message": map[string]any{"role": role, "content": text}})
		b.Write(append(m, '\n'))
	}
	line("user", prompt)
	for i := 0; i < 60; i++ {
		line("assistant", "Here is the same long reply again, so that the zip of this file is much smaller than the file.")
	}
	return b.String()
}

func sessionIndex(t *testing.T, dest, machine string, sessions []archive.Session) {
	t.Helper()
	if err := archive.WriteSessionIndex(dest, machine, sessions); err != nil {
		t.Fatal(err)
	}
}

// queryFixture is an archive LAP-1, LAP-2 and LAP-3 wrote, and this computer's profile,
// as LAP-1. Every file is written directly; nothing is pushed.
func queryFixture(t *testing.T) (root, dest string) {
	t.Helper()
	base := t.TempDir()
	root, dest = filepath.Join(base, ".claude"), filepath.Join(base, "archive")
	t.Setenv("CLAUDE_SESSIONS_MACHINE", "LAP-1")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	writeJSON(t, archive.ConfigPath(root), map[string]string{"destination": dest})

	shards := map[string]archive.Shard{
		"LAP-1": {Home: "/Users/you", Projects: map[string]archive.Project{
			qbWeb:  {Path: "/Users/you/dev/web-app", Leaf: "web-app", Remote: "https://you:placeholder-token@github.com/example-org/web-app"},
			qbAPI:  {Path: "/Users/you/dev/api", Leaf: "api", Remote: "git@github.com:example-org/api.git"},
			qbApp:  {Path: "/Users/you/dev/app", Leaf: "app"},
			qbHome: {Path: "/Users/you", Leaf: "you"},
		}},
		"LAP-2": {Home: `E:\Users\you`, Projects: map[string]archive.Project{
			qbWinWeb: {Path: `E:\web-app`, Leaf: "web-app", Remote: "git@github.com:example-org/web-app.git"},
			qbWinAPI: {Path: `E:\clients\api`, Leaf: "api", Remote: "https://github.com/other-org/api"},
		}},
		"LAP-3": {Home: "/home/you", Projects: map[string]archive.Project{
			qbNotes:  {Path: "/home/you/notes", Leaf: "notes"},
			qbAmbAPI: {Path: "/home/you/api", Leaf: "api"},
		}},
	}
	for m, s := range shards {
		s.SchemaVersion, s.Machine = archive.SchemaVersion, m
		writeJSON(t, filepath.Join(archive.ManifestDir(dest), m+".json"), s)
	}

	now := time.Now()
	ago := func(days int) time.Time { return now.AddDate(0, 0, -days) }
	index := map[string][]archive.Session{}
	for _, c := range []struct {
		machine, bucket, id, prompt string
		at                          time.Time
	}{
		{"LAP-1", qbWeb, qWeb, "Fix the invoice total", ago(3)},
		{"LAP-2", qbWinWeb, qWeb, "Fix the invoice total", ago(2)},
		{"LAP-1", qbWeb, qZipped, "Map the stock tables", ago(200)},
		{"LAP-1", qbAPI, qForked, "Ask the pelican", ago(40)},
		{"LAP-3", qbNotes, qKept, "Draft the notes", ago(400)},
		{"LAP-1", qbApp, qGone, "Removed one", ago(60)},
		{"LAP-1", qbApp, qApp, "Restored one", ago(5)},
		{"LAP-1", qbHome, qHome, "Tidy the home folder", ago(10)},
		{"LAP-2", qbWinAPI, qAPI2, "Trace the slow query", ago(100)},
		{"LAP-3", qbAmbAPI, qAmb, "Retry the import", ago(20)},
	} {
		path := shards[c.machine].Projects[c.bucket].Path
		body := queryTranscript(path, c.prompt)
		p := filepath.Join(archive.ProjectsDir(dest), c.bucket, c.id+".jsonl")
		writeFile(t, p, body)
		if err := os.Chtimes(p, c.at, c.at); err != nil {
			t.Fatal(err)
		}
		index[c.machine] = append(index[c.machine], archive.Session{Bucket: c.bucket, Project: path, ID: c.id,
			Updated: c.at, Size: int64(len(body)), Prompt: c.prompt})
	}
	for m, s := range index {
		sessionIndex(t, dest, m, s)
	}
	if _, err := convzip.Zip(filepath.Join(archive.ProjectsDir(dest), qbWeb), qZipped); err != nil {
		t.Fatal(err)
	}
	writeFile(t, forkPath(dest, qbAPI, qForked, "LAP-2"), queryTranscript("/Users/you/dev/api", "Ask the pelican"))
	writeFile(t, pagePath(dest, qbWeb, qWeb), "<p>the conversation</p>")
	writeJSON(t, archive.RetentionPath(dest), archive.Retention{Keep: []archive.KeepEntry{{Leaf: "notes"}}})
	writeJSON(t, filepath.Join(archive.DeletedDir(dest), "LAP-1.json"), map[string]any{
		"schemaVersion": 1, "machine": "LAP-1", "entries": []archive.DeletedEntry{
			{ID: qGone, Buckets: []string{qbApp}, LastAt: ago(60), DeletedAt: ago(1)},
			{ID: qApp, Buckets: []string{qbApp}, LastAt: ago(5), DeletedAt: ago(4), Undone: true},
		}})

	// This computer: qWeb changed a moment ago, and one conversation never archived.
	writeFile(t, filepath.Join(claude.ProjectsDir(root), qbWeb, qWeb+".jsonl"), queryTranscript("/Users/you/dev/web-app", "Fix the invoice total"))
	writeFile(t, filepath.Join(claude.ProjectsDir(root), qbWeb, qLocal+".jsonl"), queryTranscript("/Users/you/dev/web-app", "Watch the heron"))
	return root, dest
}

func fixtureProjects(t *testing.T, dest string) ([]projectOption, map[string]string) {
	t.Helper()
	opts, keys, err := queryProjects(dest, nil)
	if err != nil {
		t.Fatal(err)
	}
	return opts, keys
}

func optionByKey(t *testing.T, opts []projectOption, key string) projectOption {
	t.Helper()
	for _, o := range opts {
		if o.Key == key {
			return o
		}
	}
	t.Fatalf("no option %s in %+v", key, opts)
	return projectOption{}
}

func conversations(t *testing.T, root, dest string, q convQuery) convPage {
	t.Helper()
	pg, err := queryConversations(root, dest, q, time.Now())
	if err != nil {
		t.Fatalf("%+v: %v", q, err)
	}
	return pg
}

func rowIDs(pg convPage) []string {
	ids := []string{}
	for _, r := range pg.Rows {
		ids = append(ids, r.ID)
	}
	sort.Strings(ids)
	return ids
}

func sorted(ids ...string) []string {
	ids = append([]string{}, ids...)
	sort.Strings(ids)
	return ids
}

func TestProjectsGroupAcrossMachines(t *testing.T) {
	_, dest := queryFixture(t)
	opts, keys := fixtureProjects(t, dest)
	o := optionByKey(t, opts, "remote:github.com/example-org/web-app")
	if o.Name != "web-app" || !reflect.DeepEqual(o.Buckets, []string{qbWeb, qbWinWeb}) ||
		!reflect.DeepEqual(o.Paths, []machinePath{{"LAP-1", "/Users/you/dev/web-app"}, {"LAP-2", `E:\web-app`}}) {
		t.Errorf("web-app: %+v", o)
	}
	if keys[qbWeb] != o.Key || keys[qbWinWeb] != o.Key {
		t.Errorf("bucket keys %v", keys)
	}
	if b, _ := json.Marshal(opts); strings.Contains(string(b), "placeholder-token") {
		t.Errorf("a remote's token reaches the page: %s", b)
	}
	if home := optionByKey(t, opts, "leaf:you"); home.Name != "Home folder" {
		t.Errorf("the home folder is named %q", home.Name)
	}
	for i := 1; i < len(opts); i++ {
		if strings.ToLower(opts[i-1].Name) > strings.ToLower(opts[i].Name) {
			t.Errorf("not sorted by name: %q before %q", opts[i-1].Name, opts[i].Name)
		}
	}
}

func TestProjectsKeepTwoApisApart(t *testing.T) {
	_, dest := queryFixture(t)
	opts, _ := fixtureProjects(t, dest)
	a := optionByKey(t, opts, "remote:github.com/example-org/api")
	b := optionByKey(t, opts, "remote:github.com/other-org/api")
	if a.Name == b.Name || !strings.HasPrefix(a.Name, "api (") || !strings.Contains(a.Name, "github.com/example-org/api") ||
		!strings.Contains(b.Name, "github.com/other-org/api") {
		t.Errorf("names %q and %q", a.Name, b.Name)
	}
	if !reflect.DeepEqual(a.Buckets, []string{qbAPI}) || !reflect.DeepEqual(b.Buckets, []string{qbWinAPI}) {
		t.Errorf("buckets %v and %v", a.Buckets, b.Buckets)
	}
	// The api with no remote is neither: two remotes share its name.
	amb := optionByKey(t, opts, "leaf:api")
	if amb.Arg != "" || !reflect.DeepEqual(amb.Buckets, []string{qbAmbAPI}) || amb.Name == a.Name || amb.Name == b.Name {
		t.Errorf("the api with no remote: %+v", amb)
	}
}

// Ruling P10: an option's Arg is what config keep and prune --project resolve to it:
// its buckets and, beyond them, only buckets with no remote that two remotes share.
func TestProjectArgsResolveToTheirOption(t *testing.T) {
	_, dest := queryFixture(t)
	fixture, _, err := archive.Merged(dest)
	if err != nil {
		t.Fatal(err)
	}
	joined := map[string]archive.Project{ // a bucket with no remote joins the one remote with its name
		qbWeb:               {Path: "/Users/you/dev/web-app", Leaf: "web-app", Remote: "git@github.com:example-org/web-app.git", Machine: "LAP-1"},
		qbWinWeb:            {Path: `E:\web-app`, Leaf: "web-app", Remote: "https://github.com/example-org/web-app", Machine: "LAP-2"},
		"-home-you-web-app": {Path: "/home/you/web-app", Leaf: "web-app", Machine: "LAP-3"},
	}
	noPath := map[string]archive.Project{"-x-notes": {Leaf: "notes", Machine: "LAP-1"}} // no computer recorded its path
	for name, projects := range map[string]map[string]archive.Project{"fixture": fixture, "joined": joined, "no path": noPath} {
		opts, _ := groupProjects(projects, nil)
		ambiguous := map[string]bool{}
		for _, o := range opts {
			o.Arg = projectArg(projects, o)
			if o.Arg == "" {
				for _, b := range o.Buckets {
					ambiguous[b] = true
				}
			}
		}
		args := map[string]string{}
		for _, o := range opts {
			arg := projectArg(projects, o)
			args[o.Key] = arg
			if arg == "" {
				if strings.HasPrefix(o.Key, "remote:") {
					t.Errorf("%s: %s has no argument", name, o.Key)
				}
				continue
			}
			_, got, err := matchProject(projects, arg)
			if err != nil {
				t.Errorf("%s: %s: matchProject(%q): %v", name, o.Key, arg, err)
				continue
			}
			for _, b := range o.Buckets {
				if !slices.Contains(got, b) {
					t.Errorf("%s: %s: matchProject(%q) leaves out %s", name, o.Key, arg, b)
				}
			}
			for _, b := range got {
				if !slices.Contains(o.Buckets, b) && !ambiguous[b] {
					t.Errorf("%s: %s: matchProject(%q) also gives %s", name, o.Key, arg, b)
				}
			}
		}
		if name == "no path" && args["leaf:notes"] != "notes" {
			t.Errorf("no path: arguments %v, want the folder name", args)
		}
		if name == "joined" && (len(opts) != 1 || len(opts[0].Buckets) != 3) {
			t.Errorf("joined: %+v", opts)
		}
		if name == "fixture" && (args["leaf:api"] != "" || args["remote:github.com/example-org/api"] != "/Users/you/dev/api" ||
			args["leaf:app"] != "/Users/you/dev/app") {
			t.Errorf("fixture arguments %v", args)
		}
	}
}

func TestConversationsOneRowAcrossMachines(t *testing.T) {
	root, dest := queryFixture(t)
	pg := conversations(t, root, dest, convQuery{Per: 100})
	var rows []convRow
	for _, r := range pg.Rows {
		if r.ID == qWeb {
			rows = append(rows, r)
		}
	}
	if len(rows) != 1 {
		t.Fatalf("%d rows for %s", len(rows), qWeb)
	}
	r := rows[0]
	if !reflect.DeepEqual(r.Machines, []string{"LAP-1", "LAP-2"}) || !reflect.DeepEqual(sorted(r.Buckets...), []string{qbWeb, qbWinWeb}) {
		t.Errorf("machines %v buckets %v", r.Machines, r.Buckets)
	}
	if r.Title != "Fix the invoice total" || r.Project != "web-app" || r.ProjectKey != "remote:github.com/example-org/web-app" {
		t.Errorf("row %+v", r)
	}
	if want := time.Now().AddDate(0, 0, -2); r.Updated.Before(want.Add(-time.Minute)) {
		t.Errorf("updated %v, want LAP-2's newer %v", r.Updated, want)
	}
	if pg.Total != 8 || len(pg.Rows) != 8 {
		t.Errorf("total %d, %d rows: %v", pg.Total, len(pg.Rows), rowIDs(pg))
	}
}

func TestConversationsStates(t *testing.T) {
	root, dest := queryFixture(t)
	pg := conversations(t, root, dest, convQuery{Per: 100})
	rows := map[string]convRow{}
	var total int64
	for _, r := range pg.Rows {
		rows[r.ID] = r
		total += r.Size
	}
	if _, ok := rows[qGone]; ok {
		t.Error("the deleted conversation is listed")
	}
	if _, ok := rows[qApp]; !ok {
		t.Error("the conversation whose deletion was undone is not listed")
	}
	z := rows[qZipped]
	if !z.Compressed || z.Size <= 0 || z.Size >= z.Full {
		t.Errorf("zipped: %+v", z)
	}
	if !rows[qForked].Forked || !rows[qKept].Kept || !rows[qWeb].Readable || !rows[qWeb].InUse {
		t.Errorf("forked %+v\nkept %+v\nweb %+v", rows[qForked], rows[qKept], rows[qWeb])
	}
	for id, r := range rows {
		if id != qZipped && r.Compressed || id != qForked && r.Forked || id != qKept && r.Kept ||
			id != qWeb && (r.Readable || r.InUse) {
			t.Errorf("%s has a state it lacks: %+v", id, r)
		}
		if !r.Compressed && r.Size != r.Full {
			t.Errorf("%s: size %d, full %d", id, r.Size, r.Full)
		}
	}
	if pg.TotalBytes != total {
		t.Errorf("total bytes %d, want %d", pg.TotalBytes, total)
	}
}

func TestConversationsFilters(t *testing.T) {
	root, dest := queryFixture(t)
	day := func(n int) string { return time.Now().AddDate(0, 0, -n).Format("2006-01-02") }
	for _, c := range []struct {
		q    convQuery
		want []string
	}{
		{convQuery{Project: "remote:github.com/example-org/web-app"}, []string{qWeb, qZipped}},
		{convQuery{Project: "leaf:api"}, []string{qAmb}},
		{convQuery{Machine: "*many"}, []string{qWeb}},
		{convQuery{Machine: "LAP-3"}, []string{qKept, qAmb}},
		{convQuery{Age: "7d"}, []string{qWeb, qApp}},
		{convQuery{Age: "30d"}, []string{qWeb, qApp, qHome, qAmb}},
		{convQuery{Age: "over90d"}, []string{qZipped, qAPI2, qKept}},
		{convQuery{Age: "over1y"}, []string{qKept}},
		{convQuery{State: "compressed"}, []string{qZipped}},
		{convQuery{State: "kept"}, []string{qKept}},
		{convQuery{State: "forked"}, []string{qForked}},
		{convQuery{State: "inuse"}, []string{qWeb}},
		{convQuery{Since: day(45), Until: day(15)}, []string{qForked, qAmb}},
		{convQuery{Since: day(10)}, []string{qWeb, qApp, qHome}},
		{convQuery{Until: day(200)}, []string{qZipped, qKept}},
		{convQuery{Project: "remote:github.com/example-org/web-app", Age: "over90d"}, []string{qZipped}},
	} {
		c.q.Per = 100
		if got := rowIDs(conversations(t, root, dest, c.q)); !reflect.DeepEqual(got, sorted(c.want...)) {
			t.Errorf("%+v gave %v, want %v", c.q, got, sorted(c.want...))
		}
	}
}

func TestProjectFilterIsNotSubstring(t *testing.T) {
	root, dest := queryFixture(t)
	opts, _ := fixtureProjects(t, dest)
	app := optionByKey(t, opts, "leaf:app")
	if got := rowIDs(conversations(t, root, dest, convQuery{Project: app.Key})); !reflect.DeepEqual(got, []string{qApp}) {
		t.Errorf("project %s gave %v", app.Key, got)
	}
	for _, key := range []string{"app", "leaf:ap", "web-app", "remote:"} {
		if got := rowIDs(conversations(t, root, dest, convQuery{Project: key})); len(got) != 0 {
			t.Errorf("project %q gave %v", key, got)
		}
	}
}

func TestConversationsPaging(t *testing.T) {
	root, dest := queryFixture(t)
	var more []archive.Session
	for i := 0; i < 15; i++ { // the fixture's 8, and 15 more
		more = append(more, archive.Session{Bucket: qbNotes, ID: fmt.Sprintf("b2000000-0000-4000-8000-%012d", i),
			Updated: time.Now().AddDate(0, 0, -500-i), Size: 10, Prompt: "Filler"})
	}
	sessionIndex(t, dest, "LAP-4", more)
	pg := conversations(t, root, dest, convQuery{Per: 10})
	if pg.Total != 23 || pg.Pages != 3 || pg.Page != 1 || pg.Per != 10 || len(pg.Rows) != 10 {
		t.Errorf("first page: total %d pages %d page %d per %d rows %d", pg.Total, pg.Pages, pg.Page, pg.Per, len(pg.Rows))
	}
	for i := 1; i < len(pg.Rows); i++ {
		if pg.Rows[i].Updated.After(pg.Rows[i-1].Updated) {
			t.Errorf("not newest first at %d", i)
		}
	}
	if pg = conversations(t, root, dest, convQuery{Per: 10, Page: 9}); pg.Page != 3 || len(pg.Rows) != 3 {
		t.Errorf("page 9: page %d, %d rows", pg.Page, len(pg.Rows))
	}
	if pg = conversations(t, root, dest, convQuery{Project: "leaf:none"}); pg.Pages != 1 || pg.Page != 1 || pg.Rows == nil || len(pg.Rows) != 0 {
		t.Errorf("no rows: %+v", pg)
	}
}

func TestConversationsTextSearch(t *testing.T) {
	root, dest := queryFixture(t)
	for _, c := range []struct {
		q    convQuery
		want []string
	}{
		{convQuery{Text: "pelican"}, []string{qForked}},
		{convQuery{Text: "PELICAN", CaseSensitive: true}, nil},
		{convQuery{Text: "stock tables"}, []string{qZipped}}, // read from its zip
		{convQuery{Text: "heron"}, nil},
		{convQuery{Text: "heron", Look: "local"}, []string{qLocal}},
		{convQuery{Text: "pelican", Look: "local"}, nil},
		{convQuery{Text: "p[e]lican", Regexp: true}, []string{qForked}},
		{convQuery{Text: "pelican", Role: "assistant"}, nil},
	} {
		c.q.Limit = 50
		if got := rowIDs(conversations(t, root, dest, c.q)); !reflect.DeepEqual(got, sorted(c.want...)) {
			t.Errorf("%+v gave %v, want %v", c.q, got, c.want)
		}
	}
	local := conversations(t, root, dest, convQuery{Look: "local"})
	if got := rowIDs(local); !reflect.DeepEqual(got, sorted(qWeb, qLocal)) {
		t.Errorf("this computer's conversations: %v", got)
	}
	for _, r := range local.Rows {
		if !reflect.DeepEqual(r.Machines, []string{"LAP-1"}) || r.ID == qWeb && (r.Project != "web-app" || !r.InUse || !r.Readable) {
			t.Errorf("local row %+v", r)
		}
	}
	if pg := conversations(t, root, dest, convQuery{Text: "the same long reply", Limit: 2}); !pg.Stopped {
		t.Error("a search that met its limit is not marked stopped")
	}
}

// The archive is shared: a file a sync app half wrote must not fail the listing.
func TestConversationsSkipUnreadableFiles(t *testing.T) {
	root, dest := queryFixture(t)
	writeFile(t, archive.RetentionPath(dest), "{")
	writeFile(t, filepath.Join(archive.DeletedDir(dest), "LAP-2.json"), "{")
	writeFile(t, filepath.Join(archive.ManifestDir(dest), "LAP-5.json"), "{")
	pg := conversations(t, root, dest, convQuery{Per: 100})
	if pg.Unreadable != 3 || slices.Contains(rowIDs(pg), qGone) || len(pg.Rows) != 8 {
		t.Errorf("unreadable %d, rows %v: LAP-1's deleted list must still hide %s", pg.Unreadable, rowIDs(pg), qGone)
	}
}

func TestConversationsRefuseBadValues(t *testing.T) {
	root, dest := queryFixture(t)
	for _, q := range []convQuery{{Age: "forever"}, {State: "lost"}, {Look: "cloud"}, {Role: "system"},
		{Since: "2026-13-01"}, {Until: "yesterday"}, {Per: 101}, {Per: -1}, {Page: -1}, {Limit: -1},
		{Text: "(", Regexp: true}} {
		if _, err := queryConversations(root, dest, q, time.Now()); err == nil {
			t.Errorf("%+v was accepted", q)
		} else if _, ok := err.(badQuery); !ok {
			t.Errorf("%+v: %T %v", q, err, err)
		}
	}
}

func TestQueryRoutes(t *testing.T) {
	root, _ := queryFixture(t)
	indexes, unpacked := zipReads(t)
	u := newTestUI(t, uiOpts{ClaudeDir: root, NoOpen: true})
	runUI(t, u)
	cookie := login(t, u)

	resp, body := send(t, u, http.MethodGet, u.url("/api/conversations"), cookie)
	var pg convPage
	if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &pg) != nil || pg.Total != 8 || pg.Per != 10 || len(pg.Rows) != 8 {
		t.Fatalf("/api/conversations gave %d %s", resp.StatusCode, body)
	}
	for _, field := range []string{`"totalBytes":`, `"projectKey":`, `"inUse":`, `"rows":[`} {
		if !strings.Contains(body, field) {
			t.Errorf("no %s in %s", field, body)
		}
	}
	if len(*indexes)+len(*unpacked) > 0 {
		t.Errorf("a plain page load opened zips: %v %v", *indexes, *unpacked)
	}

	resp, body = send(t, u, http.MethodGet, u.url("/api/conversations?q=pelican&project=remote:github.com/example-org/api&page=1&per=5&case=false&all=true&limit=0"), cookie)
	if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &pg) != nil || len(pg.Rows) != 1 || pg.Rows[0].ID != qForked {
		t.Errorf("a search gave %d %s", resp.StatusCode, body)
	}

	var opts []projectOption
	resp, body = send(t, u, http.MethodGet, u.url("/api/projects"), cookie)
	if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &opts) != nil || len(opts) != 7 || !strings.Contains(body, `"buckets":[`) {
		t.Errorf("/api/projects gave %d %s", resp.StatusCode, body)
	}

	for _, q := range []string{"age=forever", "page=x", "per=500", "case=maybe", "since=26-11-01", "q=(&regexp=1", "look=cloud"} {
		resp, body := send(t, u, http.MethodGet, u.url("/api/conversations?"+q), cookie)
		var e map[string]string
		if resp.StatusCode != http.StatusBadRequest || json.Unmarshal([]byte(body), &e) != nil || e["error"] == "" ||
			len(e) != 1 || strings.ContainsAny(e["error"], "%:`") || !strings.HasSuffix(e["error"], ".") {
			t.Errorf("%s gave %d %s", q, resp.StatusCode, body)
		}
	}
	if resp, _ := send(t, u, http.MethodGet, u.url("/api/conversations"), ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("no cookie gave %d", resp.StatusCode)
	}

	// No archive folder chosen, then one that cannot be reached: an answer, never an error.
	for _, c := range []struct{ dest, problem string }{{"", "none"}, {filepath.Join(t.TempDir(), "gone"), "unreachable"}} {
		writeJSON(t, archive.ConfigPath(root), map[string]string{"destination": c.dest})
		for _, p := range []string{"/api/projects", "/api/conversations"} {
			if resp, body := send(t, u, http.MethodGet, u.url(p), cookie); resp.StatusCode != http.StatusOK ||
				strings.TrimSpace(body) != `{"problem":"`+c.problem+`"}` {
				t.Errorf("%s with destination %q gave %d %s", p, c.dest, resp.StatusCode, body)
			}
		}
		resp, body := send(t, u, http.MethodGet, u.url("/api/conversations?look=local"), cookie)
		if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &pg) != nil || pg.Total != 2 {
			t.Errorf("look=local with destination %q gave %d %s", c.dest, resp.StatusCode, body)
		}
	}
}

// Two computers' home folders by different names are two options, told apart.
func TestProjectsNameEachHomeFolder(t *testing.T) {
	projects := map[string]archive.Project{
		"-Users-you": {Path: "/Users/you", Leaf: "you", Machine: "LAP-1"},
		"-home-me":   {Path: "/home/me", Leaf: "me", Machine: "LAP-3"},
	}
	var names []string
	opts, _ := groupProjects(projects, []string{"/Users/you", "/home/me"})
	for _, o := range opts {
		names = append(names, o.Name)
	}
	if want := []string{"Home folder (LAP-1)", "Home folder (LAP-3)"}; !reflect.DeepEqual(names, want) {
		t.Errorf("names %v, want %v", names, want)
	}
	if opts, _ := groupProjects(projects, []string{"/home/me"}); opts[0].Name != "Home folder" {
		t.Errorf("one home folder is named %q", opts[0].Name)
	}
}

// A token in a remote never reaches the log through a refusal.
func TestMatchProjectRefusalHidesTokens(t *testing.T) {
	_, _, err := matchProject(map[string]archive.Project{
		"-Users-you-dev-api": {Path: "/Users/you/dev/api", Leaf: "api", Remote: "https://you:placeholder-token@github.com/example-org/api.git"},
		"e--clients-api":     {Path: `E:\clients\api`, Leaf: "api", Remote: "git@github.com:other-org/api.git"},
	}, "api")
	if err == nil || strings.Contains(err.Error(), "placeholder-token") || !strings.Contains(err.Error(), "github.com/example-org/api") {
		t.Errorf("refusal: %v", err)
	}
}

// Search reads only the conversations the other filters kept, and its limit counts
// conversations (Ruling P49).
func TestSearchLimitCountsConversations(t *testing.T) {
	root, dest := queryFixture(t)
	// Every archived transcript has 60 matching replies: one no longer uses up the limit.
	if pg := conversations(t, root, dest, convQuery{Text: "the same long reply", Limit: 50, Per: 100}); pg.Total != 8 || pg.Stopped {
		t.Errorf("%d conversations, stopped %v: %v", pg.Total, pg.Stopped, rowIDs(pg))
	}
	if pg := conversations(t, root, dest, convQuery{Text: "the same long reply", Limit: 3}); pg.Total != 3 || !pg.Stopped {
		t.Errorf("limit 3: %d conversations, stopped %v", pg.Total, pg.Stopped)
	}

	// 55 newer conversations on LAP-3 match too: a filter still finds the older one on LAP-2.
	var newer []archive.Session
	for i := 0; i < 55; i++ {
		id := fmt.Sprintf("c3000000-0000-4000-8000-%012d", i)
		body := queryTranscript("/home/you/notes", "Find the needle")
		writeFile(t, filepath.Join(archive.ProjectsDir(dest), qbNotes, id+".jsonl"), body)
		newer = append(newer, archive.Session{Bucket: qbNotes, Project: "/home/you/notes", ID: id,
			Updated: time.Now().Add(-time.Duration(i) * time.Minute), Size: int64(len(body)), Prompt: "Find the needle"})
	}
	sessionIndex(t, dest, "LAP-5", newer)
	old := filepath.Join(archive.ProjectsDir(dest), qbWinAPI, qAPI2+".jsonl")
	writeFile(t, old, queryTranscript(`E:\clients\api`, "Trace the slow query, and find the needle"))
	at := time.Now().AddDate(0, 0, -100)
	if err := os.Chtimes(old, at, at); err != nil {
		t.Fatal(err)
	}
	if pg := conversations(t, root, dest, convQuery{Text: "needle", Limit: 50}); pg.Total != 50 || !pg.Stopped {
		t.Errorf("unfiltered: %d conversations, stopped %v", pg.Total, pg.Stopped)
	}
	for _, q := range []convQuery{{Machine: "LAP-2"}, {Project: "remote:github.com/other-org/api"}} {
		q.Text, q.Limit = "needle", 50
		if got := rowIDs(conversations(t, root, dest, q)); !reflect.DeepEqual(got, []string{qAPI2}) {
			t.Errorf("%+v gave %v", q, got)
		}
	}
}

// A conversation deleted and then continued is in the archive again (Ruling P50).
func TestDeletedButContinuedIsShown(t *testing.T) {
	root, dest := queryFixture(t)
	ago := func(days int) time.Time { return time.Now().AddDate(0, 0, -days) }
	writeJSON(t, filepath.Join(archive.DeletedDir(dest), "LAP-2.json"), map[string]any{
		"schemaVersion": 1, "machine": "LAP-2", "entries": []archive.DeletedEntry{
			{ID: qHome, DeletedAt: ago(20)}, // continued 10 days ago
			{ID: qAPI2, DeletedAt: ago(50)}, // last changed 100 days ago
		}})
	got := rowIDs(conversations(t, root, dest, convQuery{Per: 100}))
	if !slices.Contains(got, qHome) || slices.Contains(got, qAPI2) || slices.Contains(got, qGone) {
		t.Errorf("rows %v: want %s shown, %s and %s hidden", got, qHome, qAPI2, qGone)
	}
}

// A new archive has no conversations folder yet, nor a new profile: search finds
// nothing, and is not an error.
func TestSearchWithNoConversationsFolder(t *testing.T) {
	root, dest := queryFixture(t)
	if err := os.RemoveAll(archive.ProjectsDir(dest)); err != nil {
		t.Fatal(err)
	}
	if pg := conversations(t, root, dest, convQuery{Text: "pelican", Limit: 50}); pg.Total != 0 {
		t.Errorf("archive: %v", rowIDs(pg))
	}
	if err := os.RemoveAll(claude.ProjectsDir(root)); err != nil {
		t.Fatal(err)
	}
	u := newTestUI(t, uiOpts{ClaudeDir: root, NoOpen: true})
	runUI(t, u)
	cookie := login(t, u)
	for _, q := range []string{"q=pelican", "q=heron&look=local"} {
		var pg convPage
		if resp, body := send(t, u, http.MethodGet, u.url("/api/conversations?"+q), cookie); resp.StatusCode != http.StatusOK ||
			json.Unmarshal([]byte(body), &pg) != nil || pg.Total != 0 {
			t.Errorf("%s gave %d %s", q, resp.StatusCode, body)
		}
	}
}

// config keep and unkeep print a kept remote without the token it may hold.
func TestKeepLabelHidesTokens(t *testing.T) {
	const raw = "https://you:placeholder-token@github.com/example-org/api.git"
	for _, e := range []archive.KeepEntry{{Remote: raw}, {Leaf: "api", ForRemote: raw}} {
		if l := keepLabel(e); strings.Contains(l, "placeholder-token") || !strings.Contains(l, "https://github.com/example-org/api.git") {
			t.Errorf("%+v: %s", e, l)
		}
	}
}

// Minor PA M2: on a hung network mount a view answers "unreachable" within the limit,
// and reloads wait on the one Stat already running instead of leaving one each.
func TestViewsAnswerWhenTheArchiveHangs(t *testing.T) {
	root, _ := queryFixture(t)
	var stats atomic.Int32
	release := make(chan struct{})
	hook(t, &destLimit, 50*time.Millisecond)
	hook(t, &statDest, func(p string) (os.FileInfo, error) {
		stats.Add(1)
		<-release
		return os.Stat(p)
	})
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
	})
	start := time.Now()
	for i := 0; i < 5; i++ {
		if _, problem := panelDest(root); problem != "unreachable" {
			t.Fatalf("a hung archive gave %q", problem)
		}
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("5 views took %v on a hung archive", d)
	}
	if n := stats.Load(); n != 1 {
		t.Errorf("%d Stats left blocked after 5 views, want 1", n)
	}
	close(release)
	released = true
	waitUntil(t, "the archive to answer again", func() bool { _, problem := panelDest(root); return problem == "" })
}
