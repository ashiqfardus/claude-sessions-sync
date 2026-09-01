package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/identity"
)

type importRow struct {
	Bucket   string `json:"bucket"`
	Recorded string `json:"recordedPath"`
	Local    string `json:"localPath,omitempty"`
	How      string `json:"matchedBy"`
	Filed    int    `json:"filed"`
	Skipped  int    `json:"skipped"`
	Note     string `json:"note,omitempty"`
}

// cmdImport is the reason this project exists: it files an archive's sessions onto
// this machine even when the projects live at different paths here.
func cmdImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	archiveDir := fs.String("archive", "", "override the synced destination folder")
	roots := fs.String("search-root", "", "comma-separated folders to scan for projects (default: parents of known projects, plus your home)")
	depth := fs.Int("depth", 3, "how deep to scan under each search root")
	dryRun := fs.Bool("dry-run", false, "report what would happen without writing")
	bareName := fs.Bool("include-bare-name", false, "also replace the bare folder name inside transcripts")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, _, err := resolveRoot(*claudeDir)
	if err != nil {
		return err
	}
	dest, src, err := archive.ResolveDestination(root, *archiveDir)
	if err != nil {
		return err
	}
	if dest == "" {
		return fmt.Errorf("no archive destination: run `claude-sessions config set-destination <folder>`")
	}
	if st, err := os.Stat(dest); err != nil || !st.IsDir() {
		return fmt.Errorf("%s is not reachable - is the sync client mounted?", dest)
	}

	manifest, bad, err := archive.Merged(dest)
	if err != nil {
		return err
	}
	if len(manifest) == 0 {
		return fmt.Errorf("no manifest in %s - run `claude-sessions push` on a machine that has the sessions first", dest)
	}

	searchRoots := defaultSearchRoots(manifest, *roots)

	if !*asJSON {
		fmt.Printf("archive      %s (%s)\n", dest, src)
		fmt.Printf("search roots %s\n\n", strings.Join(searchRoots, ", "))
		for _, b := range bad {
			fmt.Printf("! ignoring unreadable manifest shard %s\n", b)
		}
	}

	buckets := make([]string, 0, len(manifest))
	for name := range manifest {
		buckets = append(buckets, name)
	}
	sort.Strings(buckets)

	var rows []importRow
	var filed, sameLayout, notFound, ambiguous int

	for _, bucket := range buckets {
		p := manifest[bucket]
		archived := filepath.Join(archive.ProjectsDir(dest), bucket)
		if !hasTranscripts(archived) {
			continue
		}

		match := identity.Resolve(p.Path, p.Leaf, p.Remote, searchRoots, *depth)
		row := importRow{Bucket: bucket, Recorded: p.Path, How: string(match.How)}

		switch match.How {
		case identity.NotFound:
			row.Note = fmt.Sprintf("no folder named %q under the search roots", p.Leaf)
			if len(match.Candidates) > 0 {
				row.Note = fmt.Sprintf("%q exists here but is a different repository", p.Leaf)
			}
			notFound++
			rows = append(rows, row)
			// Say so. A project that is silently absent from the output looks like it
			// was handled, and the user has no reason to go looking for it.
			if !*asJSON {
				fmt.Printf("- %s: %s\n", bucket, row.Note)
				for _, c := range match.Candidates {
					fmt.Printf("      %s (different remote)\n", c)
				}
			}
			continue

		case identity.Ambiguous:
			row.Note = fmt.Sprintf("%d folders named %q and no git remote settles it", len(match.Candidates), p.Leaf)
			ambiguous++
			rows = append(rows, row)
			if !*asJSON {
				fmt.Printf("! %s: %s\n", bucket, row.Note)
				for _, c := range match.Candidates {
					fmt.Printf("      %s\n", c)
				}
			}
			continue
		}

		row.Local = match.Path

		// Only rewrite when the path actually differs; an identical layout needs no
		// edit, and not touching the bytes is always safer.
		rewriteFrom := ""
		if !strings.EqualFold(strings.TrimRight(p.Path, `\/`), strings.TrimRight(match.Path, `\/`)) {
			rewriteFrom = p.Path
		}

		res, err := restoreInto(root, archived, match.Path, rewriteFrom, *bareName, *dryRun)
		if err != nil {
			row.Note = err.Error()
			rows = append(rows, row)
			if !*asJSON {
				fmt.Printf("x %s: %s\n", bucket, err)
			}
			continue
		}
		row.Filed, row.Skipped = res.Copied, res.Skipped
		filed += res.Copied
		if match.How == identity.ByRecordedPath {
			sameLayout++
		}
		rows = append(rows, row)

		if !*asJSON {
			marker := "+"
			if match.How == identity.ByRecordedPath {
				marker = "="
			}
			fmt.Printf("%s %s -> %s (matched by %s): %d filed, %d already present\n",
				marker, bucket, match.Path, match.How, res.Copied, res.Skipped)
		}
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if rows == nil {
			rows = []importRow{}
		}
		return enc.Encode(rows)
	}

	fmt.Printf("\n%d transcript(s) filed. %d same layout, %d not found, %d ambiguous.\n",
		filed, sameLayout, notFound, ambiguous)
	if notFound+ambiguous > 0 {
		fmt.Println("\nAnything not placed can be filed by hand:")
		fmt.Println("  claude-sessions restore --source '<archive>/projects/<bucket>' \\")
		fmt.Println("      --project-path '<local path>' --rewrite-from '<recorded path>'")
	}
	return nil
}

