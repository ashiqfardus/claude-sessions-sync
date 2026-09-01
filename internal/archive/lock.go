package archive

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// StaleLockAfter is how long a lock file may sit untouched before it is assumed to
// belong to a process that died. The sweep runs every 30 minutes, so anything older
// than that cannot be a live run it needs to respect.
const StaleLockAfter = 20 * time.Minute

// Lock is a whole-machine advisory lock.
//
// The SessionEnd hook and the periodic sweep can fire at the same moment - ending a
// session as the timer comes round is entirely normal - and two pushes racing would
// interleave writes to the same manifest shard and index.
type Lock struct{ path string }

// AcquireLock takes the lock, or reports that another run holds it.
//
// It is advisory and best-effort by design: the caller treats failure as "someone
// else is already doing this", not as an error worth failing a session over.
func AcquireLock(claudeRoot string) (*Lock, error) {
	path := filepath.Join(claudeRoot, "session-sync.lock")

	if err := os.MkdirAll(claudeRoot, 0o755); err != nil {
		return nil, err
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		fmt.Fprintf(f, "%d %s\n", os.Getpid(), time.Now().Format(time.RFC3339))
		f.Close()
		return &Lock{path: path}, nil
	}
	if !os.IsExist(err) {
		return nil, err
	}

	// Someone holds it - or a process died without cleaning up. A stale lock that is
	// never broken would silently stop all archiving until a human noticed, which is
	// a worse failure than an occasional overlapping run.
	info, statErr := os.Stat(path)
	if statErr != nil {
		return nil, fmt.Errorf("lock held by another run")
	}
	age := time.Since(info.ModTime())
	if age < StaleLockAfter {
		return nil, fmt.Errorf("lock held by another run (%s old)", age.Round(time.Second))
	}

	if err := os.Remove(path); err != nil {
		return nil, fmt.Errorf("stale lock could not be removed: %w", err)
	}
	f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lock taken by another run while breaking a stale one")
	}
	fmt.Fprintf(f, "%d %s\n", os.Getpid(), time.Now().Format(time.RFC3339))
	f.Close()
	return &Lock{path: path}, nil
}

// Release removes the lock. Safe to call on a nil Lock so callers can defer it
// unconditionally.
func (l *Lock) Release() {
	if l == nil || l.path == "" {
		return
	}
	os.Remove(l.path)
}

// HolderPID reports the pid recorded in a lock file, for diagnostics.
func HolderPID(claudeRoot string) (int, bool) {
	raw, err := os.ReadFile(filepath.Join(claudeRoot, "session-sync.lock"))
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0, false
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, false
	}
	return pid, true
}
