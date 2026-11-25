package archive

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Holdouts names the machines whose shard lacks a flag; a shard it cannot read may be
// one of them, so that is an error, never "none".
func TestHoldouts(t *testing.T) {
	dest := t.TempDir()
	writeShard(t, dest, "LAP-1", Shard{SchemaVersion: SchemaVersion, Machine: "LAP-1", ReadsZips: true})
	writeShard(t, dest, "LAP-2", Shard{SchemaVersion: SchemaVersion, Machine: "LAP-2"})
	readsZips := func(s Shard) bool { return s.ReadsZips }

	got, err := Holdouts(dest, readsZips)
	if err != nil || !slices.Equal(got, []string{"LAP-2"}) {
		t.Fatalf("Holdouts = %v, %v; want [LAP-2]", got, err)
	}

	if err := os.WriteFile(filepath.Join(ManifestDir(dest), "LAP-3.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := Holdouts(dest, readsZips); err == nil {
		t.Fatalf("a bad shard gave %v and no error", got)
	}
}
