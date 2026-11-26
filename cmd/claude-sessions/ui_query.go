package main

import (
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/convzip"
	"github.com/ashiqfardus/claude-sessions-sync/internal/identity"
)

// The panel's project list and Conversations table. The archive is often a drive that
// downloads each file it is asked to read, so a listing reads only the index, manifest,
// retention and deleted files, and lists folders by name; only text search reads a
// conversation.

type machinePath struct {
	Machine string `json:"machine"`
	Path    string `json:"path"`
}

// projectOption is one project across computers, as import, keep and prune see it.
type projectOption struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	// Arg is what config keep and prune --project resolve to this project (Ruling P10);
	// "" when nothing does, as for a folder name two remotes share.
	Arg     string        `json:"arg"`
	Buckets []string      `json:"buckets"`
	Paths   []machinePath `json:"paths"`
}

// queryProjects lists every project, sorted by name, and gives each bucket's key. homes
// are the folders it names Home folder; nil takes each computer's own from its shard.
func queryProjects(dest string, homes []string) ([]projectOption, map[string]string, error) {
	shards, _, err := archive.ReadShards(dest)
	if err != nil {
		return nil, nil, err
	}
	if homes == nil {
		homes = homesOf(shards)
	}
	projects, _ := archive.MergeShards(dest, shards)
	opts, keys := groupProjects(projects, homes)
	for i := range opts {
		opts[i].Arg = projectArg(projects, opts[i])
	}
	return opts, keys, nil
}

// groupProjects puts the buckets of one git remote in one project, and the rest by
// folder name. A bucket with no remote joins the one remote project with its folder
// name, as keep does; when two or more have it, matchProject refuses the name, so the
// bucket stays a project of its own. A bucket with neither has no project.
func groupProjects(projects map[string]archive.Project, homes []string) ([]projectOption, map[string]string) {
	type group struct {
		opt          projectOption
		leaf, remote string
	}
	var groups []*group
	keys := map[string]string{}
	join := func(g *group, b string) {
		p := projects[b]
		g.opt.Buckets = append(g.opt.Buckets, b)
		if p.Path != "" {
			g.opt.Paths = append(g.opt.Paths, machinePath{p.Owner(), p.Path})
		}
		if g.leaf == "" {
			g.leaf = p.Leaf
		}
		keys[b] = g.opt.Key
	}
	names := make([]string, 0, len(projects))
	for b := range projects {
		names = append(names, b)
	}
	sort.Strings(names)

	byRemote := map[string]*group{}
	for _, b := range names {
		if strings.TrimSpace(projects[b].Remote) == "" {
			continue
		}
		n := identity.NormalRemote(projects[b].Remote)
		if byRemote[n] == nil {
			// The remote as SameRemote compares it: no user or token, which would reach the page's address.
			byRemote[n] = &group{opt: projectOption{Key: "remote:" + n}, remote: n}
			groups = append(groups, byRemote[n])
		}
		join(byRemote[n], b)
	}
	remotesNamed := map[string][]*group{} // a folder name, lower case -> the remote projects with a bucket of it
	for _, g := range groups {
		for _, b := range g.opt.Buckets {
			if l := strings.ToLower(projects[b].Leaf); l != "" && !slices.Contains(remotesNamed[l], g) {
				remotesNamed[l] = append(remotesNamed[l], g)
			}
		}
	}
	byLeaf := map[string]*group{}
	for _, b := range names {
		p, l := projects[b], strings.ToLower(projects[b].Leaf)
		if strings.TrimSpace(p.Remote) != "" || l == "" {
			continue
		}
		g := byLeaf[l]
		if rs := remotesNamed[l]; len(rs) == 1 {
			g = rs[0]
		} else if g == nil {
			g = &group{opt: projectOption{Key: "leaf:" + l}}
			byLeaf[l], groups = g, append(groups, g)
		}
		join(g, b)
	}

	sameLeaf := map[string]int{}
	for _, g := range groups {
		sameLeaf[strings.ToLower(g.leaf)]++
	}
	opts := make([]projectOption, 0, len(groups))
	homeOn := make([][]string, 0, len(groups)) // the computers whose home folder each option is
	for _, g := range groups {
		o := g.opt
		sort.Strings(o.Buckets)
		sort.Slice(o.Paths, func(i, j int) bool {
			a, b := o.Paths[i], o.Paths[j]
			return a.Machine < b.Machine || a.Machine == b.Machine && a.Path < b.Path
		})
		if o.Paths == nil {
			o.Paths = []machinePath{}
		}
		var on []string
		for _, mp := range o.Paths {
			if slices.ContainsFunc(homes, func(h string) bool { return samePath(mp.Path, h) }) && !slices.Contains(on, mp.Machine) {
				on = append(on, mp.Machine)
			}
		}
		switch o.Name = g.leaf; {
		case len(on) > 0:
			o.Name = "Home folder"
		case g.leaf == "":
			o.Name = g.remote
		case sameLeaf[strings.ToLower(g.leaf)] > 1 && g.remote != "":
			o.Name += " (" + g.remote + ")"
		case sameLeaf[strings.ToLower(g.leaf)] > 1:
			o.Name += " (no git remote)"
		}
		opts, homeOn = append(opts, o), append(homeOn, on)
	}
	homesNamed := 0
	for _, on := range homeOn {
		homesNamed += min(len(on), 1)
	}
	for i, on := range homeOn {
		if homesNamed > 1 && len(on) > 0 {
			opts[i].Name += " (" + strings.Join(on, ", ") + ")" // two computers' home folders by different names
		}
	}
	sort.Slice(opts, func(i, j int) bool {
		a, b := strings.ToLower(opts[i].Name), strings.ToLower(opts[j].Name)
		return a < b || a == b && opts[i].Key < opts[j].Key
	})
	return opts, keys
}

