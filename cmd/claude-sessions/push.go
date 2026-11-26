package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/convzip"
	"github.com/ashiqfardus/claude-sessions-sync/internal/identity"
	"github.com/ashiqfardus/claude-sessions-sync/internal/render"
	"github.com/ashiqfardus/claude-sessions-sync/internal/work"
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
	fromHook := fs.Bool("from-hook", false, "Stop hook mode: archive only the session named by the hook input on stdin")
	sweep := fs.Bool("sweep", false, "set by the scheduled sweep, which marks its log line: the one run that may lack the terminal's privacy grants")
	scanHistory := fs.Bool("scan-history", false, "this run only, read every conversation in full for work folders, once, for conversations from before the first run; this reads ones imported from other computers too: use it only if you trust everyone who can write to the archive")
	if err := parseFlags(fs, args, false); err != nil {
		// A hook written by a newer version may pass a flag this binary does not know.
		// That must not break the end of the session any more than a missing drive -
		// but it must still leave a trace, or a downgrade silently archives nothing.
		for _, a := range args {
			if a == "--quiet" || a == "-quiet" || a == "--quiet=true" || a == "-quiet=true" {
				if root, _, rerr := resolveRoot(*claudeDir); rerr == nil {
					newLogger(root, true, nil)("SKIP: %v (a newer version's hook? re-run `claude-sessions install`)", err)
				}
				return nil
			}
		}
		return err
	}

	opts := pushOpts{Quiet: *quiet, DryRun: *dryRun, NoHTML: *noHTML, LockWait: *lockWait, Sweep: *sweep, ScanHistory: *scanHistory}
	s := stdStreams()
	if *asJSON {
		s = jsonStreams()
	}
	if *fromHook {
		// Claude Code passes the hook's input as JSON on stdin; transcript_path is the
		// session that just replied.
		var in struct {
			TranscriptPath string `json:"transcript_path"`
		}
		_ = json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&in)
		opts.Session = in.TranscriptPath
		opts.FromHook = true
		opts.LockWait = 0 // never hold up the next prompt: a run in progress covers it

		// A hung filesystem call - a NAS asleep - is not covered by the lock wait, and
		// would hold up every reply until Claude Code kills the hook. Give up instead;
		// the sweep catches up.
		var err error
		if !withinDeadline(hookDeadline, func() { _, err = runPush(s, *claudeDir, *archiveDir, opts) }) {
			if root, _, rerr := resolveRoot(*claudeDir); rerr == nil {
				noteHookTimeout(root)
			}
			return nil
		}
		if *quiet && err != nil {
			return nil
		}
		return err
	}
	res, err := runPush(s, *claudeDir, *archiveDir, opts)
	// A quiet run skipped for the lock returns no Archive, and prints no JSON.
	if err == nil && *asJSON && res.Archive != "" {
		err = encodeJSON(os.Stdout, res)
	}

	// A SessionEnd hook that fails breaks the end of the user's session. Missing
	// drive, offline mount, unexpected error: log it and exit 0. This is the single
	// most important behaviour in the whole command.
	if *quiet && err != nil {
		return nil
	}
	return err
}

// hookDeadline bounds the per-reply hook, well inside the 30 seconds Claude Code
// allows it.
const hookDeadline = 5 * time.Second

