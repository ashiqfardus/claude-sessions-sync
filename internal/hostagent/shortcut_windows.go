package hostagent

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf16"
)

func shortcutPath(j Job) (string, error) {
	dir, err := os.UserConfigDir() // %APPDATA%
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "Microsoft", "Windows", "Start Menu", "Programs", j.ShortcutName()+".lnk"), nil
}

// ownsShortcut reports whether path is a shortcut this tool made: its Description is
// the marker, which a .lnk stores as UTF-16LE.
func ownsShortcut(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var marker []byte
	for _, u := range utf16.Encode([]rune(ShortcutMarker)) {
		marker = append(marker, byte(u), byte(u>>8))
	}
	return bytes.Contains(data, marker)
}

// linkScript makes the shortcut from environment variables, so no path is ever
// spliced into PowerShell source.
const linkScript = `$ErrorActionPreference = 'Stop'; ` +
	`$s = (New-Object -ComObject WScript.Shell).CreateShortcut($env:CS_LNK); ` +
	`$s.TargetPath = $env:CS_TARGET; $s.Arguments = $env:CS_ARGS; $s.Description = $env:CS_DESC; ` +
	`$s.WorkingDirectory = $env:USERPROFILE; $s.Save()`

// InstallShortcut adds the panel to the Start menu and returns the shortcut's path.
//
// It runs the windowless copy beside j.Binary (see WindowlessName), so opening the
// panel opens no terminal.
func InstallShortcut(j Job) (string, error) {
	path, err := shortcutPath(j)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(path); err == nil && !ownsShortcut(path) {
		return "", foreignShortcut(path)
	}
	target := filepath.Join(filepath.Dir(j.Binary), WindowlessName)
	if _, err := os.Stat(target); err != nil {
		return "", fmt.Errorf("the shortcut's program %s cannot be found (install puts it there first): %w", target, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}

	cmd := Command(systemBinary(filepath.Join("WindowsPowerShell", "v1.0", "powershell")),
		"-NoProfile", "-NonInteractive", "-Command", linkScript)
	cmd.Env = append(os.Environ(),
		"CS_LNK="+path,
		"CS_TARGET="+target,
		"CS_ARGS="+windowsArgs(j.ShortcutArgs()),
		"CS_DESC="+ShortcutMarker)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("powershell could not create %s: %v: %s", path, err, oneLine(out))
	}
	// Uninstall finds the shortcut by the marker; one without it could never be removed.
	if !ownsShortcut(path) {
		return "", fmt.Errorf("powershell did not write %s", path)
	}
	return path, nil
}

// UninstallShortcut removes the panel's Start menu shortcut, and only ours, reporting
// whether there was one.
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
