package work

import (
	"reflect"
	"testing"
)

func fakeEnv(exist ...string) Env {
	m := map[string]bool{}
	for _, e := range exist {
		m[e] = true
	}
	return Env{
		Home: "/h", ClaudeRoot: "/h/.claude", Temp: []string{"/tmp"},
		RepoRoot: func(p string) string {
			if Within(p, "/h/dev/app", false) {
				return "/h/dev/app"
			}
			return ""
		},
		Exists: func(p string) bool { return m[p] },
	}
}

func TestFoldersParentOfWrittenFile(t *testing.T) {
	got := Folders([]Candidate{{"/h/SQL/po/r.md", true}}, fakeEnv("/h/SQL/po/r.md"))
	want := []Folder{{"/h/SQL/po", false}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestFoldersDropped(t *testing.T) {
	paths := []string{"/h/dev/app/x", "/h/.claude/p", "/tmp/a", `\\srv\share\x`, "/h/missing", "rel/x"}
	env := fakeEnv(paths[:4]...)
	var c []Candidate
	for _, p := range paths {
		c = append(c, Candidate{p, false})
	}
	if got := Folders(c, env); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestFoldersBroad(t *testing.T) {
	env := fakeEnv("/h/notes.md", "/h", "/", "/h/Desktop", "/h/Downloads", "/h/Documents", "/h/Desktop/a.txt", "/h/Documents/reports")
	for _, p := range []string{"/h", "/", "/h/Desktop", "/h/Downloads", "/h/Documents"} {
		if got := Folders([]Candidate{{p, false}}, env); len(got) != 0 {
			t.Fatalf("%s: got %v", p, got)
		}
	}
	for _, p := range []string{"/h/notes.md", "/h/Desktop/a.txt"} {
		want := []Folder{{p, true}}
		if got := Folders([]Candidate{{p, true}}, env); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: got %v", p, got)
		}
	}
	want := []Folder{{"/h/Documents/reports", false}}
	if got := Folders([]Candidate{{"/h/Documents/reports", false}}, env); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestFoldersCollapse(t *testing.T) {
	env := fakeEnv("/h/SQL", "/h/SQL/po", "/h/SQL/po/x.md")
	got := Folders([]Candidate{{"/h/SQL", false}, {"/h/SQL/po", false}, {"/h/SQL/po/x.md", true}}, env)
	if want := []Folder{{"/h/SQL", false}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestFoldersWindowsCase(t *testing.T) {
	env := Env{
		Home: `C:\Users\a`, ClaudeRoot: `C:\Users\a\.claude`, Windows: true,
		RepoRoot: func(string) string { return "" },
		Exists: func(p string) bool {
			return p == `E:\SQL\po` || p == `E:\sql\po\r.md` || p == `E:\SQL\run`
		},
	}
	got := Folders([]Candidate{{`E:\SQL\po`, false}, {`e:\sql\po\r.md`, true}, {"/e/SQL/run", false}}, env)
	want := []Folder{{`E:\SQL\po`, false}, {`E:\SQL\run`, false}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func fakeWinEnv() Env {
	return Env{
		Home: `C:\Users\a`, ClaudeRoot: `C:\Users\a\.claude`, Temp: []string{`C:\Temp`}, Windows: true,
		RepoRoot: func(string) string { return "" },
		Exists:   func(string) bool { return true },
	}
}

func TestFoldersWindowsDotDot(t *testing.T) {
	for _, p := range []string{`C:\Users\a\proj\..\..`, `C:\Users\a\.claude\..\.claude\x`, `C:\Temp\x\..\y`, `C:\..\..`} {
		if got := Folders([]Candidate{{p, false}}, fakeWinEnv()); len(got) != 0 {
			t.Fatalf("%s: got %v", p, got)
		}
	}
	got := Folders([]Candidate{{`E:\SQL\a\..\po`, false}}, fakeWinEnv())
	if want := []Folder{{`E:\SQL\po`, false}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestFoldersCaseVariantOrderIndependent(t *testing.T) {
	a, b := Candidate{`E:\SQL\po`, false}, Candidate{`e:\sql\po\r.md`, true}
	x := Folders([]Candidate{a, b}, fakeWinEnv())
	y := Folders([]Candidate{b, a}, fakeWinEnv())
	if !reflect.DeepEqual(x, y) || len(x) != 1 {
		t.Fatalf("x=%v y=%v", x, y)
	}
}

func TestFoldersDotSegment(t *testing.T) {
	env := fakeEnv("/h/.ssh/config", "/h/.config/app", "/h/Desktop/.env", "/h/w/.hidden/x.md", "/h/SQL/po/.env")
	got := Folders([]Candidate{{"/h/.ssh/config", true}, {"/h/.config/app", false}, {"/h/Desktop/.env", true},
		{"/h/w/.hidden/x.md", true}, {"/h/SQL/po/.env", true}}, env)
	// The dot file in an ordinary folder leaves the folder; Collect skips the file.
	if want := []Folder{{"/h/SQL/po", false}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	if got := Folders([]Candidate{{`C:\Users\a\.aws\config`, true}}, fakeWinEnv()); len(got) != 0 {
		t.Fatalf("windows: got %v", got)
	}
}
