package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
)

type projectStat struct {
	Project  string    `json:"project"`
	Sessions int       `json:"sessions"`
	Bytes    int64     `json:"bytes"`
	Oldest   time.Time `json:"oldest"`
	Newest   time.Time `json:"newest"`
}

type statsReport struct {
	Sessions   int           `json:"sessions"`
	Projects   int           `json:"projects"`
	Bytes      int64         `json:"bytes"`
	Oldest     time.Time     `json:"oldest"`
	Newest     time.Time     `json:"newest"`
	MemoryOnly int           `json:"memoryOnlyBuckets"`
	ByProject  []projectStat `json:"byProject"`
}

func cmdStats(args []string) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	archived, archiveDir := sourceFlags(fs)
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := parseFlags(fs, args, false); err != nil {
		return err
	}
	o := statsOpts{ClaudeDir: *claudeDir, ArchiveDir: *archiveDir, Archived: *archived}
	if !*asJSON {
		_, err := runStats(stdStreams(), o)
		return err
	}
	rep, err := runStats(jsonStreams(), o)
	if err != nil {
		return err
	}
	return encodeJSON(os.Stdout, rep)
}

type statsOpts struct {
	ClaudeDir, ArchiveDir string
	Archived              bool
}

// runStats prints the totals on s.Out and returns them.
func runStats(s streams, o statsOpts) (statsReport, error) {
	root, _, err := resolveRoot(o.ClaudeDir)
	if err != nil {
		return statsReport{}, err
	}
	dir, err := sessionsDir(root, o.Archived, o.ArchiveDir)
	if err != nil {
		return statsReport{}, err
	}
	buckets, err := claude.ListBuckets(dir)
	if err != nil {
		return statsReport{}, err
	}
	if o.Archived || o.ArchiveDir != "" {
		buckets = withZipped(buckets)
	}

	paths := newResolver(s.Err)
	rep := statsReport{}
	for _, b := range buckets {
		if !b.HasTranscripts() {
			if len(b.Memory) > 0 {
				rep.MemoryOnly++
			}
			continue
		}

		name := paths.Path(b)

		ps := projectStat{Project: name}
		for _, t := range b.Transcripts {
			ps.Sessions++
			ps.Bytes += t.Size
			if ps.Oldest.IsZero() || t.ModTime.Before(ps.Oldest) {
				ps.Oldest = t.ModTime
			}
			if t.ModTime.After(ps.Newest) {
				ps.Newest = t.ModTime
			}
		}

		rep.Sessions += ps.Sessions
		rep.Bytes += ps.Bytes
		rep.Projects++
		if rep.Oldest.IsZero() || ps.Oldest.Before(rep.Oldest) {
			rep.Oldest = ps.Oldest
		}
		if ps.Newest.After(rep.Newest) {
			rep.Newest = ps.Newest
		}
		rep.ByProject = append(rep.ByProject, ps)
	}

	sort.Slice(rep.ByProject, func(i, j int) bool {
		return rep.ByProject[i].Sessions > rep.ByProject[j].Sessions
	})

	if rep.Sessions == 0 {
		fmt.Fprintln(s.Out, "No sessions found.")
		return rep, nil
	}

	fmt.Fprintf(s.Out, "\n%d session(s) across %d project(s), %s total\n",
		rep.Sessions, rep.Projects, humanSize(rep.Bytes))
	if !rep.Oldest.IsZero() {
		fmt.Fprintf(s.Out, "spanning %s to %s\n",
			rep.Oldest.Format("2006-01-02"), rep.Newest.Format("2006-01-02"))
	}
	if rep.MemoryOnly > 0 {
		fmt.Fprintf(s.Out, "%d bucket(s) hold memory but no transcripts\n", rep.MemoryOnly)
	}
	fmt.Fprintln(s.Out)

	w := tabwriter.NewWriter(s.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SESSIONS\tSIZE\tLAST USED\tPROJECT")
	for _, p := range rep.ByProject {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n",
			p.Sessions, humanSize(p.Bytes), p.Newest.Format("2006-01-02"), shorten(printable(p.Project), 48))
	}
	return rep, w.Flush()
}
