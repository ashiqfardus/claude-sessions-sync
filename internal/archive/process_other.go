//go:build !windows

package archive

import (
	"errors"
	"os"
	"syscall"
)

// processAlive reports whether pid is a running process. Signal 0 checks for
// existence without delivering anything; EPERM means it exists under another user.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
