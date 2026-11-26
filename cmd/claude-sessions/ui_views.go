package main

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/hostagent"
	"github.com/ashiqfardus/claude-sessions-sync/internal/identity"
)

// The panel's other views: overview, storage, health, activity, Never delete, deleted,
// settings and rules. Like the Conversations list, none opens a conversation in the
// archive, which a folder that streams files would download: each reads the manifest
// once, the index, retention and deleted files, and folders by name and size. A file
// it cannot read is counted and left out, never an error.

type archiveView struct {
	Path string `json:"path"`
	// Provider is the sync app whose folder it is, or "" when unknown: only then may the
	// page name a sync app (Ruling P25).
	Provider string `json:"provider"`
	Display  string `json:"display"`
}

func archiveOf(dest string) *archiveView {
	p, d := archiveLabel(dest)
	return &archiveView{Path: dest, Provider: p, Display: d}
}

// archiveLabel names the sync app whose folder dest is in, and dest as that app shows
// it, from its root: My Drive/claude-sessions. Another folder is dest, the home folder
// shown as ~.
func archiveLabel(dest string) (provider, display string) {
	parts := strings.FieldsFunc(dest, func(r rune) bool { return r == '/' || r == '\\' })
	after := func(i int) string {
		if i+1 == len(parts) {
			return parts[i] // the app's root itself
		}
		return strings.Join(parts[i+1:], "/")
	}
	for i, p := range parts {
		switch l := strings.ToLower(p); {
		case strings.HasPrefix(l, "googledrive-"): // macOS: GoogleDrive-<account>/My Drive/...
			return "Google Drive", after(i)
		case l == "my drive": // Windows: G:\My Drive\...
			return "Google Drive", strings.Join(parts[i:], "/")
		case l == "onedrive" || strings.HasPrefix(l, "onedrive-") || strings.HasPrefix(l, "onedrive "):
			return "OneDrive", after(i)
		case l == "dropbox" || strings.HasPrefix(l, "dropbox-") || strings.HasPrefix(l, "dropbox "):
			return "Dropbox", after(i)
		case l == "com~apple~clouddocs":
			return "iCloud Drive", after(i)
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(dest, home+string(filepath.Separator)) {
		return "", "~" + dest[len(home):]
	}
	return "", dest
}

type computerView struct {
	Name    string `json:"name"`
	Version string `json:"version"` // "" for a build older than the field
	// State is ok (backed up within 2 hours), update (not Ready, or no LastPush) or late.
	State string `json:"state"`
	This  bool   `json:"this"`
	// Ready is reading zips and honouring the deleted list: compressing and deleting
	// wait for every computer to be.
	Ready         bool      `json:"ready"`
	LastPush      time.Time `json:"lastPush"` // zero when unknown (Ruling P33)
	Conversations int       `json:"conversations"`
}

type summaryView struct {
	Machine       string         `json:"machine"`
	Version       string         `json:"version"`
	Problem       string         `json:"problem"` // none or unreachable, with no Archive
	Archive       *archiveView   `json:"archive"`
	Computers     []computerView `json:"computers"`
	Conversations int            `json:"conversations"`
	WorkFolders   int            `json:"workFolders"`
	LastBackup    time.Time      `json:"lastBackup"` // this computer's last PUSH: line; zero for none
	Unreadable    int            `json:"unreadable"`
}

func querySummary(root string, now time.Time) summaryView {
	v := summaryView{Machine: machineName(), Version: version, Computers: []computerView{}}
	v.LastBackup, _ = lastPush(root)
	dest, problem := panelDest(root)
	if problem != "" {
		v.Problem = problem
		return v
	}
	v.Archive = archiveOf(dest)
	shards, bad, err := archive.ReadShards(dest)
	rows, _, n := archivedNow(dest, shards)
	v.Unreadable = len(bad) + n + errCount(err)
	v.Computers = computersOf(shards, rows, v.LastBackup, now)
	ids := map[string]bool{}
	for _, s := range rows {
		ids[strings.ToLower(s.ID)] = true
	}
	v.Conversations = len(ids)
	for _, s := range shards {
		v.WorkFolders += len(s.WorkFolders)
	}
	return v
}

func errCount(err error) int {
	if err != nil {
		return 1
	}
	return 0
}

// computersOf is every computer with a shard, by name. This computer's last backup is
// the newer of its shard's and logged, its log's last PUSH: line.
func computersOf(shards map[string]archive.Shard, rows []archive.Session, logged, now time.Time) []computerView {
	count := map[string]int{}
	for _, s := range rows {
		count[s.Machine]++
	}
	out := []computerView{}
	for name, s := range shards {
		c := computerView{Name: name, Version: s.ToolVersion, This: strings.EqualFold(name, machineName()),
			Ready: s.ReadsZips && s.HonorsDeleted, LastPush: s.LastPush, Conversations: count[name]}
		if c.This && logged.After(c.LastPush) {
			c.LastPush = logged
		}
		switch {
		case !c.Ready || c.LastPush.IsZero():
			c.State = "update"
		case now.Sub(c.LastPush) <= 2*time.Hour:
			c.State = "ok"
		default:
			c.State = "late"
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// archivedNow is every index row of a conversation in the archive, and the deleted list
// as it stands: an entry undone, or a conversation continued since, is in the archive
// again (Ruling P50). bad counts the deleted lists it could not read.
func archivedNow(dest string, shards map[string]archive.Shard) (rows []archive.Session, deleted map[string]archive.DeletedEntry, bad int) {
	rows = archive.SessionsOf(dest, shards)
	deleted, names, err := archive.LoadDeletedEach(dest)
	if deleted == nil {
		deleted = map[string]archive.DeletedEntry{}
	}
	newest := map[string]time.Time{}
	for _, s := range rows {
		if id := strings.ToLower(s.ID); s.Updated.After(newest[id]) {
			newest[id] = s.Updated
		}
	}
	for id, d := range deleted {
		if d.Undone || newest[id].After(d.DeletedAt) {
			delete(deleted, id)
		}
	}
	rows = slices.DeleteFunc(rows, func(s archive.Session) bool {
		_, gone := deleted[strings.ToLower(s.ID)]
		return gone
	})
	return rows, deleted, len(names) + errCount(err)
}

// holdouts is archive.Holdouts on shards already read. A shard that cannot be read may
// be a computer without what has looks for, so it holds back too, by its file name.
func holdouts(shards map[string]archive.Shard, bad []string, has func(archive.Shard) bool) []string {
	out := []string{}
	for name, s := range shards {
		if !has(s) {
			out = append(out, name)
		}
	}
	for _, b := range bad {
		out = append(out, strings.TrimSuffix(b, filepath.Ext(b)))
	}
	sort.Strings(out)
	return out
}

// localTranscripts is this computer's conversations, by lower-cased id.
func localTranscripts(root string) map[string]string {
	out := map[string]string{}
	buckets, _ := claude.ListBuckets(claude.ProjectsDir(root))
	for _, b := range buckets {
		for _, t := range b.Transcripts {
			out[strings.ToLower(t.ID)] = t.Path
		}
	}
	return out
}

// convTitle is a conversation's first prompt, read from this computer's copy at path,
// or "Conversation <the start of its id>".
func convTitle(path, id string) string {
	if path != "" {
		if s, err := claude.ScanHead(path, headScanLines); err == nil && strings.TrimSpace(s.FirstPrompt) != "" {
			return s.FirstPrompt
		}
	}
	if len(id) > 8 {
		id = id[:8]
	}
	return "Conversation " + id
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// --- Storage ------------------------------------------------------------------

type storageProject struct {
	Key           string `json:"key"` // the project filter's key; "" for the other projects
	Name          string `json:"name"`
	Conversations int    `json:"conversations"`
	Bytes         int64  `json:"bytes"`
}

type storageView struct {
	Problem string       `json:"problem"`
	Archive *archiveView `json:"archive"`
	Sizes   struct {
		Conversations int64 `json:"conversations"` // projects/, forks/ and deleted/
		Work          int64 `json:"work"`
		Other         int64 `json:"other"`
	} `json:"sizes"`
	ZipOn bool `json:"zipOn"`
	// ZipFrees is what compressing the conversations idle for zipIdle would free.
	ZipFrees    int64    `json:"zipFrees"`
	ZipWaitsFor []string `json:"zipWaitsFor"`
	KeepDays    int      `json:"keepDays"`
	Kept        struct {
		Projects      []string `json:"projects"`
		Conversations int      `json:"conversations"`
	} `json:"kept"`
	Deleted struct {
		Count int `json:"count"`
		Here  int `json:"here"` // still on this computer
	} `json:"deleted"`
	Projects []storageProject `json:"projects"` // the 3 biggest, then the rest in one row
	// Duplicates is this run's last duplicates check, nil when there has been none.
	Duplicates *cleanSummary `json:"duplicates"`
	Unreadable int           `json:"unreadable"`
}

func queryStorage(root string, now time.Time, last *cleanSummary) storageView {
	v := storageView{ZipWaitsFor: []string{}, Projects: []storageProject{}, Duplicates: last}
	v.Kept.Projects = []string{}
	dest, problem := panelDest(root)
	if problem != "" {
		v.Problem = problem
		return v
	}
	v.Archive = archiveOf(dest)
	shards, bad, err := archive.ReadShards(dest)
	projects, lbad := archive.MergeShards(dest, shards)
	rows, deleted, n := archivedNow(dest, shards)
	ret, rerr := archive.LoadRetention(dest)
	v.Unreadable = len(bad) + len(lbad) + n + errCount(err) + errCount(rerr)

	cfg, cerr := archive.LoadConfig(root)
	v.ZipOn = cerr == nil && cfg.ZipOn() // push zips nothing with a config it cannot read
	v.ZipWaitsFor = slices.DeleteFunc(holdouts(shards, bad, func(s archive.Shard) bool { return s.ReadsZips }),
		func(name string) bool { return strings.EqualFold(name, machineName()) }) // as zipHoldouts
	v.KeepDays = ret.KeepDays
	opts, keys := groupProjects(projects, homesOf(shards))
	for _, k := range keepRows(ret, projects, opts, rows) {
		if k.Kind == "conversation" {
			v.Kept.Conversations++
		} else {
			v.Kept.Projects = append(v.Kept.Projects, k.Name)
		}
	}
	local := localTranscripts(root)
	v.Deleted.Count = len(deleted)
	for id := range deleted {
		if local[id] != "" {
			v.Deleted.Here++
		}
	}

	// One walk, by name and size: nothing is opened, and no link is followed.
	cutoff := now.Add(-zipIdle)
	loose, idle, inBucket := map[string]int64{}, map[string]bool{}, map[string]int64{}
	_ = filepath.WalkDir(dest, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil // a folder that cannot be listed is left out
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dest, p)
		parts, size := strings.Split(rel, string(filepath.Separator)), info.Size()
		switch parts[0] {
		case "projects", "forks", "deleted":
			v.Sizes.Conversations += size
		case "work":
			v.Sizes.Work += size
		default:
			v.Sizes.Other += size
		}
		if len(parts) < 3 || parts[0] != "projects" && parts[0] != "forks" || !archive.ValidName(parts[1]) {
			return nil
		}
		inBucket[parts[1]] += size
		if parts[0] != "projects" {
			return nil
		}
		// A conversation's transcript, or a file in its own folder, which its zip holds too.
		if id, ok := strings.CutSuffix(parts[2], ".jsonl"); ok && len(parts) == 3 && claude.IsSessionID(id) {
			key := parts[1] + "/" + strings.ToLower(id)
			loose[key] += size
			idle[key] = info.ModTime().Before(cutoff)
		} else if len(parts) > 3 && claude.IsSessionID(parts[2]) {
			loose[parts[1]+"/"+strings.ToLower(parts[2])] += size
		}
		return nil
	})
	for key, old := range idle {
		if old {
			v.ZipFrees += loose[key]
		}
	}
	v.ZipFrees = v.ZipFrees * 7 / 10 // shortcut: a fixed 70% saving; measure from existing zips once doctor may read them

	v.Projects = biggestProjects(opts, keys, inBucket, rows)
	return v
}

// biggestProjects is the three projects using the most space, then one row for the rest.
// A bucket no manifest names is a project of its own, by its folder name.
func biggestProjects(opts []projectOption, keys map[string]string, inBucket map[string]int64, rows []archive.Session) []storageProject {
	names := map[string]string{}
	for _, o := range opts {
		names[o.Key] = o.Name
	}
	type group struct {
		row storageProject
		ids map[string]bool
	}
	groups := map[string]*group{}
	of := func(b string) *group {
		k, gk, name := keys[b], keys[b], names[keys[b]]
		if k == "" {
			gk, name = "bucket:"+b, b
		}
		if groups[gk] == nil {
			groups[gk] = &group{row: storageProject{Key: k, Name: name}, ids: map[string]bool{}}
		}
		return groups[gk]
	}
	for b, n := range inBucket {
		of(b).row.Bytes += n
	}
	for _, s := range rows {
		of(s.Bucket).ids[strings.ToLower(s.ID)] = true
	}
	list := make([]storageProject, 0, len(groups))
	for _, g := range groups {
		g.row.Conversations = len(g.ids)
		list = append(list, g.row)
	}
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		return a.Bytes > b.Bytes || a.Bytes == b.Bytes && a.Name < b.Name
	})
	if len(list) > 3 {
		rest := storageProject{Name: plural(len(list)-3, "other project")}
		for _, p := range list[3:] {
			rest.Conversations += p.Conversations
			rest.Bytes += p.Bytes
		}
		list = append(list[:3], rest)
	}
	return list
}

// --- Never delete ---------------------------------------------------------------

type keepRow struct {
	Kind  string        `json:"kind"` // project or conversation
	Name  string        `json:"name"`
	Arg   string        `json:"arg"` // what config unkeep takes to remove the row
	Paths []machinePath `json:"paths"`
}

type retentionView struct {
	KeepDays int       `json:"keepDays"`
	Keep     []keepRow `json:"keep"`
	// Holdouts are the computers deleting waits for, which do not honour the deleted list.
	Holdouts   []string `json:"holdouts"`
	Unreadable int      `json:"unreadable"`
}

func queryRetention(dest string) retentionView {
	shards, bad, err := archive.ReadShards(dest)
	projects, lbad := archive.MergeShards(dest, shards)
	ret, rerr := archive.LoadRetention(dest)
	opts, _ := groupProjects(projects, homesOf(shards))
	for i := range opts {
		opts[i].Arg = projectArg(projects, opts[i])
	}
	return retentionView{KeepDays: ret.KeepDays, Keep: keepRows(ret, projects, opts, archive.SessionsOf(dest, shards)),
		Holdouts:   holdouts(shards, bad, func(s archive.Shard) bool { return s.HonorsDeleted }),
		Unreadable: len(bad) + len(lbad) + errCount(err) + errCount(rerr)}
}

// keepRows is the Never delete list: a row per conversation, and one per project however
// many entries keep stored for it - its remote and its folder names (Ruling P10). A
// project's Arg is a path matchProject resolves to it, which unkeeps every entry keep
// stored; else the stored value, which unkeep takes as it is. A remote is never given
// raw: it may hold a token.
func keepRows(ret archive.Retention, projects map[string]archive.Project, opts []projectOption, rows []archive.Session) []keepRow {
	byKey := map[string]projectOption{}
	for _, o := range opts {
		byKey[o.Key] = o
	}
	var order []string
	entries := map[string][]archive.KeepEntry{}
	for _, e := range ret.Keep {
		remote := e.Remote
		if remote == "" {
			remote = e.ForRemote // a folder name kept with its project's remote
		}
		key := "leaf:" + strings.ToLower(e.Leaf) // the option keys groupProjects gives
		switch {
		case e.Session != "":
			key = "conversation:" + strings.ToLower(e.Session)
		case remote != "":
			key = "remote:" + identity.NormalRemote(remote)
		}
		if entries[key] == nil {
			order = append(order, key)
		}
		entries[key] = append(entries[key], e)
	}
	titles, where := map[string]string{}, map[string][]machinePath{}
	for _, s := range rows {
		id := strings.ToLower(s.ID)
		if titles[id] == "" {
			titles[id] = s.Prompt
		}
		where[id] = append(where[id], machinePath{s.Machine, s.Project})
	}

	out := []keepRow{}
	for _, key := range order {
		es := entries[key]
		if id, ok := strings.CutPrefix(key, "conversation:"); ok {
			r := keepRow{Kind: "conversation", Name: titles[id], Arg: id, Paths: where[id]}
			if r.Name == "" {
				r.Name = convTitle("", id)
			}
			if r.Paths == nil {
				r.Paths = []machinePath{}
			}
			out = append(out, r)
			continue
		}
		r := keepRow{Kind: "project", Paths: []machinePath{}}
		for _, e := range es {
			if e.Remote != "" {
				r.Arg = identity.RedactRemote(e.Remote) // unkeep compares it by SameRemote
			}
			if e.Leaf != "" && r.Name == "" {
				r.Name = e.Leaf
			}
		}
		if r.Arg == "" {
			r.Arg = r.Name
		}
		if r.Name == "" {
			r.Name = r.Arg
		}
		if o, ok := byKey[key]; ok {
			r.Name = o.Name
			if o.Arg != "" {
				r.Arg = o.Arg
			}
		}
		for _, p := range projects {
			if p.Path != "" && slices.ContainsFunc(es, func(e archive.KeepEntry) bool { return keeps(e, p, projects) }) {
				r.Paths = append(r.Paths, machinePath{p.Owner(), p.Path})
			}
		}
		sort.Slice(r.Paths, func(i, j int) bool {
			a, b := r.Paths[i], r.Paths[j]
			return a.Machine < b.Machine || a.Machine == b.Machine && a.Path < b.Path
		})
		out = append(out, r)
	}
	return out
}

// --- Deleted --------------------------------------------------------------------

type deletedItem struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Project   string    `json:"project"`
	DeletedAt time.Time `json:"deletedAt"`
	Here      bool      `json:"here"` // this computer still has it
}

// queryDeleted is the 20 conversations deleted from the archive most recently.
func queryDeleted(root, dest string) []deletedItem {
	shards, _, _ := archive.ReadShards(dest)
	projects, _ := archive.MergeShards(dest, shards)
	_, deleted, _ := archivedNow(dest, shards)
	opts, keys := groupProjects(projects, homesOf(shards))
	names := map[string]string{}
	for _, o := range opts {
		names[o.Key] = o.Name
	}
	local := localTranscripts(root)
	items := []deletedItem{}
	for id, d := range deleted {
		it := deletedItem{ID: id, DeletedAt: d.DeletedAt, Here: local[id] != ""}
		for _, b := range d.Buckets {
			if k := keys[b]; k != "" {
				it.Project = names[k]
				break
			}
		}
		items = append(items, it)
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		return a.DeletedAt.After(b.DeletedAt) || a.DeletedAt.Equal(b.DeletedAt) && a.ID < b.ID
	})
	if len(items) > 20 {
		items = items[:20]
	}
	for i := range items {
		items[i].Title = convTitle(local[items[i].ID], items[i].ID) // this computer's own copy, never the archive's
	}
	return items
}

