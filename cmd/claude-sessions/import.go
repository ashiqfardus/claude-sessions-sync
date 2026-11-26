package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/convzip"
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
	Machine  string `json:"machine,omitempty"` // the computer the project or work folder is from
	Kind     string `json:"kind,omitempty"`    // "work" for a work folder, "" for a project
}

// cmdImport is the reason this project exists: it files an archive's sessions onto
// this machine even when the projects live at different paths here.
func cmdImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	archiveDir := fs.String("archive", "", "override the synced destination folder")
	roots := fs.String("search-root", "", "comma-separated folders to scan for projects (default: parents of known projects, plus your home)")
	depth := fs.Int("depth", 0, "how deep to scan under each search root (default 3)")
	dryRun := fs.Bool("dry-run", false, "report what would happen without writing")
	bareName := fs.Bool("include-bare-name", false, "also replace the bare folder name inside transcripts")
	update := fs.Bool("update", false, updateHelp)
	updateProfile := fs.Bool("update-profile", false, updateProfileHelp)
	noPlugins := fs.Bool("no-plugins", false, noPluginsHelp)
	noProfile := fs.Bool("no-profile", false, noProfileHelp)
	asJSON := fs.Bool("json", false, "machine-readable output")
	profileOnly := fs.Bool("profile-only", false, "bring in only the newer rules and skills: no conversations, memory or work folders, no plugins")
	noWork := fs.Bool("no-work", false, noWorkHelp)
	repoint := fs.Bool("repoint", false, "also rewrite conversations already here that name other machines' work folders (originals are backed up; any used in the last hour is skipped)")
	if err := parseFlags(fs, args, false); err != nil {
		return err
	}
	o := importOpts{ClaudeDir: *claudeDir, ArchiveDir: *archiveDir, SearchRoot: *roots, Depth: *depth,
		DryRun: *dryRun, BareName: *bareName, Update: *update, UpdateProfile: *updateProfile,
		NoPlugins: *noPlugins, NoWork: *noWork, Repoint: *repoint, ProfileOnly: *profileOnly, NoProfile: *noProfile}
	s := stdStreams()
	if *asJSON {
		s = jsonStreams()
	}
	res, err := runImport(s, o)
	if err != nil || !*asJSON {
		return err
	}
	if *profileOnly {
		return encodeJSON(os.Stdout, res.Profile)
	}
	return encodeJSON(os.Stdout, res.Rows)
}

type importOpts struct {
	ClaudeDir, ArchiveDir, SearchRoot                                   string
	Depth                                                               int
	DryRun, BareName, Update, UpdateProfile, NoPlugins, NoWork, Repoint bool
	ProfileOnly, NoProfile                                              bool
}

type importResult struct {
	Rows    []importRow   `json:"rows"`
	Profile profileResult `json:"profile"`
}

