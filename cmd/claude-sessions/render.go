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
	include := fs.String("include", "", "comma-separated text; publishes projects left out by --exclude again, on every machine")
	exclude := fs.String("exclude", "", "comma-separated text; projects matching any of it are unpublished and stay so on every machine (saved)")
	quiet := fs.Bool("quiet", false, "print nothing on success")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := parseFlags(fs, args, false); err != nil {
		return err
	}

	o := renderOpts{ClaudeDir: *claudeDir, ArchiveDir: *archiveDir, Force: *force, Quiet: *quiet, Exclude: splitList(*exclude), Include: splitList(*include)}
	if !*asJSON {
		_, err := runRender(stdStreams(), o)
		return err
	}
	res, err := runRender(jsonStreams(), o)
	if err != nil {
		return err
	}
	return encodeJSON(os.Stdout, res)
}

type renderOpts struct {
	ClaudeDir, ArchiveDir string
	Force, Quiet          bool
	Exclude               []string // saved, then excluded on every machine
	Include               []string // taken off the exclusions, on every machine
}

// runRender renders the archive's pages and returns what it did, saying so on s.Out
// unless o.Quiet.
func runRender(s streams, o renderOpts) (render.Result, error) {
	root, _, err := resolveRoot(o.ClaudeDir)
	if err != nil {
		return render.Result{}, err
	}
	dest, _, err := archive.ResolveDestination(root, o.ArchiveDir)
	if err != nil {
		return render.Result{}, err
	}
	if dest == "" {
		return render.Result{}, fmt.Errorf("no archive destination: run `claude-sessions config set-destination <folder>`")
	}
	if st, err := os.Stat(dest); err != nil || !st.IsDir() {
		return render.Result{}, fmt.Errorf("%s is not reachable - is the sync client mounted?", dest)
	}

	// Saved, then shared through the archive. A one-off exclusion was undone by the
	// next push - within 30 minutes, by the sweep - with nothing said: pages the user
	// had withdrawn for privacy were quietly published again.
	if len(o.Exclude) > 0 {
		if err := archive.ExcludeRender(root, dest, o.Exclude); err != nil {
			return render.Result{}, err
		}
		if !o.Quiet {
			fmt.Fprintf(s.Out, "Excluding %s from now on (saved to %s and %s).\n",
				strings.Join(o.Exclude, ", "), archive.ConfigPath(root), archive.SharedExcludePath(dest))
			fmt.Fprintln(s.Out, "To publish it again: claude-sessions render --include <pattern>")
		}
	}
	if len(o.Include) > 0 {
		if err := archive.IncludeRender(root, dest, o.Include); err != nil {
			return render.Result{}, err
		}
		if !o.Quiet {
			fmt.Fprintf(s.Out, "Publishing %s again (removed from %s and %s).\n",
				strings.Join(o.Include, ", "), archive.ConfigPath(root), archive.SharedExcludePath(dest))
		}
	}
	patterns, republished := archive.RenderExcludesDropped(root, dest, nil, true)
	for _, p := range republished {
		fmt.Fprintf(s.Err, "RENDER: %s is published again (another computer ran render --include %s); run render --exclude %s to withdraw it\n", p, p, p)
	}

	res, err := render.Archive(dest, render.Options{
		Force:         o.Force,
		Exclude:       patterns,
		LocalProjects: claude.ProjectsDir(root),
		BrokenNote:    renderBrokenPath(root),
	})
	if err != nil {
		return render.Result{}, err
	}
	if res.Note != "" {
		fmt.Fprintln(s.Err, "RENDER: "+res.Note)
	}

	if !o.Quiet {
		fmt.Fprintf(s.Out, "\nRendered %d, %d already up to date -> %s\n",
			res.Rendered, res.UpToDate, res.OutputDir)
		if res.Excluded > 0 {
			fmt.Fprintf(s.Out, "%d project(s) excluded", res.Excluded)
			if res.Unpublished > 0 {
				fmt.Fprintf(s.Out, "; %d previously published page(s) removed", res.Unpublished)
			}
			fmt.Fprintln(s.Out, ".")
		}
		if res.Failed > 0 {
			fmt.Fprintf(s.Out, "%d transcript(s) could not be rendered; their .jsonl files are untouched.\n", res.Failed)
		}
		if res.Skipped > 0 {
			fmt.Fprintf(s.Out, "%d transcript line(s) could not be parsed and were skipped.\n", res.Skipped)
		}
		fmt.Fprintf(s.Out, "Open %s/index.html\n", res.OutputDir)
	}
	return res, nil
}
