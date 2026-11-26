package hostagent

import (
	"os"
	"path/filepath"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
)

// shortcutPaths is where the entry goes, then where an earlier install may have left
// it (see desktopDirs).
func shortcutPaths(j Job) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, d := range desktopDirs(home, os.Getenv("XDG_DATA_HOME")) {
		out = append(out, filepath.Join(d, "claude-sessions-panel"+j.suffix()+".desktop"))
	}
	return out, nil
}

func shortcutPath(j Job) (string, error) {
	ps, err := shortcutPaths(j)
	if err != nil {
		return "", err
	}
	return ps[0], nil
}

// ownsShortcut reports whether path is an entry this tool wrote.
func ownsShortcut(path string) bool {
	data, err := os.ReadFile(path)
	return err == nil && desktopOwned(data)
}

// InstallShortcut writes the panel's app menu entry and returns its path.
func InstallShortcut(j Job) (string, error) {
	path, err := shortcutPath(j)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(path); err == nil && !ownsShortcut(path) {
		return "", foreignShortcut(path)
	}
	if err := archive.WriteFileAtomic(path, []byte(DesktopEntry(j)), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// UninstallShortcut removes the panel's entry, and only ours, wherever install put it,
// reporting whether there was one.
func UninstallShortcut(j Job) (bool, error) {
	ps, err := shortcutPaths(j)
	if err != nil {
		return false, err
	}
	removed := false
	for _, path := range ps {
		if !ownsShortcut(path) {
			continue
		}
		if err := os.Remove(path); err != nil {
			return removed, err
		}
		removed = true
	}
	return removed, nil
}
