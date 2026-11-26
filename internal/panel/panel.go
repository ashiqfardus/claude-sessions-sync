// Package panel is the browser panel's loopback server: the request guard, the
// one-time key, the redirect file and browser opener, and the embedded page. It knows
// nothing about commands; the routes it serves come from its caller.
package panel

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CSP is the panel's Content-Security-Policy: only its own scripts, styles and fonts.
const CSP = "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self' data:; connect-src 'self'; form-action 'none'; frame-ancestors 'none'; base-uri 'none'"

// PageCSP is the policy for conversation pages under /read/: no script at all.
const PageCSP = "sandbox; default-src 'none'; style-src 'unsafe-inline'"

// keyLife is how long an unused one-time key stays valid.
const keyLife = 2 * time.Minute

const usedLink = "This link has already been used. Run `claude-sessions ui` again, or open the claude-sessions panel shortcut."

// Route is one endpoint. Method is GET or POST; Prefix matches every path under Path.
type Route struct {
	Method, Path string
	Prefix       bool
	Handler      http.HandlerFunc
}

type Options struct {
	Routes []Route
	Logf   func(string, ...any)
	Now    func() time.Time
}

type Server struct {
	o      Options
	routes []Route
	shell  *template.Template
	port   int

	cookie, token, secret string
	proofKey              string // signs /launch replies; never sent, unlike secret

	mu        sync.Mutex
	keys      map[string]time.Time // unused one-time keys and when they were made
	onKeyUsed func(string)
}

// New makes the server and its three per-run secrets: the cookie value, the token
// the page sends on every POST, and the launch secret a second `ui` uses.
func New(o Options) (*Server, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	shell, err := template.ParseFS(Static, "static/index.html")
	if err != nil {
		return nil, err
	}
	s := &Server{o: o, shell: shell, cookie: randHex(), token: randHex(), secret: randHex(), proofKey: randHex(), keys: map[string]time.Time{}}
	s.routes = []Route{
		{Method: http.MethodGet, Path: "/k/", Prefix: true, Handler: s.useKey},
		{Method: http.MethodPost, Path: "/launch", Handler: s.launch},
		{Method: http.MethodGet, Path: "/", Handler: s.page},
		{Method: http.MethodGet, Path: "/static/", Prefix: true, Handler: s.static},
	}
	for _, r := range o.Routes {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			return nil, fmt.Errorf("panel: route %s has method %q, want GET or POST", r.Path, r.Method)
		}
		// The guard skips the cookie on /launch and under /k/, so no caller route may answer there.
		if r.Path == "/launch" || strings.HasPrefix(r.Path, "/k/") || r.Prefix && (strings.HasPrefix("/launch", r.Path) || strings.HasPrefix("/k/", r.Path)) {
			return nil, fmt.Errorf("panel: route %s overlaps /k/ or /launch", r.Path)
		}
	}
	s.routes = append(s.routes, o.Routes...)
	return s, nil
}

func randHex() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // no system randomness: a guessable key must never be handed out
	}
	return hex.EncodeToString(b)
}

// Listen listens on 127.0.0.1 only; port 0 picks a free port.
func (s *Server) Listen(port int) (net.Listener, error) {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	s.port = l.Addr().(*net.TCPAddr).Port
	return l, nil
}

func (s *Server) Port() int             { return s.port }
func (s *Server) Token() string         { return s.token }
func (s *Server) LaunchSecret() string  { return s.secret }
func (s *Server) ProofKey() string      { return s.proofKey }
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serve) }

func (s *Server) KeyURL(key string) string {
	return "http://127.0.0.1:" + strconv.Itoa(s.port) + "/k/" + key
}

// OnKeyUsed sets the function called with each key as it is swapped for the cookie,
// before the response is sent.
func (s *Server) OnKeyUsed(f func(key string)) {
	s.mu.Lock()
	s.onKeyUsed = f
	s.mu.Unlock()
}

// NewKey returns a single-use key that expires after keyLife unused.
func (s *Server) NewKey() string {
	k := randHex()
	s.mu.Lock()
	s.keys[k] = s.o.Now()
	s.mu.Unlock()
	return k
}

func (s *Server) cookieName() string { return "cs_panel_" + strconv.Itoa(s.port) }

// useKey burns a live key and swaps it for the session cookie. A wrong key burns nothing.
func (s *Server) useKey(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/k/")
	now := s.o.Now()
	ok := false
	s.mu.Lock()
	for k, made := range s.keys {
		match, live := same(k, key), now.Sub(made) < keyLife
		ok = ok || match && live
		if match || !live {
			delete(s.keys, k)
		}
	}
	f := s.onKeyUsed
	s.mu.Unlock()
	if !ok {
		plain(w, http.StatusForbidden, usedLink)
		return
	}
	if f != nil {
		f(key)
	}
	http.SetCookie(w, &http.Cookie{Name: s.cookieName(), Value: s.cookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	// A Strict cookie is not sent on a redirect that starts from a file:// page, so the hop to / starts from this page.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, keyPage)
}

const keyPage = "<!doctype html>\n<meta charset=\"utf-8\">\n<meta http-equiv=\"refresh\" content=\"0;url=/\">\n<title>claude-sessions panel</title>\n<p><a href=\"/\">Open the claude-sessions panel</a></p>\n"

// page is the shell, with the per-run token for the page's POSTs.
func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	var b bytes.Buffer
	if err := s.shell.Execute(&b, struct{ Token string }{s.token}); err != nil {
		s.o.Logf("PANEL: page shell: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b.Bytes())
}

var staticTypes = map[string]string{
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".woff2": "font/woff2",
	".txt":   "text/plain; charset=utf-8",
	".svg":   "image/svg+xml",
}

// static serves the embedded files of the listed types. Anything else, including the
// shell's template, is 404; embed.FS refuses `..` and other invalid paths itself.
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/static/")
	ct, ok := staticTypes[path.Ext(name)]
	b, err := Static.ReadFile("static/" + name)
	if !ok || err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Write(b)
}

func plain(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	io.WriteString(w, msg+"\n")
}
