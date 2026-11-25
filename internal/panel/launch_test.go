package panel

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRedirectFileIsPrivate(t *testing.T) {
	key := strings.Repeat("ab", 32)
	url := "http://127.0.0.1:4321/k/" + key
	path, err := WriteRedirect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)

	if filepath.Dir(path) != filepath.Clean(os.TempDir()) || !strings.HasPrefix(filepath.Base(path), "claude-sessions-panel-") || filepath.Ext(path) != ".html" {
		t.Errorf("redirect file at %s", path)
	}
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
