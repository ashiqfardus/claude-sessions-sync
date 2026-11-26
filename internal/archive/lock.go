package archive

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// StaleLockAfter is how long a lock may go without a heartbeat before it is assumed
// to belong to a process that died. A live holder refreshes it every
// lockHeartbeat, so this only has to outlast a missed beat or two.
const StaleLockAfter = 5 * time.Minute

const lockHeartbeat = time.Minute

// maxLockHold stops the heartbeat of a run that has hung - typically blocked forever
// on an unresponsive network mount. Without a cap, a hung process would hold the
// lock, and stop every future archive run, for as long as it stayed alive.
const maxLockHold = 2 * time.Hour

// Lock is a whole-machine advisory lock.
//
// The SessionEnd hook and the periodic sweep can fire at the same moment - ending a
// session as the timer comes round is entirely normal - and two pushes racing would
// interleave writes to the same manifest shard and index.
type Lock struct {
	path  string
	token string
	stop  chan struct{}
	once  sync.Once
}

// AcquireLock takes the lock, or reports that another run holds it.
//
// It is advisory and best-effort by design: the caller treats failure as "someone
// else is already doing this", not as an error worth failing a session over.
func AcquireLock(claudeRoot string) (*Lock, error) {
	path := filepath.Join(claudeRoot, "session-sync.lock")
	if err := os.MkdirAll(claudeRoot, 0o755); err != nil {
		return nil, err
	}

	l, err := tryLock(path)
	if err == nil {
		return l, nil
	}
	if !os.IsExist(err) {
		return nil, err
	}

	// Someone holds it - or a process died without cleaning up. A run killed by the
	// hook timeout left a lock that skipped every session end for the next 20
	// minutes, so a dead holder is detected by pid, not only by age.
	raw, readErr := os.ReadFile(path)
	info, statErr := os.Stat(path)
	if readErr != nil || statErr != nil {
		return nil, ErrLockHeld
	}
	age := time.Since(info.ModTime())
	pid, hasPID := parseLockPID(raw)
	if age < StaleLockAfter && (!hasPID || processAlive(pid)) {
		return nil, fmt.Errorf("%w (pid %d, %s old)", ErrLockHeld, pid, age.Round(time.Second))
	}

	// Break it only if it is still the lock that was judged stale. Another run may
	// have broken it and taken a fresh one in the meantime; removing that would let
	// two runs proceed at once.
	// ponytail: a microsecond window remains between this read and the remove.
	if again, err := os.ReadFile(path); err != nil || string(again) != string(raw) {
		return nil, fmt.Errorf("%w: taken while breaking a stale one", ErrLockHeld)
	}
	if err := os.Remove(path); err != nil {
		return nil, fmt.Errorf("stale lock could not be removed: %w", err)
	}
	l, err = tryLock(path)
	if err != nil {
		return nil, fmt.Errorf("%w: taken while breaking a stale one", ErrLockHeld)
	}
	return l, nil
}

// ErrLockHeld means another run holds the lock right now.
var ErrLockHeld = errors.New("lock held by another run")

// AcquireLockWait is AcquireLock, retrying while another run holds the lock, for up
// to wait.
//
// The hook skipped whenever the sweep, or another session's hook, was mid-push -
// routine at the end of a working day - and the session it was called for then waited
// for the next sweep, or, on a laptop about to be closed, until the next morning.
func AcquireLockWait(claudeRoot string, wait time.Duration) (*Lock, error) {
	deadline := time.Now().Add(wait)
	for {
		l, err := AcquireLock(claudeRoot)
		// Windows reports a lock file that is being deleted - the other run releasing
		// it, the very moment worth waiting for - as access denied, not as existing.
		busy := errors.Is(err, ErrLockHeld) || os.IsPermission(err)
		if err == nil || !busy || !time.Now().Before(deadline) {
			return l, err
		}
		time.Sleep(time.Second)
	}
}

func tryLock(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	l := &Lock{path: path, token: hex.EncodeToString(b[:]), stop: make(chan struct{})}
	fmt.Fprintf(f, "%d %s %s\n", os.Getpid(), l.token, time.Now().Format(time.RFC3339))
	f.Close()

	go l.heartbeat()
	return l, nil
}

// heartbeat keeps the lock fresh while the run is alive, so a long first render is
// not mistaken for a dead run and has its lock broken underneath it.
func (l *Lock) heartbeat() {
	tick := time.NewTicker(lockHeartbeat)
	defer tick.Stop()
	deadline := time.After(maxLockHold)
	for {
		select {
		case <-l.stop:
			return
		case <-deadline:
			return
		case <-tick.C:
			if !l.owned() {
				return
			}
			now := time.Now()
			_ = os.Chtimes(l.path, now, now)
		}
	}
}

