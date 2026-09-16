package identity

import (
	"os"
	"path/filepath"
	"testing"
)

// The same absolute path on two machines can hold different repositories. The
// recorded path used to win without looking.
func TestResolveRecordedPathMustNotContradictRemote(t *testing.T) {
	root := t.TempDir()
	here := writeGitConfig(t, filepath.Join(root, "api"), "[remote \"origin\"]\n\turl = git@github.com:someone/other.git\n")

	m := Resolve(here, "api", "git@github.com:example/api.git", NewScanner([]string{root}, 2))
	if m.How == ByRecordedPath {
		t.Fatalf("filed into a different repository at the recorded path: %+v", m)
	}
	if m.How != NotFound {
		t.Errorf("got %+v, want not found", m)
	}

	// Same remote, or no remote to contradict it: the recorded path stands.
	same := Resolve(here, "api", "https://github.com/someone/other", nil)
	if same.How != ByRecordedPath {
		t.Errorf("matching remote: %+v", same)
	}
}

// Every candidate being a different repository is "not here", not "ambiguous".
func TestResolveReportsDifferentRepositoriesAsNotFound(t *testing.T) {
	root := t.TempDir()
	writeGitConfig(t, filepath.Join(root, "a", "api"), "[remote \"origin\"]\n\turl = git@github.com:x/one.git\n")
	writeGitConfig(t, filepath.Join(root, "b", "api"), "[remote \"origin\"]\n\turl = git@github.com:x/two.git\n")

	m := Resolve("", "api", "git@github.com:example/api.git", NewScanner([]string{root}, 3))
	if m.How != NotFound || len(m.Candidates) != 2 {
		t.Errorf("got %+v", m)
	}

	// A third, unversioned folder of that name is the only one that could be it.
	if err := os.MkdirAll(filepath.Join(root, "c", "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	m = Resolve("", "api", "git@github.com:example/api.git", NewScanner([]string{root}, 3))
	if m.How != ByFolderName || m.Path != filepath.Join(root, "c", "api") {
		t.Errorf("got %+v", m)
	}
}

// A path recorded on another operating system is not a path here.
func TestResolveIgnoresForeignRecordedPath(t *testing.T) {
	foreign := "/home/me/api"
	if filepath.IsAbs(foreign) {
		foreign = `E:\work\api`
	}
	if m := Resolve(foreign, "", "", nil); m.How == ByRecordedPath {
		t.Errorf("a foreign path was treated as local: %+v", m)
	}
}
