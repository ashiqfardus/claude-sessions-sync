package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
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
  claude-sessions config zip-sessions on|off       zip archived conversations idle for 30 days

flags:
`)
		fs.PrintDefaults()
	}
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := parseFlags(fs, positionalAfterAction(args), true); err != nil {
		return err
	}
	o := configOpts{ClaudeDir: *claudeDir}
	if rest := fs.Args(); len(rest) > 0 {
		o.Action, o.Args = rest[0], rest[1:]
	}
	if !*asJSON {
		_, err := runConfig(stdStreams(), o)
		return err
	}
	res, err := runConfig(jsonStreams(), o)
	if err != nil {
		return err
	}
	return encodeJSON(os.Stdout, res)
}

type configOpts struct {
	ClaudeDir, Action string // Action "" is show
	Args              []string
}

// configResult is what config printed, line by line, and for show the settings.
type configResult struct {
	Action string      `json:"action"`
	Lines  []string    `json:"lines"`
	View   *configView `json:"view,omitempty"`
}

// configView is config show's settings, defaults resolved.
type configView struct {
	ConfigFile    string            `json:"configFile"`
	Destination   string            `json:"destination"` // as saved, "" when not
	InUse         string            `json:"inUse"`       // the archive commands use, from Source
	Source        string            `json:"source"`
	Placement     string            `json:"placement"`
	WorkOn        bool              `json:"workOn"`
	WorkMap       map[string]string `json:"workMap"`
	ProjectMap    map[string]string `json:"projectMap"`
	WorkExclude   []string          `json:"workExclude"`
	RenderExclude []string          `json:"renderExclude"`
	ZipSessions   bool              `json:"zipSessions"`
	SweepEvery    int               `json:"sweepEvery"` // minutes, -1 for no sweep
}

// runConfig runs one config action, printing on s.Out as the CLI does.
func runConfig(s streams, o configOpts) (configResult, error) {
	if o.Action == "" {
		o.Action = "show"
	}
	var out bytes.Buffer
	w := io.MultiWriter(s.Out, &out)
	res := configResult{Action: o.Action}
	root, _, err := resolveRoot(o.ClaudeDir)
	if err == nil && o.Action == "show" {
		res.View, err = configShow(w, root)
	} else if err == nil {
		err = configAction(w, root, append([]string{o.Action}, o.Args...))
	}
	res.Lines = []string{}
	if out.Len() > 0 {
		res.Lines = strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	}
	return res, err
}

func configShow(w io.Writer, root string) (*configView, error) {
	cfg, err := archive.LoadConfig(root)
	if err != nil {
		return nil, err
	}
	dest, src, err := archive.ResolveDestination(root, "")
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(w, "config file  %s\n", archive.ConfigPath(root))
	if cfg.Destination == "" {
		fmt.Fprintf(w, "destination  (not set)\n")
	} else {
		fmt.Fprintf(w, "destination  %s\n", cfg.Destination)
	}
	if dest != "" && src != archive.SourceConfig {
		fmt.Fprintf(w, "in use       %s (%s)\n", dest, src)
		fmt.Fprintf(w, "\nRun `claude-sessions config set-destination %q` to make that permanent.\n", dest)
	}
	on := "on"
	if !cfg.WorkOn() {
		on = "off"
	}
	placement := cfg.WorkPlacement
	if placement == "" {
		placement = "home"
	}
	fmt.Fprintf(w, "work backup  %s\n", on)
	fmt.Fprintf(w, "placement    %s\n", placement)
	keys := make([]string, 0, len(cfg.WorkMap))
	for k := range cfg.WorkMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "work map     %s -> %s\n", k, cfg.WorkMap[k])
	}
	for _, x := range cfg.WorkExclude {
		fmt.Fprintf(w, "excluded     %s\n", x)
	}
	v := &configView{ConfigFile: archive.ConfigPath(root), Destination: cfg.Destination, InUse: dest,
		Source: string(src), Placement: placement, WorkOn: cfg.WorkOn(), WorkMap: cfg.WorkMap,
		ProjectMap: cfg.ProjectMap, WorkExclude: cfg.WorkExclude, RenderExclude: cfg.RenderExclude,
		ZipSessions: cfg.ZipOn(), SweepEvery: cfg.SweepEvery}
	if v.SweepEvery == 0 {
		v.SweepEvery = 30 // an install older than the setting, or none: the default
	}
	// {} and [], not null, for the panel.
	for _, m := range []*map[string]string{&v.WorkMap, &v.ProjectMap} {
		if *m == nil {
			*m = map[string]string{}
		}
	}
	for _, l := range []*[]string{&v.WorkExclude, &v.RenderExclude} {
		if *l == nil {
			*l = []string{}
		}
	}
	return v, nil
}

// configAction runs every action but show, printing on w.
func configAction(w io.Writer, root string, rest []string) (err error) {
	action := rest[0]
	switch action {
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
		fmt.Fprintf(w, "Destination set to %s\n", abs)
		fmt.Fprintf(w, "Saved in %s\n", archive.ConfigPath(root))
		return nil

	case "map":
		cfg, err := archive.LoadConfig(root)
		if err != nil {
			return err
		}
		if len(rest) == 1 {
			if len(cfg.ProjectMap) == 0 {
				fmt.Fprintln(w, "no projects mapped")
			}
			keys := make([]string, 0, len(cfg.ProjectMap))
			for k := range cfg.ProjectMap {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(w, "%s -> %s\n", k, cfg.ProjectMap[k])
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
		fmt.Fprintf(w, "%s -> %s\nRun `claude-sessions import` to file it.\n", bucket, local)
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
		fmt.Fprintf(w, "%s unmapped\n", rest[1])
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
		fmt.Fprintf(w, "This machine is now known as %s (saved in %s).\n", name, archive.ConfigPath(root))
		if !strings.EqualFold(previous, name) {
			fmt.Fprintf(w, "It was %s. Run `claude-sessions push`, then `claude-sessions machines --forget %s`\n", previous, previous)
			fmt.Fprintln(w, "so the archive does not keep listing the old name as another machine.")
		}
		return nil

	case "work":
		if len(rest) != 2 || (rest[1] != "on" && rest[1] != "off") {
			return fmt.Errorf("usage: claude-sessions config work on|off")
		}
		return saveWork(w, root, func(c *archive.Config) { c.WorkBackup = rest[1] }, "Work folder backup is "+rest[1])

	case "work-placement":
		if len(rest) != 2 || (rest[1] != "home" && rest[1] != "machine") {
			return fmt.Errorf("usage: claude-sessions config work-placement home|machine")
		}
		return saveWork(w, root, func(c *archive.Config) { c.WorkPlacement = rest[1] }, "Work folders are filed under "+rest[1])

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
		return saveWork(w, root, func(c *archive.Config) {
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
		return saveWork(w, root, func(c *archive.Config) {
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
		if err := saveWork(w, root, func(c *archive.Config) {
			if !slices.Contains(c.WorkExclude, folder) {
				c.WorkExclude = append(append([]string{}, c.WorkExclude...), folder)
			}
		}, folder+" will not be backed up"); err != nil {
			return err
		}
		dest, _, err := archive.ResolveDestination(root, "")
		if err != nil || dest == "" {
			fmt.Fprintln(w, "The archive is not reachable; the next push removes the archived copy.")
			return nil
		}
		if _, err := os.Stat(dest); err != nil {
			fmt.Fprintln(w, "The archive is not reachable; the next push removes the archived copy.")
			return nil
		}
		lock, err := archive.AcquireLockWait(root, 45*time.Second)
		if err != nil { // a push is running and rewrites the shard: let it do the removal
			fmt.Fprintln(w, "A push is running; the next push removes the archived copy.")
			return nil
		}
		defer lock.Release()
		gone, err := purgeWork(root, dest, machineName(), folder)
		for _, g := range gone {
			fmt.Fprintf(w, "removed %s\n", g)
		}
		if err != nil {
			return err
		}
		if len(gone) > 0 {
			fmt.Fprintln(w, "They may still be in the sync service's trash or version history.")
		} else {
			fmt.Fprintln(w, "Nothing archived under that folder.")
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
		return saveWork(w, root, func(c *archive.Config) {
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
			return listKeep(w, dest)
		}
		arg := expandHome(rest[1])
		if runtime.GOOS == "windows" && gitBashRewritten(arg) {
			return gitBashError(arg)
		}
		if action == "keep" {
			return keepOne(w, dest, arg)
		}
		return unkeepOne(w, dest, arg)

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
				fmt.Fprintln(w, "keep-days is off")
			} else {
				fmt.Fprintf(w, "keep-days is %d\n", r.KeepDays)
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
		if days != r.KeepDays {
			// Every computer's next run then only records: the grace day is shared, as the
			// setting is. Just after the last, so a change is never mistaken for none.
			at := time.Now().UTC()
			if r.KeepDaysSetAt != nil && !at.After(*r.KeepDaysSetAt) {
				at = r.KeepDaysSetAt.Add(time.Nanosecond)
			}
			r.KeepDays, r.KeepDaysSetAt = days, &at
		}
		if err := archive.SaveRetention(dest, r); err != nil {
			return err
		}
		if days == 0 {
			fmt.Fprintln(w, "keep-days is off: nothing is deleted for its age.")
			return nil
		}
		// The list as saved: another computer's edit in the same sync window may have
		// dropped an entry from it.
		fmt.Fprintf(w, "keep-days is %d: every computer deletes archived conversations idle longer, except those on the keep list:\n", days)
		if len(r.Keep) == 0 {
			fmt.Fprintln(w, "  nothing is kept")
		}
		for _, e := range r.Keep {
			fmt.Fprintln(w, "  "+keepLabel(e))
		}
		fmt.Fprintf(w, "Each computer starts a day after its first run with a new or lower setting; run `claude-sessions prune --older-than %dd` to see what it deletes.\n", days)
		return nil

	case "zip-sessions":
		if len(rest) != 2 || (rest[1] != "on" && rest[1] != "off") {
			return fmt.Errorf("usage: claude-sessions config zip-sessions on|off")
		}
		on := rest[1] == "on"
		return saveWork(w, root, func(c *archive.Config) { c.ZipSessions = &on }, "Zipping idle conversations is "+rest[1]+"; zips already made stay readable")

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

func listKeep(w io.Writer, dest string) error {
	r, err := archive.LoadRetention(dest)
	if err != nil {
		return err
	}
	if len(r.Keep) == 0 {
		fmt.Fprintln(w, "nothing is kept")
	}
	for _, e := range r.Keep {
		fmt.Fprintln(w, keepLabel(e))
	}
	return nil
}

func keepLabel(e archive.KeepEntry) string {
	switch {
	case e.Session != "":
		return "conversation " + e.Session
	case e.Remote != "": // printed without a token it may hold; stored as it is
		return "project with remote " + identity.RedactRemote(e.Remote)
	case e.ForRemote != "":
		return "project folder " + e.Leaf + ", kept with remote " + identity.RedactRemote(e.ForRemote)
	}
	return "project folder " + e.Leaf
}

// sameEntry reports whether two keep entries name the same thing.
func sameEntry(a, b archive.KeepEntry) bool {
	return a.Session != "" && strings.EqualFold(a.Session, b.Session) ||
		a.Leaf != "" && strings.EqualFold(a.Leaf, b.Leaf) ||
		identity.SameRemote(a.Remote, b.Remote)
}

// sameKeep is sameEntry for a project's own entries, whose folder names name the remote
// they were kept for: the same folder name kept for another remote is another entry.
func sameKeep(a, b archive.KeepEntry) bool {
	return sameEntry(a, b) && (a.ForRemote == b.ForRemote || identity.SameRemote(a.ForRemote, b.ForRemote))
}

// keepEntriesFor is what arg names: a conversation, or a project with its buckets. A
// project found by its remote is stored by each bucket's folder name too, so it stays
// kept once no bucket reads the remote: its folder deleted, moved or on a drive not
// plugged in, the remote changed, or the machines with it forgotten. That also keeps
// another repository of the same folder name: more, never less.
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
		if p := projects[b]; e.Remote != "" && p.Leaf != "" {
			if leaf := (archive.KeepEntry{Leaf: strings.ToLower(p.Leaf), ForRemote: e.Remote}); !slices.Contains(entries, leaf) {
				entries = append(entries, leaf)
			}
		}
	}
	return entries, buckets, projects, nil
}

func keepOne(w io.Writer, dest, arg string) error {
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
		// A folder name kept for a remote is an entry of its own, so unkeeping that
		// remote leaves the folder name kept by itself, or for another remote.
		if slices.ContainsFunc(r.Keep, func(k archive.KeepEntry) bool { return sameKeep(k, e) }) {
			fmt.Fprintf(w, "Already kept: %s\n", keepLabel(e))
			continue
		}
		r.Keep = append(r.Keep, e)
		added = true
		fmt.Fprintf(w, "Kept: %s\n", keepLabel(e))
	}
	if added {
		if err := archive.SaveRetention(dest, r); err != nil {
			return err
		}
	}
	for _, b := range buckets {
		p := projects[b]
		fmt.Fprintf(w, "  %s  %s  %s\n", b, p.Owner(), p.Path)
	}
	if len(buckets) > 0 {
		fmt.Fprintln(w, "A bucket of this project that a computer adds later is kept too.")
	}
	return nil
}

// unkeepOne removes every entry arg names: what keep would store for it, and an entry
// as listed (a remote or a folder name), so one for a project no longer in the archive
// can still go. Only that last matches a folder name kept for any remote: a path names
// one repository, not another of the same folder name. A remote goes with the folder
// names kept for it, either way round.
func unkeepOne(w io.Writer, dest, arg string) error {
	r, err := archive.LoadRetention(dest)
	if err != nil {
		return err
	}
	entries, buckets, projects, matchErr := keepEntriesFor(dest, arg)
	listed := archive.KeepEntry{Session: arg, Remote: arg, Leaf: arg}
	named := func(k archive.KeepEntry) bool {
		return sameEntry(k, listed) || slices.ContainsFunc(entries, func(e archive.KeepEntry) bool { return sameKeep(k, e) })
	}
	var remotes []string
	for _, k := range r.Keep {
		if named(k) {
			remotes = append(remotes, k.Remote, k.ForRemote)
		}
	}
	var gone []archive.KeepEntry
	r.Keep = slices.DeleteFunc(r.Keep, func(k archive.KeepEntry) bool {
		del := named(k) || slices.ContainsFunc(remotes, func(rm string) bool {
			return identity.SameRemote(rm, k.Remote) || identity.SameRemote(rm, k.ForRemote)
		})
		if del {
			gone = append(gone, k)
		}
		return del
	})
	if len(gone) == 0 {
		if matchErr != nil {
			return matchErr
		}
		return notKept(r, arg, buckets, projects)
	}
	if err := archive.SaveRetention(dest, r); err != nil {
		return err
	}
	for _, k := range gone {
		fmt.Fprintf(w, "No longer kept: %s\n", keepLabel(k))
	}
	return nil
}

// notKept is unkeep's error for arg, which names no entry: with the entries that keep
// its buckets all the same - a folder name kept for a remote they no longer read - and
// how to unkeep each.
func notKept(r archive.Retention, arg string, buckets []string, projects map[string]archive.Project) error {
	var msg strings.Builder
	for _, k := range r.Keep {
		if k.Session != "" || !slices.ContainsFunc(buckets, func(b string) bool { return keeps(k, projects[b], projects) }) {
			continue
		}
		by := k.Remote
		if by == "" {
			by = k.ForRemote
		}
		if by == "" {
			by = k.Leaf
		}
		fmt.Fprintf(&msg, "\n  %s: claude-sessions config unkeep %s", keepLabel(k), by)
	}
	if msg.Len() == 0 {
		return fmt.Errorf("%s is not on the keep list", arg)
	}
	return fmt.Errorf("%s is not on the keep list itself, but these entries still keep it; unkeep them by their remote or name:%s", arg, msg.String())
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

func saveWork(w io.Writer, root string, change func(*archive.Config), msg string) error {
	cfg, err := archive.LoadConfig(root)
	if err != nil {
		return err
	}
	change(&cfg)
	if err := archive.SaveConfig(root, cfg); err != nil {
		return err
	}
	fmt.Fprintln(w, msg)
	return nil
}

// positionalAfterAction keeps the arguments of map, unmap and set-machine from being
// read as flags: a bucket name on macOS or Linux starts with "-", and the command
// import prints for it failed exactly as printed. After the action only --claude-dir
// and --json (with two dashes) are flags.
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
			// Only the double-dash --json: -json is the bucket of a folder at /json.
			case strings.HasPrefix(r, "--claude-dir=") || strings.HasPrefix(r, "-claude-dir="),
				r == "--json" || strings.HasPrefix(r, "--json="):
				flags = append(flags, r)
			default:
				pos = append(pos, r)
			}
		}
		return append(append(flags, "--", a), pos...)
	}
	return args
}
