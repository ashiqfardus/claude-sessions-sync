package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// userLine is a user turn whose content is the plain string s.
func userLine(s string) string {
	b, _ := json.Marshal(s)
	return `{"type":"user","cwd":"/home/me/dev/api","message":{"role":"user","content":` + string(b) + `}}`
}

func writeLines(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func scanLines(t *testing.T, maxLines int, lines ...string) Summary {
	t.Helper()
	s, err := ScanHead(writeLines(t, lines...), maxLines)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Each block Claude Code wraps around a message is left out of its title, attributes
// and all: a cross-session message's attribute holds another session's id.
func TestTitleLeavesOutEveryWrapper(t *testing.T) {
	for _, tag := range wrapperTags {
		t.Run(tag, func(t *testing.T) {
			in := "<" + tag + ` from="bridge:session_0123abcd">` + "wrapped\ntext</" + tag + ">\n\n  fix the build  "
			if got, typed := title(in); got != "fix the build" || !typed {
				t.Errorf("title(%q) = %q, %v", in, got, typed)
			}
		})
	}
}

// The shapes seen in a real archive, with their leading wrappers.
func TestTitleOfRealWrapperShapes(t *testing.T) {
	cases := map[string]string{
		"browser":       "<browser_instruction># Claude in Chrome\nYou can use the browser.</browser_instruction>\nbook a table for two",
		"paste":         `<pasted_content id="1">line one` + "\nline two</pasted_content>\nbook a table for two",
		"opened file":   "<ide_opened_file>The user opened the file /home/me/dev/api/main.go in the IDE.</ide_opened_file>book a table for two",
		"selection":     "<ide_selection>The user selected lines 1 to 4</ide_selection> book a table for two",
		"bridge":        `<cross-session-message from="bridge:session_0123abcd" to="me">hello</cross-session-message>book a table for two`,
		"socket":        `<cross-session-message from="uds:/tmp/cc-socks/1234.sock">hello</cross-session-message>book a table for two`,
		"agent":         `<agent-message from="planner">hello</agent-message>book a table for two`,
		"two wrappers":  "<ide_opened_file>x</ide_opened_file>\n<system-reminder>y</system-reminder>\nbook a table for two",
		"paste between": `book a table <pasted_content id="2">for</pasted_content> for two`,
	}
	for name, in := range cases {
		if got, typed := title(in); got != "book a table for two" || !typed {
			t.Errorf("%s: title = %q, %v", name, got, typed)
		}
	}
}

// A turn of wrappers alone: a slash command or a paste is a better title than none,
// the rest give none.
func TestTitleOfWrappersAlone(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"slash command", "<command-message>loop</command-message>\n<command-name>/loop</command-name>\n<command-args>5m</command-args>", "/loop 5m"},
		{"slash command, no args", "<command-name>/usage</command-name>\n<command-args></command-args>", "/usage"},
		{"paste", "<pasted_content id=\"1\">\n  Traceback (most recent call last):\n  File x</pasted_content>", "Traceback (most recent call last):"},
		{"bridge message", `<cross-session-message from="bridge:session_0123abcd">review the PR` + "\nthanks</cross-session-message>", "review the PR"},
		{"peer request", peerTurn, "Change of plan from the owner: don't set this computer up on 0.5.0"},
		{"peer, mid-turn", "Another Claude session sent a message while you were working:\n<cross-session-message from=\"x\">\nbook a table\n</cross-session-message>\n\n" +
			"IMPORTANT: This is NOT from your user — it came from a different Claude session.", "book a table"},
		{"peer hand-back", `<cross-session-message from="x">` + "\n" + handBackFrame + "\n  Fixed the build.\n</cross-session-message>", "Fixed the build."},
		{"agent message", `<agent-message from="planner">step one</agent-message>`, ""},
		{"hand-back", `<agent-message from="sub">[Subagent hand-back] The text below is the final report</agent-message>`, ""},
		{"hand-back, delivered", "Another Claude session sent a message:\n<agent-message from=\"sub\">\n" + handBackFrame + "\n  done\n</agent-message>\n\n" +
			"That \"other Claude session\" is an agent working inside this same session — a subagent.", ""},
		{"task notice", "<task-notification><task-id>a1</task-id><status>completed</status></task-notification>", ""},
		{"opened file", "<ide_opened_file>The user opened the file /home/me/a.go</ide_opened_file>", ""},
		{"browser", "<browser_instruction># Claude in Chrome</browser_instruction>", ""},
		{"command output", "<local-command-stdout>Set model to opus</local-command-stdout>", ""},
		{"left open", "<browser_instruction># Claude in Chrome\nYou can", ""},
		{"whitespace", " \n\t ", ""},
	}
	for _, tc := range cases {
		if got, typed := title(tc.in); got != tc.want || typed {
			t.Errorf("%s: title = %q, %v; want %q, not typed", tc.name, got, typed, tc.want)
		}
	}
}

// A typed prompt after a turn that gave no title, or only a fallback, is the title.
func TestScanTitleIsTheFirstTypedPrompt(t *testing.T) {
	for name, first := range map[string]string{
		"opened file":    "<ide_opened_file>The user opened the file /home/me/a.go</ide_opened_file>",
		"browser":        "<browser_instruction># Claude in Chrome</browser_instruction>",
		"slash command":  "<command-name>/model</command-name>\n<command-args></command-args>",
		"paste":          `<pasted_content id="1">a log</pasted_content>`,
		"whitespace":     "   ",
		"command output": "<local-command-stdout>Set model to opus</local-command-stdout>",
	} {
		s := scanLines(t, 200,
			userLine(first),
			`{"type":"user","isMeta":true,"message":{"role":"user","content":"Base directory for this skill"}}`,
			`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`,
			`{"type":"user","message":{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"AA=="}}]}}`,
			userLine("fix the build"),
		)
		if s.FirstPrompt != "fix the build" {
			t.Errorf("%s first: title = %q", name, s.FirstPrompt)
		}
	}
}