// projectArg is the first of o's paths, then folder names, that matchProject resolves to
// o: every bucket of o and, beyond them, only buckets with no remote whose folder name two
// remote projects share (Ruling P10). "" when none does.
func projectArg(projects map[string]archive.Project, o projectOption) string {
	var args []string
	for _, b := range o.Buckets {
		if p := projects[b].Path; p != "" {
			args = append(args, p)
		}
	}
	for _, b := range o.Buckets { // a bucket no computer recorded a path for
		if l := projects[b].Leaf; l != "" && !slices.Contains(args, l) {
			args = append(args, l)
		}
	}
	for _, arg := range args {
		_, got, err := matchProject(projects, arg)
		if err != nil {
			continue
		}
		missing := slices.ContainsFunc(o.Buckets, func(b string) bool { return !slices.Contains(got, b) })
		other := slices.ContainsFunc(got, func(b string) bool {
			return !slices.Contains(o.Buckets, b) && strings.TrimSpace(projects[b].Remote) != ""
		})
		if !missing && !other {
			return arg
		}
	}
	return ""
}

// homesOf is every computer's home folder, which the project list names Home folder.
func homesOf(shards map[string]archive.Shard) []string {
	var homes []string
	for _, s := range shards {
		if s.Home != "" {
			homes = append(homes, s.Home)
		}
	}
	return homes
}

// convQuery is the Conversations page's filters and search.
//   - Machine is "", a computer's name, or "*many" (more than one).
//   - Age is "", 7d, 30d, over90d or over1y; State "", compressed, kept, forked or inuse.
//   - Look is archive (the default) or local, this computer's profile.
//   - Limit is how many matching conversations search stops at: 0 for all. The route
//     makes it 50 when not given.
//   - Page counts from 1. Per is 10 by default, at most 100.
type convQuery struct {
	Text, Project, Machine, Age, State, Look, Role, Since, Until string
	CaseSensitive, Regexp, All                                   bool
	Limit, Page, Per                                             int
}

// convRow is one conversation across computers.
type convRow struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Project    string `json:"project"`
	ProjectKey string `json:"projectKey"`
	// Buckets[0] holds the page /read/ serves, when one does.
	Buckets  []string  `json:"buckets"`
	Machines []string  `json:"machines"`
	Updated  time.Time `json:"updated"` // zero when unknown (Rulings P33, P43)
	// Size is the zip's when compressed, else the transcript's; Full is the transcript's.
	Size       int64 `json:"size"`
	Full       int64 `json:"full"`
	Compressed bool  `json:"compressed"`
	Kept       bool  `json:"kept"`
	Forked     bool  `json:"forked"`
	InUse      bool  `json:"inUse"`
	Readable   bool  `json:"readable"`
}

