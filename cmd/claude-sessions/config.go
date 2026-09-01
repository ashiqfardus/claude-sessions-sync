package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
)

// cmdConfig makes doctor's advice actionable: without it there is no way to persist
// a destination, so a user told "no archive configured" has no command to run.
func cmdConfig(args []string) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, _, err := resolveRoot(*claudeDir)
	if err != nil {
		return err
	}

	rest := fs.Args()
	action := "show"
	if len(rest) > 0 {
		action = rest[0]
	}

	switch action {
	case "show":
		cfg, err := archive.LoadConfig(root)
		if err != nil {
			return err
		}
		dest, src, err := archive.ResolveDestination(root, "")
		if err != nil {
			return err
		}
		fmt.Printf("config file  %s\n", archive.ConfigPath(root))
		if cfg.Destination == "" {
			fmt.Printf("destination  (not set)\n")
		} else {
			fmt.Printf("destination  %s\n", cfg.Destination)
		}
		if dest != "" && src != archive.SourceConfig {
			fmt.Printf("in use       %s (%s)\n", dest, src)
			fmt.Printf("\nRun `claude-sessions config set-destination %q` to make that permanent.\n", dest)
		}
		return nil

	case "set-destination":
		if len(rest) < 2 {
			return fmt.Errorf("usage: claude-sessions config set-destination <folder>")
		}
		dest := archive.TrimSep(strings.TrimSpace(rest[1]))
		abs, err := filepath.Abs(dest)
		if err != nil {
			return err
		}

		// Create the archive folder, but never its parent: if the sync client is not
		// mounted, silently creating a plain local folder where G:\ should be is how
		// people end up believing they have a cloud backup that does not exist.
		parent := filepath.Dir(abs)
		if st, err := os.Stat(parent); err != nil || !st.IsDir() {
			return fmt.Errorf("%s does not exist - is the sync client mounted?", parent)
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return err
		}
		if err := archive.Writable(abs); err != nil {
			return fmt.Errorf("%s is not writable: %w", abs, err)
		}
		if err := archive.SaveConfig(root, archive.Config{Destination: abs}); err != nil {
			return err
		}
		fmt.Printf("Destination set to %s\n", abs)
		fmt.Printf("Saved in %s\n", archive.ConfigPath(root))
		return nil

	default:
		return fmt.Errorf("unknown config action %q (try: show, set-destination)", action)
	}
}