// --- Activity -------------------------------------------------------------------

type activityItem struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// queryActivity is the five newest of the other computers' last backups and what this
// computer's last 300 log lines say happened, in plain words.
func queryActivity(root string) []activityItem {
	type found struct {
		activityItem
		pruned string // a conversation deleted, titled once it is among the five
	}
	var items []found
	raw, _ := os.ReadFile(filepath.Join(root, "session-sync.log"))
	lines := strings.Split(string(raw), "\n")
	if len(lines) > 300 {
		lines = lines[len(lines)-300:]
	}
	for i := len(lines) - 1; i >= 0; i-- { // newest first, for lines of the same second
		stamp, rest, ok := strings.Cut(strings.TrimRight(lines[i], "\r"), "  ")
		at, err := time.ParseInLocation("2006-01-02 15:04:05", stamp, time.Local)
		if !ok || err != nil {
			continue // a stack trace's lines carry no time
		}
		if text, pruned := logActivity(rest); text != "" || pruned != "" {
			items = append(items, found{activityItem{at, text}, pruned})
		}
	}
	if dest, problem := panelDest(root); problem == "" {
		shards, _, _ := archive.ReadShards(dest)
		var names []string
		for name, s := range shards {
			if !strings.EqualFold(name, machineName()) && !s.LastPush.IsZero() { // zero is unknown (Ruling P33)
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			items = append(items, found{activityItem{shards[name].LastPush, name + " backed up"}, ""})
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].At.After(items[j].At) })
	out := []activityItem{}
	var local map[string]string
	for _, it := range items[:min(5, len(items))] {
		if it.pruned != "" {
			if local == nil {
				local = localTranscripts(root)
			}
			it.Text = "Deleted " + convTitle(local[it.pruned], it.pruned) + " from the archive"
		}
		out = append(out, it.activityItem)
	}
	return out
}

