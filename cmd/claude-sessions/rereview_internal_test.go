package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 8. The per-reply hook gives up on work that hangs - a NAS asleep - rather than
// holding up the reply until Claude Code kills it.
func TestWithinDeadlineGivesUpOnHangingWork(t *testing.T) {
	start := time.Now()
	finished := withinDeadline(50*time.Millisecond, func() { time.Sleep(5 * time.Second) })
	if finished || time.Since(start) > 2*time.Second {
		t.Fatalf("withinDeadline = %v after %s; want it to give up at the deadline", finished, time.Since(start))
	}
	if !withinDeadline(time.Second, func() {}) {
		t.Fatal("work that finished in time was reported as abandoned")
	}
}

// 10. A future-dated archived file is refused even when there is no local copy: once
// filed, it stays "newer" than every later local edit, which then never reach the
// archive.
func TestShouldRestoreRefusesAFutureDatedNewFile(t *testing.T) {
	src := filepath.Join(t.TempDir(), "note.md")
	os.WriteFile(src, []byte("x\n"), 0o644)
	future := time.Now().Add(48 * time.Hour)
	os.Chtimes(src, future, future)
	info, _ := os.Stat(src)
	if shouldRestore(info, filepath.Join(t.TempDir(), "missing.md"), false) {
		t.Fatal("a future-dated file was restored because nothing was there yet")
	}
}

// Third review 6: with no marking sweep at all - --no-sweep, or an install from before
// --sweep - nothing could ever clear a refusal, and doctor failed for good.
func TestDeniedSincePushIsClearedByAnyPushWhenNoSweepMarks(t *testing.T) {
	root := writeLog(t,
		"2026-11-20 10:20:42  SKIP: destination parent not available: stat /a: operation not permitted",
		"2026-11-20 10:31:00  PUSH: 1 copied, 0 unchanged, 1 session(s), 1 project(s) -> /a",
	)
	if line, ok := deniedSincePush(root); ok {
		t.Fatalf("a refusal stayed after a successful push with no sweep marking its runs: %q", line)
	}
}

// Third review 7: a per-reply push that gives up says so, but not after every reply.
func TestHookTimeoutIsLoggedAtMostHourly(t *testing.T) {
	root := t.TempDir()
	noteHookTimeout(root)
	noteHookTimeout(root)
	raw, _ := os.ReadFile(filepath.Join(root, "session-sync.log"))
	if n := strings.Count(string(raw), "TIMEOUT"); n != 1 {
		t.Fatalf("TIMEOUT logged %d times, want once:\n%s", n, raw)
	}
}

// 4. Where a sweep marks its runs, only its success clears a refusal; any other push
// may hold the grant the sweep lacks.
func TestDeniedSincePushIsClearedOnlyByTheSweep(t *testing.T) {
	earlier := "2026-11-20 09:50:00  PUSH: 1 copied, 0 unchanged, 1 session(s), 1 project(s) -> /a" + sweepMark
	refused := "2026-11-20 10:20:42  SKIP: destination parent not available: stat /a: operation not permitted" + sweepMark
	other := writeLog(t, earlier, refused, "2026-11-20 10:31:00  PUSH: 1 copied, 0 unchanged, 1 session(s), 1 project(s) -> /a")
	if _, ok := deniedSincePush(other); !ok {
		t.Error("a push that was not the sweep cleared the refusal")
	}
	swept := writeLog(t, refused, "2026-11-20 10:50:00  PUSH: 1 copied, 0 unchanged, 1 session(s), 1 project(s) -> /a"+sweepMark)
	if _, ok := deniedSincePush(swept); ok {
		t.Error("a successful sweep did not clear the refusal")
	}
}
