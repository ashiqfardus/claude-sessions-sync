package main

import (
	"bufio"
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
	// What import or pull filed here names folders only from lines added here since.
	imported := loadImported(root, dest, dryRun)
	for _, t := range scan {
		c, _ := work.Candidates(t, env.Home, imported[importedKey(root, t)]) // a transcript that fails part way still gives what it read
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
	real := map[string]bool{}
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
		linked := false
		for _, p := range c.Files {
			if budget > 0 && time.Since(start) > budget {
				return e, false
			}
			target := dir
			if !f.Exact {
				rel, _ := filepath.Rel(f.Path, p)
				target = filepath.Join(dir, rel)
			}
			if !realWorkDirs(dest, filepath.Dir(target), real) {
				if !linked {
					logf("WARN: work folder %s: its archive copy is reached through a link; not written", f.Path)
					linked = true
				}
				continue
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

// realWorkDirs reports whether every folder from dest/work down to dir that exists is
// a real folder, not a link or anything else. The archive is shared: a link planted
// in it would make a write or a delete there land in a local folder. real caches the
// folders already found real in this run.
func realWorkDirs(dest, dir string, real map[string]bool) bool {
	base := filepath.Join(dest, "work")
	rel, err := filepath.Rel(base, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	q := base
	for _, s := range append([]string{"."}, strings.Split(rel, string(filepath.Separator))...) {
		if q = filepath.Join(q, s); real[q] {
			continue
		}
		if _, err := os.Lstat(q); errors.Is(err, fs.ErrNotExist) {
			return true // nothing below it to follow
		}
		if !archive.IsRealDir(q) {
			return false
		}
		real[q] = true
	}
	return true
}

// purgeWork removes this machine's archived copies of folder: every shard entry inside
// it, and, when folder lies inside an archived entry, just that subfolder (the entry
// stays). It returns the folders removed. What workDir refuses, or a link reaches, is
// never deleted. The caller holds the push lock.
func purgeWork(dest, machine, folder string) ([]string, error) {
	shard := readShard(dest, machine)
	var keep []archive.WorkFolder
	var removed []string
	real := map[string]bool{}
	drop := func(p string) error {
		dir, err := workDir(dest, machine, p)
		if err != nil || !realWorkDirs(dest, filepath.Dir(dir), real) {
			return nil // hostile or malformed: skip, never delete
		}
		if _, err := os.Lstat(dir); err != nil {
			return nil
		}
		if err := os.RemoveAll(dir); err != nil { // never follows a link: removes the link
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
// its local target; ByMachine, keyed machine + "|" + source as Held.Placed is, holds
// every placement of this run, own paths included, and Prev the earlier targets of
// those still remembered (Held.Prev). Notes are the lines import and pull print.
type workRestoreResult struct {
	Placed, ByMachine            map[string]string
	Prev                         map[string][]string
	Filed, Kept, Beside, Refused int
	Notes                        []string
}

// ownPaths reports whether p is a path of this machine's own (R17/R19): its home or
// above it, in or above its Claude folder, a temp folder, one of its work folders or
// projects. A move from one would repoint this machine's own conversations, so none is
// made. An exact file skips the broad check, so the home is checked here too. A
// project at the home or another broad folder is left to that home check: Claude
// lists ~ itself.
func ownPaths(root string, env work.Env, shards map[string]archive.Shard) func(string) bool {
	// The Claude folder matters when it is no dot folder (/data/claude), which a work
	// folder could then lie in.
	inside := append([]string{root}, env.Temp...)
	own, _ := loadWorkFolders(root)
	for _, f := range own {
		inside = append(inside, f.Path)
	}
	for _, s := range shards {
		if isMe(s.Machine) {
			for _, p := range s.Projects {
				inside = append(inside, p.Path)
			}
		}
	}
	state, _ := claude.ProjectPaths(claude.StatePath(root))
	inside = slices.DeleteFunc(append(inside, state...), func(x string) bool {
		return !rewrite.AbsoluteAnywhere(x) || work.IsBroad(x, env)
	})
	return func(p string) bool {
		return work.Within(env.Home, p, true) ||
			slices.ContainsFunc(inside, func(x string) bool { return work.Within(p, x, true) || work.Within(x, p, true) })
	}
}

// remapMoves repoints each folder placed elsewhere since - by map-folder or
// work-placement - from its earlier targets (prev, Held.Prev) to the one it has now.
// Both ends come from this machine's own record, not from the archive, so no own-path
// check applies (R23): an old target is one of this machine's work folders as soon as
// it has pushed once. A target inside the other gets no move, as rules apply in
// sequence: old -> old/sub would turn old/sub/x into old/sub/sub/x. Notes name those.
func remapMoves(prev map[string][]string, now map[string]string) (moves []rewrite.Move, notes []string) {
	keys := make([]string, 0, len(prev))
	for k := range prev {
		keys = append(keys, k)
	}
	sort.Strings(keys) // the first move for a path wins
	for _, k := range keys {
		t, ok := now[k]
		if !ok {
			continue
		}
		_, src, _ := strings.Cut(k, "|")
		for _, old := range prev[k] {
			if work.Within(t, old, true) || work.Within(old, t, true) {
				notes = append(notes, fmt.Sprintf("  ! %s was placed at %s and is now at %s, one inside the other: conversations naming the old place are not repointed; edit them by hand", src, old, t))
				continue
			}
			moves = append(moves, rewrite.Move{From: old, To: t})
		}
	}
	return moves, notes
}

// forgetPrevious drops the earlier targets of the folders in placed, once a --repoint
// has moved every conversation off them.
func forgetPrevious(root string, placed map[string]string) error {
	h := work.LoadHeld(heldWorkPath(root))
	if h.Err != nil {
		return h.Err
	}
	n := len(h.Prev)
	for k := range placed {
		delete(h.Prev, k)
	}
	if len(h.Prev) == n {
		return nil
	}
	return h.Save(heldWorkPath(root))
}

// restoreWork places other machines' work folders here and files their files (spec
// §6-§7), returning the moves that repoint transcripts to them. sameLayout is pull's
// case: recorded paths, as they are. Everything read from the archive is untrusted:
// each folder goes through work.Place, each file through the push filter and
// work.SafeJoin, and files are written add-only, mode 0644, never through a link.
func restoreWork(root, dest string, shards map[string]archive.Shard, cfg archive.Config, dryRun, update, sameLayout bool) ([]rewrite.Move, workRestoreResult) {
	res := workRestoreResult{Placed: map[string]string{}, ByMachine: map[string]string{}}
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
	ownPath := ownPaths(root, l.Env, shards)
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
			// Placed elsewhere before: remembered until a --repoint moves conversations off it,
			// unless that was a place this machine's own conversations may name (I1).
			key := s.Machine + "|" + wf.Path
			old, ok := held.Placed[key]
			if ok && !samePath(old, target) && !held.NoPrev[key] && !sameLayout &&
				!slices.ContainsFunc(held.Prev[key], func(x string) bool { return samePath(x, old) }) {
				held.Prev[key] = append(held.Prev[key], old)
			}
			if held.Prev[key] = slices.DeleteFunc(held.Prev[key], func(x string) bool { return samePath(x, target) }); len(held.Prev[key]) == 0 {
				delete(held.Prev, key)
			}
			if !ok || !samePath(old, target) || sameLayout {
				// Decided when first placed: once worked in here, every target is an own path.
				held.NoPrev[key] = sameLayout || ownPath(target)
			}
			held.Placed[key] = target
			res.ByMachine[key] = target
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
	res.Prev = held.Prev
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

// repointResult is what repointLocal did: transcripts rewritten (with dryRun, that
// would be), left alone as used in the last hour, and left as they were after an
// error, named in Notes. Backup is the folder holding the originals.
type repointResult struct {
	Rewritten, Skipped, Recent int
	Backup                     string
	Notes                      []string
}

// repointedStat is a transcript as --repoint left it. While it is unchanged, push
// takes it over the archived copy, shorter or not (spec §7a).
type repointedStat struct {
	Size  int64     `json:"size"`
	MTime time.Time `json:"mtime"`
}

// repointedPath records, by "bucket/id", the transcripts --repoint rewrote that push
// has not archived since.
func repointedPath(root string) string {
	return filepath.Join(root, "session-sync-base", "repointed.json")
}

// loadRepointed reads that record. Missing or unreadable is empty: push then treats
// those transcripts as it treats any other.
func loadRepointed(root string) map[string]repointedStat {
	var out map[string]repointedStat
	if raw, err := os.ReadFile(repointedPath(root)); err == nil {
		_ = json.Unmarshal(raw, &out)
	}
	if out == nil {
		out = map[string]repointedStat{}
	}
	return out
}

func saveRepointed(root string, m map[string]repointedStat) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return archive.WriteFileAtomic(repointedPath(root), append(data, '\n'), 0o644)
}

// importedPath records the size each transcript had when import or pull last filed it
// here, by importedKey. Push looks for work folders only past that size: the bytes
// before it came from the archive, where anyone who can write it could name any local
// folder for the next push to upload.
func importedPath(root string) string {
	return filepath.Join(root, "session-sync-base", "imported.json")
}

// importedKey is a transcript's path below the projects folder, lower-cased and
// "/"-separated.
func importedKey(root, p string) string {
	if rel, err := filepath.Rel(claude.ProjectsDir(root), p); err == nil {
		p = rel
	}
	return strings.ToLower(filepath.ToSlash(p))
}

// noteFiled records the transcript just filed at p, at its size now. m is nil where
// nothing is recorded (restore, --dry-run).
func noteFiled(m map[string]int64, root, p string) {
	if m == nil || !strings.EqualFold(filepath.Ext(p), ".jsonl") {
		return
	}
	if st, err := os.Lstat(p); err == nil {
		m[importedKey(root, p)] = st.Size()
	}
}

// loadImported reads that record. When it is missing or unreadable, or a run that
// was filing died before saving it (the .pending mark), what that run filed is not
// in it: the transcripts the archive says another machine wrote are added at their
// size now (seedImported), and unless dryRun the result is saved.
func loadImported(root, dest string, dryRun bool) map[string]int64 {
	var m map[string]int64
	raw, err := os.ReadFile(importedPath(root))
	_, pending := os.Lstat(importedPath(root) + ".pending")
	if err == nil && json.Unmarshal(raw, &m) == nil && m != nil && pending != nil {
		return m
	}
	if m == nil {
		m = map[string]int64{}
	}
	for k, n := range seedImported(root, dest) {
		m[k] = max(m[k], n)
	}
	if !dryRun {
		_ = endFiling(root, m)
	}
	return m
}

// seedImported finds the transcripts filed here before the record existed: each one,
// with its subagent transcripts, of a session the archive says another machine wrote,
// and each fork filed here. Its bytes so far are all taken as filed.
func seedImported(root, dest string) map[string]int64 {
	m := map[string]int64{}
	if dest == "" {
		return m
	}
	writer := sessionWriters(dest)
	theirs := map[string]bool{}
	names, _ := archive.BucketNames(dest)
	for _, b := range names {
		entries, _ := os.ReadDir(filepath.Join(archive.ProjectsDir(dest), b))
		for _, e := range entries {
			id, ok := strings.CutSuffix(e.Name(), ".jsonl")
			if info, err := e.Info(); ok && err == nil && e.Type().IsRegular() && writer(b, id, info) != "" {
				theirs[strings.ToLower(id)] = true
			}
		}
	}
	buckets, _ := claude.ListBuckets(claude.ProjectsDir(root))
	for _, b := range buckets {
		forks := loadFiledForks(filedForksPath(root, b.Dir))
		for _, t := range b.Transcripts {
			if _, fork := forks[strings.ToLower(t.ID)]; !fork && !theirs[strings.ToLower(t.ID)] {
				continue
			}
			noteFiled(m, root, t.Path)
			for _, x := range t.Extras {
				if filepath.Dir(x) == "subagents" {
					noteFiled(m, root, filepath.Join(b.Dir, t.ID, x))
				}
			}
		}
	}
	return m
}

// beginFiling marks the record as out of date until endFiling saves it: a run killed
// in between leaves the mark, and the next load rebuilds what it filed.
func beginFiling(root string) error {
	return archive.WriteFileAtomic(importedPath(root)+".pending", nil, 0o644)
}

// startFiling loads the record for a run that files transcripts, and marks it out of
// date until finishFiling. nil under dryRun: nothing is filed.
func startFiling(root, dest string, dryRun bool) map[string]int64 {
	if dryRun {
		return nil
	}
	m := loadImported(root, dest, false)
	if err := beginFiling(root); err != nil {
		fmt.Printf("  x %s: %v\n", importedPath(root), err)
	}
	return m
}

// finishFiling saves what startFiling began.
func finishFiling(root string, m map[string]int64, dryRun bool) {
	if dryRun {
		return
	}
	if err := endFiling(root, m); err != nil {
		fmt.Printf("  x %s: %v (the next run rebuilds it from the archive)\n", importedPath(root), err)
	}
}

// endFiling saves the record and clears the mark.
func endFiling(root string, m map[string]int64) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := archive.WriteFileAtomic(importedPath(root), append(data, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Remove(importedPath(root) + ".pending"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// errLive is a transcript that changed while it was rewritten: a session in use.
var errLive = errors.New("changed while being rewritten")

// repointLocal applies work-folder moves to the transcripts already here, subagent
// transcripts included (spec §7a). A file changed in the hour before now may belong
// to a live session and is left alone. Each original is copied to
// <root>/repoint-backup/<yyyymmdd-hhmm>/<bucket>/ before the rewrite replaces it, and
// with dryRun nothing at all is written. The caller holds the lock.
func repointLocal(root string, moves []rewrite.Move, now time.Time, dryRun bool, imported map[string]int64) repointResult {
	var res repointResult
	rules := rewrite.RulesForMoves(moves)
	if len(rules) == 0 {
		return res
	}
	finds := make([]string, len(rules))
	for i, r := range rules {
		finds[i] = strings.ToLower(r.Find)
	}
	rec, recorded := loadRepointed(root), false
	buckets, _ := claude.ListBuckets(claude.ProjectsDir(root))
	for _, b := range buckets {
		for _, t := range b.Transcripts {
			rels := []string{filepath.Base(t.Path)}
			for _, x := range t.Extras {
				if filepath.Dir(x) == "subagents" && strings.EqualFold(filepath.Ext(x), ".jsonl") {
					rels = append(rels, filepath.Join(t.ID, x))
				}
			}
			for i, rel := range rels {
				p := filepath.Join(b.Dir, rel)
				st, err := os.Lstat(p)
				if err != nil || !st.Mode().IsRegular() || !pointing(p, rules, finds) {
					continue
				}
				if st.ModTime().After(now.Add(-time.Hour)) {
					res.Recent++
					continue
				}
				if dryRun {
					res.Rewritten++
					continue
				}
				if res.Backup == "" {
					if res.Backup, err = claimBackup(root, now); err != nil {
						res.Backup = ""
						res.Skipped++
						res.Notes = append(res.Notes, fmt.Sprintf("  x %s: backup: %v", p, err))
						continue
					}
				}
				switch err := repointFile(p, b.Dir, filepath.Join(res.Backup, b.Name, rel), rules, st); {
				case errors.Is(err, errLive):
					res.Recent++
				case err != nil:
					res.Skipped++
					res.Notes = append(res.Notes, fmt.Sprintf("  x %s: %v", p, err))
				default:
					res.Rewritten++
					if st, err := os.Lstat(p); err == nil && i == 0 {
						rec[b.Name+"/"+t.ID], recorded = repointedStat{Size: st.Size(), MTime: st.ModTime()}, true
					}
					// Rewritten whole, it may have grown: what was filed is now all of it.
					if _, ok := imported[importedKey(root, p)]; ok {
						noteFiled(imported, root, p)
					}
				}
			}
		}
	}
	if recorded {
		if err := saveRepointed(root, rec); err != nil {
			res.Notes = append(res.Notes, fmt.Sprintf("  x %s: %v (push keeps the archived copies of those conversations)", repointedPath(root), err))
		}
	}
	return res
}

// countPointing is how many transcripts here a --repoint would rewrite: the same files
// and the same hour's grace, read without writing.
func countPointing(root string, moves []rewrite.Move) int {
	return repointLocal(root, moves, time.Now(), true, nil).Rewritten
}

// pointing reports whether a rule would change the file at p. It reads a line at a
// time, never the whole file, and matches in full only a line holding one of finds
// (the rules' Find strings, lower-cased: rules ignore case).
func pointing(p string, rules []rewrite.Rule, finds []string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 256<<10)
	for {
		line, err := r.ReadString('\n')
		low := strings.ToLower(line)
		if slices.ContainsFunc(finds, func(x string) bool { return strings.Contains(low, x) }) && rewrite.Apply(line, rules, nil) != line {
			return true
		}
		if err != nil {
			return false
		}
	}
}

// claimBackup makes this run's backup folder, <root>/repoint-backup/<yyyymmdd-hhmm>,
// or -2, -3... after it when an earlier run this minute took the name: a second run
// must never overwrite the first one's originals.
func claimBackup(root string, now time.Time) (string, error) {
	base := filepath.Join(root, "repoint-backup")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	for i := 1; ; i++ {
		d := filepath.Join(base, now.Format("20060102-1504"))
		if i > 1 {
			d += fmt.Sprintf("-%d", i)
		}
		if err := os.Mkdir(d, 0o700); !os.IsExist(err) {
			return d, err
		}
	}
}

// repointFile rewrites p, as it was at st, into a temp file in dir, copies the
// original to backup, and only then renames the rewrite over p. Whatever fails, p is
// left as it was. dir is the bucket folder, on p's file system: a temp file left by a
// crash there is no .jsonl, and nothing reads it, where one in a session's own folder
// would be pushed with it.
func repointFile(p, dir, backup string, rules []rewrite.Rule, st fs.FileInfo) error {
	f, err := os.CreateTemp(dir, ".tmp-repoint-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	f.Close()
	defer os.Remove(tmp) // already gone once renamed over p
	if _, err := rewrite.File(p, tmp, rules, ""); err != nil {
		return err
	}
	if err := archive.CopyFile(p, backup); err != nil {
		return err
	}
	// A session resumed meanwhile has lines the rewrite lacks.
	if cur, err := os.Lstat(p); err != nil || cur.Size() != st.Size() || !cur.ModTime().Equal(st.ModTime()) {
		return errLive
	}
	return os.Rename(tmp, p)
}
