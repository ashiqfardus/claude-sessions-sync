package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/rewrite"
)

type restoreResult struct {
	Bucket        string
	Copied        int
	Skipped       int
	Memory        int
	Hits          map[string]int
	BareLeftovers int
	// Failed names transcripts that could not be filed. One bad transcript no longer
	// abandons the rest of the project.
	Failed []string
}

func cmdRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	source := fs.String("source", "", "folder of .jsonl transcripts to file (required)")
	projectPath := fs.String("project-path", "", "the project's path on THIS machine (required)")
	rewriteFrom := fs.String("rewrite-from", "", "the project path recorded inside the transcripts")
	bareName := fs.Bool("include-bare-name", false, "also replace the bare folder name (hits package names and prose too)")
	dryRun := fs.Bool("dry-run", false, "report what would happen without writing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *source == "" || *projectPath == "" {
		return fmt.Errorf("usage: claude-sessions restore --source <folder> --project-path <path> [--rewrite-from <old path>]")
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

	res, err := restoreInto(root, *source, *projectPath, *rewriteFrom, nil, *bareName, *dryRun)
	if err != nil {
		return err
	}

	fmt.Printf("\n%s %d transcript(s) and %d memory file(s), skipped %d already present -> %s\n",
		verbFor(*dryRun), res.Copied, res.Memory, res.Skipped, res.Bucket)
	for _, f := range res.Failed {
		fmt.Printf("  x %s\n", f)
	}

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
		fmt.Printf("\nNow: cd %q  then  claude --resume\n", strings.TrimRight(*projectPath, `\/`))
	}
	return nil
}

// restoreInto files transcripts from source into the bucket for projectPath.
// extra are other repositories that moved - the siblings a multi-folder workspace
// session also worked in - repointed alongside the project itself.
func restoreInto(root, source, projectPath, rewriteFrom string, extra []rewrite.Move, bareName, dryRun bool) (restoreResult, error) {
	res := restoreResult{Hits: map[string]int{}}

	entries, err := os.ReadDir(source)
	if err != nil {
		return res, fmt.Errorf("source not readable: %w", err)
	}
	var transcripts []string
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".jsonl") {
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
	if strings.TrimSpace(rewriteFrom) != "" {
		moves = append([]rewrite.Move{{From: rewriteFrom, To: newPath}}, extra...)
		bareLeaf = rewrite.LastSegment(strings.TrimRight(rewriteFrom, `\/`))
		if strings.EqualFold(bareLeaf, rewrite.LastSegment(newPath)) {
			bareLeaf = "" // unchanged name: nothing to warn about
		}
	}
	// One list, most specific path first, so a parent folder never claims the paths of
	// a repository inside it that moved somewhere else.
	rules := rewrite.RulesForMoves(moves)
	if bareName && strings.TrimSpace(rewriteFrom) != "" {
		for _, r := range rewrite.Rules(rewriteFrom, newPath, true) {
			if r.Label == "bare-name" {
				rules = append(rules, r)
			}
		}
	}

	for _, src := range transcripts {
		dst := filepath.Join(res.Bucket, filepath.Base(src))
		restoreSessionFiles(source, res.Bucket, strings.TrimSuffix(filepath.Base(src), filepath.Ext(src)), dryRun)
		if _, err := os.Stat(dst); err == nil {
			res.Skipped++
			continue
		}
		if dryRun {
			res.Copied++
			continue
		}

		if len(rules) == 0 {
			if err := copyPlain(src, dst); err != nil {
				res.Failed = append(res.Failed, fmt.Sprintf("%s: %v", filepath.Base(src), err))
				continue
			}
			res.Copied++
			continue
		}

		out, err := rewrite.File(src, dst, rules, bareLeaf)
		if err != nil {
			res.Failed = append(res.Failed, fmt.Sprintf("%s: %v", filepath.Base(src), err))
			continue
		}
		for k, v := range out.Hits {
			res.Hits[k] += v
		}
		res.BareLeftovers += out.BareLeftovers
		res.Copied++
	}

	// Memory travels with the sessions. push archives it, so a restore that left it
	// behind would put the transcripts on the new machine and strand everything the
	// project had remembered - which is exactly what happened before this was added.
	//
	// Memory is markdown, not JSONL, and is never path-rewritten: it is prose, and
	// substituting paths inside it would corrupt meaning rather than fix references.
	res.Memory = restoreMemory(filepath.Join(source, "memory"), filepath.Join(res.Bucket, "memory"), dryRun)

	if len(res.Failed) > 0 && res.Copied == 0 && res.Skipped == 0 {
		return res, fmt.Errorf("no transcript could be filed: %s", res.Failed[0])
	}
	return res, nil
}

func restoreMemory(srcDir, dstDir string, dryRun bool) int {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		dst := filepath.Join(dstDir, e.Name())
		src := filepath.Join(srcDir, e.Name())

		// Only overwrite when the archived copy is genuinely different, so a memory
		// file edited on this machine is not silently replaced by an older one.
		srcInfo, statErr := os.Stat(src)
		if statErr != nil {
			continue
		}
		if !archive.NeedsCopy(srcInfo, archive.StatOrNil(dst)) {
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

func copyPlain(src, dst string) error {
	// Delegates so that timestamps, permissions and the atomic rename are handled in
	// exactly one place.
	return archive.CopyFile(src, dst)
}

func verbFor(dryRun bool) string {
	if dryRun {
		return "would file"
	}
	return "filed"
}
