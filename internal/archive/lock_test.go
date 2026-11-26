package archive

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A pid that is certainly not running: a process that has already exited.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// A run killed by the hook timeout left a lock that blocked every session end for
// the next 20 minutes. A dead holder must not wait out the age limit.
func TestAcquireLockBreaksLockOfDeadProcess(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session-sync.lock")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%d abc 2026-09-15T08:41:26Z\n", deadPID(t))), 0o600); err != nil {
		t.Fatal(err)
	}

	l, err := AcquireLock(root)
	if err != nil {
		t.Fatalf("a fresh lock from a dead process should be broken: %v", err)
	}
	l.Release()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("Release left the lock behind")
	}
}

func TestAcquireLockRespectsLiveHolder(t *testing.T) {
	root := t.TempDir()
	first, err := AcquireLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()

	if _, err := AcquireLock(root); err == nil {
		t.Fatal("a second run took a lock held by a live process")
	}
}

// Even a live pid cannot hold the lock forever once its heartbeat has stopped.
func TestAcquireLockBreaksLockWithoutHeartbeat(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session-sync.lock")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%d abc x\n", os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-StaleLockAfter - time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	l, err := AcquireLock(root)
	if err != nil {
		t.Fatalf("a lock with no heartbeat should be broken: %v", err)
	}
	l.Release()
}

// Release used to delete whatever lock file was there - including one another run
// had taken after breaking this one - letting a third run start alongside it.
func TestReleaseLeavesAnotherRunsLock(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session-sync.lock")
	l, err := AcquireLock(root)
	if err != nil {
		t.Fatal(err)
	}
	other := []byte(fmt.Sprintf("%d someoneelse x\n", os.Getpid()))
	if err := os.WriteFile(path, other, 0o600); err != nil {
		t.Fatal(err)
	}
	l.Release()
	if got, err := os.ReadFile(path); err != nil || string(got) != string(other) {
		t.Error("Release removed a lock that belonged to another run")
	}
}

func TestProcessAlive(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Error("this process reported as not running")
	}
	if processAlive(deadPID(t)) {
		t.Error("an exited process reported as running")
	}
}

// Three half-written .tmp-* files, up to 4.4 MB, sat in a real Drive archive for a
// week after interrupted runs.
func TestRemoveOrphanTemps(t *testing.T) {
	dir := t.TempDir()
	bucket := filepath.Join(dir, "projects", "e--x")
	if err := os.MkdirAll(bucket, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(bucket, ".tmp-a.jsonl-123")
	fresh := filepath.Join(bucket, ".tmp-b.jsonl-456") // another machine, mid-copy
	keep := filepath.Join(bucket, "a.jsonl")
	for _, p := range []string{old, fresh, keep} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stale := time.Now().Add(-OrphanTempAge - time.Minute)
	for _, p := range []string{old, keep} {
		if err := os.Chtimes(p, stale, stale); err != nil {
			t.Fatal(err)
		}
	}

	if n := RemoveOrphanTemps(dir); n != 1 {
		t.Errorf("removed %d, want 1", n)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("the old temp file survived")
	}
	for _, p := range []string{fresh, keep} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed", filepath.Base(p))
		}
	}
}

func TestIsOwnTemp(t *testing.T) {
	for name, want := range map[string]bool{
		".tmp-agent-a7470fd8c6d0c2554.jsonl-4159126922": true, // a power cut during import
		".tmp-repoint-123":  true,
		".tmp-update-9":     true,
		".tmp-a.jsonl":      false, // no CreateTemp digits
		".tmp-upload":       false,
		".tmp-a.jsonl-12x":  false,
		".tmp-a.jsonl-":     false,
		"tmp-a.jsonl-123":   false,
		"agent-a1.jsonl":    false,
		".TMP-a.jsonl-123":  false,
		"x.tmp-a.jsonl-123": false,
	} {
		if got := IsOwnTemp(name); got != want {
			t.Errorf("IsOwnTemp(%q) = %v, want %v", name, got, want)
		}
	}
}

// A power cut during import left a half-written subagent in a session's folder, and
// push then took the session for one no other copy held.
func TestRemoveOwnTemps(t *testing.T) {
	projects := filepath.Join(t.TempDir(), "projects")
	sub := filepath.Join(projects, "C--Users-a", "11111111-1111-1111-1111-111111111111", "subagents")
	old := filepath.Join(sub, ".tmp-agent-x.jsonl-123")
	young := filepath.Join(sub, ".tmp-agent-y.jsonl-456")
	foreign := filepath.Join(sub, ".tmp-upload") // not a name this tool makes
	real := filepath.Join(sub, "agent-x.jsonl")
	outside := filepath.Join(t.TempDir(), "elsewhere")
	beyond := filepath.Join(outside, ".tmp-b.jsonl-1") // only reached through a link
	stale := time.Now().Add(-OrphanTempAge - time.Minute)
	for _, p := range []string{old, young, foreign, real, beyond} {
		mustWriteFile(t, p, "half")
		if p != young {
			if err := os.Chtimes(p, stale, stale); err != nil {
				t.Fatal(err)
			}
		}
	}
	linked := []string{beyond}
	if err := os.Symlink(outside, filepath.Join(projects, "linked")); err == nil {
		if err := os.Symlink(beyond, filepath.Join(sub, ".tmp-c.jsonl-2")); err == nil {
			linked = append(linked, filepath.Join(sub, ".tmp-c.jsonl-2"))
		}
	} else {
		t.Logf("no symlinks here: %v", err)
	}

	if n := RemoveOwnTemps(projects); n != 1 {
		t.Errorf("removed %d, want 1", n)
	}
	if _, err := os.Lstat(old); !os.IsNotExist(err) {
		t.Error("the old temp file survived")
	}
	for _, p := range append([]string{young, foreign, real}, linked...) {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s was removed", p)
		}
	}
	if n := RemoveOwnTemps(filepath.Join(t.TempDir(), "missing")); n != 0 {
		t.Errorf("removed %d from a missing folder", n)
	}
}
