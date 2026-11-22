package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/hostagent"
	"github.com/ashiqfardus/claude-sessions-sync/internal/render"
	"github.com/ashiqfardus/claude-sessions-sync/internal/work"
)

type level string

const (
	levelOK   level = "ok"
	levelWarn level = "warn"
	levelFail level = "fail"
	levelInfo level = "info"
)

type check struct {
	Name   string `json:"name"`
	Level  level  `json:"level"`
	Detail string `json:"detail"`
	Advice string `json:"advice,omitempty"`
}

// errChecksFailed makes doctor usable from a monitoring script: the process exits
// non-zero when something is actually broken. main prints nothing extra for it,
// because the report has already said everything useful.
var errChecksFailed = errors.New("doctor found failures")

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	archiveDir := fs.String("archive", "", "override the synced destination folder")
	asJSON := fs.Bool("json", false, "machine-readable output")
	noProbe := fs.Bool("no-write-probe", false, "skip the archive write test (which creates and deletes one file)")
	exclude := fs.String("exclude", "", "additional render exclusions to take into account, on top of the config")
	if err := parseFlags(fs, args, false); err != nil {
		return err
	}

	var checks []check
	add := func(c check) { checks = append(checks, c) }

	// --- Claude's own state ---------------------------------------------------
	root, rootSrc, err := resolveRoot(*claudeDir)
	if err != nil {
		return err
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		add(check{"claude root", levelFail, root + " does not exist",
			"Is Claude Code installed for this account? Set CLAUDE_CONFIG_DIR if it lives elsewhere."})
		return report(checks, *asJSON)
	}
	add(check{"claude root", levelOK, fmt.Sprintf("%s (%s)", root, rootSrc), ""})

	// A pinned bucket name changes what the identity machinery can assume, so say so
	// rather than letting it surprise someone later.
	if v := strings.TrimSpace(os.Getenv("CLAUDE_CODE_PROJECT_DIR_NAME")); v != "" {
		add(check{"bucket naming", levelInfo,
			"CLAUDE_CODE_PROJECT_DIR_NAME=" + v,
			"Bucket names are pinned by this variable, so they are not path slugs and must not be compared as such."})
	}

	buckets, err := claude.ListBuckets(claude.ProjectsDir(root))
	if err != nil {
		add(check{"projects", levelFail, err.Error(), ""})
		return report(checks, *asJSON)
	}
	var transcripts, memoryOnly int
	for _, b := range buckets {
		transcripts += len(b.Transcripts)
		if !b.HasTranscripts() && len(b.Memory) > 0 {
			memoryOnly++
		}
	}
	add(check{"projects", levelOK,
		fmt.Sprintf("%d bucket(s), %d transcript(s)", len(buckets), transcripts), ""})

	// --- retention ------------------------------------------------------------
	settings, found, err := claude.LoadSettings(claude.SettingsPath(root))
	switch {
	case err != nil:
		add(check{"settings.json", levelWarn, "could not parse: " + err.Error(),
			"Retention and hook checks below are unreliable until this parses."})
	case !found:
		add(check{"settings.json", levelInfo, "not present", ""})
	}
	days := settings.EffectiveCleanupPeriodDays()
	switch {
	case settings.CleanupPeriodDays == nil:
		add(check{"retention", levelWarn,
			fmt.Sprintf("cleanupPeriodDays unset, so Claude Code applies its %d-day default", claude.DefaultCleanupPeriodDays),
			"Local transcripts are deleted a month after they are written. Raise it in settings.json."})
	case days < 90:
		add(check{"retention", levelWarn, fmt.Sprintf("cleanupPeriodDays = %d", days),
			"Transcripts age out locally. The archive is your only copy after that."})
	default:
		add(check{"retention", levelOK, fmt.Sprintf("cleanupPeriodDays = %d", days), ""})
	}

	// --- the archive ----------------------------------------------------------
	dest, src, err := archive.ResolveDestination(root, *archiveDir)
	if err != nil {
		add(check{"archive", levelWarn, err.Error(), ""})
	}
	if dest == "" {
		add(check{"archive", levelFail, "no destination configured and none auto-detected",
			"Run `claude-sessions config set-destination <folder>` to choose one."})
		return report(checks, *asJSON)
	}
	st, err := os.Stat(dest)
	switch {
	case err != nil:
		add(check{"archive", levelFail, fmt.Sprintf("%s (%s) is not reachable", dest, src),
			"Sync client offline or not mounted? Nothing is being backed up right now."})
		return report(checks, *asJSON)
	case !st.IsDir():
		add(check{"archive", levelFail, dest + " is not a directory", ""})
		return report(checks, *asJSON)
	}
	add(check{"archive", levelOK, fmt.Sprintf("%s (%s)", dest, src), ""})

	// A read-only mount, an exhausted quota or a permissions problem passes every
	// other check while backing up precisely nothing.
	if *noProbe {
		add(check{"archive writable", levelInfo, "not tested (--no-write-probe)", ""})
	} else if err := archive.Writable(dest); err != nil {
		add(check{"archive writable", levelFail, err.Error(),
			"The archive cannot be written to, so no session can be saved there."})
	}

	if src == archive.SourceDetected {
		add(check{"archive config", levelInfo, "destination is auto-detected, not saved",
			fmt.Sprintf("Run `claude-sessions config set-destination %q` so every run agrees.", dest)})
	}

	if mixed := otherVersions(dest); len(mixed) > 0 {
		add(check{"versions", levelWarn,
			fmt.Sprintf("other machines run a different version than this one (%s): %s", version, strings.Join(mixed, ", ")),
			"Mixed versions skip features without saying so - an older build pushes no rules or skills. Install the same release everywhere, or `claude-sessions machines --forget <name>` for a machine no longer in use."})
	}

	if diffs := profileDiffs(dest); len(diffs) > 0 {
		add(check{"profile", levelInfo,
			fmt.Sprintf("%d rule/skill file(s) differ between machines: %s", len(diffs), profileSummary(diffs)),
			"`import --update-profile` takes the newest. Every machine's own copy stays in the archive under profile/machines/."})
	}

	// --- manifest drift -------------------------------------------------------
	// The check that matters: a bucket present in the archive but absent from the
	// manifest cannot be identity-matched, so `import` on a new machine will not
	// place it anywhere. Its transcripts are backed up but unroutable.
	manifest, badShards, err := archive.Merged(dest)
	if err != nil {
		add(check{"manifest", levelWarn, err.Error(), ""})
	}
	for _, b := range badShards {
		add(check{"manifest", levelWarn, "ignored manifest file: " + b,
			"It is unreadable, or a sync client's conflict copy of another machine's file. Other machines' files are still merged; delete this one once you have checked it."})
	}

	archived, err := archive.BucketNames(dest)
	if err != nil {
		add(check{"manifest", levelWarn, err.Error(), ""})
	}
	var unroutable []string
	for _, name := range archived {
		p, ok := manifest[name]
		if !ok || !p.Routable() {
			unroutable = append(unroutable, name)
		}
	}
	switch {
	case len(archived) == 0:
		add(check{"manifest", levelInfo, "the archive holds no projects yet", ""})
	case len(unroutable) > 0:
		add(check{"manifest", levelWarn,
			fmt.Sprintf("%d of %d archived bucket(s) have no usable identity: %s",
				len(unroutable), len(archived), strings.Join(unroutable, ", ")),
			"Identity is read from a transcript's cwd, so a bucket holding only memory files records nothing. `import` cannot place these."})
	default:
		add(check{"manifest", levelOK, fmt.Sprintf("%d project(s) recorded, all routable", len(manifest)), ""})
	}

	if _, err := os.Stat(archive.ManifestDir(dest)); os.IsNotExist(err) && len(archived) > 0 {
		add(check{"manifest format", levelInfo,
			"flat projects.json only (written by the PowerShell tools)",
			"Sharded manifest/<machine>.json arrives with `push`; until then concurrent pushes from two machines can drop entries."})
	}

	// Rendering is a user-visible feature that can quietly fall behind.
	excludePatterns := archive.RenderExcludes(root, dest, splitList(*exclude), false)
	if len(excludePatterns) > 0 {
		// A pattern that matches nothing publishes what the user believed was
		// withheld, so show what is configured and what it actually caught.
		add(check{"html excluded", levelInfo,
			fmt.Sprintf("not published as pages: %s", strings.Join(excludePatterns, ", ")),
			"Check these match the projects you meant; a pattern that matches nothing silently publishes them."})
	}
	if pages, transcripts, newestStale := renderState(dest, manifest, excludePatterns); transcripts > 0 {
		switch {
		case pages == 0 && len(excludePatterns) > 0:
			add(check{"html", levelInfo, "no pages, and every project is excluded", ""})
		case pages == 0:
			add(check{"html", levelWarn, "no pages rendered yet",
				"Run `claude-sessions render` to make the archive readable on a phone."})
		case newestStale:
			add(check{"html", levelWarn,
				fmt.Sprintf("%d page(s) for %d transcript(s), and the newest session has no current page", pages, transcripts),
				"Run `claude-sessions render`."})
		default:
			add(check{"html", levelOK, fmt.Sprintf("%d page(s) for %d transcript(s)", pages, transcripts), ""})
		}
	}

	// Global rules and skills: without them another machine resumes the sessions but
	// not the way Claude was told to work.
	if len(profileFiles(root)) > 0 {
		if n := len(profileFiles(profileDir(dest))); n == 0 {
			add(check{"profile", levelWarn, "global rules and skills are not archived yet", "Run `claude-sessions push`."})
		} else {
			add(check{"profile", levelOK, fmt.Sprintf("%d rule/skill file(s) archived", n), ""})
		}
	}

	// Work folders: what this machine's shard says is archived outside git repos.
	if cfg, _ := archive.LoadConfig(root); !cfg.WorkOn() {
		add(check{"work folders", levelInfo, "off", "`claude-sessions config work on` backs up the folders Claude worked in outside git."})
	} else {
		folders := readShard(dest, machineName()).WorkFolders
		var files, packed int
		var bytes int64
		var skipped []string
		for _, f := range folders {
			files += f.Files
			packed += f.Packed
			bytes += f.Bytes
			if f.Skipped != "" {
				skipped = append(skipped, f.Path+" ("+f.Skipped+")")
			}
		}
		sum := fmt.Sprintf("%d folder(s), %d file(s)", len(folders), files)
		if packed > 0 {
			sum += fmt.Sprintf(", %d packed unit(s)", packed)
		}
		switch {
		case len(skipped) > 0:
			add(check{"work folders", levelWarn, "not backed up: " + strings.Join(skipped, ", "),
				"A subfolder over 250 MB is not archived; the rest of the folder is. Keep it out of the way with `claude-sessions config exclude-folder <folder>`, or move the big files out."})
		case bytes > work.WarnBytes:
			add(check{"work folders", levelWarn, fmt.Sprintf("%s, %d MB archived", sum, bytes>>20),
				"The archive is growing large. Drop a folder you do not need with `claude-sessions config exclude-folder <folder>`."})
		default:
			add(check{"work folders", levelOK, sum, ""})
		}
	}

	if memoryOnly > 0 {
		add(check{"local buckets", levelInfo,
			fmt.Sprintf("%d local bucket(s) hold memory but no transcripts", memoryOnly), ""})
	}

	// --- automation -----------------------------------------------------------
	// Classify carefully. "sync-claude-sessions.ps1" CONTAINS "claude-sessions", so
	// matching that substring counts the PowerShell script as this binary too and
	// reports two archivers when only one is installed. Identify the script first,
	// and count as this binary only a hook that actually runs `claude-sessions push`.
	psHooks := settings.SessionEndHooks("sync-claude-sessions")
	var goHooks []claude.Hook
	for _, h := range settings.SessionEndHooks("claude-sessions") {
		if ours, _ := claude.IsArchiveHook(h.Command, h.Args); ours {
			goHooks = append(goHooks, h)
		}
	}
	switch {
	case len(goHooks) > 0 && len(psHooks) > 0:
		add(check{"session hook", levelWarn,
			"two archivers installed: this binary and the PowerShell script",
			"Both will run at the end of every session, doubling the work and racing on the manifest. Uninstall one."})
	case len(goHooks)+len(psHooks) > 1:
		add(check{"session hook", levelWarn,
			fmt.Sprintf("%d duplicate SessionEnd hooks installed", len(goHooks)+len(psHooks)),
			"Each one runs on every session end. Keep one."})
	case len(goHooks)+len(psHooks) == 1:
		h := append(goHooks, psHooks...)[0]
		cmd := h.Command
		if len(h.Args) > 0 {
			cmd += " " + strings.Join(h.Args, " ")
		}
		// A hook whose program is gone fails at every session end, silently.
		if len(goHooks) == 1 && filepath.IsAbs(h.Command) {
			if _, err := os.Stat(h.Command); err != nil {
				add(check{"session hook", levelFail, "the hook's program is missing: " + h.Command,
					"Re-run `claude-sessions install`."})
				break
			}
		}
		add(check{"session hook", levelOK, shorten(cmd, 96), ""})
	default:
		add(check{"session hook", levelWarn, "no SessionEnd hook installed",
			"Sessions are only archived by the periodic sweep, so the most recent one can be missed."})
	}

	// The Stop hook arrived later than SessionEnd: an older install archives only when
	// a session ends and on the sweep, so up to half an hour of work is at risk.
	perReply := false
	for _, h := range settings.HooksFor("Stop", "claude-sessions") {
		if ours, _ := claude.IsArchiveHook(h.Command, h.Args); ours {
			perReply = true
		}
	}
	if len(goHooks) > 0 && !perReply {
		add(check{"reply hook", levelInfo, "the per-reply backup is not installed",
			"Run `claude-sessions install` again: it adds a Stop hook that archives the current session after each reply."})
	}

	sweep, err := hostagent.Sweep(sweepJob(root, ""))
	if err != nil {
		add(check{"sweep", levelWarn, err.Error(), ""})
	} else if sweep.Problem != "" {
		add(check{"sweep", levelFail, fmt.Sprintf("%s: %s", sweep.Mechanism, sweep.Detail),
			sweep.Problem + ". Re-run `claude-sessions install` to register it again."})
	} else if sweep.Installed {
		add(check{"sweep", levelOK, fmt.Sprintf("%s: %s", sweep.Mechanism, sweep.Detail), ""})
	} else {
		add(check{"sweep", levelWarn, fmt.Sprintf("%s: %s", sweep.Mechanism, sweep.Detail),
			"A session killed abruptly will not be archived until the next manual push."})
	}

	// --- is anything actually being archived? ---------------------------------
	// The hook and the sweep both exit 0 by design, so a scheduler's "last result"
	// only ever proves that something started. Ask the question directly.
	for _, c := range liveness(root, dest, buckets) {
		add(c)
	}

	// --- secrets --------------------------------------------------------------
	// Auth material inside a cloud-synced folder is a stop-everything finding. Walk
	// the tree: a naive recursive copy puts these inside a bucket, not at the root,
	// where a root-only check would never see them.
	for _, found := range findSecrets(dest) {
		rel, err := filepath.Rel(dest, found)
		if err != nil {
			rel = found
		}
		add(check{"secrets", levelFail, rel + " is present in the archive",
			"Delete it from the synced folder and rotate anything it contained. This tool never copies it; something else did."})
	}

	return report(checks, *asJSON)
}

