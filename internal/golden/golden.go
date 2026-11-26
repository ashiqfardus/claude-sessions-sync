// Package golden compares command output with files under testdata/golden, after
// taking out what changes from run to run and from one OS to another. Only tests
// import it.
package golden

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "write testdata/golden/*.txt instead of comparing with them")

var stamp = regexp.MustCompile(`\d{4}-\d\d-\d\d \d\d:\d\d:\d\d`)

// Normalize replaces each root path with its placeholder (roots maps placeholder to
// path), longest first and in every form a command may print it: as given, with
// symlinks resolved (macOS /private/var), with forward slashes, and JSON-escaped.
// Then CRLF becomes LF, a log timestamp <TIME>, and every backslash a slash, so the
// result is the same on every OS.
func Normalize(s string, roots map[string]string) string {
	type pair struct{ from, to string }
	var pairs []pair
	for ph, dir := range roots {
		forms := []string{dir}
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			forms = append(forms, real)
		}
		for _, f := range forms {
			forms = append(forms, filepath.ToSlash(f))
		}
		for _, f := range forms {
			pairs = append(pairs, pair{f, ph}, pair{strings.ReplaceAll(f, `\`, `\\`), ph})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if len(pairs[i].from) != len(pairs[j].from) {
			return len(pairs[i].from) > len(pairs[j].from)
		}
		return pairs[i].from < pairs[j].from
	})
	for _, p := range pairs {
		if p.from != "" {
			s = replaceWhole(s, p.from, p.to)
		}
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = stamp.ReplaceAllString(s, "<TIME>")
	// A JSON-escaped separator (\\) is one slash, as the / it is on the other OSes. This
	// also hides a wrong separator in the output, and turns JSON's \" and \u003c into /" and /u003c.
	return strings.NewReplacer(`\\`, "/", `\`, "/").Replace(s)
}

// replaceWhole replaces each from in s that is a whole name: /x/001 is not the start of
// /x/0012. A match ends at anything but a letter, digit or _ - a separator, quote,
// space, "-", punctuation or the end of the text - whatever from itself ends in.
func replaceWhole(s, from, to string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, from)
		if i < 0 {
			break
		}
		if end := i + len(from); end < len(s) && wordByte(s[end]) {
			b.WriteString(s[:i+1])
			s = s[i+1:]
			continue
		}
		b.WriteString(s[:i])
		b.WriteString(to)
		s = s[i+len(from):]
	}
	b.WriteString(s)
	return b.String()
}

func wordByte(c byte) bool {
	return c == '_' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// Check compares got with testdata/golden/<name>.txt, or writes that file when the
// test runs with -update.
func Check(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name+".txt")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test with -update to write it)", err)
	}
	want := string(raw)
	if got == want {
		return
	}
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	i := 0
	for i < len(w) && i < len(g) && w[i] == g[i] {
		i++
	}
	t.Errorf("%s: line %d differs (go test -update rewrites it)\n--- want\n%s\n--- got\n%s",
		path, i+1, context(w, i), context(g, i))
}

// context is lines[i] with up to 3 lines either side, each numbered, the differing one marked.
func context(lines []string, i int) string {
	var b strings.Builder
	for n := i - 3; n <= i+3; n++ {
		if n < 0 || n >= len(lines) {
			continue
		}
		mark := " "
		if n == i {
			mark = ">"
		}
		fmt.Fprintf(&b, "%s %4d: %s\n", mark, n+1, lines[n])
	}
	if i >= len(lines) {
		b.WriteString("> (end of file)\n")
	}
	return b.String()
}
