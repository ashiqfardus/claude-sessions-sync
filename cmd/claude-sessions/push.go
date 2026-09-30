package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/identity"
	"github.com/ashiqfardus/claude-sessions-sync/internal/render"
)

type pushResult struct {
	Copied    int      `json:"copied"`
	Unchanged int      `json:"unchanged"`
	Sessions  int      `json:"sessions"`
	Projects  int      `json:"projects"`
	Skipped   []string `json:"skipped,omitempty"`
	Archive   string   `json:"archive"`
	DryRun    bool     `json:"dryRun,omitempty"`
}

func cmdPush(args []string) error {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	archiveDir := fs.String("archive", "", "override the synced destination folder")
	quiet := fs.Bool("quiet", false, "hook mode: log instead of printing, and never fail")
	dryRun := fs.Bool("dry-run", false, "report what would be copied without writing")
	asJSON := fs.Bool("json", false, "machine-readable output")
	noHTML := fs.Bool("no-html", false, "skip rendering the mobile HTML pages")
	lockWait := fs.Duration("lock-wait", 45*time.Second, "with --quiet, how long to wait for another run to finish before skipping")
	if err := parseFlags(fs, args, false); err != nil {
		// A hook written by a newer version may pass a flag this binary does not know.
		// That must not break the end of the session any more than a missing drive -
		// but it must still leave a trace, or a downgrade silently archives nothing.
		for _, a := range args {
			if a == "--quiet" || a == "-quiet" || a == "--quiet=true" || a == "-quiet=true" {
				if root, _, rerr := resolveRoot(*claudeDir); rerr == nil {
					newLogger(root, true)("SKIP: %v (a newer version's hook? re-run `claude-sessions install`)", err)
				}
				return nil
			}
		}
		return err
	}

	err := runPush(*claudeDir, *archiveDir, pushOpts{
		Quiet: *quiet, DryRun: *dryRun, JSON: *asJSON, NoHTML: *noHTML, LockWait: *lockWait,
	})

	// A SessionEnd hook that fails breaks the end of the user's session. Missing
	// drive, offline mount, unexpected error: log it and exit 0. This is the single
	// most important behaviour in the whole command.
	if *quiet && err != nil {
		return nil
	}
	return err
}

type pushOpts struct {
	Quiet, DryRun, JSON, NoHTML bool
	LockWait                    time.Duration // --quiet only
}

// quietRenderBudget bounds page rendering in an unattended push.
//
// The SessionEnd hook is killed after 120 seconds, and a first render of a large
// archive - or every page again after a template change - takes longer than that. A
// run killed mid-render used to keep none of its work, so every session end started
// over and never finished. Pages left over are rendered by the next run.
const quietRenderBudget = 45 * time.Second

// quietWatchdog ends an unattended push that has hung, just inside the one-hour
// limit Task Scheduler and the systemd unit impose, so it logs why.
const quietWatchdog = 55 * time.Minute

