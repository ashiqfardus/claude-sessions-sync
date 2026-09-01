// Package claude knows Claude Code's own on-disk layout: where its config lives,
// how project buckets are named, and how to read a session transcript.
//
// It is deliberately the ONLY package with that knowledge. The transcript format
// is internal to Claude Code and changes between releases, so when an upgrade
// breaks something, this is the one place to fix.
package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RootSource says how Root resolved, so callers can report it.
type RootSource string

const (
	RootFromEnv     RootSource = "CLAUDE_CONFIG_DIR"
	RootFromDefault RootSource = "default"
)

// Root returns Claude Code's config directory: $CLAUDE_CONFIG_DIR when set,
// otherwise ~/.claude.
//
// Everything else resolves through this. Hardcoding ~/.claude - as the PowerShell
// implementation did - silently operates on the wrong profile whenever the env var
// is in use, which is exactly when the user has two profiles to keep apart.
func Root() (string, RootSource, error) {
	if v := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); v != "" {
		return filepath.Clean(v), RootFromEnv, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("cannot locate home directory: %w", err)
	}
	return filepath.Join(home, ".claude"), RootFromDefault, nil
}

// ProjectsDir is where session buckets live.
func ProjectsDir(root string) string { return filepath.Join(root, "projects") }

// SettingsPath is the user-level settings.json.
func SettingsPath(root string) string { return filepath.Join(root, "settings.json") }

// StatePath locates .claude.json, which keys per-project state by absolute path.
//
// It is read to recover the real path of a bucket that holds no transcript to read a
// cwd from. It is never copied to the archive: a live session rewrites it on exit, so
// restoring it over a working profile causes more harm than it fixes.
//
// WHERE IT ACTUALLY LIVES: by default it is ~/.claude.json - a SIBLING of ~/.claude/,
// not a file inside it. Joining it onto the config root looks obviously right and
// silently finds nothing, which is exactly how it was wrong until it was tried
// against a real profile. Under CLAUDE_CONFIG_DIR it does sit inside the configured
// directory, so both are checked, in that order.
func StatePath(root string) string {
	inRoot := filepath.Join(root, ".claude.json")
	if _, err := os.Stat(inRoot); err == nil {
		return inRoot
	}
	if home, err := os.UserHomeDir(); err == nil {
		beside := filepath.Join(home, ".claude.json")
		if _, err := os.Stat(beside); err == nil {
			return beside
		}
	}
	return inRoot
}
