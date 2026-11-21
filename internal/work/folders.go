package work

import (
	"sort"
	"strings"
)

// Env carries the machine facts folder selection needs; tests inject fakes.
type Env struct {
	Home, ClaudeRoot string
	Temp             []string
	Windows          bool
	RepoRoot         func(string) string
	Exists           func(string) bool
}

// Folder is a chosen backup root. Exact means Path is a single file.
type Folder struct {
	Path  string
	Exact bool
}

// WindowsPath reports a drive-letter prefix (X:\ or X:/).
func WindowsPath(p string) bool {
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') &&
		(p[0]|0x20 >= 'a' && p[0]|0x20 <= 'z')
}

func segs(p string) []string {
	var out []string
	for _, s := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if s != "." {
			out = append(out, s)
		}
	}
	return out
}

func within(c, p []string, fold bool) bool {
	if len(c) < len(p) {
		return false
	}
	for i := range p {
		if c[i] != p[i] && !(fold && strings.EqualFold(c[i], p[i])) {
			return false
		}
	}
	return true
}

// Within reports whether child is parent or below it. Both separators count as
// one, so paths from either OS compare on any OS.
func Within(child, parent string, fold bool) bool {
	return within(segs(child), segs(parent), fold)
}

// norm returns an absolute, cleaned path; ok is false for relative or UNC paths.
func norm(p string, windows bool) (string, bool) {
	if strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, `//`) {
		return "", false
	}
	if windows && !WindowsPath(p) && len(p) >= 3 && p[0] == '/' && p[2] == '/' &&
		(p[1]|0x20 >= 'a' && p[1]|0x20 <= 'z') { // MSYS /e/dir
		p = p[1:2] + ":" + p[2:]
	}
	s := segs(p)
	if WindowsPath(p) {
		if len(s) > 0 && len(s[0]) == 2 {
			s[0] = strings.ToUpper(s[0])
		}
		if len(s) == 1 {
			return s[0] + `\`, true
		}
		return strings.Join(s, `\`), true
	}
	if !strings.HasPrefix(p, "/") {
		return "", false
	}
	for i := 0; i < len(s); i++ { // resolve ..
		if s[i] == ".." {
			j := i - 1
			if j < 0 {
				j = 0
				s = s[1:]
			} else {
				s = append(s[:j], s[i+1:]...)
			}
			i = j - 1
		}
	}
	return "/" + strings.Join(s, "/"), true
}

func parent(p string) string {
	if WindowsPath(p) {
		s := segs(p)
		if len(s) <= 2 {
			return s[0] + `\`
		}
		return strings.Join(s[:len(s)-1], `\`)
	}
	s := segs(p)
	return "/" + strings.Join(s[:max(len(s)-1, 0)], "/")
}

// IsBroad reports a folder too wide to back up: home, its ancestors, a root,
// or Desktop/Downloads/Documents directly under home.
func IsBroad(dir string, env Env) bool {
	d := segs(dir)
	fold := env.Windows || WindowsPath(dir)
	h := segs(env.Home)
	if len(d) == 0 || (WindowsPath(dir) && len(d) == 1) || within(h, d, fold) {
		return true
	}
	for _, n := range []string{"Desktop", "Downloads", "Documents"} {
		if within(d, append(append([]string{}, h...), n), fold) && len(d) == len(h)+1 {
			return true
		}
	}
	return false
}

// Folders turns raw candidates into the folders to back up, sorted by Path.
func Folders(cands []Candidate, env Env) []Folder {
	var all []Folder
	seen := map[string]bool{}
	for _, c := range cands {
		p, ok := norm(c.Path, env.Windows)
		if !ok || !env.Exists(p) {
			continue
		}
		fold := env.Windows || WindowsPath(p)
		dir := p
		if c.File {
			dir = parent(p)
		}
		skip := within(segs(dir), segs(env.ClaudeRoot), fold) || env.RepoRoot(dir) != ""
		for _, t := range env.Temp {
			skip = skip || within(segs(dir), segs(t), fold)
		}
		if skip {
			continue
		}
		f := Folder{Path: dir}
		if IsBroad(dir, env) {
			if !c.File {
				continue
			}
			f = Folder{p, true}
		}
		k := f.Path
		if fold {
			k = strings.ToLower(k)
		}
		if !seen[k] {
			seen[k] = true
			all = append(all, f)
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return len(all[i].Path) < len(all[j].Path) })
	var kept []Folder
outer:
	for _, f := range all {
		for _, k := range kept {
			if !k.Exact && Within(f.Path, k.Path, env.Windows || WindowsPath(f.Path)) {
				continue outer
			}
		}
		kept = append(kept, f)
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Path < kept[j].Path })
	return kept
}