// convPage is one page of the rows a query selects, newest first. Unreadable counts
// the transcripts search could not read and the archive files the listing skipped.
type convPage struct {
	Total      int       `json:"total"`
	Page       int       `json:"page"`
	Per        int       `json:"per"`
	Pages      int       `json:"pages"`
	Unreadable int       `json:"unreadable"`
	TotalBytes int64     `json:"totalBytes"`
	Stopped    bool      `json:"stopped"`
	Rows       []convRow `json:"rows"`
}

// badQuery is a value the page should not have sent, as the sentence it shows.
type badQuery string

func (b badQuery) Error() string { return string(b) }

// check fills in the defaults, and refuses a value outside them. It gives the dates,
// until at the end of its day.
func (q *convQuery) check() (since, until time.Time, err error) {
	if q.Look == "" {
		q.Look = "archive"
	}
	if q.Per == 0 {
		q.Per = 10
	}
	if q.Page == 0 {
		q.Page = 1
	}
	switch {
	case q.Look != "archive" && q.Look != "local":
		return since, until, badQuery("Look in the archive or on this computer.")
	case !slices.Contains([]string{"", "7d", "30d", "over90d", "over1y"}, q.Age):
		return since, until, badQuery("Choose a time from the Last message list.")
	case !slices.Contains([]string{"", "compressed", "kept", "forked", "inuse"}, q.State):
		return since, until, badQuery("Choose what to show from the Show list.")
	case !slices.Contains([]string{"", "user", "assistant"}, q.Role):
		return since, until, badQuery("Search messages from anyone, you, or Claude.")
	case q.Per < 1 || q.Per > 100:
		return since, until, badQuery("Show from 1 to 100 conversations on a page.")
	case q.Page < 1:
		return since, until, badQuery("Pages are numbered from 1.")
	case q.Limit < 0:
		return since, until, badQuery("Stop after a number of matches, or 0 for every match.")
	}
	if q.Regexp {
		if _, err := regexp.Compile(q.Text); err != nil {
			return since, until, badQuery("That pattern is not a valid regular expression.")
		}
	}
	since, err1 := parseDate(q.Since, "since")
	until, err2 := parseDate(q.Until, "until")
	if err1 != nil || err2 != nil {
		return since, until, badQuery("Write dates as YYYY-MM-DD.")
	}
	if !until.IsZero() {
		until = until.Add(24*time.Hour - time.Nanosecond) // inclusive, as ls --until
	}
	return since, until, nil
}

