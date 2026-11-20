package render

import (
	"encoding/json"
	"strings"
	"testing"
)

// renderTurn renders one session holding a single message and returns the page.
// blocks is the message's JSON content array.
func renderTurn(t *testing.T, role, blocks string) string {
	t.Helper()
	dest := writeArchive(t,
		`{"type":"`+role+`","cwd":"/work/api","timestamp":"2026-08-30T09:00:00.000Z","message":{"role":"`+role+`","content":[`+blocks+`]}}`)
	if _, err := Archive(dest, Options{}); err != nil {
		t.Fatal(err)
	}
	return page(t, dest)
}

func textBlock(text string) string {
	raw, _ := json.Marshal(map[string]string{"type": "text", "text": text})
	return string(raw)
}

func assistantPage(t *testing.T, text string) string {
	t.Helper()
	return renderTurn(t, "assistant", textBlock(text))
}

func wantAll(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
}

func wantNone(t *testing.T, got string, unwanted ...string) {
	t.Helper()
	for _, u := range unwanted {
		if strings.Contains(got, u) {
			t.Errorf("unexpected %q in:\n%s", u, got)
		}
	}
}

func TestMarkdownBoldAndInlineCode(t *testing.T) {
	got := assistantPage(t, "Run **go test** with `-count=1` now.")
	wantAll(t, got, `<strong>go test</strong>`, `<code>-count=1</code>`)
	wantNone(t, got, `**go test**`, "`-count=1`")
}

func TestMarkdownHeading(t *testing.T) {
	got := assistantPage(t, "## Results\nall green")
	wantAll(t, got, `<strong class="h">Results</strong>`, `all green`)
	wantNone(t, got, `## Results`)
}

func TestMarkdownTable(t *testing.T) {
	got := assistantPage(t, "Sizes:\n| Name | Size |\n|---|---:|\n| `a.go` | **12** |\n| b | 3 |\ndone")
	wantAll(t, got,
		`<div class="tw"><table>`,
		`<th>Name</th><th>Size</th>`,
		`<td><code>a.go</code></td><td><strong>12</strong></td>`,
		`<td>b</td><td>3</td>`,
		`done`)
	wantNone(t, got, `|---|`)
}

// A pipe line without a separator row under it is just text, not a table.
func TestMarkdownPipeWithoutSeparatorIsText(t *testing.T) {
	got := assistantPage(t, "| grep foo | wc -l")
	wantNone(t, got, `<table>`)
	wantAll(t, got, `| grep foo | wc -l`)
}

func TestMarkdownLinks(t *testing.T) {
	got := assistantPage(t, "See [the docs](https://example.com/a?x=1&y=2) for more.")
	wantAll(t, got, `<a href="https://example.com/a?x=1&amp;y=2" rel="noopener noreferrer">the docs</a>`)
}

// Only http(s) becomes a link; anything else stays literal text.
func TestMarkdownLinkRejectsOtherSchemes(t *testing.T) {
	got := assistantPage(t, "[click](javascript:alert(1)) [f](file:///etc/passwd)")
	wantNone(t, got, `href="javascript`, `href="file`)
	wantAll(t, got, `[click](javascript:alert(1))`)
}

// Formatting is applied to already-escaped text, so markup inside it stays inert.
func TestMarkdownStaysEscaped(t *testing.T) {
	got := assistantPage(t, "**<b>x</b>** `<script>alert(1)</script>` "+
		`[<i>y</i>](https://e.com/" onmouseover="alert(1))`+"\n| <img src=x> | h |\n|---|---|\n| <svg> | c |")
	wantNone(t, got, `<b>x`, `<script>alert`, `<i>y`, `" onmouseover`, `<img src`, `<svg>`)
	wantAll(t, got, `<strong>&lt;b&gt;x&lt;/b&gt;</strong>`, `<code>&lt;script&gt;alert(1)&lt;/script&gt;</code>`)
}

// Code is shown as written: no formatting inside fences or inline code.
func TestMarkdownLeavesCodeLiteral(t *testing.T) {
	got := assistantPage(t, "x `a **b** c` y\n```go\n**not bold** [l](https://e.com)\n```")
	wantAll(t, got, `<code>a **b** c</code>`, `**not bold** [l](https://e.com)`)
	wantNone(t, got, `<strong>b</strong>`, `<strong>not bold</strong>`, `<a href="https://e.com"`)
}

// Only Claude writes Markdown. Prompts, tool input and tool output are shown as written.
func TestMarkdownOnlyForAssistantText(t *testing.T) {
	user := renderTurn(t, "user", textBlock("keep **this** and `that` literal"))
	wantAll(t, user, `keep **this** and `+"`that`"+` literal`)
	wantNone(t, user, `<strong>`, `<code>that`)

	tools := renderTurn(t, "assistant",
		`{"type":"tool_use","name":"Bash","input":{"command":"echo **x**"}}`)
	wantNone(t, tools, `<strong>x</strong>`)

	result := renderTurn(t, "user", `{"type":"tool_result","content":"**raw** | a |\n|---|"}`)
	wantNone(t, result, `<strong>raw</strong>`, `<table>`)
}

func TestMarkdownInThinking(t *testing.T) {
	got := renderTurn(t, "assistant", `{"type":"thinking","thinking":"check **the lock** first"}`)
	wantAll(t, got, `<strong>the lock</strong>`)
}

// Blank lines around a code block would show as empty lines in pre-wrapped text; the
// blocks are already separated, so they are dropped.
func TestTextAroundCodeBlockHasNoBlankLines(t *testing.T) {
	got := assistantPage(t, "before:\n\n```\nx := 1\n```\n\nafter")
	wantAll(t, got, `<div class="t">before:</div><pre>`, `</pre><div class="t">after</div>`)
}

// An unpaired marker is left alone rather than swallowing the rest of the line.
func TestMarkdownUnpairedMarkers(t *testing.T) {
	got := assistantPage(t, "a ** b and ` c")
	wantAll(t, got, "a ** b and ` c")
	wantNone(t, got, `<strong>`, `<code> c`)
}
