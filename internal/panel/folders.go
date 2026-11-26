// Package panel holds the local web panel's server-side pieces.
package panel

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Folder is one directory the folder browser can show.
type Folder struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// FolderList is one directory's subfolders. Error is a fixed phrase with no detail.
type FolderList struct {
	Path      string   `json:"path"`
	Parent    string   `json:"parent"`
	Entries   []Folder `json:"entries"`
	Truncated bool     `json:"truncated"`
	Error     string   `json:"error"`
}

// maxEntries is a variable so a test can lower it.
var maxEntries = 2000

const cantOpen = "You can't open this folder."

// ListFolders lists the subfolder names of an absolute path and nothing else: no
// files, no contents. A symlink is listed only if it points to a directory; the
// listed path is never rewritten to the link's target.
func ListFolders(path string, hidden bool) FolderList {
	if !filepath.IsAbs(path) || !localPath(runtime.GOOS, path) {
		return FolderList{Path: path, Error: cantOpen}
	}
	path = filepath.Clean(path)
	out := FolderList{Path: path}
	if p := filepath.Dir(path); p != path {
		out.Parent = p
	}
	f, err := os.Open(path)
	if err != nil {
		out.Error = cantOpen
		return out
	}
	defer f.Close()
	ents, err := f.ReadDir(-1)
	if err != nil && len(ents) == 0 {
		out.Error = cantOpen
		return out
	}
	for _, e := range ents {
		name := e.Name()
		if !hidden && strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(path, name)
		// Following a linked folder is a navigation convenience, not a jail: the real
		// access control is the route's localhost and token guard.
		if e.Type()&os.ModeSymlink != 0 {
			if st, err := os.Stat(full); err != nil || !st.IsDir() {
				continue
			}
		} else if !e.IsDir() {
			continue
		}
		out.Entries = append(out.Entries, Folder{name, full})
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		a, b := strings.ToLower(out.Entries[i].Name), strings.ToLower(out.Entries[j].Name)
		if a != b {
			return a < b
		}
		return out.Entries[i].Name < out.Entries[j].Name
	})
	if len(out.Entries) > maxEntries {
		out.Entries, out.Truncated = out.Entries[:maxEntries], true
	}
	return out
}

// localPath reports whether opening p, before it is cleaned, stays on this computer's
// disks. A GET names it, so any page the browser shows can: a network path would make
// Windows connect to a host the page chose and send it the user's NTLM credentials.
// On Windows only a drive-letter path stays, which refuses \\host, /\host and \/host
// (Go reads any two leading separators as UNC), \??\UNC\host, and the \\?\ and \\.\
// device forms. Elsewhere a path starting with // is refused too.
func localPath(goos, p string) bool {
	if goos != "windows" {
		return !strings.HasPrefix(p, "//") && !strings.HasPrefix(p, `\\`)
	}
	if len(p) < 3 || p[1] != ':' || p[2] != '\\' && p[2] != '/' {
		return false
	}
	c := p[0] | 0x20 // lower case
	return 'a' <= c && c <= 'z'
}

// Roots are the browser's starting points: the home folder, then each drive on
// Windows or "/" elsewhere.
func Roots(home string, drives []string) []Folder {
	r := []Folder{{"Home folder", home}}
	for _, d := range drives {
		r = append(r, Folder{d, d})
	}
	if len(drives) == 0 && runtime.GOOS != "windows" { // no "/" on Windows
		r = append(r, Folder{"/", "/"})
	}
	return r
}
