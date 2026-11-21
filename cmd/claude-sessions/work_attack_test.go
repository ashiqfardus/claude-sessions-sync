package main_test

// Regressions from the final whole-branch review of work folders: what someone who
// can write the shared archive must not be able to do to this machine.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ashiqfardus/claude-sessions-sync/internal/work"
)

// symlink plants a link inside a test's temp tree, or skips where links need rights
// the runner lacks.
func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
}

// A link planted in work/<me>/ must not turn the exclusion purge into a delete of
// the user's real folder.
func TestPurgeRefusesPlantedLink(t *testing.T) {
	arch := t.TempDir()
	m := newMachine(t, "MAC", arch)
	configAs(t, m.claudeDir, "MAC", "set-destination", arch)
	home := t.TempDir()
	sql := filepath.Join(home, "SQL")
	priv := filepath.Join(sql, "private")
	mustWrite(t, filepath.Join(priv, "keep-me.md"), "local only\n")

	encHome := filepath.Join(arch, "work", "MAC", filepath.FromSlash(work.Encode(home)))
	mustMkdir(t, encHome)
	symlink(t, sql, filepath.Join(encHome, "SQL"))
	seedShard(t, arch, sql)
	configAs(t, m.claudeDir, "MAC", "exclude-folder", priv)
	if _, err := os.Stat(filepath.Join(priv, "keep-me.md")); err != nil {
		t.Errorf("local %s deleted by exclude-folder: %v", priv, err)
	}
}

// The same planted link must not make push write into another local folder.
func TestPushRefusesPlantedLink(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home, po, _ := poSession(t, m)
	docs := filepath.Join(home, "Docs")
	mustWrite(t, filepath.Join(docs, "r.md"), "an unrelated document\n")
	aged(t, filepath.Join(docs, "r.md"))
	enc := filepath.Join(m.archive, "work", "MAC", filepath.FromSlash(work.Encode(filepath.Dir(po))))
	mustMkdir(t, enc)
	symlink(t, docs, filepath.Join(enc, "po"))
	m.workRun(t, home, "push", "--no-html")
	if got := mustRead(t, filepath.Join(docs, "r.md")); got != "an unrelated document\n" {
		t.Errorf("local file overwritten by push through a planted link: %q", got)
	}
}
