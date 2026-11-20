package main

// The profile is what makes Claude behave the same on another machine: the global
// rules (CLAUDE.md and the files beside it that it @-includes), personal skills,
// agents, commands and output styles, and which plugins are enabled. push copies it
// to <archive>/profile; import and pull bring it back.
//
// Left out on purpose: skills/synced (claude.ai account skills, which Claude Code
// downloads itself on login), settings.json (its hooks run tools at paths that only
// exist on the machine that wrote them), credentials, caches and plugin installs.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
)

var profileDirs = []string{"skills", "agents", "commands", "output-styles"}

const noPluginsHelp = "do not install plugins that are enabled on another machine and missing here"

func profileDir(dest string) string { return filepath.Join(dest, "profile") }

// profileFiles lists the profile under base - a ~/.claude, or an archive's profile
// folder - relative to it with forward slashes. Regular files only, so a link planted
// in a shared archive is never read through. Dot-named entries (.trash, .git, .env)
// and node_modules are skipped: deleted skills, history, secrets and installs.
func profileFiles(base string) []string {
	var files []string
	if mds, err := filepath.Glob(filepath.Join(base, "*.md")); err == nil {
		for _, m := range mds {
			if info, err := os.Lstat(m); err == nil && info.Mode().IsRegular() {
				files = append(files, filepath.Base(m))
			}
		}
	}
	for _, d := range profileDirs {
		top := filepath.Join(base, d)
		if !archive.IsRealDir(top) {
			continue
		}
		_ = filepath.WalkDir(top, func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			name := e.Name()
			if path != top && (strings.HasPrefix(name, ".") || name == "node_modules" ||
				(d == "skills" && e.IsDir() && name == "synced" && filepath.Dir(path) == top)) {
				if e.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if e.Type().IsRegular() {
				if rel, err := filepath.Rel(base, path); err == nil {
					files = append(files, filepath.ToSlash(rel))
				}
			}
			return nil
		})
	}
	sort.Strings(files)
	return files
}

// pluginList is what one machine records about its plugins: which are enabled, and
// where their marketplaces come from. Values are kept as settings.json has them.
type pluginList struct {
	EnabledPlugins         map[string]any             `json:"enabledPlugins,omitempty"`
	ExtraKnownMarketplaces map[string]json.RawMessage `json:"extraKnownMarketplaces,omitempty"`
}

func readPluginList(path string) (pluginList, error) {
	var p pluginList
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	raw = []byte(strings.TrimPrefix(string(raw), "\xef\xbb\xbf"))
	if strings.TrimSpace(string(raw)) == "" {
		return p, nil
	}
	return p, json.Unmarshal(raw, &p)
}

// machineProfileDir is one machine's own copy of its profile. The shared copy is
// newest-wins across machines, so without this an older CLAUDE.md on one machine was
// simply absent from the backup once another machine had pushed a newer one.
func machineProfileDir(dest, machine string) string {
	return filepath.Join(profileDir(dest), "machines", machine)
}

// replacedDir is where an import keeps this machine's version of a rule or skill file
// it replaced, under the time of the import. No push ever writes there.
func replacedDir(dest, machine string) string {
	return filepath.Join(profileDir(dest), "replaced", machine, time.Now().Format("20060102-150405"))
}

// pushProfile copies the profile into the archive: into the shared copy, where the
// newer file wins as it does for memory, and into this machine's own copy, which no
// other machine writes. It never fails the push: the sessions are what must get
// through.
func pushProfile(root, dest string, dryRun bool, logf func(string, ...any)) (copied, unchanged int) {
	own := machineProfileDir(dest, machineName())
	for _, rel := range profileFiles(root) {
		src := filepath.Join(root, filepath.FromSlash(rel))
		dst := filepath.Join(profileDir(dest), filepath.FromSlash(rel))
		info, err := os.Stat(src)
		if err != nil {
			continue
		}
		if ownDst := filepath.Join(own, filepath.FromSlash(rel)); !dryRun && archive.NeedsCopy(info, archive.StatOrNil(ownDst)) {
			if err := archive.CopyFile(src, ownDst); err != nil {
				logf("WARN: profile/machines/%s: %v", rel, err)
			}
		}
		if !archive.NeedsCopy(info, archive.StatOrNil(dst)) {
			unchanged++
			continue
		}
		if !dryRun {
			if err := archive.CopyFile(src, dst); err != nil {
				logf("WARN: profile/%s: %v", rel, err)
				continue
			}
		}
		copied++
	}

	if dryRun {
		return copied, unchanged
	}
	list, err := readPluginList(claude.SettingsPath(root))
	if err != nil {
		logf("WARN: plugin list: settings.json: %v", err)
		return copied, unchanged
	}
	data, _ := json.MarshalIndent(list, "", "  ")
	if err := archive.WriteFileAtomic(filepath.Join(profileDir(dest), "plugins", machineName()+".json"), append(data, '\n'), 0o644); err != nil {
		logf("WARN: plugin list: %v", err)
	}
	return copied, unchanged
}

