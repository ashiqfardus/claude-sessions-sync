package archive

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MinKeepDays is the shortest age rule allowed: a typo must not empty the archive.
const MinKeepDays = 90

// MaxKeepDays is 100 years. Far more would overflow a time.Duration, and every
// conversation would then look old enough to delete.
const MaxKeepDays = 36500

// KeepEntry is one thing prune and the age rule never delete. Exactly one field is set:
// a conversation, or a project by its git remote or, when it has none, its folder name.
// A project is never stored by bucket or path, so it covers every computer's bucket.
type KeepEntry struct {
	Session string `json:"session,omitempty"`
	Remote  string `json:"remote,omitempty"`
	Leaf    string `json:"leaf,omitempty"`
	// ForRemote, on a Leaf entry, is the remote of the project kept with it: the folder
	// name keeps that project once no bucket reads the remote any more. A label only.
	ForRemote string `json:"forRemote,omitempty"`
}

// Retention is the archive's shared retention settings, retention.json at its root.
// Every machine follows the same file.
type Retention struct {
	KeepDays int `json:"keepDays,omitempty"`
	// KeepDaysSetAt is when config last changed KeepDays, in UTC: every computer's
	// first run since only records its time. nil in a file an earlier build wrote.
	KeepDaysSetAt *time.Time  `json:"keepDaysSetAt,omitempty"`
	Keep          []KeepEntry `json:"keep,omitempty"`
}

// RetentionPath is <archive>/retention.json.
func RetentionPath(dest string) string { return filepath.Join(dest, "retention.json") }

// LoadRetention reads retention.json. A missing file is the zero value. Anything else
// that is not a valid file - unreadable, empty, malformed, or holding values config
// would refuse - is an error, and callers then delete nothing: a half-synced file must
// never read as "nothing is kept".
func LoadRetention(dest string) (Retention, error) {
	var r Retention
	raw, err := os.ReadFile(RetentionPath(dest))
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return Retention{}, fmt.Errorf("%s: %w", RetentionPath(dest), err)
	}
	if r.KeepDays < 0 || r.KeepDays > 0 && r.KeepDays < MinKeepDays || r.KeepDays > MaxKeepDays {
		return Retention{}, fmt.Errorf("%s: keepDays %d is outside %d to %d", RetentionPath(dest), r.KeepDays, MinKeepDays, MaxKeepDays)
	}
	for _, e := range r.Keep {
		set := 0
		for _, v := range []string{e.Session, e.Remote, e.Leaf} {
			if strings.TrimSpace(v) != "" {
				set++
			}
		}
		if set != 1 {
			return Retention{}, fmt.Errorf("%s: a keep entry must name exactly one conversation, remote or folder: %+v", RetentionPath(dest), e)
		}
	}
	return r, nil
}

// RetentionConflicts are the files beside retention.json a sync app makes of it when two
// computers change it at once - "retention (1).json", "retention-LAP-2.json",
// "retention.sync-conflict-<time>-<id>.json" - by the one rule they share: a name that
// starts with retention and ends with .json. Either may hold what the other lost.
func RetentionConflicts(dest string) ([]string, error) {
	entries, err := os.ReadDir(dest)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if n := strings.ToLower(e.Name()); n != "retention.json" && strings.HasPrefix(n, "retention") && strings.HasSuffix(n, ".json") {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// SaveRetention writes retention.json atomically.
func SaveRetention(dest string, r Retention) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(RetentionPath(dest), append(data, '\n'), 0o644)
}
