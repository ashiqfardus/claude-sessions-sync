package main

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/work"
)

// workDir is where a work folder lives in the archive: work/<machine>/<Encode(p)>.
func workDir(dest, machine, p string) string {
	return filepath.Join(dest, "work", machine, filepath.FromSlash(work.Encode(p)))
}

// purgeWork removes this machine's archived folders inside folder and drops them from
// its shard. It deletes only paths workDir computed, so nothing outside
// work/<machine>/ is touched.
func purgeWork(dest, machine, folder string) (removed int, err error) {
	shard := readShard(dest, machine)
	var keep []archive.WorkFolder
	for _, f := range shard.WorkFolders {
		fold := runtime.GOOS == "windows" || work.WindowsPath(f.Path) || work.WindowsPath(folder)
		if !work.Within(f.Path, folder, fold) {
			keep = append(keep, f)
			continue
		}
		if err := os.RemoveAll(workDir(dest, machine, f.Path)); err != nil {
			return removed, err
		}
		removed++
	}
	if removed == 0 {
		return 0, nil
	}
	shard.WorkFolders = keep
	if shard.Machine == "" {
		shard.Machine = machine
	}
	return removed, writeManifest(dest, shard)
}
