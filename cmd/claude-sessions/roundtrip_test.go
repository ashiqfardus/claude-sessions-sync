package main_test

// Round-trip fidelity: what comes back must equal what went in.
//
// Forty-odd tests passed while import silently dropped every memory file and reset
// every timestamp, because each one checked placement or rewriting in isolation and
// none compared the result against the source. This is that comparison.

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestRoundTripPreservesEverything(t *testing.T) {
	source := newFixture(t)

	// Give the source files distinct, old timestamps so "preserved" cannot be
	// confused with "happens to be recent".
	transcript := filepath.Join(source.claudeDir, "projects", "e--work-airos-frontend",
		"11111111-2222-3333-4444-555555555555.jsonl")
	memory := filepath.Join(source.claudeDir, "projects", "e--work-legacy-app", "memory", "MEMORY.md")
	want := time.Date(2026, 3, 14, 9, 26, 53, 0, time.Local)
	for _, p := range []string{transcript, memory} {
		if err := os.Chtimes(p, want, want); err != nil {
			t.Fatal(err)
		}
	}

	if out, err := source.push(t); err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}

	// A second machine with the same layout, so pull applies and nothing is rewritten.
	target := filepath.Join(t.TempDir(), ".claude")
	mustMkdir(t, filepath.Join(target, "projects"))

	cmd := exec.Command(binary, "pull", "--claude-dir", target, "--archive", source.archive)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pull failed: %v\n%s", err, out)
	}

	// --- every file that went in comes back ---------------------------------
	srcFiles := relativeFiles(t, filepath.Join(source.claudeDir, "projects"))
	gotFiles := relativeFiles(t, filepath.Join(target, "projects"))
	if strings.Join(srcFiles, "\n") != strings.Join(gotFiles, "\n") {
		t.Errorf("round trip lost or added files.\nwent in:\n  %s\ncame back:\n  %s\n\n%s",
			strings.Join(srcFiles, "\n  "), strings.Join(gotFiles, "\n  "), out)
	}

	// --- contents are byte-identical ----------------------------------------
	for _, rel := range srcFiles {
		a := mustRead(t, filepath.Join(source.claudeDir, "projects", rel))
		b := mustRead(t, filepath.Join(target, "projects", rel))
		if a != b {
			t.Errorf("%s changed in the round trip", rel)
		}
	}

	// --- timestamps survive --------------------------------------------------
	// A restored session stamped "now" makes every conversation look like it happened
	// today, and makes the next push re-upload the entire archive.
	for _, rel := range srcFiles {
		srcInfo, err := os.Stat(filepath.Join(source.claudeDir, "projects", rel))
		if err != nil {
			t.Fatal(err)
		}
		dstInfo, err := os.Stat(filepath.Join(target, "projects", rel))
		if err != nil {
			t.Fatal(err)
		}
		if diff := dstInfo.ModTime().Sub(srcInfo.ModTime()); diff > time.Second || diff < -time.Second {
			t.Errorf("%s: mtime %v, want %v (off by %v)",
				rel, dstInfo.ModTime(), srcInfo.ModTime(), diff)
		}
	}
}

// The same guarantee through import, which rewrites the transcript rather than
// copying it verbatim - so contents legitimately differ, but nothing may be lost.
func TestImportBringsBackMemoryAndTimestamps(t *testing.T) {
	f := newImportFixture(t)
	local := f.makeRepo(t, "api", "git@github.com:example/api.git")

	f.archiveProject(t, "D--work-api", `D:\work\api`,
		"11111111-1111-1111-1111-111111111111", "fix the retry logic")
	mustWrite(t, filepath.Join(f.archive, "projects", "D--work-api", "memory", "MEMORY.md"),
		"- the retry budget is 3\n")
	f.writeManifest(t, map[string]any{
		"D--work-api": map[string]any{
			"path": `D:\work\api`, "leaf": "api",
			"remote": "git@github.com:example/api.git", "seen": "2026-08-30",
		},
	})

	archivedTranscript := filepath.Join(f.archive, "projects", "D--work-api",
		"11111111-1111-1111-1111-111111111111.jsonl")
	archivedMemory := filepath.Join(f.archive, "projects", "D--work-api", "memory", "MEMORY.md")
	want := time.Date(2026, 5, 2, 14, 5, 0, 0, time.Local)
	for _, p := range []string{archivedTranscript, archivedMemory} {
		if err := os.Chtimes(p, want, want); err != nil {
			t.Fatal(err)
		}
	}

	out, err := f.run(t, "import", "--search-root", f.dev)
	if err != nil {
		t.Fatalf("import failed: %v\n%s", err, out)
	}

	bucket := filepath.Join(f.claudeDir, "projects", slugOf(local))

	// Memory: archived by push, and it must come back or the project arrives on the
	// new machine having forgotten everything it knew.
	restoredMemory := filepath.Join(bucket, "memory", "MEMORY.md")
	body, err := os.ReadFile(restoredMemory)
	if err != nil {
		t.Fatalf("memory was not restored: %v\n%s", err, out)
	}
	if !strings.Contains(string(body), "the retry budget is 3") {
		t.Errorf("memory content wrong: %q", body)
	}

	// Timestamps on both.
	for _, p := range []string{
		filepath.Join(bucket, "11111111-1111-1111-1111-111111111111.jsonl"),
		restoredMemory,
	} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if diff := info.ModTime().Sub(want); diff > time.Second || diff < -time.Second {
			t.Errorf("%s: mtime %v, want %v", filepath.Base(p), info.ModTime(), want)
		}
	}
}

// Importing twice must not re-copy memory that has not changed.
func TestImportDoesNotRewriteUnchangedMemory(t *testing.T) {
	f := newImportFixture(t)
	f.makeRepo(t, "api", "git@github.com:example/api.git")
	f.archiveProject(t, "D--work-api", `D:\work\api`, "22222222-2222-2222-2222-222222222222", "hello")
	mustWrite(t, filepath.Join(f.archive, "projects", "D--work-api", "memory", "MEMORY.md"), "- note\n")
	f.writeManifest(t, map[string]any{
		"D--work-api": map[string]any{
			"path": `D:\work\api`, "leaf": "api",
			"remote": "git@github.com:example/api.git", "seen": "2026-08-30",
		},
	})

	if out, err := f.run(t, "import", "--search-root", f.dev); err != nil {
		t.Fatalf("first import failed: %v\n%s", err, out)
	}
	out, err := f.run(t, "import", "--search-root", f.dev)
	if err != nil {
		t.Fatalf("second import failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "1 memory file(s)") {
		t.Errorf("unchanged memory should not be copied again:\n%s", out)
	}
}

func relativeFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr == nil {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
