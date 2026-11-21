// Package rewrite repoints the project path recorded inside a transcript.
//
// A transcript records its project's absolute path in several forms, and a session
// copied from another machine has to have all of them updated or Claude Code will not
// associate it with the local folder.
package rewrite

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
)

// Rule is one literal substitution, named so the result can be reported per form.
type Rule struct {
	Label   string
	Find    string
	Replace string
	re      *regexp.Regexp
	tail    bool // also convert the rest of the path's \\ pairs to /
}

// Rules builds the substitution table for oldPath -> newPath.
//
// ORDER MATTERS. The JSON-escaped form (E:\\x) must be replaced before the literal
// form (E:\x), or the literal rule rewrites half of each escaped pair and leaves
// invalid JSON behind.
//
// includeBareName also replaces the final path segment on its own. It is off by
// default because that segment appears in package names, git remotes and ordinary
// prose, where replacing it is wrong.
func Rules(oldPath, newPath string, includeBareName bool) []Rule {
	oldPath = strings.TrimRight(oldPath, `\/`)
	newPath = strings.TrimRight(newPath, `\/`)

	rules := []Rule{
		{Label: "json-escaped", Find: strings.ReplaceAll(oldPath, `\`, `\\`), Replace: strings.ReplaceAll(newPath, `\`, `\\`)},
		{Label: "forward-slash", Find: strings.ReplaceAll(oldPath, `\`, "/"), Replace: strings.ReplaceAll(newPath, `\`, "/")},
		// Git Bash spells E:\work\app as /e/work/app, and Claude Code records it that
		// way when a session runs under MSYS.
		{Label: "git-bash", Find: msysPath(oldPath), Replace: msysOrSlash(newPath)},
		{Label: "literal", Find: oldPath, Replace: newPath},
		{Label: "bucket-slug", Find: claude.Slug(oldPath), Replace: claude.Slug(newPath)},
	}

	if includeBareName {
		oldLeaf, newLeaf := LastSegment(oldPath), LastSegment(newPath)
		if !strings.EqualFold(oldLeaf, newLeaf) {
			rules = append(rules, Rule{Label: "bare-name", Find: oldLeaf, Replace: newLeaf})
		}
	}

	out := rules[:0]
	for _, r := range rules {
		if r.Find == "" || r.Find == r.Replace {
			continue
		}
		// Case-insensitive literal matching: a drive letter's case is decided by what
		// Claude recorded, not by what is on disk.
		r.re = regexp.MustCompile(`(?i)` + regexp.QuoteMeta(r.Find))
		out = append(out, r)
	}
	return out
}

// Literal is one plain substitution - a session id, say - matched case-insensitively
// at name boundaries like every other rule.
func Literal(label, find, replace string) Rule {
	return Rule{Label: label, Find: find, Replace: replace, re: regexp.MustCompile(`(?i)` + regexp.QuoteMeta(find))}
}

// Move is one project path that changed between machines.
// ConvertTail also turns the separators after the prefix into '/', for a Windows
// folder placed on a Unix machine.
type Move struct {
	From, To    string
	ConvertTail bool
}

// RulesForMoves builds rules for several moves at once - a session in a multi-folder
// workspace names every repository it worked in. The most specific path goes first,
// so /work/app/api is repointed before its parent /work/app could claim it. Of moves
// from one path the first wins; one to the same place that converts the tail makes
// the first convert it too.
//
// ponytail: rules apply in sequence, so a destination that equals another move's
// source would be rewritten twice. Nothing in import produces that.
func RulesForMoves(moves []Move) []Rule {
	var sorted []Move
	seen := map[string]int{}
	trim := func(p string) string { return strings.ToLower(strings.TrimRight(p, `\/`)) }
	for _, m := range moves {
		if !AbsoluteAnywhere(m.From) {
			continue
		}
		if i, ok := seen[trim(m.From)]; !ok {
			seen[trim(m.From)] = len(sorted)
			sorted = append(sorted, m)
		} else if trim(sorted[i].To) == trim(m.To) {
			sorted[i].ConvertTail = sorted[i].ConvertTail || m.ConvertTail
		}
	}
	sort.SliceStable(sorted, func(i, j int) bool { return len(sorted[i].From) > len(sorted[j].From) })
	var rules []Rule
	for _, m := range sorted {
		rs := Rules(m.From, m.To, false)
		for i := range rs {
			rs[i].tail = m.ConvertTail && rs[i].Label == "json-escaped"
		}
		rules = append(rules, rs...)
	}
	return rules
}

// AbsoluteAnywhere reports whether p is an absolute path on Windows or on a Unix,
// with at least one folder below the root.
//
// A move's From is a path another machine recorded, taken from the archive. "e" or
// "/" there would rewrite every occurrence of that text in every transcript, and the
// damaged lines still parse, so nothing would catch it.
func AbsoluteAnywhere(p string) bool {
	p = strings.TrimRight(p, `\/`)
	switch {
	case len(p) > 1 && p[0] == '/' && p[1] != '/':
		return true
	case len(p) > 3 && p[1] == ':' && unicode.IsLetter(rune(p[0])) && (p[2] == '\\' || p[2] == '/'):
		return true
	}
	return false
}

// msysPath returns the Git Bash form of a Windows drive path, or "" for any other.
func msysPath(p string) string {
	if len(p) < 2 || p[1] != ':' || !unicode.IsLetter(rune(p[0])) {
		return ""
	}
	return "/" + strings.ToLower(p[:1]) + strings.ReplaceAll(p[2:], `\`, "/")
}

func msysOrSlash(p string) string {
	if m := msysPath(p); m != "" {
		return m
	}
	return strings.ReplaceAll(p, `\`, "/")
}

// LastSegment is the last segment of a path from either OS. filepath.Base on Linux
// returns a whole Windows path, because '\' is not a separator there.
func LastSegment(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// Apply rewrites one line, reporting which rules matched.
//
// A match only counts when it is a whole path: /home/a/proj must not rewrite
// /home/a/proj2 or /home/a/proj-old, nor /mnt/home/a/proj. Before the match there
// must be no path-name character, and after it either none or a sentence-ending
// full stop.
func Apply(line string, rules []Rule, hits map[string]int) string {
	for _, r := range rules {
		matches := r.re.FindAllStringIndex(line, -1)
		if len(matches) == 0 {
			continue
		}
		var b strings.Builder
		last, n := 0, 0
		for _, m := range matches {
			if m[0] < last || !bounded(line, m[0], m[1]) {
				continue
			}
			b.WriteString(line[last:m[0]])
			// A literal replacement, so a '$' in the destination path is never
			// treated as an expansion token.
			b.WriteString(r.Replace)
			last = m[1]
			if r.tail {
				last = convertTail(&b, line, last)
			}
			n++
		}
		if n == 0 {
			continue
		}
		b.WriteString(line[last:])
		line = b.String()
		if hits != nil {
			hits[r.Label]++
		}
	}
	return line
}

// convertTail writes the rest of a JSON-escaped path from line[pos:] with each \\
// pair as '/', and returns where it stopped. A lone backslash (\n, \") ends it, so
// JSON escapes survive.
//
// shortcut: a path segment with a space ends the conversion, because a space also
// ends a path in prose; upgrade if work folders with spaces matter.
func convertTail(b *strings.Builder, line string, pos int) int {
	for strings.HasPrefix(line[pos:], `\\`) {
		j := pos + 2
		for j < len(line) && !strings.ContainsRune("\\ \t\r\n\"'`|<>", rune(line[j])) {
			j++
		}
		if j == pos+2 {
			break
		}
		b.WriteByte('/')
		b.WriteString(line[pos+2 : j])
		pos = j
	}
	return pos
}

func nameRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.'
}

