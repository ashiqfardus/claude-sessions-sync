package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/identity"
	"github.com/ashiqfardus/claude-sessions-sync/internal/rewrite"
	"github.com/ashiqfardus/claude-sessions-sync/internal/work"
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
  claude-sessions config work on|off               back up folders Claude worked in outside git
  claude-sessions config work-placement home|machine  where import files a folder with no local home
  claude-sessions config map-folder <path> <folder>   say where an archived work folder lives here
  claude-sessions config unmap-folder <path>       remove that mapping
  claude-sessions config exclude-folder <folder>   never back up a folder, and remove its archived copy
  claude-sessions config unexclude-folder <folder> back up a folder again
  claude-sessions config keep                      list what prune and keep-days never delete
  claude-sessions config keep <id|path|name>       never delete a conversation, or a project on every computer
  claude-sessions config unkeep <id|path|name>     take it off the keep list
  claude-sessions config keep-days <N|off>         delete archived conversations idle N days (N >= 90; off by default)

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
		on := "on"
		if !cfg.WorkOn() {
			on = "off"
		}
		placement := cfg.WorkPlacement
		if placement == "" {
			placement = "home"
		}
		fmt.Printf("work backup  %s\n", on)
		fmt.Printf("placement    %s\n", placement)
		keys := make([]string, 0, len(cfg.WorkMap))
		for k := range cfg.WorkMap {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("work map     %s -> %s\n", k, cfg.WorkMap[k])
		}
		for _, x := range cfg.WorkExclude {
			fmt.Printf("excluded     %s\n", x)
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
		// The old name stays this machine's: its files in the archive carry it.
		var former []string
		for _, f := range append(cfg.FormerMachines, previous) {
			if !strings.EqualFold(f, name) && !slices.ContainsFunc(former, func(x string) bool { return strings.EqualFold(x, f) }) {
				former = append(former, f)
			}
		}
		cfg.FormerMachines = former
		if err := archive.SaveConfig(root, cfg); err != nil {
			return err
		}
		fmt.Printf("This machine is now known as %s (saved in %s).\n", name, archive.ConfigPath(root))
		if !strings.EqualFold(previous, name) {
			fmt.Printf("It was %s. Run `claude-sessions push`, then `claude-sessions machines --forget %s`\n", previous, previous)
			fmt.Println("so the archive does not keep listing the old name as another machine.")
		}
		return nil

	case "work":
		if len(rest) != 2 || (rest[1] != "on" && rest[1] != "off") {
			return fmt.Errorf("usage: claude-sessions config work on|off")
		}
		return saveWork(root, func(c *archive.Config) { c.WorkBackup = rest[1] }, "Work folder backup is "+rest[1])

	case "work-placement":
		if len(rest) != 2 || (rest[1] != "home" && rest[1] != "machine") {
			return fmt.Errorf("usage: claude-sessions config work-placement home|machine")
		}
		return saveWork(root, func(c *archive.Config) { c.WorkPlacement = rest[1] }, "Work folders are filed under "+rest[1])

	case "map-folder":
		if len(rest) != 3 {
			return fmt.Errorf("usage: claude-sessions config map-folder <original path> <local folder>")
		}
		if runtime.GOOS == "windows" && gitBashRewritten(rest[1]) {
			return gitBashError(rest[1])
		}
		if !rewrite.AbsoluteAnywhere(rest[1]) {
			return fmt.Errorf("%q is not an absolute path: give the folder's path as it was on the machine that archived it", rest[1])
		}
		local, err := filepath.Abs(expandHome(rest[2]))
		if err != nil {
			return err
		}
		if st, err := os.Stat(local); err != nil || !st.IsDir() {
			return fmt.Errorf("%s does not exist or is not a folder", local)
		}
		return saveWork(root, func(c *archive.Config) {
			m := map[string]string{}
			for k, v := range c.WorkMap {
				if k != rest[1] {
					m[k] = v
				}
			}
			m[rest[1]] = local
			c.WorkMap = m
		}, rest[1]+" -> "+local)

	case "unmap-folder":
		if len(rest) != 2 {
			return fmt.Errorf("usage: claude-sessions config unmap-folder <original path>")
		}
		cfg, err := archive.LoadConfig(root)
		if err != nil {
			return err
		}
		if _, ok := cfg.WorkMap[rest[1]]; !ok {
			// A key Git Bash rewrote that is mapped is one it stored earlier: removable.
			if runtime.GOOS == "windows" && gitBashRewritten(rest[1]) {
				return gitBashError(rest[1])
			}
			return fmt.Errorf("%s is not mapped", rest[1])
		}
		return saveWork(root, func(c *archive.Config) {
			m := map[string]string{} // non-nil, so the saved file drops the old entry
			for k, v := range c.WorkMap {
				if k != rest[1] {
					m[k] = v
				}
			}
			c.WorkMap = m
		}, rest[1]+" unmapped")

	case "exclude-folder":
		if len(rest) != 2 {
			return fmt.Errorf("usage: claude-sessions config exclude-folder <folder>")
		}
		folder := expandHome(rest[1])
		if !rewrite.AbsoluteAnywhere(folder) {
			return fmt.Errorf("%q is not an absolute path", rest[1])
		}
		if !work.WindowsPath(folder) {
			if folder, err = filepath.Abs(folder); err != nil {
				return err
			}
		}
		if err := saveWork(root, func(c *archive.Config) {
			if !slices.Contains(c.WorkExclude, folder) {
				c.WorkExclude = append(append([]string{}, c.WorkExclude...), folder)
			}
		}, folder+" will not be backed up"); err != nil {
			return err
		}
		dest, _, err := archive.ResolveDestination(root, "")
		if err != nil || dest == "" {
			fmt.Println("The archive is not reachable; the next push removes the archived copy.")
			return nil
		}
		if _, err := os.Stat(dest); err != nil {
			fmt.Println("The archive is not reachable; the next push removes the archived copy.")
			return nil
		}
		lock, err := archive.AcquireLockWait(root, 45*time.Second)
		if err != nil { // a push is running and rewrites the shard: let it do the removal
			fmt.Println("A push is running; the next push removes the archived copy.")
			return nil
		}
		defer lock.Release()
		gone, err := purgeWork(root, dest, machineName(), folder)
		for _, g := range gone {
			fmt.Printf("removed %s\n", g)
		}
		if err != nil {
			return err
		}
		if len(gone) > 0 {
			fmt.Println("They may still be in the sync service's trash or version history.")
		} else {
			fmt.Println("Nothing archived under that folder.")
		}
		return nil

	case "unexclude-folder":
		if len(rest) != 2 {
			return fmt.Errorf("usage: claude-sessions config unexclude-folder <folder>")
		}
		cfg, err := archive.LoadConfig(root)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(cfg.WorkExclude, func(x string) bool { return x == rest[1] || x == expandHome(rest[1]) }) {
			return fmt.Errorf("%s is not excluded", rest[1])
		}
		return saveWork(root, func(c *archive.Config) {
			keep := []string{} // non-nil, so the saved file drops the old entry
			for _, x := range c.WorkExclude {
				if x != rest[1] && x != expandHome(rest[1]) {
					keep = append(keep, x)
				}
			}
			c.WorkExclude = keep
		}, rest[1]+" will be backed up again")

	case "keep", "unkeep":
		if len(rest) > 2 || action == "unkeep" && len(rest) != 2 {
			return fmt.Errorf("usage: claude-sessions config %s <conversation id | project path | project name>", action)
		}
		dest, err := retentionDest(root)
		if err != nil {
			return err
		}
		if len(rest) == 1 {
			return listKeep(dest)
		}
		arg := expandHome(rest[1])
		if runtime.GOOS == "windows" && gitBashRewritten(arg) {
			return gitBashError(arg)
		}
		if action == "keep" {
			return keepOne(dest, arg)
		}
		return unkeepOne(dest, arg)

	case "keep-days":
		dest, err := retentionDest(root)
		if err != nil {
			return err
		}
		r, err := archive.LoadRetention(dest)
		if err != nil {
			return err
		}
		if len(rest) == 1 {
			if r.KeepDays == 0 {
				fmt.Println("keep-days is off")
			} else {
				fmt.Printf("keep-days is %d\n", r.KeepDays)
			}
			return nil
		}
		if len(rest) != 2 {
			return fmt.Errorf("usage: claude-sessions config keep-days <N|off>")
		}
		days := 0
		if rest[1] != "off" {
			if days, err = strconv.Atoi(rest[1]); err != nil {
				return fmt.Errorf("usage: claude-sessions config keep-days <N|off>: %q is not a number of days", rest[1])
			}
			if days < archive.MinKeepDays {
				return fmt.Errorf("keep-days must be at least %d", archive.MinKeepDays)
			}
			if days > archive.MaxKeepDays {
				return fmt.Errorf("keep-days must be at most %d", archive.MaxKeepDays)
			}
		}
		r.KeepDays = days
		if err := archive.SaveRetention(dest, r); err != nil {
			return err
		}
		if days == 0 {
			fmt.Println("keep-days is off: nothing is deleted for its age.")
		} else {
			fmt.Printf("keep-days is %d: every computer deletes archived conversations idle longer, except those on the keep list.\n", days)
		}
		return nil

	default:
		return fmt.Errorf("unknown config action %q (try: show, set-destination, map, unmap, set-machine, work, work-placement, map-folder, unmap-folder, exclude-folder, unexclude-folder, keep, unkeep, keep-days)", action)
	}
}