// withinDeadline runs work and reports whether it finished in time. Work that does
// not is left running; the caller returns, and the process exits under it - its
// writes are atomic, and a lock it held is recognised as stale by its dead PID.
func withinDeadline(d time.Duration, work func()) bool {
	done := make(chan struct{})
	go func() {
		defer close(done)
		work()
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// noteHookTimeout logs that a per-reply push gave up - at most once an hour, so a slow
// drive is visible in the log without a line after every reply.
func noteHookTimeout(root string) {
	marker := filepath.Join(root, "session-sync.hook-timeout")
	if st, err := os.Stat(marker); err == nil && time.Since(st.ModTime()) < time.Hour {
		return
	}
	newLogger(root, true, nil)("TIMEOUT: a per-reply push gave up after %s, the archive's drive being slow or asleep; the sweep and the SessionEnd hook still archive the session", hookDeadline)
	_ = os.WriteFile(marker, nil, 0o600)
	now := time.Now()
	_ = os.Chtimes(marker, now, now)
}

type pushOpts struct {
	Quiet, DryRun, NoHTML bool
	LockWait              time.Duration // --quiet only
	FromHook              bool          // archive only Session, the Stop hook's
	Session               string
	Sweep                 bool // the scheduled run: its PUSH line is marked
	ScanHistory           bool // read every transcript in full for work folders, this run only
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

// runPush archives this profile's conversations and returns what it did: log lines on
// s.Err, the summary on s.Out. A run skipped for the lock, or the per-reply hook's,
// returns no Archive.
func runPush(s streams, claudeDir, archiveDir string, opts pushOpts) (_ pushResult, err error) {
	quiet, dryRun, noHTML := opts.Quiet, opts.DryRun, opts.NoHTML
	root, _, err := resolveRoot(claudeDir)
	if err != nil {
		return pushResult{}, err
	}

	logf := newLogger(root, quiet, s.Err)
	if opts.Sweep {
		// Every line of a sweep run is marked, not only its PUSH line: a sweep refused
		// from its very first run leaves no PUSH, and doctor must still see that a
		// sweep exists and was the run refused.
		plain := logf
		logf = func(format string, args ...any) { plain(format+sweepMark, args...) }
	}

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

	// The per-reply hook meets these routinely - drive offline, a sweep running - and
	// would log one line per reply; the sweep and the SessionEnd hook report them.
	skipf := logf
	if opts.FromHook {
		skipf = func(string, ...any) {}
	}

	dest, src, err := archive.ResolveDestination(root, archiveDir)
	if err != nil {
		logf("ERROR: %v", err)
		return pushResult{}, err
	}
	if dest == "" {
		skipf("SKIP: no destination configured")
		return pushResult{}, fmt.Errorf("no archive destination: run `claude-sessions config set-destination <folder>`")
	}

	// The destination's parent must already exist. Creating it would mean silently
	// making a plain local folder where an unmounted cloud drive should be, and
	// reporting success - the exact failure that makes someone trust a backup that
	// is not happening. The reason is logged: a macOS privacy refusal reads
	// "operation not permitted", and doctor looks for it.
	if st, statErr := os.Stat(filepath.Dir(dest)); statErr != nil || !st.IsDir() {
		reason := "not a directory"
		if statErr != nil {
			reason = statErr.Error()
		}
		skipf("SKIP: destination parent not available: %s", reason)
		return pushResult{}, fmt.Errorf("%s is not available - is the sync client mounted?", filepath.Dir(dest))
	}

	if err := archive.ValidateDestination(root, dest); err != nil {
		skipf("SKIP: %v", err)
		return pushResult{}, err
	}

	// Unattended runs wait for one another: the sweep and a hook, or two sessions
	// ending together, are routine. Someone at a terminal is told straight away.
	wait := time.Duration(0)
	if quiet {
		wait = opts.LockWait
	}
	lock, err := archive.AcquireLockWait(root, wait)
	if err != nil {
		skipf("SKIP: %v", err)
		if quiet {
			return pushResult{}, nil
		}
		return pushResult{}, err
	}
	defer lock.Release()

	// Before anything that walks the archive: this runs after every reply.
	if opts.FromHook {
		return pushResult{}, pushOne(root, dest, opts.Session, dryRun, logf)
	}

	if !dryRun {
		if err := os.MkdirAll(archive.ProjectsDir(dest), 0o755); err != nil {
			logf("ERROR: %v", err)
			return pushResult{}, err
		}
		if n := archive.RemoveOrphanTemps(dest); n > 0 {
			logf("CLEAN: removed %d temporary file(s) left by an interrupted run", n)
		}
		removeStaleExtracts(time.Now())
		pinMachineName(root)
	}

	buckets, err := claude.ListBuckets(claude.ProjectsDir(root))
	if err != nil {
		logf("ERROR: %v", err)
		return pushResult{}, err
	}

	statePaths, _ := claude.ProjectPaths(claude.StatePath(root))
	var hintTo io.Writer
	if !quiet {
		hintTo = s.Err
	}
	// The first archive reads, to the one of pushedBefore. Stopped on every return too,
	// a panic's included: the hint must not reach s.Err once push has returned. Nothing
	// else writes to s.Err until it is stopped.
	slowRead := hintIfSlowTo(hintTo)
	defer slowRead()
	excludes, republished := archive.RenderExcludesDropped(root, dest, nil, !dryRun)
	for _, p := range republished {
		logf("RENDER: %s is published again (another computer ran render --include %s); run render --exclude %s to withdraw it", p, p, p)
	}

	res := pushResult{Archive: dest, DryRun: dryRun}
	shard := archive.Shard{
		SchemaVersion: archive.SchemaVersion,
		Machine:       machineName(),
		ClaudeRoot:    root,
		Home:          userHome(),
		ToolVersion:   version,
		FormerNames:   savedFormer(),
		Projects:      map[string]archive.Project{},
	}
	var sessions []archive.Session

	// Related repositories are carried forward from this machine's last shard and only
	// looked for again in transcripts that changed; the first push after upgrading
	// scans everything once.
	prevShard := readShard(dest, shard.Machine)
	scanAll := !prevShard.RelatedIndexed
	shard.RelatedIndexed = true
	pushed := pushedBefore(dest, shard.Machine)
	slowRead()
	repointed := loadRepointed(root)
	repointedWas := len(repointed)
	held, holders, zips := loadHeld(root), sessionHolders(dest), loadZipCompared(root)
	// Zipped copies push compares with are unpacked here, gone when push returns.
	held.tmp, _ = os.MkdirTemp("", "claude-sessions-push-")
	zips.tmp = held.tmp
	defer os.RemoveAll(held.tmp)
	shard.SkipsHeld = true
	shard.ReadsZips = true
	// A conversation pruned from the archive, and not continued here since, stays out of
	// it. A list that cannot be read may name any: every one is archived, as before.
	shard.HonorsDeleted = true
	deleted, derr := archive.LoadDeleted(dest)
	if derr != nil {
		logf("WARN: the deleted list cannot be read, so conversations pruned from the archive may be archived again: %v", derr)
	}
	// Transcripts to look for work folders in: those changed since the last full push
	// - not only those copied now, as the per-reply hook has usually copied them.
	var workScan []string
	_, werr := loadWorkFolders(root)
	scanAllWork := !prevShard.WorkIndexed || werr != nil || opts.ScanHistory

	for _, b := range buckets {
		destBucket := filepath.Join(archive.ProjectsDir(dest), b.Name)

		// Each transcript is read once. The identity below reuses what the newest
		// one yields rather than opening it a second time.
		var newestCwd string
		var newestAt time.Time
		var changed []string

		for _, t := range b.Transcripts {
			srcInfo, statErr := os.Stat(t.Path)
			if statErr != nil {
				res.Skipped = append(res.Skipped, t.ID)
				continue
			}
			if prunedHere(deleted, t.ID, t.Path) {
				continue
			}
			wasHeld := held.known(b.Name, t.ID, srcInfo)
			outcome := archiveTranscript(dest, b.Name, t.ID, t.Path, srcInfo, pushed, repointed, held, othersHolding(holders, b.Name, t.ID), zips, dryRun, logf)
			// A transcript held, and unchanged, since a push that looked through it has
			// nothing new to look for work folders in; one held from now on is looked
			// through this once, as the per-reply hook may have archived lines of it.
			if scanAllWork || !(outcome == heldOther && wasHeld) && changedSincePush(pushed, b.Name, t.ID, srcInfo) {
				workScan = append(workScan, t.Path)
				for _, rel := range t.Extras {
					if claude.IsSubagentTranscript(rel) {
						workScan = append(workScan, filepath.Join(b.Dir, t.ID, rel))
					}
				}
			}
			switch outcome {
			case copyFailed:
				// One unreadable transcript must not abandon the rest.
				res.Skipped = append(res.Skipped, t.ID)
				continue
			case keptShorter:
				res.Skipped = append(res.Skipped, t.ID)
			case copiedOver:
				res.Copied++
				changed = append(changed, t.Path)
			case keptFork:
				res.Copied++
			default:
				res.Unchanged++
			}
			res.Sessions++

			summary, _ := claude.ScanHead(t.Path, headScanLines)
			if summary.Cwd != "" && t.ModTime.After(newestAt) {
				newestAt = t.ModTime
				newestCwd = summary.Cwd
			}
			if outcome == heldOther {
				// Another bucket's copy is the archived one, and is listed there: neither
				// this one's own files nor an index row are added here.
				continue
			}

			if outcome == keptZipped {
				res.Unchanged += len(t.Extras) // in the zip as they are here
			} else {
				c, u := archiveExtras(b.Dir, destBucket, t.ID, t.Extras, dryRun, logf)
				res.Copied += c
				res.Unchanged += u
			}

			sessions = append(sessions, archive.Session{
				Bucket: b.Name, Project: summary.Cwd, ID: t.ID,
				Updated: t.ModTime, Size: t.Size, Prompt: indexPrompt(summary.FirstPrompt, summary.Cwd, b.Name, excludes),
			})
		}

		// Memory travels with the sessions; it is small and equally unrecoverable.
		for _, name := range b.Memory {
			srcPath := filepath.Join(b.Dir, "memory", name)
			target := filepath.Join(destBucket, "memory", name)
			srcInfo, statErr := os.Stat(srcPath)
			if statErr != nil {
				continue
			}
			// This machine's own copy too, which no other machine writes. On a shared
			// layout the bucket's memory is everyone's, newest file wins, so an import
			// could not tell another machine's index from this machine's own push.
			if own := filepath.Join(machineMemoryDir(dest, machineName(), b.Name), name); !dryRun && archive.NeedsCopy(srcInfo, archive.StatOrNil(own)) {
				if err := archive.CopyFile(srcPath, own); err != nil {
					logf("WARN: memory/%s/%s: %v", machineName(), name, err)
				}
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
		if !dryRun && len(b.Memory) > 0 {
			noteHeldMemory(root, b.Name, filepath.Join(b.Dir, "memory"))
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
			entry.Remote = carriedRemote(entry, prevShard.Projects[b.Name])
			shard.Projects[b.Name] = entry
			res.Projects++
		}
	}

	if len(repointed) != repointedWas && !dryRun {
		if err := saveRepointed(root, repointed); err != nil {
			logf("WARN: %s: %v", repointedPath(root), err)
		}
	}
	if !dryRun {
		if err := held.save(root); err != nil {
			logf("WARN: %s: %v", heldPath(root), err)
		}
		if err := zips.save(root); err != nil {
			logf("WARN: %s: %v", zipComparedPath(root), err)
		}
	}

	// The folders those sessions worked in. A config that cannot be read may have lost
	// its exclusions, so nothing is copied until it is fixed.
	cfg, cerr := archive.LoadConfig(root)
	if cerr != nil {
		logf("WARN: work folders not backed up: %v", cerr)
		cfg.WorkBackup = "off"
	}
	workBudget := time.Duration(0)
	if quiet {
		workBudget = quietWorkBudget
	}
	wr := pushWork(root, dest, &shard, prevShard, workScan, cfg, dryRun, opts.ScanHistory, workBudget, logf, s.Err)
	res.Copied += wr.Copied
	res.Unchanged += wr.Unchanged

	// Global rules, skills and the plugin list, so another machine behaves the same.
	pc, pu := pushProfile(root, dest, dryRun, logf)
	res.Copied += pc
	res.Unchanged += pu

	if !dryRun {
		// Two computers under one name overwrite each other's shard. Said, never refused:
		// a reinstall here has a new ID too, and must still back up.
		if shard.InstallID = installID(root); shard.InstallID != "" && nameClash(dest, shard.Machine, shard.InstallID) {
			logf("WARN: "+nameClashMessage, shard.Machine)
		}
		shard.LastPush = time.Now().UTC().Truncate(time.Hour)
		sessions = keepArchivedHistory(dest, &shard, sessions)
		if err := writeManifest(dest, shard); err != nil {
			logf("ERROR: manifest: %v", err)
			return pushResult{}, err
		}
		// Once the shard says this computer honours the deleted list, so it waits for no
		// one but the others; before the index, which then lists none it deleted.
		// Unattended, only the sweep runs it, as only the sweep zips.
		if !quiet || opts.Sweep {
			budget := time.Duration(0)
			if quiet { // the sweep
				budget = ageRuleBudget
			}
			if ret, err := archive.LoadRetention(dest); err != nil {
				logf("WARN: keep-days: %v", err)
			} else if _, err := runAgeRule(root, dest, ret, time.Now(), budget, logf); err != nil {
				logf("WARN: keep-days: %s", strings.ReplaceAll(err.Error(), "\n", " "))
			}
		}
		if err := archive.WriteSessionIndex(dest, shard.Machine, withoutPruned(dest, sessions)); err != nil {
			logf("ERROR: index cache: %v", err)
			return pushResult{}, err
		}
		// Built from every machine, not just this one - see writeIndex.
		if err := writeIndex(dest, excludes); err != nil {
			logf("ERROR: index: %v", err)
			return pushResult{}, err
		}
	}

	// A dry run copied nothing: its line must never start with "PUSH:", which doctor
	// reads as the last real push.
	verb := "PUSH: %d copied"
	if dryRun {
		verb = "DRY-RUN: would copy %d"
	}
	logf(verb+", %d unchanged, %d session(s), %d project(s) -> %s",
		res.Copied, res.Unchanged, res.Sessions, res.Projects, dest)

	// Conversations idle for zipIdle become one zip each, in the buckets this machine
	// pushes to, once every machine reads zips. Not from the per-reply hook: it returned
	// above. Unattended, only the sweep zips: the SessionEnd hook is killed after 120
	// seconds, already shared by work folders and pages. A config that cannot be read
	// may have lost `zip-sessions off`.
	if (!quiet || opts.Sweep) && cerr == nil && cfg.ZipOn() {
		if waits, err := zipHoldouts(dest); err == nil && len(waits) == 0 {
			mine := make([]string, 0, len(shard.Projects))
			for b := range shard.Projects {
				mine = append(mine, b)
			}
			sort.Strings(mine)
			zipBudget := time.Duration(0)
			if quiet { // the sweep
				zipBudget = quietRenderBudget
			}
			if n, raw, packed := zipIdleCopies(root, dest, mine, time.Now(), zipBudget, dryRun, logf); n > 0 {
				logf("ZIP: %d conversation(s), %s -> %s", n, humanSize(raw), humanSize(packed))
			}
		}
	}

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

	if !quiet {
		for _, n := range wr.New {
			fmt.Fprintf(s.Out, "now backing up %s\n", n)
		}
		for _, p := range wr.Would {
			fmt.Fprintf(s.Out, "would copy %s\n", p)
		}
		for _, p := range wr.WouldPack {
			fmt.Fprintf(s.Out, "would %s\n", p)
		}
		if wr.Packed+wr.Compacted > 0 {
			fmt.Fprintf(s.Out, "%d subfolder(s) packed, %d packed again.\n", wr.Packed, wr.Compacted)
		}
		verb := "copied"
		if dryRun {
			verb = "would copy"
		}
		fmt.Fprintf(s.Out, "\n%s %d file(s), %d unchanged.\n", verb, res.Copied, res.Unchanged)
		fmt.Fprintf(s.Out, "%d session(s) across %d project(s) -> %s (%s)\n",
			res.Sessions, res.Projects, dest, src)
		if len(res.Skipped) > 0 {
			fmt.Fprintf(s.Out, "%d skipped: %s\n", len(res.Skipped), strings.Join(res.Skipped, ", "))
		}
	}
	return res, nil
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

// carriedRemote is entry's remote as read, or prev's when entry's folder cannot be looked
// at now - deleted, moved, on a drive not plugged in: a project kept by its remote stays
// that project. A folder that is there is taken as it reads, another remote or none.
func carriedRemote(entry, prev archive.Project) string {
	// Never a path that is not local: looking at it would connect to its host.
	if entry.Remote != "" || prev.Remote == "" || !identity.Local(entry.Path) || !samePath(entry.Path, prev.Path) {
		return entry.Remote
	}
	if _, err := os.Stat(entry.Path); err == nil {
		return entry.Remote
	}
	return prev.Remote
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
				if _, err := os.Stat(filepath.Join(archive.ProjectsDir(dest), s.Bucket, s.ID+".jsonl")); err == nil || zippedCopy(dest, s.Bucket, s.ID) != "" {
					sessions = append(sessions, s)
				}
			}
		}
	}
	return sessions
}

type archiveOutcome int

const (
	notCopied archiveOutcome = iota
	copiedOver
	keptFork
	keptShorter
	copyFailed
	heldOther  // another bucket's copy holds all of it: nothing written
	keptZipped // the archived copy is zipped and holds every turn of it and its folder
)

// archiveTranscript brings the archived copy of one local transcript up to date. The
// archived copy is replaced only by one that holds more; when each holds turns the
// other lacks, this machine's is kept beside it as a fork. A transcript in repointed
// is the exception, and its entry is used up here: the caller saves the change.
//
// Nothing is written when one of others - archive buckets holding a transcript of the
// same session - already holds every turn of this one: a conversation imported from
// another machine, and not continued here, is not archived a second time under this
// machine's folder for it. held, nil in the per-reply hook, remembers those.
//
// A bucket's zipped copy is settled first, by zips: see zipRecord.judge.
func archiveTranscript(dest, bucket, id, path string, srcInfo os.FileInfo, pushed map[string]archive.Session, repointed map[string]repointedStat, held *heldRecord, others []string, zips *zipRecord, dryRun bool, logf func(string, ...any)) archiveOutcome {
	target := filepath.Join(archive.ProjectsDir(dest), bucket, filepath.Base(path))
	if held.hit(dest, bucket, id, srcInfo) {
		delete(repointed, bucket+"/"+id)
		return heldOther
	}
	// A loose copy is never written beside a zipped one it was not compared with.
	if zp := zippedCopy(dest, bucket, id); zp != "" {
		if out, done := zips.judge(dest, bucket, id, zp, path, srcInfo, dryRun, logf); done {
			return out
		}
	}
	dstInfo := archive.StatOrNil(target)

	// Rewritten by import --repoint and unchanged since: this machine's paths, so it
	// replaces the archived copy once, shorter or not - but only over the same turns or
	// fewer. An archived copy that is newer, or holds turns this one lacks (continued
	// elsewhere, a fork), is left to the rules below. Changed here since, it is a
	// session resumed, and the rules below judge it too.
	if r, ok := repointed[bucket+"/"+id]; ok {
		delete(repointed, bucket+"/"+id)
		if srcInfo.Size() == r.Size && archive.SameMTime(srcInfo.ModTime(), r.MTime) &&
			(dstInfo == nil || !srcInfo.ModTime().Before(dstInfo.ModTime().Add(-archive.MTimeToleranceSeconds*time.Second)) &&
				slices.Contains([]claude.Relation{claude.Same, claude.Extends}, claude.Compare(path, target))) {
			if held.covers(dest, bucket, id, path, srcInfo, others) {
				return heldOther
			}
			if !dryRun {
				if err := archive.CopyFile(path, target); err != nil {
					repointed[bucket+"/"+id] = r // tried again next time
					logf("WARN: %s: %v", id, err)
					return copyFailed
				}
			}
			return copiedOver
		}
	}
	copyIt := archive.NeedsCopy(srcInfo, dstInfo)
	shorter := copyIt && dstInfo != nil && srcInfo.Size() < dstInfo.Size()

	// Dates and sizes cannot tell a continuation from a session continued on two
	// machines: on a shared layout the other machine's turns were overwritten, or this
	// one's never archived. So a copy that differs from the archived one is compared by
	// content before it replaces it - every time, not only when it changed here: a
	// fork kept by the last push is still a fork - and when it changed here since the
	// last push, in case this is the copy that forked.
	// shortcut: a branch of the same size, its mtime within 2 s of the archived copy's, is
	// taken for that copy, and its turns stay local until the conversation next changes;
	// compare content when it changed since the last push if this proves real.
	if dstInfo != nil && !sameStat(srcInfo, dstInfo) && (copyIt || changedSincePush(pushed, bucket, id, srcInfo)) {
		// Already kept as a fork, and nothing new here since: not worth reading two
		// whole transcripts - from a cloud drive, perhaps - on every push.
		if fork := archive.StatOrNil(forkPath(dest, bucket, id, machineName())); fork != nil && sameStat(srcInfo, fork) {
			return notCopied
		}
		switch claude.Compare(path, target) {
		case claude.Extends:
			copyIt, shorter = true, false
		case claude.Same, claude.Behind:
			return notCopied
		case claude.Forked:
			if held.covers(dest, bucket, id, path, srcInfo, others) {
				return heldOther
			}
			if keepFork(dest, bucket, id, path, srcInfo, dryRun, logf) {
				return keptFork
			}
			return notCopied
		}
	}

	switch {
	case shorter:
		// Transcripts only grow. A newer but shorter local file is a session that
		// forked on two machines, or a truncated copy; either way the archived
		// conversation is not thrown away for it.
		logf("WARN: %s: local copy is shorter than the archived one (%d < %d bytes); archive kept",
			id, srcInfo.Size(), dstInfo.Size())
		return keptShorter
	case !copyIt:
		return notCopied
	case held.covers(dest, bucket, id, path, srcInfo, others):
		return heldOther
	}
	if !dryRun {
		if err := archive.CopyFile(path, target); err != nil {
			logf("WARN: %s: %v", id, err)
			return copyFailed
		}
	}
	return copiedOver
}

// zipCompared is a local transcript as push compared it with its bucket's zipped copy,
// and that zip as it then was.
type zipCompared struct {
	Size       int64     `json:"size"`
	MTime      time.Time `json:"mtime"`
	ZipSize    int64     `json:"zipSize"`
	ZipMTime   time.Time `json:"zipMtime"`
	Diverged   bool      `json:"diverged,omitempty"`   // the zip and the loose copy beside it each lack turns
	Unreadable bool      `json:"unreadable,omitempty"` // the zip could not be read or unzipped
}

// zipRecord remembers, by "bucket/id", how a local transcript compared with its zipped
// copy, so each pair is unpacked and compared once: the zip may be on a cloud drive, and
// the per-reply hook runs after every reply. Nothing in it comes from the archive.
type zipRecord struct {
	m     map[string]zipCompared
	dirty bool
	tmp   string // where zips are unpacked to compare; "" for the OS temp folder
	hook  bool   // the per-reply hook: no zip is read or unzipped, only the record
}

func zipComparedPath(root string) string {
	return filepath.Join(root, "session-sync-base", "zip-compared.json")
}

// loadZipCompared reads the record. Missing or unreadable is empty: push compares again.
func loadZipCompared(root string) *zipRecord {
	z := &zipRecord{}
	if raw, err := os.ReadFile(zipComparedPath(root)); err == nil {
		_ = json.Unmarshal(raw, &z.m)
	}
	if z.m == nil {
		z.m = map[string]zipCompared{}
	}
	return z
}

func (z *zipRecord) save(root string) error {
	if !z.dirty {
		return nil
	}
	data, err := json.MarshalIndent(z.m, "", "  ")
	if err != nil {
		return err
	}
	return archive.WriteFileAtomic(zipComparedPath(root), append(data, '\n'), 0o644)
}

// judge settles a local transcript whose bucket's copy is the zip zp, before the rules
// in archiveTranscript run. A zip holding every turn of it and of its folder stays, and
// nothing is archived: done, keptZipped. So does one this machine's branch forked from,
// when that branch is archived as its fork already and unchanged since. Otherwise the
// zip is unzipped, and the rules judge the loose copy. Two zips stay beside the loose
// copy, which readers prefer and a later unzip compares with them, so no branch is
// lost: one diverged from it, and one that cannot be read or unzipped - said once for
// the zip as it is. Nor is a zip with loose files beside it unzipped: see below.
func (z *zipRecord) judge(dest, bucket, id, zp, path string, src os.FileInfo, dryRun bool, logf func(string, ...any)) (archiveOutcome, bool) {
	zst, err := os.Lstat(zp)
	if err != nil {
		return 0, false
	}
	key := bucket + "/" + id
	prev, had := z.m[key]
	now := zipCompared{Size: src.Size(), MTime: src.ModTime(), ZipSize: zst.Size(), ZipMTime: zst.ModTime()}
	sameZip := had && prev.ZipSize == now.ZipSize && archive.SameMTime(prev.ZipMTime, now.ZipMTime)
	if sameZip && prev.Unreadable {
		return 0, false
	}
	if sameZip && prev.Size == now.Size && archive.SameMTime(prev.MTime, now.MTime) {
		return keptZipped, !prev.Diverged // a diverged pair: the rules judge the loose copy
	}
	// A loose transcript beside the zip: on a shared bucket, often another machine's own
	// zip whose deletions of the loose files have not arrived. Unzipping would remove that
	// zip on the strength of files on their way out. It stays, the rules judge the loose
	// copy, and the sweep settles the pair.
	if archivedCopy(dest, bucket, id) != "" {
		return 0, false
	}
	// The per-reply hook, which may be killed at any time, leaves what the record does not
	// settle to the next full push: unpacking a zip there is not worth a reply's wait.
	if z.hook {
		return notCopied, true
	}
	dir := filepath.Dir(zp)
	unreadable := func(err error) (archiveOutcome, bool) {
		logf("WARN: %s: %v; this copy is archived loose beside the zip", id, err)
		now.Unreadable = true
		z.m[key], z.dirty = now, true
		return 0, false
	}
	if !zipHolds(zp, id, path, src) {
		p, done, err := materialize(dir, id, z.tmp)
		if errors.Is(err, os.ErrNotExist) {
			return 0, false
		}
		if err != nil {
			return unreadable(err)
		}
		r := claude.Compare(path, p)
		// A branch kept as its fork already, unchanged since, as the rules would find it.
		fork := archive.StatOrNil(forkPath(dest, bucket, id, machineName()))
		forked := r == claude.Forked && fork != nil && sameStat(src, fork)
		holds := (r == claude.Same || r == claude.Behind || forked) &&
			folderHeld(filepath.Join(filepath.Dir(path), id), filepath.Join(filepath.Dir(p), id)) == nil
		done()
		if !holds {
			// Nor beside folder files: they may be on their way out too.
			if dryRun || len(claude.SessionFiles(filepath.Join(dir, id))) > 0 {
				return 0, false
			}
			switch err := convzip.Unzip(dir, id); {
			case errors.Is(err, convzip.ErrDiverged):
				if !had || !prev.Diverged { // said once, not on every run until it is resolved
					logf("WARN: %s: %v; the zip is kept beside the loose copy", id, err)
				}
				now.Diverged = true
				z.m[key], z.dirty = now, true
				return 0, false
			case err != nil && !errors.Is(err, os.ErrNotExist):
				return unreadable(err)
			}
			if had {
				delete(z.m, key)
				z.dirty = true
			}
			return 0, false
		}
	}
	z.m[key], z.dirty = now, true
	return keptZipped, true
}

// unzip is convzip.Unzip, which tests replace to have the zip go mid-run.
var unzip = convzip.Unzip

// zipIdle is how long every archived file of a conversation, and this machine's
// transcript of it, must have gone unchanged before push zips it.
const zipIdle = 30 * 24 * time.Hour

// zipHoldouts names the other machines whose shard does not say they read zips; push
// zips nothing until there are none. This machine's own shard is left out: it predates
// this build until the push in progress writes it.
func zipHoldouts(dest string) ([]string, error) {
	out, err := archive.Holdouts(dest, func(s archive.Shard) bool { return s.ReadsZips })
	return slices.DeleteFunc(out, func(n string) bool { return strings.EqualFold(n, machineName()) }), err
}

// zipSkippedPath remembers, by "bucket/id", the size of each archived copy push could
// not zip as it was: over the cap, a leftover beside its zip that diverged from it, or a
// zip or unzip that failed. Each is said once, and tried again only once that size
// changes.
func zipSkippedPath(root string) string {
	return filepath.Join(root, "session-sync-base", "zip-skipped.json")
}

// bothSeenPath remembers, by "bucket/id", each zip a sweep found with loose files beside
// it: that state by stat alone, when it was first seen, confirmed and last seen.
// Nothing in it comes from the archive.
func bothSeenPath(root string) string {
	return filepath.Join(root, "session-sync-base", "both-seen.json")
}

type bothSeen struct {
	State     string    `json:"state"`
	Since     time.Time `json:"since"`
	Confirmed time.Time `json:"confirmed,omitempty"`
	Last      time.Time `json:"last"`
}

// A zip and the loose files beside it are removed or rewritten only once they have stood
// as they are bothSettle, which a later sighting confirms, and bothAwake more after
// that: wall-clock time alone may have passed with this computer asleep and its sync
// client stopped. A gap over bothGap between two sightings starts again.
const (
	bothSettle = 24 * time.Hour // a day: outlasts a lid closed mid-upload, and costs only space
	bothAwake  = time.Hour      // awake this long since the confirmation, the sync client has caught up
	bothGap    = 72 * time.Hour // longer between sightings, this computer was off: its view may be days old
)

// next is the record of a pair seen in state at now, and whether to act on it now.
// A new state, a clock gone back, or a gap of days is a first sighting again.
func (prev bothSeen) next(state string, now time.Time) (bothSeen, bool) {
	if prev.State != state || prev.Since.After(now) || prev.Confirmed.After(now) || prev.Last.After(now) || now.Sub(prev.Last) > bothGap {
		return bothSeen{State: state, Since: now, Last: now}, false
	}
	switch {
	case prev.Confirmed.IsZero() && now.Sub(prev.Since) >= bothSettle:
		prev.Confirmed = now
	case !prev.Confirmed.IsZero() && now.Sub(prev.Confirmed) >= bothAwake:
		return prev, true
	}
	prev.Last = now
	return prev, false
}

// bothState is a zip and the loose files beside it, by stat alone: the zip's size and
// time, then each loose file's name and size, sorted. "" when either is missing.
func bothState(dir, id string) string {
	st, err := os.Lstat(convzip.Path(dir, id))
	if err != nil || !st.Mode().IsRegular() {
		return ""
	}
	var b strings.Builder
	for _, top := range []string{filepath.Join(dir, id+".jsonl"), filepath.Join(dir, id)} {
		filepath.WalkDir(top, func(p string, d os.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return nil
			}
			if info, err := d.Info(); err == nil {
				rel, _ := filepath.Rel(dir, p)
				fmt.Fprintf(&b, "|%s %d", filepath.ToSlash(rel), info.Size())
			}
			return nil
		})
	}
	if b.Len() == 0 {
		return ""
	}
	return fmt.Sprintf("%d %d", st.Size(), st.ModTime().Unix()) + b.String()
}

// zipIdleCopies zips each conversation archived in buckets whose loose files, and this
// machine's transcript of it, are all unchanged since now-zipIdle, until budget (0 for
// none) runs out: the rest wait for the next run. Loose files beside a zip - another
// machine's zip whose deletions have not arrived, another machine's unzip whose removal
// of the zip has not, or a run killed half way - are only recorded until that state has
// stood long enough (bothSeen.next); then whatever was in flight has landed. They are
// removed when the zip holds every one, and the zip left as it is; only loose files
// holding something it lacks are unzipped into it, then zipped again. A dry run lists
// them.
func zipIdleCopies(root, dest string, buckets []string, now time.Time, budget time.Duration, dryRun bool, logf func(string, ...any)) (zipped int, raw, packed int64) {
	skipped := map[string]int64{}
	if data, err := os.ReadFile(zipSkippedPath(root)); err == nil {
		_ = json.Unmarshal(data, &skipped) // unreadable is empty: each is tried, and said, again
	}
	// Unreadable is empty too: each pair waits again, which is the safe way.
	seen := map[string]bothSeen{}
	if data, err := os.ReadFile(bothSeenPath(root)); err == nil && json.Unmarshal(data, &seen) != nil {
		seen = map[string]bothSeen{}
	}
	sawNow := map[string]bool{}
	dirty, seenDirty, start, cutoff := false, false, time.Now(), now.Add(-zipIdle)
each:
	for _, bucket := range buckets {
		dir := filepath.Join(archive.ProjectsDir(dest), bucket)
		if !archive.ValidName(bucket) || !archive.IsRealDir(dir) {
			continue
		}
		zips := convzip.Find(dir)
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if budget > 0 && time.Since(start) >= budget {
				break each
			}
			id, isTranscript := strings.CutSuffix(e.Name(), ".jsonl")
			zp := zips[strings.ToLower(id)]
			switch {
			case !claude.IsSessionID(id), isTranscript && !e.Type().IsRegular():
				continue
			// A folder with no transcript counts only beside its zip, and only holding a file:
			// what a killed Zip leaves. One emptied that the system would not remove is not.
			case !isTranscript && (!e.IsDir() || zp == "" || archive.StatOrNil(filepath.Join(dir, id+".jsonl")) != nil ||
				len(claude.SessionFiles(filepath.Join(dir, id))) == 0):
				continue
			}
			// A conversation in use is told by its transcript, without walking its folder.
			if info, err := e.Info(); isTranscript && (err != nil || info.ModTime().After(cutoff)) {
				continue
			}
			if st, err := os.Stat(filepath.Join(claude.ProjectsDir(root), bucket, id+".jsonl")); err == nil && st.ModTime().After(cutoff) {
				continue // still in use here
			}
			if _, idle := looseSize(dir, id, cutoff); !idle {
				continue
			}
			key, size := bucket+"/"+id, copyBytes(dir, id)
			if s, ok := skipped[key]; ok && s == size {
				continue
			}
			// Said once, then left until the copy changes: a failure that recurs would
			// otherwise be logged, and up to 250 MB read again, on every sweep.
			remember := func() { skipped[key], dirty = copyBytes(dir, id), true }
			if dryRun {
				if zp != "" || size <= work.MaxFolderBytes {
					logf("would zip %s", key)
				}
				continue
			}
			if zp != "" {
				// The first sight of a zip beside loose files changes nothing: another
				// machine may be zipping or unzipping it, its writes still on their way.
				rec, act := seen[key].next(bothState(dir, id), now)
				if !act {
					seen[key], seenDirty, sawNow[key] = rec, true, true
					continue
				}
				delete(seen, key) // settled below, or remembered as skipped
				seenDirty = true
				err := convzip.DropHeld(dir, id) // the zip stays
				if err == nil {
					if _, ok := skipped[key]; ok {
						delete(skipped, key)
						dirty = true
					}
					continue
				}
				if errors.Is(err, convzip.ErrNotHeld) {
					err = unzip(dir, id)
				}
				switch {
				case errors.Is(err, convzip.ErrDiverged):
					logf("WARN: %s: %v; the zip and the loose copy beside it are both kept", id, err)
					remember()
					continue
				case errors.Is(err, convzip.ErrNotRemoved):
					logf("WARN: %s: %v; the zip is kept, and the loose files left stay beside it", id, err)
					remember()
					continue
				case errors.Is(err, os.ErrNotExist) && zippedCopy(dest, bucket, id) == "":
					logf("WARN: %s: the zip went while it was read; the loose files beside it are kept", id)
					remember()
					continue
				case err != nil:
					logf("WARN: %s: not unzipped to zip again; the zip and the loose files beside it are kept: %v", id, err)
					remember()
					continue
				}
			}
			res, err := convzip.Zip(dir, id)
			switch {
			case errors.Is(err, convzip.ErrTooLarge):
				logf("ZIP: %s is over %d MB; it stays loose", key, work.MaxFolderBytes>>20)
				remember()
			case errors.Is(err, convzip.ErrZipExists): // zipped elsewhere since the listing: next run
			case err != nil && res.Files > 0: // written and checked; removing the loose files failed
				logf("WARN: %s: zipped, but loose files stay beside the zip: %v", id, err)
				remember()
			case err != nil:
				logf("WARN: %s: not zipped; it stays loose: %v", id, err)
				remember()
			default:
				zipped, raw, packed = zipped+1, raw+res.Raw, packed+res.Packed
				if _, ok := skipped[key]; ok {
					delete(skipped, key)
					dirty = true
				}
			}
		}
	}
	if dirty {
		data, _ := json.MarshalIndent(skipped, "", "  ")
		if err := archive.WriteFileAtomic(zipSkippedPath(root), append(data, '\n'), 0o644); err != nil {
			logf("WARN: %s: %v", zipSkippedPath(root), err)
		}
	}
	// A pair not reached this run is kept only while it stands as it was seen.
	for key, e := range seen {
		bucket, id, _ := strings.Cut(key, "/")
		if !sawNow[key] && (!archive.ValidName(bucket) || !claude.IsSessionID(id) ||
			bothState(filepath.Join(archive.ProjectsDir(dest), bucket), id) != e.State) {
			delete(seen, key)
			seenDirty = true
		}
	}
	if seenDirty && !dryRun {
		data, _ := json.MarshalIndent(seen, "", "  ")
		if err := archive.WriteFileAtomic(bothSeenPath(root), append(data, '\n'), 0o644); err != nil {
			logf("WARN: %s: %v", bothSeenPath(root), err)
		}
	}
	return zipped, raw, packed
}

// removeStaleExtracts removes the folders of zips unpacked in the OS temp folder that a
// killed run left behind, nothing in them touched for an hour: no push, read or check
// holds one that long, and Windows never clears its temp folder. Only the names this
// program's own MkdirTemp calls make, only folders this user owns, and a link is never
// followed.
func removeStaleExtracts(now time.Time) {
	tmp := os.TempDir()
	ents, _ := os.ReadDir(tmp)
	for _, e := range ents {
		if !e.IsDir() || !extractName(e.Name()) {
			continue
		}
		p := filepath.Join(tmp, e.Name())
		if info, err := e.Info(); err == nil && ownedByMe(info) && untouchedSince(p, now.Add(-time.Hour)) {
			os.RemoveAll(p)
		}
	}
}

// extractName reports a name os.MkdirTemp makes from one of this program's patterns
// for unpacked zips: the pattern, then digits.
func extractName(n string) bool {
	for _, pattern := range []string{"convzip-verify-", "convzip-", "claude-sessions-push-", "cs-zip"} {
		if rest, ok := strings.CutPrefix(n, pattern); ok && rest != "" && strings.Trim(rest, "0123456789") == "" {
			return true
		}
	}
	return false
}

// untouchedSince reports whether root and everything under it, links not followed, is
// no newer than cutoff. A tree too large to walk quickly, or with an entry it cannot
// stat, counts as touched.
func untouchedSince(root string, cutoff time.Time) bool {
	n, untouched := 0, true
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		n++
		var info os.FileInfo
		if err == nil {
			info, err = d.Info()
		}
		if err != nil || n > 10000 || info.ModTime().After(cutoff) {
			untouched = false
			return filepath.SkipAll
		}
		return nil
	})
	return untouched
}

