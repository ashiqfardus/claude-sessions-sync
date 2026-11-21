package main_test

// Regressions from the final whole-branch review of work folders: what someone who
// can write the shared archive must not be able to do to this machine.

import (
	"encoding/json"
	"io/fs"
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

// toolLineJSON is one assistant line in cwd whose Write targets write.
func toolLineJSON(cwd, uuid, write string) string {
	l, _ := json.Marshal(map[string]any{"type": "assistant", "cwd": cwd, "uuid": uuid, "sessionId": forkSession,
		"message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "name": "Write",
			"input": map[string]string{"file_path": write, "content": "x"}}}}})
	return string(l) + "\n"
}

// caseSensitive skips unless dir keeps case (Linux, or a case-sensitive volume).
func caseSensitive(t *testing.T, dir string) {
	t.Helper()
	p := filepath.Join(dir, "Probe")
	mustWrite(t, p, "x")
	_, err := os.Stat(filepath.Join(dir, "probe"))
	_ = os.Remove(p)
	if err == nil {
		t.Skip("this file system ignores case")
	}
}

// Two archived transcripts whose names differ only in case: where the file system
// keeps case both are filed, and the short one's size must not become the long
// one's mark (R25).
func TestCaseCollidingMarks(t *testing.T) {
	arch := t.TempDir()
	caseSensitive(t, arch)
	evil, mac := newMachine(t, "EVIL", arch), newMachine(t, "MAC", arch)
	homeE, homeM := t.TempDir(), t.TempDir()
	private := filepath.Join(homeM, "Private")
	mustWrite(t, filepath.Join(private, "salary.txt"), "very private\n")
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	mustWrite(t, mac.session(repo), sessionLines(repo, "m1"))
	mac.workRun(t, homeM, "push", "--no-html")

	configAs(t, evil.claudeDir, "EVIL", "work", "off")
	pub := filepath.Join(homeM, "Public")
	mustMkdir(t, pub)
	workSession(t, evil, pub, nil, "ls")
	evil.workRun(t, homeE, "push", "--no-html")
	bucket := filepath.Join(arch, "projects", claude.Slug(pub))
	pad := `{"type":"user","uuid":"p0","cwd":"` + pub + `","message":{"role":"user","content":"` + strings.Repeat("x", 200) + `"}}` + "\n"
	mustWrite(t, filepath.Join(bucket, "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA.jsonl"), pad+toolLineJSON(private, "h1", filepath.Join(private, "salary.txt")))
	mustWrite(t, filepath.Join(bucket, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa.jsonl"), `{"type":"user","uuid":"t1","cwd":"`+pub+`"}`+"\n")
	mac.workRun(t, homeM, "import", "--no-plugins")
	mac.workRun(t, homeM, "push", "--no-html")
	noExfil(t, arch)
}

// archiveFiles lists every file in the archive, for a failure message.
func archiveFiles(arch string) []string {
	var out []string
	_ = filepath.WalkDir(arch, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(arch, p)
			out = append(out, rel)
		}
		return nil
	})
	return out
}

// The record is lost and the archive writer has removed every trace of who wrote the
// session: nothing the archive says may decide what push reads (R26).
func TestRecordLostWriterInfoGone(t *testing.T) {
	mac, homeM, _ := exfilSetup(t)
	_ = os.Remove(filepath.Join(mac.claudeDir, "session-sync-base", "imported.json"))
	_ = os.Remove(filepath.Join(mac.archive, "index", "EVIL.json"))
	_ = os.Remove(filepath.Join(mac.archive, "manifest", "EVIL.json"))
	mac.workRun(t, homeM, "push", "--no-html")
	noExfil(t, mac.archive)
}

