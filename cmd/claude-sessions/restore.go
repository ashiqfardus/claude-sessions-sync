package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/convzip"
	"github.com/ashiqfardus/claude-sessions-sync/internal/rewrite"
	"github.com/ashiqfardus/claude-sessions-sync/internal/work"
)

type restoreResult struct {
	Bucket        string         `json:"bucket"`
	Copied        int            `json:"copied"`
	Updated       int            `json:"updated"` // already here, and the archived copy continued it
	Skipped       int            `json:"skipped"`
	Memory        int            `json:"memory"`
	Hits          map[string]int `json:"hits"`
	BareLeftovers int            `json:"bareLeftovers"`
	// Failed names transcripts that could not be filed. One bad transcript no longer
	// abandons the rest of the project.
	Failed []string `json:"failed,omitempty"`
	// Diverged describes sessions continued both here and elsewhere: the local copy is
	// left alone and the other is filed as a session of its own.
	Diverged []string `json:"diverged,omitempty"`
	// Notes are said about conversations that were filed, but not in whole.
	Notes []string `json:"notes,omitempty"`
}

type restoreOpts struct {
	BareName bool // also replace the bare folder name
	DryRun   bool
	Update   bool // refresh sessions and memory already here when the archive has more

	// Owner is the machine that last pushed the source bucket, and OwnerOf, when set,
	// the machine that wrote one archived session in it - on a shared layout not the
	// same. Forks is the archive's forks/<bucket> folder. Only import and pull know these.
	Owner   string
	OwnerOf func(bucket, id string, info os.FileInfo) string
	Forks   string

	// FormerNames maps a machine, lower-cased, to the names it went by before: its
	// forks filed under an old name are kept current, not filed again.
	FormerNames map[string][]string

	// Bucket files into this local bucket by name, instead of the one projectPath
	// slugs to: pull's same-layout case.
	Bucket string

	// Archive is the archive's root, where each machine's own memory copy lives; ""
	// for a restore from a folder outside an archive.
	Archive string

	// Imported is the record of transcripts filed from elsewhere, noted as each one
	// is filed; nil records nothing.
	Imported *importRecord
}

const updateHelp = "also refresh sessions already here that continued on another machine, and memory files that are newer in the archive"

func cmdRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	source := fs.String("source", "", "folder of .jsonl transcripts to file (required)")
	projectPath := fs.String("project-path", "", "the project's path on THIS machine (required)")
	rewriteFrom := fs.String("rewrite-from", "", "the project path recorded inside the transcripts")
	bareName := fs.Bool("include-bare-name", false, "also replace the bare folder name (hits package names and prose too)")
	dryRun := fs.Bool("dry-run", false, "report what would happen without writing")
	update := fs.Bool("update", false, updateHelp)
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := parseFlags(fs, args, false); err != nil {
		return err
	}
	o := restoreCmdOpts{ClaudeDir: *claudeDir, Source: *source, ProjectPath: *projectPath, RewriteFrom: *rewriteFrom,
		BareName: *bareName, DryRun: *dryRun, Update: *update}
	s := stdStreams()
	if *asJSON {
		s = jsonStreams()
	}
	res, err := runRestore(s, o)
	if err != nil || !*asJSON {
		return err
	}
	return encodeJSON(os.Stdout, res)
}

type restoreCmdOpts struct {
	ClaudeDir, Source, ProjectPath, RewriteFrom string
	BareName, DryRun, Update                    bool
}

// runRestore files a folder of transcripts as one project's, reports on s and returns
// what it did.
func runRestore(s streams, o restoreCmdOpts) (restoreResult, error) {
	if o.Source == "" || o.ProjectPath == "" {
		return restoreResult{}, fmt.Errorf("usage: claude-sessions restore --source <folder> --project-path <path> [--rewrite-from <old path>]")
	}
	if o.RewriteFrom != "" && !rewrite.AbsoluteAnywhere(o.RewriteFrom) {
		return restoreResult{}, fmt.Errorf("--rewrite-from must be the absolute project path recorded in the transcripts, got %q", o.RewriteFrom)
	}

	root, _, err := resolveRoot(o.ClaudeDir)
	if err != nil {
		return restoreResult{}, err
	}

	lock, err := archive.AcquireLock(root)
	if err != nil {
		return restoreResult{}, err
	}
	defer lock.Release()

	// Recorded as import records, archive or not: push reads folders only from lines
	// added here.
	imported := loadImported(root, !o.DryRun)
	res, err := restoreInto(root, o.Source, o.ProjectPath, o.RewriteFrom, nil,
		restoreOpts{BareName: o.BareName, DryRun: o.DryRun, Update: o.Update, Imported: imported})
	imported.finish(s.Err)
	if err != nil {
		return restoreResult{}, err
	}

	fmt.Fprintf(s.Out, "\n%s %d transcript(s) and %d memory file(s), skipped %d already present -> %s\n",
		verbFor(o.DryRun), res.Copied, res.Memory, res.Skipped, res.Bucket)
	if res.Updated > 0 {
		fmt.Fprintf(s.Out, "updated %d session(s) that continued on another machine\n", res.Updated)
	}
	for _, f := range res.Failed {
		fmt.Fprintf(s.Out, "  x %s\n", f)
	}
	for _, n := range res.Notes {
		fmt.Fprintf(s.Out, "  ! %s\n", n)
	}
	printDiverged(s.Out, res.Diverged)

	if len(res.Hits) > 0 {
		fmt.Fprintln(s.Out, "\nLines rewritten, by form:")
		forms := make([]string, 0, len(res.Hits))
		for k := range res.Hits {
			forms = append(forms, k)
		}
		sort.Strings(forms)
		for _, f := range forms {
			fmt.Fprintf(s.Out, "  %-14s %d\n", f, res.Hits[f])
		}
	}
	if res.BareLeftovers > 0 {
		fmt.Fprintf(s.Out, "\n%d line(s) still mention the old folder name without a path prefix\n", res.BareLeftovers)
		fmt.Fprintln(s.Out, "(package names, git remotes, prose). Re-run with --include-bare-name to replace those too.")
	}
	if !o.DryRun && res.Copied > 0 {
		fmt.Fprintf(s.Out, "\nNow: cd %s  then  claude --resume\n", shellPath(strings.TrimRight(o.ProjectPath, `\/`)))
	}
	return res, nil
}

