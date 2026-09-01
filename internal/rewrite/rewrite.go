// Package rewrite repoints the project path recorded inside a transcript.
//
// A transcript records its project's absolute path in several forms, and a session
// copied from another machine has to have all of them updated or Claude Code will not
// associate it with the local folder.
package rewrite

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Rule is one literal substitution, named so the result can be reported per form.
type Rule struct {
	Label   string
	Find    string
	Replace string
	re      *regexp.Regexp
}

// Slug flattens an absolute path the way Claude Code names a bucket.
//
// Duplicated from the claude package rather than imported, because this package must
// not depend on it: the substitution table needs the slug form of a path that may not
// exist on this machine at all.
func Slug(path string) string {
	return strings.NewReplacer(":", "-", `\`, "-", "/", "-", " ", "-").Replace(path)
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
		{Label: "literal", Find: oldPath, Replace: newPath},
		{Label: "bucket-slug", Find: Slug(oldPath), Replace: Slug(newPath)},
	}

	if includeBareName {
		oldLeaf, newLeaf := filepath.Base(oldPath), filepath.Base(newPath)
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

// Apply rewrites one line, reporting which rules matched.
func Apply(line string, rules []Rule, hits map[string]int) string {
	for _, r := range rules {
		before := line
		// A literal replacement, so a '$' in the destination path is never treated as
		// a regexp expansion token.
		line = r.re.ReplaceAllLiteralString(line, r.Replace)
		if before != line && hits != nil {
			hits[r.Label]++
		}
	}
	return line
}

// Result reports what a file-level rewrite did.
type Result struct {
	Hits          map[string]int
	Lines         int
	BareLeftovers int // lines still naming the old folder without a path prefix
}

// File rewrites src into dst.
//
// The source is never modified: rewriting happens on the way to the destination.
//
// Every output line is re-validated as JSON before the result is kept. A transcript
// that no longer parses is worse than no transcript, so on any failure the
// destination is removed and the error names the first bad line.
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

		if trimmed != "" || readErr == nil {
			res.Lines++
			out := Apply(trimmed, rules, res.Hits)

			if strings.TrimSpace(out) != "" {
				if !json.Valid([]byte(out)) {
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
