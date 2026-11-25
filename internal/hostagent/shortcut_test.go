package hostagent

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// tempHome points every home lookup this package makes at a scratch folder, so no
// test writes to the real ~/Applications, app menu or Start menu.
func tempHome(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	return home
}

func powershell() string {
	return systemBinary(filepath.Join("WindowsPowerShell", "v1.0", "powershell"))
}

// The default profile keeps the plain name; another profile gets the same suffix its
// sweep does, so installing for a scratch profile never replaces the real shortcut.
func TestShortcutNamesPerProfile(t *testing.T) {
	if got := (Job{}).ShortcutName(); got != "claude-sessions panel" {
		t.Errorf("default profile: got %q", got)
	}
	j := Job{Profile: "/home/you/scratch"}
	got := j.ShortcutName()
	if !regexp.MustCompile(`^claude-sessions panel \([0-9a-f]{8}\)$`).MatchString(got) {
		t.Fatalf("other profile: got %q", got)
	}
	if want := "claude-sessions panel (" + strings.TrimPrefix(j.TaskName(), Name+"-") + ")"; got != want {
		t.Errorf("got %q, want %q (the sweep's suffix)", got, want)
	}
}

func TestShortcutArgs(t *testing.T) {
	if got, want := (Job{}).ShortcutArgs(), []string{"ui", "--from-shortcut"}; !reflect.DeepEqual(got, want) {
		t.Errorf("default profile: got %q, want %q", got, want)
	}
	got := (Job{Profile: "/home/you/scratch"}).ShortcutArgs()
	if want := []string{"ui", "--from-shortcut", "--claude-dir", "/home/you/scratch"}; !reflect.DeepEqual(got, want) {
		t.Errorf("other profile: got %q, want %q", got, want)
	}
}

// The spec applies the string escape rule before the quoting rule, so a literal $ in
// a quoted argument is \\$ in the file, and a literal backslash is four of them.
func TestDesktopEntryQuotes(t *testing.T) {
	j := Job{Binary: "/home/a b/bin/claude-sessions", Profile: "/home/a/cost$5 100%"}
	got := DesktopEntry(j)
	want := "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=" + j.ShortcutName() + "\n" +
		"Comment=See and manage your Claude Code conversation backups\n" +
		`Exec="/home/a b/bin/claude-sessions" ui --from-shortcut --claude-dir "/home/a/cost\\$5 100%%"` + "\n" +
		"Terminal=false\n" +
		"Categories=Utility;\n" +
		"X-Claude-Sessions-Sync=" + ShortcutMarker + "\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}

	cases := map[string]string{
		"/opt/claude-sessions": "/opt/claude-sessions",
		"/opt/100%":            "/opt/100%%",
		`/home/q"t\b`:          `"/home/q\\"t\\\\b"`,
		"/home/`x`":            "\"/home/\\\\`x\\\\`\"",
		"/home/a\nb":           `"/home/a\nb"`,
	}
	for in, want := range cases {
		if got := desktopQuote(in); got != want {
			t.Errorf("desktopQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

// appleLiterals returns an AppleScript source's string literals, unescaped.
func appleLiterals(t *testing.T, src string) []string {
	var out []string
	for i := 0; i < len(src); i++ {
		if src[i] != '"' {
			continue
		}
		var b strings.Builder
		for i++; i < len(src) && src[i] != '"'; i++ {
			if src[i] == '\\' {
				i++
			}
			if i >= len(src) {
				break
			}
			b.WriteByte(src[i])
		}
		if i >= len(src) {
			t.Fatalf("unterminated string literal in %s", src)
		}
		out = append(out, b.String())
	}
	return out
}

func TestAppleScriptEscapes(t *testing.T) {
	plain := AppleScript(Job{Binary: "/Users/you/.claude/bin/claude-sessions"})
	want := `do shell script quoted form of "/Users/you/.claude/bin/claude-sessions"` +
		` & " " & quoted form of "ui" & " " & quoted form of "--from-shortcut" & " > /dev/null 2>&1 &"`
	if plain != want {
		t.Errorf("got:\n%s\nwant:\n%s", plain, want)
	}

	j := Job{Binary: `/Users/you/a "b" \c/claude-sessions`, Profile: `/Users/you/p\"q`}
	got := appleLiterals(t, AppleScript(j))
	wantLits := []string{j.Binary, " ", "ui", " ", "--from-shortcut", " ", "--claude-dir", " ", j.Profile, " > /dev/null 2>&1 &"}
	if !reflect.DeepEqual(got, wantLits) {
		t.Errorf("literals did not round-trip:\n got %q\nwant %q", got, wantLits)
	}
}

// The real interpreter is the only judge of both escaping layers: the AppleScript
// literal and the shell word `quoted form of` makes of it.
func TestAppleScriptRunsTheBinary(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("osascript is macOS only")
	}
	osa := systemBinary("osascript")
	if !filepath.IsAbs(osa) {
		t.Skip("osascript not found")
	}
	dir := filepath.Join(t.TempDir(), `it's "odd" \ $x`)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "claude-sessions")
	fake := "#!/bin/sh\nd=$(dirname \"$0\")\nprintf '%s\\n' \"$@\" > \"$d/args.tmp\" && mv \"$d/args.tmp\" \"$d/args.txt\"\n"
	if err := os.WriteFile(bin, []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	j := Job{Binary: bin, Profile: filepath.Join(dir, "profile 100% `x`")}
	if out, err := exec.Command(osa, "-e", AppleScript(j)).CombinedOutput(); err != nil {
		t.Fatalf("osascript: %v: %s", err, out)
	}

	var data []byte
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if b, err := os.ReadFile(filepath.Join(dir, "args.txt")); err == nil {
			data = b
			break
		}
	}
	if data == nil {
		t.Fatal("the fake binary never ran")
	}
	if got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"); !reflect.DeepEqual(got, j.ShortcutArgs()) {
		t.Errorf("the binary got %q, want %q", got, j.ShortcutArgs())
	}
}

// wantShortcutPath is where each OS's app menu looks, worked out independently of
// shortcutPath so a wrong location fails here.
func wantShortcutPath(home string, j Job) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Applications", j.ShortcutName()+".app")
	case "windows":
		return filepath.Join(home, "AppData", "Roaming", "Microsoft", "Windows", "Start Menu", "Programs", j.ShortcutName()+".lnk")
	}
	return filepath.Join(home, ".local", "share", "applications", "claude-sessions-panel"+j.suffix()+".desktop")
}

