package hostagent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func shortcutPath(j Job) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Applications", j.ShortcutName()+".app"), nil
}

// ownsShortcut reports whether path is an applet osacompile made: a bundle without a
// compiled script is someone else's app.
func ownsShortcut(path string) bool {
	_, err := os.Stat(filepath.Join(path, "Contents", "Resources", "Scripts", "main.scpt"))
	return err == nil
}

// InstallShortcut builds the panel applet in ~/Applications and returns its path.
//
// The applet is compiled beside its final place and renamed into it, so a failed
// osacompile leaves the previous applet working.
func InstallShortcut(j Job) (string, error) {
	path, err := shortcutPath(j)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(path); err == nil && !ownsShortcut(path) {
		return "", foreignShortcut(path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(path), ".tmp-claude-sessions-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)

	built := filepath.Join(tmp, filepath.Base(path)) // osacompile picks an applet from the .app extension
	if out, err := exec.Command(systemBinary("osacompile"), "-o", built, "-e", AppleScript(j)).CombinedOutput(); err != nil {
		return "", fmt.Errorf("osacompile failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if err := os.RemoveAll(path); err != nil {
		return "", err
	}
	if err := os.Rename(built, path); err != nil {
		return "", err
	}
	return path, nil
}

// UninstallShortcut removes the panel applet, and only an applet, reporting whether
// there was one.
func UninstallShortcut(j Job) (bool, error) {
	path, err := shortcutPath(j)
	if err != nil || !ownsShortcut(path) {
		return false, err
	}
	if err := os.RemoveAll(path); err != nil {
		return false, err
	}
	return true, nil
}
