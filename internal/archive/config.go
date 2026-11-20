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

	// RenderExclude names projects whose pages should not be written. Rendering makes
	// a conversation readable to anyone who can open the synced folder, which is a
	// different decision from archiving it.
	RenderExclude []string `json:"renderExclude,omitempty"`

	// ProjectMap says where an archived project lives on this machine, by its bucket
	// name in the archive, for projects nothing else identifies. It is per machine on
	// purpose: the local path means nothing anywhere else.
	ProjectMap map[string]string `json:"projectMap,omitempty"`

	// Machine pins the name this machine is known by in the archive. Saved here, not
	// in the environment: the sweep, launched by the system, sees no shell profile.
	Machine string `json:"machine,omitempty"`

	// FormerMachines are names this machine went by before. Its own files in the
	// archive still carry them; taken for another machine's, they came back to it as
	// forks, and its memory was merged as a stranger's.
	FormerMachines []string `json:"formerMachines,omitempty"`
}

// Mapped is the local folder the user mapped an archived bucket to, if any. Bucket
// names compare case-insensitively, as everywhere else.
func (c Config) Mapped(bucket string) (string, bool) {
	for k, v := range c.ProjectMap {
		if strings.EqualFold(k, bucket) && strings.TrimSpace(v) != "" {
			return v, true
		}
	}
	return "", false
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
	// Absolute, always. The hook runs in each project's folder and the sweep in the
	// system directory, so a relative archive became a copy of every session inside
	// every repository, and a sweep that failed silently.
	if v := strings.TrimSpace(flagValue); v != "" {
		abs, err := filepath.Abs(TrimSep(v))
		return abs, SourceFlag, err
	}

	cfg, err := LoadConfig(claudeRoot)
	if err != nil {
		return "", SourceNone, err
	}
	if v := strings.TrimSpace(cfg.Destination); v != "" {
		if !filepath.IsAbs(v) {
			return "", SourceNone, fmt.Errorf("%s: destination %q is not an absolute path - run `claude-sessions config set-destination <folder>`",
				ConfigPath(claudeRoot), v)
		}
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
	if c.RenderExclude != nil {
		merged["renderExclude"] = c.RenderExclude
	}
	if c.ProjectMap != nil {
		merged["projectMap"] = c.ProjectMap
	}
	if c.Machine != "" {
		merged["machine"] = c.Machine
	}
	if c.FormerMachines != nil {
		merged["formerMachines"] = c.FormerMachines
	}

	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(ConfigPath(claudeRoot), append(data, '\n'), 0o600)
}

// SharedExcludePath is <archive>/render-exclude.txt: one pattern per line.
func SharedExcludePath(dest string) string { return filepath.Join(dest, "render-exclude.txt") }

// RenderExcludes returns the exclusions in force for an archive: this machine's
// renderExclude, the archive's shared list, and any one-off extras.
//
// renderExclude used to live only in each machine's own config, while rendering
// covers the whole shared archive - so a second machine without the setting rendered
// the excluded project straight back. With persist, this machine's patterns are added
// to the shared list so every machine honours them. It only ever adds: to publish a
// project again, remove its line from render-exclude.txt as well as from the config.
func RenderExcludes(claudeRoot, dest string, extra []string, persist bool) []string {
	cfg, _ := LoadConfig(claudeRoot)

	var shared []string
	if raw, err := os.ReadFile(SharedExcludePath(dest)); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if p := strings.TrimSpace(line); p != "" && !strings.HasPrefix(p, "#") {
				shared = append(shared, p)
			}
		}
	}

	seen := map[string]bool{}
	var all, added []string
	add := func(p string, local bool) {
		p = strings.TrimSpace(p)
		if p == "" || seen[strings.ToLower(p)] {
			return
		}
		seen[strings.ToLower(p)] = true
		all = append(all, p)
		if local {
			added = append(added, p)
		}
	}
	for _, p := range shared {
		add(p, false)
	}
	for _, p := range cfg.RenderExclude {
		add(p, true)
	}

	if persist && len(added) > 0 {
		if _, err := os.Stat(dest); err == nil {
			body := "# Projects whose sessions are not published as pages, on any machine.\n" +
				"# One case-insensitive fragment of a project path or bucket name per line.\n"
			for _, p := range all {
				body += p + "\n"
			}
			_ = WriteFileAtomic(SharedExcludePath(dest), []byte(body), 0o644)
		}
	}

	for _, p := range extra {
		add(p, false)
	}
	return all
}

const archiveFolderName = "claude-sessions"

// detect looks for a synced folder in the conventional places.
func detect() string {
	var candidates []string

	// Google Drive for Desktop mounts as <letter>:\My Drive on Windows.
	for _, root := range driveRoots() {
		candidates = append(candidates, filepath.Join(root, "My Drive", archiveFolderName))
	}

	var cloudDrives []string
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

		// On macOS, Drive for desktop and OneDrive mount one folder per account under
		// ~/Library/CloudStorage. Choosing between two accounts would be a guess. Looked
		// at after the folders above, so a machine already using one keeps it.
		archives, drives := cloudStorage(home)
		if len(archives) == 1 {
			candidates = append(candidates, archives[0])
		}
		// A first-run fallback only when no account holds an archive yet: with two
		// holding one, picking the Google one would be the guess refused above.
		if len(archives) == 0 {
			cloudDrives = drives
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
	if len(cloudDrives) == 1 {
		return filepath.Join(cloudDrives[0], archiveFolderName)
	}
	return ""
}

// cloudStorage lists what the per-account folders under ~/Library/CloudStorage hold:
// existing archives (in GoogleDrive-*/My Drive or OneDrive-*), and the Google Drive
// roots a first push could create one in.
func cloudStorage(home string) (archives, drives []string) {
	dir := filepath.Join(home, "Library", "CloudStorage")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		var root string
		switch name := e.Name(); {
		case strings.HasPrefix(name, "GoogleDrive-"):
			root = filepath.Join(dir, name, "My Drive")
			if st, err := os.Stat(root); err == nil && st.IsDir() {
				drives = append(drives, root)
			}
		case strings.HasPrefix(name, "OneDrive-"):
			root = filepath.Join(dir, name)
		default:
			continue
		}
		if st, err := os.Stat(filepath.Join(root, archiveFolderName)); err == nil && st.IsDir() {
			archives = append(archives, filepath.Join(root, archiveFolderName))
		}
	}
	return archives, drives
}

// TrimSep removes trailing path separators so that comparisons and joins behave.
func TrimSep(p string) string { return strings.TrimRight(p, `\/`) }

// PrepareDestination checks a destination the user has just chosen, creates its
// folder and returns it absolute.
//
// The folder is created, but never its parent: if the sync client is not mounted,
// silently creating a plain local folder where G:\ should be is how people end up
// believing they have a cloud backup that does not exist.
func PrepareDestination(claudeRoot, dest string) (string, error) {
	abs, err := filepath.Abs(TrimSep(strings.TrimSpace(dest)))
	if err != nil {
		return "", err
	}
	if st, err := os.Stat(filepath.Dir(abs)); err != nil || !st.IsDir() {
		return "", fmt.Errorf("%s does not exist - is the sync client mounted?", filepath.Dir(abs))
	}
	// Validate before creating anything: refusing after having made the directory
	// leaves litter behind for a request that was rejected.
	if err := ValidateDestination(claudeRoot, abs); err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", err
	}
	if err := Writable(abs); err != nil {
		return "", fmt.Errorf("%s is not writable: %w", abs, err)
	}
	return abs, nil
}

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
