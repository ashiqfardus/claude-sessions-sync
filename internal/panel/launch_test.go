package panel

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeHome points the redirect file's Linux home at a test folder, so no test writes
// into the real home.
func fakeHome(t *testing.T, goos string) string {
	home := t.TempDir()
	oldOS, oldHome := redirectOS, redirectHome
	t.Cleanup(func() { redirectOS, redirectHome = oldOS, oldHome })
	redirectOS, redirectHome = goos, func() (string, error) { return home, nil }
	return home
}

// Snap browsers (Ubuntu's defaults) cannot read /tmp or hidden folders, so on Linux the
// file goes straight into the home folder, not hidden; elsewhere it stays in the temp folder.
func TestRedirectGoesToHomeOnLinux(t *testing.T) {
	for goos, inHome := range map[string]bool{"linux": true, "darwin": false, "windows": false} {
		home := fakeHome(t, goos)
		path, err := WriteRedirect("http://127.0.0.1:4321/k/x")
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(path)
		want := filepath.Clean(os.TempDir())
		if inHome {
			want = home
		}
		base := filepath.Base(path)
		if filepath.Dir(path) != want || !strings.HasPrefix(base, "claude-sessions-panel-") || filepath.Ext(base) != ".html" {
			t.Errorf("%s: redirect file at %s, want claude-sessions-panel-*.html in %s", goos, path, want)
		}
	}
}

func TestRedirectFileIsPrivate(t *testing.T) {
	fakeHome(t, runtime.GOOS)
	key := strings.Repeat("ab", 32)
	url := "http://127.0.0.1:4321/k/" + key
	path, err := WriteRedirect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)

	if strings.Contains(path, key) {
		t.Errorf("the key is in the file's path %s", path)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", st.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<meta http-equiv="refresh" content="0;url=` + url + `">`, `<a href="` + url + `">`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("redirect file lacks %s:\n%s", want, b)
		}
	}
}

// The opener gets the redirect file's path, so the key never reaches a process's arguments.
func TestOpenerGetsThePathNotTheURL(t *testing.T) {
	path := filepath.Join(os.TempDir(), "claude-sessions-panel-1.html")
	cmd := openCmd(path)
	args := strings.Join(cmd.Args[1:], " ")
	if !strings.HasSuffix(args, path) || strings.Contains(args, "http") {
		t.Errorf("opener arguments %q", args)
	}
}

// A crash, or a second `ui` that handed its file to the browser and exited, leaves a
// redirect file behind; on Linux it sits in plain sight in the home folder.
func TestCleanRedirectsRemovesOnlyOldRedirectFiles(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		home := fakeHome(t, goos)
		dir := home
		if goos != "linux" {
			dir = t.TempDir()
			for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
				t.Setenv(k, dir)
			}
		}
		old, err := WriteRedirect("http://127.0.0.1:4321/k/x")
		if err != nil {
			t.Fatal(err)
		}
		fresh, err := WriteRedirect("http://127.0.0.1:4321/k/y")
		if err != nil {
			t.Fatal(err)
		}
		other := filepath.Join(dir, "notes.html")
		if err := os.WriteFile(other, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		then := time.Now().Add(-2 * time.Minute)
		for _, p := range []string{old, other} {
			if err := os.Chtimes(p, then, then); err != nil {
				t.Fatal(err)
			}
		}

		CleanRedirects(time.Minute)
		for p, kept := range map[string]bool{old: false, fresh: true, other: true} {
			if _, err := os.Stat(p); (err == nil) != kept {
				t.Errorf("%s: %s kept = %v, want %v", goos, filepath.Base(p), err == nil, kept)
			}
		}
		os.Remove(fresh)
	}
}