// restoreInto files transcripts from source into the bucket for projectPath.
// extra are other repositories that moved - the siblings a multi-folder workspace
// session also worked in - repointed alongside the project itself.
func restoreInto(root, source, projectPath, rewriteFrom string, extra []rewrite.Move, opts restoreOpts) (restoreResult, error) {
	res := restoreResult{Hits: map[string]int{}}

	entries, err := os.ReadDir(source)
	if err != nil {
		return res, fmt.Errorf("source not readable: %w", err)
	}
	// Each conversation once, by id: its loose transcript, its zip, or both.
	var convs []*archivedConv
	byID := map[string]*archivedConv{} // lower-cased id
	for _, e := range entries {
		// Regular files only: a symlink in the archive would be read through, filing
		// whatever it points at as a transcript.
		if e.Type().IsRegular() && isTranscriptName(e.Name()) {
			c := &archivedConv{source: source, name: e.Name(), loose: filepath.Join(source, e.Name())}
			byID[strings.ToLower(c.id())] = c
			convs = append(convs, c)
		}
	}
	zips := convzip.Find(source)
	zipIDs := make([]string, 0, len(zips))
	for id := range zips {
		zipIDs = append(zipIDs, id)
	}
	sort.Strings(zipIDs)
	for _, id := range zipIDs {
		c := byID[id]
		if c == nil {
			c = &archivedConv{source: source, name: strings.TrimSuffix(filepath.Base(zips[id]), convzip.Suffix) + ".jsonl"}
			byID[id] = c
			convs = append(convs, c)
		}
		c.zip = zips[id]
	}
	if len(convs) == 0 {
		return res, fmt.Errorf("no .jsonl transcripts in %s (memory .md files are not sessions - check the folder)", source)
	}
	defer func() {
		for _, c := range convs {
			c.close()
		}
	}()

	projectsDir := claude.ProjectsDir(root)
	newPath, bucketName := "", opts.Bucket
	if bucketName == "" {
		// Relative input ("." or "../app") used to become a bucket named "." or "",
		// filing transcripts straight into the projects folder.
		abs, err := filepath.Abs(projectPath)
		if err != nil {
			return res, err
		}
		newPath = filepath.Clean(abs)

		// Prefer a bucket that already exists, matched case-insensitively: the casing
		// of a slug follows the string Claude recorded, not what is on disk, so
		// computing a fresh one can create a second bucket --resume never looks in.
		bucketName = claude.Slug(newPath)
		if existing, ok := claude.FindBucket(projectsDir, bucketName); ok {
			bucketName = existing.Name
		}
	}
	res.Bucket = filepath.Join(projectsDir, bucketName)

	moves := extra
	bareLeaf := ""
	if rewrite.AbsoluteAnywhere(rewriteFrom) {
		moves = append([]rewrite.Move{{From: rewriteFrom, To: newPath}}, extra...)
		bareLeaf = rewrite.LastSegment(strings.TrimRight(rewriteFrom, `\/`))
		if strings.EqualFold(bareLeaf, rewrite.LastSegment(newPath)) {
			bareLeaf = "" // unchanged name: nothing to warn about
		}
	}
	// One list, most specific path first, so a parent folder never claims the paths of
	// a repository inside it that moved somewhere else.
	rules := rewrite.RulesForMoves(moves)
	if opts.BareName && rewrite.AbsoluteAnywhere(rewriteFrom) {
		for _, r := range rewrite.Rules(rewriteFrom, newPath, true) {
			if r.Label == "bare-name" {
				rules = append(rules, r)
			}
		}
	}

	// Another machine files this machine's copy of a forked session as a fork, and
	// pushes it back. Unless it was continued there, that is this machine's own
	// conversation, and it is not filed again.
	echoes := map[string]string{} // fork file name -> the local session it copies
	if locals, err := os.ReadDir(res.Bucket); err == nil {
		// A copy filed before its writer was known carries the tag with no machine; it
		// may be this machine's too. Only a copy that adds nothing is skipped, so
		// counting it is safe.
		var tags []string
		for _, n := range append([]string{machineName(), ""}, savedFormer()...) {
			tags = append(tags, forkTag(filepath.Base(res.Bucket), n))
		}
		for _, e := range locals {
			if id, ok := strings.CutSuffix(e.Name(), ".jsonl"); ok {
				for _, tag := range tags {
					for level := 0; level < maxForkLevels; level++ {
						echoes[claude.ForkID(id, forkLevel(tag, level))+".jsonl"] = filepath.Join(res.Bucket, e.Name())
					}
				}
			}
		}
	}

	// What may land here - each transcript not here yet, and each subagent transcript -
	// is journaled first, in one fsync'd write: a run killed at any point leaves each of
	// them counted as filed in whole. Each session's files are listed once, here.
	var landing []string
	for _, c := range convs {
		if dst := filepath.Join(res.Bucket, c.name); archive.StatOrNil(dst) == nil {
			landing = append(landing, dst)
		}
		c.files = c.folderFiles()
		for _, rel := range c.files {
			if dst := filepath.Join(res.Bucket, c.id(), rel); archive.StatOrNil(dst) == nil {
				landing = append(landing, dst)
			}
		}
	}
	if !opts.DryRun {
		if err := opts.Imported.pend(landing...); err != nil {
			return res, err
		}
	}

	filed := loadFiledForks(filedForksPath(root, res.Bucket))
	for i, c := range convs {
		if i > 0 {
			convs[i-1].close() // its extract goes as soon as it is filed
		}
		name, id := c.name, c.id()
		dst := filepath.Join(res.Bucket, name)
		exists := archive.StatOrNil(dst) != nil
		// A zipped conversation is read only when it is compared or filed. One that
		// cannot be read is said once, and nothing of it is filed.
		read := func() (string, bool) {
			src, err := c.open(&res)
			if err != nil {
				res.Failed = append(res.Failed, fmt.Sprintf("%s could not be read, not filed: %v", filepath.Base(c.zip), err))
			}
			return src, err == nil
		}
		if orig, echo := echoes[strings.ToLower(name)]; echo && !exists {
			src, ok := read()
			if !ok {
				continue
			}
			if rel := claude.Compare(src, orig); rel == claude.Same || rel == claude.Behind {
				res.Skipped++
				continue
			}
		}
		// A fork filed here and deleted since is in this machine's own archived bucket
		// once a push has run. It is not filed again unless turns were added to it.
		if f, was := filed[strings.ToLower(id)]; was && !exists {
			src, ok := read()
			if !ok {
				continue
			}
			if claude.LastUUID(src) == f.Last {
				res.Skipped++
				continue
			}
		}
		if !opts.DryRun && anyMissing(res.Bucket, id, c.files) {
			src, ok := read()
			if !ok {
				continue
			}
			failed, err := restoreSessionFiles(sessionDir(src), res.Bucket, id, c.files, rules, bareLeaf, false, opts.Imported)
			if err != nil {
				return res, err
			}
			res.Failed = append(res.Failed, failed...)
		}
		if exists && !opts.Update {
			res.Skipped++
			continue
		}
		if opts.DryRun {
			if exists {
				res.Skipped++ // whether it grew is only known by writing it
			} else {
				res.Copied++
			}
			continue
		}

		src, ok := read()
		if !ok {
			continue
		}
		if !exists {
			out, err := fileTranscript(src, dst, rules, bareLeaf)
			if lerr := opts.Imported.settle(dst, true); lerr != nil {
				return res, lerr
			}
			if err != nil {
				res.Failed = append(res.Failed, fmt.Sprintf("%s: %v", name, err))
				continue
			}
			res.add(out)
			res.Copied++
			continue
		}

		switch out, state, err := landUpdate(opts.Imported, src, dst, rules, bareLeaf); {
		case errors.Is(err, errRecord):
			return res, err
		case err != nil:
			res.Failed = append(res.Failed, fmt.Sprintf("%s: %v", name, err))
		case state == extended:
			res.add(out)
			res.Updated++
		case state == diverged:
			// Both copies have turns the other lacks. Keep the local one as it is and
			// file the archived one beside it, so either can be resumed.
			owner := opts.Owner
			if info, err := os.Stat(src); err == nil && opts.OwnerOf != nil {
				if o := opts.OwnerOf(filepath.Base(source), id, info); o != "" {
					owner = o // who wrote this very copy, not who last pushed the project
				}
			}
			// This machine's tag names its own copies, and this one differs from what
			// is here: it is someone else's, even when who is not known.
			if isMe(owner) {
				owner = ""
			}
			folder := func() (string, error) { return sessionDir(src), nil }
			fid, outcome, err := fileFork(root, src, folder, res.Bucket, id,
				forkTag(filepath.Base(source), owner), rules, bareLeaf, opts, &res)
			if errors.Is(err, errRecord) {
				return res, err
			}
			if err != nil {
				res.Failed = append(res.Failed, fmt.Sprintf("%s: fork: %v", name, err))
				continue
			}
			if outcome == forkUnchanged {
				res.Skipped++
			}
			res.countFork(outcome, fmt.Sprintf("%s: the other machine's copy is session %s", id, fid))
		default:
			res.Skipped++
		}
	}

	// On a shared layout push keeps a forked copy under forks/<bucket>/<id>/<machine>,
	// since both machines write the one archived file. Every other machine's is filed
	// here as a session of its own; this machine's own is what it already has.
	if opts.Forks != "" && archive.IsRealDir(opts.Forks) {
		ids, _ := os.ReadDir(opts.Forks)
		for _, d := range ids {
			if !d.IsDir() || !claude.IsSessionID(d.Name()) {
				continue
			}
			copies, _ := os.ReadDir(filepath.Join(opts.Forks, d.Name()))
			for _, c := range copies {
				machine, ok := strings.CutSuffix(c.Name(), ".jsonl")
				if !ok || !c.Type().IsRegular() || isMe(machine) {
					continue
				}
				folder := func() (string, error) { return filepath.Join(source, d.Name()), nil }
				orig := byID[strings.ToLower(d.Name())]
				if orig != nil {
					folder = func() (string, error) { // the original's, from its zip when zipped
						src, err := orig.open(&res)
						if err != nil {
							return "", fmt.Errorf("%s could not be read: %w", filepath.Base(orig.zip), err)
						}
						return sessionDir(src), nil
					}
				}
				fid, outcome, err := fileFork(root, filepath.Join(opts.Forks, d.Name(), c.Name()), folder,
					res.Bucket, d.Name(), forkTag(filepath.Base(source), machine), rules, bareLeaf, opts, &res)
				if orig != nil {
					orig.close()
				}
				if errors.Is(err, errRecord) {
					return res, err
				}
				if err != nil {
					res.Failed = append(res.Failed, fmt.Sprintf("%s: fork from %s: %v", d.Name(), printable(machine), err))
					continue
				}
				res.countFork(outcome, fmt.Sprintf("%s: %s's copy is session %s", d.Name(), printable(machine), fid))
			}
		}
	}

	// Memory travels with the sessions. push archives it, so a restore that left it
	// behind would put the transcripts on the new machine and strand everything the
	// project had remembered - which is exactly what happened before this was added.
	//
	// Memory is markdown, not JSONL, and is never path-rewritten: it is prose, and
	// substituting paths inside it would corrupt meaning rather than fix references.
	res.Memory = restoreProjectMemory(root, opts.Archive, filepath.Base(source), filepath.Join(source, "memory"),
		filepath.Join(res.Bucket, "memory"), opts.Owner, opts.DryRun, opts.Update)

	if len(res.Failed) > 0 && res.Copied == 0 && res.Skipped == 0 && res.Updated == 0 {
		return res, fmt.Errorf("no transcript could be filed: %s", res.Failed[0])
	}
	return res, nil
}

