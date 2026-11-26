package main

import (
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// cliCommands is every command main runs, as usage lists it, but version, which has no
// flags. cliSurface fails for a listed command missing here, so a new one is read too.
var cliCommands = map[string]func([]string) error{
	"push": cmdPush, "import": cmdImport, "pull": cmdPull, "restore": cmdRestore, "machines": cmdMachines,
	"clean": cmdClean, "prune": cmdPrune, "render": cmdRender, "install": cmdInstall, "uninstall": cmdUninstall,
	"backup": cmdBackup, "ls": cmdLs, "search": cmdSearch, "stats": cmdStats, "doctor": cmdDoctor,
	"config": cmdConfig, "ui": cmdUI, "completion": cmdCompletion,
}

var (
	helpFlag   = regexp.MustCompile(`(?m)^[ \t]+-(\S+)`)
	helpConfig = regexp.MustCompile(`claude-sessions config (\S+)`)
)

// cliSurface is every command, flag and config action the CLI offers, read from its
// help: usage's commands, each command's -h flags, and config -h's actions.
func cliSurface(t *testing.T) map[string]bool {
	t.Helper()
	keys := map[string]bool{}
	out, _ := captureFile(t, &os.Stderr, func() error { usage(); return nil })
	_, list, ok := strings.Cut(out, "\ncommands:\n")
	if !ok {
		t.Fatalf("usage has no commands: list:\n%s", out)
	}
	list, _, _ = strings.Cut(list, "\n\n")
	for _, line := range strings.Split(list, "\n") {
		name := strings.Fields(line)[0]
		keys[name] = true
		if name == "version" {
			continue
		}
		cmd, ok := cliCommands[name]
		if !ok {
			t.Errorf("usage lists %q, which cliCommands does not run with -h: add it", name)
			continue
		}
		help, _ := captureFile(t, &os.Stderr, func() error { return cmd([]string{"-h"}) })
		for _, m := range helpFlag.FindAllStringSubmatch(help, -1) {
			keys[name+" --"+m[1]] = true
		}
		if name == "config" {
			for _, m := range helpConfig.FindAllStringSubmatch(help, -1) {
				keys["config "+strings.Trim(m[1], "[]")] = true
			}
		}
	}
	if len(keys) < 40 {
		t.Fatalf("read only %d keys from the CLI's help: %v", len(keys), keys)
	}
	return keys
}

// exempt reports whether the command map leaves key out on purpose: the key itself, its
// command or its flag is on mapExempt.
func exempt(key string) bool {
	cmd, flag, _ := strings.Cut(key, " ")
	return slices.Contains(mapExempt, key) || slices.Contains(mapExempt, cmd) || flag != "" && slices.Contains(mapExempt, flag)
}

func TestCommandMapCoversTheCLI(t *testing.T) {
	surface := cliSurface(t)
	covered := map[string]bool{}
	for _, e := range commandMap {
		for _, k := range e.Covers {
			covered[k] = true
			if !surface[k] {
				t.Errorf("the command map's row %q covers %q, which the CLI does not have", e.Place, k)
			}
		}
	}
	var missing []string
	for k := range surface {
		if !covered[k] && !exempt(k) {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("the panel has no control for these; give each a row in commandMap, or put it on mapExempt with a reason:\n  %s",
			strings.Join(missing, "\n  "))
	}
}

func TestCommandMapRoutesExist(t *testing.T) {
	u := newTestUI(t, uiOpts{})
	routes := map[string]bool{}
	for _, r := range u.routes() {
		routes[r.Path] = true
	}
	for _, e := range commandMap {
		if e.Route == "" && e.Screen == "" {
			continue // terminal only
		}
		if name, ok := strings.CutPrefix(e.Route, "/api/run/"); ok {
			if _, ok := uiActions[name]; !ok {
				t.Errorf("row %q runs %s, which is not an action", e.Place, e.Route)
			}
			continue
		}
		if !routes[e.Route] || strings.HasPrefix(e.Route, "/api/run/") {
			t.Errorf("row %q uses %s, which the panel does not serve", e.Place, e.Route)
		}
	}
}

func TestCommandMapExemptionsStillExist(t *testing.T) {
	surface := cliSurface(t)
	for _, x := range mapExempt {
		if x == "completion" {
			continue // its arguments are shells, not flags
		}
		found := surface[x]
		for k := range surface {
			found = found || strings.HasPrefix(x, "--") && strings.HasSuffix(k, " "+x)
		}
		if !found {
			t.Errorf("mapExempt lists %q, which the CLI no longer has: take it off", x)
		}
	}
}

func TestConfigActionsMatchTheCLI(t *testing.T) {
	var cli []string
	for k := range cliSurface(t) {
		if a, ok := strings.CutPrefix(k, "config "); ok && !strings.HasPrefix(a, "--") {
			cli = append(cli, a)
		}
	}
	panelActions := slices.Clone(panelConfigActions)
	sort.Strings(cli)
	sort.Strings(panelActions)
	if !slices.Equal(cli, panelActions) {
		t.Errorf("config -h lists %v; panelConfigActions is %v", cli, panelActions)
	}
}