func (l *Lock) owned() bool {
	raw, err := os.ReadFile(l.path)
	return err == nil && strings.Contains(string(raw), " "+l.token+" ")
}

// Release removes the lock if it is still this run's. Safe to call on a nil Lock so
// callers can defer it unconditionally.
//
// Removing whatever file sat at the path used to delete a lock another run had
// legitimately taken after breaking this one, letting a third run start alongside it.
func (l *Lock) Release() {
	if l == nil || l.path == "" {
		return
	}
	l.once.Do(func() { close(l.stop) })
	if l.owned() {
		// Windows refuses to delete a file another process has open at that instant
		// - a waiting run reading who holds the lock - and the refusal used to be
		// dropped: the lock stayed, held in name by a run that had finished, and the
		// waiter timed out. So the delete is retried for a moment.
		for i := 0; i < 20; i++ {
			if err := os.Remove(l.path); err == nil || os.IsNotExist(err) {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
}

// HolderPID reports the pid recorded in a lock file, for diagnostics.
func HolderPID(claudeRoot string) (int, bool) {
	raw, err := os.ReadFile(filepath.Join(claudeRoot, "session-sync.lock"))
	if err != nil {
		return 0, false
	}
	return parseLockPID(raw)
}

func parseLockPID(raw []byte) (int, bool) {
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0, false
	}
	pid, err := strconv.Atoi(fields[0])
	return pid, err == nil && pid > 0
}

// OrphanTempAge is how old a leftover temporary file must be before it is removed.
// Another machine may be copying into the same synced folder right now, so a fresh
// temp file is never touched.
const OrphanTempAge = time.Hour

// RemoveOrphanTemps deletes ".tmp-*" files that a killed run left in the archive,
// returning how many were removed. They are half-written copies that a sync client
// would otherwise upload and keep forever.
//
// Only the folders this tool writes are searched, and the archive root itself. An
// archive pointed at a whole drive ("G:\My Drive") would otherwise have every other
// program's .tmp- files in it deleted.
func RemoveOrphanTemps(dest string) int {
	removed := 0
	cutoff := time.Now().Add(-OrphanTempAge)
	remove := func(path string, d fs.DirEntry) {
		if !d.Type().IsRegular() || !strings.HasPrefix(d.Name(), ".tmp-") {
			return
		}
		if info, err := d.Info(); err == nil && info.ModTime().Before(cutoff) && os.Remove(path) == nil {
			removed++
		}
	}

	if entries, err := os.ReadDir(dest); err == nil {
		for _, e := range entries {
			remove(filepath.Join(dest, e.Name()), e)
		}
	}
	for _, sub := range []string{ProjectsDir(dest), ManifestDir(dest), IndexDir(dest), filepath.Join(dest, "html"),
		filepath.Join(dest, "forks"), filepath.Join(dest, "profile"), filepath.Join(dest, "memory"), filepath.Join(dest, "work"), DeletedDir(dest)} {
		_ = filepath.WalkDir(sub, func(path string, d fs.DirEntry, err error) error {
			if err == nil {
				remove(path, d)
			}
			return nil
		})
	}
	return removed
}

// IsOwnTemp reports whether name is one this tool's os.CreateTemp calls give a file
// while it is written: ".tmp-", the pattern, then "-" and the digits CreateTemp puts
// for "*". One a crash left is half a copy, never a conversation's own file.
func IsOwnTemp(name string) bool {
	rest, ok := strings.CutPrefix(name, ".tmp-")
	i := strings.LastIndexByte(rest, '-')
	if !ok || i < 0 || i == len(rest)-1 {
		return false
	}
	return strings.Trim(rest[i+1:], "0123456789") == ""
}

// RemoveOwnTemps deletes, at any depth under dir, the files IsOwnTemp names that are
// over OrphanTempAge old, returning how many. It is for ~/.claude/projects, which only
// this machine writes: run it under the lock. Links are neither followed nor removed.
func RemoveOwnTemps(dir string) int {
	removed := 0
	cutoff := time.Now().Add(-OrphanTempAge)
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() || !IsOwnTemp(d.Name()) {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().Before(cutoff) && os.Remove(path) == nil {
			removed++
		}
		return nil
	})
	return removed
}
