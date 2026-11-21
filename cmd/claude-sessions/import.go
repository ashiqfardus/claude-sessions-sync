package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/identity"
	"github.com/ashiqfardus/claude-sessions-sync/internal/rewrite"
	"github.com/ashiqfardus/claude-sessions-sync/internal/work"
)

type importRow struct {
	Bucket   string `json:"bucket"`
	Recorded string `json:"recordedPath"`
	Local    string `json:"localPath,omitempty"`
	How      string `json:"matchedBy"`
	Filed    int    `json:"filed"`
	Updated  int    `json:"updated,omitempty"`
	Skipped  int    `json:"skipped"`
	Note     string `json:"note,omitempty"`
}

// cmdImport is the reason this project exists: it files an archive's sessions onto
// this machine even when the projects live at different paths here.
func cmdImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	archiveDir := fs.String("archive", "", "override the synced destination folder")
	roots := fs.String("search-root", "", "comma-separated folders to scan for projects (default: parents of known projects, plus your home)")
	depth := fs.Int("depth", 3, "how deep to scan under each search root")
	dryRun := fs.Bool("dry-run", false, "report what would happen without writing")
	bareName := fs.Bool("include-bare-name", false, "also replace the bare folder name inside transcripts")
	update := fs.Bool("update", false, updateHelp)
	noPlugins := fs.Bool("no-plugins", false, noPluginsHelp)
	asJSON := fs.Bool("json", false, "machine-readable output")
	repoint := fs.Bool("repoint", false, "also rewrite conversations already here that name other machines' work folders (originals are backed up; any used in the last hour is skipped)")
	if err := parseFlags(fs, args, false); err != nil {
		return err
	}

	root, _, err := resolveRoot(*claudeDir)
	if err != nil {
		return err
	}
	dest, src, err := archive.ResolveDestination(root, *archiveDir)
	if err != nil {
		return err
	}
	if dest == "" {
		return fmt.Errorf("no archive destination: run `claude-sessions config set-destination <folder>`")
	}
	if st, err := os.Stat(dest); err != nil || !st.IsDir() {
		return fmt.Errorf("%s is not reachable - is the sync client mounted?", dest)
	}

	manifest, bad, err := archive.Merged(dest)
	if err != nil {
		return err
	}

	// A bucket the manifest does not know - a forgotten machine's, one from a restored
	// backup zip, one archived by the PowerShell tools - still records where its
	// sessions ran, in each transcript's cwd. That is the same source push reads, so it
	// is used rather than giving up on the bucket.
	names, _ := archive.BucketNames(dest)
	for _, name := range names {
		if _, known := manifest[name]; known {
			continue
		}
		if cwd := newestCwd(filepath.Join(archive.ProjectsDir(dest), name)); cwd != "" {
			manifest[name] = archive.Project{Path: cwd, Leaf: rewrite.LastSegment(cwd), Source: archive.FromTranscript}
		}
	}
	if len(manifest) == 0 {
		return fmt.Errorf("nothing to import: %s holds no sessions - run `claude-sessions push` on a machine that has them first", dest)
	}

	// Writing into ~/.claude/projects while a scheduled push reads it is what the lock
	// exists to prevent.
	lock, err := archive.AcquireLock(root)
	if err != nil {
		return err
	}
	defer lock.Release()

	searchRoots := defaultSearchRoots(manifest, *roots)
	// One walk of the search roots answers every project, instead of one walk each.
	scanner := identity.NewScanner(searchRoots, *depth)

	if !*asJSON {
		fmt.Printf("archive      %s (%s)\n", dest, src)
		fmt.Printf("search roots %s\n\n", strings.Join(searchRoots, ", "))
		for _, r := range searchRoots {
			// A typo here otherwise surfaces only as every project being "not found".
			if st, err := os.Stat(r); err != nil || !st.IsDir() {
				fmt.Printf("! search root %s does not exist\n", r)
			}
		}
		for _, b := range bad {
			fmt.Printf("! ignoring manifest file %s\n", b)
		}
	}

	// Every bucket in the archive, not only the ones the manifest knows. A bucket with
	// no recorded identity, or with memory but no sessions, used to be skipped without
	// a word - which reads as "handled".
	buckets := make([]string, 0, len(manifest))
	for name := range manifest {
		buckets = append(buckets, name)
	}
	for _, name := range names {
		if _, known := manifest[name]; !known {
			buckets = append(buckets, name)
		}
	}
	sort.Strings(buckets)

	shards, _, _ := archive.ReadShards(dest)

	// Another machine's home folder is this one's home by definition, however
	// different the two paths are. Only a home the machine recorded counts: guessing
	// it from a profile path took any folder holding a .claude for a home.
	localHome, _ := os.UserHomeDir()
	var homes []string
	for _, s := range shards {
		if s.Home != "" {
			homes = append(homes, s.Home)
		}
	}
	backupRoot, backupHome := backupOrigin(dest)
	if backupHome != "" {
		homes = append(homes, backupHome)
	}
	// The user's own mapping beats every inference: it is the answer for a project that
	// nothing identifies, and it is what they asked for.
	cfg, cerr := archive.LoadConfig(root)
	// Other machines' work folders, placed before any transcript is filed: a project
	// inside one lives where it landed, and their moves repoint the transcripts. A
	// config that cannot be read may have lost "work off" or a mapping.
	wcfg := cfg
	if cerr != nil {
		wcfg.WorkBackup = "off"
	}
	// Where folders were placed before this import, which restoreWork records anew: a
	// changed placement repoints its old target too (R7).
	before := work.LoadHeld(heldWorkPath(root)).Placed
	workMoves, workRes := restoreWork(root, dest, shards, wcfg, *dryRun, *update, false)
	workMoves = append(workMoves, remapMoves(root, shards, before, workRes.ByMachine)...)
	resolve := func(bucket string, p archive.Project) identity.Match {
		if local, ok := cfg.Mapped(bucket); ok {
			if st, err := os.Stat(local); err != nil || !st.IsDir() {
				return identity.Match{How: mappedMissing, Candidates: []string{local}}
			}
			return identity.Match{How: identity.ByManual, Path: local}
		}
		if t := placedPath(workRes.Placed, p.Path); t != "" {
			return identity.Match{How: byWorkFolder, Path: t}
		}
		m := identity.Resolve(p.Path, p.Leaf, p.Remote, scanner)
		if m.How == identity.ByRecordedPath || localHome == "" {
			return m
		}
		for _, h := range homes {
			if samePath(p.Path, h) {
				return identity.Match{How: identity.ByHomeFolder, Path: localHome}
			}
		}
		return m
	}

	// Every repository that moved, known before any transcript is filed, decided by
	// the same rules that file the projects - a mapping or the home folder included.
	// A session in a multi-folder workspace names its siblings too, and import used to
	// repoint only the project a session started in. Related repositories are placed
	// by git remote alone: a same-named folder is never enough to rewrite paths into.
	//
	// Projects go first, in a fixed order, and a related path that is itself a
	// project's is left to that project: the first move for a path is the one used,
	// and walking a map put a related remote match ahead of a mapping at random.
	projects := make([]string, 0, len(manifest))
	claimed := map[string]bool{}
	for name, p := range manifest {
		projects = append(projects, name)
		if p.Path != "" {
			claimed[strings.ToLower(strings.TrimRight(p.Path, `\/`))] = true
		}
	}
	sort.Strings(projects)
	var moves []rewrite.Move
	for _, name := range projects {
		p := manifest[name]
		if m := resolve(name, p); m.Path != "" && !samePath(p.Path, m.Path) {
			moves = append(moves, rewrite.Move{From: p.Path, To: m.Path})
		}
	}
	for _, name := range projects {
		for _, r := range manifest[name].Related {
			if claimed[strings.ToLower(strings.TrimRight(r.Path, `\/`))] {
				continue
			}
			m := identity.Resolve(r.Path, rewrite.LastSegment(r.Path), r.Remote, scanner)
			if m.How == identity.ByGitRemote && !samePath(r.Path, m.Path) {
				moves = append(moves, rewrite.Move{From: r.Path, To: m.Path})
			}
		}
	}
	// Each machine's own ~/.claude, named by saved tool output and by references to
	// CLAUDE.md and skills, becomes this one's - those files are restored here too.
	for _, s := range shards {
		if s.ClaudeRoot != "" && !samePath(s.ClaudeRoot, root) {
			moves = append(moves, rewrite.Move{From: s.ClaudeRoot, To: root})
		}
	}
	if backupRoot != "" && !samePath(backupRoot, root) {
		moves = append(moves, rewrite.Move{From: backupRoot, To: root})
	}
	moves = append(moves, workMoves...)
	writers := sessionWriters(dest)
	former := formerNames(dest)

	var rows []importRow
	var filed, updated, sameLayout, notFound, ambiguous int

	report := func(row importRow, marker string) {
		rows = append(rows, row)
		if !*asJSON {
			fmt.Printf("%s %s: %s\n", marker, row.Bucket, row.Note)
		}
	}

	for _, bucket := range buckets {
		p, known := manifest[bucket]
		archived := filepath.Join(archive.ProjectsDir(dest), bucket)
		if _, mapped := cfg.Mapped(bucket); !known && mapped {
			known = true // placed by the mapping; with no recorded path there is nothing to rewrite
		}
		if !known {
			notFound++
			report(importRow{Bucket: bucket, How: string(identity.NotFound),
				Note: "no project path was ever recorded for it; " + mapHint(bucket)}, "-")
			continue
		}
		if !hasTranscripts(archived) {
			if _, err := os.Stat(filepath.Join(archived, "memory")); err != nil {
				continue // nothing archived under this name
			}
			// No sessions to rewrite, but the project's memory still belongs somewhere.
			match := resolve(bucket, p)
			row := importRow{Bucket: bucket, Recorded: p.Path, How: string(match.How)}
			if match.Path == "" {
				notFound++
				row.Note = "memory only, and no local folder matches it; " + mapHint(bucket)
				report(row, "-")
				continue
			}
			local := claude.Slug(match.Path)
			if existing, ok := claude.FindBucket(claude.ProjectsDir(root), local); ok {
				local = existing.Name
			}
			n := restoreProjectMemory(root, dest, bucket, filepath.Join(archived, "memory"),
				filepath.Join(claude.ProjectsDir(root), local, "memory"), p.Owner(), *dryRun, *update)
			row.Local = match.Path
			row.Note = fmt.Sprintf("memory only -> %s (matched by %s): %d memory file(s) filed", match.Path, match.How, n)
			report(row, "+")
			continue
		}

		match := resolve(bucket, p)
		row := importRow{Bucket: bucket, Recorded: p.Path, How: string(match.How)}

		switch match.How {
		case mappedMissing:
			row.Note = fmt.Sprintf("mapped to %s, which does not exist; %s", match.Candidates[0], mapHint(bucket))
			notFound++
			rows = append(rows, row)
			if !*asJSON {
				fmt.Printf("- %s: %s\n", bucket, row.Note)
			}
			continue

		case identity.NotFound:
			row.Note = fmt.Sprintf("no folder named %q under the search roots", p.Leaf)
			if len(match.Candidates) > 0 {
				row.Note = fmt.Sprintf("%q exists here but is a different repository", p.Leaf)
			}
			notFound++
			rows = append(rows, row)
			// Say so. A project that is silently absent from the output looks like it
			// was handled, and the user has no reason to go looking for it.
			if !*asJSON {
				fmt.Printf("- %s: %s\n", bucket, row.Note)
				for _, c := range match.Candidates {
					fmt.Printf("      %s (different remote)\n", c)
				}
				fmt.Printf("      %s\n", mapHint(bucket))
			}
			continue

		case identity.Ambiguous:
			row.Note = fmt.Sprintf("%d folders named %q and no git remote settles it", len(match.Candidates), p.Leaf)
			ambiguous++
			rows = append(rows, row)
			if !*asJSON {
				fmt.Printf("! %s: %s\n", bucket, row.Note)
				for _, c := range match.Candidates {
					fmt.Printf("      %s\n", c)
				}
				fmt.Printf("      %s\n", mapHint(bucket))
			}
			continue
		}

		row.Local = match.Path

		// Only rewrite when the path actually differs; an identical layout needs no
		// edit, and not touching the bytes is always safer.
		rewriteFrom := ""
		if !samePath(p.Path, match.Path) {
			rewriteFrom = p.Path
		}

		res, err := restoreInto(root, archived, match.Path, rewriteFrom, moves,
			restoreOpts{BareName: *bareName, DryRun: *dryRun, Update: *update,
				Owner: p.Owner(), OwnerOf: writers, Forks: filepath.Join(dest, "forks", bucket), Archive: dest, FormerNames: former})
		if err != nil {
			row.Note = err.Error()
			rows = append(rows, row)
			if !*asJSON {
				fmt.Printf("x %s: %s\n", bucket, err)
			}
			continue
		}
		row.Filed, row.Skipped, row.Updated = res.Copied, res.Skipped, res.Updated
		if len(res.Failed) > 0 {
			row.Note = fmt.Sprintf("%d transcript(s) not filed: %s", len(res.Failed), strings.Join(res.Failed, "; "))
		}
		if len(res.Diverged) > 0 {
			row.Note = strings.TrimSpace(row.Note + fmt.Sprintf(" %d session(s) continued here and elsewhere, local kept and the other filed beside it: %s",
				len(res.Diverged), strings.Join(res.Diverged, "; ")))
		}
		filed += res.Copied
		updated += res.Updated
		if match.How == identity.ByRecordedPath {
			sameLayout++
		}
		rows = append(rows, row)

		if !*asJSON {
			marker := "+"
			if match.How == identity.ByRecordedPath {
				marker = "="
			}
			fmt.Printf("%s %s -> %s (matched by %s): %d filed, %d already present",
				marker, bucket, match.Path, match.How, res.Copied, res.Skipped)
			if res.Updated > 0 {
				fmt.Printf(", %d updated", res.Updated)
			}
			fmt.Println()
			for _, f := range res.Failed {
				fmt.Printf("    x %s\n", f)
			}
			for _, d := range res.Diverged {
				fmt.Printf("    ! continued here and elsewhere - %s (resume either)\n", d)
			}
		}
	}

	// Conversations already here are rewritten only when asked: that edits the user's
	// own files (spec §7a). Work-folder moves only, never project moves.
	var rp repointResult
	if *repoint {
		rp = repointLocal(root, workMoves, time.Now(), *dryRun)
	}

	profile := restoreProfile(root, dest, *dryRun, *update, !*noPlugins)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if rows == nil {
			rows = []importRow{}
		}
		return enc.Encode(rows)
	}

	fmt.Printf("\n%d transcript(s) filed", filed)
	if *update {
		fmt.Printf(", %d updated", updated)
	}
	fmt.Printf(". %d same layout, %d not found, %d ambiguous.\n", sameLayout, notFound, ambiguous)
	for _, n := range workRes.Notes {
		fmt.Println(n)
	}
	if len(workRes.Placed) > 0 {
		fmt.Println("  to place them elsewhere: claude-sessions config map-folder '<path from above>' '<local folder>', or claude-sessions config work-placement home|machine")
	}
	if *repoint {
		verb := "repointed"
		if *dryRun {
			verb = "would repoint"
		}
		fmt.Printf("%s %d conversation(s) to this machine's work folders", verb, rp.Rewritten)
		if rp.Backup != "" {
			fmt.Printf("; the originals are in %s", rp.Backup)
		}
		fmt.Println()
		if rp.Recent > 0 {
			fmt.Printf("%d conversation(s) used in the last hour left alone: run import --repoint again later\n", rp.Recent)
		}
		if rp.Skipped > 0 {
			fmt.Printf("%d conversation(s) left as they were after an error:\n", rp.Skipped)
		}
		for _, n := range rp.Notes {
			fmt.Println(n)
		}
	} else if len(workMoves) > 0 {
		if n := countPointing(root, workMoves); n > 0 {
			fmt.Printf("%d conversation(s) here still point at other machines' work folders: run claude-sessions import --repoint\n", n)
		}
	}
	printProfile(profile, *dryRun)
	if !*update && sameLayout+filed > 0 {
		fmt.Println("Sessions already here are never replaced; --update refreshes ones that continued elsewhere.")
	}
	if notFound+ambiguous > 0 {
		fmt.Println("\nFor anything not placed, say where it lives here once, then run import again:")
		fmt.Println("  claude-sessions config map <bucket> '<local folder>'")
	}
	return nil
}