// logActivity is a log line's text after its time, in plain words, or "" to leave it
// out; or the id of a conversation the age rule deleted.
func logActivity(rest string) (text, pruned string) {
	var n int
	switch {
	case strings.HasPrefix(rest, "PUSH: "):
		if _, err := fmt.Sscanf(rest, "PUSH: %d copied", &n); err == nil && n > 0 {
			return "Backed up " + plural(n, "changed file"), ""
		}
	case strings.HasPrefix(rest, "ZIP: "): // not "ZIP: <conversation> is over 250 MB"
		if _, err := fmt.Sscanf(rest, "ZIP: %d conversation(s)", &n); err == nil {
			return "Compressed " + plural(n, "conversation"), ""
		}
	case strings.HasPrefix(rest, "PRUNE: "):
		if id, _, _ := strings.Cut(strings.TrimPrefix(rest, "PRUNE: "), " "); claude.IsSessionID(id) {
			return "", strings.ToLower(id)
		}
	case strings.HasPrefix(rest, "FORK: "):
		return "A conversation continued on two computers was kept as two", ""
	case strings.HasPrefix(rest, "ERROR:"), strings.HasPrefix(rest, "SKIP:"), strings.HasPrefix(rest, "TIMEOUT:"),
		strings.HasPrefix(rest, "ABORT:"), strings.HasPrefix(rest, "PANIC:"):
		return "A backup did not finish. The details are in the log.", ""
	case strings.HasPrefix(rest, "UI: "): // an action's report; the panel logs its errors as PANEL:
		return strings.TrimSpace(strings.TrimPrefix(rest, "UI: ")), ""
	case strings.HasPrefix(rest, "RENDER: "):
		if x, _, ok := strings.Cut(strings.TrimPrefix(rest, "RENDER: "), " is published again"); ok {
			return "Pages for " + x + " were published again by another computer", ""
		}
	}
	return "", ""
}

