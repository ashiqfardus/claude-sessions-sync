package main_test

// Machines on different versions fail silently: one still on v0.2.2 pushes no
// profile, so another machine's import brings no rules across and nothing says why
// (#8). Each machine records its version, and doctor names the ones that differ.

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorWarnsWhenAnotherMachineRunsADifferentVersion(t *testing.T) {
	f := newFixture(t)
	shard, _ := json.Marshal(map[string]any{
		"schemaVersion": 1, "machine": "OLD-BOX", "toolVersion": "v0.2.2", "projects": map[string]any{},
	})
	mustWrite(t, filepath.Join(f.archive, "manifest", "OLD-BOX.json"), string(shard))

	if out, err := f.push(t, "--no-html"); err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}
	out, _ := f.run(t, "doctor", "--no-write-probe")
	if !strings.Contains(out, "OLD-BOX") || !strings.Contains(out, "v0.2.2") {
		t.Errorf("doctor did not name the machine on another version:\n%s", out)
	}
}

func TestPushRecordsItsVersionAndDoctorIsQuietWhenAlone(t *testing.T) {
	f := newFixture(t)
	if out, err := f.push(t, "--no-html"); err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}
	shards, _ := filepath.Glob(filepath.Join(f.archive, "manifest", "*.json"))
	if len(shards) != 1 || !strings.Contains(mustRead(t, shards[0]), `"toolVersion"`) {
		t.Fatalf("push did not record its version in its shard: %v", shards)
	}
	out, _ := f.run(t, "doctor", "--no-write-probe")
	if strings.Contains(out, "different version") {
		t.Errorf("doctor warned with no other machine present:\n%s", out)
	}
}