// The record is lost and a second, made-up machine lists the same session, so no
// writer can be told (R26).
func TestRecordLostWriterAmbiguous(t *testing.T) {
	mac, homeM, _ := exfilSetup(t)
	_ = os.Remove(filepath.Join(mac.claudeDir, "session-sync-base", "imported.json"))
	idx := filepath.Join(mac.archive, "index", "EVIL.json")
	raw, err := os.ReadFile(idx)
	if err != nil {
		t.Fatalf("no index file: %v (%v)", err, archiveFiles(mac.archive))
	}
	resize := func(machine string, size int) string {
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		m["machine"] = machine
		if ss, ok := m["sessions"].([]any); ok {
			for _, s := range ss {
				s.(map[string]any)["size"] = size
			}
		}
		b, _ := json.Marshal(m)
		return string(b)
	}
	mustWrite(t, filepath.Join(mac.archive, "index", "ZED.json"), resize("ZED", 1))
	mustWrite(t, idx, resize("EVIL", 2)) // EVIL's own row no longer matches its copy either
	mac.workRun(t, homeM, "push", "--no-html")
	noExfil(t, mac.archive)
}

// restore --source from a copy of the archive while the archive is offline records
// what it files all the same (R26).
func TestRestoreArchiveOffline(t *testing.T) {
	arch := filepath.Join(t.TempDir(), "arch")
	mustMkdir(t, arch)
	evil, mac := newMachine(t, "EVIL", arch), newMachine(t, "MAC", arch)
	homeE, homeM := t.TempDir(), t.TempDir()
	private := filepath.Join(homeM, "Private")
	mustWrite(t, filepath.Join(private, "salary.txt"), "very private\n")
	configAs(t, mac.claudeDir, "MAC", "set-destination", arch)
	mac.workRun(t, homeM, "push", "--no-html") // record exists
	configAs(t, evil.claudeDir, "EVIL", "work", "off")
	workSession(t, evil, private, nil, "ls")
	evil.workRun(t, homeE, "push", "--no-html")

	copied := t.TempDir()
	src := filepath.Join(arch, "projects", claude.Slug(private))
	entries, _ := os.ReadDir(src)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			mustWrite(t, filepath.Join(copied, e.Name()), mustRead(t, filepath.Join(src, e.Name())))
		}
	}
	if err := os.Rename(arch, arch+".off"); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "restore", "--claude-dir", mac.claudeDir, "--source", copied, "--project-path", private)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=", "CLAUDE_SESSIONS_MACHINE=MAC", "HOME="+homeM, "USERPROFILE="+homeM,
		"CLAUDE_SESSIONS_TEMP_DIRS="+filepath.Join(homeM, "no-temp"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	if err := os.Rename(arch+".off", arch); err != nil {
		t.Fatal(err)
	}
	mac.workRun(t, homeM, "push", "--no-html")
	noExfil(t, arch)
}

// EVIL's session carries a subagent transcript naming MAC's folders.
func TestImportedSubagentCannotExfiltrate(t *testing.T) {
	arch := t.TempDir()
	evil, mac := newMachine(t, "EVIL", arch), newMachine(t, "MAC", arch)
	homeE, homeM := t.TempDir(), t.TempDir()
	private := filepath.Join(homeM, "Private")
	mustWrite(t, filepath.Join(private, "salary.txt"), "very private\n")
	mustWrite(t, filepath.Join(homeM, "Documents", "passport.pdf"), "pdf bytes\n")
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	mustWrite(t, mac.session(repo), sessionLines(repo, "m1"))
	mac.workRun(t, homeM, "push", "--no-html")

	configAs(t, evil.claudeDir, "EVIL", "work", "off")
	repoE := gitRepo(t, filepath.Join(t.TempDir(), "dev", "evil"))
	tr := workSession(t, evil, repoE, nil, "ls")
	sub := filepath.Join(strings.TrimSuffix(tr, ".jsonl"), "subagents", "agent-1.jsonl")
	mustWrite(t, sub, toolLineJSON(private, "s1", filepath.Join(homeM, "Documents", "passport.pdf")))
	evil.workRun(t, homeE, "push", "--no-html")
	mac.workRun(t, homeM, "import", "--no-plugins")
	mac.workRun(t, homeM, "push", "--no-html")
	noExfil(t, arch)
}

