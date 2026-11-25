package main_test

// doctor and the pages see a zipped conversation as they see a loose one.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ashiqfardus/claude-sessions-sync/internal/claude"
	"github.com/ashiqfardus/claude-sessions-sync/internal/convzip"
)

// ended stamps files an hour back, as a session that ended then: doctor checks it.
func ended(t *testing.T, paths ...string) {
	t.Helper()
	ago := time.Now().Add(-time.Hour)
	for _, p := range paths {
		if err := os.Chtimes(p, ago, ago); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDoctorCountsZippedAsArchived(t *testing.T) {
	archive, mac, repo := pushedSession(t)
	ended(t, mac.session(repo), filepath.Join(archive, "projects", claude.Slug(repo), forkSession+".jsonl"))
	mac.run(t, "render")
	zipArchived(t, archive, repo)
	if out := mac.run(t, "render"); !strings.Contains(out, "Rendered 0, 1 already up to date") {
		t.Errorf("render does not keep the zipped conversation's page:\n%s", out)
	}

	out := mac.doctor(t)
	if !strings.Contains(out, "+ archived") {
		t.Errorf("doctor does not count the zipped copy as archived:\n%s", out)
	}
	if !strings.Contains(out, "1 page(s) for 1 transcript(s)") {
		t.Errorf("doctor does not count the zipped conversation's page:\n%s", out)
	}
}

// Once push has left the pair zipped, doctor reads nothing of the zip: one garbled at
// the same size and time goes unnoticed.
func TestDoctorReadsNoZipPushLeftZipped(t *testing.T) {
	archive, mac, repo := pushedSession(t)
	ended(t, mac.session(repo), filepath.Join(archive, "projects", claude.Slug(repo), forkSession+".jsonl"))
	bucket := zipArchived(t, archive, repo)
	mac.run(t, "push", "--no-html")
	garble(t, convzip.Path(bucket, forkSession))

	if out := mac.doctor(t); !strings.Contains(out, "+ archived") {
		t.Errorf("doctor read the zip push had left zipped:\n%s", out)
	}
}

// The zipped copy went on elsewhere: this machine's older, shorter copy is archived,
// as it is beside a loose copy that is ahead of it.
func TestDoctorCountsAZipAheadAsArchived(t *testing.T) {
	archive, mac, repo := pushedSession(t)
	ahead := filepath.Join(archive, "projects", claude.Slug(repo), forkSession+".jsonl")
	mustWrite(t, ahead, sessionLines(repo, "u1", "m1", "x1"))
	ended(t, mac.session(repo))
	zipArchived(t, archive, repo)

	if out := mac.doctor(t); !strings.Contains(out, "+ archived") {
		t.Errorf("doctor fails a session whose zipped copy is ahead of it:\n%s", out)
	}
}