// newestCwd is the working directory recorded by the newest transcript in an archived
// bucket, or "" when there is none.
func newestCwd(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var newest string
	var at time.Time
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.EqualFold(filepath.Ext(e.Name()), ".jsonl") {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().After(at) {
			newest, at = e.Name(), info.ModTime()
		}
	}
	if newest == "" {
		return ""
	}
	s, _ := claude.ScanHead(filepath.Join(dir, newest), headScanLines)
	return s.Cwd
}

func hasTranscripts(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".jsonl") {
			return true
		}
	}
	return false
}

// defaultSearchRoots picks somewhere sensible to look when the user has not said.
//
// The parents of projects this machine already knows about are the best guess: they
// are where this user actually keeps code. The home directory is added as a backstop.
func defaultSearchRoots(manifest map[string]archive.Project, flagValue string) []string {
	if strings.TrimSpace(flagValue) != "" {
		var out []string
		for _, r := range strings.Split(flagValue, ",") {
			if r = expandHome(strings.TrimSpace(r)); r != "" {
				out = append(out, r)
			}
		}
		return out
	}

	seen := map[string]bool{}
	var roots []string
	add := func(p string) {
		// Recorded paths come from the archive: a network path here would be scanned.
		if !identity.Local(p) {
			return
		}
		key := strings.ToLower(p)
		if seen[key] {
			return
		}
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			return
		}
		seen[key] = true
		roots = append(roots, p)
	}

	for _, p := range manifest {
		if p.Path != "" {
			add(filepath.Dir(p.Path))
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		add(home)
	}
	sort.Strings(roots)
	return roots
}

