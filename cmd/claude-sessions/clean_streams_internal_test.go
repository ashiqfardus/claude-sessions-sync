package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
)

// clean, prune and backup, which the panel runs from concurrent requests, print only on
// their own streams, clean's progress included, and return what they did.
func TestCleanPruneBackupWriteOnlyToTheirStreams(t *testing.T) {
	t.Setenv("CLAUDE_SESSIONS_MACHINE", "LAP-1")
	root := t.TempDir()
	claudeDir, dest := filepath.Join(root, ".claude"), filepath.Join(root, "archive")
	writeFile(t, filepath.Join(claudeDir, "settings.json"), "{}")
	// B's copy is held in whole by A's: clean deletes it. LAP-1 leaves held copies alone
	// and honours the deleted list, so neither waits for it.
	turns := `{"type":"user","uuid":"u1","timestamp":"2026-08-30T09:00:00.000Z"}` + "\n" +
		`{"type":"assistant","uuid":"m1","timestamp":"2026-08-30T09:00:01.000Z"}` + "\n"
	writeFile(t, filepath.Join(archive.ProjectsDir(dest), "A", cleanID+".jsonl"),
		turns+`{"type":"assistant","uuid":"m2","timestamp":"2026-08-30T09:00:02.000Z"}`+"\n")
	writeFile(t, filepath.Join(archive.ProjectsDir(dest), "B", cleanID+".jsonl"), turns)
	writeFile(t, filepath.Join(archive.ManifestDir(dest), "LAP-1.json"),
		`{"machine":"LAP-1","skipsHeld":true,"honorsDeleted":true,"projects":{"A":{"path":"/x/a"},"B":{"path":"/x/b"}}}`)

	var out, errs bytes.Buffer
	s := streams{&out, &errs}
	var cleaned cleanResult
	var listed, pruned, undone pruneResult
	var backedUp backupResult
	stdout, stderr := captureBoth(t, func() error {
		var err error
		if cleaned, err = runClean(s, cleanOpts{ClaudeDir: claudeDir, ArchiveDir: dest, Yes: true}); err != nil {
			return err
		}
		if listed, err = runPrune(s, pruneOpts{ClaudeDir: claudeDir, ArchiveDir: dest, OlderThan: "30d"}); err != nil {
			return err
		}
		if pruned, err = runPrune(s, pruneOpts{ClaudeDir: claudeDir, ArchiveDir: dest, IDs: []string{cleanID}, Yes: true}); err != nil {
			return err
		}
		if undone, err = runPrune(s, pruneOpts{ClaudeDir: claudeDir, ArchiveDir: dest, Undo: []string{cleanID}}); err != nil {
			return err
		}
		// No transcript is left here: the warning is on s.Err.
		backedUp, err = runBackup(s, backupOpts{ClaudeDir: claudeDir, Out: filepath.Join(root, "backups"), DryRun: true})
		return err
	})
	if stdout != "" || stderr != "" {
		t.Errorf("printed outside their streams:\nstdout: %q\nstderr: %q", stdout, stderr)
	}
	for _, want := range []string{"Deleted 1 copies", "Would delete 1 conversation(s)", "Deleted 1 conversation(s)",
		"Off the deleted list: " + cleanID, "source  " + claudeDir} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the text has no %q:\n%s", want, out.String())
		}
	}
	for _, want := range []string{"\rcomparing copies: 1/1\n", "WARNING: no session transcripts"} {
		if !strings.Contains(errs.String(), want) {
			t.Errorf("the error stream has no %q:\n%s", want, errs.String())
		}
	}

	if len(cleaned.Plan.Remove) != 1 || cleaned.Plan.Remove[0].Bucket != "B" || cleaned.Deleted != 1 || cleaned.Freed == 0 {
		t.Errorf("clean = %+v, want B's copy deleted", cleaned)
	}
	if len(listed.Plan.Remove) != 1 || listed.Deleted != 0 {
		t.Errorf("prune --older-than = %+v, want A's copy listed, nothing deleted", listed)
	}
	if len(pruned.Plan.Remove) != 1 || pruned.Plan.Remove[0].ID != cleanID || pruned.Deleted != 1 || pruned.Freed == 0 {
		t.Errorf("prune --yes = %+v, want it deleted", pruned)
	}
	if len(undone.Undone) != 1 || undone.Undone[0] != cleanID {
		t.Errorf("prune --undo = %+v, want it off the list", undone)
	}
	if !backedUp.DryRun || backedUp.Source != claudeDir || backedUp.Transcripts != 0 || backedUp.Files != 1 {
		t.Errorf("backup = %+v, want a dry run of settings.json alone", backedUp)
	}
	// The panel encodes these results itself: an empty list is [], never null.
	for name, v := range map[string]any{"clean": cleaned, "prune": listed, "prune --yes": pruned, "prune --undo": undone} {
		if b, _ := json.Marshal(v); bytes.Contains(b, []byte("null")) {
			t.Errorf("%s's result encodes a null: %s", name, b)
		}
	}
	if _, err := os.Stat(filepath.Join(archive.ProjectsDir(dest), "A", cleanID+".jsonl")); err == nil {
		t.Error("prune --yes left A's copy")
	}
}

