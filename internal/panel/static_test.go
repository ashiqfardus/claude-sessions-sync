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

// readmeLine is the one place the README URL may appear: app.js's constant.
const readmeLine = `export const README = '` + readmeURL + `'`

// svgNS is the SVG namespace name, which is never fetched (Ruling P28). Only its quoted
// form is allowed, so a longer URL that starts with it is still caught.
var svgNS = regexp.MustCompile("http://www\\.w3\\.org/2000/svg([\"'`])")

// remoteLoads lists what in a file the browser loads could fetch from another computer.
func remoteLoads(name, s string) []string {
	if name == "static/js/app.js" { // only its README constant, the panel's one external link
		s = strings.Replace(s, readmeLine, "", 1)
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
		{"static/js/app.js", readmeLine, true},
		{"static/js/app.js", readmeLine + "\na.href = '" + readmeURL + "'", false},
		{"static/js/settings.js", `a.href = "` + readmeURL + `"`, false},
		{"static/js/settings.js", readmeLine, false},
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
		`<link rel="stylesheet" href="/static/panel.css">`, `<script type="module" src="/static/js/app.js">`, `<link rel="icon" href="/static/favicon.svg">`,
		`href="#main">Skip to content</a>`, `<nav id="rail" aria-label="Panel" class="rail">`, `role="separator" aria-orientation="vertical" aria-controls="rail"`,
		`<main id="main"`, `id="toasts" role="status"`, `id="dialogs"`,
		`id="t-overview"`, `id="t-conversations"`, `id="t-storage"`, `id="t-delete"`, `id="t-import"`, `id="t-settings"`, `id="t-commands"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("shell lacks %s", want)
		}
	}
	// The icon is named, so the browser never asks for /favicon.ico (a 404 in the console).
	if w := do(s, http.MethodGet, "/static/favicon.svg", "Cookie", login(t, s)); w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/svg+xml" {
		t.Errorf("GET /static/favicon.svg gave %d %q", w.Code, w.Header().Get("Content-Type"))
	}
}

// pageCore is what Task 18's modules export for every screen.
var pageCore = map[string][]string{
	"static/js/app.js":     {"h", "icon", "api", "follow", "toast", "modal", "confirmDialog", "register", "noArchive", "when", "reducedMotion", "allowExternal", "README", "GENERIC"},
	"static/js/rail.js":    {"rail"},
	"static/js/combo.js":   {"combo"},
	"static/js/pager.js":   {"pager"},
	"static/js/folders.js": {"chooseFolder"},
}

var (
	jsExport      = regexp.MustCompile(`export\s+(?:async\s+)?(?:function\*?|const|let|var|class)\s+([A-Za-z_$][\w$]*)`)
	jsExportList  = regexp.MustCompile(`export\s*\{([^}]*)\}`)
	jsDoneCloses  = regexp.MustCompile(`addEventListener\(\s*'done'\s*,[^}]*\.close\(\)`)
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
			for _, m := range jsExportList.FindAllStringSubmatch(s, -1) {
				for _, n := range strings.Split(m[1], ",") {
					if f := strings.Fields(n); len(f) > 0 {
						exports[p][f[len(f)-1]] = true // `a as b` exports b
					}
				}
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

// The router imports js/<screen>.js on a screen's first visit and expects it to register
// that same name (Ruling P31); a register call elsewhere would never be imported.
func TestScreensRegister(t *testing.T) {
	files := staticFiles(t)
	m := regexp.MustCompile(`SCREENS\s*=\s*\[([^\]]*)\]`).FindStringSubmatch(files["static/js/app.js"])
	if m == nil {
		t.Fatal("app.js has no SCREENS list")
	}
	routed := map[string]bool{}
	for _, q := range regexp.MustCompile(`'([a-z]+)'`).FindAllStringSubmatch(m[1], -1) {
		routed[q[1]] = true
	}
	call := regexp.MustCompile(`\bregister\(\s*'([a-z]+)'`)
	for p, s := range files {
		if path.Ext(p) != ".js" || p == "static/js/app.js" {
			continue
		}
		for _, c := range call.FindAllStringSubmatch(s, -1) {
			if !routed[c[1]] || p != "static/js/"+c[1]+".js" {
				t.Errorf("%s registers %q, which the router never imports from there", p, c[1])
			}
		}
	}
	for name := range routed {
		s, ok := files["static/js/"+name+".js"]
		switch {
		case !ok:
			t.Errorf("js/%s.js is missing", name)
		case !strings.Contains(s, "register('"+name+"'"):
			t.Errorf("js/%s.js does not register('%s', ...)", name, name)
		}
	}
}

// Every screen is in the rail's menu: by its own link, or by the link of its section
// (app.js's SECTION), which must be a rail link itself. Each link is named for the
// folded rail, with an icon in the rail's style; the phone's Menu button opens them.
func TestRailListsEveryScreen(t *testing.T) {
	files := staticFiles(t)
	app, shell := files["static/js/app.js"], files["static/index.html"]
	m := regexp.MustCompile(`SCREENS\s*=\s*\[([^\]]*)\]`).FindStringSubmatch(app)
	sec := regexp.MustCompile(`SECTION\s*=\s*\{([^}]*)\}`).FindStringSubmatch(app)
	nav := regexp.MustCompile(`(?s)<nav id="rail".*?</nav>`).FindString(shell)
	if m == nil || sec == nil || nav == "" {
		t.Fatal("app.js has no SCREENS or SECTION, or index.html no rail")
	}
	links := map[string]bool{}
	for _, a := range regexp.MustCompile(`<a href="#/([a-z]+)"[^>]*>(.*?)</a>`).FindAllStringSubmatch(nav, -1) {
		links[a[1]] = true
		if !strings.Contains(a[0], `aria-label="`) || !strings.Contains(a[2], `viewBox="0 0 24 24"`) || !strings.Contains(a[2], `stroke-width="1.8"`) {
			t.Errorf("rail link #/%s lacks its aria-label or its 24-grid, 1.8-stroke icon", a[1])
		}
	}
	section := map[string]string{}
	for _, p := range regexp.MustCompile(`(\w+):\s*'([a-z]+)'`).FindAllStringSubmatch(sec[1], -1) {
		section[p[1]] = p[2]
	}
	for _, q := range regexp.MustCompile(`'([a-z]+)'`).FindAllStringSubmatch(m[1], -1) {
		name := q[1]
		if to, ok := section[name]; ok {
			if !links[to] {
				t.Errorf("screen %s marks section %s, which has no rail link", name, to)
			}
		} else if !links[name] {
			t.Errorf("screen %s has no rail link and no section", name)
		}
	}
	if !links["commands"] {
		t.Error("Commands is not in the rail")
	}
	if !strings.Contains(nav, `aria-controls="rail-menu"`) || !strings.Contains(nav, `id="rail-menu"`) || !strings.Contains(nav, `aria-expanded="false"`) {
		t.Error("the rail lacks the phone's Menu button controlling #rail-menu")
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
		if strings.Contains(s, "new EventSource(") && !jsDoneCloses.MatchString(s) {
			t.Errorf("%s opens an EventSource whose 'done' handler does not close it first", p)
		}
	}
	app := files["static/js/app.js"]
	for _, want := range []string{`'X-CS-Token'`, `meta[name="cs-token"]`, `credentials: 'same-origin'`, `'/api/jobs/events?id='`} {
		if !strings.Contains(app, want) {
			t.Errorf("app.js lacks %s", want)
		}
	}
}

