package archive

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SchemaVersion is stamped into every shard this tool writes.
const SchemaVersion = 1

// PathSource records how a project's real path was recovered, because the
// confidence differs sharply between them.
type PathSource string

const (
	FromTranscript PathSource = "transcript"  // a cwd field inside a session file
	FromClaudeJSON PathSource = "claude-json" // the projects map in ~/.claude.json
	FromManual     PathSource = "manual"      // an operator-supplied override
	FromUnknown    PathSource = ""            // nothing worked: the bucket is unroutable
)

// Project is one bucket's identity, as recorded by one machine.
type Project struct {
	Path     string     `json:"path"`   // real absolute path, recovered - NOT decoded from the bucket name
	Leaf     string     `json:"leaf"`   // final path segment, the folder-name fallback
	Remote   string     `json:"remote"` // git remote, the strongest cross-machine identity
	OS       string     `json:"os,omitempty"`
	Seen     string     `json:"seen"` // YYYY-MM-DD
	Sessions int        `json:"sessions,omitempty"`
	Source   PathSource `json:"source,omitempty"`

	// Machine names the machine an entry came from. A shard already records this at
	// the top level, but it is serialised per-project too, because the merged
	// projects.json compatibility view has nowhere else to put it - and the
	// PowerShell implementation's format carries it per project as well.
	Machine string `json:"machine,omitempty"`
}

// Routable reports whether this entry can be matched to a local folder at all.
func (p Project) Routable() bool { return p.Path != "" || p.Leaf != "" || p.Remote != "" }

// Shard is one machine's manifest file: manifest/<machine>.json.
//
// Sharding replaces the single read-merge-write projects.json, which loses data in
// ordinary use: two machines pushing inside the same sync window both merge onto the
// version they read, and the second write silently drops the first machine's
// entries. Nothing detects it, because both writes are well-formed JSON.
//
// One writer per file removes the race entirely. Readers merge in memory.
type Shard struct {
	SchemaVersion int                `json:"schemaVersion"`
	Machine       string             `json:"machine"`
	Projects      map[string]Project `json:"projects"`

	// Sessions is only read from shards written before the index was split out into
	// its own file. New pushes write index/<machine>.json instead: session rows grow
	// without bound, and the manifest is read by every import and every doctor run,
	// which should not mean parsing megabytes of index data to find a handful of
	// project paths.
	Sessions []Session `json:"sessions,omitempty"`
}

// IndexDir holds the per-machine session listings that INDEX.md is rendered from.
func IndexDir(dest string) string { return filepath.Join(dest, "index") }

// SessionIndex is one machine's contribution to the browsable index.
type SessionIndex struct {
	SchemaVersion int       `json:"schemaVersion"`
	Machine       string    `json:"machine"`
	Sessions      []Session `json:"sessions"`
}

// Session is one row of the browsable index.
type Session struct {
	Bucket  string    `json:"bucket"`
	Project string    `json:"project"`
	ID      string    `json:"id"`
	Updated time.Time `json:"updated"`
	Size    int64     `json:"size"`
	Prompt  string    `json:"prompt"`
	Machine string    `json:"-"`
}

