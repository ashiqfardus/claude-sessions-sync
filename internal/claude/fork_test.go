package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func entryLine(uuid, cwd string) string {
	return `{"type":"user","uuid":"` + uuid + `","cwd":"` + cwd + `","message":{"role":"user","content":"x"}}`
}

func writeTranscript(t *testing.T, name string, uuids ...string) string {
	t.Helper()
	return writeTranscriptAt(t, name, "/work/app", uuids...)
}

func writeTranscriptAt(t *testing.T, name, cwd string, uuids ...string) string {
	t.Helper()
	var lines []string
	for _, u := range uuids {
		lines = append(lines, entryLine(u, cwd))
	}
	// A trailing entry without a uuid, as Claude Code writes for titles and summaries.
	lines = append(lines, `{"type":"summary","summary":"done"}`)
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The 5:00 / 5:29 case: one machine kept working on a copy another machine had
// already continued, so each has turns the other lacks.
func TestCompareFindsASessionThatForked(t *testing.T) {
	archived := writeTranscript(t, "a.jsonl", "u1", "u2", "b1", "b2")
	local := writeTranscript(t, "l.jsonl", "u1", "u2", "a1")
	if got := Compare(local, archived); got != Forked {
		t.Fatalf("Compare = %v, want Forked", got)
	}
}

func TestCompareSeesAContinuation(t *testing.T) {
	archived := writeTranscript(t, "a.jsonl", "u1", "u2")
	local := writeTranscript(t, "l.jsonl", "u1", "u2", "u3")
	if got := Compare(local, archived); got != Extends {
		t.Fatalf("Compare = %v, want Extends", got)
	}
	if got := Compare(archived, local); got != Behind {
		t.Fatalf("Compare = %v, want Behind", got)
	}
}

// import rewrites paths, so a copy filed from the archive differs in bytes from it
// while holding exactly the same conversation.
func TestCompareIgnoresRewrittenPaths(t *testing.T) {
	archived := writeTranscriptAt(t, "a.jsonl", `E:\\web-app`, "u1", "u2")
	local := writeTranscriptAt(t, "l.jsonl", "/Users/me/Developer/NextJs Applications/web-app", "u1", "u2")
	if got := Compare(local, archived); got != Same {
		t.Fatalf("Compare = %v, want Same", got)
	}
}

// Claude Code ends most transcripts with entries that carry no uuid - titles, cost,
// the last prompt. A copy with more of them holds more, even with the same last turn.
func TestCompareCountsTrailingMetadata(t *testing.T) {
	plain := writeTranscriptAt(t, "a.jsonl", `E:\\x`, "u1", "u2")
	titled := filepath.Join(t.TempDir(), "b.jsonl")
	os.WriteFile(titled, []byte(mustReadFile(t, writeTranscriptAt(t, "c.jsonl", "/Users/me/x", "u1", "u2"))+
		`{"type":"custom-title","customTitle":"renamed","sessionId":"s"}`+"\n"), 0o644)
	if got := Compare(titled, plain); got != Extends {
		t.Fatalf("Compare(renamed, plain) = %v, want Extends", got)
	}
	if got := Compare(plain, titled); got != Behind {
		t.Fatalf("Compare(plain, renamed) = %v, want Behind", got)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A transcript with no uuids at all says nothing about how two copies relate.
func TestCompareIsUnknownWithoutUUIDs(t *testing.T) {
	a := filepath.Join(t.TempDir(), "a.jsonl")
	os.WriteFile(a, []byte(`{"type":"summary","summary":"x"}`+"\n"), 0o644)
	b := writeTranscript(t, "b.jsonl", "u1")
	if got := Compare(a, b); got != Unknown {
		t.Fatalf("Compare = %v, want Unknown", got)
	}
}

// HasUUID reads in 1 MB chunks; an entry straddling two reads must still be found.
func TestHasUUIDAcrossAChunkBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.jsonl")
	pad := `{"type":"x","text":"` + strings.Repeat("a", 1<<20-30) + `"}` + "\n"
	os.WriteFile(path, []byte(pad+entryLine("needle-uuid", "/w")+"\n"), 0o644)
	if !HasUUID(path, "needle-uuid") {
		t.Fatal("an entry split across two reads was missed")
	}
	if HasUUID(path, "absent-uuid") {
		t.Fatal("found a uuid that is not there")
	}
}

func TestForkIDIsStableAndAUUID(t *testing.T) {
	a := ForkID("70e28154-ccf7-420d-abc9-68b2c86e2241", "e--web-app")
	if a != ForkID("70e28154-ccf7-420d-abc9-68b2c86e2241", "e--web-app") {
		t.Fatal("ForkID is not deterministic, so a second import would file a second copy")
	}
	if a == ForkID("70e28154-ccf7-420d-abc9-68b2c86e2241", "other-bucket") {
		t.Fatal("two sources produced the same fork id")
	}
	if len(a) != 36 || a[14] != '4' || strings.Count(a, "-") != 4 {
		t.Fatalf("ForkID %q is not a version-4 UUID", a)
	}
}

func TestIsTranscriptID(t *testing.T) {
	for id, want := range map[string]bool{
		"11111111-2222-3333-4444-555555555555":                   true,
		"agent-a1b2c3d4":                                         true,
		"11111111-2222-3333-4444-555555555555 (conflicted copy)": false,
		"11111111-2222-3333-4444-555555555555 (1)":               false,
		"notes": false,
	} {
		if got := IsTranscriptID(id); got != want {
			t.Errorf("IsTranscriptID(%q) = %v, want %v", id, got, want)
		}
	}
}

// trailingNoTurns is what Claude Code appends to a session with no new turn, as on
// the first two-machine run: metadata lines with no uuid, a hook attachment that
// carries one (written on resume, never answered), and - should a later release do
// it - a metadata line with a uuid of its own.
const trailingNoTurns = `{"type":"last-prompt","lastPrompt":"go on","leafUuid":"u2","sessionId":"s"}
{"type":"cost-state","totalCostUSD":1.5,"sessionId":"s"}
{"type":"tools-used","tools":["Bash"],"sessionId":"s"}
{"type":"derived-from","sessions":["s"],"v":1,"sessionId":"s"}
{"type":"bridge-session","bridgeSessionId":"b","lastSequenceNum":3,"sessionId":"s"}
{"type":"attachment","uuid":"h1","parentUuid":"u2","attachment":{"type":"hook_success"},"sessionId":"s"}
{"type":"cost-state","uuid":"m1","totalCostUSD":1.6,"sessionId":"s"}
`

// A copy with the same turns plus trailing lines that add none is behind a copy with
// more turns, never forked from it (findings a and f).
func TestCompareIgnoresTrailingLinesThatAddNoTurn(t *testing.T) {
	stale := filepath.Join(t.TempDir(), "stale.jsonl")
	if err := os.WriteFile(stale, []byte(entryLine("u1", "/w")+"\n"+entryLine("u2", "/w")+"\n"+trailingNoTurns), 0o644); err != nil {
		t.Fatal(err)
	}
	ahead := writeTranscriptAt(t, "ahead.jsonl", `E:\\w`, "u1", "u2", "u3")
	if got := Compare(ahead, stale); got != Extends {
		t.Errorf("Compare(more turns, same turns + metadata) = %v, want Extends", got)
	}
	if got := Compare(stale, ahead); got != Behind {
		t.Errorf("Compare(same turns + metadata, more turns) = %v, want Behind", got)
	}
	if got := LastUUID(stale); got != "u2" {
		t.Errorf("LastUUID = %q, want the last turn u2", got)
	}
}

// system lines - a stop hook's summary - are part of the conversation and still count.
func TestCompareCountsSystemEntries(t *testing.T) {
	sys := `{"type":"system","subtype":"stop_hook_summary","uuid":"s9","parentUuid":"u2","sessionId":"s"}`
	a := filepath.Join(t.TempDir(), "a.jsonl")
	os.WriteFile(a, []byte(entryLine("u1", "/w")+"\n"+entryLine("u2", "/w")+"\n"+sys+"\n"), 0o644)
	b := writeTranscriptAt(t, "b.jsonl", `E:\\w`, "u1", "u2", "x3")
	if got := Compare(a, b); got != Forked {
		t.Errorf("Compare = %v, want Forked: each has an entry the other lacks", got)
	}
}

// A prompt queued and never run is user input: a copy that ends with one holds a turn
// a copy with other new turns lacks, so the two are forked, not one behind (task 4b).
// Other attachments still add no turn.
func TestCompareCountsAnUnrunQueuedPromptAsATurn(t *testing.T) {
	queued := `{"type":"attachment","uuid":"q1","parentUuid":"u2","attachment":{"type":"queued_command","prompt":"and then"},"sessionId":"s"}`
	a := filepath.Join(t.TempDir(), "a.jsonl")
	if err := os.WriteFile(a, []byte(entryLine("u1", "/w")+"\n"+entryLine("u2", "/w")+"\n"+queued+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := writeTranscriptAt(t, "b.jsonl", `E:\\w`, "u1", "u2", "b3", "b4")
	if got := Compare(a, b); got != Forked {
		t.Errorf("Compare(queued prompt, new turns) = %v, want Forked", got)
	}
	if got := Compare(b, a); got != Forked {
		t.Errorf("Compare(new turns, queued prompt) = %v, want Forked", got)
	}
}

// A copy holding only the other's last turn passes Compare, never HoldsAll.
func TestHoldsAllWantsEveryTurn(t *testing.T) {
	full := writeTranscript(t, "full.jsonl", "u1", "u2", "u3")
	moved := writeTranscriptAt(t, "moved.jsonl", `C:\\work\\app`, "u1", "u2", "u3", "u4")
	planted := writeTranscript(t, "planted.jsonl", "u3", "u9")
	if !HoldsAll(moved, full) {
		t.Error("a continuation with other paths does not hold the original")
	}
	if HoldsAll(full, moved) {
		t.Error("the original holds a turn it lacks")
	}
	if Compare(planted, full) != Extends {
		t.Fatal("setup: Compare should be fooled by the last uuid")
	}
	if HoldsAll(planted, full) {
		t.Error("a file holding only the last turn holds them all")
	}
	broken := filepath.Join(t.TempDir(), "broken.jsonl")
	os.WriteFile(broken, []byte(entryLine("u1", "/w")+"\n{not json\n"), 0o644)
	if HoldsAll(full, broken) {
		t.Error("an unreadable line was taken as nothing lost")
	}
}