// retentionDest is the archive whose retention.json config edits. It must exist: the
// file is shared, and writing it into a folder that is not there would create one.
func retentionDest(root string) (string, error) {
	dest, _, err := archive.ResolveDestination(root, "")
	if err != nil {
		return "", err
	}
	if dest == "" {
		return "", fmt.Errorf("no archive destination: run `claude-sessions config set-destination <folder>`")
	}
	if st, err := os.Stat(dest); err != nil || !st.IsDir() {
		return "", fmt.Errorf("the archive %s is not reachable", dest)
	}
	return dest, nil
}

func listKeep(dest string) error {
	r, err := archive.LoadRetention(dest)
	if err != nil {
		return err
	}
	if len(r.Keep) == 0 {
		fmt.Println("nothing is kept")
	}
	for _, e := range r.Keep {
		fmt.Println(keepLabel(e))
	}
	return nil
}

func keepLabel(e archive.KeepEntry) string {
	switch {
	case e.Session != "":
		return "conversation " + e.Session
	case e.Remote != "":
		return "project with remote " + e.Remote
	}
	return "project folder " + e.Leaf
}

// sameEntry reports whether two keep entries name the same thing.
func sameEntry(a, b archive.KeepEntry) bool {
	return a.Session != "" && strings.EqualFold(a.Session, b.Session) ||
		a.Leaf != "" && strings.EqualFold(a.Leaf, b.Leaf) ||
		identity.SameRemote(a.Remote, b.Remote)
}

