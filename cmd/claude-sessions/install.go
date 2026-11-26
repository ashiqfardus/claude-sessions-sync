package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/hostagent"
)

// The scheduled sweep's calls, replaced by tests that must not register a real job.
var (
	installSweep      = hostagent.Install
	installShortcut   = hostagent.InstallShortcut
	uninstallShortcut = hostagent.UninstallShortcut
	uninstallSweep    = hostagent.Uninstall
	legacySweep       = hostagent.LegacySweep
	removeLegacySweep = hostagent.RemoveLegacySweep
	executable        = os.Executable // which copy install runs as, faked by tests
)

func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	archiveDir := fs.String("archive", "", "set the archive destination while installing")
	every := fs.Int("every", 30, "how often the periodic sweep runs, in minutes")
	keepLegacy := fs.Bool("keep-powershell-hook", false, "leave an existing sync-claude-sessions.ps1 hook in place")
	noSweep := fs.Bool("no-sweep", false, "install the session hook only, without the scheduled sweep")
	replaceLegacySweep := fs.Bool("replace-powershell-sweep", false, "also remove the scheduled task registered by the PowerShell predecessor")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := parseFlags(fs, args, false); err != nil {
		return err
	}
	o := installOpts{ClaudeDir: *claudeDir, ArchiveDir: *archiveDir, Every: *every,
		NoSweep: *noSweep, KeepLegacy: *keepLegacy, ReplaceLegacySweep: *replaceLegacySweep}
	if !*asJSON {
		_, err := runInstall(stdStreams(), o)
		return err
	}
	res, err := runInstall(jsonStreams(), o)
	if err != nil {
		return err
	}
	return encodeJSON(os.Stdout, res)
}

type installOpts struct {
	ClaudeDir, ArchiveDir                   string
	Every                                   int
	NoSweep, KeepLegacy, ReplaceLegacySweep bool
}

// installResult is what install did, as its lines say it.
type installResult struct {
	Archive        string `json:"archive"`
	Binary         string `json:"binary"`
	Hook           string `json:"hook"`                     // "new", or how many existing entries it replaced
	Sweep          string `json:"sweep"`                    // the job registered; with --no-sweep "skipped (--no-sweep)", or "removed (only after replies)" for one installed earlier
	SweepFailed    string `json:"sweepFailed,omitempty"`    // why the sweep could not be registered, or with --no-sweep removed
	Shortcut       string `json:"shortcut"`                 // where the panel shortcut is, or "" if it could not be made
	ShortcutFailed string `json:"shortcutFailed,omitempty"` // why it could not be made; never fails the install
}