func runPush(claudeDir, archiveDir string, opts pushOpts) (err error) {
	quiet, dryRun, asJSON, noHTML := opts.Quiet, opts.DryRun, opts.JSON, opts.NoHTML
	root, _, err := resolveRoot(claudeDir)
	if err != nil {
		return err
	}

	logf := newLogger(root, quiet)

	// An unattended push that hangs - a dead NFS, SMB or rclone mount blocks forever
	// in a read - ends itself. launchd has no time limit at all, and the lock it holds
	// would otherwise keep every later run waiting until the heartbeat cap.
	if quiet {
		watchdog := time.AfterFunc(quietWatchdog, func() {
			logf("ABORT: still running after %s, probably blocked on the archive's filesystem; exiting so the next run can start", quietWatchdog)
			os.Exit(0) // exit 0: this is a hook, and must never fail the session
		})
		defer watchdog.Stop()
	}

	// Even a panic must not take the session down with it.
	defer func() {
		if r := recover(); r != nil {
			logf("PANIC: %v", r)
			err = fmt.Errorf("push panicked: %v", r)
		}
	}()

	dest, src, err := archive.ResolveDestination(root, archiveDir)
	if err != nil {
		logf("ERROR: %v", err)
		return err
	}
	if dest == "" {
		logf("SKIP: no destination configured")
		return fmt.Errorf("no archive destination: run `claude-sessions config set-destination <folder>`")
	}

	// The destination's parent must already exist. Creating it would mean silently
	// making a plain local folder where an unmounted cloud drive should be, and
	// reporting success - the exact failure that makes someone trust a backup that
	// is not happening.
	if st, statErr := os.Stat(filepath.Dir(dest)); statErr != nil || !st.IsDir() {
		logf("SKIP: destination parent not available: %s", filepath.Dir(dest))
		return fmt.Errorf("%s is not available - is the sync client mounted?", filepath.Dir(dest))
	}

	if err := archive.ValidateDestination(root, dest); err != nil {
		logf("SKIP: %v", err)
		return err
	}

	// Unattended runs wait for one another: the sweep and a hook, or two sessions
	// ending together, are routine. Someone at a terminal is told straight away.
	wait := time.Duration(0)
	if quiet {
		wait = opts.LockWait
	}
	lock, err := archive.AcquireLockWait(root, wait)
	if err != nil {
		logf("SKIP: %v", err)
		if quiet {
			return nil
		}
		return err
	}
	defer lock.Release()

	if !dryRun {
		if err := os.MkdirAll(archive.ProjectsDir(dest), 0o755); err != nil {
			logf("ERROR: %v", err)
			return err
		}
		if n := archive.RemoveOrphanTemps(dest); n > 0 {
			logf("CLEAN: removed %d temporary file(s) left by an interrupted run", n)
		}
	}

	buckets, err := claude.ListBuckets(claude.ProjectsDir(root))
	if err != nil {
		logf("ERROR: %v", err)
		return err
	}

	statePaths, _ := claude.ProjectPaths(claude.StatePath(root))
	excludes := archive.RenderExcludes(root, dest, nil, !dryRun)

	res := pushResult{Archive: dest, DryRun: dryRun}
	shard := archive.Shard{
		SchemaVersion: archive.SchemaVersion,
		Machine:       machineName(),
		Projects:      map[string]archive.Project{},
	}
	var sessions []archive.Session

	// Related repositories are carried forward from this machine's last shard and only
	// looked for again in transcripts that changed; the first push after upgrading
	// scans everything once.
	prevShard := readShard(dest, shard.Machine)
	scanAll := !prevShard.RelatedIndexed
	shard.RelatedIndexed = true

	for _, b := range buckets {
		destBucket := filepath.Join(archive.ProjectsDir(dest), b.Name)

		// Each transcript is read once. The identity below reuses what the newest
		// one yields rather than opening it a second time.
		var newestCwd string
		var newestAt time.Time
		var changed []string

		for _, t := range b.Transcripts {
			target := filepath.Join(destBucket, filepath.Base(t.Path))
			srcInfo, statErr := os.Stat(t.Path)
			if statErr != nil {
				res.Skipped = append(res.Skipped, t.ID)
				continue
			}
			dstInfo := archive.StatOrNil(target)
			if archive.NeedsCopy(srcInfo, dstInfo) && dstInfo != nil && srcInfo.Size() < dstInfo.Size() {
				// Transcripts only grow. A newer but shorter local file is a session
				// that forked on two machines, or a truncated copy; either way the
				// archived conversation is not thrown away for it.
				logf("WARN: %s: local copy is shorter than the archived one (%d < %d bytes); archive kept",
					t.ID, srcInfo.Size(), dstInfo.Size())
				res.Skipped = append(res.Skipped, t.ID)
			} else if archive.NeedsCopy(srcInfo, dstInfo) {
				if !dryRun {
					if err := archive.CopyFile(t.Path, target); err != nil {
						// One unreadable transcript must not abandon the rest.
						logf("WARN: %s: %v", t.ID, err)
						res.Skipped = append(res.Skipped, t.ID)
						continue
					}
				}
				res.Copied++
				changed = append(changed, t.Path)
			} else {
				res.Unchanged++
			}
			res.Sessions++

			for _, rel := range t.Extras {
				srcPath := filepath.Join(b.Dir, t.ID, rel)
				srcInfo, statErr := os.Stat(srcPath)
				if statErr != nil {
					continue
				}
				extraTarget := filepath.Join(destBucket, t.ID, rel)
				if !archive.NeedsCopy(srcInfo, archive.StatOrNil(extraTarget)) {
					res.Unchanged++
					continue
				}
				if !dryRun {
					if err := archive.CopyFile(srcPath, extraTarget); err != nil {
						logf("WARN: %s/%s: %v", t.ID, rel, err)
						continue
					}
				}
				res.Copied++
			}

			summary, _ := claude.ScanHead(t.Path, headScanLines)
			sessions = append(sessions, archive.Session{
				Bucket: b.Name, Project: summary.Cwd, ID: t.ID,
				Updated: t.ModTime, Size: t.Size, Prompt: indexPrompt(summary.FirstPrompt, summary.Cwd, b.Name, excludes),
			})
			if summary.Cwd != "" && t.ModTime.After(newestAt) {
				newestAt = t.ModTime
				newestCwd = summary.Cwd
			}
		}

		// Memory travels with the sessions; it is small and equally unrecoverable.
		for _, name := range b.Memory {
			srcPath := filepath.Join(b.Dir, "memory", name)
			target := filepath.Join(destBucket, "memory", name)
			srcInfo, statErr := os.Stat(srcPath)
			if statErr != nil {
				continue
			}
			if archive.NeedsCopy(srcInfo, archive.StatOrNil(target)) {
				if !dryRun {
					if err := archive.CopyFile(srcPath, target); err != nil {
						logf("WARN: memory/%s: %v", name, err)
						continue
					}
				}
				res.Copied++
			} else {
				res.Unchanged++
			}
		}

		if entry, ok := projectIdentity(b, newestCwd, statePaths); ok {
			scan := changed
			if scanAll {
				scan = nil
				for _, t := range b.Transcripts {
					scan = append(scan, t.Path)
				}
			}
			entry.Related = relatedRepos(entry.Path, prevShard.Projects[b.Name].Related, scan)
			shard.Projects[b.Name] = entry
			res.Projects++
		}
	}

	if !dryRun {
		sessions = keepArchivedHistory(dest, &shard, sessions)
		if err := writeManifest(dest, shard); err != nil {
			logf("ERROR: manifest: %v", err)
			return err
		}
		if err := archive.WriteSessionIndex(dest, shard.Machine, sessions); err != nil {
			logf("ERROR: index cache: %v", err)
			return err
		}
		// Built from every machine, not just this one - see writeIndex.
		if err := writeIndex(dest, excludes); err != nil {
			logf("ERROR: index: %v", err)
			return err
		}
	}

	logf("PUSH: %d copied, %d unchanged, %d session(s), %d project(s) -> %s",
		res.Copied, res.Unchanged, res.Sessions, res.Projects, dest)

	// Readable pages for phones and tablets. Best-effort by design: a rendering
	// problem must never fail a sync that has already succeeded, so this is wrapped
	// and only ever logged.
	if !dryRun && !noHTML {
		ropts := render.Options{Exclude: excludes, LocalProjects: claude.ProjectsDir(root)}
		if quiet {
			ropts.Budget = quietRenderBudget
		}
		if r, err := render.Archive(dest, ropts); err != nil {
			logf("HTML step failed (transcripts are still archived): %v", err)
		} else if r.Deferred > 0 {
			logf("HTML: %d rendered, %d up to date, %d left for the next run", r.Rendered, r.UpToDate, r.Deferred)
		} else {
			logf("HTML: %d rendered, %d up to date", r.Rendered, r.UpToDate)
		}
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	if !quiet {
		verb := "copied"
		if dryRun {
			verb = "would copy"
		}
		fmt.Printf("\n%s %d file(s), %d unchanged.\n", verb, res.Copied, res.Unchanged)
		fmt.Printf("%d session(s) across %d project(s) -> %s (%s)\n",
			res.Sessions, res.Projects, dest, src)
		if len(res.Skipped) > 0 {
			fmt.Printf("%d skipped: %s\n", len(res.Skipped), strings.Join(res.Skipped, ", "))
		}
	}
	return nil
}

// projectIdentity recovers what is needed to place this bucket on another machine.
//
// cwdFromNewest is what the caller already read while copying, so this does not open
// the same transcript again.
func projectIdentity(b claude.Bucket, cwdFromNewest string, statePaths []string) (archive.Project, bool) {
	entry := archive.Project{
		OS:       runtime.GOOS,
		Sessions: len(b.Transcripts),
	}

	// How fresh this machine's knowledge of the bucket is - which is what decides a
	// conflict with another machine. Stamping "today" made every machine that pushed
	// today tie, however stale its copy of the project.
	var newest time.Time
	if len(b.Transcripts) > 0 {
		newest = b.Transcripts[0].ModTime // sorted newest first
	}
	for _, name := range b.Memory {
		if info, err := os.Stat(filepath.Join(b.Dir, "memory", name)); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	if !newest.IsZero() {
		entry.Seen = newest.UTC().Format(time.RFC3339)
	}

	if cwdFromNewest != "" {
		entry.Path = cwdFromNewest
		entry.Source = archive.FromTranscript
	}

	// A bucket with memory but no transcript has no cwd to read. Without this
	// fallback it is archived but unroutable - the gap doctor has been reporting.
	if entry.Path == "" {
		if p, ok := claude.MatchBucket(b.Name, statePaths); ok {
			entry.Path = p
			entry.Source = archive.FromClaudeJSON
		}
	}

	if entry.Path != "" {
		entry.Leaf = filepath.Base(entry.Path)
		// Read from .git/config; never execute git in a directory we merely found.
		entry.Remote = identity.Remote(entry.Path)
	}

	// Record it even when unroutable, so it is at least visible rather than absent.
	return entry, entry.Path != "" || len(b.Transcripts) > 0
}

// readShard loads this machine's previous shard from the archive, or an empty one.
func readShard(dest, machine string) archive.Shard {
	var s archive.Shard
	if raw, err := os.ReadFile(filepath.Join(archive.ManifestDir(dest), machine+".json")); err == nil {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

// relatedRepos lists the other git repositories a project's sessions worked in.
//
// A session in a multi-folder workspace starts in one repository and cds into its
// siblings, and import used to repoint only the one it started in - every path into
// the others still named the old machine. Each working directory is reduced to its
// repository root; the project itself, repositories that contain it, and anything
// without a remote are left out, because a remote is the only safe way to find the
// folder again elsewhere.
func relatedRepos(own string, prev []archive.Related, transcripts []string) []archive.Related {
	byKey := map[string]archive.Related{}
	for _, r := range prev {
		byKey[strings.ToLower(r.Path)] = r
	}
	ownRoot := own
	if r := identity.RepoRoot(own); r != "" {
		ownRoot = r
	}
	for _, t := range transcripts {
		cwds, _ := claude.Cwds(t)
		for _, c := range cwds {
			root := identity.RepoRoot(nativePath(c))
			if root == "" || strings.EqualFold(root, ownRoot) || within(ownRoot, root) {
				continue
			}
			key := strings.ToLower(root)
			if _, ok := byKey[key]; ok {
				continue
			}
			if remote := identity.Remote(root); remote != "" {
				byKey[key] = archive.Related{Path: root, Remote: remote}
			}
		}
	}
	out := make([]archive.Related, 0, len(byKey))
	for _, r := range byKey {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// nativePath turns a Git Bash path (/e/work/app), which Claude Code records when a
// session runs under MSYS, into the Windows path it names. Elsewhere it is unchanged.
func nativePath(p string) string {
	if runtime.GOOS != "windows" || len(p) < 2 || p[0] != '/' || !unicode.IsLetter(rune(p[1])) || (len(p) > 2 && p[2] != '/') {
		return p
	}
	return strings.ToUpper(p[1:2]) + `:\` + filepath.FromSlash(strings.TrimPrefix(p[2:], "/"))
}

// within reports whether child is strictly inside parent, ignoring case.
func within(child, parent string) bool {
	sep := string(filepath.Separator)
	c := strings.ToLower(filepath.Clean(child)) + sep
	p := strings.ToLower(filepath.Clean(parent)) + sep
	return len(c) > len(p) && strings.HasPrefix(c, p)
}

// keepArchivedHistory carries this machine's earlier manifest and index entries
// forward for sessions that are gone from local disk but still in the archive.
//
// Both files used to be rebuilt from local disk alone. Claude Code deletes old
// transcripts locally, and the archive is then the only copy - yet its row vanished
// from INDEX.md, and a project whose transcripts had all aged out lost its identity,
// leaving it unroutable on import.
func keepArchivedHistory(dest string, shard *archive.Shard, sessions []archive.Session) []archive.Session {
	if raw, err := os.ReadFile(filepath.Join(archive.ManifestDir(dest), shard.Machine+".json")); err == nil {
		var old archive.Shard
		if json.Unmarshal(raw, &old) == nil {
			for bucket, p := range old.Projects {
				if _, ok := shard.Projects[bucket]; ok || !archive.ValidName(bucket) {
					continue
				}
				if st, err := os.Stat(filepath.Join(archive.ProjectsDir(dest), bucket)); err == nil && st.IsDir() {
					shard.Projects[bucket] = p
				}
			}
		}
	}

	have := map[string]bool{}
	for _, s := range sessions {
		have[s.Bucket+"/"+s.ID] = true
	}
	if raw, err := os.ReadFile(filepath.Join(archive.IndexDir(dest), shard.Machine+".json")); err == nil {
		var old archive.SessionIndex
		if json.Unmarshal(raw, &old) == nil {
			for _, s := range old.Sessions {
				if have[s.Bucket+"/"+s.ID] || !archive.ValidName(s.Bucket) || !archive.ValidName(s.ID) {
					continue
				}
				if _, err := os.Stat(filepath.Join(archive.ProjectsDir(dest), s.Bucket, s.ID+".jsonl")); err == nil {
					sessions = append(sessions, s)
				}
			}
		}
	}
	return sessions
}

func writeManifest(dest string, shard archive.Shard) error {
	data, err := json.MarshalIndent(shard, "", "  ")
	if err != nil {
		return err
	}
	shardPath := filepath.Join(archive.ManifestDir(dest), shard.Machine+".json")
	if err := archive.WriteFileAtomic(shardPath, append(data, '\n'), 0o644); err != nil {
		return err
	}

	// projects.json stays as a read-only compatibility view for the PowerShell tools
	// and for anyone opening the folder by hand. It is regenerated from the shards,
	// never merged in place, so a concurrent push cannot lose another machine's work.
	merged, _, err := archive.Merged(dest)
	if err != nil {
		return err
	}
	legacy, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	return archive.WriteFileAtomic(archive.LegacyManifestPath(dest), append(legacy, '\n'), 0o644)
}

// maxIndexRowsPerProject caps how many sessions each project lists in INDEX.md.
const maxIndexRowsPerProject = 200

// writeIndex regenerates INDEX.md from EVERY machine.
//
// INDEX.md is one file at the archive root. Building it from only the local machine's
// sessions - which is what the first version did - means each push erases every other
// machine's listing, the same defect the sharded manifest exists to avoid. Rows are
// cached in the shards, so this costs no reads across the cloud filesystem.
// indexPrompt is the opening prompt as it may appear in INDEX.md and index/*.json:
// withheld for an excluded project - excluding it used to hide only its pages - and
// with credentials masked, as on the pages.
func indexPrompt(prompt, project, bucket string, excludes []string) string {
	if render.IsExcluded(project, bucket, excludes) {
		return "(excluded)"
	}
	return render.Redact(prompt)
}

// splitList parses a comma-separated flag value.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func writeIndex(dest string, excludes []string) error {
	rows, err := archive.AllSessions(dest)
	if err != nil {
		return err
	}

	machines := map[string]bool{}
	byProject := map[string][]archive.Session{}
	for _, r := range rows {
		machines[r.Machine] = true
		key := r.Project
		if key == "" {
			key = r.Bucket
		}
		byProject[key] = append(byProject[key], r)
	}
	names := make([]string, 0, len(byProject))
	for k := range byProject {
		names = append(names, k)
	}
	sort.Strings(names)

	contributors := make([]string, 0, len(machines))
	for m := range machines {
		contributors = append(contributors, m)
	}
	sort.Strings(contributors)

	for i, c := range contributors {
		contributors[i] = mdText(c)
	}

	var b strings.Builder
	b.WriteString("# Claude sessions\n\n")
	// Dated by the newest session, not by "now": a push that changed nothing then
	// writes nothing, rather than a new INDEX.md for the sync client to upload.
	if len(rows) > 0 {
		fmt.Fprintf(&b, "Newest session %s. ", rows[0].Updated.Format("2006-01-02 15:04"))
	}
	fmt.Fprintf(&b, "%d session(s) from %s.\n\n", len(rows), strings.Join(contributors, ", "))
	b.WriteString("Bring these to another machine with `claude-sessions import`.\n\n")

	multiMachine := len(contributors) > 1

	for _, name := range names {
		group := byProject[name]
		sort.Slice(group, func(i, j int) bool { return group[i].Updated.After(group[j].Updated) })

		fmt.Fprintf(&b, "## %s\n\n", mdText(name))

		// INDEX.md is meant to be browsable on a phone. Listing every session a
		// long-lived project ever had turns it into a file nothing wants to open, and
		// the full record is in the .jsonl files and the per-machine index anyway.
		shown := group
		omitted := 0
		if len(shown) > maxIndexRowsPerProject {
			omitted = len(shown) - maxIndexRowsPerProject
			shown = shown[:maxIndexRowsPerProject]
		}

		if multiMachine {
			b.WriteString("| Updated | Size | Machine | Session | First prompt |\n|---|---|---|---|---|\n")
		} else {
			b.WriteString("| Updated | Size | Session | First prompt |\n|---|---|---|---|\n")
		}
		for _, r := range shown {
			// Rows from another machine may predate an exclusion or redaction, so both
			// are applied again here rather than trusted from the index file.
			prompt := mdText(indexPrompt(r.Prompt, r.Project, r.Bucket, excludes))
			if multiMachine {
				fmt.Fprintf(&b, "| %s | %s | %s | `%s` | %s |\n",
					r.Updated.Format("2006-01-02 15:04"), humanSize(r.Size), mdText(r.Machine), r.ID, prompt)
			} else {
				fmt.Fprintf(&b, "| %s | %s | `%s` | %s |\n",
					r.Updated.Format("2006-01-02 15:04"), humanSize(r.Size), r.ID, prompt)
			}
		}
		if omitted > 0 {
			fmt.Fprintf(&b, "\n_%d older session(s) not listed here; every one is still in `projects/`, and the full listing is in `index/`._\n", omitted)
		}
		b.WriteString("\n")
	}

	return archive.WriteFileAtomic(filepath.Join(dest, "INDEX.md"), []byte(b.String()), 0o644)
}

// mdText makes text from the archive inert inside INDEX.md: one line, no control
// characters, and Markdown syntax escaped. Project names, prompts and machine names
// are written by other machines; unescaped, one could break the table or embed a
// remote image that reports every time the file is previewed.
func mdText(s string) string {
	s = strings.Join(strings.Fields(printable(s)), " ")
	return strings.NewReplacer(`\`, `\\`, "`", "\\`", "*", `\*`, "_", `\_`, "[", `\[`, "]", `\]`,
		"<", `\<`, ">", `\>`, "!", `\!`, "|", `\|`, "#", `\#`).Replace(s)
}

// machineName identifies the writer of a manifest shard.
//
// It includes the username, not just the hostname. Two accounts on one machine each
// have their own ~/.claude and their own sessions, but share a hostname - so keying
// on the hostname alone means they overwrite each other's shard, and each push
// silently discards the other user's project identities.
func machineName() string {
	host := "unknown-machine"
	if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
		host = h
	}

	username := ""
	if u, err := user.Current(); err == nil {
		username = u.Username
		// Windows reports DOMAIN\user; only the account name is wanted.
		if i := strings.LastIndexAny(username, `\/`); i >= 0 {
			username = username[i+1:]
		}
	}

	name := host
	if username != "" && !strings.EqualFold(host, username) {
		name = host + "-" + username
	}
	// A hostname or username can contain characters that are awkward in a filename.
	return strings.NewReplacer("/", "-", `\`, "-", ":", "-", " ", "-", "*", "-", "?", "-").Replace(name)
}

// maxLogBytes is when session-sync.log is rotated to session-sync.log.1.
const maxLogBytes = 1 << 20

// newLogger returns a printf-style logger that appends to session-sync.log.
//
// The log is the only record of what an unattended hook or sweep did. It never
// returns an error: failing to log must not fail a push.
func newLogger(root string, quiet bool) func(string, ...any) {
	path := filepath.Join(root, "session-sync.log")
	return func(format string, args ...any) {
		line := fmt.Sprintf("%s  %s\n", time.Now().Format("2006-01-02 15:04:05"),
			fmt.Sprintf(format, args...))
		// Two runs an hour, forever: keep one previous log rather than an unbounded one.
		if info, err := os.Stat(path); err == nil && info.Size() > maxLogBytes {
			_ = os.Rename(path, path+".1")
		}
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			f.WriteString(line)
			f.Close()
		}
		// Progress goes to stderr, never stdout: stdout carries the result, and with
		// --json a stray log line makes the output unparseable.
		if !quiet {
			fmt.Fprint(os.Stderr, line)
		}
	}
}