// isTranscriptName reports whether a file at the top of a bucket is named as a
// transcript, by its id. Anything else - "<id> (conflicted copy).jsonl", a sync
// client's - would be filed as a session of its own and pushed back.
func isTranscriptName(name string) bool {
	ext := filepath.Ext(name)
	return strings.EqualFold(ext, ".jsonl") && claude.IsTranscriptID(strings.TrimSuffix(name, ext))
}

// sessionDir is a transcript's own folder, beside it: its subagent transcripts and
// saved tool output.
func sessionDir(transcript string) string {
	return strings.TrimSuffix(transcript, filepath.Ext(transcript))
}

// archivedConv is one archived conversation of a bucket: its loose transcript, its zip,
// or both. A zip is read only when the conversation is compared or filed: on a drive
// that streams files on demand, reading one downloads it.
type archivedConv struct {
	source, name string   // the bucket, and the transcript's file name as filed
	loose, zip   string   // either may be ""
	files        []string // its folder's files, journaled before any lands
	path         string   // the transcript as read, while open
	done         func()
	err          error
	said         bool // the note on a loose copy that diverged from the zip
}

func (c *archivedConv) id() string { return strings.TrimSuffix(c.name, filepath.Ext(c.name)) }

// folderFiles lists the conversation's folder: the loose one's files and a zip's entries
// under it, read from the zip's index alone. An index that cannot be read lists none;
// the zip is then read, and the reason said, only if the conversation is filed.
func (c *archivedConv) folderFiles() []string {
	files := claude.SessionFiles(filepath.Join(c.source, c.id()))
	if c.zip == "" {
		return files
	}
	seen := map[string]bool{}
	for _, f := range files {
		seen[f] = true
	}
	ents, _ := convzip.Entries(c.zip)
	for name := range ents {
		rel, ok := strings.CutPrefix(name, c.id()+"/")
		rel = filepath.FromSlash(rel)
		if ok && !strings.HasSuffix(name, "/") && filepath.IsLocal(rel) && !seen[rel] {
			seen[rel] = true
			files = append(files, rel)
		}
	}
	sort.Strings(files)
	return files
}

