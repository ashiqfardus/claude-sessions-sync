package hostagent

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

func shortcutPath(j Job) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Applications", j.ShortcutName()+".app"), nil
}

// ownsShortcut reports whether path is our applet: its compiled script holds the
// marker comment AppleScript starts with. Any other bundle, even another AppleScript
// applet under the same name, is the user's.
func ownsShortcut(path string) bool {
	data, err := os.ReadFile(filepath.Join(path, "Contents", "Resources", "Scripts", "main.scpt"))
	if err != nil {
		return false
	}
	// The compiled script held the comment both as plain text and as UTF-16BE (the form
	// its string literals take) on the macOS this was built on; either one is enough.
	var be []byte
	for _, c := range []byte(ShortcutMarker) {
		be = append(be, 0, c)
	}
	return bytes.Contains(data, []byte(ShortcutMarker)) || bytes.Contains(data, be)
}

// InstallShortcut builds the panel applet in ~/Applications and returns its path.
//
// The applet is compiled beside its final place. The old one is renamed aside, the new
// one renamed in, and only then is the old one removed. A failure at any step leaves
// the previous applet in place.
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
	if out, err := Command(systemBinary("osacompile"), "-o", built, "-e", AppleScript(j)).CombinedOutput(); err != nil {
		return "", fmt.Errorf("osacompile failed: %v: %s", err, oneLine(out))
	}
	old := tmp + "-old.app" // beside the target too, so every rename stays on one volume
	if err := os.Rename(path, old); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Rename(built, path); err != nil {
		os.Rename(old, path) // put the previous applet back; if this fails too it is still at old
		return "", err
	}
	os.RemoveAll(old) // the new applet is in place; a leftover hidden copy harms nothing
	return path, nil
}

// UninstallShortcut removes the panel applet, and only ours, reporting whether there
// was one.
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
