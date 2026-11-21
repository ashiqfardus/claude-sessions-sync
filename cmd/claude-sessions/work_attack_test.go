package main_test

// Regressions from the final whole-branch review of work folders: what someone who
// can write the shared archive must not be able to do to this machine.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
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

// Two imports with the other machine's shard changed in between give a remap whose
// old end is one of this machine's own folders; --repoint must not rewrite this
// machine's own conversation.
func TestRemapNeverMovesOwnTarget(t *testing.T) {
	arch := t.TempDir()
	mac := newMachine(t, "MAC", arch)
	homeM := t.TempDir()
	notes := filepath.Join(homeM, "notes")
	mustWrite(t, filepath.Join(notes, "x.md"), "mine\n")
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	own := workSession(t, mac, repo, []string{filepath.Join(notes, "x.md")})
	aged(t, own)
	mac.workRun(t, homeM, "push", "--no-html")
	before := mustRead(t, own)

	// A hand-written EVIL shard: home /evilhome, folder /evilhome/notes, with a copy.
	writeEvil := func(home string) {
		b, _ := json.Marshal(map[string]any{"schemaVersion": 1, "machine": "EVIL", "home": home, "projects": map[string]any{},
			"workFolders": []map[string]any{{"path": "/evilhome/notes"}}})
		mustWrite(t, filepath.Join(arch, "manifest", "EVIL.json"), string(b))
	}
	mustWrite(t, filepath.Join(arch, "work", "EVIL", "evilhome", "notes", "a.md"), "a\n")
	writeEvil("/evilhome")
	mac.workRun(t, homeM, "import", "--no-plugins")
	writeEvil("/elsewhere")
	mac.workRun(t, homeM, "import", "--no-plugins")
	out := mac.workRun(t, homeM, "import", "--no-plugins", "--repoint")
	if after := mustRead(t, own); after != before {
		t.Errorf("own conversation rewritten:\n%s\n%s", after, out)
	}
}

