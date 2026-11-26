package archive

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDeletedUnion(t *testing.T) {
	dest := t.TempDir()
	const x, y = "aaaaaaaa-0000-4000-8000-000000000001", "aaaaaaaa-0000-4000-8000-000000000002"
	t0 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	deletedX := DeletedEntry{ID: x, Buckets: []string{"e--web-app"}, LastUUID: "u3", LastAt: t0.AddDate(0, -6, 0), DeletedAt: t0}
	must(RecordDeleted(dest, "LAP-1", []DeletedEntry{deletedX}))
	must(RecordDeleted(dest, "LAP-2", []DeletedEntry{{ID: strings.ToUpper(x), DeletedAt: t0.Add(time.Hour), Undone: true}}))
	must(RecordDeleted(dest, "LAP-1", []DeletedEntry{{ID: y, DeletedAt: t0.Add(2 * time.Hour)}}))

	// A sync client's conflict copies are not read, whatever they hold.
	must(os.WriteFile(filepath.Join(DeletedDir(dest), "LAP-1 (1).json"), []byte("{"), 0o644))
	conflict, err := os.ReadFile(filepath.Join(DeletedDir(dest), "LAP-1.json"))
	must(err)
	again := strings.Replace(string(conflict), t0.Add(2*time.Hour).Format(time.RFC3339), t0.Add(5*time.Hour).Format(time.RFC3339), 1)
	again = strings.Replace(again, y, x, 1) // would delete x again, after the undo
	must(os.WriteFile(filepath.Join(DeletedDir(dest), "LAP-1.sync-conflict-20261101-ABC.json"), []byte(again), 0o644))
	must(os.WriteFile(filepath.Join(DeletedDir(dest), "notes.txt"), []byte("x"), 0o644))

	d, err := LoadDeleted(dest)
	must(err)
	if len(d) != 2 {
		t.Fatalf("LoadDeleted = %+v, want x and y", d)
	}
	if e := d[x]; !e.Undone || !e.DeletedAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("x: %+v, want undone by LAP-2", e)
	}
	if e := d[y]; e.Undone || e.ID != y {
		t.Errorf("y: %+v, want deleted", e)
	}

	// Each machine's file keeps every entry it recorded.
	var own deletedFile
	readJSON(t, filepath.Join(DeletedDir(dest), "LAP-1.json"), &own)
	if len(own.Entries) != 2 || !reflect.DeepEqual(own.Entries[0].Buckets, deletedX.Buckets) || own.Entries[0].LastUUID != "u3" || !own.Entries[0].LastAt.Equal(deletedX.LastAt) {
		t.Errorf("LAP-1's file: %+v", own)
	}

	// A machine's file that cannot be read is an error, and is never written over.
	bad := filepath.Join(DeletedDir(dest), "LAP-3.json")
	must(os.WriteFile(bad, []byte(`{"entries": [`), 0o644))
	if _, err := LoadDeleted(dest); err == nil {
		t.Error("LoadDeleted read past an unreadable file")
	}
	if err := RecordDeleted(dest, "LAP-3", []DeletedEntry{{ID: y, DeletedAt: t0}}); err == nil {
		t.Error("RecordDeleted wrote over an unreadable file")
	}
	if got, _ := os.ReadFile(bad); string(got) != `{"entries": [` {
		t.Errorf("the unreadable file was rewritten: %q", got)
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
}

// A view reads every deleted list it can; push and prune refuse while one cannot be read.
func TestLoadDeletedEachSkipsOnlyTheUnreadable(t *testing.T) {
	dest := t.TempDir()
	const x = "aaaaaaaa-0000-4000-8000-000000000001"
	if err := RecordDeleted(dest, "LAP-1", []DeletedEntry{{ID: x, DeletedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(DeletedDir(dest), "LAP-2.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDeleted(dest); err == nil {
		t.Error("LoadDeleted read past a list it cannot read")
	}
	got, bad, err := LoadDeletedEach(dest)
	if _, ok := got[x]; err != nil || !ok || !reflect.DeepEqual(bad, []string{"LAP-2.json"}) {
		t.Errorf("entries %v, unreadable %v, %v", got, bad, err)
	}
}