// looseSize totals the regular files of id's loose copy in dir, its transcript and
// folder, and reports whether none changed after cutoff; a time in the future is after
// it. One it cannot read is not idle.
func looseSize(dir, id string, cutoff time.Time) (size int64, idle bool) {
	idle = true
	for _, top := range []string{filepath.Join(dir, id+".jsonl"), filepath.Join(dir, id)} {
		filepath.WalkDir(top, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				if p != top || !errors.Is(err, os.ErrNotExist) {
					idle = false
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				idle = false
				return nil
			}
			size += info.Size()
			idle = idle && !info.ModTime().After(cutoff)
			return nil
		})
	}
	return size, idle
}

// copyBytes is how zip-skipped.json knows a copy again: the bytes of its loose files and
// of its zip, as they are now.
func copyBytes(dir, id string) int64 {
	n, _ := looseSize(dir, id, time.Time{})
	if st, err := os.Lstat(convzip.Path(dir, id)); err == nil {
		n += st.Size()
	}
	return n
}

// heldStat is a local transcript as push found it, held in whole by the archived
// copy in another bucket, In, as that copy then was.
type heldStat struct {
	Size    int64     `json:"size"`
	MTime   time.Time `json:"mtime"`
	In      string    `json:"in"`
	InSize  int64     `json:"inSize"`
	InMTime time.Time `json:"inMtime"`
}

