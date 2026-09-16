package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// HookNeedle identifies this tool's own SessionEnd hook.
//
// Matching is done on the whole command line. Note that "sync-claude-sessions.ps1"
// contains this string, so callers must exclude the PowerShell predecessor
// separately - see IsArchiveHook.
const HookNeedle = "claude-sessions"

// legacyNeedle names the PowerShell script this tool replaces.
const legacyNeedle = "sync-claude-sessions"

// archiveCommand matches this tool's binary running push: the program name as a whole
// path component, then "push". Matching "claude-sessions" anywhere counted the user's
// own `claude-sessions render` hook, or a script under ~/dev/claude-sessions-sync, as
// ours - and install and uninstall deleted them.
var archiveCommand = regexp.MustCompile(`(?i)(^|[\\/"'\s])claude-sessions(\.exe)?["']?\s+push(\s|$)`)

// IsArchiveHook reports whether a hook command is this tool's push, and separately
// whether it is the PowerShell predecessor.
func IsArchiveHook(command string, args []string) (ours, legacy bool) {
	line := strings.TrimSpace(command + " " + strings.Join(args, " "))
	if strings.Contains(strings.ToLower(line), legacyNeedle) {
		return false, true
	}
	return archiveCommand.MatchString(line), false
}

// InstallHook merges a SessionEnd hook into settings.json.
//
// settings.json belongs to the user, not to this tool: it may hold formatter hooks,
// permissions, model settings and keys no version of this program has heard of. The
// file is round-tripped with its key order intact, only our own hook entries are
// touched, and a timestamped backup is written first.
//
// Idempotent: re-running replaces our own previous entry rather than adding a second.
// Returns how many existing entries were replaced.
func InstallHook(settingsPath, binary string, alsoRemoveLegacy bool) (replaced int, err error) {
	settingsPath = resolveLink(settingsPath)
	doc, err := loadGeneric(settingsPath)
	if err != nil {
		return 0, err
	}

	if data, err := os.ReadFile(settingsPath); err == nil {
		backup := fmt.Sprintf("%s.bak-%s", settingsPath, time.Now().Format("20060102-150405"))
		_ = os.WriteFile(backup, data, 0o600)
	}

	hooks, _ := doc.get("hooks").(*object)
	if hooks == nil {
		hooks = newObject()
	}
	existing, _ := hooks.get("SessionEnd").([]any)
	kept, replaced := withoutOurHooks(existing, alsoRemoveLegacy)

	entry := newObject()
	hook := newObject()
	hook.set("type", "command")
	hook.set("command", binary)
	hook.set("args", []any{"push", "--quiet"})
	hook.set("timeout", 120)
	hook.set("statusMessage", "Archiving session")
	entry.set("hooks", []any{hook})

	hooks.set("SessionEnd", append(kept, entry))
	doc.set("hooks", hooks)

	if err := writeGeneric(settingsPath, doc); err != nil {
		return replaced, err
	}

	// Prove it reads back, rather than trusting the write.
	check, err := loadGeneric(settingsPath)
	if err != nil {
		return replaced, fmt.Errorf("settings.json was written but no longer parses: %w", err)
	}
	if h, _ := check.get("hooks").(*object); h == nil {
		return replaced, fmt.Errorf("hook was written but could not be read back from %s", settingsPath)
	}
	return replaced, nil
}

// RemoveHook deletes this tool's SessionEnd hook, leaving everything else alone.
func RemoveHook(settingsPath string, alsoRemoveLegacy bool) (removed int, err error) {
	settingsPath = resolveLink(settingsPath)
	doc, err := loadGeneric(settingsPath)
	if err != nil {
		return 0, err
	}
	hooks, _ := doc.get("hooks").(*object)
	if hooks == nil {
		return 0, nil
	}
	existing, _ := hooks.get("SessionEnd").([]any)
	kept, removed := withoutOurHooks(existing, alsoRemoveLegacy)
	if removed == 0 {
		return 0, nil
	}

	if len(kept) > 0 {
		hooks.set("SessionEnd", kept)
	} else {
		hooks.del("SessionEnd")
	}
	if len(hooks.keys) == 0 {
		doc.del("hooks")
	}
	return removed, writeGeneric(settingsPath, doc)
}

// withoutOurHooks drops our hooks from each group, and a group only once nothing else
// is left in it. Dropping whole groups deleted other people's hooks that happened to
// share one with ours.
func withoutOurHooks(groups []any, alsoRemoveLegacy bool) (kept []any, removed int) {
	kept = make([]any, 0, len(groups))
	for _, g := range groups {
		group, _ := g.(*object)
		inner, _ := group.getHooks()
		if group == nil || inner == nil {
			kept = append(kept, g)
			continue
		}
		var rest []any
		for _, h := range inner {
			hm, _ := h.(*object)
			if hm != nil {
				command, _ := hm.get("command").(string)
				var args []string
				if as, ok := hm.get("args").([]any); ok {
					for _, a := range as {
						if s, ok := a.(string); ok {
							args = append(args, s)
						}
					}
				}
				if ours, legacy := IsArchiveHook(command, args); ours || (legacy && alsoRemoveLegacy) {
					removed++
					continue
				}
			}
			rest = append(rest, h)
		}
		if len(rest) == len(inner) {
			kept = append(kept, g)
		} else if len(rest) > 0 {
			group.set("hooks", rest)
			kept = append(kept, group)
		}
	}
	return kept, removed
}

func (o *object) getHooks() ([]any, bool) {
	if o == nil {
		return nil, false
	}
	inner, ok := o.get("hooks").([]any)
	return inner, ok
}

// resolveLink follows a symlinked settings.json (dotfiles managers keep it elsewhere)
// so the write replaces the real file instead of turning the link into a copy.
func resolveLink(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return path
}

func loadGeneric(path string) (*object, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return newObject(), nil
	}
	if err != nil {
		return nil, err
	}
	raw = []byte(strings.TrimPrefix(string(raw), "\xef\xbb\xbf"))
	if len(strings.TrimSpace(string(raw))) == 0 {
		return newObject(), nil
	}
	v, err := decodeOrdered(raw)
	if err != nil {
		return nil, fmt.Errorf("%s does not parse as JSON, refusing to touch it: %w", path, err)
	}
	doc, ok := v.(*object)
	if !ok {
		return nil, fmt.Errorf("%s is not a JSON object, refusing to touch it", path)
	}
	return doc, nil
}

func writeGeneric(path string, doc *object) error {
	data, err := encodeOrdered(doc)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	// Keep the file's permissions; a new file gets owner-only.
	perm := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}

	// Temp file then rename: a settings.json truncated by a crash mid-write would
	// break every future Claude Code session.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-settings-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	_ = os.Chmod(tmpName, perm)
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}
