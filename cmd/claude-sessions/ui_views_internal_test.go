package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/convzip"
)

// getJSON fetches a panel route and decodes its answer into v; it must be a 200.
func getJSON(t *testing.T, u *uiServer, cookie, path string, v any) string {
	t.Helper()
	resp, body := send(t, u, http.MethodGet, u.url(path), cookie)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s gave %d %s", path, resp.StatusCode, body)
	}
	if err := json.Unmarshal([]byte(body), v); err != nil {
		t.Fatalf("%s: %v in %s", path, err, body)
	}
	return body
}

// appendLog writes lines to this computer's log, stamped at.
func appendLog(t *testing.T, root string, at time.Time, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(root, "session-sync.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		fmt.Fprintf(f, "%s  %s\n", at.Format("2006-01-02 15:04:05"), l)
	}
}

func writeShard(t *testing.T, dest string, s archive.Shard) {
	t.Helper()
	s.SchemaVersion = archive.SchemaVersion
	writeJSON(t, filepath.Join(archive.ManifestDir(dest), s.Machine+".json"), s)
}

func TestPanelWorksWithNoArchive(t *testing.T) {
	t.Setenv("CLAUDE_SESSIONS_MACHINE", "LAP-1")
	u := newTestUI(t, uiOpts{NoOpen: true}) // a new profile: no folder chosen, none found
	runUI(t, u)
	cookie := login(t, u)
	gets := 0
	for _, r := range u.routes() {
		// /read/ answers a missing page with 404, and a job's events need its id.
		if r.Method != http.MethodGet || r.Prefix || r.Path == "/api/jobs/events" {
			continue
		}
		gets++
		var v any
		getJSON(t, u, cookie, r.Path, &v)
	}
	if gets < 13 {
		t.Errorf("only %d GET routes", gets)
	}
	for _, c := range []struct{ dest, problem string }{{"", "none"}, {filepath.Join(t.TempDir(), "gone"), "unreachable"}} {
		writeJSON(t, archive.ConfigPath(u.root), map[string]string{"destination": c.dest})
		var s summaryView
		getJSON(t, u, cookie, "/api/summary", &s)
		var st storageView
		getJSON(t, u, cookie, "/api/storage", &st)
		if s.Archive != nil || s.Problem != c.problem || st.Archive != nil || st.Problem != c.problem {
			t.Errorf("destination %q: summary %+v, storage %+v", c.dest, s, st)
		}
		if s.Machine != "LAP-1" || s.Version != version || s.Computers == nil {
			t.Errorf("summary %+v", s)
		}
		for _, p := range []string{"/api/retention", "/api/deleted", "/api/profile"} {
			if resp, body := send(t, u, http.MethodGet, u.url(p), cookie); resp.StatusCode != http.StatusOK ||
				strings.TrimSpace(body) != `{"problem":"`+c.problem+`"}` {
				t.Errorf("%s with destination %q gave %d %s", p, c.dest, resp.StatusCode, body)
			}
		}
		var set settingsView
		getJSON(t, u, cookie, "/api/settings", &set)
		if set.Archive != nil || set.Problem != c.problem || set.Config == nil || set.LogPath == "" {
			t.Errorf("settings %+v", set)
		}
		var health []healthItem
		getJSON(t, u, cookie, "/api/health", &health)
		if len(health) == 0 || health[0].Level != "fail" {
			t.Errorf("health %+v", health)
		}
	}
}

// A sync folder that downloads each file it is asked to read: the Overview, Storage and
// a plain Conversations list read index files, names and sizes only.
func TestQueriesReadNoConversationFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 000 does not stop reads on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads a file whatever its mode")
	}
	root, dest := queryFixture(t)
	indexes, unpacked := zipReads(t)
	u := newTestUI(t, uiOpts{ClaudeDir: root, NoOpen: true})
	runUI(t, u)
	cookie := login(t, u)
	routes := []string{"/api/summary", "/api/storage", "/api/conversations"}
	before := map[string]string{}
	for _, p := range routes {
		var v any
		before[p] = getJSON(t, u, cookie, p, &v)
	}
	var s summaryView
	var st storageView
	json.Unmarshal([]byte(before["/api/summary"]), &s)
	json.Unmarshal([]byte(before["/api/storage"]), &st)
	if s.Conversations != 8 || st.Sizes.Conversations == 0 || st.ZipFrees == 0 || len(st.Projects) == 0 {
		t.Fatalf("summary %+v\nstorage %+v", s, st)
	}

	locked := 0
	for _, dir := range []string{archive.ProjectsDir(dest), filepath.Join(dest, "forks")} {
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && (strings.HasSuffix(p, ".jsonl") || strings.HasSuffix(p, convzip.Suffix)) {
				if err := os.Chmod(p, 0); err != nil {
					t.Fatal(err)
				}
				locked++
			}
			return nil
		})
	}
	if locked < 10 {
		t.Fatalf("locked %d files", locked)
	}
	for _, p := range routes {
		var v any
		if after := getJSON(t, u, cookie, p, &v); after != before[p] {
			t.Errorf("%s changed once the conversations could not be read:\n%s\n%s", p, before[p], after)
		}
	}
	if len(*indexes)+len(*unpacked) > 0 {
		t.Errorf("zips opened: %v %v", *indexes, *unpacked)
	}
}

