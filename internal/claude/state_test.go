package claude

import (
	"os"
	"path/filepath"
	"testing"
)

// .claude.json records project paths in whatever form Claude happened to see, which on
// Windows has included a lowercase drive letter and forward slashes - a form that must
// still match a bucket named with a capital drive and backslashes.
func TestMatchBucketAcrossPathForms(t *testing.T) {
	paths := []string{
		"e:/airos-frontend",
		"C:/Users/Administrator",
		`E:\Laravel Applications\innovix`,
	}

	cases := []struct {
		bucket string
		want   string
	}{
		{"e--airos-frontend", "e:/airos-frontend"},
		{"E--airos-frontend", "e:/airos-frontend"}, // casing differs from disk
		{"C--Users-Administrator", "C:/Users/Administrator"},
		{"E--Laravel-Applications-innovix", `E:\Laravel Applications\innovix`},
		{"nothing--like--this", ""},
	}

	for _, tc := range cases {
		got, ok := MatchBucket(tc.bucket, paths)
		if tc.want == "" {
			if ok {
				t.Errorf("MatchBucket(%q) unexpectedly matched %q", tc.bucket, got)
			}
			continue
		}
		if !ok || got != tc.want {
			t.Errorf("MatchBucket(%q) = %q, %v; want %q", tc.bucket, got, ok, tc.want)
		}
	}
}

func TestProjectPathsReadsKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")
	body := `{"numStartups":41,"projects":{"e:/airos-frontend":{"allowedTools":[]},"C:/Users/me":{}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	paths, err := ProjectPaths(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("got %d paths, want 2: %v", len(paths), paths)
	}
}

func TestProjectPathsMissingFileIsNotAnError(t *testing.T) {
	paths, err := ProjectPaths(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("a missing file is a normal state: %v", err)
	}
	if len(paths) != 0 {
		t.Errorf("got %v", paths)
	}
}

// The bug this guards against: .claude.json is a SIBLING of ~/.claude/, not a file
// inside it. Joining it onto the config root looks right and silently finds nothing.
func TestStatePathFindsFileBesideConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows

	root := filepath.Join(home, ".claude")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	beside := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(beside, []byte(`{"projects":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := StatePath(root); got != beside {
		t.Errorf("StatePath = %q, want the sibling file %q", got, beside)
	}

	// A file inside the config dir wins, which is where it sits under
	// CLAUDE_CONFIG_DIR.
	inside := filepath.Join(root, ".claude.json")
	if err := os.WriteFile(inside, []byte(`{"projects":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := StatePath(root); got != inside {
		t.Errorf("StatePath = %q, want the file inside the config dir %q", got, inside)
	}
}
