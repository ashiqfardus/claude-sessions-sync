package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
)

// cmdClean deletes archived copies of a session that another copy in the archive holds
// in whole. Each machine used to archive the sessions it imported again, under its own
// folder for the project, so a conversation used on two machines was stored twice.
func cmdClean(args []string) error {
	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	archiveDir := fs.String("archive", "", "override the synced destination folder")
	yes := fs.Bool("yes", false, "delete them; without it, only list what would be deleted")
	if err := parseFlags(fs, args, false); err != nil {
		return err
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
	if *yes {
		// This machine's push must not archive a copy while it is being deleted.
		lock, err := archive.AcquireLockWait(root, time.Minute)
		if err != nil {
			return err
		}
		defer lock.Release()
	}

	plan := planClean(dest, true)
	printCleanPlan(plan, *yes)
	if !*yes || len(plan.Remove) == 0 {
		return nil
	}
	if plan.Unread != "" {
		// A machine whose manifest cannot be read may be one that uploads them again.
		return fmt.Errorf("nothing deleted: %s; run clean again once it has synced", plan.Unread)
	}

	var freed int64
	var failed int
	for _, c := range plan.Remove {
		if err := c.verify(dest); err != nil {
			fmt.Fprintf(os.Stderr, "  - %s/%s kept: %v\n", c.Bucket, c.ID, err)
			failed++
			continue
		}
		if err := c.delete(dest); err != nil {
			fmt.Fprintf(os.Stderr, "  x %s/%s: %v\n", c.Bucket, c.ID, err)
			failed++
			continue
		}
		freed += c.Size
	}
	fmt.Printf("\nDeleted %d copies, %s.\n", len(plan.Remove)-failed, mb(freed))
	fmt.Println("Run clean on one computer at a time, and only after the other has synced.")
	fmt.Println("A sync client may keep deleted files in its trash, still counting toward your storage -\n" +
		"Google Drive does, for 30 days. Empty it to get the space back now.")
	if failed > 0 {
		return fmt.Errorf("%d copies were not deleted", failed)
	}
	return nil
}

// dupCopy is one archived copy of a session that another copy, In, holds in whole.
type dupCopy struct {
	Bucket, ID, In string
	Size           int64 // the transcript, its own folder and its page
}

type cleanPlan struct {
	Remove []dupCopy
	// Waiting are copies held in whole elsewhere that are kept for now, because a
	// machine pushing to their bucket would archive them again; by machine.
	Waiting     map[string][]dupCopy
	WaitingSize int64
	Unsure      int    // copies kept because their own folder holds a file the other lacks
	Unread      string // why a machine's manifest could not be read, if one could not
}

// planClean finds, for every session archived in more than one bucket, the copies that
// another holds in whole. Copies are compared by turns, as push compares them: a copy
// filed on another machine has that machine's paths, so its bytes differ.
func planClean(dest string, progress bool) cleanPlan {
	plan := cleanPlan{Waiting: map[string][]dupCopy{}}

	// Who pushes to each bucket, and whether every one of them leaves a held copy
	// alone. A bucket no machine lists any more is pushed to by none.
	shards, bad, err := archive.ReadShards(dest)
	switch {
	case err != nil:
		plan.Unread = err.Error()
	case len(bad) > 0:
		plan.Unread = "cannot read manifest/" + strings.Join(bad, ", manifest/")
	}
	stale := map[string][]string{} // lower-cased bucket -> machines that would push it again
	listed := map[string]bool{}    // buckets some machine pushes to
	for _, s := range shards {
		for b := range s.Projects {
			listed[strings.ToLower(b)] = true
			if !s.SkipsHeld {
				stale[strings.ToLower(b)] = append(stale[strings.ToLower(b)], s.Machine)
			}
		}
	}

	holders := sessionHolders(dest)
	ids := make([]string, 0, len(holders))
	for id, bs := range holders {
		if len(bs) > 1 {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	for n, id := range ids {
		if progress && n%25 == 0 {
			fmt.Fprintf(os.Stderr, "\rcomparing copies: %d/%d", n, len(ids))
		}
		type cp struct {
			bucket, path string
			size         int64
			covers       int
		}
		var copies []cp
		for _, b := range holders[id] {
			if p := archivedCopy(dest, b, id); p != "" {
				st, _ := os.Lstat(p)
				copies = append(copies, cp{bucket: b, path: p, size: st.Size()})
			}
		}
		// rel[i][j] is how copy i relates to copy j.
		rel := make([][]claude.Relation, len(copies))
		for i := range copies {
			rel[i] = make([]claude.Relation, len(copies))
		}
		for i := range copies {
			for j := i + 1; j < len(copies); j++ {
				r := claude.Compare(copies[i].path, copies[j].path)
				rel[i][j], rel[j][i] = r, inverse(r)
			}
		}
		holds := func(i, j int) bool { return rel[i][j] == claude.Same || rel[i][j] == claude.Extends }
		for i := range copies {
			for j := range copies {
				if i != j && holds(i, j) {
					copies[i].covers++
				}
			}
		}
		// The copy that holds the most others goes first, so it is the one kept. A copy
		// is deleted only for one that is kept, never for another being deleted: no
		// order of comparisons can delete every copy of a session.
		order := make([]int, len(copies))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool {
			ca, cb := copies[order[a]], copies[order[b]]
			if ca.covers != cb.covers {
				return ca.covers > cb.covers
			}
			if ca.size != cb.size {
				return ca.size > cb.size
			}
			return ca.bucket < cb.bucket
		})
		var kept []int
		for _, i := range order {
			keeper := -1
			for _, k := range kept {
				// Never kept for one in a bucket no machine lists: a copy planted in the
				// archive must not have the real ones deleted for it.
				if holds(k, i) && listed[strings.ToLower(copies[k].bucket)] {
					keeper = k
					break
				}
			}
			c := copies[i]
			if keeper < 0 || forked(dest, c.bucket, id) || !coveredFolder(dest, c.bucket, copies[keeper].bucket, id) {
				if keeper >= 0 && !forked(dest, c.bucket, id) {
					plan.Unsure++
				}
				kept = append(kept, i)
				continue
			}
			d := dupCopy{Bucket: c.bucket, ID: id, In: copies[keeper].bucket, Size: c.size + copySize(dest, c.bucket, id)}
			if machines := stale[strings.ToLower(c.bucket)]; len(machines) > 0 {
				for _, m := range machines {
					plan.Waiting[m] = append(plan.Waiting[m], d)
				}
				plan.WaitingSize += d.Size
				kept = append(kept, i) // still there: it may keep another
				continue
			}
			plan.Remove = append(plan.Remove, d)
		}
	}
	if progress && len(ids) > 0 {
		fmt.Fprintf(os.Stderr, "\rcomparing copies: %d/%d\n", len(ids), len(ids))
	}
	return plan
}

func inverse(r claude.Relation) claude.Relation {
	switch r {
	case claude.Extends:
		return claude.Behind
	case claude.Behind:
		return claude.Extends
	}
	return r
}

// forked reports whether a fork is kept beside this copy: forks/<bucket>/<id> is filed
// with it, and would be left without it.
func forked(dest, bucket, id string) bool {
	_, err := os.Lstat(filepath.Join(dest, "forks", bucket, id))
	return err == nil
}

// coveredFolder reports whether every file in the copy's own folder - its subagents
// and saved tool output - is also in the kept copy's folder.
func coveredFolder(dest, bucket, keeper, id string) bool {
	dir := filepath.Join(archive.ProjectsDir(dest), bucket, id)
	if _, err := os.Lstat(dir); err != nil {
		return true // none
	}
	if !archive.IsRealDir(dir) {
		return false
	}
	other := filepath.Join(archive.ProjectsDir(dest), keeper, id)
	ok := true
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			ok = false
			return filepath.SkipAll
		}
		if !d.Type().IsRegular() {
			return nil // a folder is walked; a link is removed, not what it names
		}
		rel, _ := filepath.Rel(dir, p)
		if st, err := os.Lstat(filepath.Join(other, rel)); err != nil || !st.Mode().IsRegular() {
			ok = false
			return filepath.SkipAll
		}
		return nil
	})
	return ok
}