// cmdPull is the blunt counterpart to import: copy archived transcripts into buckets
// of the same name, with no path rewriting and no identity matching.
//
// Correct only when the machines share a layout. import is what you want otherwise,
// and the output says so.
func cmdPull(args []string) error {
	fs := flag.NewFlagSet("pull", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	archiveDir := fs.String("archive", "", "override the synced destination folder")
	dryRun := fs.Bool("dry-run", false, "report what would be copied without writing")
	update := fs.Bool("update", false, updateHelp)
	noPlugins := fs.Bool("no-plugins", false, noPluginsHelp)
	if err := parseFlags(fs, args, false); err != nil {
		return err
	}

	root, _, err := resolveRoot(*claudeDir)
	if err != nil {
		return err
	}
	dest, _, err := archive.ResolveDestination(root, *archiveDir)
	if err != nil {
		return err
	}
	if dest == "" {
		return fmt.Errorf("no archive destination: run `claude-sessions config set-destination <folder>`")
	}

	archivedBuckets, err := archive.BucketNames(dest)
	if err != nil {
		return err
	}
	if len(archivedBuckets) == 0 {
		fmt.Println("Nothing to pull: the archive holds no projects.")
		return nil
	}

	// Pulling writes into ~/.claude/projects, which a scheduled push reads.
	lock, err := archive.AcquireLock(root)
	if err != nil {
		return err
	}
	defer lock.Release()

	projectsDir := claude.ProjectsDir(root)
	manifest, _, _ := archive.Merged(dest)
	writers := sessionWriters(dest)
	var pulled, updated, already, memory int
	var forked, failed []string

	for _, name := range archivedBuckets {
		srcDir := filepath.Join(archive.ProjectsDir(dest), name)

		// Match an existing local bucket case-insensitively before creating one.
		targetName := name
		if existing, ok := claude.FindBucket(projectsDir, name); ok {
			targetName = existing.Name
		}

		if !hasTranscripts(srcDir) {
			// Memory travels with the sessions, here as in restore.
			memory += restoreProjectMemory(root, dest, name, filepath.Join(srcDir, "memory"),
				filepath.Join(projectsDir, targetName, "memory"), manifest[name].Owner(), *dryRun, *update)
			continue
		}
		// The same filing as import, by bucket name and with no path rewriting: a
		// session continued on two machines comes across as a second session here too.
		res, err := restoreInto(root, srcDir, "", "", nil, restoreOpts{
			DryRun: *dryRun, Update: *update, Bucket: targetName,
			Owner: manifest[name].Owner(), OwnerOf: writers, Forks: filepath.Join(dest, "forks", name), Archive: dest,
			FormerNames: formerNames(dest),
		})
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		pulled += res.Copied
		updated += res.Updated
		already += res.Skipped
		memory += res.Memory
		forked = append(forked, res.Diverged...)
		failed = append(failed, res.Failed...)
	}

	profile := restoreProfile(root, dest, *dryRun, *update, !*noPlugins)
	// Work folders at their recorded paths, under the same safety rules as import.
	var workRes workRestoreResult
	if cfg, err := archive.LoadConfig(root); err == nil {
		shards, _, _ := archive.ReadShards(dest)
		_, workRes = restoreWork(root, dest, shards, cfg, *dryRun, *update, true)
	}

	fmt.Printf("\n%s %d transcript(s) and %d memory file(s), %d already present.\n",
		verbFor(*dryRun), pulled, memory, already)
	for _, n := range workRes.Notes {
		fmt.Println(n)
	}
	if updated > 0 {
		fmt.Printf("updated %d session(s) that continued on another machine\n", updated)
	}
	for _, f := range failed {
		fmt.Printf("  x %s\n", f)
	}
	printDiverged(forked)
	printProfile(profile, *dryRun)
	fmt.Println("pull files by bucket name only. If a project lives at a different path here,")
	fmt.Println("use `claude-sessions import`, which matches by identity and rewrites paths.")
	return nil
}

// formerNames maps each machine, lower-cased, to the names it went by before, as its
// shard records them.
func formerNames(dest string) map[string][]string {
	shards, _, _ := archive.ReadShards(dest)
	out := map[string][]string{}
	for _, s := range shards {
		if len(s.FormerNames) > 0 {
			out[strings.ToLower(s.Machine)] = s.FormerNames
		}
	}
	return out
}

// sessionWriters answers which machine wrote an archived session: the one whose index
// records a local copy of the same size and time. On a shared layout that is often not
// the machine that last pushed the project, which is usually the one importing.
func sessionWriters(dest string) func(bucket, id string, info os.FileInfo) string {
	rows, _ := archive.AllSessions(dest)
	byKey := map[string][]archive.Session{}
	for _, s := range rows {
		k := strings.ToLower(s.Bucket + "/" + s.ID)
		byKey[k] = append(byKey[k], s)
	}
	return func(bucket, id string, info os.FileInfo) string {
		rows := byKey[strings.ToLower(bucket+"/"+id)]
		for _, s := range rows {
			// Never this machine: a copy that differs from what is here is not its own.
			if !isMe(s.Machine) && s.Size == info.Size() && withinTolerance(s.Updated, info.ModTime()) {
				return s.Machine
			}
		}
		// The writer's index can lag its copy: the per-reply hook archives a session
		// without touching the index, and a machine that then died never catches up.
		// When only one other machine has ever recorded the session, it is that one's.
		other := ""
		for _, s := range rows {
			switch {
			case isMe(s.Machine):
			case other == "":
				other = s.Machine
			case !strings.EqualFold(other, s.Machine):
				return "" // two candidates: not a guess worth making
			}
		}
		return other
	}
}

// mappedMissing is a project the user mapped to a folder that is no longer there.
const mappedMissing identity.How = "mapped folder missing"

// mapHint is the command that places a project import could not.
func mapHint(bucket string) string {
	return fmt.Sprintf("fix: claude-sessions config map %s '<local folder>'", bucket)
}

func samePath(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(a, `\/`), strings.TrimRight(b, `\/`))
}
