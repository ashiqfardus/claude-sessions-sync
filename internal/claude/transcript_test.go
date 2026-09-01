package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// A transcript that ends mid-line is what a killed session leaves behind. The
// reader must count the bad line and still return everything before it - refusing
// to read 400 good entries because of one truncated tail is the worse outcome.
func TestScanTruncatedFinalLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	content := strings.Join([]string{
		`{"type":"user","cwd":"E:\\airos-frontend","sessionId":"abc","timestamp":"2026-08-31T10:00:00.000Z","message":{"role":"user","content":"fix the build"}}`,
		`{"type":"assistant","timestamp":"2026-08-31T10:00:05.000Z","message":{"role":"assistant","content":[{"type":"text","text":"on it"}]}}`,
		`{"type":"user","timestamp":"2026-08-31T10:0`, // killed mid-write
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := ScanAll(path)
	if err != nil {
		t.Fatalf("a truncated line must not be fatal: %v", err)
	}
	if s.Cwd != `E:\airos-frontend` {
		t.Errorf("cwd = %q, want E:\\airos-frontend", s.Cwd)
	}
	if s.FirstPrompt != "fix the build" {
		t.Errorf("firstPrompt = %q", s.FirstPrompt)
	}
	if s.Skipped != 1 {
		t.Errorf("skipped = %d, want 1", s.Skipped)
	}
	if s.SessionID != "abc" {
		t.Errorf("sessionId = %q", s.SessionID)
	}
}

// An entry shape we have never seen must parse and contribute nothing. The format
// is internal to Claude Code and changes between releases; an upgrade must not turn
// the archive unreadable.
func TestScanUnknownEntryShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	content := strings.Join([]string{
		`{"type":"some-future-thing","payload":{"nested":{"deeply":[1,2,3]}},"cwd":"/home/a/dev/x"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`,
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := ScanAll(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Skipped != 0 {
		t.Errorf("a well-formed unknown shape is not a skip: got %d", s.Skipped)
	}
	if s.Cwd != "/home/a/dev/x" {
		t.Errorf("cwd = %q", s.Cwd)
	}
	if s.FirstPrompt != "hello" {
		t.Errorf("firstPrompt = %q", s.FirstPrompt)
	}
}

// Lines far larger than bufio.Scanner's 64KB ceiling are routine here: tool results
// carry whole files.
func TestScanVeryLongLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	huge := strings.Repeat("x", 300_000)
	content := `{"type":"user","cwd":"/tmp/p","message":{"role":"user","content":"` + huge + `"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"ok"}}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := ScanAll(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Lines != 2 {
		t.Errorf("lines = %d, want 2 - a long line must not truncate the read", s.Lines)
	}
	if s.Skipped != 0 {
		t.Errorf("skipped = %d, want 0", s.Skipped)
	}
}

func TestFirstPromptSkipsMachineText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain string", `"just do it"`, "just do it"},
		{"block array", `[{"type":"text","text":"line one"},{"type":"text","text":"line two"}]`, "line one line two"},
		{"whitespace collapsed", `"  a\n\n   b  "`, "a b"},
		{"slash command", `"<command-name>/loop</command-name>"`, ""},
		{"system reminder", `"<system-reminder>ignore me</system-reminder>"`, ""},
		{"empty", `""`, ""},
		{"tool result block", `[{"type":"tool_result","text":""}]`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstPrompt([]byte(tc.in)); got != tc.want {
				t.Errorf("firstPrompt(%s) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFirstPromptTruncates(t *testing.T) {
	long := `"` + strings.Repeat("a", 200) + `"`
	got := firstPrompt([]byte(long))
	if len(got) != maxPromptLen+3 || !strings.HasSuffix(got, "...") {
		t.Errorf("got %d chars, want %d plus an ellipsis", len(got), maxPromptLen)
	}
}

// Real-world encodings: a UTF-8 BOM plus CRLF line endings, from testdata rather
// than an inline string so the exact bytes are what is under test.
//
// The BOM matters more than it looks: it sits on the FIRST line, which is the line
// carrying cwd, so failing to strip it costs the project's identity - not merely one
// unreadable entry.
func TestScanHandlesBOMAndCRLF(t *testing.T) {
	s, err := ScanAll(filepath.Join("testdata", "bom-crlf.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Skipped != 0 {
		t.Errorf("skipped = %d, want 0 - a BOM must not make a line unreadable", s.Skipped)
	}
	if s.Cwd != `/home/me/dev/api` {
		t.Errorf("cwd = %q - the BOM line carries the identity", s.Cwd)
	}
	if s.SessionID != "bom-crlf-fixture" {
		t.Errorf("sessionId = %q", s.SessionID)
	}
	if s.FirstPrompt != "does a BOM break the reader" {
		t.Errorf("firstPrompt = %q", s.FirstPrompt)
	}
	if s.Lines != 2 {
		t.Errorf("lines = %d, want 2", s.Lines)
	}
}

// A user turn very often arrives with a reminder appended. Dropping the whole
// message would leave the index blank for a session the user definitely typed in.
func TestFirstPromptKeepsHumanTextAroundInjectedBlocks(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"reminder appended",
			`"deploy the staging branch<system-reminder>Do not do X</system-reminder>"`,
			"deploy the staging branch",
		},
		{
			"reminder wrapped around",
			`"<system-reminder>ignore</system-reminder>what changed in auth?<system-reminder>also ignore</system-reminder>"`,
			"what changed in auth?",
		},
		{
			"slash command alone still yields nothing",
			`"<command-name>/loop</command-name><command-args>5m</command-args>"`,
			"",
		},
		{
			"unterminated block does not leak markup",
			`"real question<system-reminder>truncated..."`,
			"real question",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstPrompt([]byte(tc.in)); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWalkStopsEarly(t *testing.T) {
	seen := 0
	_, err := Walk(filepath.Join("testdata", "bom-crlf.jsonl"), func(Record) bool {
		seen++
		return false
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Errorf("callback ran %d times, want 1 - returning false must stop the walk", seen)
	}
}

// Slicing a UTF-8 string at a byte offset splits multi-byte characters, which shows
// up as a replacement character mid-word in listings and search snippets.
func TestTruncateDoesNotSplitRunes(t *testing.T) {
	// A star lands exactly on the cut boundary.
	s := strings.Repeat("a", maxPromptLen-1) + "★ and more text after the cut"
	got := truncate(s, maxPromptLen)

	if !utf8.ValidString(got) {
		t.Errorf("truncate produced invalid UTF-8: %q", got)
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Errorf("truncate produced a replacement character: %q", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("expected an ellipsis, got %q", got)
	}
}
