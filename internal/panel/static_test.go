package panel

import (
	"crypto/sha256"
	"encoding/hex"
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
	source, err := Static.ReadFile("static/fonts/SOURCE.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fontFiles {
		b, err := Static.ReadFile("static/fonts/" + f)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if !strings.HasPrefix(string(b), "wOF2") || len(b) >= 30*1024 {
			t.Errorf("%s: %d bytes, starts %q; want a woff2 under 30 KB", f, len(b), b[:min(4, len(b))])
		}
		// SOURCE.txt lists each file on its own line with the SHA-256 of IBM's copy.
		sum := sha256.Sum256(b)
		recorded := false
		for _, line := range strings.Split(string(source), "\n") {
			recorded = recorded || strings.HasPrefix(strings.TrimSpace(line), f+" ") && strings.Contains(line, hex.EncodeToString(sum[:]))
		}
		if !recorded {
			t.Errorf("%s: SHA-256 %x is not the one SOURCE.txt records", f, sum)
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

const readmeURL = "https://github.com/ashiqfardus/claude-sessions-sync#readme"

// svgNS is the SVG namespace name, which is never fetched (Ruling P28). Only its quoted
// form is allowed, so a longer URL that starts with it is still caught.
var svgNS = regexp.MustCompile("http://www\\.w3\\.org/2000/svg([\"'`])")

// remoteLoads lists what in a file the browser loads could fetch from another computer.
func remoteLoads(name, s string) []string {
	if name == "static/js/settings.js" {
		s = strings.ReplaceAll(s, readmeURL, "")
	}
	s = svgNS.ReplaceAllString(s, "$1")
	var found []string
	for _, bad := range []string{"http://", "https://", "@import"} {
		if strings.Contains(s, bad) {
			found = append(found, bad)
		}
	}
	return found
}

// Only what the browser loads is scanned (Ruling P6): OFL.txt and SOURCE.txt hold
// URLs as text and are never fetched.
func TestStaticHasNoRemoteLoads(t *testing.T) {
	for p, s := range staticFiles(t) {
		switch path.Ext(p) {
		case ".html", ".css", ".js", ".svg":
			if found := remoteLoads(p, s); len(found) > 0 {
				t.Errorf("%s contains %q", p, found)
			}
		}
	}
}

func TestRemoteScanAllowsOnlyTheSVGNamespace(t *testing.T) {
	for _, c := range []struct {
		name, text string
		ok         bool
	}{
		{"static/index.html", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">`, true},
		{"static/js/app.js", `document.createElementNS('http://www.w3.org/2000/svg', 'path')`, true},
		{"static/js/app.js", "document.createElementNS(`http://www.w3.org/2000/svg`, 'path')", true},
		{"static/js/app.js", `import x from "http://www.w3.org/2000/svg/x.js"`, false},
		{"static/js/app.js", `fetch("http://www.w3.org/2000/svg.evil.example/")`, false},
		{"static/index.html", `<svg xmlns="https://www.w3.org/2000/svg">`, false},
		{"static/panel.css", `.a{background:url("http://example.com/a.png")}`, false},
		{"static/panel.css", `@import "fonts.css";`, false},
		{"static/js/settings.js", `a.href = "` + readmeURL + `"`, true},
		{"static/js/app.js", `a.href = "` + readmeURL + `"`, false},
	} {
		if found := remoteLoads(c.name, c.text); (len(found) == 0) != c.ok {
			t.Errorf("%s %s: found %q, want allowed=%v", c.name, c.text, found, c.ok)
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
		`href="#main">Skip to content</a>`, `<nav id="rail" aria-label="Panel" class="rail">`, `role="separator" aria-orientation="vertical" aria-controls="rail"`,
		`<main id="main"`, `id="toasts" role="status"`, `id="dialogs"`,
		`id="t-overview"`, `id="t-conversations"`, `id="t-storage"`, `id="t-delete"`, `id="t-import"`, `id="t-settings"`, `id="t-commands"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("shell lacks %s", want)
		}
	}
}

// pageCore is what Task 18's modules export for every screen.
var pageCore = map[string][]string{
	"static/js/app.js":     {"h", "icon", "api", "follow", "toast", "modal", "confirmDialog", "register", "noArchive", "when"},
	"static/js/rail.js":    {"rail"},
	"static/js/combo.js":   {"combo"},
	"static/js/pager.js":   {"pager"},
	"static/js/folders.js": {"chooseFolder"},
}

var (
	jsExport      = regexp.MustCompile(`export\s+(?:async\s+)?(?:function\*?|const|let|var|class)\s+([A-Za-z_$][\w$]*)`)
	jsImportPath  = regexp.MustCompile(`(?:\bfrom|\bimport)\s*\(?\s*['"](\.[^'"]+)['"]`)
	jsNamedImport = regexp.MustCompile(`import\s*\{([^}]*)\}\s*from\s*['"](\.[^'"]+)['"]`)
)

// A module that fails to link stops every script on the page, with no message to the person.
func TestPageModulesLink(t *testing.T) {
	files := staticFiles(t)
	exports := map[string]map[string]bool{}
	for p, s := range files {
		if path.Ext(p) == ".js" {
			exports[p] = map[string]bool{}
			for _, m := range jsExport.FindAllStringSubmatch(s, -1) {
				exports[p][m[1]] = true
			}
		}
	}
	for p, names := range pageCore {
		if exports[p] == nil {
			t.Errorf("%s is missing", p)
			continue
		}
		for _, n := range names {
			if !exports[p][n] {
				t.Errorf("%s does not export %s", p, n)
			}
		}
	}
	for p, s := range files {
		if path.Ext(p) != ".js" {
			continue
		}
		for _, m := range jsImportPath.FindAllStringSubmatch(s, -1) {
			if _, ok := files[path.Join(path.Dir(p), m[1])]; !ok {
				t.Errorf("%s imports %s, which is not embedded", p, m[1])
			}
		}
		for _, m := range jsNamedImport.FindAllStringSubmatch(s, -1) {
			from := path.Join(path.Dir(p), m[2])
			for _, n := range strings.Split(m[1], ",") {
				if f := strings.Fields(n); len(f) > 0 && exports[from] != nil && !exports[from][f[0]] {
					t.Errorf("%s imports %s from %s, which does not export it", p, f[0], m[2])
				}
			}
		}
	}
}

// The router's screens are exactly the shell's templates, and each has the h1 that
// navigation moves focus to.
func TestRouterMatchesTheShell(t *testing.T) {
	files := staticFiles(t)
	m := regexp.MustCompile(`SCREENS\s*=\s*\[([^\]]*)\]`).FindStringSubmatch(files["static/js/app.js"])
	if m == nil {
		t.Fatal("app.js has no SCREENS list")
	}
	routed := map[string]bool{}
	for _, q := range regexp.MustCompile(`'([a-z]+)'`).FindAllStringSubmatch(m[1], -1) {
		routed[q[1]] = true
	}
	shell := files["static/index.html"]
	templates := regexp.MustCompile(`(?s)<template id="t-([a-z]+)">(.*?)</template>`).FindAllStringSubmatch(shell, -1)
	if len(templates) == 0 {
		t.Fatal("index.html has no t-<screen> templates")
	}
	for _, tm := range templates {
		if !routed[tm[1]] {
			t.Errorf("template t-%s has no route", tm[1])
		}
		delete(routed, tm[1])
		if !strings.Contains(tm[2], `<h1 tabindex="-1">`) {
			t.Errorf("template t-%s has no <h1 tabindex=\"-1\"> to move focus to", tm[1])
		}
	}
	for name := range routed {
		t.Errorf("route %s has no template t-%s", name, name)
	}
}

// The sidebar's values (Ruling P11) agree across rail.js, the separator's ARIA range
// and the stylesheet's first width.
func TestRailMatchesTheSeparator(t *testing.T) {
	files := staticFiles(t)
	num := func(file, pattern string) string {
		m := regexp.MustCompile(pattern).FindStringSubmatch(files[file])
		if m == nil {
			t.Errorf("%s: no match for %s", file, pattern)
			return ""
		}
		return m[1]
	}
	js, shell := "static/js/rail.js", "static/index.html"
	for _, c := range [][2]string{
		{num(js, `\bRAIL = (\d+)`), "200"}, {num(js, `\bMIN = (\d+)`), "64"}, {num(js, `\bMAX = (\d+)`), "320"},
		{num(js, `\bSNAP = (\d+)`), "140"}, {num(js, `\bSTEP = (\d+)`), "16"},
		{num(shell, `aria-valuemin="(\d+)"`), "64"}, {num(shell, `aria-valuemax="(\d+)"`), "320"},
		{num(shell, `aria-valuenow="(\d+)"`), "200"}, {num("static/panel.css", `\.rail\{width:(\d+)px\}`), "200"},
	} {
		if c[0] != c[1] {
			t.Errorf("got %s, want %s", c[0], c[1])
		}
	}
	if !strings.Contains(files[js], `'cs.railWidth'`) {
		t.Error("rail.js does not keep the width in localStorage['cs.railWidth']")
	}
}

// Rules the CSP or a ruling makes silent when broken: the browser ignores a style or
// on* attribute set from script, storage can throw, and an EventSource left open
// reconnects every few seconds (Ruling P20).
func TestPageScriptsKeepTheRules(t *testing.T) {
	files := staticFiles(t)
	for p, s := range files {
		if path.Ext(p) != ".js" {
			continue
		}
		if m := regexp.MustCompile(`setAttribute\(\s*['"](style|on)|javascript:`).FindString(s); m != "" {
			t.Errorf("%s contains %q", p, m)
		}
		for i, line := range strings.Split(s, "\n") {
			if strings.Contains(line, "localStorage") && !strings.Contains(line, "try {") {
				t.Errorf("%s:%d uses localStorage outside a one-line try/catch", p, i+1)
			}
		}
		if strings.Contains(s, "new EventSource(") && !strings.Contains(s, ".close()") {
			t.Errorf("%s opens an EventSource and never closes it", p)
		}
	}
	app := files["static/js/app.js"]
	for _, want := range []string{`'X-CS-Token'`, `meta[name="cs-token"]`, `credentials: 'same-origin'`, `'/api/jobs/events?id='`} {
		if !strings.Contains(app, want) {
			t.Errorf("app.js lacks %s", want)
		}
	}
}