// keepEntriesFor is what arg names: a conversation, or a project with its buckets. A
// project found by its remote whose match includes buckets with none is stored by its
// folder name too, so those stay kept once the machines with the remote are forgotten.
func keepEntriesFor(dest, arg string) ([]archive.KeepEntry, []string, map[string]archive.Project, error) {
	if claude.IsSessionID(arg) {
		return []archive.KeepEntry{{Session: strings.ToLower(arg)}}, nil, nil, nil
	}
	if arg == "." || arg == ".." || !rewrite.AbsoluteAnywhere(arg) && strings.ContainsAny(arg, `/\`) {
		abs, err := filepath.Abs(arg) // `config keep .` in the project
		if err != nil {
			return nil, nil, nil, err
		}
		arg = abs
	}
	projects, err := projectsOf(dest)
	if err != nil {
		return nil, nil, nil, err
	}
	e, buckets, err := matchProject(projects, arg)
	if err != nil {
		return nil, nil, nil, err
	}
	entries := []archive.KeepEntry{e}
	for _, b := range buckets {
		if p := projects[b]; e.Remote != "" && strings.TrimSpace(p.Remote) == "" && p.Leaf != "" {
			if leaf := (archive.KeepEntry{Leaf: strings.ToLower(p.Leaf)}); !slices.Contains(entries, leaf) {
				entries = append(entries, leaf)
			}
		}
	}
	return entries, buckets, projects, nil
}

func keepOne(dest, arg string) error {
	r, err := archive.LoadRetention(dest)
	if err != nil {
		return err
	}
	entries, buckets, projects, err := keepEntriesFor(dest, arg)
	if err != nil {
		return err
	}
	added := false
	for _, e := range entries {
		if slices.ContainsFunc(r.Keep, func(k archive.KeepEntry) bool { return sameEntry(k, e) }) {
			fmt.Printf("Already kept: %s\n", keepLabel(e))
			continue
		}
		r.Keep = append(r.Keep, e)
		added = true
		fmt.Printf("Kept: %s\n", keepLabel(e))
	}
	if added {
		if err := archive.SaveRetention(dest, r); err != nil {
			return err
		}
	}
	for _, b := range buckets {
		p := projects[b]
		fmt.Printf("  %s  %s  %s\n", b, p.Owner(), p.Path)
	}
	if len(buckets) > 0 {
		fmt.Println("A bucket of this project that a computer adds later is kept too.")
	}
	return nil
}

// unkeepOne removes every entry arg names: what keep would store for it, and an entry
// as listed (a remote or a folder name), so one for a project no longer in the archive
// can still go.
func unkeepOne(dest, arg string) error {
	r, err := archive.LoadRetention(dest)
	if err != nil {
		return err
	}
	entries, _, _, matchErr := keepEntriesFor(dest, arg)
	entries = append(entries, archive.KeepEntry{Session: arg, Remote: arg, Leaf: arg})
	n := len(r.Keep)
	r.Keep = slices.DeleteFunc(r.Keep, func(k archive.KeepEntry) bool {
		return slices.ContainsFunc(entries, func(e archive.KeepEntry) bool { return sameEntry(k, e) })
	})
	if len(r.Keep) == n {
		if matchErr != nil {
			return matchErr
		}
		return fmt.Errorf("%s is not on the keep list", arg)
	}
	if err := archive.SaveRetention(dest, r); err != nil {
		return err
	}
	fmt.Printf("No longer kept: %s\n", arg)
	return nil
}

// saveWork loads the config, applies change and saves it.
// gitBashRewritten reports whether p looks like a Unix path Git Bash (MSYS) rewrote
// before this program saw it: /Users/me/SQL arrives as C:/Program Files/Git/Users/me/SQL.
func gitBashRewritten(p string) bool {
	return work.WindowsPath(p) && strings.Contains(strings.ToLower(strings.ReplaceAll(p[2:], `\`, "/")), "/program files/git/")
}

func gitBashError(p string) error {
	return fmt.Errorf("%s looks like Git Bash rewrote the path; run it from PowerShell or prefix it with MSYS_NO_PATHCONV=1", p)
}

func saveWork(root string, change func(*archive.Config), msg string) error {
	cfg, err := archive.LoadConfig(root)
	if err != nil {
		return err
	}
	change(&cfg)
	if err := archive.SaveConfig(root, cfg); err != nil {
		return err
	}
	fmt.Println(msg)
	return nil
}

// positionalAfterAction keeps the arguments of map, unmap and set-machine from being
// read as flags: a bucket name on macOS or Linux starts with "-", and the command
// import prints for it failed exactly as printed. After the action only --claude-dir
// is a flag.
var positionalActions = []string{"map", "unmap", "set-machine", "work", "work-placement", "map-folder", "unmap-folder", "exclude-folder", "unexclude-folder", "keep", "unkeep", "keep-days"}

func positionalAfterAction(args []string) []string {
	for i, a := range args {
		if !slices.Contains(positionalActions, a) {
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
