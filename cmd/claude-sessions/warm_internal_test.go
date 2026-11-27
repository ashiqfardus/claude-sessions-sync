package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/work"
)

// packedArchive is LAP-1's archive with one conversation of web-app, here at search,
// and two work folders: notes, packed in n units of one file each, and old, in one
// unit, which LAP-2 excludes. An import on LAP-2 reads notes' packs and never old's.
func packedArchive(t *testing.T, n int) (claudeDir, dest, search string) {
	t.Helper()
	t.Setenv("CLAUDE_SESSIONS_MACHINE", "LAP-2")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_SESSIONS_TEMP_DIRS", filepath.Join(home, "no-temp")+string(os.PathListSeparator))
	root := t.TempDir()
	claudeDir, dest, search = filepath.Join(root, ".claude"), filepath.Join(root, "archive"), filepath.Join(root, "dev")
	proj := filepath.Join(search, "web-app")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	const id = "11111111-1111-1111-1111-111111111111"
	cwd, _ := json.Marshal(proj)
	writeFile(t, filepath.Join(archive.ProjectsDir(dest), claude.Slug(proj), id+".jsonl"),
		`{"type":"user","cwd":`+string(cwd)+`,"sessionId":"`+id+`","message":{"role":"user","content":"hi"}}`+"\n")

	const homeA = "/home/a"
	src := filepath.Join(root, "src.md")
	writeFile(t, src, "a note\n")
	fi, _ := os.Stat(src)
	pack := func(folder, unit string) {
		dir, err := workDir(dest, "LAP-1", folder)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := work.WritePack(filepath.Join(dir, unit+work.PackSuffix), []work.PackFile{{Rel: "note.md", Path: src, Size: fi.Size(), ModTime: fi.ModTime()}}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < n; i++ {
		pack(homeA+"/notes", fmt.Sprintf("u%02d", i))
	}
	pack(homeA+"/old", "u00")
	shard, _ := json.Marshal(archive.Shard{SchemaVersion: 1, Machine: "LAP-1", Home: homeA,
		Projects:    map[string]archive.Project{claude.Slug(proj): {Path: proj, Leaf: "web-app"}},
		WorkFolders: []archive.WorkFolder{{Path: homeA + "/notes"}, {Path: homeA + "/old"}}})
	writeFile(t, filepath.Join(archive.ManifestDir(dest), "LAP-1.json"), string(shard))
	if err := archive.SaveConfig(claudeDir, archive.Config{Destination: dest, WorkExclude: []string{filepath.Join(home, "old")}}); err != nil {
		t.Fatal(err)
	}
	return claudeDir, dest, search
}

// A dry run reads each pack it files from once more, first and side by side, at most
// eight at once: on a drive that downloads each file as it is opened, they download
// together, not one by one. It warms no pack it does not read - none of a folder
// excluded here - and prints, byte for byte, what it printed with no warming.
func TestImportDryRunWarmsEachPackItReadsInParallel(t *testing.T) {
	const n = 12
	claudeDir, dest, search := packedArchive(t, n)
	var mu sync.Mutex
	var warmed, read []string
	inFlight, most := 0, 0
	eight, full := make(chan struct{}), sync.Once{}
	realExtract := extractPack
	hook(t, &extractPack, func(p string, mk func() (string, error), skip func(string, int64, time.Time) bool) ([]work.PackFile, []string, error) {
		mu.Lock()
		read = append(read, p)
		mu.Unlock()
		return realExtract(p, mk, skip)
	})
	warming := func(paths []string, r func(string)) func() {
		return archive.Warm(paths, func(p string) {
			mu.Lock()
			warmed = append(warmed, p)
			if inFlight++; inFlight > most {
				most = inFlight
			}
			if inFlight == 8 {
				full.Do(func() { close(eight) })
			}
			mu.Unlock()
			select { // held until eight are read at once, so one at a time fails below
			case <-eight:
			case <-time.After(5 * time.Second):
			}
			r(p)
			mu.Lock()
			inFlight--
			mu.Unlock()
		})
	}
	run := func() string {
		var out bytes.Buffer
		if _, err := runImport(streams{&out, io.Discard}, importOpts{ClaudeDir: claudeDir, ArchiveDir: dest, SearchRoot: search, Depth: 1, DryRun: true}); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}

	hook(t, &warm, warming)
	got := run()
	if most != 8 {
		t.Errorf("%d pack(s) read at once at most, want 8", most)
	}
	if len(read) != n || strings.Contains(strings.Join(read, "\n"), work.Encode("/home/a/old")) {
		t.Fatalf("fixture: read %d pack(s), want notes' %d and none of old's:\n%s", len(read), n, strings.Join(read, "\n"))
	}
	sort.Strings(warmed)
	if strings.Join(warmed, "\n") != strings.Join(read, "\n") { // read in the order listed
		t.Errorf("warmed:\n%s\nwant each pack read, once:\n%s", strings.Join(warmed, "\n"), strings.Join(read, "\n"))
	}
	if !strings.Contains(got, fmt.Sprintf("(%d file(s) would be filed", n)) {
		t.Errorf("the dry run does not count notes' files:\n%s", got)
	}

	hook(t, &warm, func([]string, func(string)) func() { return func() {} }) // as before warming
	if before := run(); got != before {
		t.Errorf("warming changed the output:\n%s\nwithout it:\n%s", got, before)
	}
}

// Progress goes to the error stream as \r-ended text, as the panel turns it into
// progress events: work folders counted by the packs read, then conversations. None of
// it is in --json output or the log.
func TestImportPullProgressOnStderrOnly(t *testing.T) {
	claudeDir, dest, search := packedArchive(t, 3)
	hook(t, &progressEvery, 0) // every step: TestProgressIsThrottled holds them back
	var progress bytes.Buffer
	if _, err := runImport(streams{io.Discard, &progress}, importOpts{ClaudeDir: claudeDir, ArchiveDir: dest, SearchRoot: search, Depth: 1, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	want := "reading work folders: 0/3\rreading work folders: 1/3\rreading work folders: 2/3\rreading work folders: 3/3\r" +
		"reading conversations: 0/1\rreading conversations: 1/1\r"
	if progress.String() != want {
		t.Errorf("progress %q, want %q", progress.String(), want)
	}

	// pull places a work folder only at its recorded path, which /home/a is not here.
	for _, c := range []struct {
		args []string
		work string
	}{{[]string{"import", "--search-root", search, "--depth", "1"}, "reading work folders: 3/3\r"}, {[]string{"pull"}, "reading work folders\r"}} {
		stdout, stderr := captureBoth(t, func() error {
			cmd := map[string]func([]string) error{"import": cmdImport, "pull": cmdPull}[c.args[0]]
			return cmd(append(c.args[1:], "--claude-dir", claudeDir, "--archive", dest, "--dry-run", "--json"))
		})
		var rows []importRow
		if err := json.Unmarshal([]byte(stdout), &rows); err != nil || strings.Contains(stdout, "reading") {
			t.Errorf("%s --json: not JSON alone (%v):\n%s", c.args[0], err, stdout)
		}
		if !strings.Contains(stderr, "reading conversations: 1/1\r") || !strings.Contains(stderr, c.work) {
			t.Errorf("%s --json: no progress on stderr: %q", c.args[0], stderr)
		}
	}
}
