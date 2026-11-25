package main

import (
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
	archived, archiveDir := sourceFlags(fs)
	project := fs.String("project", "", "only sessions whose path or bucket contains this text")
	role := fs.String("role", "", "only messages from this role (user, assistant)")
	useRegexp := fs.Bool("regexp", false, "treat the query as a regular expression")
	caseSensitive := fs.Bool("case-sensitive", false, "match case exactly (default: case-insensitive)")
	all := fs.Bool("all", false, "also search tool input, tool output and thinking")
	limit := fs.Int("limit", 50, "stop after this many matches (0 = all)")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := parseFlags(fs, args, true); err != nil {
		return err
	}
	o := searchOpts{ClaudeDir: *claudeDir, ArchiveDir: *archiveDir, Query: strings.Join(fs.Args(), " "),
		Project: *project, Role: *role, Archived: *archived, Regexp: *useRegexp,
		CaseSensitive: *caseSensitive, All: *all, Limit: *limit}
	if !*asJSON {
		_, err := runSearch(stdStreams(), o)
		return err
	}
	res, err := runSearch(jsonStreams(), o)
	if err != nil {
		return err
	}
	return encodeJSON(os.Stdout, res.Hits)
}

type searchOpts struct {
	ClaudeDir, ArchiveDir, Query, Project, Role string
	Archived, Regexp, CaseSensitive, All        bool
	Limit                                       int
}

// searchResult is what a search found: its hits, how many sessions it read and could
// not read, and whether it stopped at the limit.
type searchResult struct {
	Hits                []hit
	Scanned, Unreadable int
	Stopped             bool
}

// runSearch prints the hits on s.Out and returns them.
func runSearch(s streams, o searchOpts) (searchResult, error) {
	query := o.Query
	if strings.TrimSpace(query) == "" {
		return searchResult{}, fmt.Errorf("usage: claude-sessions search [flags] <text>")
	}

	pattern := query
	if !o.Regexp {
		pattern = regexp.QuoteMeta(query)
	}
	if !o.CaseSensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return searchResult{}, fmt.Errorf("bad pattern: %w", err)
	}

	root, _, err := resolveRoot(o.ClaudeDir)
	if err != nil {
		return searchResult{}, err
	}
	dir, err := sessionsDir(root, o.Archived, o.ArchiveDir)
	if err != nil {
		return searchResult{}, err
	}
	buckets, err := claude.ListBuckets(dir)
	if err != nil {
		return searchResult{}, err
	}
	if o.Archived || o.ArchiveDir != "" {
		buckets = withZipped(buckets)
	}

	var hits []hit
	var scanned, unreadable int

	paths := newResolver(s.Err)

	// Newest sessions first, across every project. --limit used to keep the first
	// matches in alphabetical bucket order, which cut off exactly the recent sessions
	// someone is usually looking for.
	var candidates []candidate
	for _, b := range buckets {
		// A project filter is decided per bucket, so a filtered search never opens the
		// transcripts it is about to discard.
		if o.Project != "" {
			path := paths.Path(b)
			if !strings.Contains(strings.ToLower(path), strings.ToLower(o.Project)) &&
				!strings.Contains(strings.ToLower(b.Name), strings.ToLower(o.Project)) {
				continue
			}
		}
		for _, tr := range b.Transcripts {
			candidates = append(candidates, candidate{bucket: b.Name, t: tr})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].t.ModTime.After(candidates[j].t.ModTime)
	})

	for _, c := range candidates {
		if o.Limit > 0 && len(hits) >= o.Limit {
			break
		}
		tr := c.t

		// One pass. The cwd is picked up from the same walk that does the matching,
		// rather than paying for a separate head scan over every file.
		tpath, cleanup, err := openTranscript(s.Err, tr.Path)
		if err != nil {
			continue // a zip that cannot be read: noted on s.Err, skipped
		}
		scanned++
		var found []hit
		cwd := ""
		_, err = claude.Walk(tpath, func(r claude.Record) bool {
			if cwd == "" && r.Cwd != "" {
				cwd = r.Cwd
			}
			if o.Role != "" && !strings.EqualFold(r.Role, o.Role) && !strings.EqualFold(r.Type, o.Role) {
				return true
			}
			// The message text by default; with --all also tool input, tool output and
			// thinking, which is where "which command did I run" is answered.
			texts := []string{r.Text}
			if o.All {
				for _, blk := range r.Blocks {
					if blk.Kind != "text" {
						texts = append(texts, blk.Text)
					}
				}
			}
			for _, text := range texts {
				if loc := re.FindStringIndex(text); loc != nil {
					found = append(found, hit{
						Bucket: c.bucket, SessionID: tr.ID,
						Role: firstNonEmpty(r.Role, r.Type), When: r.Timestamp,
						Line: r.Line, Snippet: snippet(text, loc[0], loc[1]),
					})
					break
				}
			}
			return o.Limit <= 0 || len(hits)+len(found) < o.Limit
		})
		cleanup()
		if err != nil {
			unreadable++
		}

		// The path is only known once the walk has run, so stamp it afterwards.
		paths.Set(c.bucket, cwd)
		path, _ := paths.Known(c.bucket)
		if path == "" {
			path = c.bucket
		}
		for i := range found {
			found[i].Project = path
		}
		// Hits arrive grouped by session, newest session first, in line order within
		// it. They are not re-sorted: sorting by time across sessions interleaved them
		// and reprinted the same session header over and over.
		hits = append(hits, found...)
	}

	if hits == nil {
		hits = []hit{} // an empty result is [], never null
	}
	res := searchResult{Hits: hits, Scanned: scanned, Unreadable: unreadable, Stopped: o.Limit > 0 && len(hits) >= o.Limit}

	if len(hits) == 0 {
		fmt.Fprintf(s.Out, "No matches for %q in %d session(s).\n", query, scanned)
		return res, nil
	}

	lastSession := ""
	for _, h := range hits {
		if h.SessionID != lastSession {
			fmt.Fprintf(s.Out, "\n%s  %s\n", printable(h.Project), h.SessionID)
			lastSession = h.SessionID
		}
		when := ""
		if !h.When.IsZero() {
			when = h.When.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(s.Out, "  %-16s %-9s %s\n", when, h.Role, printable(h.Snippet))
	}

	fmt.Fprintf(s.Out, "\n%d match(es); %d session(s) searched, newest first.\n", len(hits), scanned)
	if res.Stopped {
		fmt.Fprintf(s.Out, "Stopped at --limit %d; older sessions were not searched.\n", o.Limit)
	}
	if unreadable > 0 {
		fmt.Fprintf(s.Out, "%d transcript(s) could not be read.\n", unreadable)
	}
	if o.Archived {
		fmt.Fprintln(s.Out, "Archived sessions are resumable once filed here: claude-sessions import")
	}
	// Two steps rather than `&&`, which Windows PowerShell 5.1 does not parse.
	fmt.Fprintf(s.Out, "Resume one with:  cd %s  then  claude --resume %s\n", shellPath(printable(hits[0].Project)), hits[0].SessionID)
	return res, nil
}

// shellPath quotes a path for a cd the user will type. Go's %q escapes backslashes,
// which turned E:\work\api into "E:\\work\\api" - not a path on Windows.
func shellPath(p string) string {
	if strings.ContainsAny(p, " '\"&;()$`") {
		return `"` + p + `"`
	}
	return p
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