// --- Health ---------------------------------------------------------------------

type healthItem struct {
	Name     string `json:"name"` // doctor's check, which Settings looks items up by
	Level    string `json:"level"`
	Text     string `json:"text"`
	FixLabel string `json:"fixLabel"`
	FixHref  string `json:"fixHref"`
}

// healthFlight runs one doctor at a time, and a caller that comes while it runs gets its
// result (Ruling P15): doctor leaves a slow check's read behind, so a doctor for each
// Overview load would pile up blocked reads while the drive is slow.
type healthFlight struct {
	mu  sync.Mutex
	cur *healthRun
}

type healthRun struct {
	done  chan struct{}
	items []healthItem // set before done is closed
}

// do runs run in the first caller's goroutine, where net/http recovers a panic.
func (f *healthFlight) do(run func() []healthItem) []healthItem {
	f.mu.Lock()
	if r := f.cur; r != nil {
		f.mu.Unlock()
		<-r.done
		return r.items
	}
	r := &healthRun{done: make(chan struct{})}
	f.cur = r
	f.mu.Unlock()
	defer func() { // a panic in run still lets the waiting callers go
		f.mu.Lock()
		f.cur = nil
		f.mu.Unlock()
		close(r.done)
	}()
	r.items = run()
	return r.items
}

