package main_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
)

// includeFixture: LAP-1 pushed web-app's conversation; LAP-2 shares the archive.
func includeFixture(t *testing.T) (lap1, lap2 machine, page string) {
	t.Helper()
	lap1 = newMachine(t, "LAP-1", t.TempDir())
	repo := gitRepo(t, filepath.Join(t.TempDir(), "web-app"))
	mustMkdir(t, filepath.Dir(lap1.session(repo)))
	mustWrite(t, lap1.session(repo), sessionLines(repo, "u1"))
	lap1.run(t, "push")
	lap2 = newMachine(t, "LAP-2", lap1.archive)
	page = filepath.Join(lap1.archive, "html", "web-app")
	return
}

func pagesIn(t *testing.T, archiveDir string) int {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(filepath.Join(archiveDir, "html"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".html") && !strings.HasSuffix(p, "index.html") {
			n++
		}
		return nil
	})
	return n
}

func TestRenderIncludeHoldsOnTheOtherComputer(t *testing.T) {
	lap1, lap2, _ := includeFixture(t)
	if pagesIn(t, lap1.archive) == 0 {
		t.Fatal("fixture: no pages")
	}
	lap2cfg := `{"destination": "` + filepath.ToSlash(lap2.archive) + `", "renderExclude": ["web-app"]}` + "\n"
	mustWrite(t, archive.ConfigPath(lap2.claudeDir), lap2cfg)
	lap1.run(t, "render", "--exclude", "web-app")
	if n := pagesIn(t, lap1.archive); n != 0 {
		t.Fatalf("exclude left %d pages", n)
	}
	lap1.run(t, "render", "--include", "web-app")
	lap2.run(t, "push")
	if pagesIn(t, lap1.archive) == 0 {
		t.Error("the other computer's push did not publish the project again")
	}
	if cfg, _ := os.ReadFile(archive.ConfigPath(lap2.claudeDir)); strings.Contains(string(cfg), "web-app") {
		t.Errorf("LAP-2 still lists it: %s", cfg)
	}
}

func TestExcludeAfterIncludeDropsTheMark(t *testing.T) {
	lap1, _, _ := includeFixture(t)
	lap1.run(t, "render", "--exclude", "web-app")
	lap1.run(t, "render", "--include", "web-app")
	lap1.run(t, "render", "--exclude", "web-app")
	if shared := mustRead(t, filepath.Join(lap1.archive, "render-exclude.txt")); strings.Contains(shared, "!web-app") {
		t.Errorf("mark left:\n%s", shared)
	}
	if n := pagesIn(t, lap1.archive); n != 0 {
		t.Errorf("%d pages published", n)
	}
}