// open is the transcript to file, its folder beside it. A zipped one is extracted to
// the OS temp folder, loose files beside the zip laid over it, until close. When the zip
// cannot be read, a loose copy beside it is filed alone, with a note; with none, the
// error is kept and returned again.
func (c *archivedConv) open(res *restoreResult) (string, error) {
	switch {
	case c.zip == "":
		return c.loose, nil
	case c.path != "" || c.err != nil:
		return c.path, c.err
	}
	p, done, err := materialize(c.source, c.id(), "")
	if err == nil {
		c.done = done
		if st, serr := os.Lstat(p); serr != nil || !st.Mode().IsRegular() || !isTranscriptName(filepath.Base(p)) {
			err = errors.New("its transcript is not a regular file named by its id")
		}
	}
	if err != nil {
		c.close()
		if c.loose == "" {
			c.err = err
			return "", err
		}
		res.Notes = append(res.Notes, fmt.Sprintf("%s could not be read (%v): the loose copy beside it is filed, without any folder file only the zip holds",
			filepath.Base(c.zip), err))
		c.zip = "" // the loose copy from now on
		return c.loose, nil
	}
	if c.loose != "" && !c.said && zipHasMore(c.zip, c.id(), p) {
		c.said = true
		res.Notes = append(res.Notes, fmt.Sprintf("%s: the loose copy beside its zip is filed; the zipped copy's other turns stay in the archive", c.id()))
	}
	c.path = p
	return p, nil
}

// close removes the conversation's extract; open makes it again.
func (c *archivedConv) close() {
	if c.done != nil {
		c.done()
	}
	c.done, c.path = nil, ""
}

