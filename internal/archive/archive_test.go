package archive

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- the invariant that already shipped as a bug once -----------------------

func TestNeedsCopyToleratesRoundedTimestamps(t *testing.T) {
	base := time.Date(2026, 8, 31, 12, 0, 0, 271881400, time.UTC)

	cases := []struct {
		name     string
		srcSize  int64
		dstSize  int64
		dstDelta time.Duration
		want     bool
	}{
		// Google Drive rounds a copied file's mtime down, making the destination
		// fractionally older than its source. An exact >= comparison re-copies the
		// whole archive on every run, forever.
		{"drive rounding", 100, 100, -881400 * time.Nanosecond, false},
		{"same instant", 100, 100, 0, false},
		{"within tolerance", 100, 100, -1900 * time.Millisecond, false},
		{"FAT 2-second granularity", 100, 100, -2 * time.Second, false},
		{"genuinely older", 100, 100, -10 * time.Second, true},
		{"destination newer", 100, 100, 5 * time.Second, false},
		{"size differs", 100, 101, 0, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := fakeInfo{size: tc.srcSize, mod: base}
			dst := fakeInfo{size: tc.dstSize, mod: base.Add(tc.dstDelta)}
			if got := NeedsCopy(src, dst); got != tc.want {
				t.Errorf("NeedsCopy = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNeedsCopyWhenDestinationMissing(t *testing.T) {
	if !NeedsCopy(fakeInfo{size: 1, mod: time.Now()}, nil) {
		t.Error("a missing destination must always be copied")
	}
}

type fakeInfo struct {
	size int64
	mod  time.Time
}

func (f fakeInfo) Name() string       { return "f" }
func (f fakeInfo) Size() int64        { return f.size }
func (f fakeInfo) Mode() os.FileMode  { return 0o644 }
func (f fakeInfo) ModTime() time.Time { return f.mod }
func (f fakeInfo) IsDir() bool        { return false }
func (f fakeInfo) Sys() any           { return nil }

// --- atomic writes ----------------------------------------------------------

func TestWriteFileAtomicOverwritesAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "INDEX.md")

	if err := WriteFileAtomic(path, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Windows will not rename onto an existing file; the second write proves the
	// fallback path works.
	if err := WriteFileAtomic(path, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second" {
		t.Errorf("content = %q, want %q", got, "second")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected only the target file, found %v", names)
	}
}

func TestWritable(t *testing.T) {
	if err := Writable(t.TempDir()); err != nil {
		t.Errorf("a temp dir should be writable: %v", err)
	}
	if err := Writable(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Error("a missing directory must not report as writable")
	}
}

// --- config -----------------------------------------------------------------

func TestSaveConfigPreservesUnknownKeys(t *testing.T) {
	root := t.TempDir()
	// A newer build, or a hand-edited file, may carry keys this version knows
	// nothing about. Losing them silently would be a nasty surprise.
	seed := `{"destination":"/old","renderHtml":false,"excludeProjects":["secret"]}`
	if err := os.WriteFile(ConfigPath(root), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := SaveConfig(root, Config{Destination: "/new"}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(ConfigPath(root))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["destination"] != "/new" {
		t.Errorf("destination = %v", got["destination"])
	}
	if got["renderHtml"] != false {
		t.Error("unknown key renderHtml was lost")
	}
	if _, ok := got["excludeProjects"]; !ok {
		t.Error("unknown key excludeProjects was lost")
	}
}

func TestResolveDestinationPrecedence(t *testing.T) {
	root := t.TempDir()

	if _, src, _ := ResolveDestination(root, "/from/flag"); src != SourceFlag {
		t.Errorf("flag should win, got %v", src)
	}

	if err := SaveConfig(root, Config{Destination: "/from/config"}); err != nil {
		t.Fatal(err)
	}
	dest, src, err := ResolveDestination(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if src != SourceConfig || dest != "/from/config" {
		t.Errorf("config should be used, got %q from %v", dest, src)
	}

	// The flag still beats a saved config.
	if dest, src, _ := ResolveDestination(root, "/from/flag"); src != SourceFlag || dest != "/from/flag" {
		t.Errorf("flag must override config, got %q from %v", dest, src)
	}
}

func TestLoadConfigMissingIsNotAnError(t *testing.T) {
	c, err := LoadConfig(t.TempDir())
	if err != nil {
		t.Fatalf("an unconfigured machine is normal: %v", err)
	}
	if c.Destination != "" {
		t.Errorf("destination = %q, want empty", c.Destination)
	}
}

// --- the sharded manifest ---------------------------------------------------

func writeShard(t *testing.T, dest, machine string, s Shard) {
	t.Helper()
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(filepath.Join(ManifestDir(dest), machine+".json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// The reason for sharding: with one shared projects.json, two machines pushing in
// the same window both merge onto the version they read and the second write drops
// the first machine's entries. With one file per machine, both survive.
func TestMergedKeepsEveryMachinesEntries(t *testing.T) {
	dest := t.TempDir()

	writeShard(t, dest, "LAP-1", Shard{SchemaVersion: 1, Machine: "LAP-1", Projects: map[string]Project{
		"e--api": {Path: `E:\api`, Leaf: "api", Seen: "2026-08-30"},
	}})
	writeShard(t, dest, "MACBOOK", Shard{SchemaVersion: 1, Machine: "MACBOOK", Projects: map[string]Project{
		"-Users-me-dev-web": {Path: "/Users/me/dev/web", Leaf: "web", Seen: "2026-08-31"},
	}})

	merged, bad, err := Merged(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Errorf("unexpected bad shards: %v", bad)
	}
	if len(merged) != 2 {
		t.Fatalf("got %d projects, want 2 - a machine's entries were lost", len(merged))
	}
	if merged["e--api"].Machine != "LAP-1" {
		t.Errorf("attribution lost: %+v", merged["e--api"])
	}
}

func TestMergedNewestSeenWins(t *testing.T) {
	dest := t.TempDir()

	writeShard(t, dest, "OLD", Shard{Machine: "OLD", Projects: map[string]Project{
		"e--api": {Path: `E:\old-location\api`, Leaf: "api", Seen: "2026-08-01"},
	}})
	writeShard(t, dest, "NEW", Shard{Machine: "NEW", Projects: map[string]Project{
		"e--api": {Path: `E:\api`, Leaf: "api", Seen: "2026-08-31"},
	}})

	merged, _, err := Merged(dest)
	if err != nil {
		t.Fatal(err)
	}
	if merged["e--api"].Path != `E:\api` {
		t.Errorf("newest seen date should win, got %q from %q",
			merged["e--api"].Path, merged["e--api"].Machine)
	}
}

// A shard beats the legacy flat file, which has no attribution and may already have
// lost a write before this tool ever ran.
func TestMergedPrefersShardOverLegacy(t *testing.T) {
	dest := t.TempDir()

	legacy := `{"e--api":{"path":"E:\\stale","leaf":"api","seen":"2026-09-09"}}`
	if err := os.WriteFile(LegacyManifestPath(dest), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	writeShard(t, dest, "LAP-1", Shard{Machine: "LAP-1", Projects: map[string]Project{
		"e--api": {Path: `E:\current`, Leaf: "api", Seen: "2026-08-01"},
	}})

	merged, _, err := Merged(dest)
	if err != nil {
		t.Fatal(err)
	}
	if merged["e--api"].Path != `E:\current` {
		t.Errorf("shard should beat legacy even with an older date, got %q", merged["e--api"].Path)
	}
}

// One machine writing nonsense must not blind every other machine to the archive.
func TestMergedSkipsCorruptShard(t *testing.T) {
	dest := t.TempDir()

	writeShard(t, dest, "GOOD", Shard{Machine: "GOOD", Projects: map[string]Project{
		"e--api": {Path: `E:\api`, Leaf: "api", Seen: "2026-08-31"},
	}})
	if err := WriteFileAtomic(filepath.Join(ManifestDir(dest), "BROKEN.json"),
		[]byte(`{"schemaVersion":1,"projects":{ this is not json`), 0o644); err != nil {
		t.Fatal(err)
	}

	merged, bad, err := Merged(dest)
	if err != nil {
		t.Fatalf("a corrupt shard must not fail the merge: %v", err)
	}
	if len(merged) != 1 {
		t.Errorf("good shard should still be read, got %d entries", len(merged))
	}
	if len(bad) != 1 || bad[0] != "BROKEN.json" {
		t.Errorf("the corrupt shard should be reported, got %v", bad)
	}
}

func TestMergedOnEmptyArchive(t *testing.T) {
	merged, bad, err := Merged(t.TempDir())
	if err != nil {
		t.Fatalf("an empty archive is not an error: %v", err)
	}
	if len(merged) != 0 || len(bad) != 0 {
		t.Errorf("got %d entries, %d bad", len(merged), len(bad))
	}
}

func TestProjectRoutable(t *testing.T) {
	cases := []struct {
		p    Project
		want bool
	}{
		{Project{Path: `E:\api`}, true},
		{Project{Leaf: "api"}, true},
		{Project{Remote: "git@github.com:o/r.git"}, true},
		// The memory-only bucket: archived, but nothing to match on.
		{Project{Seen: "2026-08-31"}, false},
		{Project{}, false},
	}
	for _, tc := range cases {
		if got := tc.p.Routable(); got != tc.want {
			t.Errorf("Routable(%+v) = %v, want %v", tc.p, got, tc.want)
		}
	}
}

func TestBucketNames(t *testing.T) {
	dest := t.TempDir()
	for _, b := range []string{"e--api", "c--dev-web"} {
		if err := os.MkdirAll(filepath.Join(ProjectsDir(dest), b), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(ProjectsDir(dest), "loose.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	names, err := BucketNames(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "c--dev-web" || names[1] != "e--api" {
		t.Errorf("got %v, want sorted bucket dirs only", names)
	}
}
