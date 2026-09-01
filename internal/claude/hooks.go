package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// HookNeedle identifies this tool's own SessionEnd hook.
//
// Matching is done on the whole command line. Note that "sync-claude-sessions.ps1"
// contains this string, so callers must exclude the PowerShell predecessor
// separately - see IsOurHook.
const HookNeedle = "claude-sessions"

// legacyNeedle names the PowerShell script this tool replaces.
const legacyNeedle = "sync-claude-sessions"

// InstallHook merges a SessionEnd hook into settings.json.
//
// settings.json belongs to the user, not to this tool: it may hold formatter hooks,
// permissions, model settings and keys no version of this program has heard of. The
// file is therefore round-tripped as a generic map, so every key that is not ours
// survives byte-for-byte in meaning, and a timestamped backup is written first.
//
// Idempotent: re-running replaces our own previous entry rather than adding a second.
// Returns how many existing entries were replaced.
func InstallHook(settingsPath, binary string, alsoRemoveLegacy bool) (replaced int, err error) {
	doc, err := loadGeneric(settingsPath)
	if err != nil {
		return 0, err
	}

	if _, err := os.Stat(settingsPath); err == nil {
		backup := fmt.Sprintf("%s.bak-%s", settingsPath, time.Now().Format("20060102-150405"))
		if data, err := os.ReadFile(settingsPath); err == nil {
			_ = os.WriteFile(backup, data, 0o600)
		}
	}

	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}

	existing, _ := hooks["SessionEnd"].([]any)
	kept := make([]any, 0, len(existing))
	for _, group := range existing {
		ours, legacy := classifyGroup(group)
		if ours || (legacy && alsoRemoveLegacy) {
			replaced++
			continue
		}
		kept = append(kept, group)
	}

	entry := map[string]any{
		"hooks": []any{map[string]any{
			"type":          "command",
			"command":       binary,
			"args":          []any{"push", "--quiet"},
			"timeout":       120,
			"statusMessage": "Archiving session",
		}},
	}

	hooks["SessionEnd"] = append(kept, entry)
	doc["hooks"] = hooks

	if err := writeGeneric(settingsPath, doc); err != nil {
		return replaced, err
	}

	// Prove it reads back, rather than trusting the write.
	check, err := loadGeneric(settingsPath)
	if err != nil {
		return replaced, fmt.Errorf("settings.json was written but no longer parses: %w", err)
	}
	if h, _ := check["hooks"].(map[string]any); h == nil {
		return replaced, fmt.Errorf("hook was written but could not be read back from %s", settingsPath)
	}
	return replaced, nil
}

// RemoveHook deletes this tool's SessionEnd hook, leaving everything else alone.
func RemoveHook(settingsPath string, alsoRemoveLegacy bool) (removed int, err error) {
	doc, err := loadGeneric(settingsPath)
	if err != nil {
		return 0, err
	}
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		return 0, nil
	}
	existing, _ := hooks["SessionEnd"].([]any)
	if existing == nil {
		return 0, nil
	}

	kept := make([]any, 0, len(existing))
	for _, group := range existing {
		ours, legacy := classifyGroup(group)
		if ours || (legacy && alsoRemoveLegacy) {
			removed++
			continue
		}
		kept = append(kept, group)
	}
	if removed == 0 {
		return 0, nil
	}

	if len(kept) > 0 {
		hooks["SessionEnd"] = kept
	} else {
		delete(hooks, "SessionEnd")
	}
	if len(hooks) > 0 {
		doc["hooks"] = hooks
	} else {
		delete(doc, "hooks")
	}
	return removed, writeGeneric(settingsPath, doc)
}

// classifyGroup reports whether a hook group is ours, or the PowerShell predecessor.
func classifyGroup(group any) (ours, legacy bool) {
	m, _ := group.(map[string]any)
	if m == nil {
		return false, false
	}
	inner, _ := m["hooks"].([]any)
	for _, h := range inner {
		hm, _ := h.(map[string]any)
		if hm == nil {
			continue
		}
		var parts []string
		if c, ok := hm["command"].(string); ok {
			parts = append(parts, c)
		}
		if as, ok := hm["args"].([]any); ok {
			for _, a := range as {
				if s, ok := a.(string); ok {
					parts = append(parts, s)
				}
			}
		}
		line := strings.ToLower(strings.Join(parts, " "))
		switch {
		case strings.Contains(line, legacyNeedle):
			legacy = true
		case strings.Contains(line, HookNeedle):
			ours = true
		}
	}
	return ours, legacy
}

func loadGeneric(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return map[string]any{}, nil
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s does not parse as JSON, refusing to touch it: %w", path, err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, nil
}

func writeGeneric(path string, doc map[string]any) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	// Temp file then rename: a settings.json truncated by a crash mid-write would
	// break every future Claude Code session.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-settings-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}
