package main_test

// Two machines sharing one archive, over several rounds - the cases a single push or
// import never reaches. CLAUDE_SESSIONS_MACHINE gives each its own name.

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
)

type machine struct {
	name, claudeDir, archive string
}

func newMachine(t *testing.T, name, archive string) machine {
	t.Helper()
	m := machine{name: name, claudeDir: filepath.Join(t.TempDir(), ".claude"), archive: archive}
	mustMkdir(t, filepath.Join(m.claudeDir, "projects"))
	return m
}

func (m machine) run(t *testing.T, args ...string) string {
	t.Helper()
	full := append([]string{args[0], "--claude-dir", m.claudeDir, "--archive", m.archive}, args[1:]...)
	cmd := exec.Command(binary, full...)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=", "CLAUDE_SESSIONS_MACHINE="+m.name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", m.name, args, err, out)
	}
	return string(out)
}

func (m machine) hook(t *testing.T, transcript string) {
	t.Helper()
	cmd := exec.Command(binary, "push", "--claude-dir", m.claudeDir, "--archive", m.archive, "--quiet", "--from-hook")
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR=", "CLAUDE_SESSIONS_MACHINE="+m.name)
	cmd.Stdin = strings.NewReader(`{"transcript_path":` + quoted(transcript) + `}`)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("hook: %v\n%s", err, out)
	}
}

func (m machine) session(repo string) string {
	return filepath.Join(m.claudeDir, "projects", claude.Slug(repo), forkSession+".jsonl")
}

func (m machine) sessions(t *testing.T, repo string) []string {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Join(m.claudeDir, "projects", claude.Slug(repo)))
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// contents is every turn this machine holds for the project, across its sessions.
func (m machine) holds(t *testing.T, repo, turn string) bool {
	t.Helper()
	for _, s := range m.sessions(t, repo) {
		if strings.Contains(mustRead(t, filepath.Join(m.claudeDir, "projects", claude.Slug(repo), s)), `"`+turn+`"`) {
			return true
		}
	}
	return false
}

// away moves dir aside while fn runs.
func away(t *testing.T, dir string, fn func()) {
	t.Helper()
	if err := os.Rename(dir, dir+".away"); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(dir+".away", dir)
	fn()
}

