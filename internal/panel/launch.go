package panel

import (
	"encoding/json"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// launch hands a second `claude-sessions ui` a fresh key for the running panel. It is
// exempt from the cookie, Origin and token checks; the secret, read from the 0600
// panel file, replaces them. A browser cannot send X-CS-Launch cross-site without a
// preflight, which gets 405, and always sends Origin on a POST.
func (s *Server) launch(w http.ResponseWriter, r *http.Request) {
	if r.Header["Origin"] != nil || !same(r.Header.Get("X-CS-Launch"), s.secret) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"key": s.NewKey()})
}

// Seams for the tests: where WriteRedirect decides its folder.
var redirectOS, redirectHome = runtime.GOOS, os.UserHomeDir

// WriteRedirect writes a private page that redirects to url, so the opener is handed a
// file path and the key never appears in any process's arguments. The caller deletes it,
// on use, after 60 s and at exit: on Linux it sits in plain sight in the home folder.
func WriteRedirect(url string) (string, error) {
	f, err := os.CreateTemp(redirectDir(), redirectPrefix+"*"+redirectExt) // O_EXCL, mode 0600, a random name
	if err != nil {
		return "", err
	}
	u := html.EscapeString(url)
	_, err = f.WriteString("<!doctype html>\n<meta charset=\"utf-8\">\n<meta http-equiv=\"refresh\" content=\"0;url=" + u + "\">\n<title>claude-sessions panel</title>\n<p><a href=\"" + u + "\">Open the claude-sessions panel</a></p>\n")
	// On Windows the file's privacy relies on the per-user %TEMP% folder's ACL.
	if err == nil && runtime.GOOS != "windows" {
		err = f.Chmod(0o600)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// Open shows the redirect file at path in the default browser. It never takes a URL.
func Open(path string) error {
	cmd := openCmd(path)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // xdg-open can wait for the browser to exit; only reap it
	return nil
}

const redirectPrefix, redirectExt = "claude-sessions-panel-", ".html"

// redirectDir is where WriteRedirect puts its files: the temp folder, or on Linux the
// home folder, as Snap Firefox and Chromium (Ubuntu's defaults) have a private /tmp and
// cannot read hidden folders.
func redirectDir() string {
	if redirectOS == "linux" {
		if h, err := redirectHome(); err == nil {
			return h
		}
	}
	return os.TempDir()
}

// CleanRedirects removes redirect files older than age: a crash leaves one behind, and
// so does a second `ui`, which hands its file to the browser and exits.
func CleanRedirects(age time.Duration) {
	dir := redirectDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasPrefix(name, redirectPrefix) || !strings.HasSuffix(name, redirectExt) {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > age {
			os.Remove(filepath.Join(dir, name))
		}
	}
}