// copySize is the size of a copy's own folder and its page.
func copySize(dest, bucket, id string) int64 {
	var n int64
	dir := filepath.Join(archive.ProjectsDir(dest), bucket, id)
	if archive.IsRealDir(dir) {
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				if info, err := d.Info(); err == nil {
					n += info.Size()
				}
			}
			return nil
		})
	}
	if st, err := os.Lstat(pagePath(dest, bucket, id)); err == nil && st.Mode().IsRegular() {
		n += st.Size()
	}
	return n
}

func pagePath(dest, bucket, id string) string {
	return filepath.Join(dest, "html", bucket, id+".html")
}

// verify proves, right before the copy is deleted, that the kept copy has every turn
// of its transcript and of each subagent transcript in its folder, and every other file
// there at least as large. The plan judged by last turns and names; this reads it all.
func (c dupCopy) verify(dest string) error {
	keep, own := archivedCopy(dest, c.In, c.ID), archivedCopy(dest, c.Bucket, c.ID)
	if keep == "" || own == "" {
		return fmt.Errorf("a copy is no longer there")
	}
	if !claude.HoldsAll(keep, own) {
		return fmt.Errorf("%s lacks some of its turns", c.In)
	}
	dir := filepath.Join(archive.ProjectsDir(dest), c.Bucket, c.ID)
	if _, err := os.Lstat(dir); err != nil {
		return nil // no folder of its own
	}
	if !archive.IsRealDir(dir) {
		return fmt.Errorf("its folder is not a plain folder")
	}
	other := filepath.Join(archive.ProjectsDir(dest), c.In, c.ID)
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		kept, err := os.Lstat(filepath.Join(other, rel))
		if err != nil || !kept.Mode().IsRegular() {
			return fmt.Errorf("%s has no %s", c.In, filepath.ToSlash(rel))
		}
		if strings.EqualFold(filepath.Ext(p), ".jsonl") {
			if !claude.HoldsAll(filepath.Join(other, rel), p) {
				return fmt.Errorf("%s lacks turns of %s", c.In, filepath.ToSlash(rel))
			}
		} else if info, err := d.Info(); err != nil || kept.Size() < info.Size() {
			return fmt.Errorf("%s has a smaller %s", c.In, filepath.ToSlash(rel))
		}
		return nil
	})
}

