package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// A plugin is installed into the profile being imported into. An inherited empty
// CLAUDE_CONFIG_DIR once made claude install into its working directory.
func TestPluginEnvTargetsTheImportedProfile(t *testing.T) {
	in := []string{"PATH=/bin", "CLAUDE_CONFIG_DIR=", "claude_config_dir=/stale"}
	got := strings.Join(pluginEnv("/p/.claude", false, in), " ")
	if got != "PATH=/bin CLAUDE_CONFIG_DIR=/p/.claude" {
		t.Errorf("other profile: %q", got)
	}
	if got := strings.Join(pluginEnv("/home/u/.claude", true, in), " "); got != "PATH=/bin" {
		t.Errorf("default profile must leave CLAUDE_CONFIG_DIR unset: %q", got)
	}
}

// Plugin ids and marketplace sources come from the shared archive and become
// arguments to `claude`. Anything that could be read as a flag or an odd git
// transport must be refused, not passed on.
func TestPluginInstallArgsRefusesUnsafeValues(t *testing.T) {
	markets := map[string]json.RawMessage{
		"gh":    json.RawMessage(`{"source":{"source":"github","repo":"DietrichGebert/ponytail"}}`),
		"git":   json.RawMessage(`{"source":{"source":"git","url":"https://github.com/addyosmani/agent-skills.git"}}`),
		"ext":   json.RawMessage(`{"source":{"source":"git","url":"ext::sh -c id"}}`),
		"flag":  json.RawMessage(`{"source":{"source":"github","repo":"--upload-pack=x/y"}}`),
		"local": json.RawMessage(`{"source":{"source":"directory","path":"C:\\plugins"}}`),
	}
	for _, c := range []struct {
		id   string
		want string // "" = refused
	}{
		{"ponytail@gh", "plugin install ponytail --marketplace DietrichGebert/ponytail -y"},
		{"agent-skills@git", "plugin install agent-skills --marketplace https://github.com/addyosmani/agent-skills.git -y"},
		{"code-review@claude-plugins-official", "plugin install code-review@claude-plugins-official -y"},
		{"x@ext", ""},
		{"x@flag", ""},
		{"x@local", ""},
		{"-rf@gh", ""},
		{"x@-y", ""},
		{"no-marketplace", ""},
	} {
		args, ok := pluginInstallArgs(c.id, markets)
		got := ""
		if ok {
			got = strings.Join(args, " ")
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.id, got, c.want)
		}
	}
}

func TestPluginErrorLinePicksTheUsefulLine(t *testing.T) {
	e := errors.New("exit 1")
	if g := pluginErrorLine("Adding marketplace...\nCloning...\nError: Failed to clone repository\ndone", e); g != "Error: Failed to clone repository" {
		t.Errorf("got %q", g)
	}
	if g := pluginErrorLine("Adding marketplace...\n\nstuff happened\n", e); g != "stuff happened" {
		t.Errorf("got %q", g)
	}
	if g := pluginErrorLine("", e); g != "exit 1" {
		t.Errorf("got %q", g)
	}
	if g := pluginErrorLine(strings.Repeat("x", 500), e); len([]rune(g)) > 205 {
		t.Errorf("not trimmed: %d", len(g))
	}
}

func TestRunPluginInstallRetriesOnce(t *testing.T) {
	oldRun, oldDelay := installPluginOnce, pluginRetryDelay
	defer func() { installPluginOnce, pluginRetryDelay = oldRun, oldDelay }()
	pluginRetryDelay = 0
	calls := 0
	installPluginOnce = func(string, []string, string) (string, error) {
		calls++
		if calls == 1 {
			return "Adding marketplace...\nerror: network", errors.New("x")
		}
		return "", nil
	}
	if got := runPluginInstall("claude", nil, ""); got != "installed" || calls != 2 {
		t.Errorf("got %q after %d calls", got, calls)
	}
	calls = 0
	installPluginOnce = func(string, []string, string) (string, error) {
		calls++
		return "Adding marketplace...\nerror: network", errors.New("x")
	}
	if got := runPluginInstall("claude", nil, ""); got != "failed: error: network" || calls != 2 {
		t.Errorf("got %q after %d calls", got, calls)
	}
}

func TestPluginSummaryAndSizeText(t *testing.T) {
	var b strings.Builder
	printPluginSummary(&b, profileResult{Plugins: []pluginRun{{Status: "installed"}, {Status: "failed: x"}, {Status: "claude is not on PATH"}}})
	if b.String() != "! 2 plugin(s) not installed; run the commands above, one at a time\n" {
		t.Errorf("got %q", b.String())
	}
	b.Reset()
	printPluginSummary(&b, profileResult{Plugins: []pluginRun{{Status: "installed"}}})
	if b.Len() != 0 {
		t.Errorf("printed %q", b.String())
	}
	for n, want := range map[int64]string{0: "0 bytes", 1023: "1023 bytes", 12 << 10: "12 KB", 464<<20 + 1<<19: "464.5 MB", 200 << 10: "0.2 MB"} {
		if got := sizeText(n); got != want {
			t.Errorf("sizeText(%d)=%q want %q", n, got, want)
		}
	}
}
