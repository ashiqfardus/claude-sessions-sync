package render

import (
	"regexp"
	"strings"
)

// markdown formats the Markdown Claude writes in its replies. It runs on text that is
// already HTML-escaped, so the only tags in its output are the ones written here:
// "<", ">", "&" and quotes from the transcript are entities by now and stay inert.
//
// shortcut: covers what Claude's replies lean on (bold, inline code, http(s) links,
// headings, pipe tables). Italics stay literal because * and _ in paths and globs
// would misfire; lists already read fine as pre-wrapped text.
func markdown(escaped string) string {
	lines := strings.Split(escaped, "\n")
	var b strings.Builder
	sep := ""
	for i := 0; i < len(lines); i++ {
		b.WriteString(sep)
		sep = "\n"
		line := lines[i]
		if isTableRow(line) && i+1 < len(lines) && isTableSep(lines[i+1]) {
			end := i + 2
			for end < len(lines) && isTableRow(lines[end]) {
				end++
			}
			writeTable(&b, line, lines[i+2:end])
			i = end - 1
			sep = "" // the table is a block; a newline after it would add a blank line
			continue
		}
		if m := heading.FindStringSubmatch(line); m != nil {
			b.WriteString(`<strong class="h">` + inline(m[1]) + `</strong>`)
			continue
		}
		b.WriteString(inline(line))
	}
	return b.String()
}

var (
	heading = regexp.MustCompile(`^#{1,6}\s+(.+)$`)
	bold    = regexp.MustCompile(`\*\*([^*\n]+?)\*\*`)
	// No "<" in the URL: escaped text has none, so one can only come from a tag
	// added above, and no "*", so bold markers cannot reach into an href.
	link = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^\s)<*]+)\)`)
)

// inline formats one line: `code` spans are kept verbatim, bold and links apply
// outside them. An unpaired backtick is left as written.
func inline(s string) string {
	parts := strings.Split(s, "`")
	if len(parts)%2 == 0 {
		parts[len(parts)-2] += "`" + parts[len(parts)-1]
		parts = parts[:len(parts)-1]
	}
	var b strings.Builder
	for i, p := range parts {
		if i%2 == 1 {
			b.WriteString("<code>" + p + "</code>")
			continue
		}
		p = bold.ReplaceAllString(p, "<strong>$1</strong>")
		p = link.ReplaceAllString(p, `<a href="$2" rel="noopener noreferrer">$1</a>`)
		b.WriteString(p)
	}
	return b.String()
}

func isTableRow(s string) bool { return strings.HasPrefix(strings.TrimSpace(s), "|") }

// isTableSep matches the |---|:--:| line that makes the row above it a table header.
func isTableSep(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "|") && strings.Contains(s, "-") && strings.Trim(s, "|-: ") == ""
}

func writeTable(b *strings.Builder, header string, rows []string) {
	b.WriteString(`<div class="tw"><table><tr>`)
	for _, c := range cells(header) {
		b.WriteString("<th>" + c + "</th>")
	}
	b.WriteString("</tr>")
	for _, r := range rows {
		b.WriteString("<tr>")
		for _, c := range cells(r) {
			b.WriteString("<td>" + c + "</td>")
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</table></div>")
}

func cells(row string) []string {
	row = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(row), "|"), "|")
	out := strings.Split(row, "|")
	for i, c := range out {
		out[i] = inline(strings.TrimSpace(c))
	}
	return out
}