// heldRecord remembers, by "bucket/id", the transcripts push leaves to another
// bucket's copy, so each is compared again only once it changes: comparing reads both
// copies, one of them on a cloud drive. Nothing in it comes from the archive.
type heldRecord struct {
	m     map[string]heldStat
	dirty bool
	tmp   string // where covers unpacks zipped copies; "" for the OS temp folder
}

func heldPath(root string) string {
	return filepath.Join(root, "session-sync-base", "held.json")
}

// loadHeld reads the record. Missing or unreadable is empty: push then compares again.
func loadHeld(root string) *heldRecord {
	h := &heldRecord{}
	if raw, err := os.ReadFile(heldPath(root)); err == nil {
		_ = json.Unmarshal(raw, &h.m)
	}
	if h.m == nil {
		h.m = map[string]heldStat{}
	}
	return h
}

func (h *heldRecord) save(root string) error {
	if !h.dirty {
		return nil
	}
	data, err := json.MarshalIndent(h.m, "", "  ")
	if err != nil {
		return err
	}
	return archive.WriteFileAtomic(heldPath(root), append(data, '\n'), 0o644)
}

// hit reports whether the transcript and the copy that held it are both unchanged
// since push found it held. A copy that changed - replaced by a fork, say - is compared
// again.
func (h *heldRecord) hit(dest, bucket, id string, src os.FileInfo) bool {
	if !h.known(bucket, id, src) {
		return false
	}
	s := h.m[bucket+"/"+id]
	size, mtime, ok := copyStat(dest, s.In, id)
	return ok && size == s.InSize && archive.SameMTime(mtime, s.InMTime)
}