func bounded(s string, start, end int) bool {
	if start > 0 {
		prev, size := utf8.DecodeLastRuneInString(s[:start])
		// "\n" and "\t" escapes inside a JSON string end in a letter but are
		// separators, and a path very often follows one in tool output.
		escaped := size == 1 && strings.ContainsRune("ntr", prev) && start >= 2 && s[start-2] == '\\'
		if nameRune(prev) && !escaped {
			return false
		}
	}
	if end < len(s) {
		next, size := utf8.DecodeRuneInString(s[end:])
		if next == '.' {
			after, _ := utf8.DecodeRuneInString(s[end+size:])
			return end+size == len(s) || !nameRune(after)
		}
		if nameRune(next) {
			return false
		}
	}
	return true
}

// Result reports what a file-level rewrite did.
type Result struct {
	Hits          map[string]int
	Lines         int
	BareLeftovers int // lines still naming the old folder without a path prefix
	Unparseable   int // lines that were not valid JSON before the rewrite either
}

// File rewrites src into dst.
//
// The source is never modified: rewriting happens on the way to the destination.
//
// Every line that was valid JSON must still be valid after the rewrite; if one is not,
// the destination is removed and the error names the line. Lines that were already
// broken - a truncated last line from a killed session is normal - are carried
// across as they are and counted, rather than failing the whole transcript.
func File(src, dst string, rules []Rule, bareLeaf string) (Result, error) {
	res := Result{Hits: map[string]int{}}

	in, err := os.Open(src)
	if err != nil {
		return res, err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return res, err
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-"+filepath.Base(dst)+"-*")
	if err != nil {
		return res, err
	}
	tmpName := tmp.Name()
	fail := func(e error) (Result, error) {
		tmp.Close()
		os.Remove(tmpName)
		return res, e
	}

	r := bufio.NewReaderSize(in, 256*1024)
	w := bufio.NewWriter(tmp)

	var bareRe *regexp.Regexp
	if bareLeaf != "" {
		bareRe = regexp.MustCompile(`(?i)` + regexp.QuoteMeta(bareLeaf))
	}

	for {
		line, readErr := r.ReadString('\n')
		trimmed := strings.TrimRight(line, "\r\n")
		if res.Lines == 0 {
			// A UTF-8 BOM makes the first line - the one carrying cwd - unparseable.
			trimmed = string(bytes.TrimPrefix([]byte(trimmed), []byte("\xef\xbb\xbf")))
		}

		if trimmed != "" || readErr == nil {
			res.Lines++
			out := Apply(trimmed, rules, res.Hits)

			if strings.TrimSpace(out) != "" {
				switch {
				case !json.Valid([]byte(trimmed)):
					res.Unparseable++
				case !json.Valid([]byte(out)):
					return fail(fmt.Errorf("rewrite produced invalid JSON at line %d of %s; nothing was kept:\n%s",
						res.Lines, filepath.Base(src), truncateForError(out)))
				}
				if bareRe != nil && bareRe.MatchString(out) {
					res.BareLeftovers++
				}
			}

			// JSONL is written with LF endings regardless of platform.
			if _, err := w.WriteString(out + "\n"); err != nil {
				return fail(err)
			}
		}

		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return fail(readErr)
		}
	}

	if err := w.Flush(); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return res, err
	}

	// Carry the source's timestamp and permissions across.
	//
	// This is not cosmetic. A restored transcript stamped "now" makes every session
	// look like it happened today - destroying the chronology the archive exists to
	// preserve - and makes the next push see every local file as newer than its
	// archived copy, re-uploading the entire archive.
	if info, err := os.Stat(src); err == nil {
		if err := os.Chmod(tmpName, info.Mode().Perm()); err != nil {
			os.Remove(tmpName)
			return res, err
		}
		if err := os.Chtimes(tmpName, info.ModTime(), info.ModTime()); err != nil {
			os.Remove(tmpName)
			return res, err
		}
	}

	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return res, err
	}
	return res, nil
}

func truncateForError(s string) string {
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}
