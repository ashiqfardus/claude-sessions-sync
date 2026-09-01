package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Record is one transcript entry, reduced to the parts this tool uses.
type Record struct {
	Type      string    // "user", "assistant", or something a later release invented
	Role      string    // message.role, when present
	Text      string    // human-readable text, with injected blocks removed
	Blocks    []Block   // the message's content, structured, for rendering
	Timestamp time.Time // zero when absent or in an unrecognised layout
	Cwd       string    // the project's real absolute path, as Claude recorded it
	SessionID string
	IsMeta    bool
	Line      int // 1-based line number, for reporting a match
}

// Block is one piece of a message's content.
//
// Kinds seen in practice are text, thinking, tool_use and tool_result; anything else
// is carried through with whatever text could be extracted, so a shape introduced by
// a later Claude Code release still renders as something rather than vanishing.
type Block struct {
	Kind string // "text", "thinking", "tool_use", "tool_result", or whatever was found
	Text string
	Name string // tool name, for tool_use
}

// Stats reports what a walk saw.
type Stats struct {
	Lines   int
	Skipped int // lines that did not parse at all
}

// Summary is the identity-and-headline view of one transcript.
type Summary struct {
	Path        string
	SessionID   string
	Cwd         string
	FirstPrompt string
	First, Last time.Time
	Lines       int
	Skipped     int
}

// entry is the small, tolerant subset of a transcript line we depend on.
//
// Everything is optional. The format is internal to Claude Code and changes between
// releases, so a line carrying fields we have never seen must still parse - it
// simply contributes nothing.
type entry struct {
	Type      string `json:"type"`
	Cwd       string `json:"cwd"`
	SessionID string `json:"sessionId"`
	Timestamp string `json:"timestamp"` // kept as a string: an unexpected layout must
	IsMeta    bool   `json:"isMeta"`    // not make the whole line unreadable
	Message   struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

const maxPromptLen = 90

// utf8BOM is written as bytes rather than a "\u{feff}" escape because a literal BOM
// anywhere but the very start of a Go source file is a compile error, and the escape
// is easy to mangle when this file is edited by tooling.
var utf8BOM = string([]byte{0xEF, 0xBB, 0xBF})

// Walk streams a transcript, calling fn for every entry it can parse. Return false
// from fn to stop early.
//
// A line that does not parse is counted and skipped, never fatal: a killed session
// leaves a half-written final line, and refusing to read the 400 good entries before
// it would be the worse outcome.
func Walk(path string, fn func(Record) bool) (Stats, error) {
	var st Stats

	f, err := os.Open(path)
	if err != nil {
		return st, err
	}
	defer f.Close()

	// bufio.Scanner abandons any line over 64KB. Transcript lines routinely carry
	// whole files and tool output, so read with a Reader, which grows to fit.
	r := bufio.NewReaderSize(f, 256*1024)

	for {
		line, readErr := readLine(r)
		if line != "" || readErr == nil {
			st.Lines++
			if st.Lines == 1 {
				// A UTF-8 BOM would otherwise make the FIRST line unparseable - and
				// that is the line carrying cwd, so a single invisible marker would
				// cost the project's identity, not just one entry.
				line = strings.TrimPrefix(line, utf8BOM)
			}
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				var e entry
				if json.Unmarshal([]byte(trimmed), &e) != nil {
					st.Skipped++
				} else {
					rec := Record{
						Type:      e.Type,
						Role:      e.Message.Role,
						Text:      contentText(e.Message.Content),
						Blocks:    contentBlocks(e.Message.Content),
						Timestamp: parseTime(e.Timestamp),
						Cwd:       e.Cwd,
						SessionID: e.SessionID,
						IsMeta:    e.IsMeta,
						Line:      st.Lines,
					}
					if !fn(rec) {
						return st, nil
					}
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return st, nil
			}
			return st, readErr
		}
	}
}

// ScanAll reads the whole transcript.
func ScanAll(path string) (Summary, error) { return scan(path, 0) }

// ScanHead reads at most maxLines lines, stopping as soon as it has both the cwd and
// a first prompt.
func ScanHead(path string, maxLines int) (Summary, error) { return scan(path, maxLines) }

func scan(path string, maxLines int) (Summary, error) {
	s := Summary{Path: path}

	st, err := Walk(path, func(r Record) bool {
		if s.Cwd == "" && r.Cwd != "" {
			s.Cwd = r.Cwd
		}
		if s.SessionID == "" && r.SessionID != "" {
			s.SessionID = r.SessionID
		}
		if !r.Timestamp.IsZero() {
			if s.First.IsZero() || r.Timestamp.Before(s.First) {
				s.First = r.Timestamp
			}
			if r.Timestamp.After(s.Last) {
				s.Last = r.Timestamp
			}
		}
		if s.FirstPrompt == "" && r.Type == "user" && !r.IsMeta {
			s.FirstPrompt = truncate(r.Text, maxPromptLen)
		}
		if maxLines > 0 {
			if s.Cwd != "" && s.FirstPrompt != "" {
				return false
			}
			if r.Line >= maxLines {
				return false
			}
		}
		return true
	})

	s.Lines, s.Skipped = st.Lines, st.Skipped
	return s, err
}

// readLine returns one line without its terminator, however long it is.
func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}

