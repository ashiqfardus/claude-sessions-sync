package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/convzip"
)

// A settled pair whose zip another machine removes while this sweep unzips it: the
// sweep says the zip went, and zips nothing again; the loose files stay as they are.
func TestSweepSaysWhenTheZipWentWhileUnzipping(t *testing.T) {
	root, dest := t.TempDir(), t.TempDir()
	dir := filepath.Join(archive.ProjectsDir(dest), "web-app")
	line := func(u string) string {
		return `{"type":"user","uuid":"` + u + `","message":{"role":"user","content":"x"}}` + "\n"
	}
	old := time.Now().Add(-40 * 24 * time.Hour)
	tp := filepath.Join(dir, cleanID+".jsonl")
	writeFile(t, tp, line("u1"))
	os.Chtimes(tp, old, old)
	if _, err := convzip.Zip(dir, cleanID); err != nil {
		t.Fatal(err)
	}
	writeFile(t, tp, line("u1")+line("u2")) // a turn the zip lacks: unzipped to be zipped again
	os.Chtimes(tp, old, old)
	now := time.Now()
	seen := map[string]bothSeen{"web-app/" + cleanID: {State: bothState(dir, cleanID),
		Since: now.Add(-27 * time.Hour), Confirmed: now.Add(-2 * time.Hour), Last: now.Add(-2 * time.Hour)}}
	data, _ := json.Marshal(seen)
	writeFile(t, bothSeenPath(root), string(data))

	defer func(old func(string, string) error) { unzip = old }(unzip)
	unzip = func(d, id string) error {
		os.Remove(convzip.Path(d, id)) // the other machine's removal lands
		return os.ErrNotExist
	}
	var log strings.Builder
	n, _, _ := zipIdleCopies(root, dest, []string{"web-app"}, now, 0, false, func(f string, a ...any) { log.WriteString(f) })
	if n != 0 || fileExistsHere(convzip.Path(dir, cleanID)) {
		t.Errorf("the sweep zipped again while another machine was acting")
	}
	if !strings.Contains(log.String(), "the zip went while it was read") {
		t.Errorf("the log does not say the zip went: %q", log.String())
	}
	if b, _ := os.ReadFile(tp); string(b) != line("u1")+line("u2") {
		t.Errorf("the loose transcript changed: %q", b)
	}
}

func fileExistsHere(p string) bool { _, err := os.Lstat(p); return err == nil }