// archiveLag is how long a finished session may go unarchived before doctor fails:
// the default 30-minute sweep plus ten minutes' grace.
const archiveLag = 40 * time.Minute

// quietPushAge is how long since the last successful push before doctor warns. The
// sweep pushes every 30 minutes whether or not anything changed, so this is several
// missed runs - or a machine that was switched off.
const quietPushAge = 3 * time.Hour

// liveness checks the two things that show the archive is really being kept up to
// date: sessions that ended a while ago are in it, and pushes are succeeding.
func liveness(root, dest string, buckets []claude.Bucket) []check {
	var out []check

	var pending []string
	cutoff := time.Now().Add(-archiveLag)
	for _, b := range buckets {
		for _, t := range b.Transcripts {
			if t.ModTime.After(cutoff) {
				continue // may still be live, or the sweep has not come round yet
			}
			src, err := os.Stat(t.Path)
			if err != nil {
				continue
			}
			dst := archive.StatOrNil(filepath.Join(archive.ProjectsDir(dest), b.Name, filepath.Base(t.Path)))
			// A shorter local copy is a session forked across machines, which push
			// deliberately leaves alone; it is not a failure to archive. Nor is one
			// whose copy is saved as a fork beside the archived one.
			fork := archive.StatOrNil(forkPath(dest, b.Name, t.ID, machineName()))
			if archive.NeedsCopy(src, dst) && (dst == nil || src.Size() >= dst.Size()) && archive.NeedsCopy(src, fork) {
				pending = append(pending, b.Name+"/"+t.ID)
			}
		}
	}
	if len(pending) > 0 {
		out = append(out, check{"archived", levelFail,
			fmt.Sprintf("%d session(s) that ended over %d minutes ago are not in the archive, e.g. %s",
				len(pending), int(archiveLag.Minutes()), pending[0]),
			"Neither the hook nor the sweep has copied them. Run `claude-sessions push` and read ~/.claude/session-sync.log for why."})
	} else {
		out = append(out, check{"archived", levelOK, fmt.Sprintf("every session that ended over %d minutes ago is in the archive", int(archiveLag.Minutes())), ""})
	}

	last, ok := lastPush(root)
	switch {
	case !ok:
		out = append(out, check{"last push", levelWarn, "no successful push recorded in session-sync.log",
			"Run `claude-sessions push` once, then check the hook and the sweep."})
	case time.Since(last) > quietPushAge:
		out = append(out, check{"last push", levelWarn,
			fmt.Sprintf("the last successful push was %s ago (%s)", time.Since(last).Round(time.Minute), last.Format("2006-01-02 15:04")),
			"Expected every 30 minutes while this machine is on. If it has been on, the sweep is not running or is failing: see session-sync.log."})
	default:
		out = append(out, check{"last push", levelOK, last.Format("2006-01-02 15:04"), ""})
	}

	// doctor runs in a terminal, which may hold a privacy grant the background sweep
	// does not, so a refusal only shows up in what the sweep logged.
	if line, denied := deniedSincePush(root); denied {
		// Say which run was refused: the sweep marks its own lines.
		bySweep := strings.HasSuffix(strings.TrimSpace(line), strings.TrimSpace(sweepMark))
		hint := "A push was refused access to the archive. Check the folder's permissions."
		switch {
		case runtime.GOOS == "darwin" && bySweep:
			hint = "macOS privacy settings refused the background sweep. In System Settings > Privacy & Security > Full Disk Access, add " +
				filepath.Join(root, "bin", "claude-sessions") + ". A rebuilt binary loses the grant: run `install` again and re-add it."
		case runtime.GOOS == "darwin":
			hint = "macOS privacy settings refused a push run from a terminal or Claude Code. Give that app access in System Settings > Privacy & Security > Files and Folders (or Full Disk Access)."
		case bySweep:
			hint = "The background sweep was refused access to the archive. Check the folder's permissions for the account the sweep runs as."
		}
		out = append(out, check{"archive access", levelFail, line, hint})
	}
	return out
}