// zipHasMore reports whether the zipped transcript of id has a turn the transcript at p
// lacks: Materialize lays a loose copy that diverged from it over it. It reads that
// one entry.
func zipHasMore(zp, id, p string) bool {
	dir, err := os.MkdirTemp("", "claude-sessions-zipped-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir)
	skip := func(n string, _ int64, _ time.Time) bool { return !strings.EqualFold(n, id+".jsonl") }
	out, _, err := work.ExtractPackLimit(zp, func() (string, error) { return dir, nil }, skip, work.MaxFolderBytes)
	return err == nil && len(out) == 1 && !claude.HoldsAll(p, out[0].Path)
}

// anyMissing reports whether a file of a session's folder is not in the local bucket.
func anyMissing(bucket, id string, rels []string) bool {
	for _, rel := range rels {
		if archive.StatOrNil(filepath.Join(bucket, id, rel)) == nil {
			return true
		}
	}
	return false
}

func (r *restoreResult) add(out rewrite.Result) {
	for k, v := range out.Hits {
		r.Hits[k] += v
	}
	r.BareLeftovers += out.BareLeftovers
}

// fileTranscript writes src to dst, repointing paths when there are rules.
func fileTranscript(src, dst string, rules []rewrite.Rule, bareLeaf string) (rewrite.Result, error) {
	if len(rules) == 0 {
		return rewrite.Result{Hits: map[string]int{}}, archive.CopyFile(src, dst)
	}
	out, err := rewrite.File(src, dst, rules, bareLeaf)
	if err != nil {
		return out, err
	}
	for _, v := range out.Hits {
		if v > 0 {
			olderThanArchive(src, dst)
			break
		}
	}
	return out, nil
}

type updateState int

const (
	unchanged updateState = iota
	extended              // the archived copy continues the local one: replaced
	diverged              // both sides have turns the other lacks: left alone
)

// updateTranscript replaces dst with a fresh copy of src only when the copy strictly
// extends it - the same session, continued on another machine. A copy that is behind
// the local one, or the same conversation in other bytes, is left alone; one where
// both sides have turns the other lacks is reported as diverged. The two are compared
// by entry, not by bytes: the local copy may itself have been rewritten by an import.
//
// replacing, when not nil, runs just before dst is replaced; its error stops it.
func updateTranscript(src, dst string, rules []rewrite.Rule, bareLeaf string, replacing func() error) (rewrite.Result, updateState, error) {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-update-*")
	if err != nil {
		return rewrite.Result{}, unchanged, err
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName) // already gone when it was renamed into place
	replace := func() error {
		if replacing != nil {
			if err := replacing(); err != nil {
				return err
			}
		}
		return os.Rename(tmpName, dst)
	}

	out, err := fileTranscript(src, tmpName, rules, bareLeaf)
	if err != nil {
		return out, unchanged, err
	}
	switch claude.Compare(tmpName, dst) {
	case claude.Same, claude.Behind:
		return out, unchanged, nil
	case claude.Forked:
		return out, diverged, nil
	case claude.Extends:
		return out, extended, replace()
	}

	// No entry ids to go by: a byte prefix is the only evidence of a continuation.
	fresh, err := os.ReadFile(tmpName)
	if err != nil {
		return out, unchanged, err
	}
	current, err := os.ReadFile(dst)
	if err != nil {
		return out, unchanged, err
	}
	switch {
	case bytes.Equal(fresh, current):
		return out, unchanged, nil
	case len(fresh) < len(current) || !bytes.HasPrefix(fresh, current):
		return out, diverged, nil
	}
	return out, extended, replace()
}

// landUpdate is updateTranscript journaled: dst is pended just before it is replaced,
// and settled after, at its new size only if it was replaced.
func landUpdate(r *importRecord, src, dst string, rules []rewrite.Rule, bareLeaf string) (rewrite.Result, updateState, error) {
	pended := false
	out, state, err := updateTranscript(src, dst, rules, bareLeaf, func() error {
		if err := r.pend(dst); err != nil {
			return err
		}
		pended = true
		return nil
	})
	if pended {
		if lerr := r.settle(dst, state == extended && err == nil); lerr != nil {
			return out, state, lerr
		}
	}
	return out, state, err
}

// forkTag names where a forked copy came from - the archived bucket and the machine
// whose copy it is - for ForkID. The machine that owns a bucket computes the same tag
// for its own sessions, which is how it recognises one coming back.
func forkTag(bucket, machine string) string { return strings.ToLower(bucket + "@" + machine) }

// fileFork files src - another machine's copy of session id, which was continued here
// as well - as a session of its own, with its session id replaced throughout. Its id
// is derived from the original and the tag, so filing it again updates it in place,
// and only with Update. sessionDir is the archived folder of subagent transcripts and
// saved tool output that goes with it; a file of it that cannot be filed is added to
// res.Failed, as restoreSessionFiles reports one of the session's own.
//
// The fork copy can itself be continued here while the other machine continues the
// original. Then it no longer contains the other side, and the other side is filed
// as the next level of fork rather than dropped. folder is read only once the fork is
// filed or updated.
func fileFork(root, src string, folder func() (string, error), bucketDir, id, tag string, rules []rewrite.Rule, bareLeaf string, opts restoreOpts, res *restoreResult) (string, forkOutcome, error) {
	info, err := os.Stat(src)
	if err != nil {
		return "", forkUnchanged, err
	}
	recPath := filedForksPath(root, bucketDir)
	filed := loadFiledForks(recPath)
	remember := func(fid, dst string) {
		if !opts.DryRun {
			filed[fid] = filedFork{From: info.Size(), Last: claude.LastUUID(dst)}
			saveFiledForks(recPath, filed)
		}
	}
	// A copy filed before its writer was known has the tag with no machine, and one
	// from a machine since renamed has its old name. That same copy is kept current
	// rather than filed a second time.
	known := func(t string) bool {
		fid := claude.ForkID(id, t)
		_, was := filed[fid]
		return was || archive.StatOrNil(filepath.Join(bucketDir, fid+".jsonl")) != nil
	}
	// Split at the first "@": a bucket's slug has none, a name set with set-machine may.
	if at := strings.Index(tag, "@"); at >= 0 && !known(tag) {
		for _, alt := range append([]string{""}, opts.FormerNames[tag[at+1:]]...) {
			if t := forkTag(tag[:at], alt); t != tag && known(t) {
				tag = t
				break
			}
		}
	}

	for level := 0; level < maxForkLevels; level++ {
		fid := claude.ForkID(id, forkLevel(tag, level))
		dst := filepath.Join(bucketDir, fid+".jsonl")
		forkRules := append(append([]rewrite.Rule{}, rules...), rewrite.Literal("session-id", id, fid))
		exists := archive.StatOrNil(dst) != nil
		// Filed before and deleted here since: the user kept the other copy. It comes
		// back only once the other machine has gone on with it.
		if f, was := filed[fid]; was && !exists && info.Size() <= f.From {
			return fid, forkUnchanged, nil
		}
		outcome := forkNew
		switch {
		case opts.DryRun && !exists:
			return fid, forkNew, nil
		case opts.DryRun || (exists && !opts.Update):
			return fid, forkUnchanged, nil
		case !exists:
			if err := opts.Imported.land(dst, func() error { _, err := fileTranscript(src, dst, forkRules, bareLeaf); return err }); err != nil {
				return "", forkUnchanged, err
			}
		default:
			_, state, err := landUpdate(opts.Imported, src, dst, forkRules, bareLeaf)
			if err != nil {
				return "", forkUnchanged, err
			}
			switch state {
			case diverged:
				continue // this copy was continued here too
			case extended:
				outcome = forkUpdated
			default:
				outcome = forkUnchanged
			}
		}
		sessionDir, err := folder()
		var rels []string
		if err == nil {
			rels = claude.SessionFiles(sessionDir)
		} else {
			res.Failed = append(res.Failed, fmt.Sprintf("%s: filed without its folder files: %v", fid, err))
		}
		for _, rel := range rels {
			if to := filepath.Join(bucketDir, fid, rel); archive.StatOrNil(to) == nil {
				err := opts.Imported.land(to, func() error { return fileSessionFile(filepath.Join(sessionDir, rel), to, rel, forkRules, bareLeaf) })
				if errors.Is(err, errRecord) {
					return "", forkUnchanged, err
				}
				if err != nil {
					res.Failed = append(res.Failed, fmt.Sprintf("%s/%s: %v", fid, filepath.ToSlash(rel), err))
				}
			}
		}
		if outcome != forkUnchanged {
			remember(fid, dst)
		}
		return fid, outcome, nil
	}
	return "", forkUnchanged, fmt.Errorf("continued on both machines %d times over; file it by hand with `restore`", maxForkLevels)
}

// filedForksPath records the forks this machine filed into one local bucket. Kept
// beside the memory base, out of Claude Code's projects folder.
func filedForksPath(root, bucketDir string) string {
	return filepath.Join(root, "session-sync-base", filepath.Base(bucketDir), "forks.json")
}

// filedFork is one fork filed here: the size of the other machine's copy it came
// from, and the last entry of the copy filed here.
type filedFork struct {
	From int64  `json:"from"`
	Last string `json:"last"`
}

func loadFiledForks(path string) map[string]filedFork {
	m := map[string]filedFork{}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &m)
	}
	return m
}

