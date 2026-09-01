package claude

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"
)

// Summary is what a scan of one transcript yields.
type Summary struct {
	Path        string
	SessionID   string
	Cwd         string    // the project's real absolute path, as Claude recorded it
	FirstPrompt string    // first human-authored message, for a readable index
	First       time.Time // earliest entry timestamp seen
	Last        time.Time // latest entry timestamp seen
	Lines       int       // lines read
	Skipped     int       // lines that did not parse, or that we did not recognise
}

// entry is the small, tolerant subset of a transcript line we depend on.
//
// Everything is optional and nothing is required to be present. The format is
// internal to Claude Code and changes between releases, so a line carrying fields
// we have never seen must still parse - it simply contributes nothing.
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

// maxPromptLen keeps the index readable on a phone.
const maxPromptLen = 90

// ScanAll reads the whole transcript. Use ScanHead when only identity and the first
// prompt are needed - it stops as soon as it has them.
func ScanAll(path string) (Summary, error) { return scan(path, 0) }

// ScanHead reads at most maxLines lines, stopping early once both the cwd and a
// first prompt are known.
func ScanHead(path string, maxLines int) (Summary, error) { return scan(path, maxLines) }

func scan(path string, maxLines int) (Summary, error) {
	s := Summary{Path: path}

	f, err := os.Open(path)
	if err != nil {
		return s, err
	}
	defer f.Close()

	// A bufio.Scanner caps a line at 64KB by default and gives up on anything longer.
	// Transcript lines routinely carry whole file contents and tool output, so read
	// with a Reader instead: an enormous line is normal here, not an error.
	r := bufio.NewReaderSize(f, 256*1024)

	for {
		if maxLines > 0 && s.Lines >= maxLines {
			break
		}
		line, err := readLine(r)
		if len(line) == 0 && err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return s, err
		}
		s.Lines++

		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		var e entry
		if json.Unmarshal([]byte(trimmed), &e) != nil {
			// A killed session leaves a half-written final line. Count it and move on;
			// refusing to read the other 400 entries would be the worse outcome.
			s.Skipped++
			if errors.Is(err, io.EOF) {
				break
			}
			continue
		}

		if s.Cwd == "" && e.Cwd != "" {
			s.Cwd = e.Cwd
		}
		if s.SessionID == "" && e.SessionID != "" {
			s.SessionID = e.SessionID
		}
		if ts := parseTime(e.Timestamp); !ts.IsZero() {
			if s.First.IsZero() || ts.Before(s.First) {
				s.First = ts
			}
			if ts.After(s.Last) {
				s.Last = ts
			}
		}
		if s.FirstPrompt == "" && e.Type == "user" && !e.IsMeta {
			s.FirstPrompt = firstPrompt(e.Message.Content)
		}

		if maxLines > 0 && s.Cwd != "" && s.FirstPrompt != "" {
			break
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}

	return s, nil
}

// readLine returns one line without its terminator, however long it is.
//
// bufio.Reader.ReadString grows its own buffer to fit, unlike bufio.Scanner, which
// abandons any line over 64KB - and transcript lines routinely exceed that.
func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}

// parseTime accepts the layouts transcripts have used, and gives up quietly.
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

// firstPrompt extracts readable text from a message's content, which is either a
// plain string or an array of typed blocks.
//
// Slash commands and injected reminders are skipped: they are not what the human
// typed, and an index full of "<command-name>" tells the reader nothing.
func firstPrompt(raw json.RawMessage) string {
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

	text = strings.TrimSpace(strings.Join(strings.Fields(text), " "))
	if text == "" {
		return ""
	}
	for _, marker := range []string{"<command-name", "<local-command", "<system-reminder"} {
		if strings.Contains(text, marker) {
			return ""
		}
	}
	if len(text) > maxPromptLen {
		return text[:maxPromptLen] + "..."
	}
	return text
}
