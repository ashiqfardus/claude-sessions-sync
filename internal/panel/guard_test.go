package panel

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	usedLinkText   = "This link has already been used."
	refreshToShell = `<meta http-equiv="refresh" content="0;url=/">`
)

// newTestServer builds a server with one GET route and one POST route, listening on a
// random loopback port so the Host check has a real port to match.
func newTestServer(t *testing.T, now *time.Time) *Server {
	t.Helper()
	o := Options{Routes: []Route{
		{Method: http.MethodGet, Path: "/api/ping", Handler: func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "pong") }},
		{Method: http.MethodPost, Path: "/api/poke", Handler: func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "poked") }},
	}}
	if now != nil {
		o.Now = func() time.Time { return *now }
	}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return s
}

func (s *Server) host() string { return "127.0.0.1:" + strconv.Itoa(s.Port()) }

// do sends one request straight to the handler. hdr holds name, value pairs; a
// "Host" pair replaces the default Host, and a "Cookie" pair is sent as given.
func do(s *Server, method, path string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.Host = s.host()
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i] == "Host" {
			r.Host = hdr[i+1]
			continue
		}
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

// login swaps a fresh key for the session cookie and returns it as a Cookie header value.
func login(t *testing.T, s *Server) string {
	t.Helper()
	w := do(s, http.MethodGet, "/k/"+s.NewKey())
	if w.Code != http.StatusOK {
		t.Fatalf("/k/ gave %d", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == "cs_panel_"+strconv.Itoa(s.Port()) {
			return c.Name + "=" + c.Value
		}
	}
	t.Fatal("no session cookie")
	return ""
}

// post sends a POST with the cookie and the Origin and token the panel's own page sends.
func post(s *Server, cookie, path string, hdr ...string) *httptest.ResponseRecorder {
	base := []string{"Cookie", cookie, "Origin", "http://" + s.host(), "X-CS-Token", s.Token()}
	return do(s, http.MethodPost, path, append(base, hdr...)...)
}

func TestRefusesWrongHost(t *testing.T) {
	s := newTestServer(t, nil)
	cookie := login(t, s)
	other := "127.0.0.1:" + strconv.Itoa(s.Port()+1)
	for _, h := range []string{"evil.example:" + strconv.Itoa(s.Port()), other, ""} {
		if w := do(s, http.MethodGet, "/api/ping", "Host", h, "Cookie", cookie); w.Code != http.StatusForbidden {
			t.Errorf("Host %q: got %d, want 403", h, w.Code)
		}
		if w := do(s, http.MethodGet, "/k/"+s.NewKey(), "Host", h); w.Code != http.StatusForbidden {
			t.Errorf("Host %q on /k/: got %d, want 403", h, w.Code)
		}
	}
	// Both allowed names pass, in any case.
	for _, h := range []string{s.host(), "LocalHost:" + strconv.Itoa(s.Port())} {
		if w := do(s, http.MethodGet, "/api/ping", "Host", h, "Cookie", cookie); w.Code != http.StatusOK {
			t.Errorf("Host %q: got %d, want 200", h, w.Code)
		}
	}
}

func TestRefusesMissingCookie(t *testing.T) {
	s := newTestServer(t, nil)
	w := do(s, http.MethodGet, "/api/ping")
	if w.Code != http.StatusForbidden || w.Body.Len() != 0 {
		t.Errorf("/api/ping without cookie: %d %q, want a bare 403", w.Code, w.Body.String())
	}
	w = do(s, http.MethodGet, "/api/ping", "Cookie", "cs_panel_"+strconv.Itoa(s.Port())+"="+strings.Repeat("0", 64))
	if w.Code != http.StatusForbidden {
		t.Errorf("/api/ping with a wrong cookie: %d, want 403", w.Code)
	}
	w = do(s, http.MethodGet, "/")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), usedLinkText) {
		t.Errorf("/ without cookie: %d %q, want 403 with the plain page", w.Code, w.Body.String())
	}
	if w := do(s, http.MethodGet, "/api/ping", "Cookie", login(t, s)); w.Code != http.StatusOK || w.Body.String() != "pong" {
		t.Errorf("/api/ping with cookie: %d %q", w.Code, w.Body.String())
	}
}

