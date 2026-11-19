package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/rewrite"
)

type restoreResult struct {
	Bucket        string
	Copied        int
	Updated       int // already here, and the archived copy continued it
	Skipped       int
	Memory        int
	Hits          map[string]int
	BareLeftovers int
	// Failed names transcripts that could not be filed. One bad transcript no longer
	// abandons the rest of the project.
	Failed []string
	// Diverged names sessions continued both here and elsewhere; they are left alone.
	Diverged []string
}

type restoreOpts struct {
	BareName bool // also replace the bare folder name
	DryRun   bool
	Update   bool // refresh sessions and memory already here when the archive has more
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
	if err := parseFlags(fs, args, false); err != nil {
		return err
	}
	if *source == "" || *projectPath == "" {
		return fmt.Errorf("usage: claude-sessions restore --source <folder> --project-path <path> [--rewrite-from <old path>]")
	}
	if *rewriteFrom != "" && !rewrite.AbsoluteAnywhere(*rewriteFrom) {
		return fmt.Errorf("--rewrite-from must be the absolute project path recorded in the transcripts, got %q", *rewriteFrom)
	}

	root, _, err := resolveRoot(*claudeDir)
	if err != nil {
		return err
	}

	lock, err := archive.AcquireLock(root)
	if err != nil {
		return err
	}
	defer lock.Release()

	res, err := restoreInto(root, *source, *projectPath, *rewriteFrom, nil,
		restoreOpts{BareName: *bareName, DryRun: *dryRun, Update: *update})
	if err != nil {
		return err
	}

	fmt.Printf("\n%s %d transcript(s) and %d memory file(s), skipped %d already present -> %s\n",
		verbFor(*dryRun), res.Copied, res.Memory, res.Skipped, res.Bucket)
	if res.Updated > 0 {
		fmt.Printf("updated %d session(s) that continued on another machine\n", res.Updated)
	}
	for _, f := range res.Failed {
		fmt.Printf("  x %s\n", f)
	}
	printDiverged(res.Diverged)

	if len(res.Hits) > 0 {
		fmt.Println("\nLines rewritten, by form:")
		forms := make([]string, 0, len(res.Hits))
		for k := range res.Hits {
			forms = append(forms, k)
		}
		sort.Strings(forms)
		for _, f := range forms {
			fmt.Printf("  %-14s %d\n", f, res.Hits[f])
		}
	}
	if res.BareLeftovers > 0 {
		fmt.Printf("\n%d line(s) still mention the old folder name without a path prefix\n", res.BareLeftovers)
		fmt.Println("(package names, git remotes, prose). Re-run with --include-bare-name to replace those too.")
	}
	if !*dryRun && res.Copied > 0 {
		fmt.Printf("\nNow: cd %s  then  claude --resume\n", shellPath(strings.TrimRight(*projectPath, `\/`)))
	}
	return nil
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
	var transcripts []string
	for _, e := range entries {
		// Regular files only: a symlink in the archive would be read through, filing
		// whatever it points at as a transcript.
		if e.Type().IsRegular() && strings.EqualFold(filepath.Ext(e.Name()), ".jsonl") {
			transcripts = append(transcripts, filepath.Join(source, e.Name()))
		}
	}
	if len(transcripts) == 0 {
		return res, fmt.Errorf("no .jsonl transcripts in %s (memory .md files are not sessions - check the folder)", source)
	}

	// Relative input ("." or "../app") used to become a bucket named "." or "", filing
	// transcripts straight into the projects folder.
	abs, err := filepath.Abs(projectPath)
	if err != nil {
		return res, err
	}
	newPath := filepath.Clean(abs)
	projectsDir := claude.ProjectsDir(root)

	// Prefer a bucket that already exists, matched case-insensitively: the casing of a
	// slug follows the string Claude recorded, not what is on disk, so computing a
	// fresh one can create a second bucket that --resume never looks in.
	bucketName := claude.Slug(newPath)
	if existing, ok := claude.FindBucket(projectsDir, bucketName); ok {
		bucketName = existing.Name
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

	for _, src := range transcripts {
		name := filepath.Base(src)
		dst := filepath.Join(res.Bucket, name)
		restoreSessionFiles(source, res.Bucket, strings.TrimSuffix(name, filepath.Ext(name)), opts.DryRun)
		exists := archive.StatOrNil(dst) != nil
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

		if !exists {
			out, err := fileTranscript(src, dst, rules, bareLeaf)
			if err != nil {
				res.Failed = append(res.Failed, fmt.Sprintf("%s: %v", name, err))
				continue
			}
			res.add(out)
			res.Copied++
			continue
		}

		switch out, state, err := updateTranscript(src, dst, rules, bareLeaf); {
		case err != nil:
			res.Failed = append(res.Failed, fmt.Sprintf("%s: %v", name, err))
		case state == extended:
			res.add(out)
			res.Updated++
		case state == diverged:
			res.Diverged = append(res.Diverged, name)
		default:
			res.Skipped++
		}
	}

	// Memory travels with the sessions. push archives it, so a restore that left it
	// behind would put the transcripts on the new machine and strand everything the
	// project had remembered - which is exactly what happened before this was added.
	//
	// Memory is markdown, not JSONL, and is never path-rewritten: it is prose, and
	// substituting paths inside it would corrupt meaning rather than fix references.
	res.Memory = restoreMemory(filepath.Join(source, "memory"), filepath.Join(res.Bucket, "memory"), opts.DryRun, opts.Update)

	if len(res.Failed) > 0 && res.Copied == 0 && res.Skipped == 0 && res.Updated == 0 {
		return res, fmt.Errorf("no transcript could be filed: %s", res.Failed[0])
	}
	return res, nil
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
// extends it - the same session, continued on another machine. Transcripts are
// append-only, so anything else is a session that also continued here, and replacing
// it would lose those turns.
func updateTranscript(src, dst string, rules []rewrite.Rule, bareLeaf string) (rewrite.Result, updateState, error) {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-update-*")
	if err != nil {
		return rewrite.Result{}, unchanged, err
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName) // already gone when it was renamed into place

	out, err := fileTranscript(src, tmpName, rules, bareLeaf)
	if err != nil {
		return out, unchanged, err
	}
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
	return out, extended, os.Rename(tmpName, dst)
}

// restoreMemory files a project's memory from the archive.
//
// Memory is loaded into every session as instructions, and the archive is written by
// other machines and anyone the folder is shared with. So a file that already exists
// here is never replaced unless the user asks with update - replacing used to happen
// whenever the archived copy was newer, and a file dated in the future stayed newer
// than any local edit forever. Even then, a future-dated file is refused.
func restoreMemory(srcDir, dstDir string, dryRun, update bool) int {
	// A symlink planted in the archive in place of the folder would otherwise copy
	// whatever it points at into this machine's memory, and the next push back out.
	if !archive.IsRealDir(srcDir) {
		return 0
	}
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		dst := filepath.Join(dstDir, e.Name())
		src := filepath.Join(srcDir, e.Name())

		if srcInfo, err := e.Info(); err != nil || !shouldRestore(srcInfo, dst, update) {
			continue
		}
		if !dryRun {
			if err := archive.CopyFile(src, dst); err != nil {
				continue
			}
		}
		n++
	}
	return n
}

// restoreSessionFiles copies a session's own folder - subagent transcripts and saved
// tool output - adding only files that are not here yet. A local file belongs to a
// live or newer session and is never replaced.
//
// ponytail: paths inside subagent transcripts are not rewritten; add it if a
// subagent's recorded cwd ever matters to Claude Code.
func restoreSessionFiles(srcBucket, dstBucket, id string, dryRun bool) {
	for _, rel := range claude.SessionFiles(filepath.Join(srcBucket, id)) {
		dst := filepath.Join(dstBucket, id, rel)
		if dryRun || archive.StatOrNil(dst) != nil {
			continue
		}
		_ = archive.CopyFile(filepath.Join(srcBucket, id, rel), dst)
	}
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

// printDiverged names sessions --update left alone, and why.
func printDiverged(names []string) {
	if len(names) == 0 {
		return
	}
	fmt.Printf("\n%d session(s) continued both here and on another machine, so neither copy\n", len(names))
	fmt.Println("contains the other; the local one was kept:")
	for _, n := range names {
		fmt.Printf("  %s\n", n)
	}
}

func verbFor(dryRun bool) string {
	if dryRun {
		return "would file"
	}
	return "filed"
}
