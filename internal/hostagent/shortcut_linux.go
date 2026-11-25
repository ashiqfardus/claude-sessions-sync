package hostagent

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
)

// shortcut: ~/.local/share/applications only; a desktop that moves XDG_DATA_HOME
// elsewhere will not list the entry - honour XDG_DATA_HOME if someone reports it.
func shortcutPath(j Job) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "applications", "claude-sessions-panel"+j.suffix()+".desktop"), nil
}

// ownsShortcut reports whether path is an entry this tool wrote.
func ownsShortcut(path string) bool {
	data, err := os.ReadFile(path)
	return err == nil && bytes.Contains(data, []byte("X-Claude-Sessions-Sync="))
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

// UninstallShortcut removes the panel's entry, and only ours, reporting whether there
// was one.
func UninstallShortcut(j Job) (bool, error) {
	path, err := shortcutPath(j)
	if err != nil || !ownsShortcut(path) {
		return false, err
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}
