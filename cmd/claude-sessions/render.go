package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/render"
)

func cmdRender(args []string) error {
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	archiveDir := fs.String("archive", "", "override the synced destination folder")
	force := fs.Bool("force", false, "re-render pages that are already up to date")
	exclude := fs.String("exclude", "", "comma-separated text; projects matching any of it are not rendered")
	quiet := fs.Bool("quiet", false, "print nothing on success")
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
	if st, err := os.Stat(dest); err != nil || !st.IsDir() {
		return fmt.Errorf("%s is not reachable - is the sync client mounted?", dest)
	}

	cfg, _ := archive.LoadConfig(root)
	patterns := cfg.RenderExclude
	if strings.TrimSpace(*exclude) != "" {
		patterns = append(patterns, strings.Split(*exclude, ",")...)
	}

	res, err := render.Archive(dest, render.Options{
		Force:         *force,
		Exclude:       patterns,
		LocalProjects: claude.ProjectsDir(root),
	})
	if err != nil {
		return err
	}

	if !*quiet {
		fmt.Printf("\nRendered %d, %d already up to date -> %s\n",
			res.Rendered, res.UpToDate, res.OutputDir)
		if res.Failed > 0 {
			fmt.Printf("%d transcript(s) could not be rendered; their .jsonl files are untouched.\n", res.Failed)
		}
		if res.Skipped > 0 {
			fmt.Printf("%d transcript line(s) could not be parsed and were skipped.\n", res.Skipped)
		}
		fmt.Printf("Open %s/index.html\n", res.OutputDir)
	}
	return nil
}
