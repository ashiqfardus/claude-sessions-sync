package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/hostagent"
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
	if err := fs.Parse(args); err != nil {
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

	// --- manifest drift -------------------------------------------------------
	// The check that matters: a bucket present in the archive but absent from the
	// manifest cannot be identity-matched, so `import` on a new machine will not
	// place it anywhere. Its transcripts are backed up but unroutable.
	manifest, badShards, err := archive.Merged(dest)
	if err != nil {
		add(check{"manifest", levelWarn, err.Error(), ""})
	}
	for _, b := range badShards {
		add(check{"manifest", levelWarn, "unreadable shard: " + b, "It is being ignored; other shards still merged."})
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

	if memoryOnly > 0 {
		add(check{"local buckets", levelInfo,
			fmt.Sprintf("%d local bucket(s) hold memory but no transcripts", memoryOnly), ""})
	}

	// --- automation -----------------------------------------------------------
	goHooks := settings.SessionEndHooks("claude-sessions")
	psHooks := settings.SessionEndHooks("sync-claude-sessions.ps1")
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
		add(check{"session hook", levelOK, shorten(cmd, 96), ""})
	default:
		add(check{"session hook", levelWarn, "no SessionEnd hook installed",
			"Sessions are only archived by the periodic sweep, so the most recent one can be missed."})
	}

	sweep, err := hostagent.Sweep()
	if err != nil {
		add(check{"sweep", levelWarn, err.Error(), ""})
	} else if sweep.Installed {
		l := levelOK
		if strings.Contains(sweep.Detail, "NOT running") {
			l = levelFail
		}
		add(check{"sweep", l, fmt.Sprintf("%s: %s", sweep.Mechanism, sweep.Detail), ""})
	} else {
		add(check{"sweep", levelWarn, fmt.Sprintf("%s: %s", sweep.Mechanism, sweep.Detail),
			"A session killed abruptly will not be archived until the next manual push."})
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

// secretNames are files that must never reach a synced folder.
//
//	.credentials.json - auth tokens
//	.claude.json      - rewritten by any live session; may carry project state
var secretNames = map[string]bool{
	".credentials.json": true,
	".claude.json":      true,
}

// maxSecretScanDepth bounds the walk.
//
// Everything this tool writes lives at <archive>/, <archive>/projects/<bucket>/ or
// <archive>/projects/<bucket>/memory/ - depth 3. Walking deeper on a cloud or network
// filesystem costs real time on every doctor run and cannot find anything the tool
// itself put there.
const maxSecretScanDepth = 3

func findSecrets(dest string) []string {
	var found []string
	// Errors are ignored per-entry so one unreadable subdirectory cannot suppress the
	// whole check.
	_ = filepath.WalkDir(dest, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path == dest {
				return nil
			}
			if d.Name() == "html" {
				return filepath.SkipDir // rendered output, never a credential store
			}
			rel, relErr := filepath.Rel(dest, path)
			if relErr == nil && len(strings.Split(rel, string(filepath.Separator))) >= maxSecretScanDepth {
				return filepath.SkipDir
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

func resolveRoot(override string) (string, claude.RootSource, error) {
	if strings.TrimSpace(override) != "" {
		return filepath.Clean(override), "--claude-dir", nil
	}
	return claude.Root()
}
