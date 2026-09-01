package archive

import (
	"encoding/json"
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

	// Sessions is what this machine contributed to the browsable index.
	//
	// It lives in the shard for the same reason the projects do: INDEX.md is a single
	// file at the archive root, so a machine that regenerated it from only its own
	// sessions would erase every other machine's listing on each push. Cached here,
	// the index can be rebuilt from every shard without re-reading transcripts across
	// a cloud filesystem.
	Sessions []Session `json:"sessions,omitempty"`
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
func AllSessions(dest string) ([]Session, error) {
	shards, _, err := ReadShards(dest)
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, s := range shards {
		for _, sess := range s.Sessions {
			sess.Machine = s.Machine
			out = append(out, sess)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
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
