package work

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
)

// testLocal is this machine, MAC, with a temp home holding a fake repo at dev/app.
func testLocal(t *testing.T) Local {
	home := t.TempDir()
	repo := filepath.Join(home, "dev", "app")
	return Local{Machine: "MAC", Env: Env{
		Home: home, ClaudeRoot: filepath.Join(home, ".claude"), Temp: []string{"", filepath.Join(home, "tmp")},
		RepoRoot: func(p string) string {
			if Within(p, repo, true) {
				return repo
			}
			return ""
		},
	}}
}

func mustPlace(t *testing.T, src Source, l Local, want string) {
	t.Helper()
	got, err := Place(src, l)
	if err != nil || got != want {
		t.Fatalf("Place(%+v) = %q, %v; want %q", src, got, err, want)
	}
}

func TestPlaceHome(t *testing.T) {
	l := testLocal(t)
	mustPlace(t, Source{Machine: "WIN", Home: "/Users/a", Path: "/Users/a/SQL/po"}, l, filepath.Join(l.Home, "SQL", "po"))
	l.Placement = "home"
	mustPlace(t, Source{Machine: "WIN", Home: `C:\Users\a`, Path: `c:\users\A\SQL\po`}, l, filepath.Join(l.Home, "SQL", "po"))
}

func TestPlaceOutsideHome(t *testing.T) {
	l := testLocal(t)
	mustPlace(t, Source{Machine: "WIN", Home: `C:\Users/a`, Path: `E:\SQL\po`}, l,
		filepath.Join(l.Home, "claude-work", "WIN", "E", "SQL", "po"))
	l.Placement = "machine"
	mustPlace(t, Source{Machine: "MAC", Home: "/Users/a", Path: "/Users/a/SQL/po"}, l,
		filepath.Join(l.Home, "claude-work", "MAC", "Users", "a", "SQL", "po"))
}

func TestPlaceMap(t *testing.T) {
	l := testLocal(t)
	dir, po := filepath.Join(l.Home, "mapped"), filepath.Join(l.Home, "po-here")
	l.Map = map[string]string{"e:\\sql": dir, "/Users/a/SQL": dir}
	src := Source{Machine: "WIN", Home: `C:\Users\a`, Path: `E:\SQL\po\run`}
	mustPlace(t, src, l, filepath.Join(dir, "po", "run"))
	src.Path = `E:\SQL`
	mustPlace(t, src, l, dir)
	mustPlace(t, Source{Machine: "MAC2", Home: "/Users/a", Path: "/users/a/sql/x"}, l, filepath.Join(dir, "x"))
	l.Map[`E:/SQL/po`] = po // the most specific key wins
	src.Path = `E:\SQL\po\run`
	mustPlace(t, src, l, filepath.Join(po, "run"))
}

func TestPlaceOrigin(t *testing.T) {
	l := testLocal(t)
	own, mapped := filepath.Join(l.Home, "SQL"), filepath.Join(l.Home, "mapped")
	orig := filepath.Join(own, "po")
	l.Own = []string{own}
	l.Map = map[string]string{`E:\SQL`: mapped}
	src := Source{Machine: "WIN", Home: `C:\Users\a`, Path: `E:\SQL\po`, Origin: &archive.WorkOrigin{Machine: "MAC", Path: orig}}
	mustPlace(t, src, l, orig) // rule 0 wins over the map

	viaMap := filepath.Join(mapped, "po")
	l.Own = []string{filepath.Join(l.Home, "other")} // origin is not one of ours: rules 1-3
	mustPlace(t, src, l, viaMap)
	l.Own = []string{own}
	src.Origin = &archive.WorkOrigin{Machine: "OTHER", Path: orig}
	mustPlace(t, src, l, viaMap)
	sep := string(filepath.Separator)
	src.Origin = &archive.WorkOrigin{Machine: "MAC", Path: own + sep + ".." + sep + "bin"}
	mustPlace(t, src, l, viaMap)
	src.Origin = &archive.WorkOrigin{Machine: "MAC", Path: "SQL/po"}
	l.Own = []string{"SQL"}
	mustPlace(t, src, l, viaMap)
}