type profileResult struct {
	Files    []string    // profile files filed here
	Replaced []string    // of those, local files that differed and were replaced
	Plugins  []pluginRun // plugins enabled on another machine and missing here
}

type pluginRun struct {
	ID     string
	Args   []string // for `claude`
	Status string   // "installed", "would install", or why it needs running by hand
}

func (p pluginRun) Command() string { return "claude " + strings.Join(p.Args, " ") }

// restoreProfile files the archived profile into root. A file already here is kept
// unless update is set and the archived copy is newer, as for memory: these files are
// loaded as instructions, and the archive is written by other machines.
//
// Plugins enabled on any machine and not here are installed with `claude plugin
// install`, unless installPlugins is false.
func restoreProfile(root, dest string, dryRun, update, installPlugins bool) profileResult {
	var res profileResult
	src := profileDir(dest)
	if !archive.IsRealDir(src) {
		return res
	}
	keep := replacedDir(dest, machineName())
	for _, rel := range profileFiles(src) {
		from := filepath.Join(src, filepath.FromSlash(rel))
		to := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Stat(from)
		if err != nil || !shouldRestore(info, to, update) {
			continue
		}
		// A local file that differs is another machine's version winning over this
		// one's. Keep this machine's first, so it can be had back, and say so. Not in
		// profile/machines/: the next push rewrites that with the file now here.
		replacing := false
		if local, err := os.ReadFile(to); err == nil {
			archived, err := os.ReadFile(from)
			if err != nil || bytes.Equal(local, archived) {
				continue
			}
			replacing = true
		}
		if !dryRun {
			if replacing {
				if err := archive.CopyFile(to, filepath.Join(keep, filepath.FromSlash(rel))); err != nil {
					continue // never replace what could not be kept
				}
			}
			if err := archive.CopyFile(from, to); err != nil {
				continue
			}
		}
		if replacing {
			res.Replaced = append(res.Replaced, rel)
		}
		res.Files = append(res.Files, rel)
	}

	if !installPlugins {
		return res
	}
	local, _ := readPluginList(claude.SettingsPath(root))
	res.Plugins = missingPlugins(src, local)
	claudeBin, lookErr := exec.LookPath("claude")
	for i := range res.Plugins {
		p := &res.Plugins[i]
		switch {
		case dryRun:
			p.Status = "would install"
		case lookErr != nil:
			p.Status = "claude is not on PATH"
		default:
			p.Status = runPluginInstall(claudeBin, p.Args, root)
		}
	}
	return res
}

// shouldRestore is the rule for every file import and pull bring back next to the
// sessions: always add a missing file; replace one only on request, only with a newer
// copy, and never with one dated in the future - that would stay "newer" than any
// local edit forever.
func shouldRestore(src os.FileInfo, dst string, update bool) bool {
	// Checked first: a future-dated file filed because nothing was here yet stays
	// "newer" than every later local edit, which then never reaches the archive.
	if src.ModTime().After(time.Now().Add(time.Hour)) {
		return false
	}
	dstInfo := archive.StatOrNil(dst)
	if dstInfo == nil {
		return true
	}
	return update && archive.NeedsCopy(src, dstInfo)
}

// Names and sources from the archive become arguments to `claude`. No shell is
// involved, but a value starting with "-" would still be read as a flag.
var (
	pluginNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	githubRepoRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9._-]+$`)
	httpsURLRe   = regexp.MustCompile(`^https://[A-Za-z0-9][^\s]*$`)
)

// missingPlugins combines every machine's plugin list and returns the plugins enabled
// somewhere and not enabled locally, each with the arguments that install it.
func missingPlugins(profile string, local pluginList) []pluginRun {
	entries, err := os.ReadDir(filepath.Join(profile, "plugins"))
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	for id, v := range local.EnabledPlugins {
		if v != false {
			seen[strings.ToLower(id)] = true
		}
	}
	var out []pluginRun
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.EqualFold(filepath.Ext(e.Name()), ".json") || !archive.ValidName(e.Name()) {
			continue
		}
		list, err := readPluginList(filepath.Join(profile, "plugins", e.Name()))
		if err != nil {
			continue
		}
		ids := make([]string, 0, len(list.EnabledPlugins))
		for id := range list.EnabledPlugins {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if list.EnabledPlugins[id] == false || seen[strings.ToLower(id)] {
				continue
			}
			args, ok := pluginInstallArgs(id, list.ExtraKnownMarketplaces)
			if !ok {
				continue
			}
			seen[strings.ToLower(id)] = true
			out = append(out, pluginRun{ID: id, Args: args})
		}
	}
	return out
}