func TestComputerStates(t *testing.T) {
	root, dest := queryFixture(t)
	now := time.Now()
	for _, s := range []archive.Shard{
		{Machine: "LAP-1", ToolVersion: version, ReadsZips: true, HonorsDeleted: true, LastPush: now.Add(-5 * time.Hour)},
		{Machine: "LAP-2", ToolVersion: "0.6.0", ReadsZips: true, HonorsDeleted: true, LastPush: now.Add(-time.Hour)},
		{Machine: "LAP-3", ReadsZips: true, HonorsDeleted: true, LastPush: now.Add(-4 * time.Hour)},
		{Machine: "LAP-4", HonorsDeleted: true, LastPush: now.Add(-time.Hour)},
		{Machine: "LAP-5", ReadsZips: true, HonorsDeleted: true}, // no lastPush: unknown (Ruling P33)
	} {
		writeShard(t, dest, s)
	}
	pushed := now.Add(-10 * time.Minute)
	appendLog(t, root, pushed, "PUSH: 3 copied, 9 unchanged, 4 session(s), 2 project(s) -> "+dest)

	sum := querySummary(root, now)
	if sum.Archive == nil || sum.Archive.Path != dest || sum.Problem != "" {
		t.Fatalf("summary %+v", sum)
	}
	got := map[string]computerView{}
	var names []string
	for _, c := range sum.Computers {
		got[c.Name] = c
		names = append(names, c.Name)
	}
	if want := []string{"LAP-1", "LAP-2", "LAP-3", "LAP-4", "LAP-5"}; !reflect.DeepEqual(names, want) {
		t.Errorf("computers %v, want %v", names, want)
	}
	for name, want := range map[string]string{"LAP-1": "ok", "LAP-2": "ok", "LAP-3": "late", "LAP-4": "update", "LAP-5": "update"} {
		if got[name].State != want {
			t.Errorf("%s is %q, want %q: %+v", name, got[name].State, want, got[name])
		}
	}
	if !got["LAP-1"].This || got["LAP-2"].This || !got["LAP-2"].Ready || got["LAP-4"].Ready || got["LAP-2"].Version != "0.6.0" {
		t.Errorf("flags %+v", got)
	}
	// This computer's last backup is the newer of its shard's and its log's.
	if at := got["LAP-1"].LastPush; at.Sub(pushed).Abs() > time.Second || sum.LastBackup.Sub(pushed).Abs() > time.Second {
		t.Errorf("this computer's last backup %v, summary %v, want %v", at, sum.LastBackup, pushed)
	}
	// One conversation is on two computers, one was deleted: 9 indexed, 8 in the archive.
	if sum.Conversations != 8 || got["LAP-1"].Conversations != 5 || got["LAP-2"].Conversations != 2 || got["LAP-3"].Conversations != 2 {
		t.Errorf("conversations %d, per computer %+v", sum.Conversations, got)
	}
	newer := now.Add(-time.Minute).Truncate(time.Second) // the shard newer than the log
	writeShard(t, dest, archive.Shard{Machine: "LAP-1", ReadsZips: true, HonorsDeleted: true, LastPush: newer})
	if sum = querySummary(root, now); !sum.LastBackup.Equal(newer) || !sum.Computers[0].LastPush.Equal(newer) {
		t.Errorf("last backup %v, this computer's row %v, want %v", sum.LastBackup, sum.Computers[0].LastPush, newer)
	}
}

// Ruling I2: a shard's lastPush is cut to the hour, so it is judged by the hour's end;
// this computer's log gives the exact time.
func TestComputerStateAtTheHourBoundary(t *testing.T) {
	t.Setenv("CLAUDE_SESSIONS_MACHINE", "LAP-1")
	at := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC) // a push between 08:00 and 08:59
	shards := map[string]archive.Shard{
		"LAP-1": {Machine: "LAP-1", ReadsZips: true, HonorsDeleted: true, LastPush: at},
		"LAP-2": {Machine: "LAP-2", ReadsZips: true, HonorsDeleted: true, LastPush: at},
	}
	logged := at.Add(30 * time.Minute)
	for _, c := range []struct {
		now        time.Time
		lap1, lap2 string
	}{
		{logged.Add(2 * time.Hour), "ok", "ok"},
		{logged.Add(2*time.Hour + time.Second), "late", "ok"},
		{at.Add(3 * time.Hour), "late", "ok"},
		{at.Add(3*time.Hour + time.Second), "late", "late"},
	} {
		got := map[string]string{}
		for _, cv := range computersOf(shards, nil, logged, c.now) {
			got[cv.Name] = cv.State
		}
		if got["LAP-1"] != c.lap1 || got["LAP-2"] != c.lap2 {
			t.Errorf("at %s: %v, want LAP-1 %s, LAP-2 %s", c.now.Format("15:04:05"), got, c.lap1, c.lap2)
		}
	}
}

// Every check name in the package's code has a plain sentence.
func TestEveryDoctorCheckHasPlainWords(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	var src []byte
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src = append(src, b...)
	}
	names := regexp.MustCompile(`check\{"([^"]+)"`).FindAllStringSubmatch(string(src), -1)
	if len(names) < 25 {
		t.Fatalf("found %d check names in %d files", len(names), len(files))
	}
	for _, m := range names {
		if plainChecks[m[1]] == nil {
			t.Errorf("no plain words for doctor's %q check", m[1])
		}
	}
}

// Every check doctor gives on a few archives has plain words of its own, which never
// show a path or a token.
func TestHealthWordsForEveryDoctorRun(t *testing.T) {
	isolateUI(t)
	root, dest := queryFixture(t)
	writeFile(t, filepath.Join(dest, "projects", qbWeb, ".credentials.json"), "{}")
	writeFile(t, filepath.Join(archive.ManifestDir(dest), "LAP-9 (1).json"), "{")
	writeFile(t, claude.SettingsPath(root), `{"cleanupPeriodDays": 20}`)
	writeFile(t, filepath.Join(root, "CLAUDE.md"), "rules")
	writeFile(t, filepath.Join(profileDir(dest), "machines", "LAP-1", "CLAUDE.md"), "one")
	writeFile(t, filepath.Join(profileDir(dest), "machines", "LAP-2", "CLAUDE.md"), "two")
	writeJSON(t, archive.RetentionPath(dest), archive.Retention{KeepDays: 120})
	appendLog(t, root, time.Now().Add(-5*time.Hour), "PUSH: 1 copied, 0 unchanged, 1 session(s), 1 project(s)")
	appendLog(t, root, time.Now(), "WARN: x: operation not permitted")
	t.Setenv("CLAUDE_CODE_PROJECT_DIR_NAME", "pinned")
	idleHere(t, root)

	noDest, gone := t.TempDir(), t.TempDir()
	writeJSON(t, archive.ConfigPath(gone), map[string]string{"destination": filepath.Join(gone, "missing")})
	seen := map[string]bool{}
	for _, r := range []string{root, noDest, gone} {
		for _, o := range []doctorOpts{{NoProbe: true}, {}, {NoProbe: true, Light: true}} {
			o.ClaudeDir = r
			rep, _ := runDoctor(quietStreams(), o)
			for _, c := range rep.Checks {
				seen[c.Name] = true
				if plainChecks[c.Name] == nil {
					t.Errorf("no plain words for %q (%s)", c.Name, c.Detail)
					continue
				}
				it := plainCheck(c)
				if it.Name != c.Name || it.Level != string(c.Level) || !strings.HasSuffix(it.Text, ".") ||
					strings.Contains(it.Text, root) || strings.Contains(it.Text, dest) || strings.Contains(it.Text, "placeholder-token") ||
					strings.ContainsAny(it.Text, "`") || it.FixHref != "" && !strings.HasPrefix(it.FixHref, "#/") || (it.FixHref == "") != (it.FixLabel == "") {
					t.Errorf("%+v gave %+v", c, it)
				}
			}
		}
	}
	if len(seen) < 20 {
		t.Errorf("only %d check names seen: %v", len(seen), seen)
	}
	if it := plainCheck(check{"a check added later", levelWarn, "/Users/you/secret", ""}); it.Text == "" || strings.Contains(it.Text, "/Users") || it.FixHref == "" {
		t.Errorf("an unknown check gave %+v", it)
	}
}