// A subagent transcript planted into MAC's own archived session.
func TestPlantedSubagentCannotExfiltrate(t *testing.T) {
	arch := t.TempDir()
	mac := newMachine(t, "MAC", arch)
	homeM := t.TempDir()
	private := filepath.Join(homeM, "Private")
	mustWrite(t, filepath.Join(private, "salary.txt"), "very private\n")
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	mustWrite(t, mac.session(repo), sessionLines(repo, "m1"))
	mac.workRun(t, homeM, "push", "--no-html")
	mustWrite(t, filepath.Join(arch, "projects", claude.Slug(repo), forkSession, "subagents", "agent-9.jsonl"),
		toolLineJSON(private, "s9", filepath.Join(private, "salary.txt")))
	mac.workRun(t, homeM, "import", "--no-plugins")
	mac.workRun(t, homeM, "push", "--no-html")
	noExfil(t, arch)
}

// The archive writer extends MAC's own archived transcript; MAC imports with --update.
func TestUpdateExtendedOwnCannotExfiltrate(t *testing.T) {
	arch := t.TempDir()
	mac := newMachine(t, "MAC", arch)
	homeM := t.TempDir()
	private := filepath.Join(homeM, "Private")
	mustWrite(t, filepath.Join(private, "salary.txt"), "very private\n")
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	mustWrite(t, mac.session(repo), sessionLines(repo, "m1"))
	aged(t, mac.session(repo))
	mac.workRun(t, homeM, "push", "--no-html")
	a := filepath.Join(arch, "projects", claude.Slug(repo), forkSession+".jsonl")
	mustWrite(t, a, mustRead(t, a)+toolLineJSON(private, "evil1", filepath.Join(private, "salary.txt")))
	mac.workRun(t, homeM, "import", "--no-plugins", "--update")
	mac.workRun(t, homeM, "push", "--no-html")
	noExfil(t, arch)
}

// pull files EVIL's transcript at its recorded path.
func TestPulledTranscriptCannotExfiltrate(t *testing.T) {
	arch := t.TempDir()
	evil, mac := newMachine(t, "EVIL", arch), newMachine(t, "MAC", arch)
	homeE, homeM := t.TempDir(), t.TempDir()
	private := filepath.Join(homeM, "Private")
	mustWrite(t, filepath.Join(private, "salary.txt"), "very private\n")
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	mustWrite(t, mac.session(repo), sessionLines(repo, "m1"))
	mac.workRun(t, homeM, "push", "--no-html")
	configAs(t, evil.claudeDir, "EVIL", "work", "off")
	workSession(t, evil, private, nil, "ls")
	evil.workRun(t, homeE, "push", "--no-html")
	mac.workRun(t, homeM, "pull", "--no-plugins")
	if _, err := os.Stat(mac.session(private)); err != nil {
		t.Fatalf("not pulled: %v", err)
	}
	mac.workRun(t, homeM, "push", "--no-html")
	noExfil(t, arch)
}

// A link at work/<machine> itself, or at work/, is never written through (C1).
func TestPushRefusesLinkedWorkRoot(t *testing.T) {
	for _, level := range []string{"machine", "work"} {
		m := newMachine(t, "MAC", t.TempDir())
		home, _, _ := poSession(t, m)
		docs := filepath.Join(home, "Docs")
		mustMkdir(t, docs)
		mustMkdir(t, filepath.Join(m.archive, "work"))
		link := filepath.Join(m.archive, "work", "MAC")
		if level == "work" {
			link = filepath.Join(m.archive, "work")
			_ = os.Remove(link)
		}
		symlink(t, docs, link)
		m.workRun(t, home, "push", "--no-html")
		if got := archiveFiles(docs); len(got) != 0 {
			t.Errorf("%s: push wrote through a link into %s: %v", level, docs, got)
		}
	}
}

