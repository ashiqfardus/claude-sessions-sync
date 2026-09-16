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
	Path   string `json:"path"`   // real absolute path, recovered - NOT decoded from the bucket name
	Leaf   string `json:"leaf"`   // final path segment, the folder-name fallback
	Remote string `json:"remote"` // git remote, the strongest cross-machine identity
	OS     string `json:"os,omitempty"`
	// Seen is when this machine's data for the bucket last changed: the newest
	// transcript's mtime as RFC 3339 UTC. Older entries hold a bare YYYY-MM-DD, which
	// still compares correctly as a string against the longer form.
	Seen     string     `json:"seen"`
	Sessions int        `json:"sessions,omitempty"`
	Source   PathSource `json:"source,omitempty"`

	// Machine names the machine an entry came from. A shard already records this at
	// the top level, but it is serialised per-project too, because the merged
	// projects.json compatibility view has nowhere else to put it - and the
	// PowerShell implementation's format carries it per project as well.
	Machine string `json:"machine,omitempty"`

	// Related are the other repositories this project's sessions worked in - the
	// sibling folders of a multi-folder workspace. import repoints paths into them
	// too, which it can only do if it knows where they were and what they are.
	Related []Related `json:"related,omitempty"`
}

// Related is one other repository a project's sessions touched. Only git
// repositories with a remote are recorded: a remote is what lets import find the
// folder on another machine without guessing.
type Related struct {
	Path   string `json:"path"`
	Remote string `json:"remote"`
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

	// RelatedIndexed records that every transcript has been scanned for related
	// repositories once, so later pushes only scan the transcripts that changed.
	RelatedIndexed bool `json:"relatedIndexed,omitempty"`

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
			if !ValidName(sess.Bucket) || !ValidName(sess.ID) {
				continue
			}
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
	if !ValidName(machine) {
		return 0, fmt.Errorf("%q is not a machine name", machine)
	}
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
				if !ValidName(s.Bucket) || !ValidName(s.ID) {
					continue
				}
				pages = append(pages, filepath.Join(dest, "html", s.Bucket, s.ID+".html"))
			}
		}
	}

	if err := os.Remove(shard); err != nil {
		return 0, err
	}
	os.Remove(filepath.Join(IndexDir(dest), machine+".json"))

	// projects.json is regenerated from the shards on the next push, but until then its
	// copy of this machine's entries would keep routing imports to it.
	if legacy, err := ReadLegacy(dest); err == nil && legacy != nil {
		for bucket, p := range legacy {
			if p.Machine == machine {
				delete(legacy, bucket)
			}
		}
		if data, err := json.MarshalIndent(legacy, "", "  "); err == nil {
			_ = WriteFileAtomic(LegacyManifestPath(dest), append(data, '\n'), 0o644)
		}
	}

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
		for name := range s.Projects {
			if !ValidName(name) {
				delete(s.Projects, name)
			}
		}
		shards[s.Machine] = s
	}
	return shards, bad, nil
}

// ValidName reports whether a bucket or session name read from the archive is a
// single plain path component.
//
// The manifest and index live on a shared, synced folder. A name like "../../x" or
// ".." there would otherwise be joined into paths this tool reads, writes, and - in
// `machines forget` - deletes.
func ValidName(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.ContainsAny(name, `/\:`) && filepath.Base(name) == name
}

// ReadLegacy loads the flat projects.json written by the PowerShell implementation.
//
// It has no schema version, so its entries are treated as the lowest-confidence
// source when merging.
func ReadLegacy(dest string) (map[string]Project, error) {
	raw, err := os.ReadFile(LegacyManifestPath(dest))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Windows PowerShell 5.1 writes UTF-8 with a BOM, which encoding/json rejects.
	raw = []byte(strings.TrimPrefix(string(raw), "\xef\xbb\xbf"))
	var m map[string]Project
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	for name := range m {
		if !ValidName(name) {
			delete(m, name)
		}
	}
	return m, nil
}

// legacyMachine marks an entry that came from projects.json.
const legacyMachine = "(projects.json)"

// Merged returns one view of every project any machine has recorded.
//
// Conflicts on the same bucket go to the newest "seen" value; an exact tie goes to
// the machine whose name sorts first, so every run and every machine agrees. (Map
// order used to decide, which let import choose a different path from one run to
// the next.)
//
// Shards always beat the legacy projects.json. That file is also regenerated from
// the shards, so an entry in it attributed to a machine is ignored whenever that
// machine has a shard - the shard is its current word - and dropped entirely once the
// machine has been forgotten. Otherwise every entry fed back into itself forever.
//
// An unreadable projects.json is reported in the bad list, not returned as an error:
// it used to fail every push, silently under --quiet.
func Merged(dest string) (map[string]Project, []string, error) {
	out := map[string]Project{}

	shards, bad, err := ReadShards(dest)
	if err != nil {
		return nil, bad, err
	}

	legacy, err := ReadLegacy(dest)
	if err != nil {
		bad = append(bad, "projects.json")
	}
	for bucket, p := range legacy {
		// A machine that only ever ran the PowerShell tool has no shard, and its
		// entries are the only record of its projects: those are kept.
		if _, hasShard := shards[p.Machine]; hasShard {
			continue
		}
		p.Machine = legacyMachine
		out[bucket] = p
	}

	names := make([]string, 0, len(shards))
	for name := range shards {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for bucket, p := range shards[name].Projects {
			p.Machine = name
			existing, seen := out[bucket]
			if !seen || existing.Machine == legacyMachine || p.Seen > existing.Seen {
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