func saveFiledForks(path string, m map[string]filedFork) {
	if data, err := json.MarshalIndent(m, "", "  "); err == nil {
		_ = archive.WriteFileAtomic(path, append(data, '\n'), 0o644)
	}
}

// forkOutcome is what filing a fork did, so a round with nothing new says nothing.
type forkOutcome int

const (
	forkUnchanged forkOutcome = iota
	forkNew
	forkUpdated
)

// countFork adds what filing a fork did to the result, with a note only when it did
// something.
func (r *restoreResult) countFork(outcome forkOutcome, note string) {
	switch outcome {
	case forkNew:
		r.Copied++
	case forkUpdated:
		r.Updated++
	default:
		return
	}
	r.Diverged = append(r.Diverged, note)
}

// maxForkLevels bounds how many times one session can fork again before import stops
// and says so, rather than filing copies without end.
const maxForkLevels = 8

// forkLevel is the tag for the level-th fork of the same copy; level 0 is the tag.
func forkLevel(tag string, level int) string {
	if level == 0 {
		return tag
	}
	return fmt.Sprintf("%s#%d", tag, level)
}

// restoreProjectMemory files one archived project's memory into dstDir.
//
// Every machine keeps its own copy in the archive (memory/<machine>/<bucket>/), which
// no other machine writes; each other machine's is merged on its own, against what was
// last taken from that machine. The bucket's shared memory folder is written by every
// machine on a shared layout - an import could not tell another machine's index from
// its own last push - so it is used only for a writer with no copy of its own, an
// older version. This machine's own copy restores a machine with no memory here at
// all, and is otherwise left alone.
func restoreProjectMemory(root, dest, bucket, sharedDir, dstDir, owner string, dryRun, update bool) int {
	var mine, others []string
	if dest != "" {
		machines, _ := os.ReadDir(filepath.Join(dest, "memory"))
		for _, m := range machines {
			if !m.IsDir() || !archive.ValidName(m.Name()) || !archive.IsRealDir(machineMemoryDir(dest, m.Name(), bucket)) {
				continue
			}
			if isMe(m.Name()) {
				mine = append(mine, m.Name())
			} else {
				others = append(others, m.Name())
			}
		}
	}
	n := 0
	covered := map[string]bool{}
	for _, m := range append(mine, others...) { // its own first: a restore onto an empty machine
		covered[strings.ToLower(m)] = true
		n += restoreMemory(root, machineMemoryDir(dest, m, bucket), dstDir, m+"@"+bucket, dryRun, update, isMe(m))
	}
	if !covered[strings.ToLower(owner)] {
		n += restoreMemory(root, sharedDir, dstDir, bucket, dryRun, update, isMe(owner))
	}
	if !dryRun {
		noteHeldMemory(root, filepath.Base(filepath.Dir(dstDir)), dstDir)
	}
	return n
}