// queryHealth is doctor's checks in plain words: failures, warnings, then the rest. It
// writes no test file to the archive.
func queryHealth(root string, logf func(string, ...any)) []healthItem {
	rep, _ := runDoctor(quietStreams(), doctorOpts{ClaudeDir: root, NoProbe: true})
	items := make([]healthItem, 0, len(rep.Checks))
	for _, c := range rep.Checks {
		if plainChecks[c.Name] == nil {
			logf("PANEL: health: %s: %s: %s", c.Name, c.Level, c.Detail) // what the generic sentence points to
		}
		items = append(items, plainCheck(c))
	}
	return sortHealth(items)
}

func sortHealth(items []healthItem) []healthItem {
	rank := map[string]int{"fail": 0, "warn": 1, "ok": 2, "info": 3}
	sort.SliceStable(items, func(i, j int) bool { return rank[items[i].Level] < rank[items[j].Level] })
	return items
}

func plainCheck(c check) healthItem {
	if c.Level == levelWarn && strings.Contains(c.Detail, ": not finished after ") { // any step timed()
		return say(c, "Checking the archive took too long: the folder may be downloading files only when they're opened.", "", "")
	}
	if f := plainChecks[c.Name]; f != nil {
		return f(c)
	}
	return say(c, "One check has something to say. The details are in the log.", "Open the log", toSettings)
}

const (
	toSettings = "#/settings"
	toStorage  = "#/storage"
	toImport   = "#/import"
)

func say(c check, text, fixLabel, fixHref string) healthItem {
	return healthItem{Name: c.Name, Level: string(c.Level), Text: text, FixLabel: fixLabel, FixHref: fixHref}
}

// scanned is the number format reads from detail, 0 when it reads none.
func scanned(detail, format string) int {
	var n int
	fmt.Sscanf(detail, format, &n)
	return n
}

// listAfter is what follows sep in detail: the computers doctor names.
func listAfter(detail, sep string) string {
	_, list, _ := strings.Cut(detail, sep)
	return list
}