// otherVersions names every other machine whose shard was written by a different
// build, as "NAME (version)".
func otherVersions(dest string) []string {
	shards, _, _ := archive.ReadShards(dest)
	me := machineName()
	var out []string
	for name, s := range shards {
		if strings.EqualFold(name, me) || s.ToolVersion == version {
			continue
		}
		v := s.ToolVersion
		if v == "" {
			v = "an older build that does not record it"
		}
		out = append(out, fmt.Sprintf("%s (%s)", name, v))
	}
	sort.Strings(out)
	return out
}

// deniedSincePush returns the newest "operation not permitted" line logged after the
// last push that clears it, if there is one. Where a sweep marks its runs, only its
// own success clears it: any other push - a hook, a terminal - holds a terminal's
// privacy grant, which the sweep may lack. Where none does - `install --no-sweep`, or
// an install from before --sweep - any successful push clears it, or it never could.
func deniedSincePush(root string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(root, "session-sync.log"))
	if err != nil {
		return "", false
	}
	mark := strings.TrimSpace(sweepMark)
	lines := strings.Split(string(raw), "\n")
	// Any line a sweep marked shows a marking sweep is installed - its PUSH, or the
	// refusal of one that has never once been allowed.
	sweeps := false
	for _, l := range lines {
		if strings.HasSuffix(strings.TrimSpace(l), mark) {
			sweeps = true
			break
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		_, rest, _ := strings.Cut(lines[i], "  ")
		if strings.HasPrefix(rest, "PUSH:") && (!sweeps || strings.HasSuffix(strings.TrimSpace(rest), mark)) {
			return "", false
		}
		if strings.Contains(strings.ToLower(lines[i]), "operation not permitted") {
			return lines[i], true
		}
	}
	return "", false
}