// queryConversations is one page of the conversations q selects: the archive's, one
// row per conversation across computers, or this computer's. A file in the archive that
// cannot be read is counted in Unreadable and left out, not an error: the archive is
// shared, and a sync app may have half written it.
func queryConversations(root, dest string, q convQuery, now time.Time) (convPage, error) {
	since, until, err := q.check()
	if err != nil {
		return convPage{}, err
	}
	if q.Look == "archive" && dest == "" {
		return convPage{}, errors.New("no archive destination: run `claude-sessions config set-destination <folder>`")
	}
	var pg convPage
	var shards map[string]archive.Shard
	keys, names := map[string]string{}, map[string]string{}
	isKept := func(string, []string) bool { return false }
	if dest != "" {
		var bad []string
		shards, bad, err = archive.ReadShards(dest) // once, for the projects and the index
		if err != nil {
			return convPage{}, err
		}
		projects, lbad := archive.MergeShards(dest, shards)
		pg.Unreadable += len(bad) + len(lbad)
		var opts []projectOption
		opts, keys = groupProjects(projects, homesOf(shards))
		for _, o := range opts {
			names[o.Key] = o.Name
		}
		ret, err := archive.LoadRetention(dest)
		if err != nil {
			pg.Unreadable++ // shown as keeping nothing
		}
		isKept = keptBy(ret, projects)
	}

	byID := map[string]*convRow{}
	var rows []*convRow
	add := func(id, bucket, machine, title, path string, updated time.Time, size int64) { // newest first
		r := byID[id]
		if r == nil {
			r = &convRow{ID: id, Title: title, Project: path, Updated: updated, Size: size, Full: size}
			if r.Project == "" {
				r.Project = bucket
			}
			byID[id], rows = r, append(rows, r)
		}
		if r.Title == "" {
			r.Title = title
		}
		if k := keys[bucket]; r.ProjectKey == "" && k != "" {
			r.ProjectKey, r.Project = k, names[k]
		}
		if !slices.Contains(r.Buckets, bucket) {
			r.Buckets = append(r.Buckets, bucket)
		}
		if !slices.Contains(r.Machines, machine) {
			r.Machines = append(r.Machines, machine)
		}
	}
	local := map[string]time.Time{} // this computer's transcripts' last change
	if q.Look == "archive" {
		for _, s := range archive.SessionsOf(dest, shards) {
			add(strings.ToLower(s.ID), s.Bucket, s.Machine, s.Prompt, s.Project, s.Updated, s.Size)
		}
		deleted, bad, err := archive.LoadDeletedEach(dest)
		if err != nil {
			pg.Unreadable++ // the folder cannot be listed: nothing is shown as deleted
		}
		pg.Unreadable += len(bad) // only that file's entries are shown again
		// A deleted conversation continued since is archived again, so shown (Ruling P50).
		// Updated is the indexing computer's clock and DeletedAt the deleting one's: a skew
		// larger than the gap between them can show a deleted one, or hide a continued one,
		// until the next push indexes it again.
		rows = slices.DeleteFunc(rows, func(r *convRow) bool {
			d, ok := deleted[r.ID]
			return ok && !d.Undone && !r.Updated.After(d.DeletedAt)
		})
		buckets, _ := claude.ListBuckets(claude.ProjectsDir(root))
		for _, b := range buckets {
			for _, t := range b.Transcripts {
				if id := strings.ToLower(t.ID); t.ModTime.After(local[id]) {
					local[id] = t.ModTime
				}
			}
		}
	} else {
		ls, err := runLs(quietStreams(), lsOpts{ClaudeDir: root})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return convPage{}, err
		}
		for _, r := range ls {
			title, id := r.FirstPrompt, strings.ToLower(r.SessionID)
			if strings.HasPrefix(title, "(unreadable: ") { // ls's note holds the raw error
				title = ""
				pg.Unreadable++
			}
			add(id, r.Bucket, machineName(), title, r.Project, r.Updated, r.SizeBytes)
			if r.Updated.After(local[id]) {
				local[id] = r.Updated
			}
		}
	}

	rows = slices.DeleteFunc(rows, func(r *convRow) bool {
		r.Kept = isKept(r.ID, r.Buckets)
		return q.Project != "" && r.ProjectKey != q.Project ||
			q.Machine == "*many" && len(r.Machines) < 2 ||
			q.Machine != "" && q.Machine != "*many" && !slices.ContainsFunc(r.Machines, func(m string) bool { return strings.EqualFold(m, q.Machine) }) ||
			!inDates(r.Updated, q.Age, since, until, now) ||
			q.State == "kept" && !r.Kept
	})
	if dest != "" {
		files := map[string]bucketFiles{} // each bucket listed once
		for _, r := range rows {
			for _, b := range r.Buckets {
				if _, ok := files[b]; !ok {
					files[b] = listBucket(dest, b, q.Look == "archive")
				}
			}
			for _, b := range r.Buckets {
				f := files[b]
				if zp, ok := f.zips[r.ID]; ok && !r.Compressed {
					if st, err := os.Lstat(zp); err == nil {
						r.Compressed, r.Size = true, st.Size()
					}
				}
				r.Forked = r.Forked || f.forks[r.ID]
				r.Readable = r.Readable || f.pages[r.ID]
			}
			sort.SliceStable(r.Buckets, func(i, j int) bool { return files[r.Buckets[i]].pages[r.ID] && !files[r.Buckets[j]].pages[r.ID] })
		}
	}
	rows = slices.DeleteFunc(rows, func(r *convRow) bool {
		t, ok := local[r.ID]
		r.InUse = ok && now.Sub(t) < archiveLag
		return q.State == "compressed" && !r.Compressed || q.State == "forked" && !r.Forked || q.State == "inuse" && !r.InUse
	})

	// Search reads only the conversations every other filter kept (Ruling P49), and
	// Limit counts conversations: each stops at its first match.
	if strings.TrimSpace(q.Text) != "" && len(rows) > 0 {
		o := searchOpts{ClaudeDir: root, Query: q.Text, Role: q.Role, Archived: q.Look == "archive",
			Regexp: q.Regexp, CaseSensitive: q.CaseSensitive, All: q.All, Limit: q.Limit,
			Only: map[string]bool{}, FirstHit: true}
		if o.Archived {
			o.ArchiveDir = dest // the archive listed above
		}
		for _, r := range rows {
			o.Only[r.ID] = true
		}
		res, err := runSearch(quietStreams(), o)
		if err != nil && !errors.Is(err, fs.ErrNotExist) { // no conversations folder yet: no match
			return convPage{}, err
		}
		hit := map[string]bool{}
		for _, h := range res.Hits {
			hit[strings.ToLower(h.SessionID)] = true
		}
		rows = slices.DeleteFunc(rows, func(r *convRow) bool { return !hit[r.ID] })
		pg.Stopped, pg.Unreadable = res.Stopped, pg.Unreadable+res.Unreadable
	}

	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		return a.Updated.After(b.Updated) || a.Updated.Equal(b.Updated) && a.ID < b.ID
	})
	pg.Total, pg.Per = len(rows), q.Per
	pg.Pages = max(1, (pg.Total+q.Per-1)/q.Per)
	pg.Page = min(q.Page, pg.Pages)
	pg.Rows = []convRow{}
	for i, r := range rows {
		pg.TotalBytes += r.Size
		if i >= (pg.Page-1)*q.Per && i < pg.Page*q.Per {
			sort.Strings(r.Machines)
			pg.Rows = append(pg.Rows, *r)
		}
	}
	return pg, nil
}

