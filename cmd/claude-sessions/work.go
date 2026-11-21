package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/rewrite"
	"github.com/ashiqfardus/claude-sessions-sync/internal/work"
)

// workDir is where a work folder lives in the archive: work/<machine>/<Encode(p)>.
// It is the one place that turns a path from shared archive data into a disk path, so
// it refuses anything that could land outside work/<machine>/.
func workDir(dest, machine, p string) (string, error) {
	if !archive.ValidName(machine) {
		return "", fmt.Errorf("invalid machine name %q", machine)
	}
	if !rewrite.AbsoluteAnywhere(p) {
		return "", fmt.Errorf("%q is not an absolute path", p)
	}
	for _, s := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if s == ".." {
			return "", fmt.Errorf("%q contains ..", p)
		}
	}
	enc := work.Encode(p)
	if enc == "" {
		return "", fmt.Errorf("%q encodes to nothing", p)
	}
	base := filepath.Join(dest, "work", machine)
	dir := filepath.Join(base, filepath.FromSlash(enc))
	if rel, err := filepath.Rel(base, dir); err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q resolves outside the work folder", p)
	}
	return dir, nil
}

// purgeWork removes this machine's archived copies of folder: every shard entry inside
// it, and, when folder lies inside an archived entry, just that subfolder (the entry
// stays). It returns the folders removed. Entries workDir refuses are never deleted.
// The caller holds the push lock.
func purgeWork(dest, machine, folder string) ([]string, error) {
	shard := readShard(dest, machine)
	var keep []archive.WorkFolder
	var removed []string
	drop := func(p string) error {
		dir, err := workDir(dest, machine, p)
		if err != nil {
			return nil // hostile or malformed: skip, never delete
		}
		if _, err := os.Stat(dir); err != nil {
			return nil
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		removed = append(removed, p)
		return nil
	}
	changed := false
	for _, f := range shard.WorkFolders {
		fold := runtime.GOOS == "windows" || work.WindowsPath(f.Path) || work.WindowsPath(folder)
		switch {
		case work.Within(f.Path, folder, fold):
			if _, err := workDir(dest, machine, f.Path); err != nil {
				keep = append(keep, f)
				continue
			}
			if err := drop(f.Path); err != nil {
				return removed, err
			}
			changed = true
		case work.Within(folder, f.Path, fold):
			if err := drop(folder); err != nil {
				return removed, err
			}
			keep = append(keep, f)
		default:
			keep = append(keep, f)
		}
	}
	if !changed {
		return removed, nil
	}
	shard.WorkFolders = keep
	if shard.Machine == "" {
		shard.Machine = machine
	}
	return removed, writeManifest(dest, shard)
}