// runImport files the archive's projects and work folders here, reports on s as it
// goes, and returns a row for each.
func runImport(s streams, o importOpts) (importResult, error) {
	if o.ProfileOnly {
		for _, c := range []struct {
			set  bool
			name string
		}{{o.Update, "update"}, {o.Repoint, "repoint"}, {o.NoWork, "no-work"}, {o.SearchRoot != "", "search-root"},
			{o.Depth != 0, "depth"}, {o.BareName, "include-bare-name"}, {o.NoProfile, "no-profile"}} {
			if c.set {
				return importResult{}, fmt.Errorf("--profile-only brings in rules and skills only; it cannot be combined with --%s", c.name)
			}
		}
	}
	if o.Depth == 0 { // unset
		o.Depth = 3
	}
	if o.NoProfile && o.UpdateProfile {
		return importResult{}, fmt.Errorf("--no-profile and --update-profile cannot be combined: --update-profile replaces rule and skill files that --no-profile leaves alone")
	}
	if o.NoWork && o.Repoint {
		return importResult{}, fmt.Errorf("--no-work and --repoint cannot be combined: --repoint rewrites conversations to work folders that --no-work does not restore")
	}

	root, _, err := resolveRoot(o.ClaudeDir)
	if err != nil {
		return importResult{}, err
	}
	dest, src, err := archive.ResolveDestination(root, o.ArchiveDir)
	if err != nil {
		return importResult{}, err
	}
	if dest == "" {
		return importResult{}, fmt.Errorf("no archive destination: run `claude-sessions config set-destination <folder>`")
	}
	if st, err := os.Stat(dest); err != nil || !st.IsDir() {
		return importResult{}, fmt.Errorf("%s is not reachable - is the sync client mounted?", dest)
	}

	if o.ProfileOnly {
		lock, err := archive.AcquireLock(root)
		if err != nil {
			return importResult{}, err
		}
		defer lock.Release()
		p := restoreProfile(root, dest, o.DryRun, true, false, true)
		p.Files, p.Replaced, p.Plugins = nonNil(p.Files), nonNil(p.Replaced), nonNil(p.Plugins)
		if len(p.Files) == 0 {
			fmt.Fprintln(s.Out, "No rules or skills here are older than the archive's.")
		}
		printProfile(s.Out, p, o.DryRun)
		return importResult{Rows: []importRow{}, Profile: p}, nil
	}

	slowRead := hintIfSlowTo(s.Err)
	manifest, bad, err := archive.Merged(dest)
	slowRead()
	if err != nil {
		return importResult{}, err
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
		return importResult{}, fmt.Errorf("nothing to import: %s holds no sessions - run `claude-sessions push` on a machine that has them first", dest)
	}

	// Writing into ~/.claude/projects while a scheduled push reads it is what the lock
	// exists to prevent.
	lock, err := archive.AcquireLock(root)
	if err != nil {
		return importResult{}, err
	}
	defer lock.Release()

	searchRoots := defaultSearchRoots(manifest, o.SearchRoot)
	// One walk of the search roots answers every project, instead of one walk each.
	scanner := identity.NewScanner(searchRoots, o.Depth)

	fmt.Fprintf(s.Out, "archive      %s (%s)\n", dest, src)
	fmt.Fprintf(s.Out, "search roots %s\n\n", strings.Join(searchRoots, ", "))
	for _, r := range searchRoots {
		// A typo here otherwise surfaces only as every project being "not found".
		if st, err := os.Stat(r); err != nil || !st.IsDir() {
			fmt.Fprintf(s.Out, "! search root %s does not exist\n", r)
		}
	}
	for _, b := range bad {
		fmt.Fprintf(s.Out, "! ignoring manifest file %s\n", b)
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
	// --no-work leaves all of this empty: no placement, no moves, no held record.
	var workMoves []rewrite.Move
	var workRes workRestoreResult
	var remapNotes []string
	if !o.NoWork {
		workMoves, workRes = restoreWork(root, dest, shards, wcfg, o.DryRun, o.Update, false)
		// A folder placed elsewhere since an earlier import repoints from where it was, until
		// a --repoint has moved every conversation off it (spec §7a).
		var remaps []rewrite.Move
		remaps, remapNotes = remapMoves(workRes.Prev, workRes.ByMachine)
		workMoves = append(workMoves, remaps...)
	}
	resolve := func(bucket string, p archive.Project) identity.Match {
		if local, ok := cfg.Mapped(bucket); ok {
			if st, err := os.Stat(local); err != nil || !st.IsDir() {
				return identity.Match{How: mappedMissing, Candidates: []string{local}}
			}
			return identity.Match{How: identity.ByManual, Path: local}
		}
		m := identity.Resolve(p.Path, p.Leaf, p.Remote, scanner)
		if m.How == identity.ByRecordedPath || m.How == identity.ByGitRemote {
			return m
		}
		// A fallback: a repository found by its remote is never work-folder content,
		// however far up a placed folder reaches. And only a folder that is here once
		// the work folders are placed: a container (~/Developer, a few loose files) does
		// not bring the repositories in it, and a bucket for one not here would be
		// pushed back with no identity.
		// shortcut: a dry run places nothing, so a project only the work folder would
		// bring reads as not found there; upgrade if dry runs must match exactly.
		if t := placedPath(workRes.Placed, p.Path); t != "" {
			if st, err := os.Stat(t); err == nil && st.IsDir() {
				return identity.Match{How: byWorkFolder, Path: t}
			}
		}
		if localHome == "" {
			return m
		}
		for _, h := range homes {
			if samePath(p.Path, h) {
				return identity.Match{How: identity.ByHomeFolder, Path: localHome}
			}
			// So are its Desktop, Documents and Downloads: Windows has one per profile,
			// and a name match among them is ambiguous.
			if ps := pathSegs(p.Path); len(ps) == len(pathSegs(h))+1 && work.Within(p.Path, h, true) {
				for _, name := range []string{"Desktop", "Documents", "Downloads"} {
					if !strings.EqualFold(ps[len(ps)-1], name) {
						continue
					}
					local := filepath.Join(localHome, name)
					if st, err := os.Stat(local); err == nil && st.IsDir() {
						return identity.Match{How: identity.ByHomeFolder, Path: local}
					}
				}
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
	imported := loadImported(root, !o.DryRun)

	var rows []importRow
	var filed, updated, sameLayout, notFound, ambiguous int

	report := func(row importRow, marker string) {
		rows = append(rows, row)
		fmt.Fprintf(s.Out, "%s %s: %s\n", marker, row.Bucket, row.Note)
	}

	for _, bucket := range buckets {
		// Named like a file kept beside a project - imported.log - its memory base would
		// take that file's path.
		if !safeBucketName(bucket) {
			report(importRow{Bucket: printable(bucket), Note: notBucket, Machine: manifest[bucket].Owner()}, "!")
			continue
		}
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
			row := importRow{Bucket: bucket, Recorded: p.Path, How: string(match.How), Machine: p.Owner()}
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
				filepath.Join(claude.ProjectsDir(root), local, "memory"), p.Owner(), o.DryRun, o.Update)
			row.Local = match.Path
			row.Note = fmt.Sprintf("memory only -> %s (matched by %s): %d memory file(s) filed", match.Path, match.How, n)
			report(row, "+")
			continue
		}

		match := resolve(bucket, p)
		row := importRow{Bucket: bucket, Recorded: p.Path, How: string(match.How), Machine: p.Owner()}

		switch match.How {
		case mappedMissing:
			row.Note = fmt.Sprintf("mapped to %s, which does not exist; %s", match.Candidates[0], mapHint(bucket))
			notFound++
			rows = append(rows, row)
			fmt.Fprintf(s.Out, "- %s: %s\n", bucket, row.Note)
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
			fmt.Fprintf(s.Out, "- %s: %s\n", bucket, row.Note)
			for _, c := range match.Candidates {
				fmt.Fprintf(s.Out, "      %s (different remote)\n", c)
			}
			fmt.Fprintf(s.Out, "      %s\n", mapHint(bucket))
			continue

		case identity.Ambiguous:
			row.Note = fmt.Sprintf("%d folders named %q and no git remote settles it", len(match.Candidates), p.Leaf)
			ambiguous++
			rows = append(rows, row)
			fmt.Fprintf(s.Out, "! %s: %s\n", bucket, row.Note)
			for _, c := range match.Candidates {
				fmt.Fprintf(s.Out, "      %s\n", c)
			}
			fmt.Fprintf(s.Out, "      %s\n", mapHint(bucket))
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
			restoreOpts{BareName: o.BareName, DryRun: o.DryRun, Update: o.Update,
				Owner: p.Owner(), OwnerOf: writers, Forks: filepath.Join(dest, "forks", bucket), Archive: dest, FormerNames: former,
				Imported: imported})
		if errors.Is(err, errRecord) {
			return importResult{}, err // never file what cannot be recorded
		}
		if err != nil {
			row.Note = err.Error()
			rows = append(rows, row)
			fmt.Fprintf(s.Out, "x %s: %s\n", bucket, err)
			continue
		}
		row.Filed, row.Skipped, row.Updated = res.Copied, res.Skipped, res.Updated
		row.Note = resultNote(res)
		filed += res.Copied
		updated += res.Updated
		if match.How == identity.ByRecordedPath {
			sameLayout++
		}
		rows = append(rows, row)

		marker := "+"
		if match.How == identity.ByRecordedPath {
			marker = "="
		}
		fmt.Fprintf(s.Out, "%s %s -> %s (matched by %s): %d filed, %d already present",
			marker, bucket, match.Path, match.How, res.Copied, res.Skipped)
		if res.Updated > 0 {
			fmt.Fprintf(s.Out, ", %d updated", res.Updated)
		}
		fmt.Fprintln(s.Out)
		for _, f := range res.Failed {
			fmt.Fprintf(s.Out, "    x %s\n", f)
		}
		for _, n := range res.Notes {
			fmt.Fprintf(s.Out, "    ! %s\n", n)
		}
		for _, d := range res.Diverged {
			fmt.Fprintf(s.Out, "    ! continued here and elsewhere - %s (resume either)\n", d)
		}
	}
	rows = append(rows, workRows(workRes.ByMachine)...)

	// Conversations already here are rewritten only when asked: that edits the user's
	// own files (spec §7a). Work-folder moves only, never project moves.
	var rp repointResult
	if o.Repoint {
		rp = repointLocal(root, workMoves, time.Now(), o.DryRun, imported)
		if rp.Err != nil {
			return importResult{}, rp.Err
		}
		if !o.DryRun && rp.Recent == 0 && rp.Skipped == 0 {
			if err := forgetPrevious(root, workRes.ByMachine); err != nil {
				rp.Notes = append(rp.Notes, fmt.Sprintf("  x restored work record: %v", err))
			}
		}
	}
	imported.finish(s.Err)

	profile := restoreProfile(root, dest, o.DryRun, o.UpdateProfile, !o.NoPlugins, !o.NoProfile)
	// A caller that encodes the result gets [] for an empty list, never null.
	profile.Files, profile.Replaced, profile.Plugins = nonNil(profile.Files), nonNil(profile.Replaced), nonNil(profile.Plugins)
	result := importResult{Rows: nonNil(rows), Profile: profile}

	fmt.Fprintf(s.Out, "\n%d transcript(s) filed", filed)
	if o.Update {
		fmt.Fprintf(s.Out, ", %d updated", updated)
	}
	fmt.Fprintf(s.Out, ". %d same layout, %d not found, %d ambiguous.\n", sameLayout, notFound, ambiguous)
	for _, n := range append(workRes.Notes, remapNotes...) {
		fmt.Fprintln(s.Out, printable(n)) // they name files and folders the archive chose
	}
	if o.NoWork {
		fmt.Fprintln(s.Out, noWorkNote)
	}
	if o.NoProfile {
		fmt.Fprintln(s.Out, noProfileNote)
	}
	if len(workRes.Placed) > 0 {
		fmt.Fprintln(s.Out, "  to place them elsewhere: claude-sessions config map-folder '<path from above>' '<local folder>', or claude-sessions config work-placement home|machine")
	}
	if o.Repoint {
		verb := "repointed"
		if o.DryRun {
			verb = "would repoint"
		}
		fmt.Fprintf(s.Out, "%s %d conversation(s) to this machine's work folders", verb, rp.Rewritten)
		if rp.Backup != "" {
			fmt.Fprintf(s.Out, "; the originals are in %s", rp.Backup)
		}
		fmt.Fprintln(s.Out)
		if rp.Recent > 0 {
			fmt.Fprintf(s.Out, "%d conversation(s) used in the last hour left alone: run import --repoint again later\n", rp.Recent)
		}
		if rp.Skipped > 0 {
			fmt.Fprintf(s.Out, "%d conversation(s) left as they were after an error:\n", rp.Skipped)
		}
		for _, n := range rp.Notes {
			fmt.Fprintln(s.Out, n)
		}
	} else if len(workMoves) > 0 && s.Out != io.Discard { // it reads every conversation here, for a line of text alone
		if n := countPointing(root, workMoves); n > 0 {
			fmt.Fprintf(s.Out, "%d conversation(s) here still point at other machines' work folders: run claude-sessions import --repoint\n", n)
		}
	}
	printProfile(s.Out, profile, o.DryRun)
	if !o.Update && sameLayout+filed > 0 {
		fmt.Fprintln(s.Out, "Sessions already here are never replaced; --update refreshes ones that continued elsewhere.")
	}
	if notFound+ambiguous > 0 {
		fmt.Fprintln(s.Out, "\nFor anything not placed, say where it lives here once, then run import again:")
		fmt.Fprintln(s.Out, "  claude-sessions config map <bucket> '<local folder>'")
	}
	return result, nil
}

// nonNil is l, or an empty list for nil: JSON encodes it as [], not null.
func nonNil[T any](l []T) []T {
	if l == nil {
		return []T{}
	}
	return l
}

// resultNote sums up, for a row, what restoreInto could not file in whole.
func resultNote(res restoreResult) string {
	note := ""
	if len(res.Failed) > 0 {
		note = fmt.Sprintf("%d transcript(s) not filed: %s", len(res.Failed), strings.Join(res.Failed, "; "))
	}
	if len(res.Diverged) > 0 {
		note = strings.TrimSpace(note + fmt.Sprintf(" %d session(s) continued here and elsewhere, local kept and the other filed beside it: %s",
			len(res.Diverged), strings.Join(res.Diverged, "; ")))
	}
	if len(res.Notes) > 0 {
		note = strings.TrimSpace(note + " " + strings.Join(res.Notes, "; "))
	}
	return note
}

// workRows are the work folders placed here, one row per computer and folder, by
// recorded path.
func workRows(byMachine map[string]string) []importRow {
	var rows []importRow
	for k, target := range byMachine {
		m, path, _ := strings.Cut(k, "|")
		rows = append(rows, importRow{Kind: "work", Machine: m, Recorded: path, Local: target})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Recorded != rows[j].Recorded {
			return rows[i].Recorded < rows[j].Recorded
		}
		return rows[i].Machine < rows[j].Machine
	})
	return rows
}

// newestCwd is the working directory recorded by the newest transcript in an archived
// bucket, or "" when there is none. A zipped one is dated by its entry, from the zip's
// index, and read from a temp extract only when it is the newest.
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
			newest, at = filepath.Join(dir, e.Name()), info.ModTime()
		}
	}
	zipped := ""
	for _, zp := range convzip.Find(dir) {
		id := strings.TrimSuffix(filepath.Base(zp), convzip.Suffix)
		if ents, err := convzip.Entries(zp); err == nil && ents[id+".jsonl"].ModTime.After(at) {
			zipped, at = id, ents[id+".jsonl"].ModTime
		}
	}
	if zipped != "" {
		if p, done, err := materialize(dir, zipped, ""); err == nil {
			defer done()
			newest = p // else the newest loose one
		}
	}
	if newest == "" {
		return ""
	}
	s, _ := claude.ScanHead(newest, headScanLines)
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
	return len(convzip.Find(dir)) > 0
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
	updateProfile := fs.Bool("update-profile", false, updateProfileHelp)
	noPlugins := fs.Bool("no-plugins", false, noPluginsHelp)
	noWork := fs.Bool("no-work", false, noWorkHelp)
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := parseFlags(fs, args, false); err != nil {
		return err
	}
	o := pullOpts{ClaudeDir: *claudeDir, ArchiveDir: *archiveDir, DryRun: *dryRun, Update: *update,
		UpdateProfile: *updateProfile, NoPlugins: *noPlugins, NoWork: *noWork}
	s := stdStreams()
	if *asJSON {
		s = jsonStreams()
	}
	rows, err := runPull(s, o)
	if err != nil || !*asJSON {
		return err
	}
	return encodeJSON(os.Stdout, rows)
}