// AllSessions returns the index rows every machine has recorded.
//
// Reads index/<machine>.json, falling back to sessions embedded in a shard written by
// an older version so an existing archive keeps its listing.
func AllSessions(dest string) ([]Session, error) {
	byMachine := map[string][]Session{}

	if entries, err := os.ReadDir(IndexDir(dest)); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(IndexDir(dest), e.Name()))
			if err != nil {
				continue // one unreadable listing must not hide the others
			}
			var idx SessionIndex
			if json.Unmarshal(raw, &idx) != nil {
				continue
			}
			if idx.Machine == "" {
				idx.Machine = strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
			}
			byMachine[idx.Machine] = idx.Sessions
		}
	}

	shards, _, err := ReadShards(dest)
	if err != nil {
		return nil, err
	}
	for _, s := range shards {
		if _, ok := byMachine[s.Machine]; !ok && len(s.Sessions) > 0 {
			byMachine[s.Machine] = s.Sessions
		}
	}

	var out []Session
	for machine, sessions := range byMachine {
		for _, sess := range sessions {
			sess.Machine = machine
			out = append(out, sess)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

// WriteSessionIndex stores this machine's index rows.
func WriteSessionIndex(dest, machine string, sessions []Session) error {
	data, err := json.MarshalIndent(SessionIndex{
		SchemaVersion: SchemaVersion,
		Machine:       machine,
		Sessions:      sessions,
	}, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(filepath.Join(IndexDir(dest), machine+".json"), append(data, '\n'), 0o644)
}

// Machines summarises who has contributed to this archive.
type MachineInfo struct {
	Name     string
	Projects int
	Sessions int
	LastSeen string
}

// Machines lists every machine recorded in the archive.
//
// A machine that is renamed, retired or reinstalled leaves its shard behind, and
// nothing can infer that it is gone - so the tool reports what it sees and lets a
// human decide, rather than deleting another machine's data on a guess.
func Machines(dest string) ([]MachineInfo, error) {
	shards, _, err := ReadShards(dest)
	if err != nil {
		return nil, err
	}
	sessions, err := AllSessions(dest)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, s := range sessions {
		counts[s.Machine]++
	}

	var out []MachineInfo
	for name, s := range shards {
		info := MachineInfo{Name: name, Projects: len(s.Projects), Sessions: counts[name]}
		for _, p := range s.Projects {
			if p.Seen > info.LastSeen {
				info.LastSeen = p.Seen
			}
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ForgetMachine removes one machine's shard, index and rendered pages.
//
// Its transcripts are left untouched under projects/ - they are the irreplaceable
// part, and dropping them because a machine name changed would be catastrophic. The
// HTML pages are removed, because leaving them behind means a retired machine's
// sessions stay browsable while being absent from every listing.
//
// Returns how many pages were removed.
func ForgetMachine(dest, machine string) (int, error) {
	shard := filepath.Join(ManifestDir(dest), machine+".json")
	if _, err := os.Stat(shard); err != nil {
		return 0, fmt.Errorf("no machine named %q in this archive", machine)
	}

	// Read the sessions before deleting the index that names them.
	var pages []string
	if raw, err := os.ReadFile(filepath.Join(IndexDir(dest), machine+".json")); err == nil {
		var idx SessionIndex
		if json.Unmarshal(raw, &idx) == nil {
			for _, s := range idx.Sessions {
				pages = append(pages, filepath.Join(dest, "html", s.Bucket, s.ID+".html"))
			}
		}
	}

	if err := os.Remove(shard); err != nil {
		return 0, err
	}
	os.Remove(filepath.Join(IndexDir(dest), machine+".json"))

	// Build the set of sessions the remaining machines still claim, once.
	//
	// Asking that question per page meant re-reading and re-parsing every shard and
	// index file for each one: retiring a machine with 500 sessions was 500 full
	// manifest reads across a cloud filesystem.
	claimed := map[string]bool{}
	remaining, err := AllSessions(dest)
	if err != nil {
		// Unsure which sessions survive: keep every page rather than delete one that
		// is still in use.
		return 0, nil
	}
	for _, s := range remaining {
		claimed[s.ID] = true
	}

	removed := 0
	for _, p := range pages {
		// A bucket shared with another machine keeps its pages.
		id := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		if claimed[id] {
			continue
		}
		if err := os.Remove(p); err == nil {
			removed++
		}
	}
	return removed, nil
}

// ReadShards loads every manifest shard in the archive.
//
// An unreadable or malformed shard is skipped, not fatal: one machine writing
// nonsense must not blind every other machine to the rest of the archive. The names
// of skipped files are returned so doctor can report them.
func ReadShards(dest string) (map[string]Shard, []string, error) {
	shards := map[string]Shard{}
	var bad []string

	entries, err := os.ReadDir(ManifestDir(dest))
	if os.IsNotExist(err) {
		return shards, nil, nil
	}
	if err != nil {
		return shards, nil, err
	}

	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
			continue
		}
		path := filepath.Join(ManifestDir(dest), e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			bad = append(bad, e.Name())
			continue
		}
		var s Shard
		if err := json.Unmarshal(raw, &s); err != nil {
			bad = append(bad, e.Name())
			continue
		}
		if s.Machine == "" {
			s.Machine = strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		}
		shards[s.Machine] = s
	}
	return shards, bad, nil
}

// ReadLegacy loads the flat projects.json written by the PowerShell implementation.
//
// It has no schema version and no machine attribution, so its entries are treated as
// the lowest-confidence source when merging.
func ReadLegacy(dest string) (map[string]Project, error) {
	raw, err := os.ReadFile(LegacyManifestPath(dest))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]Project
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// Merged returns one view of every project any machine has recorded.
//
// Conflicts on the same bucket are settled by the newest "seen" date. Shards always
// beat the legacy projects.json, which carries no attribution and may have already
// lost a write before this tool ever ran.
func Merged(dest string) (map[string]Project, []string, error) {
	out := map[string]Project{}

	legacy, err := ReadLegacy(dest)
	if err != nil {
		return nil, nil, err
	}
	for bucket, p := range legacy {
		p.Machine = "(projects.json)"
		out[bucket] = p
	}

	shards, bad, err := ReadShards(dest)
	if err != nil {
		return nil, bad, err
	}
	for _, s := range shards {
		for bucket, p := range s.Projects {
			p.Machine = s.Machine
			existing, seen := out[bucket]
			if !seen || existing.Machine == "(projects.json)" || p.Seen > existing.Seen {
				out[bucket] = p
			}
		}
	}
	return out, bad, nil
}

// BucketNames lists the bucket folders actually present in the archive, which is not
// the same set as the manifest: a bucket holding only memory files has never had a
// cwd to read, so the PowerShell implementation recorded nothing for it at all.
func BucketNames(dest string) ([]string, error) {
	entries, err := os.ReadDir(ProjectsDir(dest))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}
