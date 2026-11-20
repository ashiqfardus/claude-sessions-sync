package main_test

// Two machines on the same layout push one session to the same archived file. When
// both continued it - the 5:00 / 5:29 case - dates and sizes cannot say which copy
// matters, so the archived copy is kept and this machine's goes beside it as a fork.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const forkBucket = "e--work-api"
const forkSession = "66666666-6666-6666-6666-666666666666"

func uuidLines(uuids ...string) string {
	var b strings.Builder
	for _, u := range uuids {
		b.WriteString(`{"type":"user","uuid":"` + u + `","cwd":"E:\\work\\api","sessionId":"` + forkSession +
			`","timestamp":"2026-11-20T05:00:00.000Z","message":{"role":"user","content":"turn ` + u + `"}}` + "\n")
	}
	return b.String()
}

func (f fixture) seedSession(t *testing.T, local, archived string, localNewer bool) (string, string) {
	t.Helper()
	l := filepath.Join(f.claudeDir, "projects", forkBucket, forkSession+".jsonl")
	a := filepath.Join(f.archive, "projects", forkBucket, forkSession+".jsonl")
	mustWrite(t, l, local)
	mustWrite(t, a, archived)
	older, newer := time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)
	lt, at := older, newer
	if localNewer {
		lt, at = newer, older
	}
	os.Chtimes(l, lt, lt)
	os.Chtimes(a, at, at)
	return l, a
}

func TestPushKeepsBothCopiesOfAForkedSession(t *testing.T) {
	f := newFixture(t)
	_, a := f.seedSession(t, uuidLines("u1", "u2", "a1"), uuidLines("u1", "u2", "b1", "b2"), true)

	out, err := f.push(t, "--no-html")
	if err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}
	if got := mustRead(t, a); !strings.Contains(got, `"b2"`) {
		t.Errorf("the other machine's turns were overwritten in the archive:\n%s", got)
	}
	forks, _ := filepath.Glob(filepath.Join(f.archive, "forks", forkBucket, forkSession, "*.jsonl"))
	if len(forks) != 1 || !strings.Contains(mustRead(t, forks[0]), `"a1"`) {
		t.Fatalf("this machine's turns are not kept as a fork: %v\n%s", forks, out)
	}
}

// The older copy here is the one that forked: it was never archived before.
func TestPushArchivesAnOlderForkedCopyToo(t *testing.T) {
	f := newFixture(t)
	f.seedSession(t, uuidLines("u1", "u2", "a1"), uuidLines("u1", "u2", "b1", "b2"), false)

	if out, err := f.push(t, "--no-html"); err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}
	forks, _ := filepath.Glob(filepath.Join(f.archive, "forks", forkBucket, forkSession, "*.jsonl"))
	if len(forks) != 1 {
		t.Fatalf("an older forked copy was not kept: %v", forks)
	}
}

// A fork is archived: doctor must not keep reporting it as missing.
func TestDoctorCountsAForkAsArchived(t *testing.T) {
	f := newFixture(t)
	f.seedSession(t, uuidLines("u1", "u2", "a1", "a2", "a3"), uuidLines("u1", "u2", "b1"), true)

	if out, err := f.push(t, "--no-html"); err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}
	out, _ := f.run(t, "doctor", "--no-write-probe")
	if strings.Contains(out, forkBucket+"/"+forkSession) {
		t.Errorf("doctor reports an archived fork as missing:\n%s", out)
	}
}

func TestPushStillReplacesWhatItContinues(t *testing.T) {
	f := newFixture(t)
	_, a := f.seedSession(t, uuidLines("u1", "u2", "u3"), uuidLines("u1", "u2"), true)

	if out, err := f.push(t, "--no-html"); err != nil {
		t.Fatalf("push failed: %v\n%s", err, out)
	}
	if got := mustRead(t, a); !strings.Contains(got, `"u3"`) {
		t.Errorf("a plain continuation was not archived:\n%s", got)
	}
	if forks, _ := filepath.Glob(filepath.Join(f.archive, "forks", "*")); len(forks) > 0 {
		t.Errorf("a continuation was treated as a fork: %v", forks)
	}
}
