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
	archived := writeTranscriptAt(t, "a.jsonl", `E:\\airos-frontend`, "u1", "u2")
	local := writeTranscriptAt(t, "l.jsonl", "/Users/me/Developer/NextJs Applications/airos-frontend", "u1", "u2")
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

// hasUUID reads in 1 MB chunks; an entry straddling two reads must still be found.
func TestHasUUIDAcrossAChunkBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.jsonl")
	pad := `{"type":"x","text":"` + strings.Repeat("a", 1<<20-30) + `"}` + "\n"
	os.WriteFile(path, []byte(pad+entryLine("needle-uuid", "/w")+"\n"), 0o644)
	if !hasUUID(path, "needle-uuid") {
		t.Fatal("an entry split across two reads was missed")
	}
	if hasUUID(path, "absent-uuid") {
		t.Fatal("found a uuid that is not there")
	}
}

func TestForkIDIsStableAndAUUID(t *testing.T) {
	a := ForkID("70e28154-ccf7-420d-abc9-68b2c86e2241", "e--airos-frontend")
	if a != ForkID("70e28154-ccf7-420d-abc9-68b2c86e2241", "e--airos-frontend") {
		t.Fatal("ForkID is not deterministic, so a second import would file a second copy")
	}
	if a == ForkID("70e28154-ccf7-420d-abc9-68b2c86e2241", "other-bucket") {
		t.Fatal("two sources produced the same fork id")
	}
	if len(a) != 36 || a[14] != '4' || strings.Count(a, "-") != 4 {
		t.Fatalf("ForkID %q is not a version-4 UUID", a)
	}
}
