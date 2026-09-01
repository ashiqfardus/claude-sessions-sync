package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/identity"
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
	if err := fs.Parse(args); err != nil {
		return err
	}

	err := runPush(*claudeDir, *archiveDir, *quiet, *dryRun, *asJSON)

	// A SessionEnd hook that fails breaks the end of the user's session. Missing
	// drive, offline mount, unexpected error: log it and exit 0. This is the single
	// most important behaviour in the whole command.
	if *quiet && err != nil {
		return nil
	}
	return err
}

func runPush(claudeDir, archiveDir string, quiet, dryRun, asJSON bool) (err error) {
	root, _, err := resolveRoot(claudeDir)
	if err != nil {
		return err
	}

	logf := newLogger(root, quiet)

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

	lock, err := archive.AcquireLock(root)
	if err != nil {
		// The sweep and the hook firing together is routine, not a problem.
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
	}

	buckets, err := claude.ListBuckets(claude.ProjectsDir(root))
	if err != nil {
		logf("ERROR: %v", err)
		return err
	}

	statePaths, _ := claude.ProjectPaths(claude.StatePath(root))

	res := pushResult{Archive: dest, DryRun: dryRun}
	shard := archive.Shard{
		SchemaVersion: archive.SchemaVersion,
		Machine:       machineName(),
		Projects:      map[string]archive.Project{},
	}

	for _, b := range buckets {
		destBucket := filepath.Join(archive.ProjectsDir(dest), b.Name)

		// Each transcript is read once. The identity below reuses what the newest
		// one yields rather than opening it a second time.
		var newestCwd string
		var newestAt time.Time

		for _, t := range b.Transcripts {
			target := filepath.Join(destBucket, filepath.Base(t.Path))
			srcInfo, statErr := os.Stat(t.Path)
			if statErr != nil {
				res.Skipped = append(res.Skipped, t.ID)
				continue
			}
			if archive.NeedsCopy(srcInfo, archive.StatOrNil(target)) {
				if !dryRun {
					if err := archive.CopyFile(t.Path, target); err != nil {
						// One unreadable transcript must not abandon the rest.
						logf("WARN: %s: %v", t.ID, err)
						res.Skipped = append(res.Skipped, t.ID)
						continue
					}
				}
				res.Copied++
			} else {
				res.Unchanged++
			}
			res.Sessions++

			summary, _ := claude.ScanHead(t.Path, headScanLines)
			shard.Sessions = append(shard.Sessions, archive.Session{
				Bucket: b.Name, Project: summary.Cwd, ID: t.ID,
				Updated: t.ModTime, Size: t.Size, Prompt: summary.FirstPrompt,
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
			shard.Projects[b.Name] = entry
			res.Projects++
		}
	}

	if !dryRun {
		if err := writeManifest(dest, shard); err != nil {
			logf("ERROR: manifest: %v", err)
			return err
		}
		// Built from every machine's shard, not just this one - see writeIndex.
		if err := writeIndex(dest); err != nil {
			logf("ERROR: index: %v", err)
			return err
		}
	}

	logf("PUSH: %d copied, %d unchanged, %d session(s), %d project(s) -> %s",
		res.Copied, res.Unchanged, res.Sessions, res.Projects, dest)

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
		Seen:     time.Now().Format("2006-01-02"),
		Sessions: len(b.Transcripts),
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

// writeIndex regenerates INDEX.md from EVERY machine's shard.
//
// INDEX.md is one file at the archive root. Building it from only the local machine's
// sessions - which is what the first version did - means each push erases every other
// machine's listing, the same defect the sharded manifest exists to avoid. Rows are
// cached in the shards, so this costs no reads across the cloud filesystem.
func writeIndex(dest string) error {
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

	var b strings.Builder
	b.WriteString("# Claude sessions\n\n")
	fmt.Fprintf(&b, "Updated %s. %d session(s) from %s.\n\n",
		time.Now().Format("2006-01-02 15:04"), len(rows), strings.Join(contributors, ", "))
	b.WriteString("Bring these to another machine with `claude-sessions import`.\n\n")

	multiMachine := len(contributors) > 1

	for _, name := range names {
		group := byProject[name]
		sort.Slice(group, func(i, j int) bool { return group[i].Updated.After(group[j].Updated) })

		fmt.Fprintf(&b, "## %s\n\n", name)
		if multiMachine {
			b.WriteString("| Updated | Size | Machine | Session | First prompt |\n|---|---|---|---|---|\n")
		} else {
			b.WriteString("| Updated | Size | Session | First prompt |\n|---|---|---|---|\n")
		}
		for _, r := range group {
			// A pipe in a prompt would break the table.
			prompt := strings.ReplaceAll(r.Prompt, "|", "\\|")
			if multiMachine {
				fmt.Fprintf(&b, "| %s | %s | %s | `%s` | %s |\n",
					r.Updated.Format("2006-01-02 15:04"), humanSize(r.Size), r.Machine, r.ID, prompt)
			} else {
				fmt.Fprintf(&b, "| %s | %s | `%s` | %s |\n",
					r.Updated.Format("2006-01-02 15:04"), humanSize(r.Size), r.ID, prompt)
			}
		}
		b.WriteString("\n")
	}

	return archive.WriteFileAtomic(filepath.Join(dest, "INDEX.md"), []byte(b.String()), 0o644)
}

func machineName() string {
	if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
		// A hostname can contain characters that are awkward in a filename.
		return strings.NewReplacer("/", "-", `\`, "-", ":", "-", " ", "-").Replace(h)
	}
	return "unknown-machine"
}

// newLogger returns a printf-style logger that appends to session-sync.log.
//
// The log is the only record of what an unattended hook or sweep did. It never
// returns an error: failing to log must not fail a push.
func newLogger(root string, quiet bool) func(string, ...any) {
	path := filepath.Join(root, "session-sync.log")
	return func(format string, args ...any) {
		line := fmt.Sprintf("%s  %s\n", time.Now().Format("2006-01-02 15:04:05"),
			fmt.Sprintf(format, args...))
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
