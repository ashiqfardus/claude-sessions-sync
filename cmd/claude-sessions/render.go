package main

import (
	"encoding/json"
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
	exclude := fs.String("exclude", "", "comma-separated text; projects matching any of it are unpublished and stay so on every machine (saved)")
	quiet := fs.Bool("quiet", false, "print nothing on success")
	asJSON := fs.Bool("json", false, "machine-readable output")
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
	if st, err := os.Stat(dest); err != nil || !st.IsDir() {
		return fmt.Errorf("%s is not reachable - is the sync client mounted?", dest)
	}

	// Saved, then shared through the archive. A one-off exclusion was undone by the
	// next push - within 30 minutes, by the sweep - with nothing said: pages the user
	// had withdrawn for privacy were quietly published again.
	if extra := splitList(*exclude); len(extra) > 0 {
		cfg, err := archive.LoadConfig(root)
		if err != nil {
			return err
		}
		cfg.RenderExclude = append(cfg.RenderExclude, extra...)
		if err := archive.SaveConfig(root, cfg); err != nil {
			return err
		}
		if !*quiet && !*asJSON {
			fmt.Printf("Excluding %s from now on (saved to %s and %s).\n",
				strings.Join(extra, ", "), archive.ConfigPath(root), archive.SharedExcludePath(dest))
			fmt.Println("To publish again, remove it from renderExclude there and from render-exclude.txt.")
		}
	}
	patterns := archive.RenderExcludes(root, dest, nil, true)

	res, err := render.Archive(dest, render.Options{
		Force:         *force,
		Exclude:       patterns,
		LocalProjects: claude.ProjectsDir(root),
	})
	if err != nil {
		return err
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}

	if !*quiet {
		fmt.Printf("\nRendered %d, %d already up to date -> %s\n",
			res.Rendered, res.UpToDate, res.OutputDir)
		if res.Excluded > 0 {
			fmt.Printf("%d project(s) excluded", res.Excluded)
			if res.Unpublished > 0 {
				fmt.Printf("; %d previously published page(s) removed", res.Unpublished)
			}
			fmt.Println(".")
		}
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