// A Windows folder mapped on a Unix machine, the session started in it or below it:
// the project resolves through the work folder, and the rest of every path is
// converted.
func TestWindowsToUnixTailConverted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows-form paths are native here")
	}
	for _, sub := range []string{"", `\sub`} {
		arch := t.TempDir()
		win, mac := newMachine(t, "WIN", arch), newMachine(t, "MAC", arch)
		homeW, homeM := t.TempDir(), t.TempDir()
		tr := workSession(t, win, `E:\SQL\po`+sub, []string{`E:\SQL\po\r.md`, `E:\SQL\po` + sub + `\x.md`}, `cd "E:\SQL\po" && sqlcmd -i q.sql -o out.csv`)
		aged(t, tr)
		win.workRun(t, homeW, "push", "--no-html")
		sp := filepath.Join(arch, "manifest", "WIN.json")
		var s map[string]any
		if err := json.Unmarshal([]byte(mustRead(t, sp)), &s); err != nil {
			t.Fatal(err)
		}
		s["home"] = `C:\Users\a`
		s["workFolders"] = []map[string]any{{"path": `E:\SQL\po`, "files": 2}}
		b, _ := json.Marshal(s)
		mustWrite(t, sp, string(b))
		mustWrite(t, filepath.Join(arch, "work", "WIN", "E", "SQL", "po", "r.md"), "report\n")
		mustWrite(t, filepath.Join(arch, "work", "WIN", "E", "SQL", "po", "out.csv"), "a,b\n")

		sql := filepath.Join(homeM, "SQL")
		mustMkdir(t, sql)
		configAs(t, mac.claudeDir, "MAC", "map-folder", `E:\SQL`, sql)
		out := mac.workRun(t, homeM, "import", "--no-plugins")
		if _, err := os.Stat(filepath.Join(sql, "po", "r.md")); err != nil {
			t.Fatalf("not placed: %v\n%s", err, out)
		}
		project := filepath.Join(sql, "po", strings.TrimPrefix(sub, `\`))
		got := mustRead(t, mac.session(project))
		if !strings.Contains(got, inJSON(filepath.Join(sql, "po", "r.md"))) || !strings.Contains(got, inJSON(filepath.Join(project, "x.md"))) ||
			strings.Contains(got, `E:`) || strings.Contains(got, `\\`) {
			t.Errorf("cwd E:\\SQL\\po%s: not fully repointed:\n%s", sub, got)
		}
	}
}

// caseInsensitive skips unless dir ignores case, as macOS and Windows file systems do.
func caseInsensitive(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "linux" {
		t.Skip("Linux file systems keep case")
	}
	p := filepath.Join(dir, "Probe")
	mustWrite(t, p, "x")
	_, err := os.Stat(filepath.Join(dir, "probe"))
	_ = os.Remove(p)
	if err != nil {
		t.Skip("this file system keeps case")
	}
}

// An exclusion holds for the folder named in another case: on macOS it is the same one.
func TestExcludeFolderIgnoresCase(t *testing.T) {
	m := newMachine(t, "MAC", t.TempDir())
	home := t.TempDir()
	caseInsensitive(t, home)
	po := filepath.Join(home, "SQL", "po")
	mustWrite(t, filepath.Join(po, "r.md"), "report\n")
	mustWrite(t, filepath.Join(po, "private", "pay.md"), "private\n")
	configAs(t, m.claudeDir, "MAC", "exclude-folder", filepath.Join(po, "private"))
	lower := filepath.Join(home, "sql", "po")
	workSession(t, m, lower, []string{filepath.Join(lower, "r.md")})
	m.workRun(t, home, "push", "--no-html")
	files := workFiles(m.archive)
	if !slices.ContainsFunc(files, func(f string) bool { return strings.HasSuffix(f, "/r.md") }) {
		t.Fatalf("folder not backed up: %v", files)
	}
	if slices.ContainsFunc(files, func(f string) bool { return strings.HasSuffix(f, "/pay.md") }) {
		t.Errorf("excluded folder uploaded: %v", files)
	}
}

// A restored file is still the other machine's when B's session names its folder in
// another case.
func TestHeldFileIgnoresCase(t *testing.T) {
	p := newWorkPair(t)
	caseInsensitive(t, p.homeB)
	p.b.workRun(t, p.homeB, "import", "--no-plugins")
	lower := filepath.Join(p.homeB, "my work", "run 1")
	mustWrite(t, filepath.Join(lower, "b.md"), "B's own\n")
	workSession(t, p.b, gitRepo(t, filepath.Join(t.TempDir(), "dev", "web")), []string{filepath.Join(lower, "b.md")})
	p.b.workRun(t, p.homeB, "push", "--no-html")
	for _, f := range workFiles(p.b.archive) {
		if strings.HasPrefix(f, "MAC/") && strings.HasSuffix(f, "রিপোর্ট.md") {
			t.Errorf("A's restored file pushed as B's: %s", f)
		}
	}
}

// restore from an archive bucket files transcripts too: the same record holds.
func TestRestoredTranscriptCannotExfiltrate(t *testing.T) {
	arch := t.TempDir()
	evil, mac := newMachine(t, "EVIL", arch), newMachine(t, "MAC", arch)
	homeE, homeM := t.TempDir(), t.TempDir()
	private := filepath.Join(homeM, "Private")
	mustWrite(t, filepath.Join(private, "salary.txt"), "very private\n")
	configAs(t, mac.claudeDir, "MAC", "set-destination", arch)
	mac.workRun(t, homeM, "push", "--no-html") // MAC keeps a record already
	configAs(t, evil.claudeDir, "EVIL", "work", "off")
	workSession(t, evil, private, nil, "ls")
	evil.workRun(t, homeE, "push", "--no-html")

	cmd := exec.Command(binary, "restore", "--claude-dir", mac.claudeDir,
		"--source", filepath.Join(arch, "projects", claude.Slug(private)), "--project-path", private)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=", "CLAUDE_SESSIONS_MACHINE=MAC", "HOME="+homeM, "USERPROFILE="+homeM,
		"CLAUDE_SESSIONS_TEMP_DIRS="+filepath.Join(homeM, "no-temp"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	mac.workRun(t, homeM, "push", "--no-html")
	noExfil(t, arch)
}