// --undo with no conversation is a usage error, as `prune --undo` alone is, not "prune
// needs --older-than".
func TestPruneUndoWithoutIDsIsAUsageError(t *testing.T) {
	_, err := runPrune(quietStreams(), pruneOpts{Undo: []string{}})
	if err == nil || !strings.HasPrefix(err.Error(), "usage: claude-sessions prune --undo") {
		t.Errorf("err = %v, want the usage", err)
	}
	if _, err := runPrune(quietStreams(), pruneOpts{}); err != errNoSelector {
		t.Errorf("err = %v, want errNoSelector", err)
	}
}

// Freed is what the deleted copies took: their transcripts, their own folders and their
// pages, which the goldens' size masks cannot show.
func TestCleanFreedIsTheBytesRemoved(t *testing.T) {
	t.Setenv("CLAUDE_SESSIONS_MACHINE", "LAP-1")
	root := t.TempDir()
	claudeDir, dest := filepath.Join(root, ".claude"), filepath.Join(root, "archive")
	turns := `{"type":"user","uuid":"u1","timestamp":"2026-08-30T09:00:00.000Z"}` + "\n" +
		`{"type":"assistant","uuid":"m1","timestamp":"2026-08-30T09:00:01.000Z"}` + "\n"
	writeFile(t, filepath.Join(archive.ProjectsDir(dest), "A", cleanID+".jsonl"),
		turns+`{"type":"assistant","uuid":"m2","timestamp":"2026-08-30T09:00:02.000Z"}`+"\n")
	for _, b := range []string{"B", "C"} { // each held in whole by A's copy
		writeFile(t, filepath.Join(archive.ProjectsDir(dest), b, cleanID+".jsonl"), turns)
	}
	for _, b := range []string{"A", "B"} { // a folder file A holds too
		writeFile(t, filepath.Join(archive.ProjectsDir(dest), b, cleanID, "tool-results", "r1.txt"), strings.Repeat("x", 1000))
	}
	writeFile(t, pagePath(dest, "B", cleanID), strings.Repeat("p", 333))
	writeFile(t, filepath.Join(archive.ManifestDir(dest), "LAP-1.json"),
		`{"machine":"LAP-1","skipsHeld":true,"honorsDeleted":true,"projects":{"A":{"path":"/x/a"},"B":{"path":"/x/b"},"C":{"path":"/x/c"}}}`)
	bytesIn := func() (n int64) {
		filepath.WalkDir(dest, func(p string, d os.DirEntry, err error) error {
			if info, ierr := d.Info(); err == nil && ierr == nil && d.Type().IsRegular() {
				n += info.Size()
			}
			return nil
		})
		return n
	}
	before := bytesIn()
	res, err := runClean(quietStreams(), cleanOpts{ClaudeDir: claudeDir, ArchiveDir: dest, Yes: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted != 2 {
		t.Fatalf("clean = %+v, want B's and C's copies deleted", res)
	}
	if removed := before - bytesIn(); res.Freed != removed || removed <= 1333 {
		t.Errorf("Freed %d, but %d bytes were removed", res.Freed, removed)
	}
}