// lastPush is the time of the newest "PUSH:" line in the log, or its rotated copy.
func lastPush(root string) (time.Time, bool) {
	for _, name := range []string{"session-sync.log", "session-sync.log.1"} {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		lines := strings.Split(string(raw), "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			stamp, rest, ok := strings.Cut(lines[i], "  ")
			if !ok || !strings.HasPrefix(rest, "PUSH:") {
				continue
			}
			if t, err := time.ParseInLocation("2006-01-02 15:04:05", stamp, time.Local); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// secretNames are files that must never reach a synced folder.
//
//	.credentials.json - auth tokens
//	.claude.json      - rewritten by any live session; may carry project state
var secretNames = map[string]bool{
	".credentials.json": true,
	".claude.json":      true,
}

// findSecrets walks the whole archive except the rendered pages.
//
// It used to stop three levels down to save time on a cloud filesystem - which
// skipped projects/<bucket>/memory, and the session folders where subagent transcripts
// and saved tool output now live, four and five levels down. A check that does not
// look where the files are is worse than none: it reports "nothing found".
func findSecrets(dest string) []string {
	var found []string
	// Errors are ignored per-entry so one unreadable subdirectory cannot suppress the
	// whole check.
	_ = filepath.WalkDir(dest, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != dest && d.Name() == "html" && filepath.Dir(path) == dest {
				return filepath.SkipDir // rendered output, never a credential store
			}
			return nil
		}
		if secretNames[strings.ToLower(d.Name())] {
			found = append(found, path)
		}
		return nil
	})
	return found
}

// doctorReport is the machine-readable shape.
//
// It is an object rather than a bare array so a monitor does not have to aggregate
// levels itself, and so the build that produced a report is identifiable.
type doctorReport struct {
	Version  string  `json:"version"`
	Checks   []check `json:"checks"`
	Warnings int     `json:"warnings"`
	Failures int     `json:"failures"`
	OK       bool    `json:"ok"`
}

func report(checks []check, asJSON bool) error {
	var warns, fails int
	for _, c := range checks {
		switch c.Level {
		case levelWarn:
			warns++
		case levelFail:
			fails++
		}
	}

	if asJSON {
		if checks == nil {
			checks = []check{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(doctorReport{
			Version:  version,
			Checks:   checks,
			Warnings: warns,
			Failures: fails,
			OK:       fails == 0,
		}); err != nil {
			return err
		}
		if fails > 0 {
			return errChecksFailed
		}
		return nil
	}

	fmt.Println()
	for _, c := range checks {
		mark := " "
		switch c.Level {
		case levelOK:
			mark = "+"
		case levelWarn:
			mark = "!"
		case levelFail:
			mark = "x"
		}
		fmt.Printf("  %s %-16s %s\n", mark, c.Name, c.Detail)
	}

	fmt.Println()
	for _, c := range checks {
		if c.Advice != "" {
			fmt.Printf("  %s: %s\n", c.Name, c.Advice)
		}
	}
	if warns > 0 || fails > 0 {
		fmt.Printf("\n%d warning(s), %d failure(s).\n", warns, fails)
	} else {
		fmt.Println("\nAll checks passed.")
	}

	if fails > 0 {
		return errChecksFailed
	}
	return nil
}

// resolveRoot finds the Claude profile, always as an absolute path. A relative
// --claude-dir used to be written into the hook as-is, and Claude Code runs hooks in
// the project's folder - so a cloned repository could supply the program that ran at
// the end of every session.
func resolveRoot(override string) (string, claude.RootSource, error) {
	root, src, err := claude.Root()
	if strings.TrimSpace(override) != "" {
		root, src = "", "--claude-dir"
		root, err = filepath.Abs(override)
	}
	// The machine name the user pinned is read here, where every command first knows
	// its profile, so every name it writes agrees.
	if err == nil {
		if cfg, cerr := archive.LoadConfig(root); cerr == nil {
			savedMachine, formerMachines = cfg.Machine, cfg.FormerMachines
		}
	}
	return root, src, err
}

// renderState summarises how current the HTML is.
//
// It compares counts and the newest transcript against its page, rather than every
// file: doctor runs against a cloud filesystem and should not stat the whole archive
// twice to answer a cosmetic question.
func renderState(dest string, manifest map[string]archive.Project, exclude []string) (pages, transcripts int, newestStale bool) {
	buckets, err := archive.BucketNames(dest)
	if err != nil {
		return 0, 0, false
	}

	var newestPath, newestBucket string
	var newestMod time.Time

	for _, bucket := range buckets {
		// A project the user deliberately withheld has no page by design. Counting it
		// as missing produces a warning whose advice - run render - can never help.
		project := bucket
		if p, ok := manifest[bucket]; ok && p.Path != "" {
			project = p.Path
		}
		if render.IsExcluded(project, bucket, exclude) {
			continue
		}

		entries, err := os.ReadDir(filepath.Join(archive.ProjectsDir(dest), bucket))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".jsonl") {
				continue
			}
			transcripts++
			info, err := e.Info()
			if err != nil {
				continue
			}
			if info.ModTime().After(newestMod) {
				newestMod = info.ModTime()
				newestBucket = bucket
				newestPath = strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
			}
		}
	}

	htmlDirs, err := os.ReadDir(filepath.Join(dest, "html"))
	if err != nil {
		return 0, transcripts, transcripts > 0
	}
	for _, d := range htmlDirs {
		if !d.IsDir() {
			continue
		}
		// Count only what is in scope, or the figures disagree with each other:
		// pages left over from before an exclusion would be counted against a
		// transcript total that no longer includes them.
		project := d.Name()
		if p, ok := manifest[d.Name()]; ok && p.Path != "" {
			project = p.Path
		}
		if render.IsExcluded(project, d.Name(), exclude) {
			continue
		}
		files, err := os.ReadDir(filepath.Join(dest, "html", d.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			// A project's index.html lists its sessions; it is not one.
			if !f.IsDir() && strings.EqualFold(filepath.Ext(f.Name()), ".html") && !strings.EqualFold(f.Name(), "index.html") {
				pages++
			}
		}
	}

	if newestPath != "" {
		page := filepath.Join(dest, "html", newestBucket, newestPath+".html")
		info, err := os.Stat(page)
		newestStale = err != nil || !archive.SameMTime(info.ModTime(), newestMod)
	}
	return pages, transcripts, newestStale
}
