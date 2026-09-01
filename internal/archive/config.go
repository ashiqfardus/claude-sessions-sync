// Package archive handles the synced destination folder: where it is, what is
// recorded in it, and how local files compare against it.
package archive

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
// Auto-detection is a convenience, never a requirement - the whole point of writing
// to a plain folder is that Google Drive, OneDrive, Dropbox, iCloud, Syncthing and a
// NAS mount are all just paths. Anything undetected is passed with --archive.
func ResolveDestination(claudeRoot, flagValue string) (string, Source, error) {
	if v := strings.TrimSpace(flagValue); v != "" {
		return trimSep(v), SourceFlag, nil
	}

	cfg, err := LoadConfig(claudeRoot)
	if err != nil {
		return "", SourceNone, err
	}
	if v := strings.TrimSpace(cfg.Destination); v != "" {
		return trimSep(v), SourceConfig, nil
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

const archiveFolderName = "claude-sessions"

// detect looks for a synced folder in the conventional places.
func detect() string {
	var candidates []string

	if runtime.GOOS == "windows" {
		// Google Drive for Desktop mounts as <letter>:\My Drive.
		for c := 'A'; c <= 'Z'; c++ {
			candidates = append(candidates, filepath.Join(string(c)+`:\`, "My Drive", archiveFolderName))
		}
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
			candidates = append(candidates, filepath.Join(append([]string{home}, append(rel, archiveFolderName)...)...))
		}
	}

	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}

	// Nothing named claude-sessions yet: fall back to a parent that exists, so a
	// first run has somewhere sensible to create it.
	if runtime.GOOS == "windows" {
		for c := 'A'; c <= 'Z'; c++ {
			parent := filepath.Join(string(c)+`:\`, "My Drive")
			if st, err := os.Stat(parent); err == nil && st.IsDir() {
				return filepath.Join(parent, archiveFolderName)
			}
		}
	}
	return ""
}

func trimSep(p string) string {
	return strings.TrimRight(p, `\/`)
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