// The README is the panel's only external link: app.js holds it as README, and
// allowExternal is only ever called as allowExternal(README) (it also throws on any
// other URL): never with a variable, never renamed, never taken from a dynamic import.
// It is called exactly once, by Settings for its About link (Ruling P32).
func TestOnlyTheReadmeIsExternal(t *testing.T) {
	files := staticFiles(t)
	if !strings.Contains(files["static/js/app.js"], readmeLine) {
		t.Errorf("app.js lacks %s", readmeLine)
	}
	calls := 0
	for p, s := range files {
		if path.Ext(p) != ".js" {
			continue
		}
		if externalMisuse(p, s) {
			t.Errorf("%s uses allowExternal other than as allowExternal(README)", p)
		}
		if p == "static/js/app.js" { // the definition, whose comment names the call
			continue
		}
		if n := len(readmeCall.FindAllString(s, -1)); n > 0 && p != "static/js/settings.js" {
			t.Errorf("%s calls allowExternal(README); only Settings' About link may", p)
		} else {
			calls += n
		}
	}
	if calls != 1 {
		t.Errorf("settings.js calls allowExternal(README) %d times, want exactly once", calls)
	}
}

var readmeCall = regexp.MustCompile(`\ballowExternal\(README\)`)

// externalMisuse reports any allowExternal left once its allowed forms are removed:
// app.js's definition, a plain entry in a static import list, and allowExternal(README).
func externalMisuse(name, s string) bool {
	if name == "static/js/app.js" {
		s = strings.Replace(s, "export function allowExternal(url)", "", 1)
	}
	for _, m := range jsNamedImport.FindAllStringSubmatch(s, -1) {
		var kept []string
		for _, e := range strings.Split(m[1], ",") {
			if f := strings.Fields(e); len(f) != 1 || f[0] != "allowExternal" { // `allowExternal as x` stays, and fails
				kept = append(kept, e)
			}
		}
		s = strings.Replace(s, m[0], strings.Replace(m[0], m[1], strings.Join(kept, ","), 1), 1)
	}
	return strings.Contains(readmeCall.ReplaceAllString(s, ""), "allowExternal")
}

