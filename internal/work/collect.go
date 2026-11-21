package work

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const (
	MaxFileBytes   = 10 << 20  // larger files are skipped
	MaxFolderBytes = 250 << 20 // per folder, after filtering
	WarnBytes      = 500 << 20 // doctor warns above this
)

var skipDirs = map[string]bool{
	"node_modules": true, ".next": true, "vendor": true, ".venv": true, "venv": true,
	"__pycache__": true, ".cache": true, "dist": true, "build": true, "target": true, ".gradle": true,
}

// skipDir ignores case: on macOS and Windows Node_Modules is node_modules.
func skipDir(name string) bool { return skipDirs[strings.ToLower(name)] }

var deniedExt = map[string]bool{}

func init() {
	for _, e := range strings.Fields(`.png .jpg .jpeg .gif .webp .bmp .tif .tiff .heic .ico .svg
.mp4 .mov .webm .avi .mkv .mp3 .wav .m4a .zip .gz .tgz .tar .7z .rar .bz2 .xz
.apk .aab .ipa .exe .dll .so .dylib .jar .class .o .a .bin .sqlite .sqlite3 .db .bak .dump`) {
		deniedExt[e] = true
	}
}

// Allowed decides whether a file at rel ("/"-separated, below a work folder) is
// copied. reason is "dot", "folder" (under a skipped folder), "secret", "type" or
// "large", checked in that order. Collect prunes skipped folders before asking;
// restore, reading an archive push did not write, relies on the check here.
func Allowed(rel string, size int64) (bool, string) {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		if strings.HasPrefix(p, ".") {
			return false, "dot"
		}
		if i < len(parts)-1 && skipDir(p) {
			return false, "folder"
		}
	}
	b := strings.ToLower(parts[len(parts)-1])
	ext := filepath.Ext(b)
	if strings.HasPrefix(b, "id_rsa") || strings.HasPrefix(b, "id_ed25519") ||
		strings.Contains(b, "credentials") || strings.Contains(b, "secret") ||
		ext == ".pem" || ext == ".key" || ext == ".p12" || ext == ".pfx" {
		return false, "secret"
	}
	if deniedExt[ext] {
		return false, "type"
	}
	if size > MaxFileBytes {
		return false, "large"
	}
	return true, ""
}

// Collected is the filtered file list of one folder. Skipped counts files only.
type Collected struct {
	Files   []string // absolute, sorted
	Bytes   int64
	Skipped map[string]int
}

// Collect lists the files of f that may be archived, minus excluded paths.
func Collect(f Folder, exclude []string) (Collected, error) {
	c := Collected{Skipped: map[string]int{}}
	fold := runtime.GOOS == "windows"
	add := func(path, rel string, size int64) {
		for _, x := range exclude {
			if Within(path, x, fold) {
				c.Skipped["excluded"]++
				return
			}
		}
		if ok, why := Allowed(rel, size); !ok {
			c.Skipped[why]++
			return
		}
		c.Files = append(c.Files, path)
		c.Bytes += size
	}
	if f.Exact {
		fi, err := os.Stat(f.Path)
		if err != nil {
			return c, err
		}
		add(f.Path, filepath.Base(f.Path), fi.Size())
		return c, nil
	}
	if _, err := os.Stat(f.Path); err != nil {
		return c, err
	}
	err := filepath.WalkDir(f.Path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == f.Path {
				return err
			}
			return nil // unreadable entry: leave it out
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			if p == f.Path {
				return nil
			}
			if skipDir(d.Name()) || strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			if _, e := os.Lstat(filepath.Join(p, ".git")); e == nil {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		fi, e := d.Info()
		if e != nil {
			return nil
		}
		rel, _ := filepath.Rel(f.Path, p)
		add(p, filepath.ToSlash(rel), fi.Size())
		return nil
	})
	sort.Strings(c.Files)
	return c, err
}

// Encode turns an absolute path into its archive-relative, "/"-separated form.
func Encode(p string) string {
	s := segs(p)
	if WindowsPath(p) {
		s[0] = strings.TrimSuffix(s[0], ":")
	}
	return strings.Join(s, "/")
}