func TestShortcutInstallAndUninstall(t *testing.T) {
	home := tempHome(t)
	bin := filepath.Join(home, ".claude", "bin", "claude-sessions")
	switch runtime.GOOS {
	case "darwin":
		if !filepath.IsAbs(systemBinary("osacompile")) {
			t.Skip("osacompile not found")
		}
	case "windows":
		if !filepath.IsAbs(powershell()) {
			t.Skip("powershell not found")
		}
		bin += ".exe"
		if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{bin, filepath.Join(filepath.Dir(bin), WindowlessName)} {
			if err := os.WriteFile(f, []byte("MZ"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	j := Job{Binary: bin, Profile: filepath.Join(home, "scratch profile")}
	want := wantShortcutPath(home, j)

	for round := 1; round <= 2; round++ { // the second install replaces the first
		path, err := InstallShortcut(j)
		if err != nil {
			t.Fatalf("install %d: %v", round, err)
		}
		if path != want {
			t.Fatalf("install %d wrote %s, want %s", round, path, want)
		}
		entries, err := os.ReadDir(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Errorf("install %d left %d entries beside the shortcut, want only it", round, len(entries))
		}
	}

	switch runtime.GOOS {
	case "darwin":
		if _, err := os.Stat(filepath.Join(want, "Contents", "Resources", "Scripts", "main.scpt")); err != nil {
			t.Errorf("the applet has no compiled script: %v", err)
		}
	case "windows":
		cmd := exec.Command(powershell(), "-NoProfile", "-NonInteractive", "-Command",
			`$s = (New-Object -ComObject WScript.Shell).CreateShortcut($env:CS_LNK); $s.TargetPath; $s.Arguments; $s.Description`)
		cmd.Env = append(os.Environ(), "CS_LNK="+want)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("reading the shortcut: %v", err)
		}
		got := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(out), "\r", "")), "\n")
		if len(got) != 3 {
			t.Fatalf("got %q", got)
		}
		if !strings.EqualFold(filepath.Base(got[0]), WindowlessName) {
			t.Errorf("target %q is not the windowless copy", got[0])
		}
		if got[1] != windowsArgs(j.ShortcutArgs()) {
			t.Errorf("arguments %q, want %q", got[1], windowsArgs(j.ShortcutArgs()))
		}
		if got[2] != ShortcutMarker {
			t.Errorf("description %q, want the marker", got[2])
		}
	default:
		data, err := os.ReadFile(want)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != DesktopEntry(j) {
			t.Errorf("entry:\n%s\nwant:\n%s", data, DesktopEntry(j))
		}
		if st, _ := os.Stat(want); st.Mode().Perm() != 0o644 {
			t.Errorf("mode %v, want 0644", st.Mode().Perm())
		}
	}

	removed, err := UninstallShortcut(j)
	if err != nil || !removed {
		t.Fatalf("uninstall = %v, %v; want true, nil", removed, err)
	}
	if _, err := os.Lstat(want); !os.IsNotExist(err) {
		t.Errorf("the shortcut is still there: %v", err)
	}
	if removed, err := UninstallShortcut(j); err != nil || removed {
		t.Errorf("second uninstall = %v, %v; want false, nil", removed, err)
	}
}

// A file at our path that this tool did not write is someone else's: uninstall leaves
// it and says nothing was removed, and install refuses to replace it.
func TestUninstallLeavesForeignShortcuts(t *testing.T) {
	home := tempHome(t)
	j := Job{Binary: filepath.Join(home, ".claude", "bin", "claude-sessions"), Profile: filepath.Join(home, "scratch")}
	path, err := shortcutPath(j)
	if err != nil {
		t.Fatal(err)
	}
	if path != wantShortcutPath(home, j) {
		t.Fatalf("shortcutPath = %s, want %s", path, wantShortcutPath(home, j))
	}

	probe := path // the file whose survival proves nothing was touched
	content := []byte("[Desktop Entry]\nType=Application\nName=mine\nExec=/usr/bin/true\n")
	if runtime.GOOS == "darwin" { // an app bundle without an AppleScript applet's main.scpt
		probe = filepath.Join(path, "Contents", "Info.plist")
		content = []byte("<plist/>")
	}
	if err := os.MkdirAll(filepath.Dir(probe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(probe, content, 0o644); err != nil {
		t.Fatal(err)
	}

	removed, err := UninstallShortcut(j)
	if err != nil || removed {
		t.Errorf("uninstall = %v, %v; want false, nil", removed, err)
	}
	if got, err := InstallShortcut(j); err == nil {
		t.Errorf("install replaced a foreign shortcut at %s", got)
	}
	if data, err := os.ReadFile(probe); err != nil || string(data) != string(content) {
		t.Errorf("the foreign shortcut changed: %q, %v", data, err)
	}
}