// leftTo reports whether the transcript is unchanged since push left it to a copy that
// is still archived. That copy may have grown since: it is only replaced by one with
// more.
func (h *heldRecord) leftTo(dest, bucket, id string, src os.FileInfo) bool {
	if !h.known(bucket, id, src) {
		return false
	}
	in := h.m[bucket+"/"+id].In
	return archivedCopy(dest, in, id) != "" || zippedCopy(dest, in, id) != ""
}

// known reports whether the transcript is unchanged since push found it held: its
// lines were looked through for work folders then.
func (h *heldRecord) known(bucket, id string, src os.FileInfo) bool {
	if h == nil {
		return false
	}
	s, ok := h.m[bucket+"/"+id]
	return ok && s.Size == src.Size() && archive.SameMTime(s.MTime, src.ModTime())
}

// covers reports whether one of the others holds every turn of the transcript at path,
// and everything in its own folder, and records which.
func (h *heldRecord) covers(dest, bucket, id, path string, src os.FileInfo, others []string) bool {
	if h == nil {
		return false
	}
	key := bucket + "/" + id
	if _, ok := h.m[key]; ok {
		delete(h.m, key)
		h.dirty = true
	}
	for _, other := range others {
		size, mtime, ok := copyStat(dest, other, id)
		if !ok {
			continue
		}
		// A zipped copy is compared unpacked, its folder beside it; a loose one in place.
		p, done, err := materialize(filepath.Join(archive.ProjectsDir(dest), other), id, h.tmp)
		if err != nil {
			continue
		}
		// The folder too: a copy archived by a build that left subagents behind
		// lacks this one's.
		r := claude.Compare(path, p)
		held := (r == claude.Same || r == claude.Behind) &&
			folderHeld(filepath.Join(filepath.Dir(path), id), filepath.Join(filepath.Dir(p), id)) == nil
		done()
		if held {
			h.m[key] = heldStat{Size: src.Size(), MTime: src.ModTime(), In: other, InSize: size, InMTime: mtime}
			h.dirty = true
			return true
		}
	}
	return false
}