func TestKeyWorksOnce(t *testing.T) {
	now := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	s := newTestServer(t, &now)
	var used []string
	s.OnKeyUsed(func(k string) { used = append(used, k) })

	key := s.NewKey()
	if len(key) != 64 {
		t.Fatalf("key %q is not 32 bytes of hex", key)
	}
	if w := do(s, http.MethodGet, "/k/"+strings.Repeat("0", 64)); w.Code != http.StatusForbidden {
		t.Errorf("wrong key: %d, want 403", w.Code)
	}
	if w := do(s, http.MethodGet, "/k/"); w.Code != http.StatusForbidden {
		t.Errorf("empty key: %d, want 403", w.Code)
	}

	w := do(s, http.MethodGet, "/k/"+key)
	if w.Code != http.StatusOK || w.Header().Get("Location") != "" || !strings.Contains(w.Body.String(), refreshToShell) {
		t.Fatalf("first use: %d %q, want 200 with a page refreshing to /", w.Code, w.Body.String())
	}
	if strings.Contains(strings.ToLower(w.Body.String()), "<script") {
		t.Errorf("the key page has a script: %q", w.Body.String())
	}
	if w.Header().Get("Content-Security-Policy") != CSP || w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("key page headers: %v", w.Header())
	}
	sc := w.Header().Get("Set-Cookie")
	for _, want := range []string{"cs_panel_" + strconv.Itoa(s.Port()) + "=", "Path=/", "HttpOnly", "SameSite=Strict"} {
		if !strings.Contains(sc, want) {
			t.Errorf("Set-Cookie %q lacks %q", sc, want)
		}
	}
	if len(used) != 1 || used[0] != key {
		t.Errorf("OnKeyUsed got %q, want [%s]", used, key)
	}

	w = do(s, http.MethodGet, "/k/"+key)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), usedLinkText) {
		t.Errorf("second use: %d %q, want 403 with the plain page", w.Code, w.Body.String())
	}
	if w.Header().Get("Set-Cookie") != "" {
		t.Error("a refused key set a cookie")
	}

	late := s.NewKey()
	now = now.Add(3 * time.Minute)
	if w := do(s, http.MethodGet, "/k/"+late); w.Code != http.StatusForbidden {
		t.Errorf("expired key: %d, want 403", w.Code)
	}
	if len(used) != 1 {
		t.Errorf("OnKeyUsed ran for a refused key: %q", used)
	}
}

func TestPostNeedsOriginAndToken(t *testing.T) {
	s := newTestServer(t, nil)
	cookie := login(t, s)
	origin := "http://" + s.host()
	cases := map[string][]string{
		"no Origin":   {"Cookie", cookie, "X-CS-Token", s.Token()},
		"evil Origin": {"Cookie", cookie, "Origin", "http://evil.example", "X-CS-Token", s.Token()},
		"no token":    {"Cookie", cookie, "Origin", origin},
		"wrong token": {"Cookie", cookie, "Origin", origin, "X-CS-Token", strings.Repeat("0", 64)},
		"no cookie":   {"Origin", origin, "X-CS-Token", s.Token()},
	}
	for name, hdr := range cases {
		if w := do(s, http.MethodPost, "/api/poke", hdr...); w.Code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, w.Code)
		}
	}
	if w := post(s, cookie, "/api/poke"); w.Code != http.StatusOK || w.Body.String() != "poked" {
		t.Errorf("all right: %d %q, want 200", w.Code, w.Body.String())
	}
}

func TestWrongMethod(t *testing.T) {
	s := newTestServer(t, nil)
	cookie := login(t, s)
	if w := do(s, http.MethodGet, "/api/poke", "Cookie", cookie); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/poke: %d, want 405", w.Code)
	}
	if w := post(s, cookie, "/api/ping"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/ping: %d, want 405", w.Code)
	}
	// A cross-site fetch that sets X-CS-Launch is preflighted, and the preflight fails.
	if w := do(s, http.MethodOptions, "/launch", "Origin", "http://evil.example"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("OPTIONS /launch: %d, want 405", w.Code)
	}
	if _, err := New(Options{Routes: []Route{{Method: http.MethodPut, Path: "/api/x"}}}); err == nil {
		t.Error("New accepted a PUT route")
	}
}

