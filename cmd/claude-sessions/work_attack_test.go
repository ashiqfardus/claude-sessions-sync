package main_test

// Regressions from the final whole-branch review of work folders: what someone who
// can write the shared archive must not be able to do to this machine.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// exfilSetup is MAC after importing EVIL's session, which names MAC's private folder as
// its cwd and MAC's passport as a file it wrote. It returns MAC's copy of the session.
func exfilSetup(t *testing.T) (mac machine, homeM, filed string) {
	t.Helper()
	arch := t.TempDir()
	evil := newMachine(t, "EVIL", arch)
	mac = newMachine(t, "MAC", arch)
	homeE := t.TempDir()
	homeM = t.TempDir()
	private := filepath.Join(homeM, "Private")
	mustWrite(t, filepath.Join(private, "salary.txt"), "very private\n")
	mustWrite(t, filepath.Join(homeM, "Documents", "passport.pdf"), "pdf bytes\n")
	// MAC's own session somewhere harmless, so MAC has a shard.
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	mustWrite(t, mac.session(repo), sessionLines(repo, "m1"))
	mac.workRun(t, homeM, "push", "--no-html")

	configAs(t, evil.claudeDir, "EVIL", "work", "off")
	workSession(t, evil, private, []string{filepath.Join(homeM, "Documents", "passport.pdf")}, "ls")
	evil.workRun(t, homeE, "push", "--no-html")

	mac.workRun(t, homeM, "import", "--no-plugins")
	filed = mac.session(private)
	if _, err := os.Stat(filed); err != nil {
		t.Fatalf("EVIL's session not filed: %v", err)
	}
	return mac, homeM, filed
}

func noExfil(t *testing.T, arch string) {
	t.Helper()
	for _, f := range workFiles(arch) {
		if strings.Contains(f, "salary.txt") || strings.Contains(f, "passport.pdf") {
			t.Errorf("%s uploaded from MAC because an imported transcript named it", f)
		}
	}
}

// A transcript filed by import names a private folder of this machine; the next push
// must not upload that folder.
func TestImportedTranscriptCannotExfiltrate(t *testing.T) {
	mac, homeM, _ := exfilSetup(t)
	mac.workRun(t, homeM, "push", "--no-html")
	noExfil(t, mac.archive)
}

// Continued here after import, a conversation's new lines name folders as any other's.
func TestImportedTranscriptContinuedHere(t *testing.T) {
	mac, homeM, filed := exfilSetup(t)
	mine := filepath.Join(homeM, "Mine", "n.md")
	mustWrite(t, mine, "written here\n")
	// Resumed from a repository, which is never a work folder: only the Write counts.
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "web"))
	line, _ := json.Marshal(map[string]any{"type": "assistant", "cwd": repo, "uuid": "here1",
		"message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "name": "Write",
			"input": map[string]string{"file_path": mine, "content": "x"}}}}})
	f, err := os.OpenFile(filed, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write(append(line, '\n'))
	f.Close()

	mac.workRun(t, homeM, "push", "--no-html")
	noExfil(t, mac.archive)
	if _, err := os.Stat(filepath.Join(workCopy(mac, filepath.Dir(mine)), "n.md")); err != nil {
		t.Errorf("the folder named by the line added here is not backed up: %v\n%v", err, workFiles(mac.archive))
	}
}

// Filed by a version that kept no record: the first run rebuilds it from the archive.
func TestImportedBeforeRecordIsSeeded(t *testing.T) {
	mac, homeM, _ := exfilSetup(t)
	if err := os.Remove(filepath.Join(mac.claudeDir, "session-sync-base", "imported.json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	mac.workRun(t, homeM, "push", "--no-html")
	noExfil(t, mac.archive)
}

// An import killed before it saved the record leaves a mark; the next run rebuilds it.
func TestImportKilledBeforeRecordIsSeeded(t *testing.T) {
	mac, homeM, _ := exfilSetup(t)
	rec := filepath.Join(mac.claudeDir, "session-sync-base", "imported.json")
	mustWrite(t, rec, "{}\n")
	mustWrite(t, rec+".pending", "")
	mac.workRun(t, homeM, "push", "--no-html")
	noExfil(t, mac.archive)
	if _, err := os.Stat(rec + ".pending"); !os.IsNotExist(err) {
		t.Errorf("mark left: %v", err)
	}
}