// plainChecks gives each of doctor's checks the page's words. A detail is read only for
// a number or the computers it names, never shown: it can hold a path.
var plainChecks = map[string]func(c check) healthItem{
	"claude root": func(c check) healthItem {
		if c.Level == levelFail {
			return say(c, "Claude Code's folder isn't on this computer. Install Claude Code, or set CLAUDE_CONFIG_DIR to where it keeps its files.", "", "")
		}
		return say(c, "Claude Code's folder is in place.", "", "")
	},
	"bucket naming": func(c check) healthItem {
		return say(c, "CLAUDE_CODE_PROJECT_DIR_NAME is set, so Claude Code names project folders by it, not by their path.", "", "")
	},
	"projects": func(c check) healthItem {
		if c.Level == levelFail {
			return say(c, "Claude Code's projects folder can't be read on this computer.", "", "")
		}
		var buckets, transcripts int
		fmt.Sscanf(c.Detail, "%d bucket(s), %d transcript(s)", &buckets, &transcripts)
		return say(c, "This computer has "+plural(transcripts, "conversation")+" in "+plural(buckets, "project folder")+".", "", "")
	},
	"settings.json": func(c check) healthItem {
		if c.Level == levelWarn {
			return say(c, "Claude Code's settings.json can't be read, so the checks of its hooks and of how long it keeps conversations may be wrong.", "", "")
		}
		return say(c, "Claude Code has no settings.json on this computer yet.", "", "")
	},
	"retention": func(c check) healthItem {
		days := scanned(c.Detail, "cleanupPeriodDays = %d")
		if days == 0 {
			days = scanned(c.Detail, "cleanupPeriodDays unset, so Claude Code applies its %d-day default")
		}
		if c.Level == levelWarn {
			return say(c, fmt.Sprintf("Claude Code deletes conversations on this computer after %s. The archive keeps them; raise cleanupPeriodDays in settings.json to keep them here too.", plural(days, "day")), "", "")
		}
		return say(c, fmt.Sprintf("Claude Code keeps conversations on this computer for %s.", plural(days, "day")), "", "")
	},
	"archive": func(c check) healthItem {
		switch {
		case c.Level == levelOK:
			return say(c, "The archive folder can be reached.", "", "")
		case strings.Contains(c.Detail, "no destination"):
			return say(c, "No archive folder yet. Choose the folder your sync app keeps in sync.", "Choose folder", toSettings)
		case strings.Contains(c.Detail, "is not reachable"):
			return say(c, "The archive folder can't be reached right now. Check that your sync app is running.", "Change folder", toSettings)
		case c.Level == levelFail:
			return say(c, "The archive folder setting names a file, not a folder. Choose the folder again.", "Choose folder", toSettings)
		}
		return say(c, "The archive folder setting can't be read. Choose the folder again.", "Choose folder", toSettings)
	},
	"archive files": func(c check) healthItem {
		if c.Detail == notRespondingAdvice {
			return say(c, "The archive folder isn't answering. If a sync app keeps it, make it available offline; otherwise check that its drive is connected.", "", "")
		}
		return say(c, "Your archive folder downloads files only when they're opened, which makes backups slow. Make it available offline in your sync app.", "", "")
	},
	"archive checks": func(c check) healthItem {
		return say(c, "Some checks of the archive were skipped because the folder isn't answering.", "", "")
	},
	"archive writable": func(c check) healthItem {
		switch c.Level {
		case levelFail:
			return say(c, "This computer can't write to the archive folder, so nothing can be backed up. Check its permissions and free space.", "", "")
		case levelInfo:
			return say(c, "Writing to the archive folder isn't tested from here, so the panel leaves no test file in it.", "", "")
		}
		return say(c, "This computer can write to the archive folder.", "", "")
	},
	"archive config": func(c check) healthItem {
		return say(c, "The archive folder was found, not chosen. Choose it so every backup uses the same one.", "Choose folder", toSettings)
	},
	"versions": func(c check) healthItem {
		return say(c, "Your other computers run another version of claude-sessions: "+listAfter(c.Detail, "): ")+
			". Install the same version on each; an older one quietly skips newer features.", "See computers", toSettings)
	},
	"name": func(c check) healthItem {
		return say(c, "Another computer uses this computer's name, so each overwrites the other's records. Give one of them its own name.", "Rename", toSettings)
	},
	"last backup": func(c check) healthItem {
		if name, ok := strings.CutSuffix(c.Detail, " -"); ok {
			return say(c, name+" doesn't record when it backs up: it runs an older version.", "See computers", toSettings)
		}
		f := strings.Fields(c.Detail) // the name, then 2006-01-02 15:04
		if len(f) < 3 {
			return say(c, "A computer's last backup isn't known.", "See computers", toSettings)
		}
		return say(c, strings.Join(f[:len(f)-2], " ")+" last backed up "+f[len(f)-2]+" "+f[len(f)-1]+".", "", "")
	},
	"zips": func(c check) healthItem {
		switch {
		case c.Level == levelInfo:
			return say(c, "Compressing old conversations is off.", "Manage storage", toStorage)
		case c.Level == levelOK:
			return say(c, "Conversations nobody has used for 30 days are compressed.", "", "")
		case strings.Contains(c.Detail, "read zips: "):
			return say(c, "Compressing old conversations waits for these computers to update claude-sessions: "+listAfter(c.Detail, "read zips: ")+".", "See computers", toSettings)
		}
		return say(c, "Compressing old conversations waits until every computer's record in the archive can be read.", "See computers", toSettings)
	},
	"prune": func(c check) healthItem {
		switch {
		case c.Level == levelInfo:
			return say(c, "No conversation is deleted for its age.", "Manage storage", toStorage)
		case c.Level == levelOK:
			return say(c, fmt.Sprintf("Conversations whose last message is over %s old are deleted from the archive, except those on Never delete.",
				plural(scanned(c.Detail, "keep-days %d"), "day")), "Manage storage", toStorage)
		case strings.HasPrefix(c.Detail, "nothing is deleted for its age: "):
			return say(c, "Nothing is deleted for its age: the archive's retention settings can't be read. Let your sync app finish, or remove a conflict copy of retention.json.", "", "")
		case strings.Contains(c.Detail, "deleted list: "):
			return say(c, "Deleting old conversations waits for these computers to update claude-sessions: "+listAfter(c.Detail, "deleted list: ")+".", "See computers", toSettings)
		}
		return say(c, "Deleting old conversations waits until every computer's record in the archive can be read.", "See computers", toSettings)
	},
	"profile": func(c check) healthItem {
		switch c.Level {
		case levelInfo:
			return say(c, plural(scanned(c.Detail, "%d rule/skill"), "rule and skill file")+" differ between your computers. Each keeps its own until you choose.", "Compare", toSettings)
		case levelWarn:
			return say(c, "Your rules and skills aren't in the archive yet. Back up once to add them.", "", "")
		}
		return say(c, plural(scanned(c.Detail, "%d rule/skill"), "rule and skill file")+" are in the archive.", "", "")
	},
	"manifest": func(c check) healthItem {
		switch {
		case c.Level == levelOK:
			return say(c, "Every project in the archive can be placed on your other computers.", "", "")
		case c.Level == levelInfo:
			return say(c, "The archive holds no projects yet.", "", "")
		case strings.HasPrefix(c.Detail, "ignored manifest file: "):
			return say(c, "A computer's record in the archive can't be read, or is a sync app's conflict copy. The other computers' records are still used.", "", "")
		case strings.Contains(c.Detail, "no usable identity"):
			return say(c, plural(scanned(c.Detail, "%d of"), "project folder")+" in the archive hold notes only, so the other computers can't tell where they belong.", "Review folders", toImport)
		}
		return say(c, "The archive's project records can't be read.", "", "")
	},
	"manifest format": func(c check) healthItem {
		return say(c, "The archive has only the older PowerShell tools' project list. The next backup adds the newer records.", "", "")
	},
	"html excluded": func(c check) healthItem {
		return say(c, "Some projects are left out of the readable pages.", "Change", toSettings)
	},
	"html": func(c check) healthItem {
		switch {
		case c.Level == levelOK:
			return say(c, plural(scanned(c.Detail, "%d page(s)"), "readable page")+", all up to date.", "", "")
		case c.Level == levelInfo:
			return say(c, "No readable pages: every project is left out of them.", "Change", toSettings)
		case strings.HasPrefix(c.Detail, "no pages"):
			return say(c, "No readable pages yet. Rebuild them to read your conversations on a phone.", "Rebuild pages", toSettings)
		}
		return say(c, "Readable pages are behind: the newest conversation has no current page.", "Rebuild pages", toSettings)
	},
	"work folders": func(c check) healthItem {
		switch {
		case c.Level == levelOK && scanned(c.Detail, "%d folder(s)") == 0:
			return say(c, "Work folders are on. None is backed up from this computer yet.", "", "")
		case c.Level == levelOK:
			return say(c, plural(scanned(c.Detail, "%d folder(s)"), "work folder")+" backed up.", "", "")
		case c.Level == levelInfo:
			return say(c, "Work folders aren't backed up. Turn them on to back up the folders Claude works in outside git.", "Work folders", toSettings)
		case strings.HasPrefix(c.Detail, "not backed up: "):
			return say(c, "Part of a work folder isn't backed up: a subfolder over 250 MB is left out.", "Work folders", toSettings)
		}
		return say(c, "Work folders take a lot of space in the archive. Exclude a folder you don't need.", "Work folders", toSettings)
	},
	"local buckets": func(c check) healthItem {
		return say(c, plural(scanned(c.Detail, "%d local"), "project folder")+" on this computer hold notes but no conversations.", "", "")
	},
	"session hook": func(c check) healthItem {
		switch {
		case c.Level == levelOK:
			return say(c, "Each session is backed up when it ends.", "", "")
		case c.Level == levelFail:
			return say(c, "The backup at the end of each session points at a program that's gone. Set up automatic backup again.", "Set up again", toSettings)
		case strings.HasPrefix(c.Detail, "two archivers"):
			return say(c, "Two backup tools run at the end of each session: this one and the older PowerShell script. Remove one of them.", "", "")
		case strings.HasPrefix(c.Detail, "no SessionEnd"):
			return say(c, "Sessions aren't backed up when they end, only on the schedule. Set up automatic backup again.", "Set up again", toSettings)
		}
		return say(c, "The backup at the end of each session is set up more than once.", "Set up again", toSettings)
	},
	"reply hook": func(c check) healthItem {
		return say(c, "The backup after each reply isn't set up yet. Set up automatic backup again to add it.", "Set up again", toSettings)
	},
	"sweep": func(c check) healthItem {
		switch c.Level {
		case levelOK:
			return say(c, "The scheduled backup is set up.", "", "")
		case levelFail:
			return say(c, "The scheduled backup is set up but won't back anything up. Set it up again.", "Set up again", toSettings)
		}
		return say(c, "The scheduled backup isn't set up, so a session that's cut off waits for the next backup.", "Set up again", toSettings)
	},
	"archived": func(c check) healthItem {
		if c.Level == levelFail {
			return say(c, plural(scanned(c.Detail, "%d session(s)"), "conversation")+" that ended over 40 minutes ago aren't in the archive. Back up now, and see the log for why.", "Open the log", toSettings)
		}
		return say(c, "Every conversation that ended more than 40 minutes ago is in the archive.", "", "")
	},
	"last push": func(c check) healthItem {
		switch {
		case c.Level == levelOK:
			return say(c, "The last backup finished normally.", "", "")
		case strings.HasPrefix(c.Detail, "no successful push"):
			return say(c, "This computer hasn't backed up yet. Back up now, then check automatic backup.", "Set up again", toSettings)
		}
		return say(c, "This computer last backed up over 3 hours ago. If it has been on, automatic backup isn't running.", "Open the log", toSettings)
	},
	"archive access": func(c check) healthItem {
		return say(c, "A backup was refused access to the archive folder. Check the folder's permissions; on a Mac, check Privacy & Security too.", "Open the log", toSettings)
	},
	"secrets": func(c check) healthItem {
		return say(c, "A sign-in file, .credentials.json or .claude.json, is in the archive folder. Delete it from the synced folder and change any password or key it held; claude-sessions never copies it. Run claude-sessions doctor to see where it is.", "", "")
	},
}

