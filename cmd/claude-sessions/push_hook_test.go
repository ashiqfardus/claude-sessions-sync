package main_test

// The Stop hook archives the session that just replied, and only it: it runs after
// every reply, so it skips the manifest, the index and the pages, and throttles big
// transcripts that a sync client re-uploads whole each time.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func (f fixture) pushFromHook(t *testing.T, transcript string) string {
	t.Helper()
	in, _ := json.Marshal(map[string]string{"session_id": "x", "transcript_path": transcript, "hook_event_name": "Stop"})
	cmd := exec.Command(binary, "push", "--claude-dir", f.claudeDir, "--archive", f.archive, "--quiet", "--from-hook")
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=")
	cmd.Stdin = strings.NewReader(string(in))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("a hook must never fail: %v\n%s", err, out)
	}
	return string(out)
}

const hookSession = "11111111-2222-3333-4444-555555555555"

func TestPushFromHookArchivesOnlyThatSession(t *testing.T) {
	f := newFixture(t)
	other := filepath.Join(f.claudeDir, "projects", "e--work-other", "77777777-7777-7777-7777-777777777777.jsonl")
	mustWrite(t, other, uuidLines("o1"))
	session := filepath.Join(f.claudeDir, "projects", "e--work-web-app", hookSession+".jsonl")

	f.pushFromHook(t, session)

	if _, err := os.Stat(filepath.Join(f.archive, "projects", "e--work-web-app", hookSession+".jsonl")); err != nil {
		t.Errorf("the session that replied was not archived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.archive, "projects", "e--work-other")); err == nil {
		t.Error("another session was archived by the per-reply hook")
	}
	if shards, _ := filepath.Glob(filepath.Join(f.archive, "manifest", "*.json")); len(shards) > 0 {
		t.Errorf("the per-reply hook did a full push: %v", shards)
	}
}

// A sync client uploads the whole file each time: a big transcript whose archived
// copy is only a minute behind is left for the next reply or the sweep.
func TestPushFromHookThrottlesALargeSession(t *testing.T) {
	f := newFixture(t)
	session := filepath.Join(f.claudeDir, "projects", "e--work-web-app", hookSession+".jsonl")
	big := uuidLines("u1") + `{"type":"x","text":"` + strings.Repeat("a", 6<<20) + `"}` + "\n"
	mustWrite(t, session, big+uuidLines("u2"))
	archived := filepath.Join(f.archive, "projects", "e--work-web-app", hookSession+".jsonl")
	mustWrite(t, archived, big)
	now := time.Now()
	os.Chtimes(session, now, now)
	os.Chtimes(archived, now.Add(-time.Minute), now.Add(-time.Minute))

	f.pushFromHook(t, session)

	if got := mustRead(t, archived); strings.Contains(got, `"u2"`) {
		t.Error("a large session was re-archived a minute after its last copy")
	}
}

// An install from before the per-reply hook existed only archives at session end and
// on the sweep; doctor says how to get it.
func TestDoctorSaysWhenThePerReplyHookIsMissing(t *testing.T) {
	f := newFixture(t)
	mustWrite(t, filepath.Join(f.claudeDir, "settings.json"), `{"cleanupPeriodDays":3650,"hooks":{"SessionEnd":[
      {"hooks":[{"type":"command","command":"claude-sessions","args":["push","--quiet"]}]}]}}`)

	out, _ := f.run(t, "doctor", "--no-write-probe")
	if !strings.Contains(out, "per-reply") {
		t.Errorf("doctor did not mention the missing per-reply hook:\n%s", out)
	}
}

// The hook's input names a file; only a transcript in this profile's projects folder
// is ever copied.
func TestPushFromHookIgnoresAPathOutsideProjects(t *testing.T) {
	f := newFixture(t)
	outside := filepath.Join(t.TempDir(), "e--work-x", "88888888-8888-8888-8888-888888888888.jsonl")
	mustWrite(t, outside, uuidLines("x1"))

	f.pushFromHook(t, outside)

	if _, err := os.Stat(filepath.Join(f.archive, "projects", "e--work-x")); err == nil {
		t.Error("a file outside the projects folder was archived")
	}
}
