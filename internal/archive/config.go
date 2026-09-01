// Package archive handles the synced destination folder: where it is, what is
// recorded in it, and how local files compare against it.
package archive

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is <claude-root>/session-sync.json.
//
// The filename and the "destination" key are inherited from the PowerShell
// implementation on purpose: an existing install keeps working after the switch.
type Config struct {
	Destination string `json:"destination"`
}

// ConfigPath is where the destination is persisted.
func ConfigPath(claudeRoot string) string { return filepath.Join(claudeRoot, "session-sync.json") }

// Source describes how the destination was resolved, for reporting.
type Source string

const (
	SourceFlag     Source = "flag"
	SourceConfig   Source = "session-sync.json"
	SourceDetected Source = "auto-detected"
	SourceNone     Source = "unset"
)

// ResolveDestination finds the archive folder: the flag, then the saved config, then
// auto-detection of the usual synced folders.
//
// Auto-detection is a convenience, never a requirement - the point of writing to a
// plain folder is that Google Drive, OneDrive, Dropbox, iCloud, Syncthing and a NAS
// mount are all just paths. Anything undetected is passed with --archive and saved
// with `claude-sessions config set-destination`.
func ResolveDestination(claudeRoot, flagValue string) (string, Source, error) {
	if v := strings.TrimSpace(flagValue); v != "" {
		return TrimSep(v), SourceFlag, nil
	}

	cfg, err := LoadConfig(claudeRoot)
	if err != nil {
		return "", SourceNone, err
	}
	if v := strings.TrimSpace(cfg.Destination); v != "" {
		return TrimSep(v), SourceConfig, nil
	}

	if d := detect(); d != "" {
		return d, SourceDetected, nil
	}
	return "", SourceNone, nil
}

// LoadConfig reads session-sync.json. A missing file yields a zero Config, not an
// error: an unconfigured machine is a normal state.
func LoadConfig(claudeRoot string) (Config, error) {
	var c Config
	raw, err := os.ReadFile(ConfigPath(claudeRoot))
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return c, nil
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("%s: %w", ConfigPath(claudeRoot), err)
	}
	return c, nil
}

// SaveConfig persists the destination, preserving any keys this version does not
// know about - a newer build, or a hand-edited file, must not lose settings.
func SaveConfig(claudeRoot string, c Config) error {
	merged := map[string]any{}
	if raw, err := os.ReadFile(ConfigPath(claudeRoot)); err == nil {
		_ = json.Unmarshal(raw, &merged) // a corrupt file is replaced, not honoured
	}
	merged["destination"] = c.Destination

	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(ConfigPath(claudeRoot), append(data, '\n'), 0o600)
}

const archiveFolderName = "claude-sessions"

// detect looks for a synced folder in the conventional places.
func detect() string {
	var candidates []string

	// Google Drive for Desktop mounts as <letter>:\My Drive on Windows.
	for _, root := range driveRoots() {
		candidates = append(candidates, filepath.Join(root, "My Drive", archiveFolderName))
	}

	if home, err := os.UserHomeDir(); err == nil {
		for _, rel := range [][]string{
			{"Google Drive", "My Drive"},
			{"Google Drive"},
			{"OneDrive"},
			{"Dropbox"},
			{"Library", "Mobile Documents", "com~apple~CloudDocs"}, // iCloud Drive on macOS
			{"Sync"}, // Syncthing's common default
		} {
			parts := append([]string{home}, rel...)
			candidates = append(candidates, filepath.Join(append(parts, archiveFolderName)...))
		}
	}

	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}

	// Nothing named claude-sessions yet: fall back to a synced parent that exists, so
	// a first run has somewhere sensible to create it.
	for _, root := range driveRoots() {
		parent := filepath.Join(root, "My Drive")
		if st, err := os.Stat(parent); err == nil && st.IsDir() {
			return filepath.Join(parent, archiveFolderName)
		}
	}
	return ""
}

// TrimSep removes trailing path separators so that comparisons and joins behave.
func TrimSep(p string) string { return strings.TrimRight(p, `\/`) }

// ValidateDestination rejects an archive folder that would make the tool copy into
// its own source.
//
// Pointing the destination at ~/.claude (or inside it) means every push writes new
// files under the directory it is reading, which the next push then reads and copies
// again - the archive grows without bound and the live store fills with duplicates.
// It is an easy mistake to make and an unpleasant one to undo.
func ValidateDestination(claudeRoot, dest string) error {
	root, err := filepath.Abs(claudeRoot)
	if err != nil {
		return err
	}
	target, err := filepath.Abs(dest)
	if err != nil {
		return err
	}

	rel, err := filepath.Rel(root, target)
	if err != nil {
		// Different volumes: cannot be nested, which is the answer we wanted.
		return nil
	}
	if rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)) {
		return fmt.Errorf("%s is inside the Claude directory (%s): the archive would copy into its own source", target, root)
	}
	return nil
}

// ProjectsDir is where transcripts live inside the archive.
func ProjectsDir(dest string) string { return filepath.Join(dest, "projects") }

// ManifestDir holds the per-machine manifest shards.
func ManifestDir(dest string) string { return filepath.Join(dest, "manifest") }

// LegacyManifestPath is the single merged projects.json.
//
// Still written on every push as a read-only compatibility view - the PowerShell
// scripts read it, and it is what a human browsing the folder will open - but it is
// no longer the source of truth. See manifest.go for why.
func LegacyManifestPath(dest string) string { return filepath.Join(dest, "projects.json") }