// inDates reports whether t passes the age and date filters. A zero t is unknown
// (Rulings P33, P43), so no age or date selects it.
func inDates(t time.Time, age string, since, until, now time.Time) bool {
	if t.IsZero() {
		return age == "" && since.IsZero() && until.IsZero()
	}
	switch age {
	case "7d":
		since = maxTime(since, now.AddDate(0, 0, -7))
	case "30d":
		since = maxTime(since, now.AddDate(0, 0, -30))
	case "over90d":
		if !t.Before(now.AddDate(0, 0, -90)) {
			return false
		}
	case "over1y":
		if !t.Before(now.AddDate(-1, 0, 0)) {
			return false
		}
	}
	return !t.Before(since) && (until.IsZero() || !t.After(until))
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// bucketFiles is what three folder listings say of one archive bucket, by lower-cased
// conversation id.
type bucketFiles struct {
	zips         map[string]string
	forks, pages map[string]bool
}

// listBucket lists b's zips (when zips is set), forks and pages by name: nothing in
// them is opened.
func listBucket(dest, b string, zips bool) bucketFiles {
	f := bucketFiles{forks: map[string]bool{}, pages: map[string]bool{}}
	if !archive.ValidName(b) {
		return f
	}
	if zips {
		f.zips = convzip.Find(filepath.Join(archive.ProjectsDir(dest), b))
	}
	ents, _ := os.ReadDir(filepath.Join(dest, "forks", b))
	for _, e := range ents {
		if e.IsDir() {
			f.forks[strings.ToLower(e.Name())] = true
		}
	}
	ents, _ = os.ReadDir(filepath.Join(dest, "html", b))
	for _, e := range ents {
		if id, ok := strings.CutSuffix(e.Name(), ".html"); ok && e.Type().IsRegular() {
			f.pages[strings.ToLower(id)] = true
		}
	}
	return f
}

// panelDest is the archive folder, or why the panel has none: "none" when no folder is
// chosen, "unreachable" when it cannot be reached.
func panelDest(root string) (dest, problem string) {
	dest, _, err := archive.ResolveDestination(root, "")
	if err != nil || dest == "" {
		return "", "none"
	}
	if !answers(dest) {
		return "", "unreachable"
	}
	return dest, ""
}

// destLimit is how long a view waits for the archive folder to answer; statDest is
// os.Stat. A test replaces both.
var (
	destLimit = 3 * time.Second
	statDest  = os.Stat
)

// statRun is a Stat of an archive folder in progress.
type statRun struct {
	started time.Time
	done    chan struct{}
	ok      bool // set before done is closed
}

var (
	statMu   sync.Mutex
	statting = map[string]*statRun{}
)

// answers reports whether dest is a folder, as a Stat says within destLimit of starting.
// On a hung network mount a Stat never returns, so callers share the one in progress:
// reloads leave one blocked read, not one each, and once it is late they answer at once.
func answers(dest string) bool {
	statMu.Lock()
	r := statting[dest]
	if r == nil {
		r = &statRun{started: time.Now(), done: make(chan struct{})}
		statting[dest] = r
		stat := statDest
		go func() {
			st, err := stat(dest)
			r.ok = err == nil && st.IsDir()
			statMu.Lock()
			delete(statting, dest)
			statMu.Unlock()
			close(r.done)
		}()
	}
	statMu.Unlock()
	late := time.NewTimer(time.Until(r.started.Add(destLimit)))
	defer late.Stop()
	select {
	case <-r.done:
		return r.ok
	case <-late.C:
		return false
	}
}

// parseConvQuery reads /api/conversations' parameters. limit counts conversations: 50
// when not given, 0 for all.
func parseConvQuery(v url.Values) (convQuery, error) {
	q := convQuery{Text: v.Get("q"), Project: v.Get("project"), Machine: v.Get("machine"), Age: v.Get("age"),
		State: v.Get("state"), Look: v.Get("look"), Role: v.Get("role"), Since: v.Get("since"), Until: v.Get("until"), Limit: 50}
	for name, p := range map[string]*bool{"case": &q.CaseSensitive, "regexp": &q.Regexp, "all": &q.All} {
		if s := v.Get(name); s != "" {
			b, err := strconv.ParseBool(s)
			if err != nil {
				return q, badQuery("The search options are on or off.")
			}
			*p = b
		}
	}
	for name, p := range map[string]*int{"limit": &q.Limit, "page": &q.Page, "per": &q.Per} {
		if s := v.Get(name); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil {
				return q, badQuery("Pages and match counts are whole numbers.")
			}
			*p = n
		}
	}
	return q, nil
}

