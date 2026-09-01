// Package identity resolves which local folder an archived project belongs to.
//
// The git remote is the strongest cross-machine identity available, so it is read
// for every candidate folder found during a scan.
package identity

import (
	"bufio"
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
func Remote(dir string) string {
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
			url = strings.TrimSpace(value)
			break
		}
	}
	return url
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

// hostPort matches a leading host with an explicit port, e.g. "github.com:22/".
// A port is a transport detail, not part of the repository's identity.
var hostPort = regexp.MustCompile(`^([^/:]+):\d+(/|$)`)

func normaliseRemote(u string) string {
	u = strings.TrimSpace(u)

	// scheme://
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	// user@ or user:password@
	if i := strings.Index(u, "@"); i >= 0 && !strings.Contains(u[:i], "/") {
		u = u[i+1:]
	}
	// Drop an explicit port before the scp-style colon is rewritten, or ssh://host:22/
	// would turn into host/22/ and never match https://host/.
	u = hostPort.ReplaceAllString(u, "$1$2")

	// scp-style host:path -> host/path
	u = strings.ReplaceAll(u, ":", "/")
	u = strings.TrimSuffix(u, ".git")
	u = strings.TrimRight(u, "/")

	// Collapse any doubled separator the rewrites left behind.
	for strings.Contains(u, "//") {
		u = strings.ReplaceAll(u, "//", "/")
	}
	return strings.ToLower(u)
}