// A link at work/<former name> or work/<machine>: exclude-folder never deletes the
// local folder it leads to (C1, M6).
func TestPurgeRefusesLinkedMachineFolder(t *testing.T) {
	for _, n := range []string{"OLD", "MAC"} {
		arch := t.TempDir()
		m := newMachine(t, "MAC", arch)
		configAs(t, m.claudeDir, "MAC", "set-destination", arch)
		configAs(t, m.claudeDir, "OLD", "set-machine", "MAC")
		home := t.TempDir()
		priv := filepath.Join(home, "SQL", "private")
		mustWrite(t, filepath.Join(priv, "keep-me.md"), "local only\n")
		// work/<n> leads to a folder whose encoded path ends at the real home.
		fake := t.TempDir()
		enc := filepath.Join(fake, filepath.FromSlash(work.Encode(home)))
		mustMkdir(t, filepath.Dir(enc))
		symlink(t, home, enc)
		mustMkdir(t, filepath.Join(arch, "work"))
		symlink(t, fake, filepath.Join(arch, "work", n))
		out := configAs(t, m.claudeDir, "MAC", "exclude-folder", priv)
		if _, err := os.Stat(filepath.Join(priv, "keep-me.md")); err != nil {
			t.Errorf("%s: local folder deleted: %v\n%s", n, err, out)
		}
	}
}

// The purged copy is itself a link: only the link goes, never what it leads to (C1).
func TestPurgeFinalLinkOnly(t *testing.T) {
	arch := t.TempDir()
	m := newMachine(t, "MAC", arch)
	configAs(t, m.claudeDir, "MAC", "set-destination", arch)
	home := t.TempDir()
	priv := filepath.Join(home, "SQL", "private")
	mustWrite(t, filepath.Join(priv, "keep-me.md"), "local only\n")
	other := filepath.Join(home, "Other")
	mustWrite(t, filepath.Join(other, "keep.md"), "x\n")
	copyDir := filepath.Join(arch, "work", "MAC", filepath.FromSlash(work.Encode(priv)))
	mustMkdir(t, filepath.Dir(copyDir))
	symlink(t, other, copyDir)
	configAs(t, m.claudeDir, "MAC", "exclude-folder", priv)
	if _, err := os.Stat(filepath.Join(other, "keep.md")); err != nil {
		t.Errorf("followed the final link: %v", err)
	}
}

// MAC works in ~/notes but has not pushed since, so the folder is in no local record
// when EVIL's folder is placed there: it was there before, and is never the old end
// of a remap (R27).
func TestRemapNeverMovesOwnUnpushed(t *testing.T) {
	arch := t.TempDir()
	mac := newMachine(t, "MAC", arch)
	homeM := t.TempDir()
	notes := filepath.Join(homeM, "notes")
	mustWrite(t, filepath.Join(notes, "x.md"), "mine\n")
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	own := workSession(t, mac, repo, []string{filepath.Join(notes, "x.md")})
	aged(t, own)
	before := mustRead(t, own)
	other := newMachine(t, "OTHER", arch)
	repoO := gitRepo(t, filepath.Join(t.TempDir(), "dev", "other"))
	mustWrite(t, other.session(repoO), sessionLines(repoO, "o1"))
	other.workRun(t, t.TempDir(), "push", "--no-html")
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

// importedKeyOf is how the record names a transcript: its path below projects/.
func importedKeyOf(m machine, p string) string {
	rel, _ := filepath.Rel(filepath.Join(m.claudeDir, "projects"), p)
	return filepath.ToSlash(rel)
}

// An import killed after a transcript landed and before its size was journaled leaves
// it pending in imported.log: the whole file counts as filed (R28).
func TestKillWindowState(t *testing.T) {
	mac, homeM, filed := exfilSetup(t)
	base := filepath.Join(mac.claudeDir, "session-sync-base")
	mustWrite(t, filepath.Join(base, "imported.json"), "{}\n") // as compacted before that transcript
	line, _ := json.Marshal(map[string]string{"pending": importedKeyOf(mac, filed)})
	mustWrite(t, filepath.Join(base, "imported.log"), string(line)+"\n")
	mac.workRun(t, homeM, "push", "--no-html")
	noExfil(t, mac.archive)
}

// A run that finishes folds its journal into the record: no journal left, and each
// transcript filed at its size (R28).
func TestImportJournalCompacted(t *testing.T) {
	mac, _, filed := exfilSetup(t)
	base := filepath.Join(mac.claudeDir, "session-sync-base")
	if _, err := os.Stat(filepath.Join(base, "imported.log")); !os.IsNotExist(err) {
		t.Errorf("journal left after a finished import: %v", err)
	}
	var marks map[string]int64
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(base, "imported.json"))), &marks); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(filed)
	if got := marks[importedKeyOf(mac, filed)]; got != st.Size() {
		t.Errorf("mark %d, want %d: %v", got, st.Size(), marks)
	}
}