// archivedCopy is the archived transcript of session id in bucket, "" unless it is a
// plain file there: never one read through a link planted in the shared archive.
func archivedCopy(dest, bucket, id string) string {
	if !archive.ValidName(bucket) || !archive.ValidName(id) {
		return ""
	}
	p := filepath.Join(archive.ProjectsDir(dest), bucket, id+".jsonl")
	if st, err := os.Lstat(p); err != nil || !st.Mode().IsRegular() {
		return ""
	}
	return p
}

// zippedCopy is the zip of session id in bucket, "" unless it is a plain file there by
// its exact name: a sync client's conflict copy is not one.
func zippedCopy(dest, bucket, id string) string {
	if !archive.ValidName(bucket) || !claude.IsSessionID(id) {
		return ""
	}
	p := convzip.Path(filepath.Join(archive.ProjectsDir(dest), bucket), id)
	if st, err := os.Lstat(p); err != nil || !st.Mode().IsRegular() {
		return ""
	}
	return p
}

// copyStat is the size and time of bucket's archived copy of id: its loose transcript's,
// or else its zip file's own, so a held copy is checked again without reading any zip.
func copyStat(dest, bucket, id string) (int64, time.Time, bool) {
	p := archivedCopy(dest, bucket, id)
	if p == "" {
		p = zippedCopy(dest, bucket, id)
	}
	if st, err := os.Lstat(p); p != "" && err == nil {
		return st.Size(), st.ModTime(), true
	}
	return 0, time.Time{}, false
}