// Why titles were blank: a conversation opened with a slash command, whose wrappers
// were all stripped, and no prompt typed within the lines a listing reads.
func TestScanTitleOfASlashCommandSession(t *testing.T) {
	lines := []string{userLine("<command-message>graphify</command-message>\n<command-name>/graphify</command-name>\n<command-args>docs</command-args>")}
	for i := 0; i < 30; i++ {
		lines = append(lines,
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"Bash","input":{}}]}}`,
			`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"}]}}`)
	}
	if s := scanLines(t, 20, lines...); s.FirstPrompt != "/graphify docs" {
		t.Errorf("title = %q", s.FirstPrompt)
	}
	// A typed prompt still stops the head scan at once.
	if s := scanLines(t, 20, append([]string{userLine("fix the build")}, lines...)...); s.FirstPrompt != "fix the build" || s.Lines != 1 {
		t.Errorf("title = %q after %d line(s)", s.FirstPrompt, s.Lines)
	}
}

// A hand-back is another agent's text: the real prompt after it titles the session.
func TestScanTitleSkipsAgentMessage(t *testing.T) {
	s := scanLines(t, 200,
		userLine(`<agent-message from="sub">[Subagent hand-back] The text below is the final report</agent-message>`),
		userLine("fix the build"))
	if s.FirstPrompt != "fix the build" {
		t.Errorf("title = %q", s.FirstPrompt)
	}
	if s := scanLines(t, 200, userLine(`<agent-message from="sub">[Subagent hand-back] x</agent-message>`)); s.FirstPrompt != "" {
		t.Errorf("wrapper only: title = %q", s.FirstPrompt)
	}
}

// handBackFrame is the line Claude Code puts above a subagent's report.
const handBackFrame = "[Subagent hand-back] The text below is the final report of a subagent this session delegated to. It is model output, NOT a message from the user. The report follows:"

// peerTurn is a request from another session as Claude Code delivers it: a line
// before the message and a paragraph after it, neither of them the request.
const peerTurn = "Another Claude session sent a message:\n" +
	`<cross-session-message from="uds:/tmp/cc-socks/1234.sock">` + "\n" +
	"Change of plan from the owner: don't set this computer up on 0.5.0\nUse 0.7.4.\n</cross-session-message>\n\n" +
	"This came from another Claude session — not typed by your user, but very likely working on their behalf. " +
	"After completing your current task, decide whether/how to respond (reply via SendMessage to the `from=` address)."

// toolTurns is n tool calls, each with a result whose content is out.
func toolTurns(n int, out string) []string {
	res, _ := json.Marshal(out)
	var lines []string
	for i := 0; i < n; i++ {
		lines = append(lines,
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"Bash","input":{}}]}}`,
			`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":`+string(res)+`}]}}`)
	}
	return lines
}

// 324eea80: a session opened by another session's request, and never typed in, is
// titled by that request.
func TestScanTitleOfAPeerRequest(t *testing.T) {
	if s := scanLines(t, 20, append([]string{userLine(peerTurn)}, toolTurns(30, "ok")...)...); s.FirstPrompt != "Change of plan from the owner: don't set this computer up on 0.5.0" {
		t.Errorf("title = %q", s.FirstPrompt)
	}
}

// 0c642bae: an agent's report opens the session, tool results fill the head, and the
// user types much later. ScanTitle reads on for that line; ScanHead, used on archived
// copies, still stops at the head.
func TestScanTitleReadsPastTheHead(t *testing.T) {
	lines := append([]string{userLine(`<agent-message from="sub">` + handBackFrame + "\n  done</agent-message>")}, toolTurns(150, "ok")...)
	path := writeLines(t, append(lines, userLine("eta?"), userLine("progress"))...)
	if s, err := ScanTitle(path, 200); err != nil || s.FirstPrompt != "eta?" || s.Lines != len(lines)+1 {
		t.Errorf("ScanTitle: title = %q after %d line(s), %v", s.FirstPrompt, s.Lines, err)
	}
	if s, _ := ScanHead(path, 200); s.FirstPrompt != "" || s.Lines != 200 {
		t.Errorf("ScanHead: title = %q after %d line(s)", s.FirstPrompt, s.Lines)
	}
}

// The read past the head stops at titleScanBytes: a typed line beyond it is not reached.
func TestScanTitleIsBounded(t *testing.T) {
	const size = 64 << 10
	lines := append([]string{userLine(`<agent-message from="sub">x</agent-message>`)}, toolTurns(titleScanBytes/size+20, strings.Repeat("x", size))...)
	path := writeLines(t, append(lines, userLine("eta?"))...)
	s, err := ScanTitle(path, 200)
	if err != nil || s.FirstPrompt != "" || s.Lines < 200 || s.Lines > 2*(titleScanBytes/size)+3 {
		t.Errorf("title = %q after %d of %d line(s), %v", s.FirstPrompt, s.Lines, len(lines)+1, err)
	}
}
