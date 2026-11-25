package hostagent

import (
	"fmt"
	"strings"
)

// This file holds what the "claude-sessions panel" shortcut contains on each OS, with
// no build tag and no system calls, so every platform's content is tested everywhere
// (see spec.go). Writing and removing the shortcut is in shortcut_<os>.go.

// ShortcutMarker tags a shortcut as this tool's: install replaces and uninstall removes
// only what carries it, never a shortcut the user made.
const ShortcutMarker = "claude-sessions-sync panel"

// ShortcutName is the name the app menu shows. A non-default profile gets its sweep's
// suffix, so a scratch install never replaces the real profile's shortcut.
func (j Job) ShortcutName() string {
	if s := j.suffix(); s != "" {
		return "claude-sessions panel (" + s[1:] + ")"
	}
	return "claude-sessions panel"
}

// ShortcutArgs is what the shortcut runs after the binary. --from-shortcut makes the
// panel stop by itself once its last tab has gone.
func (j Job) ShortcutArgs() []string {
	args := []string{"ui", "--from-shortcut"}
	if j.Profile != "" {
		args = append(args, "--claude-dir", j.Profile)
	}
	return args
}

// DesktopEntry is the Linux app menu entry.
func DesktopEntry(j Job) string {
	line := desktopQuote(j.Binary)
	for _, a := range j.ShortcutArgs() {
		line += " " + desktopQuote(a)
	}
	return "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=" + j.ShortcutName() + "\n" +
		"Comment=See and manage your Claude Code conversation backups\n" +
		"Exec=" + line + "\n" +
		"Terminal=false\n" +
		"Categories=Utility;\n" +
		"X-Claude-Sessions-Sync=" + ShortcutMarker + "\n"
}

// desktopQuote writes one Exec= argument as the Desktop Entry spec reads it. '%' is
// doubled so it is never a field code. An argument holding a reserved character goes
// in double quotes, where '"', '`', '$' and '\' take a backslash. Readers apply the
// string escape rule before the quoting rule, so every backslash is then doubled
// again (a literal '$' is \\$ in the file) and a newline becomes \n.
func desktopQuote(s string) string {
	s = strings.ReplaceAll(s, "%", "%%")
	if strings.ContainsAny(s, " \t\n\"'\\><~|&;$*?#()`") {
		s = `"` + strings.NewReplacer(`"`, `\"`, "`", "\\`", "$", `\$`, `\`, `\\`).Replace(s) + `"`
	}
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(s)
}

// AppleScript is the script of the macOS applet. Every argument goes through `quoted
// form of`, so the shell sees each as one word. With every stream redirected and a
// trailing &, `do shell script` returns at once instead of waiting for the panel.
//
// The first line is the marker as a comment. The compiled main.scpt keeps it, and
// that is how uninstall tells our applet from one the user saved under the same
// name. It sits in the source because osacompile signs the bundle: an edit to
// Info.plist after compiling would break the signature.
func AppleScript(j Job) string {
	s := "-- " + ShortcutMarker + "\ndo shell script quoted form of " + appleString(j.Binary)
	for _, a := range j.ShortcutArgs() {
		s += ` & " " & quoted form of ` + appleString(a)
	}
	return s + ` & " > /dev/null 2>&1 &"`
}

func appleString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// foreignShortcut is install's refusal to replace a shortcut it did not make.
func foreignShortcut(path string) error {
	return fmt.Errorf("%s already exists and was not made by claude-sessions - leaving it alone", path)
}