// zipHolds reports whether the zip, by its index, holds the local transcript at path and
// every file of its folder at their sizes and times: what a loose copy is judged by.
func zipHolds(zp, id, path string, src os.FileInfo) bool {
	ents, err := convzip.Entries(zp)
	if err != nil {
		return false
	}
	same := func(name string, st os.FileInfo) bool {
		e, ok := ents[name]
		return ok && e.Size == st.Size() && archive.SameMTime(e.ModTime, st.ModTime())
	}
	if !same(id+".jsonl", src) {
		return false
	}
	dir := filepath.Join(filepath.Dir(path), id)
	for _, rel := range claude.SessionFiles(dir) {
		if st, err := os.Stat(filepath.Join(dir, rel)); err == nil && !same(id+"/"+filepath.ToSlash(rel), st) {
			return false
		}
	}
	return true
}

// sessionHolders lists, by lower-cased session id, the archive buckets holding a
// transcript of it, loose or zipped. A bucket it cannot list is left out.
func sessionHolders(dest string) map[string][]string {
	out, _ := holdersOf(dest)
	return out
}

// holdersOf is sessionHolders, and the first error listing the archive or a bucket in
// it: prune must not delete on a view that may leave out a copy.
func holdersOf(dest string) (map[string][]string, error) {
	out := map[string][]string{}
	names, failed := archive.BucketNames(dest)
	for _, b := range names {
		entries, err := os.ReadDir(filepath.Join(archive.ProjectsDir(dest), b))
		if err != nil && failed == nil {
			failed = fmt.Errorf("a folder in the archive cannot be listed, and may hold a newer copy: %w", err)
		}
		loose, zipped := map[string]bool{}, []string{}
		for _, e := range entries {
			if !e.Type().IsRegular() {
				continue
			}
			if id, ok := strings.CutSuffix(e.Name(), ".jsonl"); ok && claude.IsSessionID(id) {
				out[strings.ToLower(id)] = append(out[strings.ToLower(id)], b)
				loose[strings.ToLower(id)] = true
			} else if id, ok := strings.CutSuffix(e.Name(), convzip.Suffix); ok && claude.IsSessionID(id) {
				zipped = append(zipped, strings.ToLower(id))
			}
		}
		// A zip beside its loose transcript is the same copy: the bucket is listed once.
		for _, id := range zipped {
			if !loose[id] {
				out[id] = append(out[id], b)
			}
		}
	}
	return out, failed
}

// othersHolding is the buckets other than bucket that hold session id.
func othersHolding(holders map[string][]string, bucket, id string) []string {
	var out []string
	for _, b := range holders[strings.ToLower(id)] {
		if !strings.EqualFold(b, bucket) {
			out = append(out, b)
		}
	}
	return out
}

// archiveExtras copies a session's own folder - subagent transcripts and saved tool
// output - where it changed, reporting how many files it copied and left alone.
func archiveExtras(bucketDir, destBucket, id string, extras []string, dryRun bool, logf func(string, ...any)) (copied, unchanged int) {
	// From the bucket folder down, a link planted in the shared archive would take the
	// copies outside it: none is made, nor any folder created.
	for _, rel := range extras {
		if !archive.ThroughRealDirs(filepath.Dir(destBucket), filepath.Join(destBucket, id, rel)) {
			logf("WARN: %s: a link or a file stands where a folder should be on the way to %s/%s in the archive; its folder is not archived",
				id, filepath.Base(destBucket), id)
			return 0, 0
		}
	}
	for _, rel := range extras {
		srcPath := filepath.Join(bucketDir, id, rel)
		srcInfo, err := os.Stat(srcPath)
		if err != nil {
			continue
		}
		target := filepath.Join(destBucket, id, rel)
		if !archive.NeedsCopy(srcInfo, archive.StatOrNil(target)) {
			unchanged++
			continue
		}
		if !dryRun {
			if err := archive.CopyFile(srcPath, target); err != nil {
				logf("WARN: %s/%s: %v", id, rel, err)
				continue
			}
		}
		copied++
	}
	return copied, unchanged
}

// quickSize and quickEvery throttle the per-reply backup. A sync client uploads a
// whole file each time it changes, and may keep every upload as a version that counts
// against the account's storage, so a transcript over quickSize is copied at most
// every quickEvery - the sweep's own interval; whatever is left is caught by the next
// reply, the sweep or the end of the session.
const (
	quickSize  = 5 << 20
	quickEvery = 30 * time.Minute
)

// pushOne archives the one session a Stop hook names, without the manifest, index or
// pages: it runs after every reply, and the next full push brings those up to date.
func pushOne(root, dest, transcript string, dryRun bool, logf func(string, ...any)) error {
	// The path comes from the hook's input. Only a transcript directly inside one of
	// this profile's buckets is ever read.
	abs, err := filepath.Abs(transcript)
	if err != nil || !strings.EqualFold(filepath.Ext(abs), ".jsonl") ||
		!sameDir(filepath.Dir(filepath.Dir(abs)), claude.ProjectsDir(root)) {
		logf("SKIP: hook named %q, which is not a transcript in %s", transcript, claude.ProjectsDir(root))
		return nil
	}
	bucketDir := filepath.Dir(abs)
	bucket := filepath.Base(bucketDir)
	id := strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
	srcInfo, err := os.Lstat(abs)
	if err != nil || !srcInfo.Mode().IsRegular() || !archive.ValidName(bucket) || !archive.ValidName(id) {
		return nil
	}

	// Throttled against whichever copy this machine wrote last: the archived one, or
	// for a forked session its fork.
	destBucket := filepath.Join(archive.ProjectsDir(dest), bucket)
	if srcInfo.Size() > quickSize {
		for _, copied := range []string{filepath.Join(destBucket, id+".jsonl"), forkPath(dest, bucket, id, machineName())} {
			if c := archive.StatOrNil(copied); c != nil && srcInfo.ModTime().Sub(c.ModTime()) < quickEvery {
				return nil
			}
		}
	}
	repointed := loadRepointed(root)
	was := len(repointed)
	zips := loadZipCompared(root)
	zips.hook = true
	// Only one with no copy here can be pruned from the archive, and not continued here
	// since: the list is read for no other, as this runs after every reply.
	if archive.StatOrNil(filepath.Join(destBucket, id+".jsonl")) == nil && zippedCopy(dest, bucket, id) == "" {
		if d, err := archive.LoadDeleted(dest); err == nil && prunedHere(d, id, abs) {
			return nil
		}
	}
	// Not compared with other buckets' copies: a session still replying here holds
	// turns no other copy has. The next full push judges one that does not.
	switch archiveTranscript(dest, bucket, id, abs, srcInfo, pushedBefore(dest, machineName()), repointed, nil, nil, zips, dryRun, logf) {
	case copiedOver, keptFork:
		archiveExtras(bucketDir, destBucket, id, claude.SessionFiles(filepath.Join(bucketDir, id)), dryRun, logf)
	}
	if len(repointed) != was && !dryRun {
		if err := saveRepointed(root, repointed); err != nil {
			logf("WARN: %s: %v", repointedPath(root), err)
		}
	}
	if !dryRun {
		if err := zips.save(root); err != nil {
			logf("WARN: %s: %v", zipComparedPath(root), err)
		}
	}
	return nil
}