// restoreMemory files a project's memory from the archive.
//
// Memory is loaded into every session as instructions, and the archive is written by
// other machines and anyone the folder is shared with. So a file that already exists
// here is never replaced unless the user asks with update - replacing used to happen
// whenever the archived copy was newer, and a file dated in the future stayed newer
// than any local edit forever. Even then, a future-dated file is refused.
func restoreMemory(root, srcDir, dstDir, source string, dryRun, update, own bool) int {
	// A symlink planted in the archive in place of the folder would otherwise copy
	// whatever it points at into this machine's memory, and the next push back out.
	if !archive.IsRealDir(srcDir) {
		return 0
	}
	// This machine's own archived memory is a backup of itself, not another side:
	// taken back, a copy pushed before this machine's latest edits undid them, and a
	// file deleted here returned, since the archive never deletes. Only a machine with
	// no memory here at all - a lost one, restored - takes it.
	if own && archive.IsRealDir(dstDir) {
		return 0
	}
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return 0
	}
	// What was last taken from this source, one folder per project and source: it is
	// how a later import tells "deleted here" from "never seen here". Kept beside
	// session-sync.json, out of Claude Code's own projects folder.
	base := filepath.Join(root, "session-sync-base", filepath.Base(filepath.Dir(dstDir)), source)
	// Everything this machine has held, from any source: what is in it and not here
	// was deleted here. The base alone missed an entry that went from here to another
	// machine and was then deleted here - offered back, it read as that machine's own.
	held := loadHeldMemory(root, filepath.Base(filepath.Dir(dstDir)))
	if !archive.IsRealDir(dstDir) {
		held = heldMemory{} // nothing here at all: a restore, not a deletion
	}
	stamp := time.Now().Format("20060102-150405")
	n := 0
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		dst := filepath.Join(dstDir, e.Name())
		src := filepath.Join(srcDir, e.Name())
		baseFile := filepath.Join(base, e.Name())
		srcInfo, err := e.Info()
		if err != nil {
			continue
		}
		// Already here as archived: remember it was taken, so that deleting it here is
		// not later mistaken for never having had it.
		if !dryRun && sameContent(src, dst) && !sameContent(src, baseFile) {
			_ = archive.CopyFile(src, baseFile)
		}

		// MEMORY.md is the index of every memory, loaded into each session, and each
		// machine edits its own. Replacing one with the other dropped one machine's
		// entries, here and after the next push in the archive; a plain union brought
		// back every entry either machine deleted. So with update it is merged against
		// what was last taken: what either side added arrives, what either deleted
		// stays deleted.
		if update && strings.EqualFold(e.Name(), "MEMORY.md") && archive.StatOrNil(dst) != nil {
			if mergeMemoryIndex(src, dst, baseFile, srcInfo, dryRun, held.Entries) {
				n++
			}
			continue
		}
		// A file this machine deleted after taking it is not brought back, as long as
		// the archived copy is still the one it took.
		if archive.StatOrNil(dst) == nil && (sameContent(src, baseFile) || held.Files[strings.ToLower(e.Name())]) {
			continue
		}
		if !shouldRestore(srcInfo, dst, update) {
			continue
		}
		if !dryRun {
			// A memory file replaced by another machine's newer copy is kept, out of the
			// way of push and of the memory Claude loads.
			if mine, err := os.ReadFile(dst); err == nil {
				if theirs, err := os.ReadFile(src); err != nil || bytes.Equal(mine, theirs) {
					continue
				}
				if err := archive.CopyFile(dst, filepath.Join(dstDir, ".replaced", stamp, e.Name())); err != nil {
					continue // never replace what could not be kept
				}
			}
			if err := archive.CopyFile(src, dst); err != nil {
				continue
			}
			_ = archive.CopyFile(src, baseFile)
		}
		n++
	}
	return n
}

