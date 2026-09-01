package main

import (
	"encoding/json"
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
	Sessions    int           `json:"sessions"`
	Projects    int           `json:"projects"`
	Bytes       int64         `json:"bytes"`
	Oldest      time.Time     `json:"oldest"`
	Newest      time.Time     `json:"newest"`
	MemoryOnly  int           `json:"memoryOnlyBuckets"`
	AtRiskUnder int           `json:"-"`
	ByProject   []projectStat `json:"byProject"`
}

func cmdStats(args []string) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, _, err := resolveRoot(*claudeDir)
	if err != nil {
		return err
	}
	buckets, err := claude.ListBuckets(claude.ProjectsDir(root))
	if err != nil {
		return err
	}

	rep := statsReport{}
	for _, b := range buckets {
		if !b.HasTranscripts() {
			if len(b.Memory) > 0 {
				rep.MemoryOnly++
			}
			continue
		}

		name := b.Name
		if newest, ok := b.Newest(); ok {
			if s, err := claude.ScanHead(newest.Path, headScanLines); err == nil && s.Cwd != "" {
				name = s.Cwd
			}
		}

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

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}

	if rep.Sessions == 0 {
		fmt.Println("No sessions found.")
		return nil
	}

	fmt.Printf("\n%d session(s) across %d project(s), %s total\n",
		rep.Sessions, rep.Projects, humanSize(rep.Bytes))
	if !rep.Oldest.IsZero() {
		fmt.Printf("spanning %s to %s\n",
			rep.Oldest.Format("2006-01-02"), rep.Newest.Format("2006-01-02"))
	}
	if rep.MemoryOnly > 0 {
		fmt.Printf("%d bucket(s) hold memory but no transcripts\n", rep.MemoryOnly)
	}
	fmt.Println()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SESSIONS\tSIZE\tLAST USED\tPROJECT")
	for _, p := range rep.ByProject {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n",
			p.Sessions, humanSize(p.Bytes), p.Newest.Format("2006-01-02"), shorten(p.Project, 48))
	}
	return w.Flush()
}
