package work

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
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
	// Every rule trusts the home; empty or a root would make Within match anything.
	if h := segs(l.Home); !filepath.IsAbs(l.Home) || len(h) == 0 || WindowsPath(l.Home) && len(h) == 1 {
		return "", fmt.Errorf("%w: %s: no local home", ErrRefused, src.Path)
	}
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
		// Below base the archive chose every name, whichever rule placed it.
		if t := segs(target); why == "" && len(t) >= len(segs(base)) && slices.ContainsFunc(t[len(segs(base)):], codeDir) {
			why = "in a folder code is loaded from"
		}
		if why == "" { // after the name checks, so a refused name is never stat'ed
			why = onTheWay(target, base, false)
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
		for _, v := range l.Map { // outside the home only inside a folder the user mapped
			if len(segs(v)) > len(segs(base)) && Within(target, v, true) {
				base = v
			}
		}
		if base == "" {
			return "", "", fmt.Errorf("%w: %s: outside the home and no mapped folder (claude-sessions config map-folder)", ErrRefused, target)
		}
		return target, base, nil
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

// onTheWay returns why a folder below base on the way to p, p included, refuses
// writes, or "". A symlink or junction: name checks cannot see through one, and
// Windows puts a "Start Menu" junction into AppData in every home. With repo, a
// nested git repo: a planted .go init() or conftest.py runs on the next build.
func onTheWay(p, base string, repo bool) string {
	for len(segs(p)) > len(segs(base)) {
		if fi, err := os.Lstat(p); err == nil && fi.Mode().Type()&^fs.ModeDir != 0 {
			return "a link or junction on the way"
		}
		if repo {
			if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
				return "inside a git repo"
			}
		}
		if p == filepath.Dir(p) { // a root: stop even if base was malformed
			break
		}
		p = filepath.Dir(p)
	}
	return ""
}

// codeDir reports a folder a language runtime loads code from on its own: a module
// planted there runs with no step by the user. build, dist and target are not: push
// skips them below a root only, and a work folder may live under one.
func codeDir(name string) bool {
	return anyFold(name, "node_modules", "venv", "vendor", "__pycache__", "site-packages", "dist-packages")
}

func anyFold(s string, names ...string) bool {
	return slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(s, n) })
}

// refused returns why target must not be written, or "". Case is ignored throughout,
// since the macOS and Windows file systems ignore it: ~/library is ~/Library.
func refused(target string, exact bool, env Env) string {
	t, drive, h := segs(target), WindowsPath(target), segs(env.Home)
	// A UNC target has no drive letter, but on Windows its names still alias.
	win := drive || runtime.GOOS == "windows"
	rest := t // what the archive chose; the local home itself is trusted
	if len(h) > 0 && within(t, h, true) {
		rest = t[len(h):]
	} else if drive {
		rest = t[1:]
	}
	top := len(t) == 0 || drive && len(t) == 1 || within(h, t, true) // a root, the home or above it
	in := func(dir string) bool { return dir != "" && Within(target, dir, true) }
	// Above counts too: the folder's files would land inside.
	near := func(dir string) bool { return in(dir) || dir != "" && Within(dir, target, true) }
	broad := env
	broad.Windows = true // IsBroad ignores case only for Windows
	switch {
	case near(env.ClaudeRoot):
		return "in or above the Claude folder"
	case slices.ContainsFunc(env.Temp, near):
		return "in or above a temp folder"
	case env.RepoRoot(target) != "":
		return "inside a git repo"
	case IsBroad(target, broad) && (!exact || top): // Exact exempts only Desktop, Downloads, Documents
		return "a broad folder"
	case env.Home != "" && in(filepath.Join(env.Home, "Library")):
		return "in ~/Library"
	case !drive && len(t) > 0 && anyFold(t[0], "etc", "Library", "System"),
		drive && len(t) > 1 && anyFold(t[1], "Windows", "Program Files", "Program Files (x86)"):
		return "a system folder"
	}
	return badNames(rest, win)
}

// badNames returns why one of these archive-chosen names is refused, or "". win
// means the path is a Windows one.
func badNames(names []string, win bool) string {
	if modCache(names) {
		return "in the Go module cache"
	}
	for _, s := range names {
		switch {
		case strings.HasPrefix(s, "."):
			return "a dot folder or file"
		case anyFold(s, "AppData", "vimfiles", "_vimrc", "_gvimrc", "site-packages", "dist-packages", "activate.d", "deactivate.d") ||
			win && anyFold(s, "ProgramData", "WindowsPowerShell", "PowerShell"):
			return "in a folder that runs or configures programs"
		case win && (strings.ContainsAny(s, ":~") || strings.TrimRight(s, ". ") != s):
			// Win32 drops a trailing dot or space, reads APPDAT~1 as AppData, and
			// "AppData::$INDEX_ALLOCATION" as the folder itself.
			return "a name Windows reads as another"
		}
	}
	return ""
}

// modCache reports go/pkg/mod, the default Go module cache: a planted module source
// is built into the next program that needs that version.
func modCache(s []string) bool {
	for i := 0; i+2 < len(s); i++ {
		if anyFold(s[i], "go") && anyFold(s[i+1], "pkg") && anyFold(s[i+2], "mod") {
			return true
		}
	}
	return false
}

// SafeJoin joins an archive-relative path (with "/") onto target. It refuses anything
// that could leave target, every name refused() refuses, and a link or a nested repo
// on the way.
func SafeJoin(target, rel string) (string, error) {
	out := filepath.Join(target, filepath.FromSlash(rel))
	why := ""
	switch {
	case rel == "" || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) || len(rel) >= 2 && rel[1] == ':' ||
		!filepath.IsLocal(filepath.FromSlash(rel)) || !Within(out, target, false):
		why = "not inside the folder"
	default:
		// Split on both: on Windows a backslash inside a name is a separator.
		why = badNames(strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' }),
			WindowsPath(out) || runtime.GOOS == "windows")
		if why == "" && modCache(segs(out)) { // go/pkg/mod may straddle target and rel
			why = "in the Go module cache"
		}
		if why == "" {
			why = onTheWay(out, target, true)
		}
	}
	if why != "" {
		return "", fmt.Errorf("%w: %q under %s: %s", ErrRefused, rel, target, why)
	}
	return out, nil
}
