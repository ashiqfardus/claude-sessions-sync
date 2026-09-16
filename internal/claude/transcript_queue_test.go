package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func walkAll(t *testing.T, lines ...string) []Record {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out []Record
	if _, err := Walk(path, func(r Record) bool { out = append(out, r); return true }); err != nil {
		t.Fatal(err)
	}
	return out
}

// Shapes copied from a real Claude Code 2.1.251 transcript.
func TestWalkReadsPromptsQueuedWhileBusy(t *testing.T) {
	recs := walkAll(t,
		`{"type":"attachment","timestamp":"2026-09-01T07:46:52.571Z","attachment":{"type":"queued_command","prompt":"readme needs to be step by step guide for user","commandMode":"prompt","origin":{"kind":"human"}}}`,
		`{"type":"attachment","attachment":{"type":"queued_command","prompt":"<task-notification>done</task-notification>","commandMode":"task-notification"}}`,
	)
	if recs[0].Type != "user" || recs[0].Role != "user" || recs[0].Text != "readme needs to be step by step guide for user" {
		t.Errorf("queued prompt = %+v", recs[0])
	}
	if len(recs[0].Blocks) != 1 || recs[0].Blocks[0].Kind != "text" {
		t.Errorf("queued prompt blocks = %+v", recs[0].Blocks)
	}
	// Background task notifications are not something the user typed.
	if recs[1].Type == "user" || recs[1].Text != "" {
		t.Errorf("a task notification was read as a user prompt: %+v", recs[1])
	}
}

func TestInjectedBlocksAreStrippedOnlyWhereInjected(t *testing.T) {
	recs := walkAll(t,
		// An assistant explaining the tag must keep everything after it.
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Slash commands arrive as <command-name> blocks, then the rest of the answer."}]}}`,
		// Tool output carries reminders too, and they used to reach the pages.
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"file contents<system-reminder>internal note</system-reminder>"}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":[{"type":"image","source":{}},{"type":"text","text":"caption"}]}]}}`,
	)
	if !strings.Contains(recs[0].Blocks[0].Text, "the rest of the answer") {
		t.Errorf("assistant text was cut: %q", recs[0].Blocks[0].Text)
	}
	if got := recs[1].Blocks[0].Text; strings.Contains(got, "internal note") || !strings.Contains(got, "file contents") {
		t.Errorf("tool result = %q", got)
	}
	if got := recs[2].Blocks[0].Text; got != "(image)\ncaption" {
		t.Errorf("image result = %q", got)
	}
}
