package main

import "testing"

// R4-4: a fresh macOS install whose sweep has never once been allowed: every sweep is
// refused, the hooks (Claude Code's grant) succeed. doctor should still report it.
func TestFourthReviewSweepNeverAllowedIsReported(t *testing.T) {
	root := writeLog(t,
		"2026-11-20 10:00:00  PUSH: 3 copied, 0 unchanged, 1 session(s), 1 project(s) -> /a",
		"2026-11-20 10:20:42  SKIP: destination parent not available: stat /a: operation not permitted"+sweepMark,
		"2026-11-20 10:31:00  PUSH: 1 copied, 0 unchanged, 1 session(s), 1 project(s) -> /a",
	)
	if _, ok := deniedSincePush(root); !ok {
		t.Error("a sweep that has never succeeded was cleared by a hook's push")
	}
}
