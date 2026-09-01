package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/rewrite"
)

type restoreResult struct {
	Bucket        string
	Copied        int
	Skipped       int
	Hits          map[string]int
	BareLeftovers int
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

	res, err := restoreInto(root, *source, *projectPath, *rewriteFrom, *bareName, *dryRun)
	if err != nil {
		return err
	}

	fmt.Printf("\n%s %d transcript(s), skipped %d already present -> %s\n",
		verbFor(*dryRun), res.Copied, res.Skipped, res.Bucket)

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
func restoreInto(root, source, projectPath, rewriteFrom string, bareName, dryRun bool) (restoreResult, error) {
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

	newPath := strings.TrimRight(projectPath, `\/`)
	projectsDir := claude.ProjectsDir(root)

	// Prefer a bucket that already exists, matched case-insensitively: the casing of a
	// slug follows the string Claude recorded, not what is on disk, so computing a
	// fresh one can create a second bucket that --resume never looks in.
	bucketName := claude.Slug(newPath)
	if existing, ok := claude.FindBucket(projectsDir, bucketName); ok {
		bucketName = existing.Name
	}
	res.Bucket = filepath.Join(projectsDir, bucketName)

	var rules []rewrite.Rule
	bareLeaf := ""
	if strings.TrimSpace(rewriteFrom) != "" {
		rules = rewrite.Rules(rewriteFrom, newPath, bareName)
		bareLeaf = filepath.Base(strings.TrimRight(rewriteFrom, `\/`))
		if strings.EqualFold(bareLeaf, filepath.Base(newPath)) {
			bareLeaf = "" // unchanged name: nothing to warn about
		}
	}

	for _, src := range transcripts {
		dst := filepath.Join(res.Bucket, filepath.Base(src))
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
				return res, err
			}
			res.Copied++
			continue
		}

		out, err := rewrite.File(src, dst, rules, bareLeaf)
		if err != nil {
			return res, err
		}
		for k, v := range out.Hits {
			res.Hits[k] += v
		}
		res.BareLeftovers += out.BareLeftovers
		res.Copied++
	}

	return res, nil
}

func copyPlain(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}

func verbFor(dryRun bool) string {
	if dryRun {
		return "would file"
	}
	return "filed"
}
