package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
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

	hr := work.LoadHeld(heldWorkPath(root))
	if hr.Err != nil {
		logf("WARN: restored work record: %v", hr.Err)
	}
	held := hr.Files
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
		// Restored from another machine and unchanged since: that machine's, not ours. Not
		// copied, and not counted toward the cap either.
		c.Files = slices.DeleteFunc(c.Files, func(p string) bool {
			h, ok := held[p]
			if !ok {
				return false
			}
			st, err := os.Stat(p)
			if err != nil || h.Size != st.Size() || !archive.SameMTime(h.MTime, st.ModTime()) {
				return false
			}
			c.Bytes -= st.Size()
			return true
		})
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
	for i := range out {
		if o := originOf(out[i].Path, hr.Placed, shard.Machine, fold(out[i].Path)); o != nil {
			out[i].Origin = o
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

// originOf names where folder f came from when it is, or lies below, a folder placed
// here from another machine: that machine and the matching source path. The most
// specific placement wins. Never this machine itself.
func originOf(f string, placed map[string]string, self string, fold bool) *archive.WorkOrigin {
	var best *archive.WorkOrigin
	bestLen := -1
	for k, t := range placed {
		m, src, ok := strings.Cut(k, "|")
		if !ok || m == self || len(t) <= bestLen || !work.Within(f, t, fold || work.WindowsPath(t)) {
			continue
		}
		sep := "/"
		if work.WindowsPath(src) {
			sep = `\`
		}
		path := strings.TrimRight(src, `/\`)
		rest := strings.Trim(f[min(len(t), len(f)):], `/\`)
		if len(f) > len(t) && rest != "" {
			rest = strings.NewReplacer(`/`, sep, `\`, sep).Replace(rest)
			path += sep + rest
		}
		best, bestLen = &archive.WorkOrigin{Machine: m, Path: path}, len(t)
	}
	return best
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

// heldWorkPath is this machine's record of work files restored from other machines
// and where their folders were placed.
func heldWorkPath(root string) string {
	return filepath.Join(root, "session-sync-base", "work-held.json")
}

// byWorkFolder is a project that lies inside a placed work folder: identity.Resolve
// cannot find folders under claude-work/.
const byWorkFolder identity.How = "work folder"

// workRestoreResult is what restoreWork did. Placed maps each placed source path to
// its local target; Notes are the lines import and pull print.
type workRestoreResult struct {
	Placed                       map[string]string
	Filed, Kept, Beside, Refused int
	Notes                        []string
}

// restoreWork places other machines' work folders here and files their files (spec
// §6-§7), returning the moves that repoint transcripts to them. sameLayout is pull's
// case: recorded paths, as they are. Everything read from the archive is untrusted:
// each folder goes through work.Place, each file through the push filter and
// work.SafeJoin, and files are written add-only, mode 0644, never through a link.
func restoreWork(root, dest string, shards map[string]archive.Shard, cfg archive.Config, dryRun, update, sameLayout bool) ([]rewrite.Move, workRestoreResult) {
	res := workRestoreResult{Placed: map[string]string{}}
	if !cfg.WorkOn() {
		return nil, res
	}
	l := work.Local{Env: workEnv(root), Machine: machineName(), Placement: cfg.WorkPlacement, Map: cfg.WorkMap, SameLayout: sameLayout}
	own, _ := loadWorkFolders(root)
	for _, f := range own {
		l.Own = append(l.Own, f.Path)
	}
	held := work.LoadHeld(heldWorkPath(root))
	if held.Err != nil {
		res.Notes = append(res.Notes, fmt.Sprintf("WARN: restored work record unreadable, starting a new one: %v", held.Err))
	}
	now := time.Now()
	var moves []rewrite.Move
	// Push never archives a home, an ancestor of one or a root; a hostile shard that
	// lists one would turn it into a move that repoints every path under it.
	homes := []string{l.Home}
	for _, s := range shards {
		homes = append(homes, s.Home)
	}
	broad := func(p string) bool {
		return slices.ContainsFunc(homes, func(h string) bool { return work.IsBroad(p, work.Env{Home: h}) })
	}
	// A path of this machine's own - its home or Claude folder or above them, or in or
	// above one of its work folders or projects - keeps its files placed but never
	// becomes a move: that would repoint this machine's own conversations. An exact
	// file skips the broad check, so the home is checked here too. A project at the
	// home or another broad folder is left to that home check: Claude lists ~ itself.
	inside := slices.Clone(l.Own)
	for _, s := range shards {
		if isMe(s.Machine) {
			for _, p := range s.Projects {
				inside = append(inside, p.Path)
			}
		}
	}
	state, _ := claude.ProjectPaths(claude.StatePath(root))
	inside = slices.DeleteFunc(append(inside, state...), func(x string) bool {
		return !rewrite.AbsoluteAnywhere(x) || work.IsBroad(x, l.Env)
	})
	ownPath := func(p string) bool {
		return work.Within(l.Home, p, true) || work.Within(root, p, true) ||
			slices.ContainsFunc(inside, func(x string) bool { return work.Within(p, x, true) || work.Within(x, p, true) })
	}
	would := func(done string) string {
		if dryRun {
			return "would be " + done
		}
		return done
	}

	names := make([]string, 0, len(shards))
	for n := range shards {
		names = append(names, n)
	}
	sort.Strings(names) // the first machine to place a path keeps it
	for _, name := range names {
		s := shards[name]
		if isMe(s.Machine) || !(strings.HasPrefix(s.Home, "/") || work.WindowsPath(s.Home)) {
			continue
		}
		var n workRestoreResult // this machine's counts
		var went, notes []string
		refuse := func(p string, why any) {
			n.Refused++
			notes = append(notes, fmt.Sprintf("  ! %s: refused: %v", p, why))
		}
		for _, wf := range s.WorkFolders {
			dir, err := workDir(dest, s.Machine, wf.Path)
			if err != nil {
				refuse(wf.Path, err)
				continue
			}
			if !realArchivePath(dest, dir, wf.Exact) {
				continue // listed with no copy (over the cap, or not pushed yet), or a link
			}
			if !wf.Exact && broad(wf.Path) {
				refuse(wf.Path, "a broad folder")
				continue
			}
			target, err := work.Place(work.Source{Machine: s.Machine, Home: s.Home, Path: wf.Path, Exact: wf.Exact, Origin: wf.Origin}, l)
			if errors.Is(err, work.ErrRefused) {
				refuse(wf.Path, strings.TrimPrefix(err.Error(), work.ErrRefused.Error()+": "))
				continue
			} else if err != nil {
				continue // not a path on this machine: pull's same layout does not fit
			}
			held.Placed[s.Machine+"|"+wf.Path] = target
			went = append(went, wf.Path+" -> "+target)
			if target != wf.Path && ownPath(wf.Path) {
				refuse(wf.Path, "a path of this machine's own: its files are placed, conversations are not repointed")
			} else {
				if _, ok := res.Placed[wf.Path]; !ok {
					res.Placed[wf.Path] = target
				}
				if target != wf.Path {
					moves = append(moves, rewrite.Move{From: wf.Path, To: target, ConvertTail: work.WindowsPath(wf.Path) && runtime.GOOS != "windows"})
				}
			}

			// A file goes to trel below tdir. The push filter runs on trel, its path in
			// the folder, as push runs it below a root.
			tdir := target
			if wf.Exact {
				tdir = filepath.Dir(target)
			}
			sep := "/"
			if work.WindowsPath(wf.Path) {
				sep = `\`
			}
			file := func(src, rel string, fi fs.FileInfo) {
				from, trel := wf.Path+sep+strings.ReplaceAll(rel, "/", sep), rel
				if wf.Exact {
					from, trel = wf.Path, filepath.Base(target)
				}
				if ok, why := work.Allowed(trel, fi.Size()); !ok {
					refuse(from, why)
					return
				}
				if fi.ModTime().After(now.Add(time.Hour)) { // would stay "newer" than any edit here
					refuse(from, "dated in the future")
					return
				}
				dst, err := work.SafeJoin(tdir, trel)
				if err != nil {
					refuse(from, err)
					return
				}
				same := func(a fs.FileInfo) bool { return a.Size() == fi.Size() && archive.SameMTime(a.ModTime(), fi.ModTime()) }
				// mine: restored from this machine and unchanged since, so replaceable.
				mine := func(p string, st fs.FileInfo) bool {
					h, ok := held.Files[p]
					return ok && strings.EqualFold(h.Machine, s.Machine) && h.Size == st.Size() && archive.SameMTime(h.MTime, st.ModTime())
				}
				write := func(p string) bool {
					if dryRun {
						return true
					}
					if err := copyRestored(src, p, fi); err != nil {
						notes = append(notes, fmt.Sprintf("  x %s: %v", p, err))
						return false
					}
					if st, err := os.Stat(p); err == nil {
						held.Files[p] = work.HeldFile{Machine: s.Machine, From: from, Size: st.Size(), MTime: st.ModTime()}
					}
					return true
				}
				st, err := os.Lstat(dst)
				switch {
				case os.IsNotExist(err) || err == nil && update && st.Mode().IsRegular() && mine(dst, st) && !same(st):
					if write(dst) {
						n.Filed++
					}
				case err != nil || !update || !st.Mode().IsRegular() || mine(dst, st) || st.Size() == fi.Size() && sameContent(src, dst):
					n.Kept++ // add-only; never through a link
				default:
					// Kept, theirs beside it: replacing it would push theirs over ours everywhere.
					// The machine name is archive data too: one named "dll" would turn
					// "version" into "version.dll".
					ext := filepath.Ext(trel)
					name := "." + s.Machine + ext
					if ok, why := work.Allowed(strings.TrimSuffix(trel, ext)+name, fi.Size()); !ok {
						refuse(from, why)
						return
					}
					sib, err := work.SafeJoin(tdir, strings.TrimSuffix(trel, ext)+name)
					if err != nil {
						refuse(from, err)
						return
					}
					ss, err := os.Lstat(sib)
					if os.IsNotExist(err) || err == nil && ss.Mode().IsRegular() && !same(ss) && mine(sib, ss) {
						if write(sib) {
							n.Beside++
							notes = append(notes, fmt.Sprintf("  ~ %s kept; %s's copy %s beside it as %s", dst, s.Machine, would("written"), filepath.Base(sib)))
						}
					} else {
						n.Kept++
					}
				}
			}
			if wf.Exact {
				if fi, err := os.Lstat(dir); err == nil {
					file(dir, "", fi)
				}
				continue
			}
			// WalkDir never follows a link; only regular files are read.
			_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
				if err != nil || !d.Type().IsRegular() {
					return nil
				}
				fi, err := d.Info()
				rel, rerr := filepath.Rel(dir, p)
				if err == nil && rerr == nil {
					file(p, filepath.ToSlash(rel), fi)
				}
				return nil
			})
		}
		res.Filed, res.Kept, res.Beside, res.Refused = res.Filed+n.Filed, res.Kept+n.Kept, res.Beside+n.Beside, res.Refused+n.Refused
		if len(went) == 0 && n.Refused == 0 {
			continue
		}
		where := strings.Join(went, ", ")
		if where == "" {
			where = "none placed"
		}
		res.Notes = append(res.Notes, fmt.Sprintf("work folders from %s: %s (%d file(s) %s, %d kept, %d beside, %d refused)",
			s.Machine, where, n.Filed, would("filed"), n.Kept, n.Beside, n.Refused))
		res.Notes = append(res.Notes, notes...)
	}
	if !dryRun {
		if err := held.Save(heldWorkPath(root)); err != nil {
			res.Notes = append(res.Notes, fmt.Sprintf("  x restored work record: %v", err))
		}
	}
	return moves, res
}

// realArchivePath reports whether p, a work copy under dest/work, is a real folder
// (with exact, a regular file) reached through real folders only. The archive is
// shared: a link planted in it would make restore copy files from anywhere.
func realArchivePath(dest, p string, exact bool) bool {
	fi, err := os.Lstat(p)
	if err != nil || exact != fi.Mode().IsRegular() || !exact && !fi.IsDir() {
		return false
	}
	base := filepath.Join(dest, "work")
	for q := filepath.Dir(p); ; q = filepath.Dir(q) {
		if !archive.IsRealDir(q) {
			return false
		}
		if q == base || q == filepath.Dir(q) {
			return true
		}
	}
}

// copyRestored writes the archive file src to dst through a temp file and a rename,
// mode 0644 whatever the archive says, keeping its time. want is src as the walk saw
// it: a file swapped since, or grown past the cap, is not read.
func copyRestored(src, dst string, want fs.FileInfo) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	// Against a fresh Lstat, not want: on Windows a walked entry carries a file id only
	// on volumes that have object ids, so FAT, exFAT and some cloud drives never match.
	st, err := f.Stat()
	l, lerr := os.Lstat(src)
	if err != nil || lerr != nil || !st.Mode().IsRegular() || !l.Mode().IsRegular() || !os.SameFile(st, l) || st.Size() != want.Size() {
		return fmt.Errorf("changed in the archive while restoring")
	}
	data, err := io.ReadAll(io.LimitReader(f, work.MaxFileBytes+1))
	if err != nil {
		return err
	}
	if len(data) > work.MaxFileBytes {
		return fmt.Errorf("over %d MB", work.MaxFileBytes>>20)
	}
	if err := archive.WriteFileAtomic(dst, data, 0o644); err != nil {
		return err
	}
	return os.Chtimes(dst, want.ModTime(), want.ModTime())
}

// placedPath is where p lives here when it lies inside a placed work folder: the
// folder's target plus the rest of p, from the most specific folder. "" otherwise.
func placedPath(placed map[string]string, p string) string {
	best := ""
	for src := range placed {
		if work.Within(p, src, true) && (len(src) > len(best) || len(src) == len(best) && src < best) {
			best = src
		}
	}
	all := pathSegs(p)
	if best == "" || slices.Contains(all, "..") {
		return "" // archive data: a ".." here would climb out of the target
	}
	return filepath.Join(append([]string{placed[best]}, all[len(pathSegs(best)):]...)...)
}

// pathSegs splits a path of either OS form into its names, as work.Within counts them.
func pathSegs(s string) []string {
	return slices.DeleteFunc(strings.FieldsFunc(s, func(r rune) bool { return r == '/' || r == '\\' }), func(x string) bool { return x == "." })
}
