package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

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
	if err := fs.Parse(args); err != nil {
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
	if dest == "" {
		return fmt.Errorf("no archive destination. Run `claude-sessions config set-destination <folder>` first")
	}
	if err := archive.ValidateDestination(root, dest); err != nil {
		return err
	}
	if *archiveDir != "" {
		if err := archive.SaveConfig(root, archive.Config{Destination: dest}); err != nil {
			return err
		}
		src = archive.SourceConfig
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

		status, err := hostagent.Install(binary, *every)
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
	if err := fs.Parse(args); err != nil {
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

	if err := hostagent.Uninstall(); err != nil {
		fmt.Printf("  sweep     FAILED: %v\n", err)
	} else {
		fmt.Printf("  sweep     removed\n")
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
	// onto itself would truncate it.
	if strings.EqualFold(self, target) {
		return target, nil
	}

	in, err := os.Open(self)
	if err != nil {
		return "", err
	}
	defer in.Close()

	// Windows will not overwrite a running executable, but the running one is the
	// source here, not the target - unless a scheduled sweep is executing the target
	// at this instant, which the rename below makes atomic anyway.
	tmp, err := os.CreateTemp(binDir, ".tmp-claude-sessions-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	if err := os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("could not install the binary to %s: %w", target, err)
	}
	return target, nil
}
