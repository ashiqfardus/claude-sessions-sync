package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
)

// cmdConfig makes doctor's advice actionable: without it there is no way to persist
// a destination, so a user told "no archive configured" has no command to run.
func cmdConfig(args []string) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	claudeDir := fs.String("claude-dir", "", "override $CLAUDE_CONFIG_DIR / ~/.claude")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `usage:
  claude-sessions config [show]                    print the archive destination
  claude-sessions config set-destination <folder>  choose and save the archive destination
  claude-sessions config map                       list projects mapped by hand
  claude-sessions config map <bucket> <folder>     say where an archived project lives here
  claude-sessions config unmap <bucket>            remove a mapping
  claude-sessions config set-machine <name>        pin the name this machine is known by

flags:
`)
		fs.PrintDefaults()
	}
	if err := parseFlags(fs, positionalAfterAction(args), true); err != nil {
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
		abs, err := archive.PrepareDestination(root, expandHome(rest[1]))
		if err != nil {
			return err
		}
		if err := archive.SaveConfig(root, archive.Config{Destination: abs}); err != nil {
			return err
		}
		fmt.Printf("Destination set to %s\n", abs)
		fmt.Printf("Saved in %s\n", archive.ConfigPath(root))
		return nil

	case "map":
		cfg, err := archive.LoadConfig(root)
		if err != nil {
			return err
		}
		if len(rest) == 1 {
			if len(cfg.ProjectMap) == 0 {
				fmt.Println("no projects mapped")
			}
			keys := make([]string, 0, len(cfg.ProjectMap))
			for k := range cfg.ProjectMap {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Printf("%s -> %s\n", k, cfg.ProjectMap[k])
			}
			return nil
		}
		if len(rest) != 3 {
			return fmt.Errorf("usage: claude-sessions config map <archived bucket> <local folder>")
		}
		bucket := rest[1]
		if !archive.ValidName(bucket) {
			return fmt.Errorf("%q is not a bucket name: use the name `import` printed for the project", bucket)
		}
		local, err := filepath.Abs(expandHome(rest[2]))
		if err != nil {
			return err
		}
		if st, err := os.Stat(local); err != nil || !st.IsDir() {
			return fmt.Errorf("%s does not exist or is not a folder", local)
		}
		if cfg.ProjectMap == nil {
			cfg.ProjectMap = map[string]string{}
		}
		for k := range cfg.ProjectMap { // one entry per bucket, whatever its casing
			if strings.EqualFold(k, bucket) {
				delete(cfg.ProjectMap, k)
			}
		}
		cfg.ProjectMap[bucket] = local
		if err := archive.SaveConfig(root, cfg); err != nil {
			return err
		}
		fmt.Printf("%s -> %s\nRun `claude-sessions import` to file it.\n", bucket, local)
		return nil

	case "unmap":
		if len(rest) != 2 {
			return fmt.Errorf("usage: claude-sessions config unmap <archived bucket>")
		}
		cfg, err := archive.LoadConfig(root)
		if err != nil {
			return err
		}
		found := false
		for k := range cfg.ProjectMap {
			if strings.EqualFold(k, rest[1]) {
				delete(cfg.ProjectMap, k)
				found = true
			}
		}
		if !found {
			return fmt.Errorf("%s is not mapped", rest[1])
		}
		// Saved even when now empty: a nil map would leave the old entries in the file.
		if err := archive.SaveConfig(root, cfg); err != nil {
			return err
		}
		fmt.Printf("%s unmapped\n", rest[1])
		return nil

	case "set-machine":
		if len(rest) != 2 {
			return fmt.Errorf("usage: claude-sessions config set-machine <name>")
		}
		name := safeMachine.Replace(strings.TrimSpace(rest[1]))
		if name == "" || !archive.ValidName(name) {
			return fmt.Errorf("%q cannot name a machine: use letters, digits, dots and dashes", rest[1])
		}
		cfg, err := archive.LoadConfig(root)
		if err != nil {
			return err
		}
		previous := machineName()
		cfg.Machine = name
		if err := archive.SaveConfig(root, cfg); err != nil {
			return err
		}
		fmt.Printf("This machine is now known as %s (saved in %s).\n", name, archive.ConfigPath(root))
		if !strings.EqualFold(previous, name) {
			fmt.Printf("It was %s. Run `claude-sessions push`, then `claude-sessions machines --forget %s`\n", previous, previous)
			fmt.Println("so the archive does not keep listing the old name as another machine.")
		}
		return nil

	default:
		return fmt.Errorf("unknown config action %q (try: show, set-destination, map, unmap, set-machine)", action)
	}
}

// positionalAfterAction keeps the arguments of map, unmap and set-machine from being
// read as flags: a bucket name on macOS or Linux starts with "-", and the command
// import prints for it failed exactly as printed. After the action only --claude-dir
// is a flag.
func positionalAfterAction(args []string) []string {
	for i, a := range args {
		if a != "map" && a != "unmap" && a != "set-machine" {
			continue
		}
		flags := append([]string{}, args[:i]...)
		var pos []string
		for j := i + 1; j < len(args); j++ {
			switch r := args[j]; {
			case r == "--":
			case r == "--claude-dir" || r == "-claude-dir":
				flags = append(flags, r)
				if j+1 < len(args) {
					j++
					flags = append(flags, args[j])
				}
			case strings.HasPrefix(r, "--claude-dir=") || strings.HasPrefix(r, "-claude-dir="):
				flags = append(flags, r)
			default:
				pos = append(pos, r)
			}
		}
		return append(append(flags, "--", a), pos...)
	}
	return args
}
