package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/identity"
	"github.com/ashiqfardus/claude-sessions-sync/internal/rewrite"
	"github.com/ashiqfardus/claude-sessions-sync/internal/work"
)

// quietWorkBudget bounds work-folder copying in an unattended push, as
// quietRenderBudget does for pages. Files left over are copied by the next run.
const quietWorkBudget = 30 * time.Second

// workPushResult is what pushWork did. New holds "<folder> (<n> files, <size>)" for
// each folder backed up for the first time; Would, under --dry-run, the files a real
// run would copy.
type workPushResult struct {
	Folders, Copied, Unchanged int
	Bytes                      int64
	New                        []string
	Deferred                   bool
	Would                      []string
}

// workEnv is what choosing work folders needs to know about this machine.
// CLAUDE_SESSIONS_TEMP_DIRS replaces the temp list; the tests need it, as every test
// folder lives under the system temp folder.
func workEnv(root string) work.Env {
	temp := []string{os.TempDir(), "/tmp", "/private/tmp", "/var/folders", "/private/var/folders"}
	if v := os.Getenv("CLAUDE_SESSIONS_TEMP_DIRS"); v != "" {
		temp = filepath.SplitList(v)
	}
	// An empty entry contains every path, and would silently drop every folder.
	temp = slices.DeleteFunc(temp, func(s string) bool { return s == "" })
	return work.Env{
		Home: userHome(), ClaudeRoot: root, Temp: temp, Windows: runtime.GOOS == "windows",
		RepoRoot: identity.RepoRoot,
		Exists:   func(p string) bool { _, err := os.Stat(p); return err == nil },
	}
}