// pluginInstallArgs builds `claude plugin install` arguments for name@marketplace.
// A marketplace with a recorded GitHub or https source is installed from that source,
// which adds the marketplace if this machine lacks it; any other (the built-in
// official one) by id. A local-path marketplace is not portable and is skipped.
func pluginInstallArgs(id string, markets map[string]json.RawMessage) ([]string, bool) {
	name, market, ok := strings.Cut(id, "@")
	if !ok || !pluginNameRe.MatchString(name) || !pluginNameRe.MatchString(market) {
		return nil, false
	}
	raw, known := markets[market]
	if !known {
		return []string{"plugin", "install", id, "-y"}, true
	}
	var m struct {
		Source struct {
			Source string `json:"source"`
			Repo   string `json:"repo"`
			URL    string `json:"url"`
		} `json:"source"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return nil, false
	}
	switch {
	case m.Source.Source == "github" && githubRepoRe.MatchString(m.Source.Repo):
		return []string{"plugin", "install", name, "--marketplace", m.Source.Repo, "-y"}, true
	case (m.Source.Source == "git" || m.Source.Source == "url") && httpsURLRe.MatchString(m.Source.URL):
		return []string{"plugin", "install", name, "--marketplace", m.Source.URL, "-y"}, true
	}
	return nil, false
}

// pluginInstallTimeout bounds one install: it clones a repository.
const pluginInstallTimeout = 3 * time.Minute

func runPluginInstall(bin string, args []string, root string) string {
	ctx, cancel := context.WithTimeout(context.Background(), pluginInstallTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = pluginEnv(root, claude.IsDefaultRoot(root), os.Environ())
	cmd.Dir = root // not the caller's folder, whose .claude/settings.json would apply
	out, err := cmd.CombinedOutput()
	if err == nil {
		return "installed"
	}
	msg := strings.TrimSpace(string(out))
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	if msg == "" {
		msg = err.Error()
	}
	return "failed: " + msg
}

// pluginEnv points `claude` at the profile being imported into. The default profile is
// reached by leaving CLAUDE_CONFIG_DIR unset: set, Claude Code reads .claude.json from
// inside it rather than beside it. An inherited empty value is dropped too - with it,
// claude installed into its working directory.
func pluginEnv(root string, isDefault bool, environ []string) []string {
	var env []string
	for _, kv := range environ {
		if k, _, _ := strings.Cut(kv, "="); !strings.EqualFold(k, "CLAUDE_CONFIG_DIR") {
			env = append(env, kv)
		}
	}
	if !isDefault {
		env = append(env, "CLAUDE_CONFIG_DIR="+root)
	}
	return env
}

// printProfile reports what restoreProfile did.
func printProfile(res profileResult, dryRun bool) {
	if len(res.Files) == 0 && len(res.Plugins) == 0 {
		return
	}
	fmt.Printf("\nprofile: %s %d rule/skill file(s)", verbFor(dryRun), len(res.Files))
	if len(res.Files) > 0 {
		fmt.Printf(" (%s)", profileSummary(res.Files))
	}
	fmt.Println()
	for _, rel := range res.Replaced {
		fmt.Printf("  ! %s replaced: it differed from another machine's newer copy (this machine's is kept in the archive under profile/replaced/%s/)\n",
			rel, machineName())
	}
	for _, p := range res.Plugins {
		marker := "+"
		if p.Status != "installed" && p.Status != "would install" {
			marker = "x"
		}
		fmt.Printf("  %s plugin %s: %s\n", marker, p.ID, p.Status)
		if marker == "x" || dryRun {
			fmt.Printf("      %s\n", p.Command())
		}
	}
}

// profileDiffs lists profile files that more than one machine has archived with
// different content: what `import --update` would settle by taking the newest.
func profileDiffs(dest string) []string {
	machines, _ := os.ReadDir(filepath.Join(profileDir(dest), "machines"))
	first := map[string][]byte{}
	differs := map[string]bool{}
	for _, m := range machines {
		dir := filepath.Join(profileDir(dest), "machines", m.Name())
		if !archive.IsRealDir(dir) {
			continue
		}
		for _, rel := range profileFiles(dir) {
			body, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
			if err != nil {
				continue
			}
			if prev, seen := first[rel]; !seen {
				first[rel] = body
			} else if !bytes.Equal(prev, body) {
				differs[rel] = true
			}
		}
	}
	out := make([]string, 0, len(differs))
	for rel := range differs {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

// profileSummary names the top-level items: "CLAUDE.md, skills/graphify, ...".
func profileSummary(files []string) string {
	var names []string
	seen := map[string]bool{}
	for _, f := range files {
		parts := strings.SplitN(f, "/", 3)
		key := parts[0]
		if len(parts) > 1 {
			key = parts[0] + "/" + parts[1]
		}
		if !seen[key] {
			seen[key] = true
			names = append(names, key)
		}
	}
	if len(names) > 8 {
		names = append(names[:8], fmt.Sprintf("+%d more", len(names)-8))
	}
	return strings.Join(names, ", ")
}
