package main_test

// Every machine pushes its rules and skills into the same archive. The shared copy is
// newest-wins, so each machine also keeps its own, which no other machine writes (#5).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// thisMachinesProfile finds the per-machine profile folder this machine wrote.
func thisMachinesProfile(t *testing.T, archive, other string) string {
	t.Helper()
	dirs, _ := filepath.Glob(filepath.Join(archive, "profile", "machines", "*"))
	for _, d := range dirs {
		if filepath.Base(d) != other {
			return d
		}
	}
	t.Fatalf("no per-machine profile folder besides %s; have %v", other, dirs)
	return ""
}

func TestPushKeepsEachMachinesRulesRecoverable(t *testing.T) {
	f := newFixture(t)
	p := filepath.Join(f.archive, "profile")
	// Another machine pushed newer rules first.
	mustWrite(t, filepath.Join(p, "CLAUDE.md"), "windows rules\n")
	mustWrite(t, filepath.Join(p, "machines", "WIN-BOX", "CLAUDE.md"), "windows rules\n")
	mustWrite(t, filepath.Join(f.claudeDir, "CLAUDE.md"), "mac rules\n")
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(f.claudeDir, "CLAUDE.md"), old, old); err != nil {
		t.Fatal(err)
	}

	if out, err := f.push(t, "--no-html"); err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}

	if got := mustRead(t, filepath.Join(p, "machines", "WIN-BOX", "CLAUDE.md")); got != "windows rules\n" {
		t.Errorf("the other machine's rules were touched: %q", got)
	}
	mine := thisMachinesProfile(t, f.archive, "WIN-BOX")
	if got := mustRead(t, filepath.Join(mine, "CLAUDE.md")); got != "mac rules\n" {
		t.Errorf("this machine's older rules are not in the archive: %q", got)
	}
}

// --update-profile replacing a local rule file that differs must say so, and must
// leave the local version recoverable from the archive.
func TestImportUpdateReportsAndKeepsAReplacedRuleFile(t *testing.T) {
	f := profileImportFixture(t)
	p := filepath.Join(f.archive, "profile")
	mustWrite(t, filepath.Join(p, "CLAUDE.md"), "archived rules\n")
	mustWrite(t, filepath.Join(f.claudeDir, "CLAUDE.md"), "local rules\n")
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(f.claudeDir, "CLAUDE.md"), old, old); err != nil {
		t.Fatal(err)
	}

	out, err := f.run(t, "import", "--search-root", f.dev, "--no-plugins", "--update-profile")
	if err != nil {
		t.Fatalf("import --update-profile failed: %v\n%s", err, out)
	}
	if got := mustRead(t, filepath.Join(f.claudeDir, "CLAUDE.md")); got != "archived rules\n" {
		t.Fatalf("--update-profile did not take the archived CLAUDE.md: %q\n%s", got, out)
	}
	if !strings.Contains(out, "replaced") || !strings.Contains(out, "CLAUDE.md") {
		t.Errorf("the replaced file was not reported:\n%s", out)
	}
	kept, _ := filepath.Glob(filepath.Join(p, "replaced", "*", "*", "CLAUDE.md"))
	if len(kept) != 1 || mustRead(t, kept[0]) != "local rules\n" {
		t.Errorf("the replaced local version is not recoverable from the archive: %v\n%s", kept, out)
	}
}

func TestDoctorReportsRulesThatDifferBetweenMachines(t *testing.T) {
	f := newFixture(t)
	p := filepath.Join(f.archive, "profile", "machines")
	mustWrite(t, filepath.Join(p, "WIN-BOX", "CLAUDE.md"), "windows rules\n")
	mustWrite(t, filepath.Join(p, "MAC-BOX", "CLAUDE.md"), "mac rules\n")
	mustWrite(t, filepath.Join(p, "WIN-BOX", "RTK.md"), "same\n")
	mustWrite(t, filepath.Join(p, "MAC-BOX", "RTK.md"), "same\n")

	out, _ := f.run(t, "doctor", "--no-write-probe")
	if !strings.Contains(out, "CLAUDE.md") || !strings.Contains(out, "differ") {
		t.Errorf("doctor did not report the differing CLAUDE.md:\n%s", out)
	}
	if strings.Contains(out, "RTK.md") {
		t.Errorf("doctor reported an identical file as differing:\n%s", out)
	}
}