// machineMemoryDir is one machine's own copy of a project's memory in the archive.
func machineMemoryDir(dest, machine, bucket string) string {
	return filepath.Join(dest, "memory", machine, bucket)
}

func userHome() string {
	h, _ := os.UserHomeDir()
	return h
}

// sweepMark ends the PUSH line of the scheduled sweep. Every other run - a hook, a
// terminal - holds a terminal's privacy grants, which the background sweep may lack,
// so only the sweep's success says it can reach the archive.
const sweepMark = " [sweep]"

// pushedBefore is what this machine's last push recorded about each session, keyed
// "bucket/id": its local size and time then.
func pushedBefore(dest, machine string) map[string]archive.Session {
	out := map[string]archive.Session{}
	raw, err := os.ReadFile(filepath.Join(archive.IndexDir(dest), machine+".json"))
	if err != nil {
		return out
	}
	var idx archive.SessionIndex
	if json.Unmarshal(raw, &idx) == nil {
		for _, s := range idx.Sessions {
			out[s.Bucket+"/"+s.ID] = s
		}
	}
	return out
}

// changedSincePush reports whether a local transcript changed since this machine last
// pushed it. One that did not cannot have forked here, and is not worth reading.
func changedSincePush(pushed map[string]archive.Session, bucket, id string, src os.FileInfo) bool {
	s, ok := pushed[bucket+"/"+id]
	return !ok || s.Size != src.Size() || !withinTolerance(s.Updated, src.ModTime())
}

func sameStat(a, b os.FileInfo) bool {
	return a.Size() == b.Size() && withinTolerance(a.ModTime(), b.ModTime())
}

func withinTolerance(a, b time.Time) bool {
	d := a.Sub(b)
	return d <= archive.MTimeToleranceSeconds*time.Second && d >= -archive.MTimeToleranceSeconds*time.Second
}

// forkPath is where a session continued both here and on another machine keeps this
// machine's copy, beside the archived one rather than over it. It is outside
// projects/ so nothing mistakes it for a session of its own.
func forkPath(dest, bucket, id, machine string) string {
	return filepath.Join(dest, "forks", bucket, id, machine+".jsonl")
}

// keepFork archives this machine's copy of a forked session, reporting whether it
// wrote anything.
func keepFork(dest, bucket, id, src string, srcInfo os.FileInfo, dryRun bool, logf func(string, ...any)) bool {
	path := forkPath(dest, bucket, id, machineName())
	existing := archive.StatOrNil(path)
	if !archive.NeedsCopy(srcInfo, existing) {
		return false
	}
	if !dryRun {
		if err := archive.CopyFile(src, path); err != nil {
			logf("WARN: %s: fork: %v", id, err)
			return false
		}
	}
	if existing == nil && !dryRun { // said once, not after every reply that keeps it current
		logf("FORK: %s was continued here and on another machine; the archived copy is kept and this one is saved as forks/%s/%s/%s.jsonl",
			id, bucket, id, machineName())
	}
	return true
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
	// An entry the PowerShell tool wrote is read back marked as such; written out
	// again, it keeps the machine it named - otherwise the name is gone after one push.
	for bucket, p := range merged {
		p.Machine = p.Owner()
		merged[bucket] = p
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
	rows = withoutPruned(dest, rows)

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

// safeMachine makes a machine name usable as a file name.
var safeMachine = strings.NewReplacer("/", "-", `\`, "-", ":", "-", " ", "-", "*", "-", "?", "-")

// savedMachine is the name saved in session-sync.json, and formerMachines the names
// this machine went by before it, loaded by resolveRoot: every command finds its
// profile through it before it names anything. machineMu guards both: the panel runs
// commands from concurrent requests. Write them with setSavedMachine, read the names
// with savedFormer. It is one value for the whole process, so the panel assumes one
// Claude profile per process.
var (
	savedMachine   string
	formerMachines []string
	machineMu      sync.RWMutex
)

func setSavedMachine(name string, former []string) {
	machineMu.Lock()
	defer machineMu.Unlock()
	savedMachine, formerMachines = name, former
}

// savedFormer is a copy of the names this machine went by before.
func savedFormer() []string {
	machineMu.RLock()
	defer machineMu.RUnlock()
	return slices.Clone(formerMachines)
}

// isMe reports whether name is this machine's, now or under a name it went by before.
func isMe(name string) bool {
	for _, n := range append([]string{machineName()}, savedFormer()...) {
		if strings.EqualFold(name, n) {
			return true
		}
	}
	return false
}

// pinMachineName saves the name a machine first pushed under. Computed each time, it
// changed with the hostname - macOS renames itself after a clash on the network -
// and the machine then took its own files in the archive for another machine's.
func pinMachineName(root string) {
	machineMu.RLock()
	pinned := savedMachine != ""
	machineMu.RUnlock()
	if pinned || strings.TrimSpace(os.Getenv("CLAUDE_SESSIONS_MACHINE")) != "" {
		return
	}
	cfg, err := archive.LoadConfig(root)
	if err != nil {
		return // a file it cannot read is not overwritten
	}
	cfg.Machine = machineName()
	if archive.SaveConfig(root, cfg) == nil {
		setSavedMachine(cfg.Machine, savedFormer())
	}
}

// installID is this install's ID from session-sync.json, made of 16 random bytes and
// saved there on first use. "" when the file cannot be read or saved: a new ID every
// push would read as another computer each time.
func installID(root string) string {
	cfg, err := archive.LoadConfig(root)
	if err != nil {
		return ""
	}
	if cfg.InstallID != "" {
		return cfg.InstallID
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	cfg.InstallID = hex.EncodeToString(b)
	if archive.SaveConfig(root, cfg) != nil {
		return ""
	}
	return cfg.InstallID
}

// nameClashMessage is what push logs and doctor fails with, given the name.
const nameClashMessage = "Another computer also uses the name %s. Give one of them its own name with `claude-sessions config set-machine`."

// nameClash reports whether the archive's shard for machine was written by another
// install than id: two computers pushing under one name.
func nameClash(dest, machine, id string) bool {
	s := readShard(dest, machine)
	return s.InstallID != "" && s.InstallID != id
}

// machineName identifies the writer of a manifest shard.
//
// It includes the username, not just the hostname. Two accounts on one machine each
// have their own ~/.claude and their own sessions, but share a hostname - so keying
// on the hostname alone means they overwrite each other's shard, and each push
// silently discards the other user's project identities.
func machineName() string {
	// A name the user pinned with `config set-machine`: a macOS hostname changes with
	// the network, and a machine under a new name is taken for another one - its own
	// forks come back to it. The environment variable is for tests, which run several
	// "machines" side by side; it reaches no scheduled sweep.
	safe := safeMachine
	if v := strings.TrimSpace(os.Getenv("CLAUDE_SESSIONS_MACHINE")); v != "" {
		return safe.Replace(v)
	}
	machineMu.RLock()
	saved := savedMachine
	machineMu.RUnlock()
	if saved != "" {
		return safe.Replace(saved)
	}

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
	return safe.Replace(name)
}

// maxLogBytes is when session-sync.log is rotated to session-sync.log.1.
const maxLogBytes = 1 << 20

// newLogger returns a printf-style logger that appends to session-sync.log, and writes
// each line to progress too unless quiet.
//
// The log is the only record of what an unattended hook or sweep did. It never
// returns an error: failing to log must not fail a push.
func newLogger(root string, quiet bool, progress io.Writer) func(string, ...any) {
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
			fmt.Fprint(progress, line)
		}
	}
}