func sessionLines(cwd string, uuids ...string) string {
	return strings.ReplaceAll(uuidLines(uuids...), `E:\\work\\api`, strings.ReplaceAll(cwd, `\`, `\\`))
}

func gitRepo(t *testing.T, dir string) string {
	t.Helper()
	mustWrite(t, filepath.Join(dir, ".git", "config"), "[remote \"origin\"]\n\turl = git@github.com:example/api.git\n")
	return dir
}

// The user's own case: different paths, a session continued on both, then switching
// back and forth. Every turn reaches both machines, and each ends with the session
// and one fork - no copies multiplying round after round.
func TestTwoMachinesWithDifferentPathsSettle(t *testing.T) {
	archive := t.TempDir()
	mac, win := newMachine(t, "MAC", archive), newMachine(t, "WIN", archive)
	macRepo := gitRepo(t, filepath.Join(t.TempDir(), "dev", "api"))
	winRepo := gitRepo(t, filepath.Join(t.TempDir(), "work", "api"))
	mustWrite(t, mac.session(macRepo), sessionLines(macRepo, "u1", "m1"))
	mustWrite(t, win.session(winRepo), sessionLines(winRepo, "u1", "w1"))

	for round := 0; round < 3; round++ {
		mac.run(t, "push", "--no-html")
		win.run(t, "push", "--no-html")
		// One filesystem stands in for two: the other machine's folder is put away
		// while each imports, as it would not exist there.
		away(t, winRepo, func() { mac.run(t, "import", "--search-root", filepath.Dir(macRepo), "--update", "--no-plugins") })
		away(t, macRepo, func() { win.run(t, "import", "--search-root", filepath.Dir(winRepo), "--update", "--no-plugins") })
	}
	for _, m := range []struct {
		machine
		repo string
	}{{mac, macRepo}, {win, winRepo}} {
		if got := m.sessions(t, m.repo); len(got) != 2 {
			t.Errorf("%s holds %d sessions after three rounds, want the session and one fork: %v", m.name, len(got), got)
		}
		if !m.holds(t, m.repo, "m1") || !m.holds(t, m.repo, "w1") {
			t.Errorf("%s is missing a turn", m.name)
		}
	}
}

// Shared layout, with the writer's index stale after its per-reply hook: the fork must
// still be tagged with the machine that wrote it, so it is filed once, and the writer
// never gets its own conversation back (#1).
func TestSharedLayoutForkIsFiledOnceAfterAHookPush(t *testing.T) {
	archive := t.TempDir()
	a, b := newMachine(t, "BOX-A", archive), newMachine(t, "BOX-B", archive)
	repo := gitRepo(t, filepath.Join(t.TempDir(), "api"))
	root := filepath.Dir(repo)

	mustWrite(t, b.session(repo), sessionLines(repo, "u1", "b0"))
	b.run(t, "push", "--no-html")
	mustWrite(t, b.session(repo), sessionLines(repo, "u1", "b0", "b1"))
	b.hook(t, b.session(repo)) // archived, but B's index still says b0

	mustWrite(t, a.session(repo), sessionLines(repo, "u1", "b0", "a1"))
	later := time.Now().Add(time.Minute) // A's turn came after B's, as it would
	os.Chtimes(a.session(repo), later, later)
	a.run(t, "push", "--no-html")
	a.run(t, "import", "--search-root", root, "--update", "--no-plugins")
	b.run(t, "push", "--no-html") // B's sweep: its index catches up
	a.run(t, "import", "--search-root", root, "--update", "--no-plugins")
	a.run(t, "push", "--no-html")

	if got := a.sessions(t, repo); len(got) != 2 {
		t.Errorf("A holds %v, want its session and one fork of B's", got)
	}
	b.run(t, "import", "--search-root", root, "--update", "--no-plugins")
	if got := b.sessions(t, repo); len(got) != 2 {
		t.Errorf("B holds %v, want its session and one fork of A's - not its own copy back", got)
	}
	if !b.holds(t, repo, "a1") {
		t.Error("A's turn did not reach B")
	}
	if !a.holds(t, repo, "b1") {
		t.Error("B's turn did not reach A")
	}
}

// A memory entry deleted on one machine stays deleted, and a new one from the other
// machine still arrives (#2).
func TestMemoryDeletionSurvivesARound(t *testing.T) {
	archive := t.TempDir()
	a, b := newMachine(t, "BOX-A", archive), newMachine(t, "BOX-B", archive)
	aRepo := gitRepo(t, filepath.Join(t.TempDir(), "x", "api"))
	bRepo := gitRepo(t, filepath.Join(t.TempDir(), "y", "api"))
	aMem := filepath.Join(a.claudeDir, "projects", claude.Slug(aRepo), "memory")
	bMem := filepath.Join(b.claudeDir, "projects", claude.Slug(bRepo), "memory")
	mustWrite(t, a.session(aRepo), sessionLines(aRepo, "u1"))
	mustWrite(t, b.session(bRepo), sessionLines(bRepo, "u1"))
	mustWrite(t, filepath.Join(bMem, "MEMORY.md"), "- [X](x.md) - x\n- [Y](y.md) - y\n")
	mustWrite(t, filepath.Join(bMem, "x.md"), "x\n")
	mustWrite(t, filepath.Join(bMem, "y.md"), "y\n")

	sync := func() {
		b.run(t, "push", "--no-html")
		a.run(t, "push", "--no-html")
		away(t, bRepo, func() { a.run(t, "import", "--search-root", filepath.Dir(aRepo), "--update", "--no-plugins") })
		a.run(t, "push", "--no-html")
		away(t, aRepo, func() { b.run(t, "import", "--search-root", filepath.Dir(bRepo), "--update", "--no-plugins") })
	}
	sync()
	if got := mustRead(t, filepath.Join(aMem, "MEMORY.md")); !strings.Contains(got, "y.md") {
		t.Fatalf("A never received B's entries:\n%s", got)
	}

	// B forgets Y; A adds Z - minutes later, as edits are, not within the 2 s that
	// push counts as the same time.
	later := time.Now().Add(5 * time.Minute)
	mustWrite(t, filepath.Join(bMem, "MEMORY.md"), "- [X](x.md) - x\n")
	os.Remove(filepath.Join(bMem, "y.md"))
	aIndex := mustRead(t, filepath.Join(aMem, "MEMORY.md"))
	mustWrite(t, filepath.Join(aMem, "MEMORY.md"), aIndex+"- [Z](z.md) - z\n")
	mustWrite(t, filepath.Join(aMem, "z.md"), "z\n")
	for _, p := range []string{filepath.Join(bMem, "MEMORY.md"), filepath.Join(aMem, "MEMORY.md"), filepath.Join(aMem, "z.md")} {
		os.Chtimes(p, later, later)
	}
	sync()
	sync()

	for name, dir := range map[string]string{"A": aMem, "B": bMem} {
		got := mustRead(t, filepath.Join(dir, "MEMORY.md"))
		if strings.Contains(got, "y.md") {
			t.Errorf("%s: the deleted entry came back:\n%s", name, got)
		}
		if !strings.Contains(got, "z.md") || !strings.Contains(got, "x.md") {
			t.Errorf("%s: an entry that should be there is missing:\n%s", name, got)
		}
	}
	if _, err := os.Stat(filepath.Join(bMem, "y.md")); err == nil {
		t.Error("B: the deleted memory file was copied back")
	}
}
