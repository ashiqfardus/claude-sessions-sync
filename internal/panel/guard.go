package panel

import (
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"
)

func same(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// serve checks every request, in order: the Host (against DNS rebinding), the session
// cookie (except for the key swap and /launch), then Origin and token on anything but
// GET. Only then does it route.
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	if !strings.HasPrefix(p, "/read/") { // conversation pages set PageCSP themselves
		h.Set("Content-Security-Policy", CSP)
	}
	if !strings.HasPrefix(p, "/static/") {
		h.Set("Cache-Control", "no-store")
	}

	port := strconv.Itoa(s.port)
	if !strings.EqualFold(r.Host, "127.0.0.1:"+port) && !strings.EqualFold(r.Host, "localhost:"+port) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	// /k/ has no cookie yet; /launch has its own checks (secret, no Origin) instead.
	if !strings.HasPrefix(p, "/k/") && p != "/launch" {
		if c, err := r.Cookie(s.cookieName()); err != nil || !same(c.Value, s.cookie) {
			if p == "/" {
				plain(w, http.StatusForbidden, usedLink)
			} else {
				w.WriteHeader(http.StatusForbidden)
			}
			return
		}
		// Every method but GET, not only POST, so no route method can slip past.
		if r.Method != http.MethodGet && (r.Header.Get("Origin") != "http://"+r.Host || !same(r.Header.Get("X-CS-Token"), s.token)) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
	}

	found := false
	for _, rt := range s.routes {
		if p == rt.Path || rt.Prefix && strings.HasPrefix(p, rt.Path) {
			if r.Method == rt.Method {
				rt.Handler(w, r)
				return
			}
			found = true
		}
	}
	if found {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	http.NotFound(w, r)
}
