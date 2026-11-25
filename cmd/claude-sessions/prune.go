package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/convzip"
	"github.com/ashiqfardus/claude-sessions-sync/internal/rewrite"
)

// pruneSel chooses what prune deletes. Different kinds combine with AND, several of one
// kind with OR.
type pruneSel struct {
	OlderThan     int // days since the last message; 0 when not given
	Projects, IDs []string
}

// pruneItem is one conversation prune deletes: every archived copy of it.
type pruneItem struct {
	ID, Project string
	Buckets     []string
	LastAt      time.Time // the newest copy's last turn
	LastUUID    string    // that turn's uuid, "" for none; while listed, also "" when that copy is zipped: only the check before recording reads a zip for it
	Prompt      string
	Size        int64
	dated       bool    // every copy had a time when listed
	sizes       []int64 // every copy's size when dated, in the order read: a turn added grows one
}

type prunePlan struct {
	Remove   []pruneItem
	Kept     int                        // matched, but on the keep list
	Holdouts []string                   // machines whose build would upload them again
	Unread   string                     // why a manifest could not be read, if one could not
	Matched  map[string]archive.Project // the buckets --project named

	// What applyPrune dates each conversation by again, right before deleting it.
	cutoff time.Time           // zero without --older-than
	local  map[string][]string // this machine's own copies, by id

	// The age rule's budget. Planning stops at its first half, and the checks before
	// recording at its end, so the checks always have the second half.
	deadline time.Time // zero for none
	cut      string    // the id planning stopped before; "" when it went through
}

// ageScan is how the age rule selects: unattended, once a day, on an archive that may be
// a drive that fetches each file only when it is opened. It dates by stat first. A
// transcript or fork whose file changed after the cutoff is too young to select, so its
// tail is not read. A zip is dated by its file's own time, which Zip sets to the newest
// of its files', its last activity, and neither its index nor its content is read. It
// starts at from, where the last run stopped, and stops choosing once half its budget
// (0 for none) is spent, leaving the rest to the checks. This only selects: applyPrune
// dates each one selected exactly before anything is recorded, as for prune.
type ageScan struct {
	budget time.Duration
	from   string
}

var errNoSelector = errors.New("prune needs --older-than, --project or a conversation id")

// stringList is a flag given once per value.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

// Set skips a repeat: parseFlags parses every flag twice.
func (s *stringList) Set(v string) error {
	if !slices.Contains(*s, v) {
		*s = append(*s, v)
	}
	return nil
}