func hasTranscripts(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".jsonl") {
			return true
		}
	}
	return false
}

// defaultSearchRoots picks somewhere sensible to look when the user has not said.
//
// The parents of projects this machine already knows about are the best guess: they
// are where this user actually keeps code. The home directory is added as a backstop.
func defaultSearchRoots(manifest map[string]archive.Project, flagValue string) []string {
	if strings.TrimSpace(flagValue) != "" {
		var out []string
		for _, r := range strings.Split(flagValue, ",") {
			if r = strings.TrimSpace(r); r != "" {
				out = append(out, r)
			}
		}
		return out
	}

	seen := map[string]bool{}
	var roots []string
	add := func(p string) {
		if p == "" {
			return
		}
		key := strings.ToLower(p)
		if seen[key] {
			return
		}
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			return
		}
		seen[key] = true
		roots = append(roots, p)
	}

	for _, p := range manifest {
		if p.Path != "" {
			add(filepath.Dir(p.Path))
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		add(home)
	}
	sort.Strings(roots)
	return roots
}

// cmdPull is the blunt counterpart to import: copy archived transcripts into buckets
// of the same name, with no path rewriting and no identity matching.
//
// Correct only when the machines share a layout. import is what you want otherwise,
// and the output says so.
func cmdPull(args []string) error {
	fs := flag.NewFlagSet("pull", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	archiveDir := fs.String("archive", "", "override the synced destination folder")
	dryRun := fs.Bool("dry-run", false, "report what would be copied without writing")
	if err := fs.Parse(args); err != nil {
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

	archivedBuckets, err := archive.BucketNames(dest)
	if err != nil {
		return err
	}
	if len(archivedBuckets) == 0 {
		fmt.Println("Nothing to pull: the archive holds no projects.")
		return nil
	}

	projectsDir := claude.ProjectsDir(root)
	var pulled, already int

	for _, name := range archivedBuckets {
		srcDir := filepath.Join(archive.ProjectsDir(dest), name)
		entries, err := os.ReadDir(srcDir)
		if err != nil {
			continue
		}

		// Match an existing local bucket case-insensitively before creating one.
		targetName := name
		if existing, ok := claude.FindBucket(projectsDir, name); ok {
			targetName = existing.Name
		}

		for _, e := range entries {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".jsonl") {
				continue
			}
			target := filepath.Join(projectsDir, targetName, e.Name())
			if _, err := os.Stat(target); err == nil {
				already++
				continue
			}
			if !*dryRun {
				if err := copyPlain(filepath.Join(srcDir, e.Name()), target); err != nil {
					return err
				}
			}
			pulled++
		}
	}

	fmt.Printf("\n%s %d transcript(s), %d already present.\n", verbFor(*dryRun), pulled, already)
	fmt.Println("pull files by bucket name only. If a project lives at a different path here,")
	fmt.Println("use `claude-sessions import`, which matches by identity and rewrites paths.")
	return nil
}