func parseTime(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t
		}
	}
	return time.Time{}
}

// injected matches the blocks Claude Code splices into a user turn: slash-command
// plumbing, reminders, and captured command output.
//
// These are stripped rather than used to reject the whole message. A real question
// very often arrives with a reminder appended, and dropping the message entirely
// would leave the index showing nothing for a session the user definitely typed in.
// Go's regexp is RE2, which has no backreferences, so each pair is spelled out. The
// closed forms come first: alternation is leftmost-first, and an unterminated
// fallback listed earlier would swallow the rest of the message.
var injected = regexp.MustCompile(`(?s)` + strings.Join([]string{
	`<system-reminder>.*?</system-reminder>`,
	`<command-name>.*?</command-name>`,
	`<command-message>.*?</command-message>`,
	`<command-args>.*?</command-args>`,
	`<local-command-stdout>.*?</local-command-stdout>`,
	`<local-command-stderr>.*?</local-command-stderr>`,
	// Unterminated: a truncated or malformed block still should not leak markup
	// into an index entry.
	`<system-reminder>.*`,
	`<command-name>.*`,
	`<local-command-stdout>.*`,
}, "|"))

// contentText pulls readable text out of a message's content, which is either a
// plain string or an array of typed blocks.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var text string
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		text = asString
	} else {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &blocks) != nil {
			return ""
		}
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		text = strings.Join(parts, " ")
	}

	text = injected.ReplaceAllString(text, " ")
	return strings.TrimSpace(strings.Join(strings.Fields(text), " "))
}

// rawBlock is the tolerant view of one content block. Every field is optional.
type rawBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Thinking string          `json:"thinking"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
	Content  json.RawMessage `json:"content"`
}

// contentBlocks structures a message's content for rendering.
//
// A plain string becomes a single text block, which is how a simple user turn is
// stored. Anything whose shape is not recognised still yields a block carrying
// whatever text could be found, so a format change degrades the display rather than
// emptying it.
func contentBlocks(raw json.RawMessage) []Block {
	if len(raw) == 0 {
		return nil
	}

	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		if strings.TrimSpace(asString) == "" {
			return nil
		}
		return []Block{{Kind: "text", Text: asString}}
	}

	var raws []rawBlock
	if json.Unmarshal(raw, &raws) != nil {
		return nil
	}

	var out []Block
	for _, rb := range raws {
		switch rb.Type {
		case "thinking":
			if rb.Thinking != "" {
				out = append(out, Block{Kind: "thinking", Text: rb.Thinking})
			} else if rb.Text != "" {
				out = append(out, Block{Kind: "thinking", Text: rb.Text})
			}
		case "tool_use":
			out = append(out, Block{Kind: "tool_use", Name: rb.Name, Text: prettyJSON(rb.Input)})
		case "tool_result":
			out = append(out, Block{Kind: "tool_result", Text: flattenResult(rb.Content)})
		case "image":
			out = append(out, Block{Kind: "image", Text: "(image)"})
		default:
			text := rb.Text
			if text == "" {
				text = rb.Thinking
			}
			kind := rb.Type
			if kind == "" {
				kind = "text"
			}
			if text != "" {
				out = append(out, Block{Kind: kind, Text: text})
			}
		}
	}
	return out
}

// flattenResult reduces a tool result, which is a string or a list of blocks.
func flattenResult(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		return asString
	}
	var raws []rawBlock
	if json.Unmarshal(raw, &raws) != nil {
		return string(raw)
	}
	var parts []string
	for _, rb := range raws {
		if rb.Text != "" {
			parts = append(parts, rb.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// prettyJSON formats a tool's input readably, falling back to the raw bytes.
func prettyJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}

// truncate cuts to max BYTES but never mid-rune: slicing a UTF-8 string at an
// arbitrary offset splits multi-byte characters and prints a replacement character.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.ToValidUTF8(s[:cut], "") + "..."
}

// firstPrompt is retained for tests that exercise content extraction directly.
func firstPrompt(raw json.RawMessage) string {
	return truncate(contentText(raw), maxPromptLen)
}