func TestCSPHeader(t *testing.T) {
	s := newTestServer(t, nil)
	cookie := login(t, s)
	for _, p := range []string{"/", "/static/index.html"} {
		w := do(s, http.MethodGet, p, "Cookie", cookie)
		if got := w.Header().Get("Content-Security-Policy"); got != CSP {
			t.Errorf("%s: CSP %q, want %q", p, got, CSP)
		}
	}
	// The template is never served raw: .html is not a static type.
	if w := do(s, http.MethodGet, "/static/index.html", "Cookie", cookie); w.Code != http.StatusNotFound {
		t.Errorf("/static/index.html: %d, want 404", w.Code)
	}
	if w := do(s, http.MethodGet, "/static/../static/index.html", "Cookie", cookie); w.Code != http.StatusNotFound {
		t.Errorf("/static/..: %d, want 404", w.Code)
	}
	// Every response carries the fixed headers, a refusal included.
	for _, w := range []*httptest.ResponseRecorder{
		do(s, http.MethodGet, "/api/ping", "Cookie", cookie),
		do(s, http.MethodGet, "/api/ping", "Host", "evil.example"),
	} {
		for k, v := range map[string]string{
			"X-Content-Type-Options":  "nosniff",
			"Referrer-Policy":         "no-referrer",
			"X-Frame-Options":         "DENY",
			"Content-Security-Policy": CSP,
			"Cache-Control":           "no-store",
		} {
			if got := w.Header().Get(k); got != v {
				t.Errorf("%s = %q, want %q", k, got, v)
			}
		}
	}
	// Conversation pages set their own policy; the panel's must not be added to it.
	if got := do(s, http.MethodGet, "/read/x/y", "Cookie", cookie).Header().Get("Content-Security-Policy"); got != "" {
		t.Errorf("/read/ got CSP %q, want none from the guard", got)
	}
}

func TestLaunchNeedsTheSecret(t *testing.T) {
	s := newTestServer(t, nil)
	for name, hdr := range map[string][]string{
		"no header":    nil,
		"wrong secret": {"X-CS-Launch", strings.Repeat("0", 64)},
		"with Origin":  {"X-CS-Launch", s.LaunchSecret(), "Origin", "http://" + s.host()},
		"wrong Host":   {"X-CS-Launch", s.LaunchSecret(), "Host", "evil.example:" + strconv.Itoa(s.Port())},
	} {
		if w := do(s, http.MethodPost, "/launch", hdr...); w.Code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, w.Code)
		}
	}
	w := do(s, http.MethodPost, "/launch", "X-CS-Launch", s.LaunchSecret())
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("right secret: %d, Cache-Control %q", w.Code, w.Header().Get("Cache-Control"))
	}
	var got struct{ Key string }
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got.Key) != 64 {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
	if w := do(s, http.MethodGet, "/k/"+got.Key); w.Code != http.StatusOK {
		t.Errorf("launch key first use: %d, want 200", w.Code)
	}
	if w := do(s, http.MethodGet, "/k/"+got.Key); w.Code != http.StatusForbidden {
		t.Errorf("launch key second use: %d, want 403", w.Code)
	}
}

func TestListensOnLoopbackOnly(t *testing.T) {
	s, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	a := l.Addr().(*net.TCPAddr)
	if !a.IP.Equal(net.IPv4(127, 0, 0, 1)) || a.Port == 0 || a.Port != s.Port() {
		t.Fatalf("listening on %v, Port() %d", a, s.Port())
	}
	if got, want := s.KeyURL("abc"), "http://127.0.0.1:"+strconv.Itoa(a.Port)+"/k/abc"; got != want {
		t.Errorf("KeyURL = %q, want %q", got, want)
	}
}

// End to end over the real socket: the key page's refresh, followed by the same client,
// reaches / with the cookie and gets the shell. (A Go client ignores SameSite; the
// file:// case it guards against was checked in Chrome.)
func TestRefreshLandsOnTheShell(t *testing.T) {
	s, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go http.Serve(l, s.Handler())
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	get := func(u string) (*http.Response, string) {
		res, err := c.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res, string(b)
	}

	res, body := get(s.KeyURL(s.NewKey()))
	i := strings.Index(body, `content="0;url=`)
	if res.StatusCode != http.StatusOK || i < 0 {
		t.Fatalf("key page: %d %q", res.StatusCode, body)
	}
	target := body[i+len(`content="0;url=`):]
	next, err := res.Request.URL.Parse(target[:strings.IndexByte(target, '"')])
	if err != nil {
		t.Fatal(err)
	}

	// The jar's cookies for / are what the client sends there; the guard refuses / without one.
	carried := false
	for _, ck := range jar.Cookies(next) {
		carried = carried || ck.Name == s.cookieName() && ck.Value == s.cookie
	}
	if !carried {
		t.Fatalf("the client has no session cookie for %s", next)
	}
	res, body = get(next.String())
	want := `<meta name="cs-token" content="` + s.Token() + `">`
	if res.StatusCode != http.StatusOK || next.Path != "/" || !strings.Contains(body, want) {
		t.Errorf("got %d at %s, body %q, want the shell with %s", res.StatusCode, next, body, want)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("shell Content-Type %q", ct)
	}
}

func TestRandomValuesDiffer(t *testing.T) {
	s, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, v := range []string{s.cookie, s.Token(), s.LaunchSecret(), s.NewKey(), s.NewKey()} {
		if len(v) != 64 || seen[v] {
			t.Errorf("value %q is short or repeated", v)
		}
		seen[v] = true
	}
}
