package main

import (
	"encoding/json"
	"flag"
	"fmt"
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

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	archiveDir := fs.String("archive", "", "override the synced destination folder")
	asJSON := fs.Bool("json", false, "machine-readable output")
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
			"Run with --archive '<folder>' once, or install a sync client."})
		return report(checks, *asJSON)
	}
	if st, err := os.Stat(dest); err != nil {
		add(check{"archive", levelFail, fmt.Sprintf("%s (%s) is not reachable", dest, src),
			"Sync client offline or not mounted? Nothing is being backed up right now."})
		return report(checks, *asJSON)
	} else if !st.IsDir() {
		add(check{"archive", levelFail, dest + " is not a directory", ""})
		return report(checks, *asJSON)
	}
	add(check{"archive", levelOK, fmt.Sprintf("%s (%s)", dest, src), ""})

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
	if len(unroutable) > 0 {
		add(check{"manifest", levelWarn,
			fmt.Sprintf("%d of %d archived bucket(s) have no usable identity: %s",
				len(unroutable), len(archived), strings.Join(unroutable, ", ")),
			"Identity is read from a transcript's cwd, so a bucket holding only memory files records nothing. `import` cannot place these."})
	} else {
		add(check{"manifest", levelOK, fmt.Sprintf("%d project(s) recorded, all routable", len(manifest)), ""})
	}

	if _, err := os.Stat(archive.ManifestDir(dest)); os.IsNotExist(err) {
		add(check{"manifest format", levelInfo,
			"flat projects.json only (written by the PowerShell tools)",
			"Sharded manifest/<machine>.json arrives with `push`; until then concurrent pushes from two machines can drop entries."})
	}

	if memoryOnly > 0 {
		add(check{"local buckets", levelInfo,
			fmt.Sprintf("%d local bucket(s) hold memory but no transcripts", memoryOnly), ""})
	}

	// --- automation -----------------------------------------------------------
	// Both the Go binary and the PowerShell script it replaces count as installed:
	// on this machine the PowerShell hook is the one actually running.
	if hook, ok := settings.SessionEndHook("claude-sessions", "sync-claude-sessions"); ok {
		cmd := hook.Command
		if len(hook.Args) > 0 {
			cmd += " " + strings.Join(hook.Args, " ")
		}
		add(check{"session hook", levelOK, shorten(cmd, 96), ""})
	} else {
		add(check{"session hook", levelWarn, "no SessionEnd hook installed",
			"Sessions are only archived by the periodic sweep, so the most recent one can be missed."})
	}

	sweep, err := hostagent.Sweep()
	if err != nil {
		add(check{"sweep", levelWarn, err.Error(), ""})
	} else if sweep.Installed {
		add(check{"sweep", levelOK, fmt.Sprintf("%s: %s", sweep.Mechanism, sweep.Detail), ""})
	} else {
		add(check{"sweep", levelWarn, fmt.Sprintf("%s: %s", sweep.Mechanism, sweep.Detail),
			"A session killed abruptly will not be archived until the next manual push."})
	}

	// --- secrets --------------------------------------------------------------
	// A credentials file inside the archive means auth material is being synced to
	// a cloud folder. That is a stop-everything finding, not a warning.
	for _, name := range []string{".credentials.json", ".claude.json"} {
		if _, err := os.Stat(filepath.Join(dest, name)); err == nil {
			add(check{"secrets", levelFail, name + " is present in the archive",
				"Delete it from the synced folder. This tool never copies it; something else did."})
		}
	}

	return report(checks, *asJSON)
}

func report(checks []check, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(checks)
	}

	fmt.Println()
	var warns, fails int
	for _, c := range checks {
		mark := " "
		switch c.Level {
		case levelOK:
			mark = "+"
		case levelWarn:
			mark = "!"
			warns++
		case levelFail:
			mark = "x"
			fails++
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
	return nil
}

func resolveRoot(override string) (string, claude.RootSource, error) {
	if strings.TrimSpace(override) != "" {
		return filepath.Clean(override), "--claude-dir", nil
	}
	return claude.Root()
}