func TestExternalMisuseCases(t *testing.T) {
	for _, c := range []struct {
		name, src string
		bad       bool
	}{
		{"static/js/settings.js", "import { h, allowExternal, README } from './app.js'\nallowExternal(README)", false},
		{"static/js/settings.js", "import { h } from './app.js'", false},
		{"static/js/app.js", "export function allowExternal(url) {}", false},
		{"static/js/settings.js", "export function allowExternal(url) {}", true},
		{"static/js/settings.js", "import { allowExternal, README } from './app.js'\nallowExternal(url)", true},
		{"static/js/settings.js", "import { allowExternal } from './app.js'\nconst f = allowExternal", true},
		{"static/js/settings.js", "import { allowExternal as x } from './app.js'\nx(url)", true},
		{"static/js/settings.js", "import { h, allowExternal as allow } from './app.js'", true},
		{"static/js/pager.js", "export { allowExternal as x } from './app.js'", true},
		{"static/js/settings.js", "const { allowExternal } = await import('./app.js')\nallowExternal(README)", true},
		{"static/js/settings.js", "const { allowExternal: x } = await import('./app.js')", true},
		{"static/js/settings.js", "import('./app.js').then(({ allowExternal }) => allowExternal(README))", true},
		{"static/js/settings.js", "import * as app from './app.js'\napp.allowExternal(url)", true},
	} {
		if got := externalMisuse(c.name, c.src); got != c.bad {
			t.Errorf("%s: %q gave misuse=%v, want %v", c.name, c.src, got, c.bad)
		}
	}
}

// h() refuses every attribute that can load or link to another computer.
func TestHRefusesRemoteAttributes(t *testing.T) {
	app := staticFiles(t)["static/js/app.js"]
	m := regexp.MustCompile(`const REFUSED = \[([^\]]*)\]`).FindStringSubmatch(app)
	if m == nil {
		t.Fatal("app.js has no REFUSED list")
	}
	for _, name := range []string{"style", "srcdoc", "ping", "data", "poster", "background", "manifest", "codebase", "archive", "formaction", "action", "srcset", "imagesrcset", "xlink:href"} {
		if !strings.Contains(m[1], "'"+name+"'") {
			t.Errorf("h() does not refuse %s", name)
		}
	}
	// data-* never loads, so data-src is the one name ending in src it lets through.
	for _, want := range []string{"new URL(", "location.origin", "location.protocol", "endsWith('src')", "startsWith('data-')", "toLowerCase()"} {
		if !strings.Contains(app, want) {
			t.Errorf("app.js's URL gate lacks %s", want)
		}
	}
}

// Each screen module registers the name the router imports it by (Ruling P31), and every
// slot('<name>') it reads is a data-slot in its own template: a missing one stops the
// screen with only the generic sentence, and index.html is merged by hand.
func TestScreensRegisterTheirName(t *testing.T) {
	files := staticFiles(t)
	m := regexp.MustCompile(`SCREENS\s*=\s*\[([^\]]*)\]`).FindStringSubmatch(files["static/js/app.js"])
	if m == nil {
		t.Fatal("app.js has no SCREENS list")
	}
	shell := files["static/index.html"]
	for _, q := range regexp.MustCompile(`'([a-z]+)'`).FindAllStringSubmatch(m[1], -1) {
		name := q[1]
		js, ok := files["static/js/"+name+".js"]
		if !ok {
			t.Errorf("js/%s.js is missing", name)
			continue
		}
		if !strings.Contains(js, "register('"+name+"',") {
			t.Errorf("js/%s.js does not call register('%s', ...)", name, name)
		}
		tm := regexp.MustCompile(`(?s)<template id="t-` + name + `">(.*?)</template>`).FindStringSubmatch(shell)
		if tm == nil {
			continue // TestRouterMatchesTheShell reports it
		}
		for _, s := range regexp.MustCompile(`\bslot\('([a-z-]+)'\)`).FindAllStringSubmatch(js, -1) {
			if !strings.Contains(tm[1], `data-slot="`+s[1]+`"`) {
				t.Errorf("js/%s.js reads slot('%s'), which t-%s does not have", name, s[1], name)
			}
		}
	}
}

