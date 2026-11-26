package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
)

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
	archived, archiveDir := sourceFlags(fs)
	project := fs.String("project", "", "only sessions whose bucket or path contains this substring")
	since := fs.String("since", "", "only sessions updated on or after this date (YYYY-MM-DD)")
	until := fs.String("until", "", "only sessions updated on or before this date (YYYY-MM-DD)")
	limit := fs.Int("limit", 0, "show at most this many sessions (0 = all)")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := parseFlags(fs, args, false); err != nil {
		return err
	}
	o := lsOpts{ClaudeDir: *claudeDir, ArchiveDir: *archiveDir, Project: *project,
		Since: *since, Until: *until, Archived: *archived, Limit: *limit}
	if !*asJSON {
		_, err := runLs(stdStreams(), o)
		return err
	}
	rows, err := runLs(jsonStreams(), o)
	if err != nil {
		return err
	}
	return encodeJSON(os.Stdout, rows)
}

type lsOpts struct {
	ClaudeDir, ArchiveDir, Project, Since, Until string
	Archived                                     bool
	Limit                                        int
}

// runLs lists the sessions on s.Out and returns them.
func runLs(s streams, o lsOpts) ([]sessionRow, error) {
	sinceT, err := parseDate(o.Since, "since")
	if err != nil {
		return nil, err
	}
	untilT, err := parseDate(o.Until, "until")
	if err != nil {
		return nil, err
	}
	if !untilT.IsZero() {
		untilT = untilT.Add(24*time.Hour - time.Nanosecond) // --until is inclusive
	}

	root, _, err := resolveRoot(o.ClaudeDir)
	if err != nil {
		return nil, err
	}

	dir, err := sessionsDir(root, o.Archived, o.ArchiveDir)
	if err != nil {
		return nil, err
	}
	buckets, err := claude.ListBuckets(dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	archived := o.Archived || o.ArchiveDir != ""
	if archived {
		buckets = withZipped(buckets)
	}

	// Collect and order first, using only directory metadata, so that --limit reads N
	// transcripts rather than every one: the loop below stops at N rows. On a machine
	// with hundreds of sessions this is the difference between instant and several
	// seconds.
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

	// The limit counts rows, not candidates: a project filter, or a zip that cannot be
	// read, drops a candidate only once it is read.
	var rows []sessionRow
	for _, c := range candidates {
		if o.Limit > 0 && len(rows) >= o.Limit {
			break
		}
		var sum claude.Summary
		if archived {
			sum, err = scanHead(s.Err, c.t.Path, headScanLines)
		} else {
			sum, err = claude.ScanTitle(c.t.Path, headScanLines) // on this disk: may read on for a title
		}
		if err != nil {
			if isZip(c.t.Path) {
				continue // noted on s.Err by scanHead
			}
			// An unreadable transcript still deserves a row: knowing a session exists
			// matters more than knowing what it was about.
			rows = append(rows, sessionRow{
				Bucket: c.bucket, Project: c.bucket, SessionID: c.t.ID,
				Updated: c.t.ModTime, SizeBytes: c.t.Size,
				FirstPrompt: "(unreadable: " + err.Error() + ")",
			})
			continue
		}
		path := sum.Cwd
		if path == "" {
			path = c.bucket
		}
		if o.Project != "" &&
			!strings.Contains(strings.ToLower(path), strings.ToLower(o.Project)) &&
			!strings.Contains(strings.ToLower(c.bucket), strings.ToLower(o.Project)) {
			continue
		}
		rows = append(rows, sessionRow{
			Bucket: c.bucket, Project: path, SessionID: c.t.ID,
			Updated: c.t.ModTime, SizeBytes: c.t.Size,
			FirstPrompt: sum.FirstPrompt, Skipped: sum.Skipped,
		})
	}
	if rows == nil {
		rows = []sessionRow{} // an empty result is [], never null
	}

	if len(rows) == 0 {
		fmt.Fprintln(s.Out, "No sessions found under", dir)
		return rows, nil
	}

	// The full session ID: `claude --resume` needs all of it, and a shortened one
	// could only be copied to find it does not work.
	w := tabwriter.NewWriter(s.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "UPDATED\tSIZE\tPROJECT\tSESSION\tFIRST PROMPT")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			r.Updated.Format("2006-01-02 15:04"),
			humanSize(r.SizeBytes),
			shorten(printable(r.Project), 34),
			r.SessionID,
			printable(r.FirstPrompt),
		)
	}
	if err := w.Flush(); err != nil {
		return rows, err
	}
	fmt.Fprintf(s.Out, "\n%d session(s).\n", len(rows))
	return rows, nil
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
	// Counted in characters: a byte slice split non-ASCII paths mid-character.
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return "..." + string(r[len(r)-(max-3):])
}
