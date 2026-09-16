package archive

import (
	"os"
	"testing"
)

// Two machines stamping the same date used to be settled by Go's random map order,
// so import could route a project differently from one run to the next.
func TestMergedTieIsDeterministic(t *testing.T) {
	for i := 0; i < 20; i++ {
		dest := t.TempDir()
		writeShard(t, dest, "ZED", Shard{Machine: "ZED", Projects: map[string]Project{
			"e--api": {Path: `E:\z\api`, Seen: "2026-09-01"},
		}})
		writeShard(t, dest, "ALPHA", Shard{Machine: "ALPHA", Projects: map[string]Project{
			"e--api": {Path: `E:\a\api`, Seen: "2026-09-01"},
		}})
		merged, _, err := Merged(dest)
		if err != nil {
			t.Fatal(err)
		}
		if merged["e--api"].Machine != "ALPHA" {
			t.Fatalf("run %d: tie went to %q", i, merged["e--api"].Machine)
		}
	}
}

// A bare date from an older push must still lose to a precise, later timestamp.
func TestMergedComparesOldDateWithTimestamp(t *testing.T) {
	dest := t.TempDir()
	writeShard(t, dest, "A", Shard{Machine: "A", Projects: map[string]Project{
		"e--api": {Path: `E:\old`, Seen: "2026-09-01"},
	}})
	writeShard(t, dest, "B", Shard{Machine: "B", Projects: map[string]Project{
		"e--api": {Path: `E:\new`, Seen: "2026-09-01T10:00:00Z"},
	}})
	merged, _, _ := Merged(dest)
	if merged["e--api"].Path != `E:\new` {
		t.Errorf("got %q", merged["e--api"].Path)
	}
}

// projects.json is regenerated from the shards. Reading it back as if it were
// independent evidence kept a forgotten machine's entries alive forever.
func TestMergedIgnoresProjectsJSONEchoOfAShard(t *testing.T) {
	dest := t.TempDir()
	legacy := `{
	  "e--gone": {"path":"E:\\gone","seen":"2026-09-01","machine":"LAP-1"},
	  "e--ps-only": {"path":"E:\\ps","seen":"2026-08-01","machine":"OLD-BOX"}
	}`
	if err := os.WriteFile(LegacyManifestPath(dest), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	writeShard(t, dest, "LAP-1", Shard{Machine: "LAP-1", Projects: map[string]Project{
		"e--api": {Path: `E:\api`, Seen: "2026-09-01"},
	}})

	merged, _, err := Merged(dest)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := merged["e--gone"]; ok {
		t.Error("an entry LAP-1's own shard no longer records was revived from projects.json")
	}
	// A machine that only ever ran the PowerShell tool has no shard: its entries are
	// the only record of its projects.
	if merged["e--ps-only"].Path != `E:\ps` {
		t.Error("a PowerShell-only machine's project was dropped")
	}
}

// A BOM from Windows PowerShell 5.1, or a Drive conflict mangling the file, used to
// fail every push from then on.
func TestMergedSurvivesBadProjectsJSON(t *testing.T) {
	dest := t.TempDir()
	if err := os.WriteFile(LegacyManifestPath(dest), []byte("\xef\xbb\xbf"+`{"e--api":{"path":"E:\\api"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	merged, bad, err := Merged(dest)
	if err != nil || len(bad) != 0 || merged["e--api"].Path != `E:\api` {
		t.Errorf("BOM: merged=%v bad=%v err=%v", merged, bad, err)
	}

	if err := os.WriteFile(LegacyManifestPath(dest), []byte(`{"e--api": nonsense`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, bad, err := Merged(dest); err != nil || len(bad) != 1 {
		t.Errorf("corrupt projects.json: bad=%v err=%v", bad, err)
	}
}

// The manifest lives on a shared folder. Its names are joined into paths that are
// read, written and - in `machines forget` - deleted.
func TestUnsafeNamesAreRejected(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "../x", `..\x`, "a/b", `C:x`} {
		if ValidName(bad) {
			t.Errorf("ValidName(%q) = true", bad)
		}
	}
	for _, good := range []string{"e--api", "C--Users-me", "-home-me-app", "11111111-2222"} {
		if !ValidName(good) {
			t.Errorf("ValidName(%q) = false", good)
		}
	}

	dest := t.TempDir()
	writeShard(t, dest, "LAP-1", Shard{Machine: "LAP-1", Projects: map[string]Project{
		"../../escape": {Path: `E:\x`},
		"e--api":       {Path: `E:\api`},
	}})
	merged, _, _ := Merged(dest)
	if _, ok := merged["../../escape"]; ok || len(merged) != 1 {
		t.Errorf("merged = %v", merged)
	}
	if _, err := ForgetMachine(dest, "../manifest/LAP-1"); err == nil {
		t.Error("ForgetMachine accepted a path as a machine name")
	}
}
