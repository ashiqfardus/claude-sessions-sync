package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/archive"
	"github.com/ashiqfardus/claude-sessions-sync/internal/convzip"
)

// idleCopy archives one conversation in dest's web-app bucket, idle for 40 days.
func idleCopy(t *testing.T, dest string, now time.Time, body string) (dir, tp string) {
	t.Helper()
	dir = filepath.Join(archive.ProjectsDir(dest), "web-app")
	tp = filepath.Join(dir, cleanID+".jsonl")
	writeFile(t, tp, body)
	old := now.Add(-40 * 24 * time.Hour)
	os.Chtimes(tp, old, old)
	return dir, tp
}

// A failure zip-skipped.json remembers may pass later, as a remove a sync client
// blocked for a moment: it is tried again once its retry time comes, even when the
// copy is unchanged. A refusal that cannot change while the copy does not waits for a
// change.
func TestZipSkippedIsRetriedOnlyWhenItMayPass(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		name, record string
		zips         bool
	}{
		{"a 0.7.0 bare size, its kind unknown", `%d`, true},
		{"a failure not yet due", `{"size":%d,"retry":"` + now.Add(24*time.Hour).Format(time.RFC3339) + `"}`, false},
		{"a failure due again", `{"size":%d,"retry":"` + now.Add(-time.Hour).Format(time.RFC3339) + `"}`, true},
		{"a refusal kept until the copy changes", `{"size":%d}`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, dest := t.TempDir(), t.TempDir()
			dir, _ := idleCopy(t, dest, now, `{"type":"user","uuid":"u1","message":{"role":"user","content":"x"}}`+"\n")
			rec := strings.Replace(c.record, "%d", strconv.FormatInt(copyBytes(dir, cleanID), 10), 1)
			writeFile(t, zipSkippedPath(root), `{"web-app/`+cleanID+`":`+rec+"}\n")

			n, _, _ := zipIdleCopies(root, dest, []string{"web-app"}, now, 0, false, func(string, ...any) {})
			if got := fileExistsHere(convzip.Path(dir, cleanID)); got != c.zips || (n == 1) != c.zips {
				t.Errorf("zipped %v (%d), want %v", got, n, c.zips)
			}
		})
	}
}

// A failed unzip is remembered with a time to try again, zipRetry on.
func TestZipSkippedRecordsWhenToRetry(t *testing.T) {
	root, dest := t.TempDir(), t.TempDir()
	now := time.Now()
	line := func(u string) string {
		return `{"type":"user","uuid":"` + u + `","message":{"role":"user","content":"x"}}` + "\n"
	}
	dir, tp := idleCopy(t, dest, now, line("u1"))
	if _, err := convzip.Zip(dir, cleanID); err != nil {
		t.Fatal(err)
	}
	idleCopy(t, dest, now, line("u1")+line("u2")) // a turn the zip lacks: unzipped to be zipped again
	key := "web-app/" + cleanID
	seen := map[string]bothSeen{key: {State: bothState(dir, cleanID),
		Since: now.Add(-27 * time.Hour), Confirmed: now.Add(-2 * time.Hour), Last: now.Add(-2 * time.Hour)}}
	data, _ := json.Marshal(seen)
	writeFile(t, bothSeenPath(root), string(data))
	defer func(old func(string, string) error) { unzip = old }(unzip)
	unzip = func(string, string) error { return errors.New("the file is held by another program") }

	zipIdleCopies(root, dest, []string{"web-app"}, now, 0, false, func(string, ...any) {})
	var got map[string]zipSkip
	if err := json.Unmarshal([]byte(mustReadFile(t, zipSkippedPath(root))), &got); err != nil {
		t.Fatal(err)
	}
	if r := got[key].Retry; !r.Equal(now.Add(zipRetry)) {
		t.Errorf("retry at %v, want %v", r, now.Add(zipRetry))
	}
	if b, _ := os.ReadFile(tp); string(b) != line("u1")+line("u2") {
		t.Errorf("the loose transcript changed: %q", b)
	}
}
