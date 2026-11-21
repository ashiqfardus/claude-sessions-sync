package work

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
)

// ErrRefused marks a target that must not be written. The archive is a shared
// folder, so everything in Source is untrusted.
var ErrRefused = errors.New("refused")

// Source is another machine's work folder (or file, when Exact), as its shard says.
type Source struct {
	Machine, Home, Path string
	Exact               bool
	Origin              *archive.WorkOrigin
}

// Local is this machine. Own lists its own work folders: an origin naming this
// machine is trusted only inside one of them.
type Local struct {
	Env
	Machine, Placement string
	Map                map[string]string
	SameLayout         bool
	Own                []string
}

// absSource reports an absolute path in either OS form, not UNC, not a bare root,
// with no "..".
func absSource(p string) bool {
	_, ok := norm(p, false)
	s := segs(p)
	return ok && len(s) > 0 && !(WindowsPath(p) && len(s) == 1) && !slices.Contains(s, "..")
}

// Place returns the local target for src: rules 0-3 of the spec, or src.Path as is
// under SameLayout, then the safety checks.
func Place(src Source, l Local) (string, error) {
	if !absSource(src.Path) {
		return "", fmt.Errorf("%w: %q is not an absolute path", ErrRefused, src.Path)
	}
	target, base, err := rule(src, l)
	if err != nil {
		return "", err
	}
	why := "not an absolute path"
	if filepath.IsAbs(target) {
		why = refused(target, src.Exact, l.Env)
		if why == "" && linked(target, base) { // after the name checks, so a refused name is never stat'ed
			why = "a link or junction on the way"
		}
	}
	if why != "" {
		return "", fmt.Errorf("%w: %s: %s", ErrRefused, target, why)
	}
	return target, nil
}

// rule returns the target and the trusted folder it was built on; the part of target
// below base came from the archive.
func rule(src Source, l Local) (target, base string, err error) {
	s := segs(src.Path)
	if l.SameLayout {
		if !filepath.IsAbs(src.Path) {
			return "", "", fmt.Errorf("%s is not a path on this machine", src.Path)
		}
		target = filepath.Clean(src.Path)
		if Within(target, l.Home, true) {
			return target, l.Home, nil
		}
		return target, filepath.VolumeName(target) + string(filepath.Separator), nil
	}
	if o := src.Origin; o != nil && o.Machine == l.Machine && absSource(o.Path) && filepath.IsAbs(o.Path) {
		for _, own := range l.Own {
			if len(segs(own)) > 0 && Within(o.Path, own, true) {
				return filepath.Clean(o.Path), own, nil
			}
		}
	}
	key := ""
	for k := range l.Map { // the most specific key; ties break by name so map order never matters
		n, m := len(segs(k)), len(segs(key))
		if n > 0 && Within(src.Path, k, true) && (key == "" || n > m || n == m && k < key) {
			key = k
		}
	}
	if key != "" {
		return filepath.Join(append([]string{l.Map[key]}, s[len(segs(key)):]...)...), l.Map[key], nil
	}
	if l.Placement == "" || l.Placement == "home" {
		if !absSource(src.Home) {
			return "", "", fmt.Errorf("%w: %s: home %q is not an absolute path", ErrRefused, src.Path, src.Home)
		}
		if Within(src.Path, src.Home, true) {
			return filepath.Join(append([]string{l.Home}, s[len(segs(src.Home)):]...)...), l.Home, nil
		}
	}
	if !archive.ValidName(src.Machine) {
		return "", "", fmt.Errorf("%w: %s: machine name %q", ErrRefused, src.Path, src.Machine)
	}
	return filepath.Join(l.Home, "claude-work", src.Machine, filepath.FromSlash(Encode(src.Path))), l.Home, nil
}

// linked reports a symlink or junction below base on the way to p. Name checks
// cannot see through one: Windows puts a "Start Menu" junction into AppData in
// every home, and macOS has /Volumes/Macintosh HD -> /.
func linked(p, base string) bool {
	for len(segs(p)) > len(segs(base)) {
		if fi, err := os.Lstat(p); err == nil && fi.Mode().Type()&^fs.ModeDir != 0 {
			return true
		}
		if p == filepath.Dir(p) { // a root: stop even if base was malformed
			break
		}
		p = filepath.Dir(p)
	}
	return false
}

func anyFold(s string, names ...string) bool {
	return slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(s, n) })
}

// refused returns why target must not be written, or "". Case is ignored throughout,
// since the macOS and Windows file systems ignore it: ~/library is ~/Library.
func refused(target string, exact bool, env Env) string {
	t, win := segs(target), WindowsPath(target)
	rest := t // what the archive chose; the local home itself is trusted
	if h := segs(env.Home); len(h) > 0 && within(t, h, true) {
		rest = t[len(h):]
	} else if win {
		rest = t[1:]
	}
	in := func(dir string) bool { return dir != "" && Within(target, dir, true) }
	broad := env
	broad.Windows = true // IsBroad ignores case only for Windows
	switch {
	case in(env.ClaudeRoot):
		return "inside the Claude folder"
	case slices.ContainsFunc(env.Temp, in):
		return "in a temp folder"
	case env.RepoRoot(target) != "":
		return "inside a git repo"
	case !exact && IsBroad(target, broad):
		return "a broad folder"
	case env.Home != "" && in(filepath.Join(env.Home, "Library")):
		return "in ~/Library"
	case !win && len(t) > 0 && anyFold(t[0], "etc", "Library", "System"),
		win && len(t) > 1 && anyFold(t[1], "Windows", "Program Files", "Program Files (x86)"):
		return "a system folder"
	}
	for _, s := range rest {
		switch {
		case strings.HasPrefix(s, "."):
			return "a dot folder or file"
		case anyFold(s, "AppData") || win && anyFold(s, "ProgramData", "WindowsPowerShell", "PowerShell"):
			return "in AppData, ProgramData or a PowerShell profile folder"
		case win && (strings.ContainsAny(s, ":~") || strings.TrimRight(s, ". ") != s):
			// Win32 drops a trailing dot or space, reads APPDAT~1 as AppData, and
			// "AppData::$INDEX_ALLOCATION" as the folder itself.
			return "a name Windows reads as another"
		}
	}
	return ""
}

// SafeJoin joins an archive-relative path (with "/") onto target, refusing anything
// that could leave it, a dot entry, or a link on the way.
func SafeJoin(target, rel string) (string, error) {
	bad := rel == "" || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) ||
		len(rel) >= 2 && rel[1] == ':' || !filepath.IsLocal(filepath.FromSlash(rel))
	// Split on both: on Windows a backslash inside a name is a separator.
	for _, s := range strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' }) {
		bad = bad || strings.HasPrefix(s, ".")
	}
	out := filepath.Join(target, filepath.FromSlash(rel))
	if bad || !Within(out, target, false) || linked(out, target) {
		return "", fmt.Errorf("%w: %q under %s", ErrRefused, rel, target)
	}
	return out, nil
}