// Nothing is filed that the journal could not record: the import stops with an error
// (R28).
func TestImportAbortsWhenJournalFails(t *testing.T) {
	arch := t.TempDir()
	evil, mac := newMachine(t, "EVIL", arch), newMachine(t, "MAC", arch)
	homeE, homeM := t.TempDir(), t.TempDir()
	private := filepath.Join(homeM, "Private")
	mustWrite(t, filepath.Join(private, "salary.txt"), "very private\n")
	configAs(t, evil.claudeDir, "EVIL", "work", "off")
	workSession(t, evil, private, nil, "ls")
	evil.workRun(t, homeE, "push", "--no-html")
	// A folder where the journal goes: no append can succeed.
	mustWrite(t, filepath.Join(mac.claudeDir, "session-sync-base", "imported.log", "x"), "x")
	out, err := mac.workCmd(homeM, "import", "--no-plugins").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "imported.log") {
		t.Errorf("import went on: %v\n%s", err, out)
	}
	if _, err := os.Stat(mac.session(private)); !os.IsNotExist(err) {
		t.Errorf("filed without a record: %v", err)
	}
}

// What the record says about itself goes to stderr, and says what is true: import
// --json stays one JSON document (R28, N4/N5).
func TestImportJSONWhenRecordFails(t *testing.T) {
	mac, homeM, _ := exfilSetup(t)
	rec := filepath.Join(mac.claudeDir, "session-sync-base", "imported.json")
	if err := os.Remove(rec); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(rec, "x"), "x") // a folder: it can be neither read nor saved
	cmd := mac.workCmd(homeM, "import", "--no-plugins", "--json")
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("import: %v\n%s%s", err, stdout.String(), stderr.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(stdout.String()), &rows); err != nil {
		t.Errorf("stdout is not JSON: %v\n%s", err, stdout.String())
	}
	if !strings.Contains(stderr.String(), "imported.json") || strings.Contains(stderr.String(), "already read") {
		t.Errorf("stderr: %q", stderr.String())
	}
}

// Subagent transcripts whose names differ only in case: the short one's size must not
// become the long one's mark (R25, subagent half).
func TestSubagentCaseCollidingMarks(t *testing.T) {
	arch := t.TempDir()
	caseSensitive(t, arch)
	evil, mac := newMachine(t, "EVIL", arch), newMachine(t, "MAC", arch)
	homeE, homeM := t.TempDir(), t.TempDir()
	private := filepath.Join(homeM, "Private")
	mustWrite(t, filepath.Join(private, "salary.txt"), "very private\n")
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	mustWrite(t, mac.session(repo), sessionLines(repo, "m1"))
	mac.workRun(t, homeM, "push", "--no-html")

	configAs(t, evil.claudeDir, "EVIL", "work", "off")
	repoE := gitRepo(t, filepath.Join(t.TempDir(), "dev", "evil"))
	tr := workSession(t, evil, repoE, nil, "ls")
	dir := filepath.Join(strings.TrimSuffix(tr, ".jsonl"), "subagents")
	pad := `{"type":"user","uuid":"p0","cwd":"` + repoE + `","message":{"role":"user","content":"` + strings.Repeat("x", 400) + `"}}` + "\n"
	mustWrite(t, filepath.Join(dir, "agent-A.jsonl"), pad+toolLineJSON(private, "s1", filepath.Join(private, "salary.txt")))
	mustWrite(t, filepath.Join(dir, "agent-a.jsonl"), `{"type":"user","uuid":"t1","cwd":"`+repoE+`"}`+"\n")
	evil.workRun(t, homeE, "push", "--no-html")
	mac.workRun(t, homeM, "import", "--no-plugins")
	mac.workRun(t, homeM, "push", "--no-html")
	noExfil(t, arch)
}

