package claude

import (
	"encoding/json"
	"os"
	"strings"
)

// DefaultCleanupPeriodDays is what Claude Code applies when the setting is absent.
//
// It is the reason an archive tool is load-bearing rather than a nicety: unset means
// local transcripts are deleted a month after they are written.
const DefaultCleanupPeriodDays = 30

// Settings is the read-only view of settings.json this tool needs.
//
// Writing settings.json is a different job with a different requirement - it must
// preserve every key and every other person's hooks - and belongs in the installer,
// which round-trips the file as a raw map rather than through this struct.
type Settings struct {
	CleanupPeriodDays *int                   `json:"cleanupPeriodDays"`
	Hooks             map[string][]HookGroup `json:"hooks"`
}

// HookGroup is one entry in a hook event's array.
type HookGroup struct {
	Matcher string `json:"matcher,omitempty"`
	Hooks   []Hook `json:"hooks"`
}

// Hook is a single command. Claude Code accepts both a shell-command string and the
// exec form with an args array; this tool always writes the exec form, because
// "shell": "powershell" invokes pwsh, which is absent on plenty of Windows machines.
type Hook struct {
	Type          string   `json:"type,omitempty"`
	Command       string   `json:"command,omitempty"`
	Args          []string `json:"args,omitempty"`
	Timeout       int      `json:"timeout,omitempty"`
	StatusMessage string   `json:"statusMessage,omitempty"`
}

// Mentions reports whether any part of the hook's command line contains needle,
// compared case-insensitively. Used to recognise our own installed hook - including
// an older PowerShell one - so install can replace it instead of adding a second.
func (h Hook) Mentions(needle string) bool {
	if strings.Contains(strings.ToLower(h.Command), strings.ToLower(needle)) {
		return true
	}
	for _, a := range h.Args {
		if strings.Contains(strings.ToLower(a), strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

// LoadSettings reads settings.json. A missing file is not an error: a machine that
// has never been configured is a normal starting state.
func LoadSettings(path string) (Settings, bool, error) {
	var s Settings
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, false, nil
	}
	if err != nil {
		return s, false, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return s, true, nil
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, true, err
	}
	return s, true, nil
}

// EffectiveCleanupPeriodDays resolves the setting against Claude Code's default.
func (s Settings) EffectiveCleanupPeriodDays() int {
	if s.CleanupPeriodDays == nil {
		return DefaultCleanupPeriodDays
	}
	return *s.CleanupPeriodDays
}

// SessionEndHooks returns every installed SessionEnd hook mentioning any needle.
//
// It returns all of them, not the first: two archivers installed at once - say this
// binary alongside the PowerShell script it replaces - both run at the end of every
// session and race on the manifest, and that is worth reporting rather than hiding
// behind the first match.
func (s Settings) SessionEndHooks(needles ...string) []Hook {
	var out []Hook
	for _, group := range s.Hooks["SessionEnd"] {
		for _, h := range group.Hooks {
			for _, n := range needles {
				if h.Mentions(n) {
					out = append(out, h)
					break
				}
			}
		}
	}
	return out
}
