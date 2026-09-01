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
	caseSensitive := fs.Bool("case-sensitive", false, "match case exactly (default: case-insensitive)")
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
	if !*caseSensitive {
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

	paths := newResolver()

	for _, b := range buckets {
		// A project filter is decided per bucket, so a filtered search never opens the
		// transcripts it is about to discard.
		if *project != "" {
			path := paths.Path(b)
			if !strings.Contains(strings.ToLower(path), strings.ToLower(*project)) &&
				!strings.Contains(strings.ToLower(b.Name), strings.ToLower(*project)) {
				continue
			}
		}

		for _, tr := range b.Transcripts {
			if *limit > 0 && len(hits) >= *limit {
				break
			}

			// One pass. The cwd is picked up from the same walk that does the
			// matching, rather than paying for a separate head scan over every file.
			scanned++
			var found []hit
			cwd := ""
			_, err := claude.Walk(tr.Path, func(r claude.Record) bool {
				if cwd == "" && r.Cwd != "" {
					cwd = r.Cwd
				}
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
				found = append(found, hit{
					Bucket: b.Name, SessionID: tr.ID,
					Role: firstNonEmpty(r.Role, r.Type), When: r.Timestamp,
					Line: r.Line, Snippet: snippet(r.Text, loc[0], loc[1]),
				})
				return *limit <= 0 || len(hits)+len(found) < *limit
			})
			if err != nil {
				unreadable++
			}

			// The path is only known once the walk has run, so stamp it afterwards.
			paths.Set(b.Name, cwd)
			path, _ := paths.Known(b.Name)
			if path == "" {
				path = b.Name
			}
			for i := range found {
				found[i].Project = path
			}
			hits = append(hits, found...)
		}
	}

	// Sort by session first, then by time within it. Sorting by time alone interleaves
	// sessions, and the grouped output below would then reprint the same session
	// header every time the listing switched back to it.
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].SessionID != hits[j].SessionID {
			return hits[i].When.After(hits[j].When)
		}
		return hits[i].Line < hits[j].Line
	})

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if hits == nil {
			hits = []hit{} // an empty result is [], never null
		}
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