// A first run that is a dry run writes no record, and the real run after it still
// marks what was here.
func TestFirstRunDryRunThenPush(t *testing.T) {
	mac, homeM, _ := exfilSetup(t)
	rec := filepath.Join(mac.claudeDir, "session-sync-base", "imported.json")
	_ = os.Remove(rec)
	mac.workRun(t, homeM, "import", "--no-plugins", "--dry-run")
	mac.workRun(t, homeM, "push", "--no-html", "--dry-run")
	if _, err := os.Stat(rec); err == nil {
		t.Errorf("dry runs wrote the record")
	}
	mac.workRun(t, homeM, "push", "--no-html")
	noExfil(t, mac.archive)
}

// Filed by an older build with no record, a session's hostile subagent transcript is
// marked by the first run too.
func TestFirstRunMarksSubagent(t *testing.T) {
	arch := t.TempDir()
	evil, mac := newMachine(t, "EVIL", arch), newMachine(t, "MAC", arch)
	homeE, homeM := t.TempDir(), t.TempDir()
	private := filepath.Join(homeM, "Private")
	mustWrite(t, filepath.Join(private, "salary.txt"), "very private\n")
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	mustWrite(t, mac.session(repo), sessionLines(repo, "m1"))
	mac.workRun(t, homeM, "push", "--no-html")
	configAs(t, evil.claudeDir, "EVIL", "work", "off")
	repoE := gitRepo(t, filepath.Join(t.TempDir(), "dev", "evil"))
	tr := workSession(t, evil, repoE, nil, "ls")
	mustWrite(t, filepath.Join(strings.TrimSuffix(tr, ".jsonl"), "subagents", "agent-1.jsonl"), toolLineJSON(private, "s1", filepath.Join(private, "salary.txt")))
	evil.workRun(t, homeE, "push", "--no-html")
	mac.workRun(t, homeM, "import", "--no-plugins")
	_ = os.Remove(filepath.Join(mac.claudeDir, "session-sync-base", "imported.json"))
	mac.workRun(t, homeM, "push", "--no-html")
	noExfil(t, arch)
}

// A first run with work off still marks what is here, so turning work on later reads
// none of it.
func TestFirstRunWorkOffThenOn(t *testing.T) {
	mac, homeM, _ := exfilSetup(t)
	_ = os.Remove(filepath.Join(mac.claudeDir, "session-sync-base", "imported.json"))
	configAs(t, mac.claudeDir, "MAC", "work", "off")
	mac.workRun(t, homeM, "push", "--no-html")
	configAs(t, mac.claudeDir, "MAC", "work", "on")
	mac.workRun(t, homeM, "push", "--no-html")
	noExfil(t, mac.archive)
}

// A fork copy planted under forks/<bucket>/<id>/EVIL.jsonl, with a subagent planted in
// the archived session folder, for a session MAC holds.
func TestPlantedForkCopyCannotExfiltrate(t *testing.T) {
	arch := t.TempDir()
	mac := newMachine(t, "MAC", arch)
	homeM := t.TempDir()
	private := filepath.Join(homeM, "Private")
	mustWrite(t, filepath.Join(private, "salary.txt"), "very private\n")
	repo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	mustWrite(t, mac.session(repo), sessionLines(repo, "m1"))
	aged(t, mac.session(repo))
	mac.workRun(t, homeM, "push", "--no-html")
	b := claude.Slug(repo)
	mustWrite(t, filepath.Join(arch, "forks", b, forkSession, "EVIL.jsonl"),
		sessionLines(repo, "m1")+toolLineJSON(private, "f1", filepath.Join(private, "salary.txt")))
	mustWrite(t, filepath.Join(arch, "projects", b, forkSession, "subagents", "agent-7.jsonl"),
		toolLineJSON(private, "s7", filepath.Join(private, "salary.txt")))
	mac.workRun(t, homeM, "import", "--no-plugins", "--update")
	mac.workRun(t, homeM, "push", "--no-html")
	noExfil(t, arch)
}
