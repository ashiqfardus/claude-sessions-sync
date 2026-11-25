package claude

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLastTurnTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The turns, then what Claude Code appends that is no turn: a hook attachment with
	// a uuid, a title and the last prompt - each dated later.
	write(`{"type":"user","uuid":"u1","timestamp":"2026-03-01T10:00:00.000Z","message":{"role":"user","content":"hi"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-03-01T10:00:05.250Z","message":{"role":"assistant","content":"hello"}}
{"type":"attachment","uuid":"h1","timestamp":"2026-08-01T00:00:00.000Z","attachment":{"type":"hook_success"}}
{"type":"summary","summary":"done","timestamp":"2026-09-01T00:00:00.000Z"}
{"type":"last-prompt","lastPrompt":"hi","timestamp":"2026-09-02T00:00:00.000Z"}
`)
	got, ok := LastTurnTime(path)
	if want := time.Date(2026, 3, 1, 10, 0, 5, 250e6, time.UTC); !ok || !got.Equal(want) {
		t.Errorf("LastTurnTime = %v %v, want %v", got, ok, want)
	}

	write(`{"type":"summary","summary":"done","timestamp":"2026-09-01T00:00:00.000Z"}` + "\n")
	if got, ok := LastTurnTime(path); ok {
		t.Errorf("no turns: LastTurnTime = %v, want none", got)
	}
	// The last turn carries no time: an earlier turn's would make it look older.
	write(`{"type":"user","uuid":"u1","timestamp":"2026-03-01T10:00:00.000Z","message":{"role":"user","content":"hi"}}
{"type":"assistant","uuid":"a1","message":{"role":"assistant","content":"hello"}}
`)
	if got, ok := LastTurnTime(path); ok {
		t.Errorf("undated last turn: LastTurnTime = %v, want none", got)
	}
	if _, ok := LastTurnTime(filepath.Join(t.TempDir(), "missing.jsonl")); ok {
		t.Error("a missing file has a last turn")
	}
}