// pushWork archives the folders this machine's sessions worked in to
// work/<machine>/ and records them in shard. It looks for folders only in scan, the
// transcripts that changed; those found before come from this machine's local record
// (prev is not read for folders). The caller holds the push lock.
func pushWork(root, dest string, shard *archive.Shard, prev archive.Shard, scan []string, cfg archive.Config, dryRun bool, budget time.Duration, logf func(string, ...any)) workPushResult {
	var res workPushResult
	// The previous folders come from this machine's own record, never from prev: the
	// shard is shared archive data, and a path added there must not get walked.
	known, _ := loadWorkFolders(root)
	if !cfg.WorkOn() {
		// Off stops copying, not the record of what the archive holds. WorkIndexed stays
		// unset, so turning it back on rescans every transcript.
		shard.WorkFolders = known
		return res
	}
	env := workEnv(root)
	fold := func(p string) bool { return env.Windows || work.WindowsPath(p) }
	// The archive is never copied into its own work/ folder.
	exclude := append(slices.Clone(cfg.WorkExclude), dest)
	excluded := func(p string) bool {
		return slices.ContainsFunc(exclude, func(x string) bool { return work.Within(p, x, fold(p)) })
	}

	var cands []work.Candidate
	for _, t := range scan {
		c, _ := work.Candidates(t, env.Home) // a transcript that fails part way still gives what it read
		cands = append(cands, c...)
	}
	// Repeated every push, so an exclusion made while the archive was away - a whole
	// folder or a part of one - is removed too. purgeWork is idempotent.
	if !dryRun {
		for _, x := range cfg.WorkExclude {
			if _, err := purgeWork(dest, shard.Machine, x); err != nil {
				logf("WARN: excluded folder %s: %v", x, err)
			}
		}
	}
	prevBy := map[string]archive.WorkFolder{}
	for _, f := range known {
		if excluded(f.Path) {
			continue
		}
		prevBy[f.Path] = f
		cands = append(cands, work.Candidate{Path: f.Path, File: f.Exact})
	}
	folders := slices.DeleteFunc(work.Folders(cands, env), func(f work.Folder) bool { return excluded(f.Path) })

	// A folder found before and not now - deleted, moved, become a repository - keeps
	// its entry unchanged: its copy is still in the archive.
	var out []archive.WorkFolder
	for _, p := range known {
		if _, ok := prevBy[p.Path]; ok && !slices.ContainsFunc(folders, func(f work.Folder) bool { return work.Within(p.Path, f.Path, fold(p.Path)) }) {
			out = append(out, p)
		}
	}

	start := time.Now()
	// copyFolder backs up one folder. It returns false when the budget ran out part way.
	copyFolder := func(f work.Folder) (archive.WorkFolder, bool) {
		dir, err := workDir(dest, shard.Machine, f.Path)
		if err != nil {
			logf("WARN: work folder %s: %v", f.Path, err)
			return archive.WorkFolder{}, true
		}
		c, err := work.Collect(f, exclude)
		if err != nil {
			logf("WARN: work folder %s: %v", f.Path, err)
			return prevBy[f.Path], true
		}
		e := archive.WorkFolder{Path: f.Path, Exact: f.Exact}
		if c.Bytes > work.MaxFolderBytes {
			logf("WARN: work folder %s is over 250 MB after filtering; not backed up (exclude parts with config exclude-folder)", f.Path)
			e.Skipped = "over 250 MB"
			return e, true
		}
		for _, p := range c.Files {
			if budget > 0 && time.Since(start) > budget {
				return e, false
			}
			target := dir
			if !f.Exact {
				rel, _ := filepath.Rel(f.Path, p)
				target = filepath.Join(dir, rel)
			}
			src, err := os.Stat(p)
			if err != nil {
				continue
			}
			// Unlike a transcript, a file may shrink: an edit within the mtime tolerance
			// that changed its size is copied too.
			if dst := archive.StatOrNil(target); !archive.NeedsCopy(src, dst) &&
				!(dst != nil && src.Size() != dst.Size() && archive.SameMTime(src.ModTime(), dst.ModTime())) {
				res.Unchanged++
				continue
			}
			if dryRun {
				res.Would = append(res.Would, p)
			} else if err := archive.CopyFile(p, target); err != nil {
				logf("WARN: %s: %v", p, err)
				continue
			}
			res.Copied++
		}
		e.Files, e.Bytes = len(c.Files), c.Bytes
		res.Folders++
		res.Bytes += c.Bytes
		if prevBy[f.Path].Files == 0 && e.Files > 0 && !dryRun {
			n := fmt.Sprintf("%s (%d files, %s)", f.Path, e.Files, humanSize(c.Bytes))
			logf("WORK: now backing up %s", n)
			res.New = append(res.New, n)
		}
		return e, true
	}

	for i, f := range folders {
		e, done := copyFolder(f)
		if !done {
			// Unfinished folders keep their last entry, or a bare one, so the next run
			// finds them again without rescanning a transcript.
			for _, g := range folders[i:] {
				old, ok := prevBy[g.Path]
				if !ok {
					old = archive.WorkFolder{Path: g.Path, Exact: g.Exact}
				}
				out = append(out, old)
			}
			res.Deferred = true
			logf("WORK: %d folder(s) left for the next run", len(folders)-i)
			break
		}
		if e.Path != "" {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b archive.WorkFolder) int { return strings.Compare(a.Path, b.Path) })
	shard.WorkFolders, shard.WorkIndexed = out, true
	if !dryRun {
		data, _ := json.MarshalIndent(out, "", "  ")
		if err := archive.WriteFileAtomic(workFoldersPath(root), append(data, '\n'), 0o644); err != nil {
			logf("WARN: work folders record: %v", err)
		}
	}
	return res
}

// workFoldersPath is this machine's record of its work folders, the source of its
// shard's list. It stays local: the archive is untrusted.
func workFoldersPath(root string) string {
	return filepath.Join(root, "session-sync-base", "work-folders.json")
}

// loadWorkFolders reads that record. An error means it is missing or unreadable, and
// every transcript must be scanned again to rebuild it.
func loadWorkFolders(root string) ([]archive.WorkFolder, error) {
	raw, err := os.ReadFile(workFoldersPath(root))
	if err != nil {
		return nil, err
	}
	var f []archive.WorkFolder
	return f, json.Unmarshal(raw, &f)
}

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
