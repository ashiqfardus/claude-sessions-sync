package claude

import (
	"encoding/json"
	"os"
	"strings"
)

// ProjectPaths returns the absolute project paths recorded in ~/.claude.json.
//
// This is the fallback identity source for a bucket that holds memory files but no
// transcript: with no session to read a cwd from, the bucket would otherwise be
// archived but unroutable - backed up, yet impossible to place on another machine.
//
// Read-only, and never copied to an archive: a live session rewrites this file on
// exit, and restoring it over a working profile causes more harm than it fixes.
//
// Only the keys of the "projects" object are decoded. The values hold per-project
// state that may include prompt history, and there is no reason to load it.
func ProjectPaths(statePath string) ([]string, error) {
	raw, err := os.ReadFile(statePath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var doc struct {
		Projects map[string]json.RawMessage `json:"projects"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}

	paths := make([]string, 0, len(doc.Projects))
	for p := range doc.Projects {
		if strings.TrimSpace(p) != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// MatchBucket finds the path whose slug equals bucket, compared case-insensitively.
//
// Casing is compared loosely because the slug follows the string Claude recorded, not
// what is on disk - E:\ has been seen recorded as "e--".
func MatchBucket(bucket string, paths []string) (string, bool) {
	for _, p := range paths {
		if strings.EqualFold(Slug(p), bucket) {
			return p, true
		}
	}
	return "", false
}
