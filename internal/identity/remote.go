// Package identity resolves which local folder an archived project belongs to.
//
// The git remote is the strongest cross-machine identity available, so it is read
// for every candidate folder found during a scan.
package identity

import (
	"bufio"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Remote returns the origin URL of the repository at dir, or "" if there is none.
//
// SECURITY: this parses .git/config directly and NEVER executes git.
//
// Running `git -C <dir> ...` inside a directory you did not create is arbitrary code
// execution: a repository's own .git/config can set core.fsmonitor, core.pager,
// core.sshCommand or an alias, and git will run them. This tool scans whole drives
// looking for candidate project folders, so it would be running git inside every
// repository the user has ever cloned - including any they pulled from a stranger.
//
// One string is needed. A config parser cannot execute anything.
//
// dir is often a path another machine recorded, so it must be Local before it is
// opened: a network path here would make every push connect to that host.
func Remote(dir string) string {
	if !Local(dir) {
		return ""
	}
	f, err := os.Open(filepath.Join(dir, ".git", "config"))
	if err != nil {
		// A worktree or submodule has .git as a file pointing elsewhere. Following it
		// is possible but is deliberately not done here: the extra reach is not worth
		// the extra surface, and a missing remote only costs a fallback to the
		// folder-name match.
		return ""
	}
	defer f.Close()

	var (
		inOrigin bool
		url      string
	)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			// Section headers look like: [remote "origin"]
			normalised := strings.Join(strings.Fields(line), " ")
			inOrigin = strings.EqualFold(normalised, `[remote "origin"]`)
			continue
		}
		if !inOrigin {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), "url") {
			url = configValue(value)
			break
		}
	}
	return url
}

// RepoRoot returns the nearest folder at or above dir that holds a .git entry, or ""
// when dir is not inside a repository. Nothing is executed, and a dir that is not
// Local - a transcript's cwd is untrusted input - is never looked at.
func RepoRoot(dir string) string {
	if !Local(dir) {
		return ""
	}
	d := filepath.Clean(dir)
	for {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// configValue reads a git config value the way git does for the forms that occur in
// a url line: surrounding double quotes are removed, and a # or ; outside quotes
// starts a comment. `url = "https://..."` or a trailing comment used to leave the
// quotes or the comment in the URL, so it never matched.
//
// ponytail: backslash escapes inside quotes are not decoded; a URL has no reason to
// contain one.
func configValue(v string) string {
	var b strings.Builder
	quoted := false
	for _, r := range strings.TrimSpace(v) {
		switch {
		case r == '"':
			quoted = !quoted
		case (r == '#' || r == ';') && !quoted:
			return strings.TrimSpace(b.String())
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// SameRemote compares two remotes ignoring protocol, credentials, a trailing .git
// and case, so that git@github.com:o/r.git and https://github.com/o/r match.
//
// Two empty remotes are NOT the same: an unknown identity must never be treated as
// evidence of a match.
func SameRemote(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	return normaliseRemote(a) == normaliseRemote(b)
}

// NormalRemote is a remote as SameRemote compares it: host and path, lower case, as in
// github.com/o/r, with no user, password or token.
func NormalRemote(u string) string { return normaliseRemote(u) }

// scpUser is where the user of an scp-form remote, [user@]host:path, ends: at the last
// @ before host:path, so an @ in the path stays. -1 when there is none, or u is not scp
// form, which git takes it for only when a : comes before any /: /data/a@b:c/repo is a
// path.
func scpUser(u string) int {
	c := strings.Index(u, ":")
	if c < 0 || strings.Contains(u[:c], "/") {
		return -1
	}
	for j := strings.LastIndex(u, "@"); j >= 0; j = strings.LastIndex(u[:j], "@") {
		if strings.Contains(u[j+1:], ":") {
			return j
		}
	}
	return -1
}

// RedactRemote is a remote as written, less what can hold a secret, for printing: a
// password, a query, and over http the user, where a token often stands. A user alone
// elsewhere stays, as git in git@host:o/r.
func RedactRemote(u string) string {
	u = strings.TrimSpace(u)
	i := strings.Index(u, "://")
	if i < 0 {
		if j := scpUser(u); j >= 0 && strings.Contains(u[:j], ":") {
			return u[j+1:]
		}
		return u
	}
	p, err := url.Parse(u)
	if err != nil { // as in normaliseRemote
		rest := u[i+3:]
		if j := strings.LastIndex(rest, "@"); j >= 0 {
			rest = rest[j+1:]
		}
		rest, _, _ = strings.Cut(rest, "?")
		return u[:i+3] + rest
	}
	changed := false
	if p.User != nil {
		if _, pw := p.User.Password(); pw || p.Scheme == "http" || p.Scheme == "https" {
			p.User, changed = nil, true
		}
	}
	if p.RawQuery != "" || p.ForceQuery {
		p.RawQuery, p.ForceQuery, changed = "", false, true
	}
	if !changed {
		return u
	}
	return p.String()
}

// hostPort matches a leading host with an explicit port, e.g. "github.com:22/".
// A port is a transport detail, not part of the repository's identity.
var hostPort = regexp.MustCompile(`^([^/:]+):\d+(/|$)`)

func normaliseRemote(u string) string {
	u = strings.TrimSpace(u)

	// A user, password or token is never part of the identity, and a caller may show it.
	if i := strings.Index(u, "://"); i >= 0 {
		if p, err := url.Parse(u); err == nil {
			path := p.RawPath // the path as written, as an scp form or plain path has it
			if path == "" {
				path = p.EscapedPath()
			}
			u = p.Hostname() + path // no user, port or query
			if p.Fragment != "" {
				u += "#" + p.EscapedFragment() // to git, part of the path
			}
		} else {
			// Not a URL Go reads, as with a / or ? in a password: what precedes the last @
			// is taken for credentials.
			u = u[i+3:]
			if j := strings.LastIndex(u, "@"); j >= 0 {
				u = u[j+1:]
			}
			u, _, _ = strings.Cut(u, "?")
			// Drop an explicit port before the scp-style colon is rewritten, or
			// ssh://host:22/ would turn into host/22/. Only a URL has a port: in scp
			// form git@host:1234/repo, 1234 is the owner.
			u = hostPort.ReplaceAllString(u, "$1$2")
		}
	} else if j := scpUser(u); j >= 0 {
		u = u[j+1:]
	} else if i := strings.Index(u, "@"); i >= 0 && !strings.Contains(u[:i], "/") {
		u = u[i+1:]
	}

	// scp-style host:path -> host/path
	u = strings.ReplaceAll(u, ":", "/")
	// Separators first, then the suffix: "repo.git/" otherwise kept its ".git".
	u = strings.TrimRight(u, "/")
	u = strings.TrimSuffix(u, ".git")

	// Collapse any doubled separator the rewrites left behind.
	for strings.Contains(u, "//") {
		u = strings.ReplaceAll(u, "//", "/")
	}
	return strings.ToLower(u)
}
