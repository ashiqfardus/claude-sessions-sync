package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
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
	LastUUID    string    // that turn's uuid; "" when that copy is zipped, as zips are never unpacked to date them
	Prompt      string
	Size        int64
}

type prunePlan struct {
	Remove   []pruneItem
	Kept     int                        // matched, but on the keep list
	Holdouts []string                   // machines whose build would upload them again
	Unread   string                     // why a manifest could not be read, if one could not
	Matched  map[string]archive.Project // the buckets --project named
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
	if root != "" {
		buckets, _ := claude.ListBuckets(claude.ProjectsDir(root))
		for _, b := range buckets {
			for _, t := range b.Transcripts {
				local[strings.ToLower(t.ID)] = append(local[strings.ToLower(t.ID)], t.Path)
			}
		}
	}
	rows := map[string]archive.Session{} // the newest index row of each, for its project and prompt
	all, _ := archive.AllSessions(dest)
	for _, r := range all {
		if _, ok := rows[strings.ToLower(r.ID)]; !ok {
			rows[strings.ToLower(r.ID)] = r
		}
	}
	cutoff := now.AddDate(0, 0, -sel.OlderThan)

	holders := sessionHolders(dest)
	ids := make([]string, 0, len(holders))
	for id := range holders {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		buckets := holders[id]
		if len(named) > 0 && !named[id] ||
			len(sel.Projects) > 0 && !slices.ContainsFunc(buckets, func(b string) bool { return inProject[b] }) {
			continue
		}
		it := pruneItem{ID: id, Buckets: buckets}
		dated := datePrune(dest, &it, local[id])
		if sel.OlderThan > 0 {
			// A conversation only in buckets no manifest lists has no identity a keep
			// entry could name, so age alone never selects it.
			listed := slices.ContainsFunc(buckets, func(b string) bool { _, ok := projects[b]; return ok })
			if !dated || !listed || !it.LastAt.Before(cutoff) {
				continue
			}
		}
		if kept(ret, id, buckets, projects) {
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
// or this machine's own - and LastUUID to that turn's. A zipped copy is dated by its
// transcript entry's modification time, from the zip's index alone: never before its
// last turn, so the conversation can only look younger. It reports false when any copy
// cannot be dated: the conversation may then be younger than it looks.
func datePrune(dest string, it *pruneItem, local []string) bool {
	dated, newest := true, ""
	loose := func(p string) {
		at, ok := claude.LastTurnTime(p)
		if !ok {
			dated = false
		} else if at.After(it.LastAt) {
			it.LastAt, newest = at, p
		}
	}
	for _, b := range it.Buckets {
		p, zp := archivedCopy(dest, b, it.ID), zippedCopy(dest, b, it.ID)
		if p == "" && zp == "" {
			dated = false // named in another case, it is not read as it.ID
		}
		if p != "" {
			loose(p)
		}
		if zp != "" {
			at, ok := zippedTurnTime(zp, it.ID)
			if !ok {
				dated = false
			} else if at.After(it.LastAt) {
				it.LastAt, newest = at, ""
			}
		}
		for _, p := range forkCopies(dest, b, it.ID) {
			loose(p)
		}
	}
	for _, p := range local {
		loose(p)
	}
	if newest != "" {
		it.LastUUID = claude.LastUUID(newest)
	}
	return dated && !it.LastAt.IsZero()
}

func zippedTurnTime(zp, id string) (time.Time, bool) {
	ents, err := convzip.Entries(zp)
	if err != nil {
		return time.Time{}, false
	}
	for name, e := range ents {
		if strings.EqualFold(name, id+".jsonl") {
			return e.ModTime, !e.ModTime.IsZero()
		}
	}
	return time.Time{}, false
}

func forkDir(dest, bucket, id string) string { return filepath.Join(dest, "forks", bucket, id) }

// forkCopies are the forks kept beside bucket's copy of id: forks/<bucket>/<id>/<machine>.jsonl.
func forkCopies(dest, bucket, id string) []string {
	dir := forkDir(dest, bucket, id)
	if !archive.IsRealDir(dir) {
		return nil
	}
	var out []string
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".jsonl") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

// conversationSize is every copy's transcript, zip, folder, page and forks.
func conversationSize(dest string, it pruneItem) int64 {
	var n int64
	for _, b := range it.Buckets {
		files := append(forkCopies(dest, b, it.ID), archivedCopy(dest, b, it.ID), zippedCopy(dest, b, it.ID))
		for _, p := range files {
			if st, err := os.Lstat(p); p != "" && err == nil {
				n += st.Size()
			}
		}
		n += copySize(dest, b, it.ID)
	}
	return n
}

// applyPrune deletes what plan lists. The caller holds the local lock: push's age rule
// runs it inside its own. It deletes nothing while a manifest cannot be read or a
// machine's build would upload the conversations again, and records every one on the
// deleted list before deleting any. fail is told of each one not deleted.
func applyPrune(dest string, plan prunePlan, now time.Time, fail func(pruneItem, error)) (deleted int, freed int64, err error) {
	if plan.Unread != "" {
		return 0, 0, fmt.Errorf("nothing deleted: %s. The computer it belongs to may upload them again", plan.Unread)
	}
	if len(plan.Holdouts) > 0 {
		return 0, 0, fmt.Errorf("nothing deleted: %s %s", strings.Join(plan.Holdouts, ", "), holdoutAdvice)
	}
	entries := make([]archive.DeletedEntry, len(plan.Remove))
	for i, it := range plan.Remove {
		entries[i] = archive.DeletedEntry{ID: it.ID, Buckets: it.Buckets, LastUUID: it.LastUUID, LastAt: it.LastAt, DeletedAt: now}
	}
	if err := archive.RecordDeleted(dest, machineName(), entries); err != nil {
		return 0, 0, fmt.Errorf("nothing deleted: the deleted list could not be written: %w", err)
	}
	for _, it := range plan.Remove {
		if err := deleteConversation(dest, it); err != nil {
			fail(it, err)
			continue
		}
		deleted++
		freed += it.Size
	}
	if deleted < len(plan.Remove) {
		err = fmt.Errorf("%d conversation(s) were not deleted", len(plan.Remove)-deleted)
	}
	return deleted, freed, err
}

const holdoutAdvice = "run a claude-sessions too old to honour the deleted list, and would upload the conversations again.\n" +
	"Update claude-sessions on each and let it push once, or run `claude-sessions machines forget <name>` for one that is gone."

// deleteConversation removes the conversation from every bucket in it.Buckets: its
// folder, its forks, its page, then its transcript and zip, so a run stopped part way
// leaves it listed for the next run to delete again. Every path is checked first, and
// nothing is deleted when a link or a stray file stands on the way to one.
func deleteConversation(dest string, it pruneItem) error {
	if !claude.IsSessionID(it.ID) {
		return fmt.Errorf("%q is not a conversation id", it.ID)
	}
	var dirs, files []string
	for _, b := range it.Buckets {
		if !archive.ValidName(b) {
			return fmt.Errorf("%q is not a bucket name", b)
		}
		bucketDir := filepath.Join(archive.ProjectsDir(dest), b)
		bd := []string{filepath.Join(bucketDir, it.ID), forkDir(dest, b, it.ID)}
		bf := []string{pagePath(dest, b, it.ID), filepath.Join(bucketDir, it.ID+".jsonl"), convzip.Path(bucketDir, it.ID)}
		for _, p := range append(slices.Clone(bd), bf...) {
			if !archive.ThroughRealDirs(dest, p) {
				return fmt.Errorf("a link stands on the way to %s; nothing deleted", p)
			}
		}
		for _, d := range bd {
			if _, err := os.Lstat(d); err == nil && !archive.IsRealDir(d) {
				return fmt.Errorf("%s is not a plain folder (a link?); nothing deleted", d)
			}
		}
		for _, f := range bf {
			if st, err := os.Lstat(f); err == nil && !st.Mode().IsRegular() {
				return fmt.Errorf("%s is not a plain file; nothing deleted", f)
			}
		}
		dirs, files = append(dirs, bd...), append(files, bf...)
	}
	for _, d := range dirs {
		if err := os.RemoveAll(d); err != nil { // does not follow links inside
			return err
		}
	}
	for _, f := range files {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// withoutPruned leaves out index rows of conversations on the deleted list. A list that
// cannot be read leaves out nothing: the rows only list what is there.
func withoutPruned(dest string, rows []archive.Session) []archive.Session {
	d, err := archive.LoadDeleted(dest)
	if err != nil || len(d) == 0 {
		return rows
	}
	return slices.DeleteFunc(rows, func(r archive.Session) bool {
		e, ok := d[strings.ToLower(r.ID)]
		return ok && !e.Undone
	})
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
		add = append(add, archive.DeletedEntry{ID: id, Buckets: d[id].Buckets, DeletedAt: now, Undone: true})
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
		fmt.Printf("--yes would delete nothing: %s %s\n", strings.Join(plan.Holdouts, ", "), holdoutAdvice)
	}
	fmt.Println("Run with --yes to delete them")
}
