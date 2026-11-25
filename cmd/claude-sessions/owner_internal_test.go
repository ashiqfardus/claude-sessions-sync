package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A folder this user made is its own, and is the only kind a temp clean-up removes.
func TestOwnedByMeKnowsItsOwnFolder(t *testing.T) {
	info, err := os.Lstat(t.TempDir())
	if err != nil || !ownedByMe(info) {
		t.Fatalf("a folder made here is not owned by this user: %v", err)
	}
}

// Only the names this program's MkdirTemp calls make are its extracts.
func TestExtractNameMatchesOnlyOurMkdirTemp(t *testing.T) {
	for _, pattern := range []string{"convzip-", "convzip-verify-", "claude-sessions-push-", "cs-zip"} {
		d, err := os.MkdirTemp("", pattern)
		if err != nil {
			t.Fatal(err)
		}
		os.Remove(d)
		if !extractName(filepath.Base(d)) {
			t.Errorf("%s, made by MkdirTemp(%q), is not taken for an extract", filepath.Base(d), pattern)
		}
	}
	for _, n := range []string{"convzip-", "cs-zip", "cs-zipper", "convzip-abc", "cs-zip12x", "cs-pack-123", "claude-sessions-zipped-123"} {
		if extractName(n) {
			t.Errorf("%s is taken for an extract", n)
		}
	}
}