func sameContent(a, b string) bool {
	x, err1 := os.ReadFile(a)
	y, err2 := os.ReadFile(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}

// mergeMemoryIndex merges the archived MEMORY.md into the local one against baseFile,
// what was last taken from it, reporting whether the local one changed. A copy dated
// in the future is refused.
func mergeMemoryIndex(src, dst, baseFile string, srcInfo os.FileInfo, dryRun bool, held map[string]bool) bool {
	// A copy dated in the future is refused outright, as for every memory file: it is
	// how an archive someone else can write would plant an instruction that stays
	// "newer" than any local edit.
	if srcInfo.ModTime().After(time.Now().Add(time.Hour)) {
		return false
	}
	theirs, err1 := os.ReadFile(src)
	mine, err2 := os.ReadFile(dst)
	dstInfo := archive.StatOrNil(dst)
	if err1 != nil || err2 != nil || dstInfo == nil {
		return false
	}
	base, _ := os.ReadFile(baseFile) // none yet: everything is new
	merged := mergeIndex(string(base), string(theirs), string(mine), srcInfo.ModTime().After(dstInfo.ModTime()), held)
	if dryRun {
		return merged != string(mine)
	}
	if merged != string(mine) {
		if archive.WriteFileAtomic(dst, []byte(merged), 0o644) != nil {
			return false // the base moves on only once its entries are here
		}
	}
	_ = archive.CopyFile(src, baseFile)
	return merged != string(mine)
}

// indexLink is the memory file an index line points at: "- [Title](file.md) - hook".
var indexLink = regexp.MustCompile(`\]\(([^)\s]+\.md)\)`)

// indexKey is what identifies a MEMORY.md entry: the file its line links to, or the
// line itself when it links to none.
func indexKey(line string) string {
	if m := indexLink.FindStringSubmatch(line); m != nil {
		return "link:" + strings.ToLower(m[1])
	}
	return "line:" + strings.TrimSpace(line)
}

// heldMemory is every memory entry and memory file this machine has had in one
// project, recorded at each push and import beside the base.
type heldMemory struct {
	Entries map[string]bool `json:"entries"`
	Files   map[string]bool `json:"files"`
}

func heldMemoryPath(root, bucket string) string {
	return filepath.Join(root, "session-sync-base", bucket, "held.json")
}

func loadHeldMemory(root, bucket string) heldMemory {
	var h heldMemory
	if raw, err := os.ReadFile(heldMemoryPath(root, bucket)); err == nil {
		_ = json.Unmarshal(raw, &h)
	}
	if h.Entries == nil {
		h.Entries = map[string]bool{}
	}
	if h.Files == nil {
		h.Files = map[string]bool{}
	}
	return h
}

// noteHeldMemory adds what memDir holds now to the project's record.
func noteHeldMemory(root, bucket, memDir string) {
	entries, err := os.ReadDir(memDir)
	if err != nil {
		return
	}
	h := loadHeldMemory(root, bucket)
	changed := false
	add := func(m map[string]bool, k string) {
		if !m[k] {
			m[k], changed = true, true
		}
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		add(h.Files, strings.ToLower(e.Name()))
		if !strings.EqualFold(e.Name(), "MEMORY.md") {
			continue
		}
		if raw, err := os.ReadFile(filepath.Join(memDir, e.Name())); err == nil {
			for _, l := range strings.Split(string(raw), "\n") {
				if strings.TrimSpace(l) != "" {
					add(h.Entries, indexKey(l))
				}
			}
		}
	}
	if changed {
		if data, err := json.MarshalIndent(h, "", "  "); err == nil {
			_ = archive.WriteFileAtomic(heldMemoryPath(root, bucket), append(data, '\n'), 0o644)
		}
	}
}

// mergeIndex merges two MEMORY.md indexes three ways. An entry is the file a line links
// to, or the line itself when it links to none; base is what was last taken from
// theirs. The result is mine, in its order, with what changed there since base:
// entries they deleted dropped, entries only they changed taken, and entries they
// added appended - and nothing this side deleted after taking it brought back. With
// no base, nothing counts as deleted, except what held - every entry this machine has
// had - names. An entry both sides changed gets the newer file's wording.
func mergeIndex(base, theirs, mine string, theirsNewer bool, held map[string]bool) string {
	key := indexKey
	lines := func(s string) []string {
		if strings.TrimSpace(s) == "" {
			return nil
		}
		return strings.Split(strings.TrimRight(s, "\r\n"), "\n")
	}
	entries := func(s string) map[string]string {
		m := map[string]string{}
		for _, l := range lines(s) {
			if k := key(l); strings.TrimSpace(l) != "" && m[k] == "" {
				m[k] = l
			}
		}
		return m
	}
	b, t := entries(base), entries(theirs)

	seen := map[string]bool{}
	var out []string
	for _, l := range lines(mine) {
		if strings.TrimSpace(l) == "" {
			out = append(out, l)
			continue
		}
		k := key(l)
		seen[k] = true
		was, inBase := b[k]
		now, inTheirs := t[k]
		switch {
		case inBase && !inTheirs && l == was:
			continue // deleted there, untouched here
		case inTheirs && now != l && inBase && l == was:
			l = now // changed there only
		case inTheirs && now != l && !(inBase && now == was) && theirsNewer:
			l = now // changed on both sides, or never compared: the newer wording
		}
		out = append(out, l)
	}
	for _, l := range lines(theirs) {
		k := key(l)
		if strings.TrimSpace(l) == "" || seen[k] {
			continue
		}
		seen[k] = true
		if _, inBase := b[k]; inBase || held[k] {
			continue // deleted here after it was taken, or after it went from here
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n") + "\n"
}

// restoreSessionFiles files a session's own folder, srcDir - rels, its subagent
// transcripts and saved tool output - adding only files that are not here yet. A local file
// belongs to a live or newer session and is never replaced. Each subagent transcript
// was pended in r before; its size is journaled once it lands, and a journal error
// stops it. failed names the files that could not be filed.
func restoreSessionFiles(srcDir, dstBucket, id string, rels []string, rules []rewrite.Rule, bareLeaf string, dryRun bool, r *importRecord) (failed []string, err error) {
	// Only what srcDir holds as plain files: rels may name a zip entry this copy lacks.
	have := map[string]bool{}
	for _, rel := range claude.SessionFiles(srcDir) {
		have[rel] = true
	}
	for _, rel := range rels {
		dst := filepath.Join(dstBucket, id, rel)
		if dryRun || !have[rel] || archive.StatOrNil(dst) != nil {
			continue
		}
		if ferr := fileSessionFile(filepath.Join(srcDir, rel), dst, rel, rules, bareLeaf); ferr != nil {
			failed = append(failed, fmt.Sprintf("%s/%s: %v", id, filepath.ToSlash(rel), ferr))
		}
		if err := r.settle(dst, true); err != nil {
			return failed, err
		}
	}
	return failed, nil
}

// fileSessionFile files one file of a session's own folder. A subagent transcript
// names the project's paths as the session does, so it goes through the same rewrite
// (valid JSON kept, written beside and renamed, dated before its source); saved tool
// output is copied byte for byte.
func fileSessionFile(src, dst, rel string, rules []rewrite.Rule, bareLeaf string) error {
	if claude.IsSubagentTranscript(rel) {
		_, err := fileTranscript(src, dst, rules, bareLeaf)
		return err
	}
	return archive.CopyFile(src, dst)
}

// olderThanArchive dates a rewritten transcript just before its archived source.
//
// A rewrite keeps the source's timestamp but usually changes the size, and when
// timestamps tie the larger file wins. So this machine's next push saw its rewritten
// copy as "newer", replaced the original machine's archived transcript with it, and
// from then on the original machine's own, shorter copy was refused as a truncation:
// its new turns were never archived again. Older by more than the comparison
// tolerance, the copy is never pushed back unless the session continues here.
func olderThanArchive(src, dst string) {
	if info, err := os.Stat(src); err == nil {
		t := info.ModTime().Add(-(archive.MTimeToleranceSeconds + 1) * time.Second)
		_ = os.Chtimes(dst, t, t)
	}
}

// printDiverged names on w the sessions --update left alone, and why.
func printDiverged(w io.Writer, names []string) {
	if len(names) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%d session(s) continued both here and on another machine. The local copy\n", len(names))
	fmt.Fprintln(w, "is kept as it is, and the other is filed as a session of its own - resume either:")
	for _, n := range names {
		fmt.Fprintf(w, "  %s\n", n)
	}
}

func verbFor(dryRun bool) string {
	if dryRun {
		return "would file"
	}
	return "filed"
}
