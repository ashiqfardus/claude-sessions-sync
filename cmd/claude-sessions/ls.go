package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
)

// headScanLines caps how far into a transcript ls reads for the cwd and the first
// prompt. Both live in the opening entries; reading a 40MB file to print one row
// would make the listing unusable.
const headScanLines = 200

type sessionRow struct {
	Bucket      string    `json:"bucket"`
	Project     string    `json:"project"`
	SessionID   string    `json:"session"`
	Updated     time.Time `json:"updated"`
	SizeBytes   int64     `json:"sizeBytes"`
	FirstPrompt string    `json:"firstPrompt"`
	Skipped     int       `json:"skippedLines,omitempty"`
}

func cmdLs(args []string) error {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	project := fs.String("project", "", "only sessions whose bucket or path contains this substring")
	limit := fs.Int("limit", 0, "show at most this many sessions (0 = all)")
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
		return fmt.Errorf("reading %s: %w", claude.ProjectsDir(root), err)
	}

	var rows []sessionRow
	for _, b := range buckets {
		for _, t := range b.Transcripts {
			s, err := claude.ScanHead(t.Path, headScanLines)
			if err != nil {
				// An unreadable transcript still deserves a row: knowing a session
				// exists matters more than knowing what it was about.
				rows = append(rows, sessionRow{
					Bucket: b.Name, Project: b.Name, SessionID: t.ID,
					Updated: t.ModTime, SizeBytes: t.Size,
					FirstPrompt: "(unreadable: " + err.Error() + ")",
				})
				continue
			}
			p := s.Cwd
			if p == "" {
				p = b.Name
			}
			if *project != "" &&
				!strings.Contains(strings.ToLower(p), strings.ToLower(*project)) &&
				!strings.Contains(strings.ToLower(b.Name), strings.ToLower(*project)) {
				continue
			}
			rows = append(rows, sessionRow{
				Bucket: b.Name, Project: p, SessionID: t.ID,
				Updated: t.ModTime, SizeBytes: t.Size,
				FirstPrompt: s.FirstPrompt, Skipped: s.Skipped,
			})
		}
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].Updated.After(rows[j].Updated) })
	if *limit > 0 && len(rows) > *limit {
		rows = rows[:*limit]
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}

	if len(rows) == 0 {
		fmt.Println("No sessions found under", claude.ProjectsDir(root))
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "UPDATED\tSIZE\tPROJECT\tSESSION\tFIRST PROMPT")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			r.Updated.Format("2006-01-02 15:04"),
			humanSize(r.SizeBytes),
			shorten(r.Project, 34),
			r.SessionID[:min(8, len(r.SessionID))],
			r.FirstPrompt,
		)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Printf("\n%d session(s).\n", len(rows))
	return nil
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0fK", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// shorten keeps the tail of a path, which is the part that identifies it.
func shorten(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return "..." + s[len(s)-(max-3):]
}
