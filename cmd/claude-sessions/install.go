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

func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	archiveDir := fs.String("archive", "", "set the archive destination while installing")
	every := fs.Int("every", 30, "how often the periodic sweep runs, in minutes")
	keepLegacy := fs.Bool("keep-powershell-hook", false, "leave an existing sync-claude-sessions.ps1 hook in place")
	noSweep := fs.Bool("no-sweep", false, "install the session hook only, without the scheduled sweep")
	replaceLegacySweep := fs.Bool("replace-powershell-sweep", false, "also remove the scheduled task registered by the PowerShell predecessor")
	if err := parseFlags(fs, args, false); err != nil {
		return err
	}

	root, _, err := resolveRoot(*claudeDir)
	if err != nil {
		return err
	}

	// The destination is settled first: installing automation that has nowhere to
	// write is worse than not installing it, because it looks like it worked.
	dest, src, err := archive.ResolveDestination(root, *archiveDir)
	if err != nil {
		return err
	}
	switch {
	case dest == "":
		return fmt.Errorf("no archive destination. Run `claude-sessions config set-destination <folder>` first")
	case *archiveDir != "":
		// The same checks as config set-destination: a typo'd or unmounted drive used
		// to be saved, followed by "Done. Sessions on this machine now archive".
		if dest, err = archive.PrepareDestination(root, dest); err != nil {
			return err
		}
		if err := archive.SaveConfig(root, archive.Config{Destination: dest}); err != nil {
			return err
		}
		src = archive.SourceConfig
	default:
		if err := archive.ValidateDestination(root, dest); err != nil {
			return err
		}
		if st, err := os.Stat(filepath.Dir(dest)); err != nil || !st.IsDir() {
			return fmt.Errorf("%s is not available - is the sync client mounted?", filepath.Dir(dest))
		}
	}

	fmt.Printf("Installing on %s\n\n", runtime.GOOS)
	fmt.Printf("  archive   %s (%s)\n", dest, src)

	// Copy the binary somewhere stable.
	//
	// The hook and the sweep must not point at wherever the binary happened to be run
	// from - a build directory, a download folder, or the archive itself. A copy under
	// the Claude directory keeps the end of every session independent of the sync
	// client being mounted.
	binary, err := installBinary(root)
	if err != nil {
		return err
	}
	fmt.Printf("  binary    %s\n", binary)

	replaced, err := claude.InstallHook(claude.SettingsPath(root), binary, !*keepLegacy)
	if err != nil {
		return err
	}
	hookNote := "new"
	if replaced > 0 {
		hookNote = fmt.Sprintf("replaced %d existing entr(y/ies)", replaced)
	}
	fmt.Printf("  hook      SessionEnd (%s)\n", hookNote)

	if *noSweep {
		fmt.Printf("  sweep     skipped (--no-sweep)\n")
	} else {
		// Report the predecessor's scheduled job rather than removing it. It belongs
		// to another program; taking it away because the names looked similar is how
		// a working backup disappears.
		if action, exists := hostagent.LegacySweep(); exists {
			if *replaceLegacySweep {
				if err := hostagent.RemoveLegacySweep(); err != nil {
					fmt.Printf("  legacy    could not remove: %v\n", err)
				} else {
					fmt.Printf("  legacy    removed the PowerShell sweep\n")
				}
			} else {
				fmt.Printf("  legacy    the PowerShell sweep is still registered:\n            %s\n", action)
				fmt.Printf("            Both will now archive. Remove it yourself, or re-run with --replace-powershell-sweep.\n")
			}
		}

		sweepBinary := binary
		if runtime.GOOS == "windows" {
			sweepBinary, err = installWindowless(binary)
		}
		var status hostagent.Status
		if err == nil {
			status, err = hostagent.Install(sweepJob(root, sweepBinary), *every)
		}
		if err != nil {
			// The hook alone still archives every session that ends normally, so this
			// is a degraded install, not a failed one.
			fmt.Printf("  sweep     FAILED: %v\n", err)
			fmt.Println("\nThe session hook is installed and will archive sessions that end normally.")
			fmt.Println("Only abruptly-killed sessions need the sweep, so this is worth fixing but not urgent.")
			return nil
		}
		fmt.Printf("  sweep     %s: %s\n", status.Mechanism, status.Detail)
	}

	fmt.Println("\nDone. Sessions on this machine now archive automatically.")
	fmt.Println("Run `claude-sessions doctor` to confirm, and restart Claude Code (or run /hooks)")
	fmt.Println("so a running session picks up the new hook.")
	return nil
}

func cmdUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	keepLegacy := fs.Bool("keep-powershell-hook", true, "leave a sync-claude-sessions.ps1 hook alone")
	if err := parseFlags(fs, args, false); err != nil {
		return err
	}

	root, _, err := resolveRoot(*claudeDir)
	if err != nil {
		return err
	}

	removed, err := claude.RemoveHook(claude.SettingsPath(root), !*keepLegacy)
	if err != nil {
		return err
	}
	fmt.Printf("  hook      removed %d entr(y/ies)\n", removed)

	if removed, err := hostagent.Uninstall(sweepJob(root, "")); err != nil {
		fmt.Printf("  sweep     FAILED: %v\n", err)
	} else if removed {
		fmt.Printf("  sweep     removed\n")
	} else {
		// Said "removed" before, which hid a sweep registered under another profile's
		// name (installed with CLAUDE_CONFIG_DIR set, uninstalled without it).
		fmt.Printf("  sweep     none registered for %s\n", root)
	}

	// The archive and the binary are left alone on purpose: the archive is the user's
	// data, and removing the binary would delete the program that is running.
	fmt.Println("\nYour archive and its transcripts are untouched.")
	fmt.Printf("The binary is still at %s if you want to remove it.\n",
		filepath.Join(root, "bin"))
	return nil
}

// installBinary copies the running executable into <claude-root>/bin.
func installBinary(root string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return "", err
	}

	name := "claude-sessions"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", err
	}
	target := filepath.Join(binDir, name)

	// Already running from the install location: nothing to copy, and copying a file
	// onto itself would truncate it. Compared as files, not strings: an 8.3 short path
	// (ADMINI~1), a symlinked root or different separators name the same file.
	if selfInfo, err := os.Stat(self); err == nil {
		if targetInfo, err := os.Stat(target); err == nil && os.SameFile(selfInfo, targetInfo) {
			return target, nil
		}
	}

	in, err := os.Open(self)
	if err != nil {
		return "", err
	}
	defer in.Close()
	return target, replaceExecutable(target, in)
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