// runInstall installs the hooks and the sweep, printing on s.Out as the CLI does.
func runInstall(s streams, o installOpts) (installResult, error) {
	var res installResult
	root, _, err := resolveRoot(o.ClaudeDir)
	if err != nil {
		return res, err
	}

	// The destination is settled first: installing automation that has nowhere to
	// write is worse than not installing it, because it looks like it worked.
	dest, src, err := archive.ResolveDestination(root, o.ArchiveDir)
	if err != nil {
		return res, err
	}
	switch {
	case dest == "":
		return res, fmt.Errorf("no archive destination. Run `claude-sessions config set-destination <folder>` first")
	case o.ArchiveDir != "":
		// The same checks as config set-destination: a typo'd or unmounted drive used
		// to be saved, followed by "Done. Sessions on this machine now archive".
		if dest, err = archive.PrepareDestination(root, dest); err != nil {
			return res, err
		}
		if err := archive.SaveConfig(root, archive.Config{Destination: dest}); err != nil {
			return res, err
		}
		src = archive.SourceConfig
	default:
		if err := archive.ValidateDestination(root, dest); err != nil {
			return res, err
		}
		if st, err := os.Stat(filepath.Dir(dest)); err != nil || !st.IsDir() {
			return res, fmt.Errorf("%s is not available - is the sync client mounted?", filepath.Dir(dest))
		}
	}

	fmt.Fprintf(s.Out, "Installing on %s\n\n", runtime.GOOS)
	fmt.Fprintf(s.Out, "  archive   %s (%s)\n", dest, src)
	res.Archive = dest

	// Copy the binary somewhere stable.
	//
	// The hook and the sweep must not point at wherever the binary happened to be run
	// from - a build directory, a download folder, or the archive itself. A copy under
	// the Claude directory keeps the end of every session independent of the sync
	// client being mounted.
	binary, note, err := installBinary(root)
	if err != nil {
		return res, err
	}
	if note != "" {
		note = " (" + note + ")"
	}
	fmt.Fprintf(s.Out, "  binary    %s%s\n", binary, note)
	res.Binary = binary

	replaced, err := claude.InstallHook(claude.SettingsPath(root), binary, !o.KeepLegacy)
	if err != nil {
		return res, err
	}
	res.Hook = "new"
	if replaced > 0 {
		res.Hook = fmt.Sprintf("replaced %d existing entr(y/ies)", replaced)
	}
	fmt.Fprintf(s.Out, "  hook      SessionEnd + Stop (%s)\n", res.Hook) // claude.ourHooks

	// Before the sweep, whose failure returns early: the shortcut does not depend on it.
	// Windows runs the windowless copy, which --no-sweep must not skip. Made once, for
	// the sweep too.
	sweepBinary, werr := binary, error(nil)
	if runtime.GOOS == "windows" {
		sweepBinary, werr = installWindowless(binary)
	}
	serr := werr
	if serr == nil {
		res.Shortcut, serr = installShortcut(sweepJob(root, binary))
	}
	if serr != nil {
		res.Shortcut, res.ShortcutFailed = "", serr.Error()
		fmt.Fprintf(s.Out, "  shortcut  FAILED: %v\n", serr)
	} else {
		fmt.Fprintf(s.Out, "  shortcut  %s\n", res.Shortcut)
	}

	every := o.Every
	if every <= 0 {
		every = 30 // as hostagent registers it
	}
	if o.NoSweep {
		// A sweep installed earlier goes too: saved as none while it still ran, the
		// setting would say what is not so.
		every = -1
		res.Sweep = "skipped (--no-sweep)"
		if removed, err := uninstallSweep(sweepJob(root, "")); err != nil {
			res.Sweep, res.SweepFailed = "", err.Error()
			fmt.Fprintf(s.Out, "  sweep     FAILED to remove the sweep installed earlier: %v\n", err)
		} else {
			if removed {
				res.Sweep = "removed (only after replies)"
			}
			fmt.Fprintf(s.Out, "  sweep     %s\n", res.Sweep)
		}
	} else {
		// Report the predecessor's scheduled job rather than removing it. It belongs
		// to another program; taking it away because the names looked similar is how
		// a working backup disappears.
		if action, exists := legacySweep(); exists {
			if o.ReplaceLegacySweep {
				if err := removeLegacySweep(); err != nil {
					fmt.Fprintf(s.Out, "  legacy    could not remove: %v\n", err)
				} else {
					fmt.Fprintf(s.Out, "  legacy    removed the PowerShell sweep\n")
				}
			} else {
				fmt.Fprintf(s.Out, "  legacy    the PowerShell sweep is still registered:\n            %s\n", action)
				fmt.Fprintf(s.Out, "            Both will now archive. Remove it yourself, or re-run with --replace-powershell-sweep.\n")
			}
		}

		err = werr
		var status hostagent.Status
		if err == nil {
			status, err = installSweep(sweepJob(root, sweepBinary), o.Every)
		}
		if err != nil {
			// The hook alone still archives every session that ends normally, so this
			// is a degraded install, not a failed one.
			res.SweepFailed = err.Error()
			fmt.Fprintf(s.Out, "  sweep     FAILED: %v\n", err)
			fmt.Fprintln(s.Out, "\nThe session hook is installed and will archive sessions that end normally.")
			fmt.Fprintln(s.Out, "Only abruptly-killed sessions need the sweep, so this is worth fixing but not urgent.")
			return res, nil
		}
		res.Sweep = fmt.Sprintf("%s: %s", status.Mechanism, status.Detail)
		fmt.Fprintf(s.Out, "  sweep     %s\n", res.Sweep)
	}

	// The interval, for the panel to show. A sweep that could not be installed or
	// removed keeps the one saved before: that job may still be registered.
	var saveErr error
	if res.SweepFailed == "" {
		saveErr = saveSweepEvery(root, every)
	}

	fmt.Fprintln(s.Out, "\nDone. Sessions on this machine now archive automatically.")
	fmt.Fprintln(s.Out, "Run `claude-sessions doctor` to confirm, and restart Claude Code (or run /hooks)")
	fmt.Fprintln(s.Out, "so a running session picks up the new hook.")
	if saveErr != nil { // only what config show and the panel say: the backup itself is set up
		fmt.Fprintf(s.Err, "\nNote: could not save the sweep's interval, so `config show` and the panel may show the old one: %v\n", saveErr)
	}
	return res, nil
}

func cmdUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	keepLegacy := fs.Bool("keep-powershell-hook", true, "leave a sync-claude-sessions.ps1 hook alone")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := parseFlags(fs, args, false); err != nil {
		return err
	}
	o := uninstallOpts{ClaudeDir: *claudeDir, KeepLegacy: *keepLegacy}
	if !*asJSON {
		_, err := runUninstall(stdStreams(), o)
		return err
	}
	res, err := runUninstall(jsonStreams(), o)
	if err != nil {
		return err
	}
	return encodeJSON(os.Stdout, res)
}

type uninstallOpts struct {
	ClaudeDir  string
	KeepLegacy bool // the CLI's default is true: false also removes the PowerShell hook
}

type uninstallResult struct {
	HookEntries int    `json:"hookEntries"` // hook entries removed
	Sweep       string `json:"sweep"`       // "removed", "none registered for <profile>" or "FAILED: <why>"
	Shortcut    string `json:"shortcut"`    // "removed", "none" or "FAILED: <why>"
}

// runUninstall removes the hooks and the sweep, printing on s.Out as the CLI does.
func runUninstall(s streams, o uninstallOpts) (uninstallResult, error) {
	var res uninstallResult
	root, _, err := resolveRoot(o.ClaudeDir)
	if err != nil {
		return res, err
	}

	removed, err := claude.RemoveHook(claude.SettingsPath(root), !o.KeepLegacy)
	if err != nil {
		return res, err
	}
	res.HookEntries = removed
	fmt.Fprintf(s.Out, "  hook      removed %d entr(y/ies)\n", removed)

	swept, err := uninstallSweep(sweepJob(root, ""))
	switch {
	case err != nil:
		res.Sweep = fmt.Sprintf("FAILED: %v", err)
	case swept:
		res.Sweep = "removed"
	default:
		// Said "removed" before, which hid a sweep registered under another profile's
		// name (installed with CLAUDE_CONFIG_DIR set, uninstalled without it).
		res.Sweep = "none registered for " + root
	}
	fmt.Fprintf(s.Out, "  sweep     %s\n", res.Sweep)
	var saveErr error
	if err == nil && swept { // none runs now: config show and the panel say so
		saveErr = saveSweepEvery(root, -1)
	}

	removedLnk, lerr := uninstallShortcut(sweepJob(root, ""))
	switch {
	case lerr != nil:
		res.Shortcut = fmt.Sprintf("FAILED: %v", lerr)
	case removedLnk:
		res.Shortcut = "removed"
	default:
		res.Shortcut = "none"
	}
	fmt.Fprintf(s.Out, "  shortcut  %s\n", res.Shortcut)

	// The archive and the binary are left alone on purpose: the archive is the user's
	// data, and removing the binary would delete the program that is running.
	fmt.Fprintln(s.Out, "\nYour archive and its transcripts are untouched.")
	fmt.Fprintf(s.Out, "The binary is still at %s if you want to remove it.\n",
		filepath.Join(root, "bin"))
	if saveErr != nil {
		fmt.Fprintf(s.Err, "\nNote: could not save that no sweep runs, so `config show` and the panel may still show one: %v\n", saveErr)
	}
	return res, nil
}

// saveSweepEvery records the sweep's interval in minutes, -1 for none.
func saveSweepEvery(root string, every int) error {
	cfg, err := archive.LoadConfig(root)
	if err != nil {
		return err
	}
	cfg.SweepEvery = every
	return archive.SaveConfig(root, cfg)
}