// cmdPrune deletes chosen conversations from the archive - every copy, in every bucket -
// and records them so no computer uploads them again. Nothing on any computer is
// touched.
func cmdPrune(args []string) error {
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	archiveDir := fs.String("archive", "", "override the synced destination folder")
	older := fs.String("older-than", "", "only conversations whose last message is older than this, as <N>d")
	var projects stringList
	fs.Var(&projects, "project", "only this project's conversations, by folder name or path (repeatable)")
	yes := fs.Bool("yes", false, "delete them; without it, only list what would be deleted")
	undo := fs.Bool("undo", false, "take the conversations given off the deleted list")
	if err := parseFlags(fs, args, true); err != nil {
		return err
	}
	sel := pruneSel{Projects: projects}
	for _, id := range fs.Args() {
		if !claude.IsSessionID(id) {
			return fmt.Errorf("%q is not a conversation id (a project goes with --project)", id)
		}
		sel.IDs = append(sel.IDs, strings.ToLower(id))
	}
	if *older != "" {
		n, err := strconv.Atoi(strings.TrimSuffix(*older, "d"))
		if !strings.HasSuffix(*older, "d") || err != nil || n < 1 || n > archive.MaxKeepDays {
			return fmt.Errorf("--older-than takes a number of days from 1 to %d, such as 365d", archive.MaxKeepDays)
		}
		sel.OlderThan = n
	}
	switch {
	case *undo && (sel.OlderThan > 0 || len(sel.Projects) > 0 || len(sel.IDs) == 0):
		return fmt.Errorf("usage: claude-sessions prune --undo <conversation id>...")
	case !*undo && sel.OlderThan == 0 && len(sel.Projects) == 0 && len(sel.IDs) == 0:
		return errNoSelector
	}
	for i, p := range sel.Projects { // read as config keep reads it
		p = expandHome(p)
		if runtime.GOOS == "windows" && gitBashRewritten(p) {
			return gitBashError(p)
		}
		if p == "." || p == ".." || !rewrite.AbsoluteAnywhere(p) && strings.ContainsAny(p, `/\`) {
			abs, err := filepath.Abs(p)
			if err != nil {
				return err
			}
			p = abs
		}
		sel.Projects[i] = p
	}

	root, _, err := resolveRoot(*claudeDir)
	if err != nil {
		return err
	}
	dest, _, err := archive.ResolveDestination(root, *archiveDir)
	if err != nil {
		return err
	}
	if dest == "" {
		return fmt.Errorf("no archive destination: run `claude-sessions config set-destination <folder>`")
	}
	if err := archive.ValidateDestination(root, dest); err != nil {
		return err
	}
	if !archive.IsRealDir(archive.ProjectsDir(dest)) {
		return fmt.Errorf("%s holds no archived sessions", dest)
	}
	if *yes || *undo {
		// This machine's push must not archive a copy, or write the list, meanwhile.
		lock, err := archive.AcquireLockWait(root, time.Minute)
		if err != nil {
			return err
		}
		defer lock.Release()
	}
	if *undo {
		return undoPrune(dest, sel.IDs, time.Now())
	}

	ret, err := archive.LoadRetention(dest)
	if err != nil {
		return err
	}
	plan, err := planPrune(root, dest, sel, ret, time.Now())
	if err != nil {
		return err
	}
	printPrunePlan(plan, *yes)
	if !*yes || len(plan.Remove) == 0 {
		return nil
	}
	n, freed, err := applyPrune(dest, plan, time.Now(), func(it pruneItem, err error) {
		fmt.Fprintf(os.Stderr, "  x %s: %v\n", it.ID, err)
	})
	if n > 0 {
		fmt.Printf("\nDeleted %d conversation(s), %s.\n", n, mb(freed))
		fmt.Println("A sync client may keep deleted files in its trash, still counting toward your storage -\n" +
			"Google Drive does, for 30 days. Empty it to get the space back now.")
	}
	return err
}

// planPrune finds the archived conversations sel names, leaving out those on the keep
// list. root is this machine's profile, whose own copy of a conversation counts toward
// its age; "" for none.
func planPrune(root, dest string, sel pruneSel, ret archive.Retention, now time.Time) (prunePlan, error) {
	return planWith(root, dest, sel, ret, now, nil)
}

// planWith is planPrune, dated as scan says for the age rule; nil for exact dates.
func planWith(root, dest string, sel pruneSel, ret archive.Retention, now time.Time, scan *ageScan) (prunePlan, error) {
	var plan prunePlan
	if sel.OlderThan <= 0 && len(sel.Projects) == 0 && len(sel.IDs) == 0 {
		return plan, errNoSelector
	}
	projects, err := projectsOf(dest)
	if err != nil {
		plan.Unread = err.Error()
		projects, _, _ = archive.Merged(dest) // what can be read, to list by
	}
	if plan.Holdouts, err = archive.Holdouts(dest, func(s archive.Shard) bool { return s.HonorsDeleted }); err != nil && plan.Unread == "" {
		plan.Unread = err.Error()
	}

	inProject := map[string]bool{}
	for _, arg := range sel.Projects {
		_, buckets, err := matchProject(projects, arg)
		if err != nil {
			return prunePlan{}, err
		}
		if plan.Matched == nil {
			plan.Matched = map[string]archive.Project{}
		}
		for _, b := range buckets {
			inProject[b], plan.Matched[b] = true, projects[b]
		}
	}
	named := map[string]bool{}
	for _, id := range sel.IDs {
		named[strings.ToLower(id)] = true
	}
	local := map[string][]string{}
	plan.local = local
	if root != "" {
		buckets, _ := claude.ListBuckets(claude.ProjectsDir(root))
		for _, b := range buckets {
			for _, t := range b.Transcripts {
				local[strings.ToLower(t.ID)] = append(local[strings.ToLower(t.ID)], t.Path)
			}
		}
	}
	isKept := keptBy(ret, projects)      // once: SameRemote is a regex per call
	rows := map[string]archive.Session{} // the newest index row of each, for its project and prompt
	all, _ := archive.AllSessions(dest)
	for _, r := range all {
		if _, ok := rows[strings.ToLower(r.ID)]; !ok {
			rows[strings.ToLower(r.ID)] = r
		}
	}
	cutoff := now.AddDate(0, 0, -sel.OlderThan)
	if sel.OlderThan > 0 {
		plan.cutoff = cutoff
	}

	holders, err := holdersOf(dest)
	if err != nil && plan.Unread == "" {
		plan.Unread = err.Error() // a bucket left out may hold the newest copy
	}
	ids := make([]string, 0, len(holders))
	for id := range holders {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	mode, planned := dateListed, time.Time{} // when choosing stops; zero for never
	if scan != nil {
		mode = dateQuick
		if scan.budget > 0 {
			start := pruneClock()
			plan.deadline, planned = start.Add(scan.budget), start.Add(scan.budget/2)
		}
		k := sort.SearchStrings(ids, scan.from)
		ids = append(slices.Clone(ids[k:]), ids[:k]...)
	}
	for i, id := range ids {
		if i > 0 && !planned.IsZero() && !pruneClock().Before(planned) {
			plan.cut = id
			break
		}
		buckets := holders[id]
		if len(named) > 0 && !named[id] ||
			len(sel.Projects) > 0 && !slices.ContainsFunc(buckets, func(b string) bool { return inProject[b] }) {
			continue
		}
		// A conversation only in buckets no manifest lists has no identity a keep entry
		// could name, so age alone never selects it.
		if sel.OlderThan > 0 && !slices.ContainsFunc(buckets, func(b string) bool { _, ok := projects[b]; return ok }) {
			continue
		}
		if scan != nil && isKept(id, buckets) {
			continue // kept at any age: not worth a read
		}
		it := pruneItem{ID: id, Buckets: buckets}
		dated := datePrune(dest, &it, local[id], now, mode, cutoff)
		it.dated = dated
		if sel.OlderThan > 0 && (!dated || !it.LastAt.Before(cutoff)) {
			continue
		}
		if isKept(id, buckets) {
			plan.Kept++
			continue
		}
		it.Project, it.Prompt = rows[id].Project, rows[id].Prompt
		for _, b := range buckets {
			if p := projects[b].Path; it.Project == "" && p != "" {
				it.Project = p
			}
			if p := archivedCopy(dest, b, id); it.Prompt == "" && p != "" {
				s, _ := claude.ScanHead(p, headScanLines)
				it.Prompt = s.FirstPrompt
			}
		}
		if it.Project == "" {
			it.Project = buckets[0]
		}
		it.Size = conversationSize(dest, it)
		plan.Remove = append(plan.Remove, it)
	}
	return plan, nil
}

// datePrune sets it.LastAt to the newest last turn of any copy - loose, zipped, forked,
// or this machine's own - and LastUUID to that turn's. A loose copy with no turn time
// is dated by its modification time, taken after its last turn is read. It reports
// false when any copy cannot be dated, or is dated after now - a wrong clock: the
// conversation may be younger than it looks, and a LastAt still to come would make push
// skip turns added before then.
//
// mode says how a zipped copy is read, and whether stat comes first:
//   - dateListed, prune's listing: by its transcript entry's modification time, from the
//     zip's index alone, never before its last turn; no uuid.
//   - dateQuick, the age rule's selection (ageScan): a file changed after cutoff dates
//     the conversation by that time, too young to select, and nothing more of it is
//     read; a zip is dated by its file's own time; no uuid from a zip.
//   - dateExact, the check before recording: by its transcript's last turn and uuid,
//     read from the zip itself.
func datePrune(dest string, it *pruneItem, local []string, now time.Time, mode dateMode, cutoff time.Time) bool {
	dated, young, newest := true, false, ""
	use := func(at time.Time, uuid string) {
		if at.IsZero() || at.After(now) {
			dated = false
		} else if at.After(it.LastAt) {
			it.LastAt, newest = at, uuid
		}
	}
	size := func(p string) (os.FileInfo, error) {
		st, err := os.Lstat(p)
		if err == nil {
			it.sizes = append(it.sizes, st.Size())
		}
		return st, err
	}
	tooYoung := func(st os.FileInfo) bool {
		if mode == dateQuick && st.ModTime().After(cutoff) {
			use(st.ModTime(), "")
			young = true
		}
		return young
	}
	loose := func(p string) {
		if young {
			return
		}
		st, err := size(p)
		if err == nil && tooYoung(st) {
			return
		}
		uuid, at := lastTurn(p) // one read: both name the same turn while it grows
		// Its time, with no turn time, from after that read: never older than what it read.
		if st, err := os.Lstat(p); at.IsZero() && err == nil {
			at = st.ModTime()
		}
		use(at, uuid)
	}
	for _, b := range it.Buckets {
		p, zp := archivedCopy(dest, b, it.ID), zippedCopy(dest, b, it.ID)
		if p == "" && zp == "" {
			dated = false // named in another case, it is not read as it.ID
		}
		if p != "" {
			loose(p)
		}
		if zp != "" && !young {
			switch st, err := size(zp); {
			case err != nil:
				use(time.Time{}, "")
			case mode == dateQuick:
				if !tooYoung(st) {
					use(st.ModTime(), "") // its last activity: Zip stamps it so
				}
			case mode == dateExact:
				use(zippedLastTurn(zp, it.ID))
			default:
				use(zippedTurnTime(zp, it.ID), "")
			}
		}
		forks, err := forkCopies(dest, b, it.ID)
		if err != nil {
			dated = false // a fork it cannot list may hold the newest turn
		}
		for _, p := range forks {
			loose(p)
		}
	}
	for _, p := range local {
		loose(p)
	}
	if !young {
		it.LastUUID = newest
	}
	return dated && !it.LastAt.IsZero()
}

// dateMode is how datePrune reads a conversation.
type dateMode int

const (
	dateListed dateMode = iota // prune's listing: a zip by its index's transcript entry
	dateQuick                  // the age rule's selection, see ageScan: stat first, a zip by its file's time
	dateExact                  // before recording: a zip by its own transcript's last turn, read from it
)

// zippedLastTurn is the last turn of the transcript in the zip zp, read from the zip
// itself, as every reader sees it: its time, else its entry's, and its uuid. Zero when it
// cannot be read. Only the check before recording reads it, for the few being deleted.
func zippedLastTurn(zp, id string) (time.Time, string) {
	p, done, err := materialize(filepath.Dir(zp), id, "")
	if err != nil {
		return time.Time{}, ""
	}
	defer done()
	uuid, at := lastTurn(p)
	if st, err := os.Stat(p); at.IsZero() && err == nil {
		at = st.ModTime() // the entry's: extracting keeps it
	}
	return at, uuid
}

// lastTurn is how prune reads a transcript's last turn; tests count the reads.
var lastTurn = claude.LastTurn

// zippedTurnTime is the zip's transcript entry's modification time, zero for none.
func zippedTurnTime(zp, id string) time.Time {
	ents, err := readZipIndex(zp)
	if err != nil {
		return time.Time{}
	}
	for name, e := range ents {
		if strings.EqualFold(name, id+".jsonl") {
			return e.ModTime
		}
	}
	return time.Time{}
}

func forkDir(dest, bucket, id string) string { return filepath.Join(dest, "forks", bucket, id) }

// forkCopies are the forks kept beside bucket's copy of id: forks/<bucket>/<id>/<machine>.jsonl.
// A folder there that cannot be read is an error: it may hold one.
func forkCopies(dest, bucket, id string) ([]string, error) {
	dir := forkDir(dest, bucket, id)
	if !archive.ThroughRealDirs(dest, dir) || !archive.IsRealDir(dir) {
		return nil, nil
	}
	ents, err := os.ReadDir(dir)
	var out []string
	for _, e := range ents {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".jsonl") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out, err
}

// conversationSize is every copy's transcript, zip, folder, page and forks.
func conversationSize(dest string, it pruneItem) int64 {
	var n int64
	for _, b := range it.Buckets {
		forks, _ := forkCopies(dest, b, it.ID)
		files := append(forks, archivedCopy(dest, b, it.ID), zippedCopy(dest, b, it.ID))
		for _, p := range files {
			if st, err := os.Lstat(p); p != "" && err == nil {
				n += st.Size()
			}
		}
		n += copySize(dest, b, it.ID)
	}
	return n
}

// pastDeadline reports whether the age rule's budget is spent. Its first conversation
// is always read, so every run gets somewhere.
func pastDeadline(plan prunePlan) bool {
	return !plan.deadline.IsZero() && !pruneClock().Before(plan.deadline)
}

// pruneClock is the age rule's budget's clock; tests drive it.
var pruneClock = time.Now

// errOutOfTime marks the first conversation the age rule's budget left unchecked, and
// so neither recorded nor deleted: where its next run starts.
var errOutOfTime = errors.New("left for the next run")

// applyPrune deletes what plan lists. The caller holds the local lock: push's age rule
// runs it inside its own. It deletes nothing while a manifest or the deleted list cannot
// be read, or a machine's build would upload the conversations again.
//
// Each conversation's paths are checked, and it is found and dated again, exactly - a
// zip by its own transcript's last turn - before any is recorded: one refused, no longer
// there, changed since it was listed - a copy added, gone or grown, a newer turn, one
// no longer datable - or by its exact date no longer older than the cutoff is reported
// to fail and neither recorded nor deleted. The rest are recorded on the deleted list
// with that exact date, set in plan.Remove too, then deleted: only the paths checked here.
// Past plan's deadline it checks no more, reports the first left with errOutOfTime, and
// records and deletes those checked.
func applyPrune(dest string, plan prunePlan, now time.Time, fail func(pruneItem, error)) (deleted int, freed int64, err error) {
	if plan.Unread != "" {
		return 0, 0, fmt.Errorf("nothing deleted: %s. The computer it belongs to may upload them again", plan.Unread)
	}
	if len(plan.Holdouts) > 0 {
		return 0, 0, fmt.Errorf("nothing deleted: %s", holdoutRefusal(plan.Holdouts))
	}
	listed, err := archive.LoadDeleted(dest)
	if err != nil {
		return 0, 0, fmt.Errorf("nothing deleted: the deleted list cannot be read: %w", err)
	}
	holders, err := holdersOf(dest)
	if err != nil {
		return 0, 0, fmt.Errorf("nothing deleted: %w", err)
	}
	var ready []pruneItem
	var checked [][]prunePath
	var entries []archive.DeletedEntry
	todo := len(plan.Remove)
	for i := range plan.Remove {
		it := &plan.Remove[i]
		if i > 0 && pastDeadline(plan) {
			todo = i
			fail(*it, errOutOfTime)
			break
		}
		paths, err := prunePaths(dest, *it)
		if err == nil && len(paths) == 0 {
			err = errNotFound
		}
		if err != nil {
			fail(*it, err)
			continue
		}
		again := pruneItem{ID: it.ID, Buckets: it.Buckets}
		dated := datePrune(dest, &again, plan.local[it.ID], now, dateExact, time.Time{})
		last := again.LastAt.Local().Format("2006-01-02 15:04")
		// A copy grown since is a turn added, whatever its time says - none, one ahead, or
		// one from a clock behind. Its time is not compared with the listing's: that read a
		// zip's index, to the second, or for the age rule only its file's time.
		switch {
		case !slices.Equal(holders[it.ID], it.Buckets) || it.dated && !dated || !slices.Equal(again.sizes, it.sizes):
			fail(*it, fmt.Errorf("it changed since it was listed (last message %s); not deleted", last))
			continue
		case !plan.cutoff.IsZero() && !again.LastAt.Before(plan.cutoff): // a zip's time behind its last turn
			fail(*it, fmt.Errorf("its last message, %s, is newer than it looked; not deleted", last))
			continue
		}
		it.LastAt, it.LastUUID = again.LastAt, again.LastUUID // recorded exactly, never by a zip's file time
		ready, checked = append(ready, *it), append(checked, paths)
		entries = append(entries, archive.DeletedEntry{ID: it.ID, Buckets: it.Buckets, LastUUID: it.LastUUID, LastAt: it.LastAt,
			DeletedAt: after(now, listed, it.ID)})
	}
	if len(entries) > 0 {
		if err := archive.RecordDeleted(dest, machineName(), entries); err != nil {
			return 0, 0, fmt.Errorf("nothing deleted: the deleted list could not be written: %w", err)
		}
	}
	for i, it := range ready {
		if err := deleteConversation(dest, it, checked[i]); err != nil {
			fail(it, err)
			continue
		}
		deleted++
		freed += it.Size
	}
	if deleted < todo {
		err = fmt.Errorf("%d conversation(s) were not deleted", todo-deleted)
	}
	return deleted, freed, err
}

// after is the time for a new deleted-list entry for id: now, or just after the entry
// it replaces, so a computer whose clock is behind still has the last word.
func after(now time.Time, listed map[string]archive.DeletedEntry, id string) time.Time {
	if was, ok := listed[strings.ToLower(id)]; ok && !now.After(was.DeletedAt) {
		return was.DeletedAt.Add(time.Nanosecond)
	}
	return now
}

func holdoutRefusal(machines []string) string {
	verb := "run"
	if len(machines) == 1 {
		verb = "runs"
	}
	return strings.Join(machines, ", ") + " " + verb + " a claude-sessions too old to honour the deleted list, and would upload the conversations again.\n" +
		"Update claude-sessions on each and let it push once, or run `claude-sessions machines --forget <name>` for one that is gone."
}

var errNotFound = errors.New("no copy of it is in the archive any more")

// removePath deletes one checked path: a folder with all in it, a file alone, so a file
// made a folder since it was checked is refused, not emptied. Tests make it fail.
var removePath = func(p string, dir bool) error {
	if dir {
		return os.RemoveAll(p) // never follows a link inside it, or in its place
	}
	return os.Remove(p)
}

// prunePath is one path of a conversation prune deletes.
type prunePath struct {
	path string
	dir  bool // its folder or forks; otherwise a file
}

// deleteConversation removes the conversation from every bucket in it.Buckets: its
// folder, its forks, its page, then its transcript and zip, so a run stopped part way
// leaves it listed for the next run to delete again. Only the paths in checked are
// deleted: one that appeared since, a continuation pushed by another computer perhaps,
// was never looked at. The paths are checked again first, and nothing is deleted when a
// link or a stray file now stands on the way to one.
func deleteConversation(dest string, it pruneItem, checked []prunePath) error {
	paths, err := prunePaths(dest, it)
	if err != nil {
		return err
	}
	n := 0
	for _, p := range paths {
		if !slices.Contains(checked, p) {
			continue
		}
		if err := removePath(p.path, p.dir); err != nil {
			return err
		}
		n++
	}
	if n == 0 {
		return errNotFound
	}
	return nil
}

// prunePaths are the paths of it that exist, in the order they are deleted, or an error
// when a link or a stray file stands on the way to any of them.
func prunePaths(dest string, it pruneItem) ([]prunePath, error) {
	if !claude.IsSessionID(it.ID) {
		return nil, fmt.Errorf("%q is not a conversation id", it.ID)
	}
	var dirs, files []string
	for _, b := range it.Buckets {
		if !archive.ValidName(b) {
			return nil, fmt.Errorf("%q is not a bucket name", b)
		}
		bucketDir := filepath.Join(archive.ProjectsDir(dest), b)
		bd := []string{filepath.Join(bucketDir, it.ID), forkDir(dest, b, it.ID)}
		bf := []string{pagePath(dest, b, it.ID), filepath.Join(bucketDir, it.ID+".jsonl"), convzip.Path(bucketDir, it.ID)}
		for _, p := range append(slices.Clone(bd), bf...) {
			if !archive.ThroughRealDirs(dest, p) {
				return nil, fmt.Errorf("a link stands on the way to %s; nothing deleted", p)
			}
		}
		for _, d := range bd {
			if _, err := os.Lstat(d); err == nil && !archive.IsRealDir(d) {
				return nil, fmt.Errorf("%s is not a plain folder (a link?); nothing deleted", d)
			}
		}
		for _, f := range bf {
			if st, err := os.Lstat(f); err == nil && !st.Mode().IsRegular() {
				return nil, fmt.Errorf("%s is not a plain file; nothing deleted", f)
			}
		}
		dirs, files = append(dirs, bd...), append(files, bf...)
	}
	var out []prunePath
	for i, p := range append(dirs, files...) {
		if _, err := os.Lstat(p); err == nil {
			out = append(out, prunePath{p, i < len(dirs)})
		}
	}
	return out, nil
}

// withoutPruned leaves out index rows of conversations on the deleted list by the rule
// push skips a local copy by, applied to the row's archived copy: one push archived
// again, continued since it was pruned, stays listed. A row whose copy is gone is left
// out. A list that cannot be read leaves out nothing: the rows only list what is there.
func withoutPruned(dest string, rows []archive.Session) []archive.Session {
	d, err := archive.LoadDeleted(dest)
	if err != nil || len(d) == 0 {
		return rows
	}
	return slices.DeleteFunc(rows, func(r archive.Session) bool {
		e, ok := d[strings.ToLower(r.ID)]
		if !ok || e.Undone {
			return false
		}
		if p := archivedCopy(dest, r.Bucket, r.ID); p != "" {
			return notNewer(e, p)
		}
		if zp := zippedCopy(dest, r.Bucket, r.ID); zp != "" {
			return !zippedTurnTime(zp, r.ID).After(e.LastAt) // dated by its index, as prune dates it
		}
		return true
	})
}

// prunedHere reports whether push leaves this computer's transcript of id at path out
// of the archive: id is on the deleted list, not undone, and the transcript holds no
// turn newer than the deletion recorded. One continued since is archived again.
func prunedHere(d map[string]archive.DeletedEntry, id, path string) bool {
	e, ok := d[strings.ToLower(id)]
	return ok && !e.Undone && notNewer(e, path)
}

// notNewer reports whether the transcript at path holds no turn after e's last. By the
// turn's uuid when e has one: the transcript's last turn is it, and turns after it are
// newer whatever their time. Otherwise by time - its last turn's, else its modification
// time - no later than e's; one still to come is later, so the transcript is kept.
func notNewer(e archive.DeletedEntry, path string) bool {
	uuid, at := claude.LastTurn(path)
	if e.LastUUID != "" && uuid == e.LastUUID {
		return true
	}
	if at.IsZero() {
		st, err := os.Stat(path)
		if err != nil {
			return false
		}
		at = st.ModTime()
	}
	if at.After(e.LastAt) {
		return false
	}
	// No later by time: still newer when e's last turn is in it with turns after it,
	// written by a clock behind. That reads all of it, so it comes last.
	return e.LastUUID == "" || !claude.HasUUID(path, e.LastUUID)
}

// ageRuleEvery is how often the age rule runs on one computer: it dates every
// archived conversation.
const ageRuleEvery = 24 * time.Hour

// ageRuleBudget bounds the sweep's age rule, as it does pages; tests shrink it.
var ageRuleBudget = quietRenderBudget

// pruneLastPath records when the age rule last ran on this computer.
func pruneLastPath(root string) string {
	return filepath.Join(root, "session-sync-base", "prune-last.json")
}

// pruneRun is prune-last.json.
type pruneRun struct {
	LastRun time.Time `json:"lastRun"`
	Next    string    `json:"next,omitempty"` // where a run its budget cut short stopped; the next starts there
	// KeepDays is the setting the run went by: one lower since is a first run again.
	KeepDays int `json:"keepDays,omitempty"`
	// KeepDaysSetAt is retention.json's when the run went by: another since is a change.
	KeepDaysSetAt *time.Time `json:"keepDaysSetAt,omitempty"`
}

// pruneLast is the age rule's last run here; false for never, or a file it cannot read.
func pruneLast(root string) (pruneRun, bool) {
	var r pruneRun
	raw, err := os.ReadFile(pruneLastPath(root))
	if err != nil || json.Unmarshal(raw, &r) != nil || r.LastRun.IsZero() {
		return pruneRun{}, false
	}
	return r, true
}

func savePruneLast(root string, r pruneRun) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return archive.WriteFileAtomic(pruneLastPath(root), append(data, '\n'), 0o644)
}

// runAgeRule deletes what `prune --older-than <keep-days>d --yes` would, gate and all,
// at most once per ageRuleEvery here, dated as ageScan says and within budget (0 for
// none): with keep-days off it does nothing. Push runs it holding the lock, once its
// own shard says it honours the deleted list; never from a hook or a dry run. Each
// conversation deleted is logged.
func runAgeRule(root, dest string, ret archive.Retention, now time.Time, budget time.Duration, logf func(string, ...any)) (deleted int, err error) {
	if ret.KeepDays < archive.MinKeepDays {
		_ = os.Remove(pruneLastPath(root)) // so turning it on again also waits a day
		return 0, nil
	}
	last, ok := pruneLast(root)
	// A last run still to come is a clock that was ahead: it runs now, not a year on.
	if ok && !last.LastRun.After(now) && now.Sub(last.LastRun) < ageRuleEvery {
		return 0, nil
	}
	// Recorded first, so a run refused or killed part way also waits a day.
	run := pruneRun{LastRun: now, Next: last.Next, KeepDays: ret.KeepDays, KeepDaysSetAt: ret.KeepDaysSetAt}
	if err := savePruneLast(root, run); err != nil {
		return 0, err
	}
	// The first run here, or the first since keep-days was changed, only records its
	// time: a day to undo a wrong keep-days, and for the sync app to finish filling a
	// new computer's view of the archive. A change is retention.json's keepDaysSetAt not
	// the one this computer's last run went by - compared, not ordered, so no clock is
	// trusted - or, in a file without it, a setting lower than that run's. A run
	// recorded without its setting is a first.
	switch {
	case !ok || last.KeepDays == 0:
		logf("KEEP-DAYS: the first run here deletes nothing; deleting starts with the next, a day on")
		return 0, nil
	case ret.KeepDays < last.KeepDays:
		logf("KEEP-DAYS: keep-days went down from %d to %d, so this run deletes nothing; deleting starts with the next, a day on", last.KeepDays, ret.KeepDays)
		return 0, nil
	case ret.KeepDaysSetAt != nil && (last.KeepDaysSetAt == nil || !ret.KeepDaysSetAt.Equal(*last.KeepDaysSetAt)):
		logf("KEEP-DAYS: keep-days was set to %d at %s, after this computer's last run, so this run deletes nothing; deleting starts with the next, a day on",
			ret.KeepDays, ret.KeepDaysSetAt.Local().Format("2006-01-02 15:04"))
		return 0, nil
	}
	// A conflict copy may hold a keep entry the file read lost.
	if copies, err := archive.RetentionConflicts(dest); err != nil || len(copies) > 0 {
		if err != nil {
			return 0, fmt.Errorf("nothing deleted: %w", err)
		}
		return 0, fmt.Errorf("nothing deleted: %s beside retention.json is a sync app's conflict copy, and may keep what retention.json does not. "+
			"Keep what it keeps with `claude-sessions config keep`, then delete it", strings.Join(copies, ", "))
	}
	// Before planning, which reads transcripts: refused every day otherwise, after reading.
	if waits, err := archive.Holdouts(dest, func(s archive.Shard) bool { return s.HonorsDeleted }); err != nil {
		return 0, fmt.Errorf("nothing deleted: %v. The computer it belongs to may upload them again", err)
	} else if len(waits) > 0 {
		return 0, fmt.Errorf("nothing deleted: %s", holdoutRefusal(waits))
	}
	// Dated by no later than a day after another computer's last push: a clock here
	// running ahead must not make young conversations look old to every computer. The
	// others being switched off holds it back too, so this is said as a fact, not a WARN.
	at := now
	if bound, machine, pushed := othersClock(dest); !bound.IsZero() && now.After(bound) {
		logf("KEEP-DAYS: counting age up to %s (%s's last backup, %s, + 25 h), not %s",
			bound.Local().Format("2006-01-02 15:04"), machine, pushed.Local().Format("2006-01-02 15:04"), now.Local().Format("2006-01-02 15:04"))
		at = bound
	}
	plan, err := planWith(root, dest, pruneSel{OlderThan: ret.KeepDays}, ret, at, &ageScan{budget: budget, from: last.Next})
	if err != nil {
		return 0, err
	}
	next, failed := plan.cut, map[string]bool{}
	if len(plan.Remove) > 0 {
		deleted, _, err = applyPrune(dest, plan, now, func(it pruneItem, err error) {
			if errors.Is(err, errOutOfTime) {
				next = it.ID
				return
			}
			failed[it.ID] = true
			logf("WARN: %s: not pruned: %v", it.ID, err)
		})
		for _, it := range plan.Remove {
			if it.ID == next {
				break // the rest were left unchecked
			}
			if deleted > 0 && !failed[it.ID] {
				logf("PRUNE: %s (%s, last message %s, %s)", it.ID, printable(it.Project), it.LastAt.Local().Format("2006-01-02"), mb(it.Size))
			}
		}
	}
	if next != last.Next {
		run.Next = next
		if err := savePruneLast(root, run); err != nil {
			logf("WARN: %s: %v", pruneLastPath(root), err)
		}
	}
	if next != "" {
		logf("KEEP-DAYS: stopped after %s; the next run carries on from %s", budget, next)
	}
	return deleted, err
}

// othersClock is 25 hours after the newest last push of any other computer, which
// computer that is, and its push: the latest the age rule dates by. Zero when no other
// shard has one, as one from a build older than lastPush has not.
func othersClock(dest string) (bound time.Time, machine string, pushed time.Time) {
	shards, _, _ := archive.ReadShards(dest)
	for name, s := range shards {
		if !isMe(name) && s.LastPush.After(pushed) { // a former name's shard has this clock's time
			machine, pushed = name, s.LastPush
		}
	}
	if pushed.IsZero() {
		return time.Time{}, "", time.Time{}
	}
	return pushed.Add(25 * time.Hour), machine, pushed
}

func undoPrune(dest string, ids []string, now time.Time) error {
	d, err := archive.LoadDeleted(dest)
	if err != nil {
		return err
	}
	var add []archive.DeletedEntry
	for _, id := range ids {
		if e, ok := d[id]; !ok || e.Undone {
			fmt.Printf("%s is not on the deleted list\n", id)
			continue
		}
		add = append(add, archive.DeletedEntry{ID: id, Buckets: d[id].Buckets, DeletedAt: after(now, d, id), Undone: true})
	}
	if len(add) == 0 {
		return nil
	}
	if err := archive.RecordDeleted(dest, machineName(), add); err != nil {
		return err
	}
	for _, e := range add {
		fmt.Printf("Off the deleted list: %s\n", e.ID)
	}
	fmt.Println("Any computer that still has it uploads it on its next push.")
	return nil
}

func printPrunePlan(plan prunePlan, yes bool) {
	if len(plan.Matched) > 0 {
		fmt.Println("--project matched:")
		buckets := make([]string, 0, len(plan.Matched))
		for b := range plan.Matched {
			buckets = append(buckets, b)
		}
		sort.Strings(buckets)
		for _, b := range buckets {
			p := plan.Matched[b]
			fmt.Printf("  %s  %s  %s\n", printable(b), printable(p.Owner()), printable(p.Path))
		}
		fmt.Println()
	}
	var total int64
	for _, it := range plan.Remove {
		date := "no date"
		if !it.LastAt.IsZero() {
			date = it.LastAt.Local().Format("2006-01-02")
		}
		prompt := []rune(printable(it.Prompt))
		if len(prompt) > 60 {
			prompt = append(prompt[:57], []rune("...")...)
		}
		fmt.Printf("  %-40s  %s  %9s  %s\n", shorten(printable(it.Project), 40), date, mb(it.Size), string(prompt))
		total += it.Size
	}
	if len(plan.Remove) == 0 {
		fmt.Println("No archived conversation to delete.")
	} else {
		verb := "Would delete"
		if yes && plan.Unread == "" && len(plan.Holdouts) == 0 {
			verb = "Deleting"
		}
		fmt.Printf("%s %d conversation(s), %s: every copy in the archive.\n", verb, len(plan.Remove), mb(total))
	}
	fmt.Printf("%d kept (on the keep list)\n", plan.Kept)
	if yes || len(plan.Remove) == 0 {
		return
	}
	switch {
	case plan.Unread != "":
		fmt.Printf("--yes would delete nothing: %s.\n", plan.Unread)
	case len(plan.Holdouts) > 0:
		fmt.Printf("--yes would delete nothing: %s\n", holdoutRefusal(plan.Holdouts))
	}
	fmt.Println("Run with --yes to delete them")
}
