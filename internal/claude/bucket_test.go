package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSlug(t *testing.T) {
	cases := []struct{ in, want string }{
		{`E:\airos-frontend`, "E--airos-frontend"},
		{`C:\Users\Administrator`, "C--Users-Administrator"},
		{"/home/aislam/dev/airos-frontend", "-home-aislam-dev-airos-frontend"},
		{`E:\Laravel Applications\innovix`, "E--Laravel-Applications-innovix"},
	}
	for _, tc := range cases {
		if got := Slug(tc.in); got != tc.want {
			t.Errorf("Slug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The lossiness is the whole reason identity is read from a transcript's cwd rather
// than decoded from the bucket name. This test exists to document it: three
// different real paths collapse onto one bucket name, so the mapping cannot be
// inverted. If this ever stops being true, the design can be simplified.
func TestSlugIsNotInvertible(t *testing.T) {
	a := Slug(`E:\Laravel Applications\innovix`)
	b := Slug(`E:\Laravel-Applications\innovix`)
	c := Slug(`E:\Laravel-Applications-innovix`)
	if a != b || b != c {
		t.Fatalf("expected all three to collapse to one name, got %q %q %q", a, b, c)
	}
}

// Bucket casing follows the cwd string Claude saw, not what is on disk: E:\ has
// been recorded as "e--". A freshly computed slug must never win over an existing
// bucket, or transcripts land somewhere /resume never looks.
func TestFindBucketIsCaseInsensitive(t *testing.T) {
	projects := t.TempDir()
	onDisk := filepath.Join(projects, "e--airos-frontend")
	if err := os.MkdirAll(onDisk, 0o755); err != nil {
		t.Fatal(err)
	}

	b, ok := FindBucket(projects, Slug(`E:\airos-frontend`)) // computes "E--airos-frontend"
	if !ok {
		t.Fatal("existing bucket not found: casing must not matter")
	}
	if b.Name != "e--airos-frontend" {
		t.Errorf("Name = %q, want the name as it is on disk", b.Name)
	}
}

func TestListBucketsSeparatesMemoryOnly(t *testing.T) {
	projects := t.TempDir()

	withSession := filepath.Join(projects, "c--dev-a")
	if err := os.MkdirAll(withSession, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(withSession, "s1.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A bucket holding only memory is the case the PowerShell implementation loses:
	// identity comes from a transcript cwd, so it records nothing at all for this.
	memOnly := filepath.Join(projects, "e--airos-frontend", "memory")
	if err := os.MkdirAll(memOnly, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memOnly, "MEMORY.md"), []byte("- note\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	buckets, err := ListBuckets(projects)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 {
		t.Fatalf("got %d buckets, want 2", len(buckets))
	}

	byName := map[string]Bucket{}
	for _, b := range buckets {
		byName[b.Name] = b
	}
	if !byName["c--dev-a"].HasTranscripts() {
		t.Error("c--dev-a should have a transcript")
	}
	if byName["e--airos-frontend"].HasTranscripts() {
		t.Error("e--airos-frontend has no transcript")
	}
	if len(byName["e--airos-frontend"].Memory) != 1 {
		t.Error("memory files should still be listed for a transcript-less bucket")
	}
}
