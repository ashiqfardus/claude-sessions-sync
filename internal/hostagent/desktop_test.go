package hostagent

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// parseExec reads an Exec= value as the Desktop Entry spec says a launcher does: the
// string escapes first (\s \n \t \r \\), then the quoting rule, where inside double
// quotes a backslash escapes " ` $ and \, then %% as a literal %. It fails the test
// on what the spec forbids: a control character, a reserved character outside quotes,
// another backslash escape, an unterminated quote or a field code.
func parseExec(t *testing.T, v string) []string {
	t.Helper()
	var s strings.Builder
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c < 0x20 || c == 0x7f {
			t.Fatalf("control character %q in the value %q", c, v)
		}
		if c != '\\' {
			s.WriteByte(c)
			continue
		}
		if i++; i == len(v) {
			t.Fatalf("a lone backslash ends %q", v)
		}
		esc, ok := map[byte]byte{'s': ' ', 'n': '\n', 't': '\t', 'r': '\r', '\\': '\\'}[v[i]]
		if !ok {
			t.Fatalf("unknown string escape \\%c in %q", v[i], v)
		}
		s.WriteByte(esc)
	}
	u := s.String()
	var args []string
	var cur strings.Builder
	in, quoted := false, false
	for i := 0; i < len(u); i++ {
		c := u[i]
		switch {
		case c == ' ' && !quoted:
			if in {
				args, in = append(args, cur.String()), false
				cur.Reset()
			}
			continue
		case c == '"':
			quoted, in = !quoted, true
			continue
		case quoted && c == '\\':
			if i++; i == len(u) || !strings.ContainsRune("\"`$\\", rune(u[i])) {
				t.Fatalf("a backslash in quotes escapes nothing it may in %q", u)
			}
			c = u[i]
		case !quoted && strings.ContainsRune("\t\n'\\><~|&;$*?#()`", rune(c)):
			t.Fatalf("reserved character %q outside quotes in %q", c, u)
		case c == '%':
			if i++; i == len(u) || u[i] != '%' {
				t.Fatalf("a field code in %q", u)
			}
		}
		cur.WriteByte(c)
		in = true
	}
	if quoted {
		t.Fatalf("unterminated quote in %q", u)
	}
	if in {
		args = append(args, cur.String())
	}
	return args
}

// A launcher reading the entry runs the binary with exactly the arguments it was given,
// whatever a path holds.
func TestDesktopExecRoundTrips(t *testing.T) {
	for _, p := range []string{
		"/opt/claude-sessions", "/home/a b/bin/claude-sessions", "/home/a/cost$5 100%",
		`/home/q"t\b`, "/home/`x`", "/home/a\nb", "/home/a\tb", "/home/a\rb", "/home/~me/#1 (x)",
		`/home/it's;&|<>*?`, `/home/\\server\share`, "/home/üñî 100%% done", "/home/a\\sb",
	} {
		j := Job{Binary: p, Profile: p}
		entry := DesktopEntry(j)
		var exec string
		for _, l := range strings.Split(entry, "\n") {
			if v, ok := strings.CutPrefix(l, "Exec="); ok {
				exec = v
			}
		}
		if got, want := parseExec(t, exec), append([]string{p}, j.ShortcutArgs()...); !reflect.DeepEqual(got, want) {
			t.Errorf("Exec=%s reads as %q, want %q", exec, got, want)
		}
	}
}

// An entry is ours only by the marker line DesktopEntry writes, not a mention of the key
// somewhere in a file the user made.
func TestDesktopOwnedNeedsTheMarkerLine(t *testing.T) {
	for entry, want := range map[string]bool{
		DesktopEntry(Job{Binary: "/opt/claude-sessions"}):                                         true,
		strings.ReplaceAll(DesktopEntry(Job{Binary: "/opt/claude-sessions"}), "\n", "\r\n"):       true,
		"[Desktop Entry]\nName=mine\nComment=not X-Claude-Sessions-Sync=" + ShortcutMarker + "\n": false,
		"[Desktop Entry]\nName=mine\nX-Claude-Sessions-Sync=something else\n":                     false,
		"[Desktop Entry]\nName=mine\nX-Claude-Sessions-Sync-Old=" + ShortcutMarker + "\n":         false,
		"[Desktop Entry]\nName=mine\nExec=/usr/bin/true\n":                                        false,
	} {
		if got := desktopOwned([]byte(entry)); got != want {
			t.Errorf("desktopOwned(%q) = %v, want %v", entry, got, want)
		}
	}
}

// The app menu lists $XDG_DATA_HOME/applications when that is set, absolute; the
// default stays next in the list, where an earlier install wrote the entry.
func TestDesktopDirsHonourXDGDataHome(t *testing.T) {
	home := t.TempDir()
	def := filepath.Join(home, ".local", "share", "applications")
	xdg := filepath.Join(t.TempDir(), "data")
	for in, want := range map[string][]string{
		"":                {def},
		"relative/data":   {def}, // the spec: a relative path is ignored
		xdg:               {filepath.Join(xdg, "applications"), def},
		filepath.Dir(def): {def},
	} {
		if got := desktopDirs(home, in); !reflect.DeepEqual(got, want) {
			t.Errorf("desktopDirs(%q) = %q, want %q", in, got, want)
		}
	}
}

// On Linux install writes the entry where XDG_DATA_HOME says, and uninstall also
// removes the one an earlier install left in the default place.
func TestShortcutFollowsXDGDataHome(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the app menu entry is Linux only")
	}
	home := tempHome(t)
	xdg := filepath.Join(home, "data")
	t.Setenv("XDG_DATA_HOME", xdg)
	j := Job{Binary: "/opt/claude-sessions"}
	old := filepath.Join(home, ".local", "share", "applications", "claude-sessions-panel.desktop")
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte(DesktopEntry(j)), 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := InstallShortcut(j)
	if want := filepath.Join(xdg, "applications", "claude-sessions-panel.desktop"); err != nil || path != want {
		t.Fatalf("install = %s, %v; want %s", path, err, want)
	}
	if removed, err := UninstallShortcut(j); err != nil || !removed {
		t.Fatalf("uninstall = %v, %v", removed, err)
	}
	for _, p := range []string{path, old} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s is still there: %v", p, err)
		}
	}
}

// install prints the reason a shortcut failed on its own line: a tool's output over
// several lines is joined into one.
func TestOneLine(t *testing.T) {
	if got := oneLine([]byte("\n  At line:1 char:1\r\n+ New-Object ...\r\n\r\n  Access denied.  \n")); got != "At line:1 char:1; + New-Object ...; Access denied." {
		t.Errorf("oneLine = %q", got)
	}
}