func TestHealthOrderAndOneDoctorAtATime(t *testing.T) {
	items := sortHealth([]healthItem{{Name: "a", Level: "info"}, {Name: "b", Level: "ok"}, {Name: "c", Level: "fail"},
		{Name: "d", Level: "warn"}, {Name: "e", Level: "fail"}})
	var order []string
	for _, it := range items {
		order = append(order, it.Name)
	}
	if want := []string{"c", "e", "d", "b", "a"}; !reflect.DeepEqual(order, want) {
		t.Errorf("order %v, want %v", order, want)
	}

	// Ruling P15: callers that come while a doctor runs get that run's result.
	var f flight[[]healthItem]
	none := []healthItem{}
	var runs atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{})
	var wg sync.WaitGroup
	got := make([][]healthItem, 5)
	for i := range got {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i] = f.do("", none, func() ([]healthItem, time.Duration) {
				runs.Add(1)
				close(started)
				<-release
				return []healthItem{{Name: "archive"}}, 0
			})
		}()
	}
	<-started
	time.Sleep(50 * time.Millisecond) // the other callers are waiting by now
	close(release)
	wg.Wait()
	if runs.Load() != 1 {
		t.Errorf("%d doctors ran at once", runs.Load())
	}
	for _, g := range got {
		if len(g) != 1 || g[0].Name != "archive" {
			t.Errorf("a caller got %+v", g)
		}
	}

	// A result is kept for its key as long as its run said, until a drop, which also
	// forgets a run in progress.
	keep := func() ([]healthItem, time.Duration) { runs.Add(1); return []healthItem{{Name: "kept"}}, time.Hour }
	for _, key := range []string{"a", "a", "b", "b"} {
		f.do(key, none, keep)
	}
	if runs.Load() != 3 {
		t.Errorf("%d runs for keys a, a, b, b; want 1 + 2", runs.Load())
	}
	f.drop()
	f.do("b", none, keep)
	inRun := make(chan struct{})
	go func() {
		<-inRun
		f.drop()
	}()
	f.do("c", none, func() ([]healthItem, time.Duration) { close(inRun); time.Sleep(20 * time.Millisecond); return keep() })
	f.do("c", none, keep)
	if runs.Load() != 6 {
		t.Errorf("%d runs after the drops, want 6", runs.Load())
	}

	// A run that panics lets its waiters go with the fallback, [] and not null, and the
	// next caller runs again.
	release, started = make(chan struct{}), make(chan struct{})
	go func() {
		defer func() { recover() }()
		f.do("p", none, func() ([]healthItem, time.Duration) { close(started); <-release; panic("doctor") })
	}()
	<-started
	waited := make(chan []healthItem)
	go func() { waited <- f.do("p", none, keep) }()
	time.Sleep(50 * time.Millisecond)
	close(release)
	if g := <-waited; g == nil || len(g) != 0 {
		t.Errorf("a caller waiting on a panic got %#v", g)
	}
	if g := f.do("p", none, keep); len(g) != 1 || runs.Load() != 7 {
		t.Errorf("after a panic: %v, %d runs", g, runs.Load())
	}
}

// Minor PA M1: a caller that comes after a drop, while a run from before it is still
// going, gets a fresh run's answer, not the one from before the drop.
func TestFlightAfterADropRunsAgain(t *testing.T) {
	var f flight[string]
	release, started := make(chan struct{}), make(chan struct{})
	first := make(chan string)
	go func() {
		first <- f.do("", "", func() (string, time.Duration) { close(started); <-release; return "before", time.Hour })
	}()
	<-started
	f.drop() // a job ended while the walk ran
	after := make(chan string)
	go func() { after <- f.do("", "", func() (string, time.Duration) { return "after", time.Hour }) }()
	time.Sleep(50 * time.Millisecond) // waiting on the first run by now
	close(release)
	if got := <-first; got != "before" {
		t.Errorf("the run's own caller got %q", got)
	}
	if got := <-after; got != "after" {
		t.Errorf("a caller after the drop got %q, want a fresh run's answer", got)
	}
}

