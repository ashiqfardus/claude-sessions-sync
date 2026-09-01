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

// headScanLines caps how far into a transcript a listing reads for the cwd and the
// first prompt. Both live in the opening entries; reading a 40MB file to print one
// row would make the listing unusable.
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

// candidate is a transcript before it has been read - everything here comes from the
// directory entry, so building the list costs no file opens.
type candidate struct {
	bucket string
	t      claude.Transcript
}

func cmdLs(args []string) error {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	project := fs.String("project", "", "only sessions whose bucket or path contains this substring")
	since := fs.String("since", "", "only sessions updated on or after this date (YYYY-MM-DD)")
	until := fs.String("until", "", "only sessions updated on or before this date (YYYY-MM-DD)")
	limit := fs.Int("limit", 0, "show at most this many sessions (0 = all)")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return err
	}

	sinceT, err := parseDate(*since, "since")
	if err != nil {
		return err
	}
	untilT, err := parseDate(*until, "until")
	if err != nil {
		return err
	}
	if !untilT.IsZero() {
		untilT = untilT.Add(24*time.Hour - time.Nanosecond) // --until is inclusive
	}

	root, _, err := resolveRoot(*claudeDir)
	if err != nil {
		return err
	}

	buckets, err := claude.ListBuckets(claude.ProjectsDir(root))
	if err != nil {
		return fmt.Errorf("reading %s: %w", claude.ProjectsDir(root), err)
	}

	// Collect and order first, using only directory metadata, so that --limit reads N
	// transcripts rather than every one. On a machine with hundreds of sessions this
	// is the difference between instant and several seconds.
	var candidates []candidate
	for _, b := range buckets {
		for _, t := range b.Transcripts {
			if !sinceT.IsZero() && t.ModTime.Before(sinceT) {
				continue
			}
			if !untilT.IsZero() && t.ModTime.After(untilT) {
				continue
			}
			candidates = append(candidates, candidate{bucket: b.Name, t: t})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].t.ModTime.After(candidates[j].t.ModTime)
	})

	// A project filter can only be applied after reading each transcript's cwd, so the
	// limit cannot be applied up front in that case.
	if *limit > 0 && *project == "" && len(candidates) > *limit {
		candidates = candidates[:*limit]
	}

	var rows []sessionRow
	for _, c := range candidates {
		if *limit > 0 && len(rows) >= *limit {
			break
		}
		s, err := claude.ScanHead(c.t.Path, headScanLines)
		if err != nil {
			// An unreadable transcript still deserves a row: knowing a session exists
			// matters more than knowing what it was about.
			rows = append(rows, sessionRow{
				Bucket: c.bucket, Project: c.bucket, SessionID: c.t.ID,
				Updated: c.t.ModTime, SizeBytes: c.t.Size,
				FirstPrompt: "(unreadable: " + err.Error() + ")",
			})
			continue
		}
		path := s.Cwd
		if path == "" {
			path = c.bucket
		}
		if *project != "" &&
			!strings.Contains(strings.ToLower(path), strings.ToLower(*project)) &&
			!strings.Contains(strings.ToLower(c.bucket), strings.ToLower(*project)) {
			continue
		}
		rows = append(rows, sessionRow{
			Bucket: c.bucket, Project: path, SessionID: c.t.ID,
			Updated: c.t.ModTime, SizeBytes: c.t.Size,
			FirstPrompt: s.FirstPrompt, Skipped: s.Skipped,
		})
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if rows == nil {
			rows = []sessionRow{} // an empty result is [], never null
		}
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
			shortID(r.SessionID),
			r.FirstPrompt,
		)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Printf("\n%d session(s).\n", len(rows))
	return nil
}

func parseDate(v, flagName string) (time.Time, error) {
	if strings.TrimSpace(v) == "" {
		return time.Time{}, nil
	}
	t, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(v), time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("--%s must be YYYY-MM-DD, got %q", flagName, v)
	}
	return t, nil
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

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// shorten keeps the tail of a path, which is the part that identifies it.
func shorten(s string, max int) string {
	if max < 4 {
		// Guard rather than panic on a negative slice index: a caller passing a tiny
		// width is a bug, but not one worth crashing a listing over.
		if len(s) <= max {
			return s
		}
		return s[:max]
	}
	if len(s) <= max {
		return s
	}
	return "..." + s[len(s)-(max-3):]
}
