package archive

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DeletedEntry is one conversation prune deleted from the archive or, with Undone, took
// back off the list. LastUUID and LastAt are its newest copy's last turn: push skips a
// local copy no newer than that, and archives one continued since.
type DeletedEntry struct {
	ID        string    `json:"id"`
	Buckets   []string  `json:"buckets,omitempty"`
	LastUUID  string    `json:"lastUuid,omitempty"`
	LastAt    time.Time `json:"lastAt"`
	DeletedAt time.Time `json:"deletedAt"`
	Undone    bool      `json:"undone,omitempty"`
}

// deletedFile is deleted/<machine>.json. Each machine writes only its own, so two
// machines never overwrite each other's through the sync client; readers take the union.
type deletedFile struct {
	SchemaVersion int            `json:"schemaVersion"`
	Machine       string         `json:"machine"`
	Entries       []DeletedEntry `json:"entries"`
}

// DeletedDir is <archive>/deleted.
func DeletedDir(dest string) string { return filepath.Join(dest, "deleted") }

// LoadDeleted is every machine's deleted list, keyed by lower-cased conversation id. Per
// id the latest DeletedAt wins, an undo on a tie. A file not named <machine>.json by its
// own machine - a sync client's conflict copy - is ignored; one that cannot be read is
// an error, as it may hold the entry that says a conversation is deleted.
func LoadDeleted(dest string) (map[string]DeletedEntry, error) {
	out := map[string]DeletedEntry{}
	ents, err := os.ReadDir(DeletedDir(dest))
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		machine, ok := strings.CutSuffix(e.Name(), ".json")
		// push never puts a space in a machine name; a conflict copy ("LAP-1 (1)") has one.
		if !ok || !e.Type().IsRegular() || !ValidName(machine) || strings.Contains(machine, " ") {
			continue
		}
		f, err := readDeleted(filepath.Join(DeletedDir(dest), e.Name()))
		if err != nil {
			return nil, err
		}
		if !ownFile(e.Name(), &f.Machine) {
			continue
		}
		for _, d := range f.Entries {
			id := strings.ToLower(d.ID)
			if !ValidName(id) {
				continue
			}
			d.ID = id
			if was, seen := out[id]; !seen || d.DeletedAt.After(was.DeletedAt) || d.DeletedAt.Equal(was.DeletedAt) && d.Undone {
				out[id] = d
			}
		}
	}
	return out, nil
}

func readDeleted(path string) (deletedFile, error) {
	var f deletedFile
	raw, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// RecordDeleted appends entries to this machine's deleted/<machine>.json, atomically. A
// file of its own that cannot be read is an error, never written over.
func RecordDeleted(dest, machine string, add []DeletedEntry) error {
	if !ValidName(machine) {
		return fmt.Errorf("%q is not a machine name", machine)
	}
	path := filepath.Join(DeletedDir(dest), machine+".json")
	f, err := readDeleted(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	f.SchemaVersion, f.Machine = SchemaVersion, machine
	f.Entries = append(f.Entries, add...)
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, append(data, '\n'), 0o644)
}