// The archive folder is chosen in one place, chooseArchive, which confirms a change with
// the old and new folders (Ruling P62): a second chooser could re-point the archive on
// one click. The no-archive state and Settings' Change folder both go through it.
func TestArchiveFolderIsChosenInOnePlace(t *testing.T) {
	files := staticFiles(t)
	for p, s := range files {
		want := 0
		if p == "static/js/app.js" {
			want = 1
		}
		if n := strings.Count(s, "'Choose the archive folder'"); path.Ext(p) == ".js" && n != want {
			t.Errorf("%s opens the archive folder chooser %d times; only chooseArchive in app.js may", p, n)
		}
	}
	app := files["static/js/app.js"]
	if !regexp.MustCompile(`async function setArchive[^}]*chooseArchive\(`).MatchString(app) {
		t.Error("app.js setArchive does not choose through chooseArchive")
	}
	if !strings.Contains(files["static/js/settings.js"], "chooseArchive(") {
		t.Error("settings.js Change folder does not choose through chooseArchive")
	}
}

// On a wide monitor every screen takes the full width (no cap on .main), and Settings
// sets its sections in two columns whose DOM order is the order they read in: down the
// first column, then the second, so keyboard order follows what is seen.
func TestScreensUseTheFullWidth(t *testing.T) {
	files := staticFiles(t)
	css := files["static/panel.css"]
	if m := regexp.MustCompile(`\.main\{[^}]*\}`).FindString(css); m == "" || strings.Contains(m, "max-width") || !strings.Contains(m, "container:main/inline-size") {
		t.Errorf("panel.css .main is %q: want no max-width, and the main container the screens query", m)
	}
	if !strings.Contains(css, "@container main (min-width:1200px){.board{display:grid") {
		t.Error("panel.css does not set Settings' board in two columns on a wide screen")
	}
	shell := files["static/index.html"]
	i := strings.Index(shell, `<template id="t-settings">`)
	settings := shell[i : i+strings.Index(shell[i:], "</template>")]
	if n := strings.Count(settings, `<div class="board-col">`); n != 2 {
		t.Errorf("Settings has %d board columns, want 2", n)
	}
	var order []string
	for _, m := range regexp.MustCompile(`<section aria-labelledby="([a-z-]+)-h"|<div class="board-col">`).FindAllStringSubmatch(settings, -1) {
		if m[1] == "" {
			m[1] = "|"
		}
		order = append(order, m[1])
	}
	if got, want := strings.Join(order, " "), "| arch comp work map | auto-backup prof pages full log"; got != want {
		t.Errorf("Settings reads %q, want %q", got, want)
	}
}

// On a phone the tables of Conversations, Bring in, Settings and Commands are one card per
// row (panel.css's table.cards, labelled by app.js); a table left out scrolls sideways in
// its box instead. Conversations' Select all says so in words, which the cards show.
func TestTablesAreCardsOnAPhone(t *testing.T) {
	files := staticFiles(t)
	shell := files["static/index.html"]
	for _, name := range []string{"conversations", "import", "settings"} {
		i := strings.Index(shell, `<template id="t-`+name+`">`)
		if i < 0 {
			t.Fatalf("index.html has no template for %s", name)
		}
		tpl := shell[i : i+strings.Index(shell[i:], "</template>")]
		tables := regexp.MustCompile(`<table class="([^"]*)"`).FindAllStringSubmatch(tpl, -1)
		if len(tables) == 0 {
			t.Errorf("%s has no table", name)
		}
		for _, m := range tables {
			if !strings.Contains(" "+m[1]+" ", " cards ") {
				t.Errorf("%s's table %q is not marked cards", name, m[1])
			}
		}
	}
	if !strings.Contains(files["static/js/commands.js"], `h('table', { class: 'commands-table cards' }`) {
		t.Error("commands.js's tables are not marked cards")
	}
	if !regexp.MustCompile(`<label class="tick"><input type="checkbox" class="check" data-slot="select-all"><span class="sr">Select all shown</span></label>`).MatchString(shell) {
		t.Error("Conversations' Select all lacks its words")
	}
}