func (u *uiServer) apiProjects(w http.ResponseWriter, r *http.Request) {
	dest, problem := panelDest(u.root)
	if problem != "" {
		sendJSON(w, map[string]string{"problem": problem})
		return
	}
	opts, _, err := queryProjects(dest, nil)
	if err != nil {
		u.logf("PANEL: projects: %v", err)
		sendStatus(w, http.StatusInternalServerError, map[string]string{"error": plainError(err)})
		return
	}
	sendJSON(w, opts)
}

// apiConversations answers with no archive too: with its problem, or, for look=local,
// with this computer's conversations.
func (u *uiServer) apiConversations(w http.ResponseWriter, r *http.Request) {
	q, err := parseConvQuery(r.URL.Query())
	if err == nil {
		dest, problem := panelDest(u.root)
		if problem != "" && q.Look != "local" {
			sendJSON(w, map[string]string{"problem": problem})
			return
		}
		var pg convPage
		if pg, err = queryConversations(u.root, dest, q, time.Now()); err == nil {
			sendJSON(w, pg)
			return
		}
	}
	var bad badQuery
	if errors.As(err, &bad) {
		sendStatus(w, http.StatusBadRequest, map[string]string{"error": string(bad)})
		return
	}
	u.logf("PANEL: conversations: %v", err)
	sendStatus(w, http.StatusInternalServerError, map[string]string{"error": plainError(err)})
}