func TestPlaceSameLayout(t *testing.T) {
	l := testLocal(t)
	l.SameLayout = true
	p := filepath.Join(l.Home, "SQL", "po")
	mustPlace(t, Source{Machine: "WIN", Home: "/Users/a", Path: p}, l, p)
	other := `E:\SQL\po`
	if runtime.GOOS == "windows" {
		other = "/Users/a/SQL/po"
	}
	if got, err := Place(Source{Machine: "WIN", Home: "/Users/a", Path: other}, l); err == nil {
		t.Fatalf("got %q", got)
	}
}

func TestPlaceRefused(t *testing.T) {
	l := testLocal(t)
	win := func(p string) Source { return Source{Machine: "WIN", Home: `C:\Users\a`, Path: p} }
	mac := func(p string) Source { return Source{Machine: "MAC2", Home: "/Users/a", Path: p} }
	exact := func(s Source) Source { s.Exact = true; return s }
	for _, src := range []Source{
		{Machine: "M", Home: "/", Path: "/x/y"},
		{Machine: "M", Home: `C:\`, Path: `C:\x\y`},
		{Machine: "M", Home: "Users/a", Path: "/Users/a/x"},
		{Machine: "M", Home: "", Path: "/Users/a/x"},
		{Machine: "M", Home: "/Users/b/../a", Path: "/Users/a/x"},
		exact(mac("/Users/a/.zshrc")),
		mac("/Users/a/.ssh/x"),
		mac("/Users/a/Library/LaunchAgents"),
		mac("/Users/a/library/LaunchAgents"),
		win(`C:\Users\a\AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Startup`),
		win(`C:\Users\a\appdata\Roaming`),
		mac("/Users/a/.claude/skills/x"),
		mac("/Users/a"),
		mac("/Users/a/Desktop"),
		mac("/Users/a/DESKTOP"),
		mac("/Users/a/dev/app/src"),
		mac("/Users/a/tmp/x"),
		mac("/Users/a/w/.hidden/x"),
		// The source path itself (R11).
		mac("Users/a/SQL"),
		mac("/Users/a/SQL/../x"),
		win(`E:\SQL\..\po`),
		mac(`\\srv\share\x`),
		mac("//srv/share/x"),
		mac(`/\srv\share\x`), // Windows reads any two leading separators as UNC
		mac(`\/srv/share/x`),
		mac(""),
		mac("/"),
		win(`E:\`),
		// Rule 3 needs a valid machine name.
		{Machine: "../x", Home: `C:\Users\a`, Path: `E:\SQL\po`},
		{Machine: "", Home: `C:\Users\a`, Path: `E:\SQL\po`},
		{Machine: `a\b`, Home: `C:\Users\a`, Path: `E:\SQL\po`},
		{Machine: ".x", Home: `C:\Users\a`, Path: `E:\SQL\po`},
		win(`E:\SQL\.git\x`),
	} {
		if got, err := Place(src, l); !errors.Is(err, ErrRefused) {
			t.Errorf("Place(%+v) = %q, %v; want refused", src, got, err)
		}
	}
	// Exact files are excepted from the broad rule only.
	mustPlace(t, exact(mac("/Users/a/Desktop/notes.md")), l, filepath.Join(l.Home, "Desktop", "notes.md"))
	l.Map = map[string]string{`E:\SQL`: filepath.Join(l.Home, "mapped", ".x")}
	if got, err := Place(win(`E:\SQL\po`), l); !errors.Is(err, ErrRefused) {
		t.Errorf("dot in map target: %q, %v", got, err)
	}
}

// Targets that only another OS produces are checked directly.
func TestPlaceRefusedTargets(t *testing.T) {
	for _, c := range []struct {
		env    Env
		target string
		ok     bool
	}{
		{fakeEnv(), "/h/SQL/po", true},
		{fakeEnv(), "/opt/work/x", true},
		{fakeEnv(), "/opt/.x/y", false}, // R9: a dot segment anywhere
		{fakeEnv(), "/etc/x", false},
		{fakeEnv(), "/ETC/x", false},
		{fakeEnv(), "/Library/LaunchDaemons/x", false},
		{fakeEnv(), "/System/Volumes/Data/h/x", false},
		{fakeEnv(), "/h/Library/x", false},
		{fakeEnv(), "/H/library/x", false},
		{fakeEnv(), "/H", false},
		{fakeEnv(), "/h/.claude/x", false},
		{fakeEnv(), "/tmp/x", false},
		{fakeEnv(), "/h/dev/app/x", false},
		{fakeWinEnv(), `E:\SQL\po`, true},
		{fakeWinEnv(), `C:\Users\a\SQL\po`, true},
		{fakeWinEnv(), `C:\ProgramData\Microsoft\Windows\Start Menu\Programs\StartUp`, false}, // R12
		{fakeWinEnv(), `D:\data\programdata\x`, false},
		{fakeWinEnv(), `C:\Windows\System32\x`, false},
		{fakeWinEnv(), `c:\program files\x`, false},
		{fakeWinEnv(), `C:\Program Files (x86)\x`, false},
		{fakeWinEnv(), `C:\Users\a\AppData\Roaming\x`, false},
		// Names Win32 rewrites into a refused one: trailing dot or space, short name, stream.
		{fakeWinEnv(), `C:\Users\a\AppData.\Roaming\x`, false},
		{fakeWinEnv(), `C:\Users\a\ `, false},
		{fakeWinEnv(), `C:\Windows \x`, false},
		{fakeWinEnv(), `C:\Users\a\APPDAT~1\Roaming\x`, false},
		{fakeWinEnv(), `C:\Users\a\AppData::$INDEX_ALLOCATION\Roaming`, false},
		{fakeWinEnv(), `C:\Temp\x`, false},
		// PowerShell profiles run at every start, like ~/.zshrc; Documents may sit in OneDrive.
		{fakeWinEnv(), `C:\Users\a\Documents\WindowsPowerShell`, false},
		{fakeWinEnv(), `C:\Users\a\OneDrive\Documents\PowerShell\x`, false},
	} {
		if why := refused(c.target, false, c.env); (why == "") != c.ok {
			t.Errorf("refused(%q) = %q, want ok=%v", c.target, why, c.ok)
		}
	}
}

// A link or junction under the home leads anywhere; Windows ships "Start Menu" as one.
func TestPlaceThroughLink(t *testing.T) {
	l := testLocal(t)
	real := filepath.Join(l.Home, "AppData", "Roaming", "Start Menu")
	if err := os.MkdirAll(filepath.Join(real, "Programs", "Startup"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(l.Home, "Start Menu")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	src := Source{Machine: "WIN", Home: `C:\Users\a`, Path: `C:\Users\a\Start Menu\Programs\Startup`}
	if got, err := Place(src, l); !errors.Is(err, ErrRefused) {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, err := SafeJoin(l.Home, "Start Menu/Programs/Startup/x.bat"); !errors.Is(err, ErrRefused) {
		t.Fatalf("SafeJoin through link: %q, %v", got, err)
	}
	l.Map = map[string]string{`E:\SQL`: filepath.Join(l.Home, "Start Menu")} // a link the user mapped is theirs
	mustPlace(t, Source{Machine: "WIN", Path: `E:\SQL\po`}, l, filepath.Join(l.Home, "Start Menu", "po"))
}

func TestSafeJoin(t *testing.T) {
	target := t.TempDir()
	for _, rel := range []string{"../x", "/abs", `\abs`, "a/../../b", ".git/config", "sub/.env", "", "./x",
		`C:\x`, `C:x`, `sub\.git\config`, `a\..\..\b`} {
		if got, err := SafeJoin(target, rel); !errors.Is(err, ErrRefused) {
			t.Errorf("SafeJoin(%q) = %q, %v", rel, got, err)
		}
	}
	if got, err := SafeJoin(target, "sub/r.md"); err != nil || got != filepath.Join(target, "sub", "r.md") {
		t.Fatalf("got %q, %v", got, err)
	}
}
