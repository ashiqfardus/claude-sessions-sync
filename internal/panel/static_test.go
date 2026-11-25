package panel

import (
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"testing"
)

var fontFiles = []string{
	"IBMPlexSans-Regular-Latin1.woff2", "IBMPlexSans-Medium-Latin1.woff2", "IBMPlexSans-SemiBold-Latin1.woff2",
	"IBMPlexMono-Regular-Latin1.woff2", "IBMPlexMono-Medium-Latin1.woff2",
}

// staticFiles returns every embedded file under static/ with its contents.
func staticFiles(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := fs.WalkDir(Static, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := Static.ReadFile(p)
		files[p] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestFontsAreEmbedded(t *testing.T) {
	for _, f := range fontFiles {
		b, err := Static.ReadFile("static/fonts/" + f)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if !strings.HasPrefix(string(b), "wOF2") || len(b) >= 30*1024 {
			t.Errorf("%s: %d bytes, starts %q; want a woff2 under 30 KB", f, len(b), b[:min(4, len(b))])
		}
	}
	if b, err := Static.ReadFile("static/fonts/OFL.txt"); err != nil || !strings.Contains(string(b), "SIL Open Font License") {
		t.Errorf("OFL.txt missing or without the licence text: %v", err)
	}
	css, err := Static.ReadFile("static/panel.css")
	if err != nil {
		t.Fatal(err)
	}
	urls := regexp.MustCompile(`url\(\s*["']?([^"')]+)`).FindAllStringSubmatch(string(css), -1)
	if len(urls) != len(fontFiles) {
		t.Errorf("panel.css has %d url(), want one per font (%d)", len(urls), len(fontFiles))
	}
	for _, m := range urls {
		if _, err := Static.ReadFile(path.Join("static", m[1])); err != nil {
			t.Errorf("panel.css url(%s) names no embedded file", m[1])
		}
	}
}

// Only what the browser loads is scanned (Ruling P6): OFL.txt and SOURCE.txt hold
// URLs as text and are never fetched.
func TestStaticHasNoRemoteLoads(t *testing.T) {
	const readme = "https://github.com/ashiqfardus/claude-sessions-sync#readme"
	for p, s := range staticFiles(t) {
		switch path.Ext(p) {
		case ".html", ".css", ".js", ".svg":
		default:
			continue
		}
		if p == "static/js/settings.js" {
			s = strings.ReplaceAll(s, readme, "")
		}
		for _, bad := range []string{"http://", "https://", "@import"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s contains %q", p, bad)
			}
		}
	}
}

func TestStaticNeverInjectsHTML(t *testing.T) {
	files := staticFiles(t)
	for p, s := range files {
		if path.Ext(p) != ".js" {
			continue
		}
		for _, bad := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function", `setTimeout("`} {
			if strings.Contains(s, bad) {
				t.Errorf("%s contains %q", p, bad)
			}
		}
	}
	shell := files["static/index.html"]
	// The CSP refuses inline styles and handlers, so any here would silently do nothing.
	for _, bad := range []string{"style=", "<style"} {
		if strings.Contains(shell, bad) {
			t.Errorf("index.html contains %q", bad)
		}
	}
	if m := regexp.MustCompile(`(?i)\son[a-z]+=`).FindString(shell); m != "" {
		t.Errorf("index.html has an event handler attribute %q", m)
	}
	for _, tag := range regexp.MustCompile(`(?i)<script\b[^>]*>`).FindAllString(shell, -1) {
		if !strings.Contains(tag, " src=") {
			t.Errorf("index.html has an inline script %q", tag)
		}
	}
}

func TestShellCarriesTheToken(t *testing.T) {
	s := newTestServer(t, nil)
	w := do(s, http.MethodGet, "/", "Cookie", login(t, s))
	body := w.Body.String()
	if want := `<meta name="cs-token" content="` + s.Token() + `">`; w.Code != http.StatusOK || !strings.Contains(body, want) {
		t.Fatalf("GET / gave %d without %s", w.Code, want)
	}
	// The pieces the page scripts and the screen tasks build on.
	for _, want := range []string{
		`<link rel="stylesheet" href="/static/panel.css">`, `<script type="module" src="/static/js/app.js">`,
		`href="#main">Skip to content</a>`, `<nav aria-label="Panel" class="rail">`, `role="separator"`,
		`<main id="main"`, `id="toasts" role="status"`, `id="dialogs"`,
		`id="t-overview"`, `id="t-conversations"`, `id="t-storage"`, `id="t-delete"`, `id="t-import"`, `id="t-settings"`, `id="t-commands"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("shell lacks %s", want)
		}
	}
}
