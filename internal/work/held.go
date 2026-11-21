package work

import (
	"encoding/json"
	"os"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
)

// HeldFile is a file restored from another machine, as it was written here. While
// the local file still has this size and time, it is that machine's, not this one's.
type HeldFile struct {
	Machine string    `json:"machine"`
	From    string    `json:"from"` // its path on that machine
	Size    int64     `json:"size"`
	MTime   time.Time `json:"mtime"`
}

// Held is the local record of restored work: Files by local path, and Placed, the
// local target of each placed folder, keyed machine + "|" + its source path.
type Held struct {
	Files  map[string]HeldFile `json:"files"`
	Placed map[string]string   `json:"placed"`
}

// LoadHeld reads the record at path. Missing or unreadable gives empty maps.
func LoadHeld(path string) Held {
	var h Held
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &h)
	}
	if h.Files == nil {
		h.Files = map[string]HeldFile{}
	}
	if h.Placed == nil {
		h.Placed = map[string]string{}
	}
	return h
}

// Save writes the record to path.
func (h Held) Save(path string) error {
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	return archive.WriteFileAtomic(path, append(data, '\n'), 0o644)
}