// --- Settings and rules ---------------------------------------------------------

type settingsView struct {
	Problem     string               `json:"problem"`
	Archive     *archiveView         `json:"archive"`
	Config      *configView          `json:"config"` // nil when the config cannot be read
	Computers   []computerView       `json:"computers"`
	WorkFolders []archive.WorkFolder `json:"workFolders"` // this computer's
	Pages       struct {
		Pages       int  `json:"pages"`
		Transcripts int  `json:"transcripts"`
		Stale       bool `json:"stale"`
	} `json:"pages"`
	ProfileDiffs int `json:"profileDiffs"`
	Sweep        struct {
		Installed bool   `json:"installed"`
		Mechanism string `json:"mechanism"`
		Problem   string `json:"problem"` // a sentence for the page
	} `json:"sweep"`
	FromShortcut bool   `json:"fromShortcut"`
	Version      string `json:"version"`
	LogPath      string `json:"logPath"`
}

func querySettings(root string, fromShortcut bool, now time.Time) settingsView {
	v := settingsView{Computers: []computerView{}, WorkFolders: []archive.WorkFolder{}, FromShortcut: fromShortcut,
		Version: version, LogPath: filepath.Join(root, "session-sync.log")}
	if res, err := runConfig(quietStreams(), configOpts{ClaudeDir: root}); err == nil {
		v.Config = res.View
	}
	sw, err := hostagent.Sweep(sweepJob(root, ""))
	v.Sweep.Installed, v.Sweep.Mechanism = sw.Installed, sw.Mechanism
	switch { // the system's own words hold paths and commands
	case err != nil:
		v.Sweep.Problem = "The scheduled backup can't be checked on this computer."
	case sw.Problem != "":
		v.Sweep.Problem = "The scheduled backup is set up but won't back anything up. Set it up again."
	}
	dest, problem := panelDest(root)
	if problem != "" {
		v.Problem = problem
		return v
	}
	v.Archive = archiveOf(dest)
	shards, _, _ := archive.ReadShards(dest)
	rows, _, _ := archivedNow(dest, shards)
	logged, _ := lastPush(root)
	v.Computers = computersOf(shards, rows, logged, now)
	for name, s := range shards {
		if strings.EqualFold(name, machineName()) && s.WorkFolders != nil {
			v.WorkFolders = s.WorkFolders
		}
	}
	manifest, _ := archive.MergeShards(dest, shards)
	v.Pages.Pages, v.Pages.Transcripts, v.Pages.Stale = renderState(dest, manifest, archive.RenderExcludes(root, dest, nil, false))
	v.ProfileDiffs = len(profileDiffs(dest))
	return v
}