type pullOpts struct {
	ClaudeDir, ArchiveDir                            string
	DryRun, Update, UpdateProfile, NoPlugins, NoWork bool
}

// runPull files the archive's projects into buckets of the same name, reports on s and
// returns a row for each project and work folder.
func runPull(s streams, o pullOpts) ([]importRow, error) {
	root, _, err := resolveRoot(o.ClaudeDir)
	if err != nil {
		return nil, err
	}
	dest, _, err := archive.ResolveDestination(root, o.ArchiveDir)
	if err != nil {
		return nil, err
	}
	if dest == "" {
		return nil, fmt.Errorf("no archive destination: run `claude-sessions config set-destination <folder>`")
	}

	archivedBuckets, err := archive.BucketNames(dest)
	if err != nil {
		return nil, err
	}
	if len(archivedBuckets) == 0 {
		fmt.Fprintln(s.Out, "Nothing to pull: the archive holds no projects.")
		return []importRow{}, nil
	}

	// Pulling writes into ~/.claude/projects, which a scheduled push reads.
	lock, err := archive.AcquireLock(root)
	if err != nil {
		return nil, err
	}
	defer lock.Release()

	projectsDir := claude.ProjectsDir(root)
	manifest, _, _ := archive.Merged(dest)
	writers := sessionWriters(dest)
	imported := loadImported(root, !o.DryRun)
	var pulled, updated, already, memory int
	var forked, failed, notes []string
	var rows []importRow

	for _, name := range archivedBuckets {
		if !safeBucketName(name) { // as in import
			fmt.Fprintf(s.Out, "! %s: %s\n", printable(name), notBucket)
			rows = append(rows, importRow{Bucket: printable(name), Note: notBucket, Machine: manifest[name].Owner()})
			continue
		}
		srcDir := filepath.Join(archive.ProjectsDir(dest), name)
		row := importRow{Bucket: name, Recorded: manifest[name].Path, How: string(byBucketName), Machine: manifest[name].Owner()}

		// Match an existing local bucket case-insensitively before creating one.
		targetName := name
		if existing, ok := claude.FindBucket(projectsDir, name); ok {
			targetName = existing.Name
		}

		if !hasTranscripts(srcDir) {
			// Memory travels with the sessions, here as in restore.
			n := restoreProjectMemory(root, dest, name, filepath.Join(srcDir, "memory"),
				filepath.Join(projectsDir, targetName, "memory"), manifest[name].Owner(), o.DryRun, o.Update)
			memory += n
			if n > 0 {
				row.Note = fmt.Sprintf("memory only: %d memory file(s) filed", n)
				rows = append(rows, row)
			}
			continue
		}
		// The same filing as import, by bucket name and with no path rewriting: a
		// session continued on two machines comes across as a second session here too.
		res, err := restoreInto(root, srcDir, "", "", nil, restoreOpts{
			DryRun: o.DryRun, Update: o.Update, Bucket: targetName,
			Owner: manifest[name].Owner(), OwnerOf: writers, Forks: filepath.Join(dest, "forks", name), Archive: dest,
			FormerNames: formerNames(dest), Imported: imported,
		})
		if errors.Is(err, errRecord) {
			return nil, err // never file what cannot be recorded
		}
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", name, err))
			row.Note = err.Error()
			rows = append(rows, row)
			continue
		}
		pulled += res.Copied
		updated += res.Updated
		already += res.Skipped
		memory += res.Memory
		forked = append(forked, res.Diverged...)
		failed = append(failed, res.Failed...)
		notes = append(notes, res.Notes...)
		row.Filed, row.Skipped, row.Updated, row.Note = res.Copied, res.Skipped, res.Updated, resultNote(res)
		rows = append(rows, row)
	}
	imported.finish(s.Err)

	profile := restoreProfile(root, dest, o.DryRun, o.UpdateProfile, !o.NoPlugins, true)
	// Work folders at their recorded paths, under the same safety rules as import.
	var workRes workRestoreResult
	if cfg, err := archive.LoadConfig(root); err == nil && !o.NoWork {
		shards, _, _ := archive.ReadShards(dest)
		_, workRes = restoreWork(root, dest, shards, cfg, o.DryRun, o.Update, true)
	}
	rows = append(rows, workRows(workRes.ByMachine)...)

	fmt.Fprintf(s.Out, "\n%s %d transcript(s) and %d memory file(s), %d already present.\n",
		verbFor(o.DryRun), pulled, memory, already)
	if o.NoWork {
		fmt.Fprintln(s.Out, noWorkNote)
	}
	for _, n := range workRes.Notes {
		fmt.Fprintln(s.Out, printable(n)) // they name files and folders the archive chose
	}
	if updated > 0 {
		fmt.Fprintf(s.Out, "updated %d session(s) that continued on another machine\n", updated)
	}
	for _, f := range failed {
		fmt.Fprintf(s.Out, "  x %s\n", f)
	}
	for _, n := range notes {
		fmt.Fprintf(s.Out, "  ! %s\n", n)
	}
	printDiverged(s.Out, forked)
	printProfile(s.Out, profile, o.DryRun)
	fmt.Fprintln(s.Out, "pull files by bucket name only. If a project lives at a different path here,")
	fmt.Fprintln(s.Out, "use `claude-sessions import`, which matches by identity and rewrites paths.")
	return nonNil(rows), nil
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

// safeBucketName reports whether name, an archive folder, may be filed as a bucket.
// Each file this tool keeps beside a bucket's memory base in session-sync-base -
// imported.json, imported.log, work-held.json - has a dot, and a bucket named like one
// would take its path; claude.Slug never makes one.
func safeBucketName(name string) bool {
	return name != "" && !strings.ContainsAny(name, `./\`)
}

// notBucket is why an archive folder is not filed.
const notBucket = "a dot or a path separator in the name, as in this tool's own files: not a project folder; skipped"

// mappedMissing is a project the user mapped to a folder that is no longer there.
const mappedMissing identity.How = "mapped folder missing"

// byBucketName is pull's match: the archive's folder name, with no identity check.
const byBucketName identity.How = "bucket name"

// mapHint is the command that places a project import could not.
func mapHint(bucket string) string {
	return fmt.Sprintf("fix: claude-sessions config map %s '<local folder>'", bucket)
}

// samePath compares two paths of any computer's, so either slash, as Windows takes them.
func samePath(a, b string) bool {
	slash := func(p string) string { return strings.TrimRight(strings.ReplaceAll(p, `\`, "/"), "/") }
	return strings.EqualFold(slash(a), slash(b))
}