func TestActivityPlainWords(t *testing.T) {
	t.Setenv("CLAUDE_SESSIONS_MACHINE", "LAP-1")
	isolateUI(t)
	for _, c := range []struct{ line, want string }{
		{"PUSH: 5 copied, 3 unchanged, 4 session(s), 2 project(s) -> /data/arch", "Backed up 5 changed files"},
		{"PUSH: 1 copied, 3 unchanged, 4 session(s), 2 project(s) -> /data/arch", "Backed up 1 changed file"},
		{"PUSH: 0 copied, 3 unchanged, 4 session(s), 2 project(s) -> /data/arch", ""},
		{"ZIP: 2 conversation(s), 1.0M -> 200.0K", "Compressed 2 conversations"},
		{"ZIP: " + qbWeb + "/" + qWeb + " is over 250 MB; it stays loose", ""},
		{"PRUNE: " + qGone + " (/Users/you/dev/app, last message 2026-01-01, 1.0 MB)", "Deleted Conversation a1000000 from the archive"},
		{"FORK: " + qWeb + " was continued here and on another machine; the archived copy is kept", "A conversation continued on two computers was kept as two"},
		{"ERROR: manifest: open /Users/you/x: permission denied", "A backup did not finish. The details are in the log."},
		{"SKIP: another run holds the lock", "A backup did not finish. The details are in the log."},
		{"TIMEOUT: a per-reply push gave up after 20s", "A backup did not finish. The details are in the log."},
		{"ABORT: still running after 25m0s", "A backup did not finish. The details are in the log."},
		{"UI: Deleted 3 conversations from the archive", "Deleted 3 conversations from the archive"},
		{"RENDER: client-portal is published again (another computer ran render --include client-portal); run render --exclude client-portal to withdraw it", "Pages for client-portal were published again by another computer"},
		{"KEEP-DAYS: counting age up to 2026-11-25 10:00 (LAP-2's last backup, 2026-11-24 09:00, + 25 h), not 2026-11-26 10:00", ""},
		{"WARN: memory/x: permission denied", ""},
		{"HTML: 3 rendered, 201 up to date", ""},
		{"PANEL: conversations: open /Users/you: denied", ""},
	} {
		root := t.TempDir() // no archive: this computer's log only
		appendLog(t, root, time.Now().Add(-time.Minute), c.line)
		got := queryActivity(root)
		if c.want == "" && len(got) != 0 || c.want != "" && (len(got) != 1 || got[0].Text != c.want || got[0].At.IsZero()) {
			t.Errorf("%q gave %+v, want %q", c.line, got, c.want)
		}
	}

	// Newest first, five at most, with the other computers' backups.
	root, dest := queryFixture(t)
	now := time.Now().Truncate(time.Second)
	writeShard(t, dest, archive.Shard{Machine: "LAP-2", LastPush: now.Add(-150 * time.Second)})
	writeShard(t, dest, archive.Shard{Machine: "LAP-3"}) // no lastPush: left out
	writeFile(t, filepath.Join(claude.ProjectsDir(root), qbApp, qGone+".jsonl"), queryTranscript("/Users/you/dev/app", "Removed one"))
	appendLog(t, root, now.Add(-6*time.Minute), "PUSH: 7 copied, 0 unchanged, 1 session(s), 1 project(s)")
	appendLog(t, root, now.Add(-4*time.Minute), "ZIP: 1 conversation(s), 1.0M -> 200.0K")
	appendLog(t, root, now.Add(-3*time.Minute), "PRUNE: "+qGone+" (/Users/you/dev/app, last message 2026-01-01, 1.0 MB)")
	appendLog(t, root, now.Add(-2*time.Minute), "WARN: something", "UI: Brought in 141 conversations")
	f, _ := os.OpenFile(filepath.Join(root, "session-sync.log"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("a stack line with no time\n\n")
	f.Close()
	appendLog(t, root, now.Add(-time.Minute), "PUSH: 2 copied, 0 unchanged, 1 session(s), 1 project(s)")
	var texts []string
	for _, it := range queryActivity(root) {
		texts = append(texts, it.Text)
	}
	if want := []string{"Backed up 2 changed files", "Brought in 141 conversations", "LAP-2 backed up",
		"Deleted Removed one from the archive", "Compressed 1 conversation"}; !reflect.DeepEqual(texts, want) {
		t.Errorf("activity %q\nwant %q", texts, want)
	}
}

func TestArchiveLabel(t *testing.T) {
	home, _ := os.UserHomeDir()
	for _, c := range []struct{ dest, provider, display string }{
		{`G:\My Drive\claude-sessions`, "Google Drive", "My Drive/claude-sessions"},
		{"/Users/you/Library/CloudStorage/GoogleDrive-you@example.com/My Drive/claude-sessions", "Google Drive", "My Drive/claude-sessions"},
		{"/Users/you/Library/CloudStorage/GoogleDrive-you@example.com/Shared drives/team/claude-sessions", "Google Drive", "Shared drives/team/claude-sessions"},
		{"~/Library/CloudStorage/OneDrive-x/claude-sessions", "OneDrive", "claude-sessions"},
		{`C:\Users\you\OneDrive - Example\claude-sessions`, "OneDrive", "claude-sessions"},
		{"/Users/you/Dropbox/claude-sessions", "Dropbox", "claude-sessions"},
		{"/Users/you/Library/Mobile Documents/com~apple~CloudDocs/claude-sessions", "iCloud Drive", "claude-sessions"},
		{"/data/arch", "", "/data/arch"},
		{filepath.Join(home, "arch"), "", "~" + string(filepath.Separator) + "arch"},
	} {
		if p, d := archiveLabel(c.dest); p != c.provider || d != c.display {
			t.Errorf("%s gave %q, %q; want %q, %q", c.dest, p, d, c.provider, c.display)
		}
	}
}

func TestDeletedTitles(t *testing.T) {
	root, dest := queryFixture(t)
	items := queryDeleted(root, dest)
	if len(items) != 1 {
		t.Fatalf("items %+v", items)
	}
	// Another computer's conversation is named by the archive's index, which keeps its
	// row after its files are gone, never by reading the archived copy.
	if it := items[0]; it.ID != qGone || it.Title != "Removed one" || it.Here || it.Project != "app" || it.DeletedAt.IsZero() {
		t.Errorf("without a copy here: %+v", it)
	}
	writeFile(t, filepath.Join(claude.ProjectsDir(root), qbApp, qGone+".jsonl"), queryTranscript("/Users/you/dev/app", "Removed one"))
	if items = queryDeleted(root, dest); len(items) != 1 || items[0].Title != "Removed one" || !items[0].Here {
		t.Errorf("with a copy here: %+v", items)
	}
	// No index row and a bucket no project groups: the id, and the folder of the path
	// the computer's manifest records; with no bucket at all, no project.
	writeJSON(t, filepath.Join(archive.DeletedDir(dest), "LAP-3.json"), map[string]any{"schemaVersion": 1, "machine": "LAP-3", "entries": []archive.DeletedEntry{
		{ID: "d5000000-0000-4000-8000-000000000001", Buckets: []string{"-legacy-tools"}, DeletedAt: time.Now()},
		{ID: "d5000000-0000-4000-8000-000000000002", DeletedAt: time.Now().Add(-time.Minute)}}})
	shards, _, _ := archive.ReadShards(dest)
	shard := shards["LAP-3"]
	shard.Projects["-legacy-tools"] = archive.Project{Path: `D:\old\tools\`}
	writeShard(t, dest, shard)
	items = queryDeleted(root, dest)
	if len(items) != 3 || items[0].Title != "Conversation d5000000" || items[0].Project != "tools" || items[1].Project != "" {
		t.Errorf("without an index row: %+v", items)
	}
	if err := os.Remove(filepath.Join(archive.DeletedDir(dest), "LAP-3.json")); err != nil {
		t.Fatal(err)
	}
	// The 20 newest, newest first, and none continued since (Ruling P50).
	var more []archive.DeletedEntry
	for i := 0; i < 25; i++ {
		more = append(more, archive.DeletedEntry{ID: fmt.Sprintf("d4000000-0000-4000-8000-%012d", i), DeletedAt: time.Now().Add(-time.Duration(i) * time.Hour)})
	}
	more = append(more, archive.DeletedEntry{ID: qHome, DeletedAt: time.Now().AddDate(0, 0, -20)}) // continued 10 days ago
	writeJSON(t, filepath.Join(archive.DeletedDir(dest), "LAP-2.json"), map[string]any{"schemaVersion": 1, "machine": "LAP-2", "entries": more})
	items = queryDeleted(root, dest)
	if len(items) != 20 || items[0].ID != more[0].ID || items[19].ID != more[19].ID {
		t.Errorf("%d items, first %+v", len(items), items[0])
	}
	if st := queryStorage(root, time.Now(), nil); st.Deleted.Count != 26 || st.Deleted.Here != 1 { // qGone, 25 more; not qHome
		t.Errorf("deleted %+v", st.Deleted)
	}
}

// Ruling P10: one Never delete row per project, whose Arg config unkeep takes to remove
// every entry keep stored for it; no remote's token reaches the page.
func TestRetentionOneRowPerProject(t *testing.T) {
	_, dest := queryFixture(t)
	for _, arg := range []string{"/Users/you/dev/web-app", qForked} {
		if err := keepOne(quietStreams().Out, dest, arg); err != nil {
			t.Fatal(err)
		}
	}
	ret, err := archive.LoadRetention(dest)
	if err != nil || len(ret.Keep) != 4 { // notes, web-app's remote and its folder name, qForked
		t.Fatalf("%v %+v", err, ret.Keep)
	}
	v := queryRetention(dest)
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "placeholder-token") || !strings.Contains(string(b), `"keep":[`) || !strings.Contains(string(b), `"holdouts":[`) {
		t.Errorf("retention JSON %s", b)
	}
	var rows []string
	for _, k := range v.Keep {
		rows = append(rows, k.Kind+" "+k.Name)
	}
	sort.Strings(rows)
	if want := []string{"conversation Ask the pelican", "project notes", "project web-app"}; !reflect.DeepEqual(rows, want) {
		t.Errorf("rows %q, want %q", rows, want)
	}
	for _, k := range v.Keep {
		// A current bucket path, which unkeeps every entry keep stored (Ruling P10).
		if k.Name == "web-app" && (len(k.Paths) != 2 || k.Arg != "/Users/you/dev/web-app") || k.Name == "notes" && k.Arg != "/home/you/notes" {
			t.Errorf("row %+v", k)
		}
		if err := unkeepOne(quietStreams().Out, dest, k.Arg); err != nil {
			t.Errorf("unkeep %q: %v", k.Arg, err)
		}
	}
	if ret, _ := archive.LoadRetention(dest); len(ret.Keep) != 0 {
		t.Errorf("left kept: %+v", ret.Keep)
	}

	// A remote no bucket reads any more: its row names it without the token.
	writeJSON(t, archive.RetentionPath(dest), archive.Retention{KeepDays: 120, Keep: []archive.KeepEntry{
		{Remote: "https://you:placeholder-token@example.com/o/gone.git"}}})
	v = queryRetention(dest)
	if b, _ := json.Marshal(v); len(v.Keep) != 1 || v.KeepDays != 120 || strings.Contains(string(b), "placeholder-token") {
		t.Errorf("a remote with no bucket: %s", b)
	}
	if err := unkeepOne(quietStreams().Out, dest, v.Keep[0].Arg); err != nil {
		t.Errorf("unkeep %q: %v", v.Keep[0].Arg, err)
	}
}

func TestStorageView(t *testing.T) {
	root, dest := queryFixture(t)
	writeFile(t, filepath.Join(dest, "work", "LAP-1", "reports", "a.txt"), strings.Repeat("w", 1000))
	writeFile(t, filepath.Join(dest, "INDEX.md"), strings.Repeat("o", 100))
	last := &cleanSummary{At: time.Now(), Copies: 2, Bytes: 300}
	st := queryStorage(root, time.Now(), last)
	if st.Archive == nil || st.Sizes.Work != 1000 || st.Sizes.Other < 100 || st.Sizes.Conversations == 0 || st.Duplicates != last {
		t.Errorf("storage %+v", st)
	}
	// Loose and idle 30 days: qKept (400 days), qForked (40) and qAPI2 (100) and qAmb (20 is not), qGone (60).
	var idle int64
	for _, p := range []string{filepath.Join(qbNotes, qKept), filepath.Join(qbAPI, qForked), filepath.Join(qbWinAPI, qAPI2), filepath.Join(qbApp, qGone)} {
		info, err := os.Stat(filepath.Join(archive.ProjectsDir(dest), p+".jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		idle += info.Size()
	}
	if st.ZipFrees != idle*7/10 || !st.ZipOn || len(st.ZipWaitsFor) != 2 { // LAP-2 and LAP-3 do not read zips
		t.Errorf("zip frees %d, want %d; on %v, waits for %v", st.ZipFrees, idle*7/10, st.ZipOn, st.ZipWaitsFor)
	}
	if !reflect.DeepEqual(st.Kept.Projects, []string{"notes"}) || st.Kept.Conversations != 0 || st.Deleted.Count != 1 || st.Deleted.Here != 0 {
		t.Errorf("kept %+v deleted %+v", st.Kept, st.Deleted)
	}
	if len(st.Projects) != 4 || st.Projects[3].Name != "4 other projects" || st.Projects[0].Bytes < st.Projects[1].Bytes {
		t.Errorf("projects %+v", st.Projects)
	}
	if st.Projects[0].Key != "remote:github.com/example-org/web-app" || st.Projects[0].Conversations != 2 {
		t.Errorf("biggest project %+v", st.Projects[0])
	}
}

// The archive is shared: a file a sync app half wrote is skipped and counted, never a 500.
func TestViewsSkipUnreadableFiles(t *testing.T) {
	root, dest := queryFixture(t)
	writeFile(t, archive.RetentionPath(dest), "{")
	writeFile(t, filepath.Join(archive.DeletedDir(dest), "LAP-2.json"), "{")
	writeFile(t, filepath.Join(archive.ManifestDir(dest), "LAP-5.json"), "{")
	os.WriteFile(filepath.Join(root, "session-sync.log"), []byte("\x00garbage\n2026-13-99 99:99:99  PUSH: 9 copied\n"), 0o600)
	u := newTestUI(t, uiOpts{ClaudeDir: root, NoOpen: true})
	runUI(t, u)
	cookie := login(t, u)
	for _, r := range u.routes() {
		if r.Method == http.MethodGet && !r.Prefix && r.Path != "/api/jobs/events" {
			var v any
			getJSON(t, u, cookie, r.Path, &v)
		}
	}
	var s summaryView
	getJSON(t, u, cookie, "/api/summary", &s)
	var st storageView
	getJSON(t, u, cookie, "/api/storage", &st)
	var ret retentionView
	getJSON(t, u, cookie, "/api/retention", &ret)
	if !st.RetentionUnreadable || !ret.RetentionUnreadable {
		t.Errorf("retention.json not read, yet storage %v and retention %v", st.RetentionUnreadable, ret.RetentionUnreadable)
	}
	if s.Conversations != 8 || s.Unreadable != 2 || st.Unreadable != 3 || st.KeepDays != 0 {
		t.Errorf("summary %d conversations, %d unreadable; storage %d unreadable", s.Conversations, s.Unreadable, st.Unreadable)
	}
	// An unreadable shard may be a computer that does not read zips: it holds them back.
	if !strings.Contains(strings.Join(st.ZipWaitsFor, ","), "LAP-5") {
		t.Errorf("zip waits for %v", st.ZipWaitsFor)
	}
}

func TestSettingsAndProfileViews(t *testing.T) {
	root, dest := queryFixture(t)
	me := readShard(dest, "LAP-1")
	me.WorkFolders = []archive.WorkFolder{{Path: "/Users/you/reports", Files: 3, Bytes: 10}}
	writeShard(t, dest, me)
	other := readShard(dest, "LAP-2")
	other.WorkFolders = []archive.WorkFolder{{Path: "/Users/you/notes"}, {Path: "/Users/you/todo.txt", Exact: true}, {Path: "/Users/you/api-docs"}}
	writeShard(t, dest, other)
	old := time.Now().Add(-time.Hour)
	writeFile(t, filepath.Join(profileDir(dest), "machines", "LAP-1", "CLAUDE.md"), "one")
	writeFile(t, filepath.Join(profileDir(dest), "machines", "LAP-2", "CLAUDE.md"), "the other")
	os.Chtimes(filepath.Join(profileDir(dest), "machines", "LAP-1", "CLAUDE.md"), old, old)
	set := querySettings(root, true, time.Now(), &profileCache{})
	if set.Archive == nil || set.Config == nil || set.Config.InUse != dest || len(set.WorkFolders) != 1 || set.ProfileDiffs != 1 ||
		!set.FromShortcut || set.Version != version || set.LogPath != filepath.Join(root, "session-sync.log") ||
		set.Pages.Pages != 1 || set.Pages.Transcripts == 0 || len(set.Computers) != 3 || set.Sweep.Mechanism == "" {
		t.Errorf("settings %+v", set)
	}
	// The other computers' folders, sorted, without this one's or a file kept on its own.
	if want := []machinePath{{"LAP-2", "/Users/you/api-docs"}, {"LAP-2", "/Users/you/notes"}}; !reflect.DeepEqual(set.OtherWorkFolders, want) {
		t.Errorf("otherWorkFolders %+v, want %+v", set.OtherWorkFolders, want)
	}
	diffs := queryProfile(dest, &profileCache{})
	if len(diffs) != 1 || diffs[0].File != "CLAUDE.md" || diffs[0].Newest != "LAP-2" || len(diffs[0].Copies) != 2 ||
		diffs[0].Copies[1].Size != int64(len("the other")) {
		t.Errorf("profile %+v", diffs)
	}
}

// idleHere gives this computer its own copy of qZipped, idle 200 days: only the zip's
// index can show the archive holds it.
func idleHere(t *testing.T, root string) {
	t.Helper()
	p := filepath.Join(claude.ProjectsDir(root), qbWeb, qZipped+".jsonl")
	writeFile(t, p, queryTranscript("/Users/you/dev/web-app", "Map the stock tables"))
	old := time.Now().AddDate(0, 0, -200)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
}

// Ruling P54: the panel's health reads no archived conversation: no transcript's head,
// no zip's index. What it leaves out says so.
func TestLightHealthReadsNoConversation(t *testing.T) {
	isolateUI(t)
	root, _ := queryFixture(t)
	idleHere(t, root)
	indexes, unpacked := zipReads(t)
	var heads atomic.Int32
	hook(t, &readHead, func(string) { heads.Add(1) })

	items, keep := queryHealth(root, t.Logf, &profileCache{})
	if heads.Load() != 0 || len(*indexes)+len(*unpacked) != 0 {
		t.Errorf("light health read %d heads, zips %v %v", heads.Load(), *indexes, *unpacked)
	}
	byName := map[string]healthItem{}
	for _, it := range items {
		byName[it.Name] = it
	}
	for _, name := range []string{"archived", "archive files"} {
		if it := byName[name]; it.Level != "info" || !strings.Contains(it.Text, "full check") {
			t.Errorf("%s: %+v", name, it)
		}
	}
	if keep != time.Minute {
		t.Errorf("kept %v", keep)
	}
	// The full doctor reads both, so the light one really left them out.
	rep, _ := runDoctor(quietStreams(), doctorOpts{ClaudeDir: root, NoProbe: true})
	if heads.Load() == 0 || len(*indexes) == 0 {
		t.Errorf("the full doctor read %d heads and zips %v: the fixture proves nothing", heads.Load(), *indexes)
	}
	for _, c := range rep.Checks {
		if c.Detail == inFullCheck {
			t.Errorf("the full doctor left %q to itself", c.Name)
		}
	}
	// A conversation missing from the archive is still found without a zip read.
	writeFile(t, filepath.Join(claude.ProjectsDir(root), qbWeb, "e5000000-0000-4000-8000-000000000001.jsonl"), "{}\n")
	old := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(claude.ProjectsDir(root), qbWeb, "e5000000-0000-4000-8000-000000000001.jsonl"), old, old)
	if c := archivedCheck(root, rootDest(t, root), buckets(t, root), true); c.Level != levelFail {
		t.Errorf("light archived check %+v", c)
	}
}

func rootDest(t *testing.T, root string) string {
	t.Helper()
	dest, _, err := archive.ResolveDestination(root, "")
	if err != nil {
		t.Fatal(err)
	}
	return dest
}

func buckets(t *testing.T, root string) []claude.Bucket {
	t.Helper()
	b, err := claude.ListBuckets(claude.ProjectsDir(root))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Ruling I1: reloads on a drive that stopped answering leave one blocked read, not one
// each: the answer is kept 5 minutes.
func TestHealthOnAStuckDriveLeavesOneReadBehind(t *testing.T) {
	root, _ := queryFixture(t)
	u := newTestUI(t, uiOpts{ClaudeDir: root, NoOpen: true})
	var blocked atomic.Int32
	release := make(chan struct{})
	hook(t, &archiveLimit, 50*time.Millisecond)
	hook(t, &beforeArchive, func(string) {
		blocked.Add(1)
		<-release
		blocked.Add(-1)
	})
	t.Cleanup(func() { close(release) }) // before the hooks are put back
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u.healthNow()
		}()
	}
	wg.Wait()
	var items []healthItem
	for i := 0; i < 5; i++ {
		items = u.healthNow()
	}
	if n := blocked.Load(); n != 1 {
		t.Errorf("%d reads of the archive left blocked after 10 loads, want 1", n)
	}
	if !slices.ContainsFunc(items, func(it healthItem) bool { return it.Name == "archive checks" }) {
		t.Errorf("no skipped checks said: %+v", items)
	}
	u.health.mu.Lock()
	kept := time.Until(u.health.last.until)
	u.health.mu.Unlock()
	if kept < 4*time.Minute {
		t.Errorf("kept %v once the archive stopped answering, want 5 minutes", kept)
	}
}

// Ruling I4: an archive folder that is a link is walked; a link inside it is not.
func TestLinkedArchiveIsWalked(t *testing.T) {
	root, dest := queryFixture(t)
	link := filepath.Join(t.TempDir(), "archive-link")
	if err := os.Symlink(dest, link); err != nil {
		t.Skip("no symbolic links here:", err)
	}
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "big.bin"), strings.Repeat("x", 5000))
	if err := os.MkdirAll(filepath.Join(dest, "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dest, "work", "linked")); err != nil {
		t.Fatal(err)
	}
	plain := queryStorage(root, time.Now(), nil)
	writeJSON(t, archive.ConfigPath(root), map[string]string{"destination": link})
	linked := queryStorage(root, time.Now(), nil)
	if linked.Sizes != plain.Sizes || linked.Sizes.Conversations == 0 || linked.Sizes.Work != 0 || linked.ZipFrees != plain.ZipFrees {
		t.Errorf("through the link %+v, directly %+v", linked.Sizes, plain.Sizes)
	}
	secret := filepath.Join("projects", qbWeb, ".credentials.json")
	writeFile(t, filepath.Join(dest, secret), "{}")
	writeFile(t, filepath.Join(outside, ".credentials.json"), "{}") // behind the link inside: not the archive's
	if got := findSecrets(link); !reflect.DeepEqual(got, []string{filepath.Join(link, secret)}) {
		t.Errorf("secrets through the link: %v", got)
	}
}

// Ruling I5: Storage walks once in 5 minutes, until a panel job ends; the duplicates
// check it shows is always this run's last.
func TestStorageKeptUntilAJobEnds(t *testing.T) {
	root, dest := queryFixture(t)
	u := newTestUI(t, uiOpts{ClaudeDir: root, NoOpen: true})
	runUI(t, u)
	cookie := login(t, u)
	var a, b, c storageView
	getJSON(t, u, cookie, "/api/storage", &a)
	writeFile(t, filepath.Join(dest, "work", "LAP-1", "x.txt"), strings.Repeat("w", 1000))
	u.mu.Lock()
	u.lastClean = &cleanSummary{Copies: 2}
	u.mu.Unlock()
	getJSON(t, u, cookie, "/api/storage", &b)
	if b.Sizes != a.Sizes || a.Duplicates != nil || b.Duplicates == nil || b.Duplicates.Copies != 2 {
		t.Errorf("kept: %+v then %+v, duplicates %v", a.Sizes, b.Sizes, b.Duplicates)
	}
	u.dropCaches()
	getJSON(t, u, cookie, "/api/storage", &c)
	if c.Sizes.Work != a.Sizes.Work+1000 {
		t.Errorf("after a job: work %d, want %d", c.Sizes.Work, a.Sizes.Work+1000)
	}
}

// A walk kept for minutes is never served for an archive gone since: reachability comes
// first, as in every other view.
func TestStorageUnreachableIsNotTheKeptWalk(t *testing.T) {
	root, dest := queryFixture(t)
	u := newTestUI(t, uiOpts{ClaudeDir: root, NoOpen: true})
	runUI(t, u)
	cookie := login(t, u)
	var a, b storageView
	if getJSON(t, u, cookie, "/api/storage", &a); a.Problem != "" || a.Archive == nil || a.Sizes.Conversations == 0 {
		t.Fatalf("reachable: %+v", a)
	}
	if err := os.Rename(dest, dest+"-away"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Rename(dest+"-away", dest) })
	if getJSON(t, u, cookie, "/api/storage", &b); b.Problem != "unreachable" || b.Archive != nil || b.Sizes.Conversations != 0 {
		t.Errorf("unreachable, but the kept walk was served: %+v", b)
	}
}

// Ruling I3: copies of different sizes differ unread; one size is compared by hash; the
// panel's cache reads nothing while every copy's size and time stand.
func TestProfileDiffsReadOnlyWhatTheyMust(t *testing.T) {
	_, dest := queryFixture(t)
	copyOf := func(machine, rel string) string {
		return filepath.Join(profileDir(dest), "machines", machine, filepath.FromSlash(rel))
	}
	for _, f := range []struct{ machine, rel, body string }{
		{"LAP-1", "CLAUDE.md", "one"}, {"LAP-2", "CLAUDE.md", "three"}, // sizes differ
		{"LAP-1", "RTK.md", "same"}, {"LAP-2", "RTK.md", "same"},
		{"LAP-1", "skills/x/SKILL.md", "abcd"}, {"LAP-2", "skills/x/SKILL.md", "abce"}, // one size
		{"LAP-3", "only.md", "alone"},
	} {
		writeFile(t, copyOf(f.machine, f.rel), f.body)
	}
	want := []string{"CLAUDE.md", "skills/x/SKILL.md"}
	if got := profileDiffs(dest); !reflect.DeepEqual(got, want) {
		t.Errorf("diffs %v, want %v", got, want)
	}
	var c profileCache
	if got := c.diffs(dest); !reflect.DeepEqual(got, want) {
		t.Errorf("cached diffs %v", got)
	}
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		return // chmod 000 does not stop reads
	}
	var all []string
	filepath.WalkDir(profileDir(dest), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			all = append(all, p)
		}
		return nil
	})
	for _, p := range all {
		os.Chmod(p, 0)
	}
	t.Cleanup(func() {
		for _, p := range all {
			os.Chmod(p, 0o644)
		}
	})
	if got := profileDiffs(dest); !slices.Contains(got, "CLAUDE.md") {
		t.Errorf("copies of different sizes were read to compare: %v", got)
	}
	if got := c.diffs(dest); !reflect.DeepEqual(got, want) {
		t.Errorf("a second load read the copies: %v", got)
	}
	// A copy of another size, its time put back, is compared again.
	os.Chmod(copyOf("LAP-2", "RTK.md"), 0o644)
	st, _ := os.Stat(copyOf("LAP-2", "RTK.md"))
	writeFile(t, copyOf("LAP-2", "RTK.md"), "changed")
	os.Chtimes(copyOf("LAP-2", "RTK.md"), st.ModTime(), st.ModTime())
	if got := c.diffs(dest); !slices.Contains(got, "RTK.md") {
		t.Errorf("a changed copy was not compared again: %v", got)
	}
}

// Minor PA M5: Settings asks for settings, profile and health at once; on a cold cache
// they compare the copies once between them, not each.
func TestProfileComparedOnceForCallersAtOnce(t *testing.T) {
	_, dest := queryFixture(t)
	writeFile(t, filepath.Join(profileDir(dest), "machines", "LAP-1", "CLAUDE.md"), "one")
	writeFile(t, filepath.Join(profileDir(dest), "machines", "LAP-2", "CLAUDE.md"), "three")
	var runs atomic.Int32
	release := make(chan struct{})
	hook(t, &compareCopies, func(c map[string][]profileCopy) []string {
		runs.Add(1)
		<-release
		return diffsOf(c)
	})
	var c profileCache
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := c.diffs(dest); !reflect.DeepEqual(got, []string{"CLAUDE.md"}) {
				t.Errorf("diffs %v", got)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond) // all three have asked by now
	close(release)
	wg.Wait()
	if n := runs.Load(); n != 1 {
		t.Errorf("compared %d times for three callers at once", n)
	}
}

// Minor m3: a manifest that cannot be read may hold back every computer.
func TestHoldoutsWithNoManifest(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("chmod 000 does not stop reads here")
	}
	root, dest := queryFixture(t)
	if err := os.Chmod(archive.ManifestDir(dest), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(archive.ManifestDir(dest), 0o755) })
	every := []string{everyComputer}
	if st := queryStorage(root, time.Now(), nil); !reflect.DeepEqual(st.ZipWaitsFor, every) || st.Unreadable == 0 {
		t.Errorf("zip waits for %v, unreadable %d", st.ZipWaitsFor, st.Unreadable)
	}
	if r := queryRetention(dest); !reflect.DeepEqual(r.Holdouts, every) {
		t.Errorf("holdouts %v", r.Holdouts)
	}
}

// Minor m4: one of a thing takes the singular.
func TestPlainWordsSingular(t *testing.T) {
	for _, c := range []struct {
		c    check
		want string
	}{
		{check{"archived", levelFail, "1 session(s) that ended over 40 minutes ago are not in the archive, e.g. b/x", ""}, "1 conversation that ended over 40 minutes ago isn't in the archive."},
		{check{"profile", levelInfo, "1 rule/skill file(s) differ between machines: CLAUDE.md", ""}, "1 rule and skill file differs between your computers."},
		{check{"profile", levelOK, "1 rule/skill file(s) archived", ""}, "1 rule and skill file is in the archive."},
		{check{"manifest", levelWarn, "1 of 4 archived bucket(s) have no usable identity: x", ""}, "1 project folder in the archive holds notes only, so the other computers can't tell where it belongs."},
		{check{"local buckets", levelInfo, "1 local bucket(s) hold memory but no transcripts", ""}, "1 project folder on this computer holds notes but no conversations."},
		{check{"html", levelOK, "1 page(s) for 1 transcript(s)", ""}, "1 readable page, up to date."},
		{check{"archived", levelFail, "3 session(s) that ended over 40 minutes ago are not in the archive, e.g. b/x", ""}, "3 conversations that ended over 40 minutes ago aren't in the archive."},
	} {
		if got := plainCheck(c.c).Text; !strings.HasPrefix(got, c.want) {
			t.Errorf("%s %q gave %q", c.c.Name, c.c.Detail, got)
		}
	}
}