type profileCopy struct {
	Machine string    `json:"machine"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
}

type profileDiff struct {
	File   string        `json:"file"`
	Newest string        `json:"newest"` // the computer whose copy changed last
	Copies []profileCopy `json:"copies"`
}

// queryProfile is each rule or skill file whose computers' copies differ, with each
// copy's size and time: no file's content.
func queryProfile(dest string) []profileDiff {
	out := []profileDiff{}
	machines, _ := os.ReadDir(filepath.Join(profileDir(dest), "machines"))
	for _, file := range profileDiffs(dest) {
		d := profileDiff{File: file, Copies: []profileCopy{}}
		var newest time.Time
		for _, m := range machines {
			dir := machineProfileDir(dest, m.Name())
			if !archive.ValidName(m.Name()) || !archive.IsRealDir(dir) {
				continue
			}
			st, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(file)))
			if err != nil || !st.Mode().IsRegular() {
				continue
			}
			d.Copies = append(d.Copies, profileCopy{m.Name(), st.Size(), st.ModTime()})
			if st.ModTime().After(newest) {
				d.Newest, newest = m.Name(), st.ModTime()
			}
		}
		out = append(out, d)
	}
	return out
}

// --- Routes ---------------------------------------------------------------------

func (u *uiServer) apiSummary(w http.ResponseWriter, r *http.Request) {
	sendJSON(w, querySummary(u.root, time.Now()))
}

func (u *uiServer) apiStorage(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	last := u.lastClean
	u.mu.Unlock()
	sendJSON(w, queryStorage(u.root, time.Now(), last))
}

func (u *uiServer) apiHealth(w http.ResponseWriter, r *http.Request) {
	sendJSON(w, u.health.do(func() []healthItem { return queryHealth(u.root, u.logf) }))
}

func (u *uiServer) apiSettings(w http.ResponseWriter, r *http.Request) {
	sendJSON(w, querySettings(u.root, u.opts.FromShortcut, time.Now()))
}

// withArchive answers {"problem": ...} when there is no archive folder to read, as
// /api/projects does, and view's answer otherwise.
func (u *uiServer) withArchive(view func(dest string) any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dest, problem := panelDest(u.root)
		if problem != "" {
			sendJSON(w, map[string]string{"problem": problem})
			return
		}
		sendJSON(w, view(dest))
	}
}
