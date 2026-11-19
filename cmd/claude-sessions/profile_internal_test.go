package main

import (
	"encoding/json"
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