// delete removes the copy: its own folder first, then the transcript, then its page, so
// a run stopped part way leaves a transcript that the next run deletes again.
func (c dupCopy) delete(dest string) error {
	bucketDir := filepath.Join(archive.ProjectsDir(dest), c.Bucket)
	if !archive.IsRealDir(bucketDir) {
		return fmt.Errorf("%s is not a plain folder (a link?)", bucketDir)
	}
	if dir := filepath.Join(bucketDir, c.ID); archive.IsRealDir(dir) {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	if p := archivedCopy(dest, c.Bucket, c.ID); p != "" {
		if err := os.Remove(p); err != nil {
			return err
		}
	}
	if archive.IsRealDir(filepath.Join(dest, "html")) && archive.IsRealDir(filepath.Dir(pagePath(dest, c.Bucket, c.ID))) {
		if st, err := os.Lstat(pagePath(dest, c.Bucket, c.ID)); err == nil && st.Mode().IsRegular() {
			_ = os.Remove(pagePath(dest, c.Bucket, c.ID)) // rendered again if ever needed
		}
	}
	return nil
}

func printCleanPlan(plan cleanPlan, yes bool) {
	if len(plan.Remove) == 0 && len(plan.Waiting) == 0 {
		fmt.Println("No duplicate copies: every archived session is stored once.")
		return
	}
	if len(plan.Remove) > 0 {
		var total int64
		byBucket := map[string][]dupCopy{}
		for _, c := range plan.Remove {
			total += c.Size
			byBucket[c.Bucket] = append(byBucket[c.Bucket], c)
		}
		verb := "Would delete"
		if yes {
			verb = "Deleting"
		}
		fmt.Printf("%s %d duplicate copies, %s - each session stays archived in the other folder:\n", verb, len(plan.Remove), mb(total))
		buckets := make([]string, 0, len(byBucket))
		for b := range byBucket {
			buckets = append(buckets, b)
		}
		sort.Slice(buckets, func(i, j int) bool { return sizeOf(byBucket[buckets[i]]) > sizeOf(byBucket[buckets[j]]) })
		for _, b := range buckets {
			fmt.Printf("  %-60s %4d  %10s  (kept in %s)\n", b, len(byBucket[b]), mb(sizeOf(byBucket[b])), byBucket[b][0].In)
		}
	}
	if len(plan.Waiting) > 0 {
		machines := make([]string, 0, len(plan.Waiting))
		for m := range plan.Waiting {
			machines = append(machines, m)
		}
		sort.Strings(machines)
		fmt.Printf("\nKept for now: %s of duplicate copies that these computers would upload again:\n", mb(plan.WaitingSize))
		for _, m := range machines {
			fmt.Printf("  %-60s %4d  %10s\n", m, len(plan.Waiting[m]), mb(sizeOf(plan.Waiting[m])))
		}
		fmt.Println("Update claude-sessions on each and let it push once (`claude-sessions push`), then run clean again.")
	}
	if plan.Unsure > 0 {
		fmt.Printf("\n%d copies kept: their subagent or tool-output files are not all in the other copy.\n", plan.Unsure)
	}
	if !yes && len(plan.Remove) > 0 {
		fmt.Println("\nRun `claude-sessions clean --yes` to delete them.")
	}
}

func sizeOf(cs []dupCopy) int64 {
	var n int64
	for _, c := range cs {
		n += c.Size
	}
	return n
}

func mb(n int64) string {
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}
