package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
)

type hit struct {
	Project   string    `json:"project"`
	Bucket    string    `json:"bucket"`
	SessionID string    `json:"session"`
	Role      string    `json:"role"`
	When      time.Time `json:"when"`
	Line      int       `json:"line"`
	Snippet   string    `json:"snippet"`
}

// cmdSearch greps every transcript. Archived history is only worth keeping if it can
// be found again, and nothing else in this space does it.
func cmdSearch(args []string) error {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	project := fs.String("project", "", "only sessions whose path or bucket contains this text")
	role := fs.String("role", "", "only messages from this role (user, assistant)")
	useRegexp := fs.Bool("regexp", false, "treat the query as a regular expression")
	ignoreCase := fs.Bool("i", true, "case-insensitive match")
	limit := fs.Int("limit", 50, "stop after this many matches (0 = all)")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return err
	}

	query := strings.Join(fs.Args(), " ")
	if strings.TrimSpace(query) == "" {
		return fmt.Errorf("usage: claude-sessions search [flags] <text>")
	}

	pattern := query
	if !*useRegexp {
		pattern = regexp.QuoteMeta(query)
	}
	if *ignoreCase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("bad pattern: %w", err)
	}

	root, _, err := resolveRoot(*claudeDir)
	if err != nil {
		return err
	}
	buckets, err := claude.ListBuckets(claude.ProjectsDir(root))
	if err != nil {
		return err
	}

	var hits []hit
	var scanned, unreadable int

	for _, b := range buckets {
		for _, tr := range b.Transcripts {
			if *limit > 0 && len(hits) >= *limit {
				break
			}
			// One cheap head-scan gives the project path, so a filtered search does
			// not read the body of transcripts it is going to discard.
			head, err := claude.ScanHead(tr.Path, headScanLines)
			if err != nil {
				unreadable++
				continue
			}
			path := head.Cwd
			if path == "" {
				path = b.Name
			}
			if *project != "" &&
				!strings.Contains(strings.ToLower(path), strings.ToLower(*project)) &&
				!strings.Contains(strings.ToLower(b.Name), strings.ToLower(*project)) {
				continue
			}

			scanned++
			_, err = claude.Walk(tr.Path, func(r claude.Record) bool {
				if r.Text == "" {
					return true
				}
				if *role != "" && !strings.EqualFold(r.Role, *role) && !strings.EqualFold(r.Type, *role) {
					return true
				}
				loc := re.FindStringIndex(r.Text)
				if loc == nil {
					return true
				}
				hits = append(hits, hit{
					Project: path, Bucket: b.Name, SessionID: tr.ID,
					Role: firstNonEmpty(r.Role, r.Type), When: r.Timestamp,
					Line: r.Line, Snippet: snippet(r.Text, loc[0], loc[1]),
				})
				return *limit <= 0 || len(hits) < *limit
			})
			if err != nil {
				unreadable++
			}
		}
	}

	sort.SliceStable(hits, func(i, j int) bool { return hits[i].When.After(hits[j].When) })

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(hits)
	}

	if len(hits) == 0 {
		fmt.Printf("No matches for %q in %d session(s).\n", query, scanned)
		return nil
	}

	lastSession := ""
	for _, h := range hits {
		if h.SessionID != lastSession {
			fmt.Printf("\n%s  %s\n", h.Project, h.SessionID)
			lastSession = h.SessionID
		}
		when := ""
		if !h.When.IsZero() {
			when = h.When.Format("2006-01-02 15:04")
		}
		fmt.Printf("  %-16s %-9s %s\n", when, h.Role, h.Snippet)
	}

	fmt.Printf("\n%d match(es) in %d session(s).\n", len(hits), scanned)
	if unreadable > 0 {
		fmt.Printf("%d transcript(s) could not be read.\n", unreadable)
	}
	fmt.Printf("Resume one with:  claude --resume %s\n", hits[0].SessionID)
	return nil
}

// snippet returns the match with a little context, on one line.
func snippet(text string, start, end int) string {
	const pad = 40
	from := start - pad
	if from < 0 {
		from = 0
	}
	to := end + pad
	if to > len(text) {
		to = len(text)
	}
	// Slicing by byte offset can cut a multi-byte rune in half, which shows up as a
	// replacement character in the middle of otherwise fine text. Walk out to the
	// nearest rune boundaries, then drop anything still malformed in the source.
	for from > 0 && !utf8.RuneStart(text[from]) {
		from--
	}
	for to < len(text) && !utf8.RuneStart(text[to]) {
		to++
	}

	out := strings.TrimSpace(strings.ToValidUTF8(text[from:to], ""))
	if from > 0 {
		out = "..." + out
	}
	if to < len(text) {
		out += "..."
	}
	return strings.Join(strings.Fields(out), " ")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
