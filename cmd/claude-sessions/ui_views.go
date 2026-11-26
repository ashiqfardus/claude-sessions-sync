package main

import (
	"crypto/sha256"
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
	LastBackup    time.Time      `json:"lastBackup"` // this computer's row's LastPush, or its log's; zero for none
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
	for _, c := range v.Computers {
		if c.This {
			v.LastBackup = c.LastPush // the same time the computer's row shows
		}
	}
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
// the newer of its shard's and logged, its log's last PUSH: line. A shard's time is cut
// to the hour, so it is judged by the end of its hour: ok up to 3 hours after it, as
// doctor's quietPushAge.
func computersOf(shards map[string]archive.Shard, rows []archive.Session, logged, now time.Time) []computerView {
	count := map[string]int{}
	for _, s := range rows {
		count[s.Machine]++
	}
	out := []computerView{}
	for name, s := range shards {
		c := computerView{Name: name, Version: s.ToolVersion, This: strings.EqualFold(name, machineName()),
			Ready: s.ReadsZips && s.HonorsDeleted, LastPush: s.LastPush, Conversations: count[name]}
		latest := c.LastPush.Add(time.Hour)
		if c.This && logged.After(c.LastPush) {
			c.LastPush, latest = logged, logged
		}
		switch {
		case !c.Ready || c.LastPush.IsZero():
			c.State = "update"
		case now.Sub(latest) <= 2*time.Hour:
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
	return withoutDeleted(dest, archive.SessionsOf(dest, shards))
}

// withoutDeleted is archivedNow on index rows already read. It reuses rows' array.
func withoutDeleted(dest string, rows []archive.Session) ([]archive.Session, map[string]archive.DeletedEntry, int) {
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
// be a computer without what has looks for, so it holds back too, by its file name; a
// manifest that cannot be read at all, err, may hold back every computer.
func holdouts(shards map[string]archive.Shard, bad []string, err error, has func(archive.Shard) bool) []string {
	if err != nil {
		return []string{everyComputer}
	}
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

// everyComputer stands for the computers in a manifest that cannot be read.
const everyComputer = "every computer"

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

// agree is one for 1 and many otherwise: a verb that follows plural.
func agree(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
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
	// RetentionUnreadable is retention.json not read: nothing shows as kept, and nothing
	// is deleted for its age.
	RetentionUnreadable bool `json:"retentionUnreadable"`
	Unreadable          int  `json:"unreadable"`
}

func emptyStorage() storageView {
	v := storageView{ZipWaitsFor: []string{}, Projects: []storageProject{}}
	v.Kept.Projects = []string{}
	return v
}

func queryStorage(root string, now time.Time, last *cleanSummary) storageView {
	v := emptyStorage()
	v.Duplicates = last
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
	v.RetentionUnreadable = rerr != nil

	cfg, cerr := archive.LoadConfig(root)
	v.ZipOn = cerr == nil && cfg.ZipOn() // push zips nothing with a config it cannot read
	v.ZipWaitsFor = slices.DeleteFunc(holdouts(shards, bad, err, func(s archive.Shard) bool { return s.ReadsZips }),
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

	// One walk, by name and size: nothing is opened, and no link is followed but one at
	// dest itself, which WalkDir would not descend.
	cutoff := now.Add(-zipIdle)
	loose, idle, inBucket := map[string]int64{}, map[string]bool{}, map[string]int64{}
	top, err := filepath.EvalSymlinks(dest)
	if err != nil {
		top = dest
	}
	_ = filepath.WalkDir(top, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil // a folder that cannot be listed is left out
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(top, p)
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
	KeepDays int `json:"keepDays"`
	// KeepDaysSetAt is when keep-days last changed, nil if never: deleting by age waits a
	// day after each computer's next backup from then.
	KeepDaysSetAt *time.Time `json:"keepDaysSetAt"`
	Keep          []keepRow  `json:"keep"`
	// Holdouts are the computers deleting waits for, which do not honour the deleted list.
	Holdouts            []string `json:"holdouts"`
	RetentionUnreadable bool     `json:"retentionUnreadable"` // nothing then shows as kept
	Unreadable          int      `json:"unreadable"`
}

func queryRetention(dest string) retentionView {
	shards, bad, err := archive.ReadShards(dest)
	projects, lbad := archive.MergeShards(dest, shards)
	ret, rerr := archive.LoadRetention(dest)
	opts, _ := groupProjects(projects, homesOf(shards))
	for i := range opts {
		opts[i].Arg = projectArg(projects, opts[i])
	}
	return retentionView{KeepDays: ret.KeepDays, KeepDaysSetAt: ret.KeepDaysSetAt, Keep: keepRows(ret, projects, opts, archive.SessionsOf(dest, shards)),
		Holdouts:            holdouts(shards, bad, err, func(s archive.Shard) bool { return s.HonorsDeleted }),
		RetentionUnreadable: rerr != nil, Unreadable: len(bad) + len(lbad) + errCount(err) + errCount(rerr)}
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
	all := archive.SessionsOf(dest, shards)
	_, deleted, _ := withoutDeleted(dest, slices.Clone(all))
	// The index keeps a deleted conversation's row until its computer pushes again: its
	// title and project name another computer's conversation without reading it.
	indexed := map[string]archive.Session{}
	for _, s := range all {
		if id := strings.ToLower(s.ID); indexed[id].Prompt == "" {
			indexed[id] = s
		}
	}
	opts, keys := groupProjects(projects, homesOf(shards))
	names := map[string]string{}
	for _, o := range opts {
		names[o.Key] = o.Name
	}
	local := localTranscripts(root)
	items := []deletedItem{}
	for id, d := range deleted {
		it := deletedItem{ID: id, DeletedAt: d.DeletedAt, Here: local[id] != ""}
		var paths []string
		for _, b := range d.Buckets {
			if k := keys[b]; k != "" {
				it.Project = names[k]
				break
			}
			paths = append(paths, projects[b].Path)
		}
		// No project groups it: the folder of the path a manifest, or the index, records.
		for _, p := range append(paths, indexed[id].Project) {
			if it.Project == "" {
				it.Project = projectLeaf(p)
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
		// This computer's own copy, then the index's title; never the archived copy.
		it := &items[i]
		it.Title = convTitle(local[it.ID], it.ID)
		if local[it.ID] == "" && indexed[it.ID].Prompt != "" {
			it.Title = indexed[it.ID].Prompt
		}
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
	log := filepath.Join(root, "session-sync.log")
	raw, _ := os.ReadFile(log)
	lines := strings.Split(string(raw), "\n")
	if len(lines) < 300 { // rotated a moment ago: the previous log holds the rest
		old, _ := os.ReadFile(log + ".1")
		lines = append(strings.Split(string(old), "\n"), lines...)
	}
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
	dest, problem := panelDest(root)
	var shards map[string]archive.Shard
	if problem == "" {
		shards, _, _ = archive.ReadShards(dest)
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
	var local, titles map[string]string
	for _, it := range items[:min(5, len(items))] {
		if it.pruned != "" {
			if local == nil {
				local, titles = localTranscripts(root), map[string]string{}
				if problem == "" { // the index keeps a deleted conversation's row a while, as queryDeleted reads it
					for _, s := range archive.SessionsOf(dest, shards) {
						if id := strings.ToLower(s.ID); titles[id] == "" {
							titles[id] = s.Prompt
						}
					}
				}
			}
			// This computer's own copy, then the index's title, else the log's own words.
			switch {
			case local[it.pruned] != "":
				it.Text = "Deleted " + convTitle(local[it.pruned], it.pruned) + " from the archive"
			case titles[it.pruned] != "":
				it.Text = "Deleted " + titles[it.pruned] + " from the archive"
			}
		}
		out = append(out, it.activityItem)
	}
	return out
}

// logActivity is a log line's text after its time, in plain words, or "" to leave it
// out; and the id of a conversation the age rule deleted, which the text names by its
// project's folder until queryActivity finds its title.
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
		id, more, _ := strings.Cut(strings.TrimPrefix(rest, "PRUNE: "), " ")
		if !claude.IsSessionID(id) {
			break
		}
		text = "Deleted " + convTitle("", id) + " from the archive"
		if p, _, ok := strings.Cut(strings.TrimPrefix(more, "("), ", last message "); ok { // (<path>, last message ...)
			if p != "" {
				text = "Deleted a conversation in " + projectLeaf(p) + " from the archive"
			}
		}
		return text, strings.ToLower(id)
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

// flight runs one computation at a time and keeps its result as long as the run says.
// A caller that comes while it runs, or while its result for the same key is kept, gets
// that result (Ruling P15): doctor leaves a slow check's read behind, and a walk of the
// archive is slow, so one for each page load would pile up reads while the drive is slow.
type flight[T any] struct {
	mu   sync.Mutex
	cur  *flightRun[T]
	last *flightRun[T]
	gen  int // drop's count: a run started before a drop is not kept
}

type flightRun[T any] struct {
	key   string
	gen   int
	done  chan struct{}
	v     T         // these three are set before done is closed
	ok    bool      // run returned rather than panicked
	until time.Time // its result is kept until then
}

// do gives key's result: kept, or the run in progress's, or run's, which runs in this
// caller's goroutine, where net/http recovers a panic. Callers waiting on a run that
// panicked get fallback.
func (f *flight[T]) do(key string, fallback T, run func() (T, time.Duration)) T {
	for {
		f.mu.Lock()
		if l := f.last; l != nil && l.key == key && time.Now().Before(l.until) {
			f.mu.Unlock()
			return l.v
		}
		r := f.cur
		if r == nil {
			break // still holding mu
		}
		gen := f.gen
		f.mu.Unlock()
		<-r.done
		if r.gen != gen {
			continue // dropped before this caller came: its answer is from before a job
		}
		if r.key == key && r.ok {
			return r.v
		} else if r.key == key {
			return fallback
		}
	}
	r := &flightRun[T]{key: key, gen: f.gen, done: make(chan struct{})}
	f.cur = r
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.cur = nil
		if r.ok && r.gen == f.gen {
			f.last = r
		}
		f.mu.Unlock()
		close(r.done)
	}()
	v, keep := run()
	r.v, r.ok, r.until = v, true, time.Now().Add(keep)
	return v
}

// drop forgets the kept result, and the one of a run in progress.
func (f *flight[T]) drop() {
	f.mu.Lock()
	f.last = nil
	f.gen++
	f.mu.Unlock()
}

// queryHealth is doctor's light checks in plain words - failures, warnings, then the
// rest - and how long to keep them: 60 s, or 5 minutes once the archive stopped
// answering, so that reloads do not leave a blocked read each. It reads no archived
// conversation and writes no test file to the archive.
func queryHealth(root string, logf func(string, ...any), profiles *profileCache) ([]healthItem, time.Duration) {
	rep, _ := runDoctor(quietStreams(), doctorOpts{ClaudeDir: root, NoProbe: true, Light: true, ProfileDiffs: profiles.diffs})
	items, keep := make([]healthItem, 0, len(rep.Checks)), time.Minute
	for _, c := range rep.Checks {
		if plainChecks[c.Name] == nil {
			logf("PANEL: health: %s: %s: %s", c.Name, c.Level, c.Detail) // what the generic sentence points to
		}
		if c.Name == "archive checks" || strings.Contains(c.Detail, ": not finished after ") {
			keep = 5 * time.Minute
		}
		items = append(items, plainCheck(c))
	}
	return sortHealth(items), keep
}

// profileCache keeps profileDiffs' answer for the copies as they were: a load that finds
// every copy's size and time unchanged reads no file.
type profileCache struct {
	mu   sync.Mutex
	key  string
	list []string
}

// get is every copy and the files whose copies differ.
func (c *profileCache) get(dest string) (map[string][]profileCopy, []string) {
	copies := profileCopies(dest)
	files := make([]string, 0, len(copies))
	for rel := range copies {
		files = append(files, rel)
	}
	sort.Strings(files)
	h := sha256.New()
	for _, rel := range files {
		for _, cp := range copies[rel] {
			fmt.Fprintf(h, "%q %q %d %d\n", rel, cp.path, cp.Size, cp.ModTime.UnixNano())
		}
	}
	key := string(h.Sum(nil))
	c.mu.Lock() // held across the comparison: callers meanwhile get its answer, not one each
	defer c.mu.Unlock()
	if c.list == nil || c.key != key {
		c.key, c.list = key, compareCopies(copies)
	}
	return copies, c.list
}

// compareCopies is diffsOf; a test counts its runs.
var compareCopies = diffsOf

func (c *profileCache) diffs(dest string) []string {
	_, list := c.get(dest)
	return list
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

// What Health and Settings say of the scheduled backup: hostagent's own words hold
// paths and commands.
const (
	sweepUncheckable = "The scheduled backup can't be checked on this computer."
	sweepBroken      = "The scheduled backup is set up but won't back anything up. Set it up again."
)

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
		if c.Detail == inFullCheck {
			return say(c, "Whether the archive folder downloads files only when they're opened is tested in the full check.", "", "")
		}
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
		return say(c, "Not every computer runs this version of claude-sessions: "+listAfter(c.Detail, "): ")+
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
		case c.Advice == zipConfigUnread:
			return say(c, "Old conversations aren't compressed: this computer's settings file can't be read, and it may say not to. "+
				"Choose the archive folder again to write a new one, then check your settings.", "Choose folder", toSettings)
		case strings.Contains(c.Detail, "read zips: "):
			return say(c, "Compressing old conversations waits until claude-sessions is updated on "+listAfter(c.Detail, "read zips: ")+".", "See computers", toSettings)
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
			return say(c, "Deleting old conversations waits until claude-sessions is updated on "+listAfter(c.Detail, "deleted list: ")+".", "See computers", toSettings)
		}
		return say(c, "Deleting old conversations waits until every computer's record in the archive can be read.", "See computers", toSettings)
	},
	"profile": func(c check) healthItem {
		switch c.Level {
		case levelInfo:
			n := scanned(c.Detail, "%d rule/skill")
			return say(c, plural(n, "rule and skill file")+agree(n, " differs", " differ")+" between your computers. Each keeps its own until you choose.", "Compare", toSettings)
		case levelWarn:
			return say(c, "Your rules and skills aren't in the archive yet. Back up once to add them.", "", "")
		}
		n := scanned(c.Detail, "%d rule/skill")
		return say(c, plural(n, "rule and skill file")+agree(n, " is", " are")+" in the archive.", "", "")
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
			n := scanned(c.Detail, "%d of")
			return say(c, plural(n, "project folder")+" in the archive"+agree(n, " holds", " hold")+" notes only, so the other computers can't tell where "+agree(n, "it belongs.", "they belong."), "Review folders", toImport)
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
			n := scanned(c.Detail, "%d page(s)")
			return say(c, plural(n, "readable page")+agree(n, ", up to date.", ", all up to date."), "", "")
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
		n := scanned(c.Detail, "%d local")
		return say(c, plural(n, "project folder")+" on this computer"+agree(n, " holds", " hold")+" notes but no conversations.", "", "")
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
			return say(c, sweepBroken, "Set up again", toSettings)
		}
		if c.Advice == "" { // doctor could not ask the system: its error is the detail
			return say(c, sweepUncheckable, "", "")
		}
		return say(c, "The scheduled backup isn't set up, so a session that's cut off waits for the next backup.", "Set up again", toSettings)
	},
	"archived": func(c check) healthItem {
		if c.Detail == inFullCheck {
			return say(c, "Whether every conversation that ended a while ago is in the archive is checked in the full check.", "", "")
		}
		if c.Level == levelFail {
			n := scanned(c.Detail, "%d session(s)")
			return say(c, plural(n, "conversation")+" that ended over 40 minutes ago"+agree(n, " isn't", " aren't")+" in the archive. Back up now, and see the log for why.", "Open the log", toSettings)
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
	// OtherWorkFolders are the other computers' work folders, by the path each had there:
	// what config map-folder takes.
	OtherWorkFolders []machinePath `json:"otherWorkFolders"`
	Pages            struct {
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

func querySettings(root string, fromShortcut bool, now time.Time, profiles *profileCache) settingsView {
	v := settingsView{Computers: []computerView{}, WorkFolders: []archive.WorkFolder{}, OtherWorkFolders: []machinePath{}, FromShortcut: fromShortcut,
		Version: version, LogPath: filepath.Join(root, "session-sync.log")}
	if res, err := runConfig(quietStreams(), configOpts{ClaudeDir: root}); err == nil {
		v.Config = res.View
	}
	sw, err := hostagent.Sweep(sweepJob(root, ""))
	v.Sweep.Installed, v.Sweep.Mechanism = sw.Installed, sw.Mechanism
	switch { // the system's own words hold paths and commands
	case err != nil:
		v.Sweep.Problem = sweepUncheckable
	case sw.Problem != "":
		v.Sweep.Problem = sweepBroken
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
		if strings.EqualFold(name, machineName()) {
			if s.WorkFolders != nil {
				v.WorkFolders = s.WorkFolders
			}
			continue
		}
		for _, f := range s.WorkFolders {
			if !f.Exact { // a single file kept on its own, not a folder to place
				v.OtherWorkFolders = append(v.OtherWorkFolders, machinePath{name, f.Path})
			}
		}
	}
	sort.Slice(v.OtherWorkFolders, func(i, j int) bool {
		a, b := v.OtherWorkFolders[i], v.OtherWorkFolders[j]
		return a.Machine < b.Machine || a.Machine == b.Machine && a.Path < b.Path
	})
	manifest, _ := archive.MergeShards(dest, shards)
	v.Pages.Pages, v.Pages.Transcripts, v.Pages.Stale = renderState(dest, manifest, archive.RenderExcludes(root, dest, nil, false))
	v.ProfileDiffs = len(profiles.diffs(dest))
	return v
}

type profileDiff struct {
	File   string        `json:"file"`
	Newest string        `json:"newest"` // the computer whose copy changed last
	Copies []profileCopy `json:"copies"`
}

// queryProfile is each rule or skill file whose computers' copies differ, with each
// copy's size and time: no file's content.
func queryProfile(dest string, profiles *profileCache) []profileDiff {
	out := []profileDiff{}
	copies, diffs := profiles.get(dest)
	for _, file := range diffs {
		d := profileDiff{File: file, Copies: copies[file]}
		var newest time.Time
		for _, c := range d.Copies {
			if c.ModTime.After(newest) {
				d.Newest, newest = c.Machine, c.ModTime
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

// apiStorage walks the archive once in 5 minutes for each folder, or after a panel job
// (dropCaches). The folder as configured is the key: nothing in the archive is read
// before a kept answer is given.
func (u *uiServer) apiStorage(w http.ResponseWriter, r *http.Request) {
	// Reachability first, as every view: a walk kept 5 minutes must not stand in for an
	// archive gone since.
	key, problem := panelDest(u.root)
	v := emptyStorage()
	v.Problem = problem
	if problem == "" {
		v = u.storage.do(key, emptyStorage(), func() (storageView, time.Duration) {
			v := queryStorage(u.root, time.Now(), nil)
			if v.Archive == nil {
				return v, 0 // no folder to walk: look again next time
			}
			return v, 5 * time.Minute
		})
	}
	u.mu.Lock()
	v.Duplicates = u.lastClean // a copy of the kept view, with this run's check
	u.mu.Unlock()
	sendJSON(w, v)
}

func (u *uiServer) apiHealth(w http.ResponseWriter, r *http.Request) { sendJSON(w, u.healthNow()) }

func (u *uiServer) healthNow() []healthItem {
	return u.health.do("", []healthItem{}, func() ([]healthItem, time.Duration) { return queryHealth(u.root, u.logf, &u.profiles) })
}

// dropCaches forgets the health, storage and folder-listing answers kept, for a job
// whose changes they would not show yet: a panel job calls it when it ends.
func (u *uiServer) dropCaches() {
	u.health.drop()
	u.storage.drop()
	u.listings.drop()
}

func (u *uiServer) apiSettings(w http.ResponseWriter, r *http.Request) {
	sendJSON(w, querySettings(u.root, u.opts.FromShortcut, time.Now(), &u.profiles))
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
