package main_test

// When nothing identifies a project - no git remote, a different folder name, or two
// candidates - the user says where it lives once, and every later import uses that.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
)

func (f importFixture) config(t *testing.T, args ...string) (string, error) {
	t.Helper()
	full := append([]string{"config", "--claude-dir", f.claudeDir}, args...)
	cmd := exec.Command(binary, full...)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

const mapSession = "55555555-5555-5555-5555-555555555555"

// A renamed folder with no git remote: import cannot know, says how to tell it, and
// files the project once told.
func TestMapFilesAProjectNothingElseCanIdentify(t *testing.T) {
	f := newImportFixture(t)
	local := f.makeRepo(t, "new-name", "")
	f.archiveProject(t, "D--work-old-name", `D:\work\old-name`, mapSession, "hello")
	f.writeManifest(t, map[string]any{
		"D--work-old-name": map[string]any{"path": `D:\work\old-name`, "leaf": "old-name", "seen": "2026-08-30"},
	})

	out, err := f.run(t, "import", "--search-root", f.dev)
	if err != nil {
		t.Fatalf("import failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "config map D--work-old-name") {
		t.Errorf("import did not say how to map the project:\n%s", out)
	}

	if out, err := f.config(t, "map", "D--work-old-name", local); err != nil {
		t.Fatalf("config map failed: %v\n%s", err, out)
	}
	out, err = f.run(t, "import", "--search-root", f.dev)
	if err != nil {
		t.Fatalf("import failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "matched by manual map") {
		t.Errorf("the mapping was not used:\n%s", out)
	}
	body := mustRead(t, filepath.Join(f.claudeDir, "projects", claude.Slug(local), mapSession+".jsonl"))
	if strings.Contains(body, `old-name`) {
		t.Errorf("the old path was not rewritten:\n%s", body)
	}
}

func TestMapSettlesTwoSameNamedFolders(t *testing.T) {
	f := newImportFixture(t)
	f.makeRepo(t, "api", "")
	other := filepath.Join(f.dev, "clients", "api")
	mustMkdir(t, other)
	f.archiveProject(t, "D--work-api", `D:\work\api`, mapSession, "hello")
	f.writeManifest(t, map[string]any{
		"D--work-api": map[string]any{"path": `D:\work\api`, "leaf": "api", "seen": "2026-08-30"},
	})

	if out, err := f.config(t, "map", "D--work-api", other); err != nil {
		t.Fatalf("config map failed: %v\n%s", err, out)
	}
	out, err := f.run(t, "import", "--search-root", f.dev)
	if err != nil {
		t.Fatalf("import failed: %v\n%s", err, out)
	}
	if got := f.localBucket(t, claude.Slug(other)); !contains(got, mapSession+".jsonl") {
		t.Errorf("not filed under the mapped folder; got %v\n%s", got, out)
	}
}

func TestMapRefusesAFolderThatDoesNotExist(t *testing.T) {
	f := newImportFixture(t)
	if out, err := f.config(t, "map", "D--work-api", filepath.Join(f.dev, "nowhere")); err == nil {
		t.Fatalf("config map accepted a missing folder:\n%s", out)
	}
}

// A mapped folder deleted later is reported, not silently skipped or guessed around.
func TestImportReportsAMappedFolderThatIsGone(t *testing.T) {
	f := newImportFixture(t)
	f.archiveProject(t, "D--work-api", `D:\work\api`, mapSession, "hello")
	f.writeManifest(t, map[string]any{
		"D--work-api": map[string]any{"path": `D:\work\api`, "leaf": "api", "seen": "2026-08-30"},
	})
	gone := filepath.Join(f.dev, "gone")
	cfg, _ := json.Marshal(map[string]any{"projectMap": map[string]string{"D--work-api": gone}})
	mustWrite(t, filepath.Join(f.claudeDir, "session-sync.json"), string(cfg))

	out, _ := f.run(t, "import", "--search-root", f.dev)
	if !strings.Contains(out, gone) || !strings.Contains(out, "does not exist") {
		t.Errorf("the missing mapped folder was not reported:\n%s", out)
	}
}

// Third review 2: every macOS and Linux bucket name starts with "-", and the command
// import prints for it must work as printed.
func TestMapTakesABucketNameStartingWithADash(t *testing.T) {
	f := newImportFixture(t)
	local := f.makeRepo(t, "api", "")
	if out, err := f.config(t, "map", "-Users-me-dev-api", local); err != nil {
		t.Fatalf("config map rejected a dash-leading bucket: %v\n%s", err, out)
	}
	if out, _ := f.config(t, "map"); !strings.Contains(out, "-Users-me-dev-api") {
		t.Errorf("the mapping was not saved:\n%s", out)
	}
	if out, err := f.config(t, "unmap", "-Users-me-dev-api"); err != nil {
		t.Fatalf("config unmap rejected a dash-leading bucket: %v\n%s", err, out)
	}
}

func TestConfigListsAndRemovesMappings(t *testing.T) {
	f := newImportFixture(t)
	local := f.makeRepo(t, "api", "")
	if out, err := f.config(t, "map", "D--work-api", local); err != nil {
		t.Fatalf("config map failed: %v\n%s", err, out)
	}
	out, _ := f.config(t, "map")
	if !strings.Contains(out, "D--work-api") || !strings.Contains(out, local) {
		t.Errorf("config map did not list the mapping:\n%s", out)
	}
	if out, err := f.config(t, "unmap", "D--work-api"); err != nil {
		t.Fatalf("config unmap failed: %v\n%s", err, out)
	}
	if out, _ := f.config(t, "map"); strings.Contains(out, "D--work-api") {
		t.Errorf("the mapping is still listed after unmap:\n%s", out)
	}
}