// installBinary copies the running executable into <claude-root>/bin, or the console
// program beside it when this is the windowless copy. note says why it copied nothing.
func installBinary(root string) (target, note string, err error) {
	self, err := executable()
	if err != nil {
		return "", "", err
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return "", "", err
	}

	name := "claude-sessions"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", "", err
	}
	target = filepath.Join(binDir, name)

	// The windowless copy (Windows only: a panel opened from the shortcut runs it) must
	// never land where the hooks run a console program: the console one beside it is
	// copied instead, and installWindowless then makes the windowless copy from that.
	if strings.EqualFold(filepath.Base(self), hostagent.WindowlessName) {
		console := filepath.Join(filepath.Dir(self), name)
		if st, err := os.Stat(console); err == nil && st.Mode().IsRegular() {
			self = console
		} else if _, err := os.Stat(target); err == nil {
			return target, "not updated: this is the windowless copy, and " + name + " is not beside it", nil
		} else {
			return "", "", fmt.Errorf("%s is the windowless copy, and %s is not beside it: run claude-sessions install from a terminal", self, name)
		}
	}

	// Already running from the install location: nothing to copy, and copying a file
	// onto itself would truncate it. Compared as files, not strings: an 8.3 short path
	// (ADMINI~1), a symlinked root or different separators name the same file.
	if selfInfo, err := os.Stat(self); err == nil {
		if targetInfo, err := os.Stat(target); err == nil && os.SameFile(selfInfo, targetInfo) {
			return target, "", nil
		}
	}

	in, err := os.Open(self)
	if err != nil {
		return "", "", err
	}
	defer in.Close()
	return target, "", replaceExecutable(target, in)
}

// installWindowless writes, next to binary, the GUI-subsystem copy the Windows sweep
// runs so that Task Scheduler opens no terminal - see hostagent.WindowlessName.
func installWindowless(binary string) (string, error) {
	data, err := os.ReadFile(binary)
	if err != nil {
		return "", err
	}
	patched, err := hostagent.Windowless(data)
	if err != nil {
		return "", fmt.Errorf("%s: %w", binary, err)
	}
	target := filepath.Join(filepath.Dir(binary), hostagent.WindowlessName)
	return target, replaceExecutable(target, bytes.NewReader(patched))
}

// replaceExecutable writes src to target via a temporary file, even when target is
// the program the hook or the sweep is running right now.
func replaceExecutable(target string, src io.Reader) error {
	// Copies left by an earlier upgrade that replaced a running binary.
	if old, _ := filepath.Glob(target + ".old-*"); len(old) > 0 {
		for _, o := range old {
			os.Remove(o) // still running is fine: it goes next time
		}
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), ".tmp-claude-sessions-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		// Windows refuses to replace an executable that is running - the hook or the
		// sweep, mid-push - but does allow renaming it. Move it aside and retry; the
		// leftover is removed by the next install.
		aside := fmt.Sprintf("%s.old-%d", target, time.Now().UnixNano())
		if runtime.GOOS != "windows" || os.Rename(target, aside) != nil {
			os.Remove(tmpName)
			return fmt.Errorf("could not install the binary to %s: %w", target, err)
		}
		if err2 := os.Rename(tmpName, target); err2 != nil {
			// Put the old binary back: a missing one breaks the hook and the sweep,
			// an old one only means the upgrade did not happen.
			os.Rename(aside, target)
			os.Remove(tmpName)
			return fmt.Errorf("could not install the binary to %s: %w", target, err2)
		}
		os.Remove(aside)
	}
	return nil
}

// sweepJob names the scheduled job for a profile. The default ~/.claude keeps the
// machine-wide name existing installs registered; any other profile gets its own.
func sweepJob(root, binary string) hostagent.Job {
	j := hostagent.Job{Binary: binary, Profile: root}
	if home, err := os.UserHomeDir(); err == nil && sameDir(root, filepath.Join(home, ".claude")) {
		j.Profile = ""
	}
	return j
}

// sameDir compares two directories as files, not strings: an 8.3 short path
// (ADMINI~1) or a symlink names the same folder, and a string mismatch registered a
// second sweep for the default profile.
func sameDir(a, b string) bool {
	if strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) {
		return true
	}
	ai, err1 := os.Stat(a)
	bi, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(ai, bi)
}
